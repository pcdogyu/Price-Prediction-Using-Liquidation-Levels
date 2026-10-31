package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/config"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/store"
)

type historyCall struct{ from, until time.Time }
type historyClient struct{ calls []historyCall }

type backgroundCandidateProviderStub struct {
	calls      int
	configured []string
	snapshot   domain.BackgroundSymbolSnapshot
	err        error
}

func TestBeginBackfillSerializesHistoricalWorkAndHonorsCancellation(t *testing.T) {
	svc := &Service{backfillGate: make(chan struct{}, 1)}
	release, ok := svc.beginBackfill(context.Background())
	if !ok {
		t.Fatal("first backfill should acquire the gate")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	acquired := make(chan bool, 1)
	go func() {
		queuedRelease, queuedOK := svc.beginBackfill(ctx)
		if queuedOK {
			queuedRelease()
		}
		acquired <- queuedOK
	}()

	select {
	case <-acquired:
		t.Fatal("second backfill acquired the gate concurrently")
	case <-time.After(20 * time.Millisecond):
	}
	cancel()
	select {
	case queuedOK := <-acquired:
		if queuedOK {
			t.Fatal("cancelled backfill should not acquire the gate")
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled backfill remained blocked")
	}
	release()
}

func (p *backgroundCandidateProviderStub) RefreshBackgroundCandidates(_ context.Context, configured []string) (domain.BackgroundSymbolSnapshot, error) {
	p.calls++
	p.configured = append([]string(nil), configured...)
	return p.snapshot, p.err
}

func TestNextDailyMaintenanceUsesBeijingTime(t *testing.T) {
	tests := []struct {
		now, want time.Time
	}{
		{time.Date(2026, 10, 14, 19, 0, 0, 0, time.UTC), time.Date(2026, 10, 14, 19, 30, 0, 0, time.UTC)},
		{time.Date(2026, 10, 14, 20, 0, 0, 0, time.UTC), time.Date(2026, 10, 15, 19, 30, 0, 0, time.UTC)},
	}
	for _, test := range tests {
		if got := nextDailyMaintenance(test.now); !got.Equal(test.want) {
			t.Errorf("nextDailyMaintenance(%v) = %v, want %v", test.now, got, test.want)
		}
	}
}

func TestNextBackgroundCandidateRefreshUsesBeijingTime(t *testing.T) {
	tests := []struct {
		now, want time.Time
	}{
		{time.Date(2026, 10, 14, 23, 40, 0, 0, time.UTC), time.Date(2026, 10, 14, 23, 50, 0, 0, time.UTC)},
		{time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 15, 23, 50, 0, 0, time.UTC)},
	}
	for _, test := range tests {
		if got := nextBackgroundCandidateRefresh(test.now); !got.Equal(test.want) {
			t.Errorf("nextBackgroundCandidateRefresh(%v) = %v, want %v", test.now, got, test.want)
		}
	}
}

func TestLoadBackgroundCandidateSnapshotPrefersValidSnapshot(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	refreshed := time.Date(2026, 10, 15, 7, 50, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60)).UTC()
	if err := st.SaveBackgroundSymbolSnapshot(context.Background(), domain.BackgroundSymbolSnapshot{
		Symbols:     []domain.WatchSymbol{{Symbol: "SOLUSDT", Rank: 1}},
		BaseCount:   1,
		TenantCount: 2,
		RefreshedAt: refreshed,
	}); err != nil {
		t.Fatal(err)
	}
	svc := New(config.Config{Symbols: []string{"BTCUSDT", "ETHUSDT", "SOLUSDT"}, BootstrapSymbols: []string{"BTCUSDT", "ETHUSDT"}, ModelPath: filepath.Join(t.TempDir(), "model.json")}, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	svc.loadBackgroundCandidateSnapshot(context.Background())
	got := svc.backgroundSymbols()
	if len(got) != 1 || got[0] != "SOLUSDT" {
		t.Fatalf("loaded candidates=%v", got)
	}
	if svc.backgroundCandidateSnapshot().TenantCount != 2 {
		t.Fatalf("loaded snapshot=%+v", svc.backgroundCandidateSnapshot())
	}
}

func TestFallbackBackgroundCandidateSnapshotUsesConfiguredUniverse(t *testing.T) {
	if BackgroundCandidateLimit != 50 {
		t.Fatalf("background candidate limit=%d, want 50", BackgroundCandidateLimit)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	configured := make([]string, 0, 60)
	for i := 0; i < 60; i++ {
		configured = append(configured, fmt.Sprintf("TOKEN%02dUSDT", i))
	}
	svc := New(config.Config{
		Symbols:          configured,
		BootstrapSymbols: []string{"BTCUSDT", "ETHUSDT", "SOLUSDT"},
		ModelPath:        filepath.Join(t.TempDir(), "model.json"),
	}, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	snapshot := svc.fallbackBackgroundCandidateSnapshot()
	if snapshot.BaseCount != BackgroundCandidateLimit || len(snapshot.Symbols) != BackgroundCandidateLimit+2 {
		t.Fatalf("fallback snapshot=%+v", snapshot)
	}
	if snapshot.Symbols[0].Symbol != "TOKEN00USDT" || snapshot.Symbols[49].Symbol != "TOKEN49USDT" {
		t.Fatalf("fallback did not use configured universe: first=%q fiftieth=%q", snapshot.Symbols[0].Symbol, snapshot.Symbols[49].Symbol)
	}
	if snapshot.Symbols[50].Symbol != "BTCUSDT" || snapshot.Symbols[51].Symbol != "ETHUSDT" {
		t.Fatalf("fallback protection symbols=%v", snapshot.Symbols[50:])
	}
}

func TestBackgroundCandidateRefreshStartsImmediatelyAndPassesConfiguredUniverse(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	configured := []string{"BTCUSDT", "ETHUSDT", "SOLUSDT", "XRPUSDT"}
	svc := New(config.Config{Symbols: configured, ModelPath: filepath.Join(t.TempDir(), "model.json")}, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	provider := &backgroundCandidateProviderStub{snapshot: domain.BackgroundSymbolSnapshot{
		Symbols:     []domain.WatchSymbol{{Symbol: "BTCUSDT", Rank: 1, QuoteVolumeUSD: 123}},
		BaseCount:   1,
		RefreshedAt: time.Now().UTC(),
	}}
	svc.SetBackgroundCandidateProvider(provider)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	svc.backgroundCandidateRefreshLoop(ctx)
	if provider.calls != 1 {
		t.Fatalf("immediate refresh calls=%d", provider.calls)
	}
	if !reflect.DeepEqual(provider.configured, configured) {
		t.Fatalf("provider configured=%v, want=%v", provider.configured, configured)
	}
	if got := svc.backgroundSymbols(); len(got) != 1 || got[0] != "BTCUSDT" {
		t.Fatalf("refreshed candidates=%v", got)
	}
}

func TestBackgroundCandidateRefreshFailureKeepsSnapshotAndExposesError(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc := New(config.Config{Symbols: []string{"BTCUSDT", "ETHUSDT"}, ModelPath: filepath.Join(t.TempDir(), "model.json")}, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	svc.applyBackgroundCandidateSnapshot(domain.BackgroundSymbolSnapshot{
		Symbols:     []domain.WatchSymbol{{Symbol: "ETHUSDT", Rank: 1, QuoteVolumeUSD: 99}},
		BaseCount:   1,
		RefreshedAt: time.Now().UTC(),
	}, false)
	svc.SetBackgroundCandidateProvider(&backgroundCandidateProviderStub{err: errors.New("ticker endpoint unavailable")})
	svc.refreshBackgroundCandidates(context.Background())
	if got := svc.backgroundSymbols(); len(got) != 1 || got[0] != "ETHUSDT" {
		t.Fatalf("failed refresh replaced candidates=%v", got)
	}
	view, err := svc.Alerts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if view.State != "watch_error" || !strings.Contains(view.WatchError, "ticker endpoint unavailable") {
		t.Fatalf("refresh error not exposed: state=%q error=%q", view.State, view.WatchError)
	}
}

func (*historyClient) Name() string { return "binance" }
func (c *historyClient) History(_ context.Context, symbol string, from, until time.Time) ([]domain.Candle, error) {
	c.calls = append(c.calls, historyCall{from, until})
	return []domain.Candle{{Exchange: c.Name(), Symbol: symbol, Time: from.Truncate(time.Minute), Open: 100, High: 101, Low: 99, Close: 100}}, nil
}
func (*historyClient) Current(context.Context, string) (domain.Candle, error) {
	return domain.Candle{}, nil
}

func TestNewRegistersOnlyBinance(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := New(config.Config{Symbols: []string{"BTCUSDT"}, ModelPath: filepath.Join(t.TempDir(), "model.json")}, st, logger)
	if len(svc.clients) != 1 || svc.clients[0].Name() != "binance" {
		t.Fatalf("registered clients=%v", svc.clients)
	}
}

func TestRecordLiquidationPersistsAndPublishes(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := New(config.Config{Symbols: []string{"BTCUSDT"}, ModelPath: filepath.Join(t.TempDir(), "model.json")}, st, logger)
	updates, cancel := svc.SubscribeLiquidations()
	defer cancel()
	now := time.Now().UTC().Truncate(time.Millisecond)
	event := domain.LiquidationEvent{ID: "live", Exchange: "binance", Symbol: "BTCUSDT", PositionSide: "long", EventTime: now, ReceivedAt: now, Price: 100, Quantity: 2, NotionalUSD: 200, Coverage: "sampled"}
	if err = svc.RecordLiquidation(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-updates:
		if got.ID != event.ID || got.NotionalUSD != event.NotionalUSD {
			t.Fatalf("published=%+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("liquidation event was not published")
	}
	events, truncated, err := st.Liquidations(context.Background(), "BTCUSDT", now.Add(-time.Second), now.Add(time.Second), 10, 0)
	if err != nil || truncated || len(events) != 1 || events[0].ID != event.ID {
		t.Fatalf("stored=%+v truncated=%v error=%v", events, truncated, err)
	}
}

func TestRealtimePriceBroadcastAndMarketOverlay(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := New(config.Config{Symbols: []string{"BTCUSDT"}, ModelPath: filepath.Join(t.TempDir(), "model.json")}, st, logger)
	now := time.Now().UTC().Truncate(time.Minute)
	if err = st.UpsertCandles(context.Background(), []domain.Candle{{Exchange: "binance", Symbol: "BTCUSDT", Time: now, Open: 100, High: 101, Low: 99, Close: 100}}); err != nil {
		t.Fatal(err)
	}
	updates, cancel := svc.SubscribePrices()
	defer cancel()
	svc.RecordAggregateTrade(domain.AggregateTrade{ID: 10, Symbol: "BTCUSDT", Time: now.Add(20 * time.Second), Price: 102, Quantity: 1})
	svc.RecordAggregateTrade(domain.AggregateTrade{ID: 11, Symbol: "BTCUSDT", Time: now.Add(21 * time.Second), Price: 103, Quantity: 1})
	svc.broadcastLatestPrices()
	select {
	case tick := <-updates:
		if tick.TradeID != 11 || tick.Price != 103 {
			t.Fatalf("price tick=%+v", tick)
		}
	case <-time.After(time.Second):
		t.Fatal("realtime price was not published")
	}
	view, err := svc.Market(context.Background(), "BTCUSDT", "1m", time.Time{}, 120)
	if err != nil {
		t.Fatal(err)
	}
	last := view.Candles[len(view.Candles)-1]
	if view.RealtimePrice == nil || view.RealtimePrice.TradeID != 11 || view.Summary.LastPrice != 103 || !view.Summary.UpdatedAt.Equal(now.Add(21*time.Second)) || last.Close != 103 || last.High != 103 || last.Complete {
		t.Fatalf("realtime market view=%+v last=%+v", view, last)
	}
	historical, err := svc.Market(context.Background(), "BTCUSDT", "1m", now.Add(time.Minute), 120)
	if err != nil || historical.RealtimePrice != nil || historical.Summary.LastPrice != 100 {
		t.Fatalf("historical market view=%+v error=%v", historical, err)
	}
}

func TestMarketHistorySharesCompleteSnapshotAcrossRequests(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc := New(config.Config{Symbols: []string{"BTCUSDT"}, ModelPath: filepath.Join(t.TempDir(), "model.json")}, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	base := time.Now().UTC().Truncate(time.Minute).Add(-3 * time.Minute)
	rows := make([]domain.Candle, 0, 3)
	for index := 0; index < 3; index++ {
		price := 100 + float64(index)
		rows = append(rows, domain.Candle{Exchange: "binance", Symbol: "BTCUSDT", Time: base.Add(time.Duration(index) * time.Minute), Open: price, High: price + 1, Low: price - 1, Close: price, VolumeUSD: 10})
	}
	if err := st.UpsertCandles(context.Background(), rows); err != nil {
		t.Fatal(err)
	}
	first, err := svc.MarketHistory(context.Background(), "BTCUSDT", "1m")
	if err != nil || len(first.Candles) != 3 || first.HasMore || first.Source != "binance_usdm_local" {
		t.Fatalf("unexpected first snapshot: len=%d has_more=%v source=%s error=%v", len(first.Candles), first.HasMore, first.Source, err)
	}
	if err := st.UpsertCandles(context.Background(), []domain.Candle{{Exchange: "binance", Symbol: "BTCUSDT", Time: base.Add(-time.Minute), Open: 99, High: 100, Low: 98, Close: 99, VolumeUSD: 10}}); err != nil {
		t.Fatal(err)
	}
	second, err := svc.MarketHistory(context.Background(), "BTCUSDT", "1m")
	if err != nil || len(second.Candles) != len(first.Candles) {
		t.Fatalf("shared snapshot was rebuilt unexpectedly: first=%d second=%d error=%v", len(first.Candles), len(second.Candles), err)
	}
}

func TestMarketHistoryUsesConfiguredBackfillWindow(t *testing.T) {
	originalFetch := fetchMarketHistoryKlines
	fetchMarketHistoryKlines = func(context.Context, string, string, time.Time, time.Time) ([]domain.MarketCandle, error) {
		return nil, errors.New("upstream unavailable")
	}
	t.Cleanup(func() { fetchMarketHistoryKlines = originalFetch })
	st, err := store.Open(filepath.Join(t.TempDir(), "history-window.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now().UTC().Truncate(time.Minute)
	rows := []domain.Candle{
		{Exchange: "binance", Symbol: "BTCUSDT", Time: now.Add(-48 * time.Hour), Open: 90, High: 91, Low: 89, Close: 90},
		{Exchange: "binance", Symbol: "BTCUSDT", Time: now.Add(-30 * time.Minute), Open: 100, High: 101, Low: 99, Close: 100},
	}
	if err := st.UpsertCandles(context.Background(), rows); err != nil {
		t.Fatal(err)
	}
	svc := New(config.Config{Symbols: []string{"BTCUSDT"}, BackfillDays: 1, ModelPath: filepath.Join(t.TempDir(), "model.json")}, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	view, err := svc.MarketHistory(context.Background(), "BTCUSDT", "1m")
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Candles) != 1 || view.Candles[0].Open != 100 {
		t.Fatalf("history window returned %#v, want only the recent candle", view.Candles)
	}
}

func TestMarketHistoryPrefersLocalHistory(t *testing.T) {
	original := fetchMarketHistoryKlines
	t.Cleanup(func() { fetchMarketHistoryKlines = original })
	var calls []string
	fetchMarketHistoryKlines = func(_ context.Context, symbol, interval string, from, until time.Time) ([]domain.MarketCandle, error) {
		calls = append(calls, symbol+"|"+interval)
		base := from.UTC().Truncate(5 * time.Minute)
		if interval == "1h" {
			base = until.UTC().Add(-time.Hour).Truncate(time.Hour)
		}
		return []domain.MarketCandle{{Time: base, Open: 100, High: 101, Low: 99, Close: 100, VolumeUSD: 10}, {Time: base.Add(5 * time.Minute), Open: 100, High: 102, Low: 98, Close: 101, VolumeUSD: 20}}, nil
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "native-history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	base := time.Now().UTC().Truncate(5 * time.Minute).Add(-15 * time.Minute)
	if err := st.UpsertCandles(context.Background(), []domain.Candle{{Exchange: "binance", Symbol: "BTCUSDT", Time: base, Open: 99, High: 100, Low: 98, Close: 99}}); err != nil {
		t.Fatal(err)
	}
	svc := New(config.Config{Symbols: []string{"BTCUSDT"}, BackfillDays: 1, ModelPath: filepath.Join(t.TempDir(), "model.json")}, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	view, err := svc.MarketHistory(context.Background(), "BTCUSDT", "5m")
	if err != nil {
		t.Fatal(err)
	}
	if view.Source != "binance_usdm_local" || len(view.Candles) != 1 || view.Candles[0].Open != 99 {
		t.Fatalf("local history was not used first: source=%s candles=%+v", view.Source, view.Candles)
	}
	if len(calls) != 0 {
		t.Fatalf("local history request unexpectedly called Binance: %v", calls)
	}
}

func TestBootstrapRequestsOnlyMissingOlderRange(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now().UTC().Truncate(time.Minute)
	rows := []domain.Candle{
		{Exchange: "binance", Symbol: "BTCUSDT", Time: now.AddDate(0, 0, -30), Open: 100, High: 101, Low: 99, Close: 100},
		{Exchange: "binance", Symbol: "BTCUSDT", Time: now, Open: 100, High: 101, Low: 99, Close: 100},
	}
	if err = st.UpsertCandles(context.Background(), rows); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := New(config.Config{Symbols: []string{"BTCUSDT"}, BackfillDays: 180, TrainingDays: 30, ModelPath: filepath.Join(t.TempDir(), "model.json")}, st, logger)
	fake := &historyClient{}
	svc.clients = nil
	svc.clients = append(svc.clients, fake)
	svc.applyBackgroundCandidateSnapshot(domain.BackgroundSymbolSnapshot{
		Symbols:   []domain.WatchSymbol{{Symbol: "BTCUSDT", Rank: 1}},
		BaseCount: 1,
	}, false)
	svc.bootstrap(context.Background())
	if len(fake.calls) != 1 {
		t.Fatalf("history calls=%#v", fake.calls)
	}
	call := fake.calls[0]
	if call.until.Sub(now.AddDate(0, 0, -30)) > time.Second || call.until.Sub(now.AddDate(0, 0, -30)) < -time.Second {
		t.Fatalf("unexpected older range end=%v", call.until)
	}
	if days := call.until.Sub(call.from).Hours() / 24; days < 149 || days > 151 {
		t.Fatalf("unexpected missing range days=%v", days)
	}
	if svc.backfillDone {
		t.Fatal("partial response must not mark the 180 day backfill complete")
	}
}
