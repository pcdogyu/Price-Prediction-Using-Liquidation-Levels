// Package notify delivers alert and signal messages to Telegram and buffers the
// work so a slow or unreachable Telegram never blocks liquidation ingestion.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/url"
	"path/filepath"
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

// PhotoSender is implemented by notification senders that can deliver a photo
// after a text task. It is optional so existing webhook senders remain text-only.
type PhotoSender interface {
	SendPhoto(ctx context.Context, token, chatID string, photo []byte, filename, caption string) error
}

// PhotoFunc builds a photo immediately before its queued task is delivered. This
// keeps the photo task next to the text task in FIFO order without blocking the
// liquidation ingestion path while K-line data is fetched.
type PhotoFunc func(context.Context) ([]byte, error)

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

const (
	// PriorityNormal is used for opening requests.
	PriorityNormal = iota
	// PriorityClose is used for closing requests so a direction switch closes
	// the existing position before an opening request is sent.
	PriorityClose
)

// Task is one queued message. Key groups duplicates, for example all aggressive
// signals of one symbol, so a burst cannot flood the chat.
type Task struct {
	Key          string
	Text         string
	Photo        PhotoFunc
	PhotoName    string
	PhotoCaption string
	Priority     int
	SerialKey    string
	// Prepare builds the message immediately before delivery. It is useful for
	// order webhooks because sent_at should describe dispatch time, not the time
	// at which a signal happened to enter a busy queue.
	Prepare func(time.Time) (string, error)

	enqueuedAt time.Time
	sequence   uint64
}

// Status summarizes the worker for the alert page.
type Status struct {
	Sent            int64
	Suppressed      int64
	Failed          int64
	Expired         int64
	QueueDepth      int
	OldestTaskAge   time.Duration
	LastQueueWait   time.Duration
	LastSendLatency time.Duration
	LastAttemptAt   time.Time
	LastSuccessAt   time.Time
	LastError       string
}

// Dispatcher is a bounded priority worker pool with per-key cooldown, retries,
// optional per-symbol serialization, and a status snapshot for the UI.
type Dispatcher struct {
	sender   Sender
	settings Settings
	log      *slog.Logger

	cooldown    time.Duration
	minSpacing  time.Duration
	attempts    int
	retryWait   time.Duration
	workers     int
	maxQueueAge time.Duration

	queue         chan Task
	priorityQueue chan Task

	mu          sync.Mutex
	nextMu      sync.Mutex
	status      Status
	lastSend    map[string]time.Time
	depth       int
	sequence    uint64
	pending     map[uint64]time.Time
	serialLocks map[string]*sync.Mutex

	// Telegram requires both credentials, while a webhook only needs its
	// endpoint. Keep the legacy constructor strict and expose a separate
	// constructor for senders whose second credential is intentionally empty.
	requireChatID bool
}

// NewDispatcher builds a worker. A capacity of zero uses 64, and zero cooldown or
// spacing falls back to five minutes and 1.5 seconds.
func NewDispatcher(sender Sender, settings Settings, capacity int, cooldown time.Duration, log *slog.Logger) *Dispatcher {
	return newDispatcher(sender, settings, capacity, cooldown, log, true, 1, 0, 1500*time.Millisecond)
}

// NewWebhookDispatcher builds a worker for a webhook sender. The endpoint is
// supplied as the first credential and no chat ID is required.
func NewWebhookDispatcher(sender Sender, settings Settings, capacity int, cooldown time.Duration, log *slog.Logger) *Dispatcher {
	return newDispatcher(sender, settings, capacity, cooldown, log, false, 8, 30*time.Second, 0)
}

func newDispatcher(sender Sender, settings Settings, capacity int, cooldown time.Duration, log *slog.Logger, requireChatID bool, workers int, maxQueueAge, minSpacing time.Duration) *Dispatcher {
	if capacity <= 0 {
		capacity = 64
	}
	if cooldown <= 0 {
		cooldown = 5 * time.Minute
	}
	if log == nil {
		log = slog.Default()
	}
	if workers <= 0 {
		workers = 1
	}
	return &Dispatcher{
		sender: sender, settings: settings, log: log, cooldown: cooldown,
		minSpacing: minSpacing, attempts: 3, retryWait: time.Second,
		workers: workers, maxQueueAge: maxQueueAge,
		queue: make(chan Task, capacity), priorityQueue: make(chan Task, capacity),
		lastSend: map[string]time.Time{}, pending: map[uint64]time.Time{},
		serialLocks: map[string]*sync.Mutex{}, requireChatID: requireChatID,
	}
}

// Start runs the worker until ctx is cancelled.
func (d *Dispatcher) Start(ctx context.Context) {
	var workers sync.WaitGroup
	workers.Add(d.workers)
	for i := 0; i < d.workers; i++ {
		go func() {
			defer workers.Done()
			d.worker(ctx)
		}()
	}
	workers.Wait()
}

func (d *Dispatcher) worker(ctx context.Context) {
	for {
		task, ok := d.nextTask(ctx)
		if !ok {
			return
		}
		d.dequeue(task)
		var serialLock *sync.Mutex
		if task.SerialKey != "" {
			serialLock = d.getSerialLock(task.SerialKey)
			serialLock.Lock()
		}
		if d.expired(task) {
			if serialLock != nil {
				serialLock.Unlock()
			}
			continue
		}
		d.deliver(ctx, task)
		if serialLock != nil {
			serialLock.Unlock()
		}
	}
}

func (d *Dispatcher) nextTask(ctx context.Context) (Task, bool) {
	d.nextMu.Lock()
	defer d.nextMu.Unlock()
	// Prefer a close task whenever one is already waiting. The second select
	// keeps the worker responsive to cancellation while still allowing normal
	// tasks to make progress when there are no close requests.
	select {
	case task := <-d.priorityQueue:
		return task, true
	default:
	}
	select {
	case <-ctx.Done():
		return Task{}, false
	case task := <-d.priorityQueue:
		return task, true
	default:
	}
	select {
	case <-ctx.Done():
		return Task{}, false
	case task := <-d.priorityQueue:
		return task, true
	case task := <-d.queue:
		return task, true
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
	if task.enqueuedAt.IsZero() {
		task.enqueuedAt = now
	}
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
	d.mu.Lock()
	if d.depth >= cap(d.queue) {
		d.status.Suppressed++
		d.status.LastError = "notification queue is full"
		d.mu.Unlock()
		d.log.Warn("notification queue full, message dropped", "key", task.Key)
		return false
	}
	d.sequence++
	task.sequence = d.sequence
	var queued bool
	if task.Priority >= PriorityClose {
		select {
		case d.priorityQueue <- task:
			queued = true
		default:
		}
	} else {
		select {
		case d.queue <- task:
			queued = true
		default:
		}
	}
	if queued {
		d.depth++
		d.pending[task.sequence] = task.enqueuedAt
		d.status.QueueDepth = d.depth
		d.mu.Unlock()
		return true
	}
	d.status.Suppressed++
	d.status.LastError = "notification queue is full"
	d.mu.Unlock()
	d.log.Warn("notification queue full, message dropped", "key", task.Key)
	return false
}

// deliver performs the retries and updates the status. A failure after all
// attempts is recorded and the message is dropped, because a stale alert is worse
// than a missing one and the page still shows the row.
func (d *Dispatcher) deliver(ctx context.Context, task Task) {
	started := time.Now()
	defer func() {
		d.mu.Lock()
		d.status.LastSendLatency = time.Since(started)
		latency := d.status.LastSendLatency
		queueDepth := d.depth
		var oldest time.Time
		for _, at := range d.pending {
			if oldest.IsZero() || at.Before(oldest) {
				oldest = at
			}
		}
		queueAge := time.Duration(0)
		if !oldest.IsZero() {
			queueAge = time.Since(oldest)
		}
		queueWait := d.status.LastQueueWait
		d.mu.Unlock()
		if d.maxQueueAge > 0 {
			d.log.Info("webhook delivery metrics", "key", task.Key, "queue_depth", queueDepth, "oldest_task_age_ms", queueAge.Milliseconds(), "queue_wait_ms", queueWait.Milliseconds(), "send_latency_ms", latency.Milliseconds())
		}
	}()
	// Direct calls are useful to tests and keep the old helper semantics. Normal
	// workers account for queue depth when they dequeue the task.
	if task.sequence == 0 {
		d.accountDirectDelivery()
	}
	token, chatID, ok := d.settings()
	if !ok {
		return
	}
	// Telegram rate limits a bot to roughly one message per second per chat, so
	// consecutive messages are spaced out instead of being rejected. Webhooks
	// deliberately use zero global spacing because their worker pool is bounded
	// separately and same-symbol serialization still applies.
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
	if task.Prepare != nil {
		text, prepareErr := task.Prepare(time.Now().UTC())
		if prepareErr != nil {
			d.mu.Lock()
			d.status.Failed++
			d.status.LastError = prepareErr.Error()
			d.mu.Unlock()
			return
		}
		task.Text = text
	}
	var err error
	var photo []byte
	var photoSender PhotoSender
	if task.Photo != nil {
		photoSender, _ = d.sender.(PhotoSender)
		if photoSender == nil {
			err = errors.New("notification sender does not support photos")
		} else {
			photo, err = task.Photo(ctx)
			if err == nil && len(photo) == 0 {
				err = errors.New("notification photo is empty")
			}
		}
	}
	if err == nil {
		for i := 0; i < d.attempts; i++ {
			if ctx.Err() != nil {
				return
			}
			d.mu.Lock()
			d.status.LastAttemptAt = time.Now().UTC()
			d.mu.Unlock()
			if task.Photo != nil {
				err = photoSender.SendPhoto(ctx, token, chatID, photo, task.PhotoName, task.PhotoCaption)
			} else {
				err = d.sender.Send(ctx, token, chatID, task.Text)
			}
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

func (d *Dispatcher) dequeue(task Task) {
	d.mu.Lock()
	if task.sequence != 0 {
		delete(d.pending, task.sequence)
	} else {
		d.removeOldestPendingLocked()
	}
	if d.depth > 0 {
		d.depth--
	}
	d.status.QueueDepth = d.depth
	if !task.enqueuedAt.IsZero() {
		d.status.LastQueueWait = time.Since(task.enqueuedAt)
	}
	d.mu.Unlock()
}

func (d *Dispatcher) accountDirectDelivery() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.removeOldestPendingLocked()
	if d.depth > 0 {
		d.depth--
	}
	d.status.QueueDepth = d.depth
}

func (d *Dispatcher) removeOldestPendingLocked() {
	var oldest uint64
	var oldestAt time.Time
	for sequence, at := range d.pending {
		if oldestAt.IsZero() || at.Before(oldestAt) {
			oldest, oldestAt = sequence, at
		}
	}
	if oldest != 0 {
		delete(d.pending, oldest)
	}
}

func (d *Dispatcher) getSerialLock(key string) *sync.Mutex {
	d.mu.Lock()
	defer d.mu.Unlock()
	if lock := d.serialLocks[key]; lock != nil {
		return lock
	}
	lock := &sync.Mutex{}
	d.serialLocks[key] = lock
	return lock
}

func (d *Dispatcher) expired(task Task) bool {
	if d.maxQueueAge <= 0 || task.enqueuedAt.IsZero() {
		return false
	}
	age := time.Since(task.enqueuedAt)
	if age <= d.maxQueueAge {
		return false
	}
	d.mu.Lock()
	d.status.Expired++
	d.status.Suppressed++
	d.status.LastError = "notification task expired in queue"
	d.status.LastQueueWait = age
	d.mu.Unlock()
	d.log.Warn("notification task expired in queue", "key", task.Key, "age_ms", age.Milliseconds())
	return true
}

// Snapshot reports the worker counters and queue depth.
func (d *Dispatcher) Snapshot() (Status, int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.status.QueueDepth = d.depth
	var oldest time.Time
	for _, at := range d.pending {
		if oldest.IsZero() || at.Before(oldest) {
			oldest = at
		}
	}
	if oldest.IsZero() {
		d.status.OldestTaskAge = 0
	} else {
		d.status.OldestTaskAge = time.Since(oldest)
	}
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

// SendPhoto posts one image to Telegram using sendPhoto. The image bytes are
// generated by the caller so the sender does not need a filesystem path.
func (t *Telegram) SendPhoto(ctx context.Context, token, chatID string, photo []byte, filename, caption string) error {
	token, chatID = strings.TrimSpace(token), strings.TrimSpace(chatID)
	if token == "" || chatID == "" {
		return errors.New("telegram token and chat id are required")
	}
	if !ValidToken(token) {
		return errors.New("telegram token has an invalid format")
	}
	if len(photo) == 0 {
		return errors.New("telegram photo is empty")
	}
	if filename = filepath.Base(strings.TrimSpace(filename)); filename == "." || filename == "" {
		filename = "kline.png"
	}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	if err := form.WriteField("chat_id", chatID); err != nil {
		return err
	}
	if caption != "" {
		if err := form.WriteField("caption", caption); err != nil {
			return err
		}
	}
	part, err := form.CreateFormFile("photo", filename)
	if err != nil {
		return err
	}
	if _, err = part.Write(photo); err != nil {
		return err
	}
	if err = form.Close(); err != nil {
		return err
	}
	base := strings.TrimRight(t.Base, "/")
	if base == "" {
		base = DefaultAPIBase
	}
	client := t.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/bot"+token+"/sendPhoto", &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", form.FormDataContentType())
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return err
	}
	var payload struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if len(responseBody) > 0 {
		_ = json.Unmarshal(responseBody, &payload)
	}
	if resp.StatusCode/100 != 2 || !payload.OK {
		message := strings.TrimSpace(payload.Description)
		if message == "" {
			message = strings.TrimSpace(string(responseBody))
		}
		if message == "" {
			message = resp.Status
		}
		if len(message) > 300 {
			message = message[:300]
		}
		return fmt.Errorf("telegram rejected the photo (http %d): %s", resp.StatusCode, message)
	}
	return nil
}
