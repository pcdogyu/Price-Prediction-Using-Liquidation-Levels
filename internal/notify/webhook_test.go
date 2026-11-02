package notify

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestWebhookSendPostsJSONAndRejectsNon2xx(t *testing.T) {
	var gotBody, gotContentType string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody, gotContentType = string(body), r.Header.Get("Content-Type")
		if r.URL.Path == "/fail" {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, `{"token":"secret"}`)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	sender := NewWebhook()
	if err := sender.Send(context.Background(), server.URL+"/ok", "ignored", `{"action":"buy"}`); err != nil {
		t.Fatalf("successful webhook returned error: %v", err)
	}
	if gotBody != `{"action":"buy"}` || gotContentType != "application/json" {
		t.Fatalf("unexpected request body or content type: %q %q", gotBody, gotContentType)
	}
	err := sender.Send(context.Background(), server.URL+"/fail", "ignored", `{"token":"secret"}`)
	if err == nil || !strings.Contains(err.Error(), "HTTP 502") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("unexpected safe error: %v", err)
	}
	if err := sender.Send(context.Background(), "ftp://example.test", "ignored", "{}"); err == nil {
		t.Fatal("non-http webhook endpoint must be rejected")
	}
}

func TestWebhookDispatcherQueuesWithEndpointOnly(t *testing.T) {
	type request struct {
		method      string
		contentType string
		body        string
	}
	requests := make(chan request, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests <- request{method: r.Method, contentType: r.Header.Get("Content-Type"), body: string(body)}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	dispatcher := NewWebhookDispatcher(NewWebhook(), func() (string, string, bool) {
		return server.URL, "", true
	}, 4, time.Minute, slog.New(slog.NewTextHandler(io.Discard, nil)))
	dispatcher.minSpacing = 0
	dispatcher.attempts = 1
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go dispatcher.Start(ctx)

	if !dispatcher.Submit(Task{Key: "steady-webhook|BTCUSDT", Text: `{"action":"buy"}`}) {
		t.Fatal("a configured endpoint-only webhook must be queued")
	}
	select {
	case got := <-requests:
		if got.method != http.MethodPost || got.contentType != "application/json" || got.body != `{"action":"buy"}` {
			t.Fatalf("unexpected webhook request: %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("webhook task was not delivered")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		status, depth := dispatcher.Snapshot()
		if status.Sent == 1 && depth == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("unexpected dispatcher status: %+v depth=%d", status, depth)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type concurrentWebhookSender struct {
	active int32
	max    int32
	mu     sync.Mutex
	events []string
}

func (s *concurrentWebhookSender) Send(ctx context.Context, _, _, text string) error {
	active := atomic.AddInt32(&s.active, 1)
	for {
		old := atomic.LoadInt32(&s.max)
		if active <= old || atomic.CompareAndSwapInt32(&s.max, old, active) {
			break
		}
	}
	select {
	case <-ctx.Done():
	case <-time.After(25 * time.Millisecond):
	}
	atomic.AddInt32(&s.active, -1)
	s.mu.Lock()
	s.events = append(s.events, text)
	s.mu.Unlock()
	return nil
}

func (s *concurrentWebhookSender) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.events...)
}

func TestWebhookDispatcherUsesEightWorkers(t *testing.T) {
	sender := &concurrentWebhookSender{}
	dispatcher := NewWebhookDispatcher(sender, openSettings(), 16, time.Minute, slog.New(slog.NewTextHandler(io.Discard, nil)))
	dispatcher.attempts = 1
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go dispatcher.Start(ctx)
	for i := 0; i < 8; i++ {
		if !dispatcher.Submit(Task{Key: "webhook-" + string(rune('a'+i)), SerialKey: "symbol-" + string(rune('a'+i)), Text: "task"}) {
			t.Fatalf("task %d was not queued", i)
		}
	}
	if !waitFor(func() bool { status, _ := dispatcher.Snapshot(); return status.Sent == 8 }) {
		status, depth := dispatcher.Snapshot()
		t.Fatalf("webhook tasks were not delivered: %+v depth=%d", status, depth)
	}
	if got := atomic.LoadInt32(&sender.max); got < 2 {
		t.Fatalf("webhook dispatcher did not run concurrently, max active=%d", got)
	}
	status, depth := dispatcher.Snapshot()
	if depth != 0 || status.QueueDepth != 0 || status.LastSendLatency <= 0 {
		t.Fatalf("queue metrics were not recorded: %+v depth=%d", status, depth)
	}
}

func TestWebhookDispatcherSerializesCoinpairAndPrioritizesClose(t *testing.T) {
	sender := &concurrentWebhookSender{}
	dispatcher := NewWebhookDispatcher(sender, openSettings(), 4, time.Minute, slog.New(slog.NewTextHandler(io.Discard, nil)))
	dispatcher.attempts = 1
	// Queue the open first to prove the separate priority lane still selects the
	// close request before it, while the serial key prevents overlap.
	if !dispatcher.Submit(Task{Key: "open", SerialKey: "BTCUSDT", Text: "open"}) || !dispatcher.Submit(Task{Key: "close", SerialKey: "BTCUSDT", Priority: PriorityClose, Text: "close"}) {
		t.Fatal("both same-symbol tasks must be queued")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go dispatcher.Start(ctx)
	if !waitFor(func() bool { status, _ := dispatcher.Snapshot(); return status.Sent == 2 }) {
		status, depth := dispatcher.Snapshot()
		t.Fatalf("same-symbol tasks were not delivered: %+v depth=%d", status, depth)
	}
	events := sender.snapshot()
	if len(events) != 2 || events[0] != "close" || events[1] != "open" {
		t.Fatalf("same-symbol order = %v, want [close open]", events)
	}
	if got := atomic.LoadInt32(&sender.max); got != 1 {
		t.Fatalf("same-symbol tasks overlapped, max active=%d", got)
	}
}

func TestWebhookDispatcherExpiresStaleTaskAndReportsQueueAge(t *testing.T) {
	sender := &concurrentWebhookSender{}
	dispatcher := NewWebhookDispatcher(sender, openSettings(), 4, time.Minute, slog.New(slog.NewTextHandler(io.Discard, nil)))
	dispatcher.attempts = 1
	stale := Task{Key: "stale", Text: "stale", enqueuedAt: time.Now().Add(-31 * time.Second)}
	if !dispatcher.Submit(stale) {
		t.Fatal("stale task must still enter the bounded queue")
	}
	status, depth := dispatcher.Snapshot()
	if depth != 1 || status.OldestTaskAge < 30*time.Second {
		t.Fatalf("queue age was not recorded: %+v depth=%d", status, depth)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go dispatcher.Start(ctx)
	if !waitFor(func() bool { status, _ := dispatcher.Snapshot(); return status.Expired == 1 }) {
		status, depth := dispatcher.Snapshot()
		t.Fatalf("stale task was not expired: %+v depth=%d", status, depth)
	}
	if len(sender.snapshot()) != 0 {
		t.Fatal("an expired task must not reach the webhook sender")
	}
	status, depth = dispatcher.Snapshot()
	if depth != 0 || status.Suppressed != 1 || status.LastQueueWait < 30*time.Second {
		t.Fatalf("unexpected expired-task metrics: %+v depth=%d", status, depth)
	}
}
