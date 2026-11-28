package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/config"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/store"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/volumeprofile"
)

func TestVolumeBootstrapSymbolsUsesBoundedConfiguredSet(t *testing.T) {
	svc := &Service{cfg: config.Config{BootstrapSymbols: []string{" btcusdt ", "ETHUSDT", "BTCUSDT", "SOLUSDT", ""}}}
	got := svc.volumeBootstrapSymbols()
	want := []string{"BTCUSDT", "ETHUSDT", "SOLUSDT"}
	if len(got) != len(want) {
		t.Fatalf("volume bootstrap symbols=%v, want=%v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("volume bootstrap symbols=%v, want=%v", got, want)
		}
	}
}

func TestVolumeBootstrapSymbolsFallsBackToBTCAndETH(t *testing.T) {
	svc := &Service{}
	got := svc.volumeBootstrapSymbols()
	want := []string{"BTCUSDT", "ETHUSDT"}
	if len(got) != len(want) {
		t.Fatalf("volume bootstrap fallback=%v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("volume bootstrap fallback=%v, want=%v", got, want)
		}
	}
}

func TestVolumeProfileSymbolsIncludesActiveAlertCandidates(t *testing.T) {
	svc := &Service{
		cfg:        config.Config{BootstrapSymbols: []string{"BTCUSDT", "SOLUSDT"}},
		candidates: []string{"ETHUSDT", "BTCUSDT", ""},
	}
	got := svc.volumeProfileSymbols()
	want := []string{"BTCUSDT", "ETHUSDT", "SOLUSDT"}
	if len(got) != len(want) {
		t.Fatalf("volume profile symbols=%v, want=%v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("volume profile symbols=%v, want=%v", got, want)
		}
	}
}

func TestCurrentVolumeSnapshotUsesLastSourceTradeAndSkipsStaleProfile(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "volume-snapshot-asof.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	session, err := volumeprofile.SessionAt(time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, tc := range []struct {
		name string
		age  time.Duration
		want int
	}{
		{name: "fresh", age: 5 * time.Second, want: 1},
		{name: "stale", age: valueAreaProfileMaxAge + time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			symbol := "BTCUSDT"
			if tc.name == "stale" {
				symbol = "ETHUSDT"
			}
			tradeTime := now.Add(-tc.age)
			if err := st.ApplyAggregateTrades(ctx, symbol, session.Start, []domain.AggregateTrade{{
				ID: int64(tc.age.Seconds()), Symbol: symbol, Time: tradeTime, Price: 100, Quantity: 2, PriceText: "100",
			}}, true); err != nil {
				t.Fatal(err)
			}
			svc := &Service{
				store: st, cfg: config.Config{BootstrapSymbols: []string{symbol}},
				volumeRequested: map[string]struct{}{symbol: {}},
			}
			profile, profileErr := svc.VolumeProfile(ctx, symbol)
			if profileErr != nil {
				t.Fatal(profileErr)
			}
			if tc.want > 0 && (profile.State != "ok" || !volumeProfileFreshAt(profile, now)) {
				t.Fatalf("profile=%+v err=%v fresh=%v", profile, profileErr, volumeProfileFreshAt(profile, now))
			}
			svc.saveCurrentVolumeSnapshots(ctx, now)
			rows, err := st.VolumeProfileSnapshots(ctx, symbol, session.Start, now.Add(time.Second))
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != tc.want {
				t.Fatalf("snapshots=%d, want=%d: %+v", len(rows), tc.want, rows)
			}
			if tc.want > 0 && !rows[0].Time.Equal(tradeTime.Truncate(time.Millisecond)) {
				t.Fatalf("snapshot time=%s, want last source trade %s", rows[0].Time, tradeTime)
			}
		})
	}
}

func TestAggregateTradeGap(t *testing.T) {
	tests := []struct {
		name   string
		lastID int64
		ids    []int64
		want   bool
	}{
		{name: "contiguous", lastID: 9, ids: []int64{10, 11, 12}},
		{name: "duplicates", lastID: 10, ids: []int64{10, 11, 11, 12}},
		{name: "first gap", lastID: 9, ids: []int64{11, 12}, want: true},
		{name: "internal gap", lastID: 9, ids: []int64{10, 12}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows := make([]domain.AggregateTrade, len(tt.ids))
			for i, id := range tt.ids {
				rows[i].ID = id
			}
			if got := aggregateTradeGap(rows, tt.lastID); got != tt.want {
				t.Fatalf("aggregateTradeGap() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEnsureVolumeProfileDeduplicatesPendingRequest(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "volume-ensure.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	svc := &Service{
		store:               st,
		volumeContext:       context.Background(),
		volumeRequested:     make(map[string]struct{}),
		volumeStreamSymbols: make(map[string]struct{}),
		volumePending:       make(map[string]struct{}),
		volumeBackfillError: make(map[string]string),
		volumeBackfillQueue: make(chan string, 4),
	}
	if err := svc.EnsureVolumeProfile(context.Background(), "SOLUSDT", false); err != nil {
		t.Fatal(err)
	}
	if err := svc.EnsureVolumeProfile(context.Background(), "SOLUSDT", false); err != nil {
		t.Fatal(err)
	}
	if got := len(svc.volumeBackfillQueue); got != 1 {
		t.Fatalf("queued backfills=%d, want one", got)
	}
	if got := <-svc.volumeBackfillQueue; got != "SOLUSDT" {
		t.Fatalf("queued symbol=%s, want SOLUSDT", got)
	}
}

func TestEnsureVolumeProfileRetriesAfterBackoffAndForceBypassesIt(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "volume-retry.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	svc := &Service{
		store:                 st,
		volumeContext:         context.Background(),
		volumeRequested:       make(map[string]struct{}),
		volumeStreamSymbols:   make(map[string]struct{}),
		volumePending:         make(map[string]struct{}),
		volumeBackfillError:   map[string]string{"SOLUSDT": "temporary exchange error"},
		volumeBackfillRetryAt: map[string]time.Time{"SOLUSDT": time.Now().Add(time.Minute)},
		volumeBackfillQueue:   make(chan string, 4),
	}

	if err := svc.EnsureVolumeProfile(context.Background(), "SOLUSDT", false); err != nil {
		t.Fatal(err)
	}
	if got := len(svc.volumeBackfillQueue); got != 0 {
		t.Fatalf("queued backfills during retry cooldown=%d, want 0", got)
	}

	if err := svc.EnsureVolumeProfile(context.Background(), "SOLUSDT", true); err != nil {
		t.Fatal(err)
	}
	if got := len(svc.volumeBackfillQueue); got != 1 {
		t.Fatalf("forced retries=%d, want 1", got)
	}
	if got := <-svc.volumeBackfillQueue; got != "SOLUSDT" {
		t.Fatalf("queued symbol=%s, want SOLUSDT", got)
	}

	delete(svc.volumePending, "SOLUSDT")
	svc.volumeBackfillError["SOLUSDT"] = "temporary exchange error"
	svc.volumeBackfillRetryAt["SOLUSDT"] = time.Now().Add(-time.Second)
	if err := svc.EnsureVolumeProfile(context.Background(), "SOLUSDT", false); err != nil {
		t.Fatal(err)
	}
	if got := len(svc.volumeBackfillQueue); got != 1 {
		t.Fatalf("automatic retries after cooldown=%d, want 1", got)
	}
}

func TestApplyLiveAggregateTradesQueuesBackfillWithoutBlocking(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "volume-live-queue.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	originalFetch := fetchAggregateTrades
	t.Cleanup(func() { fetchAggregateTrades = originalFetch })
	called := false
	fetchAggregateTrades = func(context.Context, string, int64, time.Time, time.Time) ([]domain.AggregateTrade, error) {
		called = true
		return nil, errors.New("live path must not synchronously fetch history")
	}

	svc := &Service{
		store:               st,
		volumeContext:       context.Background(),
		volumeRequested:     map[string]struct{}{"SOLUSDT": {}},
		volumeStreamSymbols: make(map[string]struct{}),
		volumePending:       make(map[string]struct{}),
		volumeBackfillError: make(map[string]string),
		volumeBackfillQueue: make(chan string, 4),
	}
	trade := domain.AggregateTrade{ID: 100, Symbol: "SOLUSDT", Time: time.Now().UTC(), Price: 100, Quantity: 1}
	if err := svc.applyLiveAggregateTrades(context.Background(), []domain.AggregateTrade{trade}); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("live consumer fetched history synchronously")
	}
	if got := len(svc.volumeBackfillQueue); got != 1 {
		t.Fatalf("queued backfills=%d, want 1", got)
	}
	if got := <-svc.volumeBackfillQueue; got != "SOLUSDT" {
		t.Fatalf("queued symbol=%s, want SOLUSDT", got)
	}
}

func TestEnsureVolumeProfileForceQueuesCompletedSession(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "volume-force.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	session, err := volumeprofile.SessionAt(time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetVolumeProfileComplete(context.Background(), "SOLUSDT", session.Start, true); err != nil {
		t.Fatal(err)
	}
	svc := &Service{
		store:               st,
		volumeContext:       context.Background(),
		volumeRequested:     make(map[string]struct{}),
		volumeStreamSymbols: make(map[string]struct{}),
		volumePending:       make(map[string]struct{}),
		volumeBackfillError: make(map[string]string),
		volumeBackfillQueue: make(chan string, 4),
	}

	if err := svc.EnsureVolumeProfile(context.Background(), "SOLUSDT", true); err != nil {
		t.Fatal(err)
	}
	if got := len(svc.volumeBackfillQueue); got != 1 {
		t.Fatalf("forced completed-session backfills=%d, want 1", got)
	}
	if got := <-svc.volumeBackfillQueue; got != "SOLUSDT" {
		t.Fatalf("queued symbol=%s, want SOLUSDT", got)
	}
}

func TestCurrentVolumeProfileQueuesRefreshForCompletedUnavailableSession(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "volume-refresh-complete.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	session, err := volumeprofile.SessionAt(time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetVolumeProfileComplete(context.Background(), "SOLUSDT", session.Start, true); err != nil {
		t.Fatal(err)
	}
	svc := &Service{
		store:                 st,
		volumeContext:         context.Background(),
		volumeRequested:       map[string]struct{}{"SOLUSDT": {}},
		volumePending:         make(map[string]struct{}),
		volumeBackfillError:   make(map[string]string),
		volumeBackfillRetryAt: make(map[string]time.Time),
		volumeBackfillQueue:   make(chan string, 4),
	}
	profile, err := svc.currentVolumeProfile(context.Background(), "SOLUSDT")
	if err != nil {
		t.Fatal(err)
	}
	if profile.State == "ok" || len(svc.volumeBackfillQueue) != 1 {
		t.Fatalf("unavailable completed profile should queue one refresh: profile=%+v queued=%d", profile, len(svc.volumeBackfillQueue))
	}
	if symbol := <-svc.volumeBackfillQueue; symbol != "SOLUSDT" {
		t.Fatalf("queued symbol=%q, want SOLUSDT", symbol)
	}

	// Simulate the worker finishing. Repeated stale reads should honor the
	// cooldown rather than immediately replaying the same session again.
	delete(svc.volumePending, "SOLUSDT")
	if err := svc.enqueueVolumeProfileBackfill(context.Background(), "SOLUSDT", true, false); err != nil {
		t.Fatal(err)
	}
	if len(svc.volumeBackfillQueue) != 0 {
		t.Fatalf("refresh queued during cooldown: %d", len(svc.volumeBackfillQueue))
	}
	svc.volumeBackfillRetryAt["SOLUSDT"] = time.Now().Add(-time.Second)
	if err := svc.enqueueVolumeProfileBackfill(context.Background(), "SOLUSDT", true, false); err != nil {
		t.Fatal(err)
	}
	if len(svc.volumeBackfillQueue) != 1 {
		t.Fatalf("refresh after cooldown queued=%d, want one", len(svc.volumeBackfillQueue))
	}
}

func TestIncompleteVolumeBackfillIsRequeuedAfterPendingClears(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "volume-incomplete-requeue.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	session, err := volumeprofile.SessionAt(time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetVolumeProfileComplete(context.Background(), "SOLUSDT", session.Start, false); err != nil {
		t.Fatal(err)
	}
	svc := &Service{
		store:               st,
		volumeContext:       context.Background(),
		volumeRequested:     make(map[string]struct{}),
		volumeStreamSymbols: make(map[string]struct{}),
		volumePending:       make(map[string]struct{}),
		volumeBackfillError: make(map[string]string),
		volumeBackfillQueue: make(chan string, 1),
	}
	svc.requeueIncompleteVolumeProfile(context.Background(), "SOLUSDT")
	if len(svc.volumeBackfillQueue) != 1 {
		t.Fatalf("queued incomplete backfill=%d, want one", len(svc.volumeBackfillQueue))
	}
	if symbol := <-svc.volumeBackfillQueue; symbol != "SOLUSDT" {
		t.Fatalf("queued symbol=%q, want SOLUSDT", symbol)
	}
}

func TestOnDemandVolumeBackfillGeneratesProfile(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "volume-on-demand.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	originalFetch := fetchAggregateTrades
	t.Cleanup(func() { fetchAggregateTrades = originalFetch })
	tradeTime := time.Now().UTC().Add(-2 * time.Second)
	fetchAggregateTrades = func(_ context.Context, symbol string, fromID int64, _, _ time.Time) ([]domain.AggregateTrade, error) {
		if fromID == 0 {
			return []domain.AggregateTrade{{ID: 100, Symbol: symbol, Time: tradeTime, Price: 100, Quantity: 2, PriceText: "100"}}, nil
		}
		return []domain.AggregateTrade{
			{ID: 100, Symbol: symbol, Time: tradeTime, Price: 100, Quantity: 2, PriceText: "100"},
			{ID: 101, Symbol: symbol, Time: tradeTime.Add(time.Second), Price: 101, Quantity: 3, PriceText: "101"},
		}, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc := &Service{
		store:               st,
		log:                 slog.New(slog.NewTextHandler(io.Discard, nil)),
		backfillGate:        make(chan struct{}, 1),
		volumeContext:       ctx,
		volumeRequested:     make(map[string]struct{}),
		volumeStreamSymbols: make(map[string]struct{}),
		volumePending:       make(map[string]struct{}),
		volumeBackfillError: make(map[string]string),
		volumeBackfillQueue: make(chan string, 4),
		volumeLiveReady:     make(chan struct{}),
	}
	close(svc.volumeLiveReady)
	go svc.volumeBackfillLoop(ctx)

	if err := svc.EnsureVolumeProfile(ctx, "SOLUSDT", false); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		profile, profileErr := svc.VolumeProfile(ctx, "SOLUSDT")
		if profileErr != nil {
			t.Fatal(profileErr)
		}
		if profile.State == "ok" {
			if len(profile.Bins) != 24 || profile.VAL == nil || profile.VAH == nil || profile.TotalVolumeUSD <= 0 {
				t.Fatalf("profile=%+v", profile)
			}
			svc.volumeMu.Lock()
			_, found := svc.volumeStreamSymbols["SOLUSDT"]
			svc.volumeMu.Unlock()
			if !found {
				t.Fatal("on-demand symbol was not added to the volume stream")
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("on-demand volume profile did not complete")
}

func TestUnmonitoredLiquidationDoesNotQueueVolumeReplay(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "unmonitored.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc := &Service{store: st, candidates: []string{"BTCUSDT"}, volumeContext: context.Background(), volumeBackfillQueue: make(chan string, 2)}
	profile, err := svc.currentVolumeProfile(context.Background(), "BNBUSDT")
	if err != nil || profile.State != "unavailable" || len(svc.volumeBackfillQueue) != 0 || svc.volumeRequestedSymbol("BNBUSDT") {
		t.Fatalf("profile=%+v err=%v queued=%d", profile, err, len(svc.volumeBackfillQueue))
	}
}

func TestFullVolumeQueueDoesNotDropOtherLiveSymbols(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "full-queue.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now().UTC()
	session, err := volumeprofile.SessionAt(now)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ApplyAggregateTrades(ctx, "ZZZUSDT", session.Start, []domain.AggregateTrade{{ID: 1, Symbol: "ZZZUSDT", Time: now.Add(-time.Second), Price: 100, Quantity: 1, PriceText: "100"}}, true); err != nil {
		t.Fatal(err)
	}
	svc := &Service{store: st, volumeContext: ctx, volumeRequested: map[string]struct{}{"AAAUSDT": {}, "ZZZUSDT": {}}, volumeBackfillQueue: make(chan string, 1)}
	svc.volumeBackfillQueue <- "OTHERUSDT"
	err = svc.applyLiveAggregateTrades(ctx, []domain.AggregateTrade{{ID: 1, Symbol: "AAAUSDT", Time: now, Price: 1, Quantity: 1}, {ID: 2, Symbol: "ZZZUSDT", Time: now, Price: 101, Quantity: 1, PriceText: "101"}})
	if err != nil {
		t.Fatal(err)
	}
	id, _, complete, err := st.VolumeProfileCursor(ctx, "ZZZUSDT", session.Start)
	if err != nil || id != 2 || !complete {
		t.Fatalf("cursor=%d complete=%v err=%v", id, complete, err)
	}
}

func TestFullVolumeQueueCanRetryAfterCapacityReturns(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "queue-retry.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc := &Service{store: st, volumeContext: ctx, volumeBackfillQueue: make(chan string, 1)}
	svc.volumeBackfillQueue <- "OTHERUSDT"
	if err := svc.enqueueVolumeProfileBackfill(ctx, "BNBUSDT", false, false); !errors.Is(err, errVolumeBackfillQueueFull) {
		t.Fatalf("full queue error=%v, want queue full", err)
	}
	<-svc.volumeBackfillQueue
	if err := svc.enqueueVolumeProfileBackfill(ctx, "BNBUSDT", false, false); err != nil {
		t.Fatalf("retry after queue capacity returned failed: %v", err)
	}
	if symbol := <-svc.volumeBackfillQueue; symbol != "BNBUSDT" {
		t.Fatalf("queued symbol=%q, want BNBUSDT", symbol)
	}
}

func TestPageRequestPromotesQueuedVolumeReplay(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "priority.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc := &Service{store: st, volumeContext: context.Background(), volumeBackfillQueue: make(chan string, 2), volumePriorityQueue: make(chan string, 2)}
	if err := svc.enqueueVolumeProfileBackfill(context.Background(), "BNBUSDT", false, false); err != nil {
		t.Fatal(err)
	}
	if err := svc.EnsureVolumeProfile(context.Background(), "BNBUSDT", false); err != nil {
		t.Fatal(err)
	}
	if err := svc.EnsureVolumeProfile(context.Background(), "BNBUSDT", false); err != nil {
		t.Fatal(err)
	}
	if len(svc.volumePriorityQueue) != 1 || <-svc.volumePriorityQueue != "BNBUSDT" {
		t.Fatal("page request did not promote exactly once")
	}
}

func TestPageRequestRefreshesStaleCompletedCursor(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "stale-page.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now().UTC()
	session, err := volumeprofile.SessionAt(now)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ApplyAggregateTrades(ctx, "BNBUSDT", session.Start, []domain.AggregateTrade{{ID: 1, Symbol: "BNBUSDT", Time: now.Add(-valueAreaProfileMaxAge - time.Minute), Price: 100, Quantity: 1, PriceText: "100"}}, true); err != nil {
		t.Fatal(err)
	}
	svc := &Service{store: st, volumeContext: ctx, volumeBackfillQueue: make(chan string, 2), volumePriorityQueue: make(chan string, 2)}
	if err := svc.EnsureVolumeProfile(ctx, "BNBUSDT", false); err != nil {
		t.Fatal(err)
	}
	if len(svc.volumePriorityQueue) != 1 || <-svc.volumePriorityQueue != "BNBUSDT" {
		t.Fatal("stale completed profile was not queued for repair")
	}
}
