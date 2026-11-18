package binancerest

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSchedulerReservesWeightForExecution(t *testing.T) {
	s := New(8, 8, 0)
	background := Request{Host: "fapi.binance.com", Path: "/fapi/v1/klines", Class: ClassBackground, Priority: PriorityBackground}
	for i := 0; i < 6; i++ {
		release, err := s.Acquire(context.Background(), background)
		if err != nil {
			t.Fatalf("background acquire %d: %v", i, err)
		}
		release()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := s.Acquire(ctx, background); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected background budget exhaustion, got %v", err)
	}
	release, err := s.Acquire(context.Background(), Request{Host: "demo-fapi.binance.com", Path: "/fapi/v1/order", Class: ClassExecution, Priority: PriorityExecution})
	if err != nil {
		t.Fatalf("execution request should use reserved capacity: %v", err)
	}
	release()
}

func TestSchedulerPacesBackgroundAndAccountRequestsSeparately(t *testing.T) {
	s := NewWithClassIntervals(1200, 4, 100*time.Millisecond, 500*time.Millisecond, 100*time.Millisecond)
	now := time.Date(2026, time.November, 18, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	background := Request{Host: "fapi.binance.com", Path: "/futures/data/openInterestHist", Class: ClassBackground, Priority: PriorityBackground}
	marketInfo := Request{Host: "fapi.binance.com", Path: "/fapi/v1/openInterest", Class: ClassMarketInfo, Priority: PriorityBackground}
	account := Request{Host: "demo-fapi.binance.com", Path: "/fapi/v1/openOrders", Class: ClassAccount, Priority: PriorityAccount}

	release, err := s.Acquire(context.Background(), background)
	if err != nil {
		t.Fatalf("acquire background request: %v", err)
	}
	release()
	release, err = s.Acquire(context.Background(), account)
	if err != nil {
		t.Fatalf("account request should not wait for background pacing: %v", err)
	}
	release()
	release, err = s.Acquire(context.Background(), marketInfo)
	if err != nil {
		t.Fatalf("market-info request should have an independent pacing clock: %v", err)
	}
	release()

	canAcquire := func(at time.Time, request Request) bool {
		t.Helper()
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.canAcquireLocked(at, request)
	}
	if canAcquire(now.Add(99*time.Millisecond), account) {
		t.Fatal("account request acquired before its 100ms interval")
	}
	if !canAcquire(now.Add(100*time.Millisecond), account) {
		t.Fatal("account request should acquire at its 100ms interval")
	}
	if canAcquire(now.Add(99*time.Millisecond), marketInfo) {
		t.Fatal("market-info request acquired before its 100ms interval")
	}
	if !canAcquire(now.Add(100*time.Millisecond), marketInfo) {
		t.Fatal("market-info request should acquire at its 100ms interval")
	}
	if canAcquire(now.Add(499*time.Millisecond), background) {
		t.Fatal("background request acquired before its 500ms interval")
	}
	if !canAcquire(now.Add(500*time.Millisecond), background) {
		t.Fatal("background request should acquire at its 500ms interval")
	}
	if !canAcquire(now, Request{Host: "demo-fapi.binance.com", Path: "/fapi/v1/order", Class: ClassExecution, Priority: PriorityExecution}) {
		t.Fatal("execution request should bypass class pacing")
	}
}

func TestClassForRequestSeparatesLiveMarketInfoFromHistoricalPages(t *testing.T) {
	cases := []struct {
		request string
		want    string
	}{
		{"https://fapi.binance.com/fapi/v1/openInterest?symbol=ETHUSDT", ClassMarketInfo},
		{"https://fapi.binance.com/futures/data/openInterestHist?symbol=ETHUSDT&period=5m&limit=1", ClassMarketInfo},
		{"https://fapi.binance.com/futures/data/openInterestHist?symbol=ETHUSDT&period=5m&limit=500&startTime=1", ClassBackground},
		{"https://fapi.binance.com/futures/data/globalLongShortAccountRatio?symbol=ETHUSDT&period=5m&limit=1", ClassMarketInfo},
		{"https://fapi.binance.com/futures/data/topLongShortPositionRatio?symbol=ETHUSDT&period=5m&limit=500", ClassBackground},
	}
	for _, tc := range cases {
		if got := ClassForRequest(tc.request); got != tc.want {
			t.Errorf("ClassForRequest(%q) = %q, want %q", tc.request, got, tc.want)
		}
	}
}

func TestNewFromEnvUsesBackgroundMinIntervalWithExistingFallback(t *testing.T) {
	t.Setenv("APP_BINANCE_REST_WEIGHT_BUDGET_PER_MIN", "900")
	t.Setenv("APP_BINANCE_REST_MAX_INFLIGHT", "3")
	t.Setenv("APP_BINANCE_REST_MIN_INTERVAL", "100ms")
	t.Setenv("APP_BINANCE_REST_BACKGROUND_MIN_INTERVAL", "500ms")
	t.Setenv("APP_BINANCE_REST_MARKET_INFO_MIN_INTERVAL", "100ms")
	s := NewFromEnv()
	if s.minInterval != 100*time.Millisecond || s.backgroundMinInterval != 500*time.Millisecond || s.marketInfoMinInterval != 100*time.Millisecond {
		t.Fatalf("unexpected class intervals: account=%s background=%s market_info=%s", s.minInterval, s.backgroundMinInterval, s.marketInfoMinInterval)
	}

	t.Setenv("APP_BINANCE_REST_BACKGROUND_MIN_INTERVAL", "")
	t.Setenv("APP_BINANCE_REST_MARKET_INFO_MIN_INTERVAL", "")
	s = NewFromEnv()
	if s.backgroundMinInterval != s.minInterval {
		t.Fatalf("background interval should fall back to account interval: account=%s background=%s", s.minInterval, s.backgroundMinInterval)
	}
	if s.marketInfoMinInterval != s.minInterval {
		t.Fatalf("market-info interval should fall back to account interval: account=%s market_info=%s", s.minInterval, s.marketInfoMinInterval)
	}
}

func TestSchedulerLimitsConcurrencyAndHonorsCancellation(t *testing.T) {
	s := New(1200, 2, 0)
	request := Request{Host: "fapi.binance.com", Path: "/fapi/v1/account", Class: ClassAccount, Priority: PriorityAccount}
	first, err := s.Acquire(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Acquire(context.Background(), request)
	if err != nil {
		first()
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := s.Acquire(ctx, request); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected cancellation while all slots are busy, got %v", err)
	}
	first()
	second()
	if got := s.Snapshot().Blocked; got == 0 {
		t.Fatal("expected a blocked request to be counted")
	}
}

func TestSchedulerPausesAcrossBinanceHostsAndParsesBanDeadline(t *testing.T) {
	s := New(1200, 4, 0)
	until, ok := ParsePauseUntil("Binance code -1003: Way too many requests; IP banned until 1793205212975")
	if !ok || until.IsZero() {
		t.Fatal("expected ban deadline to parse")
	}
	future := time.Now().UTC().Add(time.Hour)
	now := future.Add(-time.Minute)
	s.PauseUntil(future)
	for _, host := range []string{"fapi.binance.com", "demo-fapi.binance.com", "eapi.binance.com"} {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		_, err := s.Acquire(ctx, Request{Host: host, Path: "/fapi/v1/ping"})
		cancel()
		var pause *PauseError
		if !errors.As(err, &pause) {
			t.Fatalf("expected shared pause for %s, got %v", host, err)
		}
	}
	if paused, ok := s.PausedUntil(now); !ok || !paused.Equal(future) {
		t.Fatalf("unexpected pause state: %v %v", paused, ok)
	}
}

func TestSchedulerRecordsResponseWeightAndStatus(t *testing.T) {
	s := New(1200, 4, 0)
	request := Request{Host: "fapi.binance.com", Path: "/fapi/v1/exchangeInfo", Class: ClassBackground, Priority: PriorityBackground}
	release, err := s.Acquire(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	release()
	s.Complete(request, 200, 37, nil)
	snapshot := s.Snapshot()
	if len(snapshot.Endpoints) != 1 || snapshot.Endpoints[0].LastResponseWeight != 37 || snapshot.Endpoints[0].LastStatus != 200 {
		t.Fatalf("unexpected endpoint stats: %+v", snapshot.Endpoints)
	}
}

func TestSchedulerReconcilesIPWideResponseWeight(t *testing.T) {
	s := New(100, 4, 0)
	request := Request{Host: "fapi.binance.com", Path: "/fapi/v1/account", Class: ClassAccount, Priority: PriorityAccount}
	release, err := s.Acquire(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	release()
	s.Complete(request, 200, 73, nil)
	if got := s.Snapshot().WindowUsed; got != 73 {
		t.Fatalf("expected IP-wide response weight 73, got %d", got)
	}
}

func TestWeightForDepthQuery(t *testing.T) {
	for raw, want := range map[string]int{
		"/fapi/v1/depth?symbol=BTCUSDT&limit=100":  5,
		"/fapi/v1/depth?limit=500":                 10,
		"/fapi/v1/depth?limit=1000&symbol=BTCUSDT": 20,
		"/fapi/v1/depth?limit=5000":                50,
	} {
		if got := WeightForPath(raw); got != want {
			t.Errorf("WeightForPath(%q) = %d, want %d", raw, got, want)
		}
	}
}
