package binancerest

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	ClassBackground = "background"
	ClassMarketInfo = "market_info"
	ClassAccount    = "account"
	ClassExecution  = "execution"
)

type Priority uint8

const (
	PriorityBackground Priority = iota
	PriorityAccount
	PriorityExecution
)

type Request struct {
	Host     string
	Path     string
	Class    string
	Priority Priority
	Weight   int
}

type PauseError struct {
	Until time.Time
}

func (e *PauseError) Error() string {
	if e == nil || e.Until.IsZero() {
		return "Binance REST is paused"
	}
	return fmt.Sprintf("Binance REST paused until %s", e.Until.UTC().Format(time.RFC3339))
}

type EndpointStats struct {
	Key                string
	Host               string
	Path               string
	Class              string
	Requests           uint64
	Completed          uint64
	Errors             uint64
	Blocked            uint64
	LastStatus         int
	LastResponseWeight int
	LastError          string
}

type Snapshot struct {
	BudgetPerMinute int
	ReservedWeight  int
	MaxInFlight     int
	MinInterval     time.Duration
	WindowStarted   time.Time
	WindowUsed      int
	InFlight        int
	Queued          int
	Blocked         uint64
	PausedUntil     time.Time
	Endpoints       []EndpointStats
}

type Scheduler struct {
	mu                    sync.Mutex
	budget                int
	reserved              int
	maxInFlight           int
	minInterval           time.Duration
	backgroundMinInterval time.Duration
	marketInfoMinInterval time.Duration
	now                   func() time.Time
	windowStarted         time.Time
	windowUsed            int
	lastBackground        time.Time
	lastMarketInfo        time.Time
	lastAccount           time.Time
	inFlight              int
	queued                int
	blocked               uint64
	pausedUntil           time.Time
	endpoints             map[string]EndpointStats
}

var defaultScheduler = NewFromEnv()

func NewFromEnv() *Scheduler {
	accountInterval := envDuration("APP_BINANCE_REST_MIN_INTERVAL", 100*time.Millisecond)
	backgroundInterval := envDuration("APP_BINANCE_REST_BACKGROUND_MIN_INTERVAL", accountInterval)
	marketInfoInterval := envDuration("APP_BINANCE_REST_MARKET_INFO_MIN_INTERVAL", accountInterval)
	return NewWithClassIntervals(
		envInt("APP_BINANCE_REST_WEIGHT_BUDGET_PER_MIN", 1200),
		envInt("APP_BINANCE_REST_MAX_INFLIGHT", 4),
		accountInterval,
		backgroundInterval,
		marketInfoInterval,
	)
}

func New(budgetPerMinute, maxInFlight int, minInterval time.Duration) *Scheduler {
	return NewWithIntervals(budgetPerMinute, maxInFlight, minInterval, minInterval)
}

// NewWithIntervals creates a scheduler with independent pacing for account
// and background requests. A zero interval disables pacing for that class.
func NewWithIntervals(budgetPerMinute, maxInFlight int, accountMinInterval, backgroundMinInterval time.Duration) *Scheduler {
	return NewWithClassIntervals(budgetPerMinute, maxInFlight, accountMinInterval, backgroundMinInterval, backgroundMinInterval)
}

// NewWithClassIntervals creates a scheduler with independent pacing for
// account, market-info, and other background requests. A zero interval
// disables pacing for that class.
func NewWithClassIntervals(budgetPerMinute, maxInFlight int, accountMinInterval, backgroundMinInterval, marketInfoMinInterval time.Duration) *Scheduler {
	if budgetPerMinute <= 0 {
		budgetPerMinute = 1200
	}
	if maxInFlight <= 0 {
		maxInFlight = 4
	}
	if accountMinInterval < 0 {
		accountMinInterval = 100 * time.Millisecond
	}
	if backgroundMinInterval < 0 {
		backgroundMinInterval = accountMinInterval
	}
	if marketInfoMinInterval < 0 {
		marketInfoMinInterval = accountMinInterval
	}
	reserved := budgetPerMinute / 4
	return &Scheduler{
		budget:                budgetPerMinute,
		reserved:              reserved,
		maxInFlight:           maxInFlight,
		minInterval:           accountMinInterval,
		backgroundMinInterval: backgroundMinInterval,
		marketInfoMinInterval: marketInfoMinInterval,
		now:                   time.Now,
		endpoints:             make(map[string]EndpointStats),
	}
}

func Default() *Scheduler { return defaultScheduler }

func ResetDefault() {
	defaultScheduler = NewFromEnv()
}

func IsBinanceURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return strings.HasSuffix(host, "binance.com") || strings.HasSuffix(host, "binancefuture.com")
}

func NormalizeRequest(req Request) Request {
	req.Host = strings.ToLower(strings.TrimSpace(req.Host))
	if parsed, err := url.Parse(req.Host); err == nil && parsed.Hostname() != "" {
		req.Host = strings.ToLower(parsed.Hostname())
	}
	req.Host = strings.TrimSuffix(strings.Split(req.Host, "/")[0], ":443")
	req.Path = strings.TrimSpace(req.Path)
	if req.Path == "" {
		req.Path = "/"
	}
	if req.Class == "" {
		req.Class = ClassBackground
	}
	if req.Weight <= 0 {
		req.Weight = 1
	}
	return req
}

func WeightForPath(path string) int {
	path = strings.ToLower(strings.TrimSpace(path))
	parsed, err := url.Parse(path)
	pathname := path
	if err == nil {
		pathname = parsed.Path
		path = pathname + "?" + parsed.RawQuery
	}
	if strings.Contains(pathname, "/ticker/24hr") || strings.HasSuffix(pathname, "/mark") {
		return 40
	}
	if strings.Contains(path, "/depth") {
		limit := 100
		if parsed, err := url.Parse(path); err == nil {
			if value, err := strconv.Atoi(parsed.Query().Get("limit")); err == nil && value > 0 {
				limit = value
			}
		}
		switch {
		case limit <= 100:
			return 5
		case limit <= 500:
			return 10
		case limit <= 1000:
			return 20
		default:
			return 50
		}
	}
	return 1
}

func (s *Scheduler) Acquire(ctx context.Context, request Request) (func(), error) {
	if s == nil || !IsBinanceHost(request.Host) {
		return func() {}, nil
	}
	request = NormalizeRequest(request)
	key := endpointKey(request)
	s.mu.Lock()
	s.queued++
	s.mu.Unlock()
	dequeued := false
	dequeue := func() {
		if dequeued {
			return
		}
		dequeued = true
		s.mu.Lock()
		s.queued--
		s.mu.Unlock()
	}
	defer dequeue()

	for {
		now := s.clock()
		s.mu.Lock()
		s.resetWindowLocked(now)
		if s.pausedUntil.After(now) {
			until := s.pausedUntil
			s.blocked++
			s.bumpBlockedLocked(key, request)
			s.mu.Unlock()
			return nil, &PauseError{Until: until}
		}
		if s.canAcquireLocked(now, request) {
			s.windowUsed += request.Weight
			s.inFlight++
			if request.Priority < PriorityExecution {
				switch request.Class {
				case ClassAccount:
					s.lastAccount = now
				case ClassMarketInfo:
					s.lastMarketInfo = now
				default:
					s.lastBackground = now
				}
			}
			s.bumpRequestLocked(key, request)
			s.mu.Unlock()
			dequeue()
			return func() {
				s.mu.Lock()
				if s.inFlight > 0 {
					s.inFlight--
				}
				s.mu.Unlock()
			}, nil
		}
		wait := s.waitDurationLocked(now, request)
		s.mu.Unlock()
		if wait <= 0 {
			wait = 10 * time.Millisecond
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			dequeue()
			s.mu.Lock()
			s.blocked++
			s.bumpBlockedLocked(key, request)
			s.mu.Unlock()
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (s *Scheduler) Complete(request Request, status, usedWeight int, requestErr error) {
	if s == nil || !IsBinanceHost(request.Host) {
		return
	}
	request = NormalizeRequest(request)
	key := endpointKey(request)
	now := s.clock()
	s.mu.Lock()
	defer s.mu.Unlock()
	stats := s.endpoints[key]
	stats.Key = key
	stats.Host = request.Host
	stats.Path = request.Path
	stats.Class = request.Class
	stats.Completed++
	stats.LastStatus = status
	if usedWeight > 0 {
		stats.LastResponseWeight = usedWeight
		// Binance reports the IP-wide weight already consumed in this minute.
		// Reconcile our estimate with it so traffic from other processes sharing
		// the IP cannot silently push this scheduler over the exchange limit.
		s.resetWindowLocked(now)
		if usedWeight > s.windowUsed {
			s.windowUsed = usedWeight
		}
	}
	if requestErr != nil || status < 200 || status >= 300 {
		stats.Errors++
		stats.LastError = requestErrString(requestErr, status)
	}
	if status == 429 || status == httpStatusTeapot {
		until := now.Add(5 * time.Minute)
		if until.After(s.pausedUntil) {
			s.pausedUntil = until
		}
	}
	s.endpoints[key] = stats
}

func (s *Scheduler) PauseUntil(until time.Time) {
	if s == nil || until.IsZero() {
		return
	}
	s.mu.Lock()
	if until.After(s.pausedUntil) {
		s.pausedUntil = until.UTC()
	}
	s.mu.Unlock()
}

func (s *Scheduler) PausedUntil(now time.Time) (time.Time, bool) {
	if s == nil {
		return time.Time{}, false
	}
	if now.IsZero() {
		now = s.clock()
	}
	now = now.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !now.Before(s.pausedUntil) {
		s.pausedUntil = time.Time{}
		return time.Time{}, false
	}
	return s.pausedUntil, true
}

func (s *Scheduler) Snapshot() Snapshot {
	if s == nil {
		return Snapshot{}
	}
	now := s.clock()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resetWindowLocked(now)
	endpoints := make([]EndpointStats, 0, len(s.endpoints))
	for _, stats := range s.endpoints {
		endpoints = append(endpoints, stats)
	}
	return Snapshot{
		BudgetPerMinute: s.budget,
		ReservedWeight:  s.reserved,
		MaxInFlight:     s.maxInFlight,
		MinInterval:     s.minInterval,
		WindowStarted:   s.windowStarted,
		WindowUsed:      s.windowUsed,
		InFlight:        s.inFlight,
		Queued:          s.queued,
		Blocked:         s.blocked,
		PausedUntil:     s.pausedUntil,
		Endpoints:       endpoints,
	}
}

func (s *Scheduler) clock() time.Time {
	if s.now == nil {
		return time.Now().UTC()
	}
	return s.now().UTC()
}

func (s *Scheduler) resetWindowLocked(now time.Time) {
	if s.windowStarted.IsZero() || !now.Before(s.windowStarted.Add(time.Minute)) {
		s.windowStarted = now.Truncate(time.Minute)
		s.windowUsed = 0
	}
}

func (s *Scheduler) canAcquireLocked(now time.Time, request Request) bool {
	if s.inFlight >= s.maxInFlight {
		return false
	}
	limit := s.budget
	if request.Priority < PriorityExecution {
		limit -= s.reserved
	}
	if s.windowUsed+request.Weight > limit {
		return false
	}
	if last, interval, limited := s.intervalForRequest(request); limited && !last.IsZero() && now.Before(last.Add(interval)) {
		return false
	}
	return true
}

func (s *Scheduler) waitDurationLocked(now time.Time, request Request) time.Duration {
	if s.inFlight >= s.maxInFlight {
		return 10 * time.Millisecond
	}
	limit := s.budget
	if request.Priority < PriorityExecution {
		limit -= s.reserved
	}
	if s.windowUsed+request.Weight > limit {
		return s.windowStarted.Add(time.Minute).Sub(now)
	}
	if last, interval, limited := s.intervalForRequest(request); limited && !last.IsZero() {
		return last.Add(interval).Sub(now)
	}
	return 0
}

func (s *Scheduler) intervalForRequest(request Request) (time.Time, time.Duration, bool) {
	if request.Priority >= PriorityExecution {
		return time.Time{}, 0, false
	}
	switch request.Class {
	case ClassAccount:
		return s.lastAccount, s.minInterval, true
	case ClassMarketInfo:
		return s.lastMarketInfo, s.marketInfoMinInterval, true
	default:
		return s.lastBackground, s.backgroundMinInterval, true
	}
}

// ClassForRequest puts only live market-info queries on their own pacing
// clock. Larger historical pages stay in the general background class.
func ClassForRequest(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ClassBackground
	}
	path := parsed.Path
	switch path {
	case "/fapi/v1/openInterest":
		return ClassMarketInfo
	case "/futures/data/openInterestHist", "/futures/data/globalLongShortAccountRatio", "/futures/data/topLongShortPositionRatio":
		if parsed.Query().Get("limit") == "1" {
			return ClassMarketInfo
		}
	}
	return ClassBackground
}

func (s *Scheduler) bumpRequestLocked(key string, request Request) {
	stats := s.endpoints[key]
	stats.Key = key
	stats.Host = request.Host
	stats.Path = request.Path
	stats.Class = request.Class
	stats.Requests++
	s.endpoints[key] = stats
}

func (s *Scheduler) bumpBlockedLocked(key string, request Request) {
	stats := s.endpoints[key]
	stats.Key = key
	stats.Host = request.Host
	stats.Path = request.Path
	stats.Class = request.Class
	stats.Blocked++
	s.endpoints[key] = stats
}

func endpointKey(request Request) string {
	return request.Host + " " + request.Path + " " + request.Class
}

const httpStatusTeapot = 418

// ParsePauseUntil extracts Binance's optional millisecond/second ban deadline
// from a response message such as "banned until 1793205212975".
func ParsePauseUntil(text string) (time.Time, bool) {
	fields := strings.Fields(strings.ToLower(text))
	for i, field := range fields {
		if strings.Trim(field, ":,;\"'`()") != "until" || i+1 >= len(fields) {
			continue
		}
		raw := strings.Trim(fields[i+1], "`.,;:)\"'")
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			continue
		}
		if value >= 1_000_000_000_000 {
			return time.UnixMilli(value).UTC(), true
		}
		if value >= 1_000_000_000 {
			return time.Unix(value, 0).UTC(), true
		}
	}
	return time.Time{}, false
}

func requestErrString(err error, status int) string {
	if err != nil {
		return err.Error()
	}
	return strconv.Itoa(status)
}

func IsBinanceHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	host = strings.TrimPrefix(host, "https://")
	host = strings.TrimPrefix(host, "http://")
	host = strings.Split(host, "/")[0]
	return strings.HasSuffix(host, "binance.com") || strings.HasSuffix(host, "binancefuture.com")
}

func envInt(key string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key)))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func envDuration(key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	if value, err := time.ParseDuration(raw); err == nil && value >= 0 {
		return value
	}
	if value, err := strconv.Atoi(raw); err == nil && value >= 0 {
		return time.Duration(value) * time.Millisecond
	}
	return fallback
}
