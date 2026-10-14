package notify

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestTokenAndChatIDValidation(t *testing.T) {
	for _, token := range []string{"12345:ABCDEFGHIJKLMNOPQRSTUVWXYZ", " 9912:secret "} {
		if !ValidToken(token) {
			t.Fatalf("valid token rejected: %q", token)
		}
	}
	for _, token := range []string{"", " ", "abc:def", "1234", "1234:", ":secret"} {
		if ValidToken(token) {
			t.Fatalf("invalid token accepted: %q", token)
		}
	}
	for _, chatID := range []string{"12345", "-1001234567890", "+1", "@alerts_channel"} {
		if !ValidChatID(chatID) {
			t.Fatalf("valid chat id rejected: %q", chatID)
		}
	}
	for _, chatID := range []string{"", "  ", "@", "-", "12 34", "abc", strings.Repeat("9", 65)} {
		if ValidChatID(chatID) {
			t.Fatalf("invalid chat id accepted: %q", chatID)
		}
	}
	if MaskToken("12345:ABCDEFGHIJKLMNOPQRSTUVWXYZ") != "12345:••••" {
		t.Fatalf("the mask must keep only the bot id: %q", MaskToken("12345:ABCDEFGHIJKLMNOPQRSTUVWXYZ"))
	}
	if MaskToken("garbage") != "••••" || MaskToken("  ") != "" {
		t.Fatalf("unexpected mask output: %q %q", MaskToken("garbage"), MaskToken("  "))
	}
}

func TestTelegramSend(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests = append(requests, r.URL.Path+"|"+string(body))
		switch {
		case strings.Contains(r.URL.Path, "400"):
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"ok":false,"error_code":400,"description":"chat not found"}`)
		case strings.Contains(r.URL.Path, "500"):
			w.WriteHeader(http.StatusInternalServerError)
		default:
			_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":1}}`)
		}
	}))
	defer server.Close()
	sender := NewTelegram(server.URL)
	if err := sender.Send(context.Background(), "12345:secret", "-1009", "hello"); err != nil {
		t.Fatalf("a successful send must not error: %v", err)
	}
	if len(requests) != 1 || !strings.HasPrefix(requests[0], "/bot12345:secret/sendMessage") {
		t.Fatalf("unexpected request: %v", requests)
	}
	if !strings.Contains(requests[0], "text=hello") || !strings.Contains(requests[0], "chat_id=-1009") {
		t.Fatalf("the form body must carry the message: %v", requests)
	}
	err := sender.Send(context.Background(), "400:secret", "1", "hello")
	if err == nil || !strings.Contains(err.Error(), "chat not found") {
		t.Fatalf("the Telegram description must reach the caller: %v", err)
	}
	if strings.Contains(err.Error(), "400:secret") {
		t.Fatalf("the token must never appear in an error: %v", err)
	}
	if err = sender.Send(context.Background(), "500:secret", "1", "hello"); err == nil {
		t.Fatal("an empty error body must still report the status")
	}
	if err = sender.Send(context.Background(), "", "1", "hello"); err == nil {
		t.Fatal("a missing token must be refused before the network")
	}
	if err = sender.Send(context.Background(), "bad token", "1", "hello"); err == nil {
		t.Fatal("a malformed token must be refused before the network")
	}
	if err = sender.Send(context.Background(), "1:x", "", "hello"); err == nil {
		t.Fatal("a missing chat id must be refused before the network")
	}
	if fallback := NewTelegram("  "); fallback.Base != DefaultAPIBase {
		t.Fatalf("an empty base must fall back to the public endpoint: %q", fallback.Base)
	}
	if trimmed := NewTelegram(server.URL + "/"); trimmed.Base != server.URL {
		t.Fatalf("the trailing slash must be trimmed: %q", trimmed.Base)
	}
	// A cancelled context must abort before the request is made.
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err = NewTelegram(server.URL).Send(cancelled, "1:secret", "1", "hello"); err == nil {
		t.Fatal("a cancelled context must abort the send")
	}
}

// stubSender records calls and can fail on demand, which is what the retry path needs.
type stubSender struct {
	err   error
	calls int32
}

func (s *stubSender) Send(ctx context.Context, token, chatID, text string) error {
	atomic.AddInt32(&s.calls, 1)
	return s.err
}

func (s *stubSender) count() int { return int(atomic.LoadInt32(&s.calls)) }

func newTestDispatcher(sender Sender, settings Settings, cooldown time.Duration) *Dispatcher {
	d := NewDispatcher(sender, settings, 4, cooldown, slog.New(slog.NewTextHandler(io.Discard, nil)))
	d.minSpacing = 0
	d.retryWait = time.Millisecond
	return d
}

func openSettings() Settings {
	return func() (string, string, bool) { return "1:secret", "42", true }
}

func closedSettings() Settings {
	return func() (string, string, bool) { return "", "", false }
}

func waitFor(condition func() bool) bool {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return false
}

// TestDispatcherDeliversAndCoalesces covers the worker loop plus the per key cooldown
// that keeps a burst of alerts from flooding the chat.
func TestDispatcherDeliversAndCoalesces(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sender := &stubSender{}
	dispatcher := newTestDispatcher(sender, openSettings(), time.Minute)
	stopped := make(chan struct{})
	go func() { dispatcher.Start(ctx); close(stopped) }()
	if !dispatcher.Submit(Task{Key: "aggressive|BTCUSDT", Text: "first"}) {
		t.Fatal("the first message must be queued")
	}
	if !waitFor(func() bool { return sender.count() == 1 }) {
		t.Fatalf("the message was never delivered: %d calls", sender.count())
	}
	status, depth := dispatcher.Snapshot()
	if status.Sent != 1 || depth != 0 || status.Failed != 0 || status.LastSuccessAt.IsZero() || status.LastAttemptAt.IsZero() {
		t.Fatalf("unexpected status after delivery: %+v depth %d", status, depth)
	}
	if status.LastError != "" {
		t.Fatalf("a successful send must clear the error: %+v", status)
	}
	// A second message with the same key falls inside the cooldown and is suppressed
	// before it can reach the queue.
	if dispatcher.Submit(Task{Key: "aggressive|BTCUSDT", Text: "repeat"}) {
		t.Fatal("the cooldown must suppress a duplicate key")
	}
	if !dispatcher.Submit(Task{Key: "steady|BTCUSDT", Text: "other key"}) {
		t.Fatal("a different key must not be suppressed")
	}
	if !waitFor(func() bool { return sender.count() == 2 }) {
		t.Fatalf("the second key must still be delivered: %d calls", sender.count())
	}
	if status, _ := dispatcher.Snapshot(); status.Suppressed != 1 || status.Sent != 2 {
		t.Fatalf("unexpected counters: %+v", status)
	}
	cancel()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("the worker did not stop with its context")
	}
}

// TestDispatcherReportsFailuresAfterRetries keeps a broken Telegram from blocking the
// pipeline while still telling the page what went wrong.
func TestDispatcherReportsFailuresAfterRetries(t *testing.T) {
	sender := &stubSender{err: errors.New("telegram unavailable")}
	dispatcher := newTestDispatcher(sender, openSettings(), time.Minute)
	dispatcher.attempts = 3
	if !dispatcher.Submit(Task{Key: "alert|ETHUSDT|long", Text: "boom"}) {
		t.Fatal("the message must be queued")
	}
	dispatcher.deliver(context.Background(), Task{Key: "alert|ETHUSDT|long", Text: "boom"})
	status, depth := dispatcher.Snapshot()
	if depth != 0 {
		t.Fatalf("the queue depth must return to zero: %d", depth)
	}
	if status.Failed != 1 || status.Sent != 0 {
		t.Fatalf("a failed delivery must be counted once: %+v", status)
	}
	if !strings.Contains(status.LastError, "telegram unavailable") {
		t.Fatalf("the failure reason must be reported: %+v", status)
	}
	if sender.count() != 3 {
		t.Fatalf("three attempts were expected, got %d", sender.count())
	}
}

// TestDispatcherDropsWithoutBlocking proves Submit never blocks the liquidation
// pipeline, which is the whole reason the queue exists.
func TestDispatcherDropsWithoutBlocking(t *testing.T) {
	sender := &stubSender{}
	unconfigured := newTestDispatcher(sender, closedSettings(), time.Minute)
	if unconfigured.Submit(Task{Key: "a", Text: "x"}) {
		t.Fatal("an unconfigured worker must drop the message")
	}
	if status, _ := unconfigured.Snapshot(); status.Suppressed != 1 {
		t.Fatalf("the drop must be counted: %+v", status)
	}
	full := newTestDispatcher(sender, openSettings(), time.Minute)
	for i := 0; i < cap(full.queue); i++ {
		if !full.Submit(Task{Key: "key" + string(rune('a'+i)), Text: "x"}) {
			t.Fatalf("message %d should still fit", i)
		}
	}
	if full.Submit(Task{Key: "overflow", Text: "x"}) {
		t.Fatal("a full queue must drop instead of blocking")
	}
	if status, depth := full.Snapshot(); depth != cap(full.queue) || status.Suppressed != 1 {
		t.Fatalf("unexpected queue state: depth %d status %+v", depth, status)
	}
	// Settings revoked between Submit and delivery must skip the send entirely.
	deliverer := newTestDispatcher(sender, closedSettings(), time.Minute)
	deliverer.deliver(context.Background(), Task{Key: "k", Text: "x"})
	if sender.count() != 0 {
		t.Fatalf("revoked settings must skip the send: %d calls", sender.count())
	}
}
