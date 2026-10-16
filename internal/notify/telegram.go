// Package notify delivers alert and signal messages to Telegram and buffers the
// work so a slow or unreachable Telegram never blocks liquidation ingestion.
package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// DefaultAPIBase is the standard Telegram bot endpoint.
const DefaultAPIBase = "https://api.telegram.org"

// Sender delivers one text message. Implementations must respect ctx.
type Sender interface {
	Send(ctx context.Context, token, chatID, text string) error
}

// Telegram posts sendMessage with the standard library only.
type Telegram struct {
	Base   string
	Client *http.Client
}

// NewTelegram builds a sender with a sane timeout. An empty base falls back to the
// public endpoint, which also lets tests point it at an httptest server.
func NewTelegram(base string) *Telegram {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		base = DefaultAPIBase
	}
	return &Telegram{Base: base, Client: &http.Client{Timeout: 15 * time.Second}}
}

// ValidToken accepts the documented <digits>:<secret> bot token shape. The bot id
// part must be present and numeric, so ":secret" is not a token.
func ValidToken(token string) bool {
	token = strings.TrimSpace(token)
	split := strings.SplitN(token, ":", 2)
	if len(split) != 2 || split[0] == "" || split[1] == "" {
		return false
	}
	for _, r := range split[0] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// ValidChatID accepts a numeric user id or a channel/group id, including the
// @channel-name form.
func ValidChatID(chatID string) bool {
	chatID = strings.TrimSpace(chatID)
	if chatID == "" || len(chatID) > 64 {
		return false
	}
	if strings.HasPrefix(chatID, "@") {
		return len(chatID) > 1 && !strings.ContainsAny(chatID, " \t\n")
	}
	for i, r := range chatID {
		if (r == '-' || r == '+') && i == 0 {
			continue
		}
		if r < '0' || r > '9' {
			return false
		}
	}
	return chatID != "-" && chatID != "+"
}

// MaskToken keeps only the first and last four characters and hides the middle
// of the token. The full token is never returned by an API or written to a log.
func MaskToken(token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return ""
	}
	if len(token) <= 8 {
		return "••••"
	}
	return token[:4] + "****" + token[len(token)-4:]
}

// Settings supplies the current credentials at send time, so a settings change
// takes effect without restarting the worker. Reporting false means the sender is
// not usable right now and the task should be dropped. Telegram uses both return
// values; webhook senders use token as the endpoint and leave chatID empty.
type Settings func() (token, chatID string, ok bool)

// Task is one queued message. Key groups duplicates, for example all aggressive
// signals of one symbol, so a burst cannot flood the chat.
type Task struct {
	Key  string
	Text string
}

// Status summarizes the worker for the alert page.
type Status struct {
	Sent          int64
	Suppressed    int64
	Failed        int64
	LastAttemptAt time.Time
	LastSuccessAt time.Time
	LastError     string
}

// Dispatcher is a single worker goroutine with a bounded queue, per key cooldown,
// retries with backoff, and a status snapshot for the UI.
type Dispatcher struct {
	sender   Sender
	settings Settings
	log      *slog.Logger

	cooldown   time.Duration
	minSpacing time.Duration
	attempts   int
	retryWait  time.Duration

	queue chan Task

	mu       sync.Mutex
	status   Status
	lastSend map[string]time.Time
	depth    int

	// Telegram requires both credentials, while a webhook only needs its
	// endpoint. Keep the legacy constructor strict and expose a separate
	// constructor for senders whose second credential is intentionally empty.
	requireChatID bool
}

// NewDispatcher builds a worker. A capacity of zero uses 64, and zero cooldown or
// spacing falls back to five minutes and 1.5 seconds.
func NewDispatcher(sender Sender, settings Settings, capacity int, cooldown time.Duration, log *slog.Logger) *Dispatcher {
	return newDispatcher(sender, settings, capacity, cooldown, log, true)
}

// NewWebhookDispatcher builds a worker for a webhook sender. The endpoint is
// supplied as the first credential and no chat ID is required.
func NewWebhookDispatcher(sender Sender, settings Settings, capacity int, cooldown time.Duration, log *slog.Logger) *Dispatcher {
	return newDispatcher(sender, settings, capacity, cooldown, log, false)
}

func newDispatcher(sender Sender, settings Settings, capacity int, cooldown time.Duration, log *slog.Logger, requireChatID bool) *Dispatcher {
	if capacity <= 0 {
		capacity = 64
	}
	if cooldown <= 0 {
		cooldown = 5 * time.Minute
	}
	if log == nil {
		log = slog.Default()
	}
	return &Dispatcher{sender: sender, settings: settings, log: log, cooldown: cooldown, minSpacing: 1500 * time.Millisecond, attempts: 3, retryWait: time.Second, queue: make(chan Task, capacity), lastSend: map[string]time.Time{}, requireChatID: requireChatID}
}

// Start runs the worker until ctx is cancelled.
func (d *Dispatcher) Start(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case task := <-d.queue:
			d.deliver(ctx, task)
		}
	}
}

// Submit enqueues a message. It reports false when the sender is unconfigured, when
// the per key cooldown still applies, or when the queue is full; the last case is
// reported because dropping alerts is worth logging.
func (d *Dispatcher) Submit(task Task) bool {
	token, chatID, ok := d.settings()
	if !ok || strings.TrimSpace(token) == "" || (d.requireChatID && strings.TrimSpace(chatID) == "") {
		d.mu.Lock()
		d.status.Suppressed++
		d.mu.Unlock()
		return false
	}
	now := time.Now().UTC()
	if task.Key != "" {
		d.mu.Lock()
		last, seen := d.lastSend[task.Key]
		if seen && now.Sub(last) < d.cooldown {
			d.status.Suppressed++
			d.mu.Unlock()
			return false
		}
		d.mu.Unlock()
	}
	select {
	case d.queue <- task:
		d.mu.Lock()
		d.depth++
		d.mu.Unlock()
		return true
	default:
		d.mu.Lock()
		d.status.Suppressed++
		d.status.LastError = "notification queue is full"
		d.mu.Unlock()
		d.log.Warn("notification queue full, message dropped", "key", task.Key)
		return false
	}
}

// deliver performs the retries and updates the status. A failure after all
// attempts is recorded and the message is dropped, because a stale alert is worse
// than a missing one and the page still shows the row.
func (d *Dispatcher) deliver(ctx context.Context, task Task) {
	defer func() {
		d.mu.Lock()
		if d.depth > 0 {
			d.depth--
		}
		d.mu.Unlock()
	}()
	token, chatID, ok := d.settings()
	if !ok {
		return
	}
	// Telegram rate limits a bot to roughly one message per second per chat, so
	// consecutive messages are spaced out instead of being rejected. The same
	// bounded spacing also keeps webhook bursts from overwhelming the receiver.
	d.mu.Lock()
	last := d.status.LastAttemptAt
	d.mu.Unlock()
	if !last.IsZero() {
		if wait := d.minSpacing - time.Since(last); wait > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
		}
	}
	attempt := time.Now().UTC()
	d.mu.Lock()
	d.status.LastAttemptAt = attempt
	d.mu.Unlock()
	var err error
	for i := 0; i < d.attempts; i++ {
		if ctx.Err() != nil {
			return
		}
		err = d.sender.Send(ctx, token, chatID, task.Text)
		if err == nil {
			break
		}
		if i+1 < d.attempts {
			wait := d.retryWait * time.Duration(1<<i)
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if err != nil {
		d.status.Failed++
		d.status.LastError = err.Error()
		d.log.Warn("notification delivery failed", "key", task.Key, "error", err)
		return
	}
	d.status.Sent++
	d.status.LastError = ""
	d.status.LastSuccessAt = time.Now().UTC()
	if task.Key != "" {
		d.lastSend[task.Key] = d.status.LastSuccessAt
	}
}

// Snapshot reports the worker counters and queue depth.
func (d *Dispatcher) Snapshot() (Status, int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.status, d.depth
}

// Send posts one message with Telegram's sendMessage and reports API errors as Go
// errors. The token is only present in the request URL and is never logged.
func (t *Telegram) Send(ctx context.Context, token, chatID, text string) error {
	token, chatID = strings.TrimSpace(token), strings.TrimSpace(chatID)
	if token == "" || chatID == "" {
		return errors.New("telegram token and chat id are required")
	}
	if !ValidToken(token) {
		return errors.New("telegram token has an invalid format")
	}
	base := strings.TrimRight(t.Base, "/")
	if base == "" {
		base = DefaultAPIBase
	}
	client := t.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	form := url.Values{"chat_id": {chatID}, "text": {text}, "disable_web_page_preview": {"true"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/bot"+token+"/sendMessage", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return err
	}
	var payload struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
		ErrorCode   int    `json:"error_code"`
	}
	if len(body) > 0 {
		_ = json.Unmarshal(body, &payload)
	}
	if resp.StatusCode/100 != 2 || !payload.OK {
		message := strings.TrimSpace(payload.Description)
		if message == "" {
			message = strings.TrimSpace(string(body))
		}
		if message == "" {
			message = resp.Status
		}
		if len(message) > 300 {
			message = message[:300]
		}
		return fmt.Errorf("telegram rejected the message (http %d): %s", resp.StatusCode, message)
	}
	return nil
}
