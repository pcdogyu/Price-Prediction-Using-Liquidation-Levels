package notify

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
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
