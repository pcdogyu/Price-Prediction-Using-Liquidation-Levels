package notify

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
