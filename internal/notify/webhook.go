package notify

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Webhook posts an already encoded JSON payload to a trading webhook. The
// endpoint is passed by the dispatcher as its token argument so the existing
// queue, retry and cooldown machinery can be reused without storing secrets.
type Webhook struct {
	Client *http.Client
}

// NewWebhook builds a sender with a bounded request timeout.
func NewWebhook() *Webhook {
	return &Webhook{Client: &http.Client{Timeout: 20 * time.Second}}
}

// Send posts JSON and treats every 2xx response as success. Response bodies are
// deliberately not included in errors because a webhook could echo credentials.
func (w *Webhook) Send(ctx context.Context, endpoint, _ string, payload string) error {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return fmt.Errorf("webhook endpoint is not configured")
	}
	if !strings.HasPrefix(endpoint, "https://") && !strings.HasPrefix(endpoint, "http://") {
		return fmt.Errorf("webhook endpoint must use http or https")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	client := w.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("send webhook: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("webhook returned HTTP %d", resp.StatusCode)
	}
	return nil
}
