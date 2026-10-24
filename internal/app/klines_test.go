package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/config"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/store"
)

func klineService(t *testing.T) *Service {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "klines.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return New(config.Config{}, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// TestAlertKlinesRejectsUnknownInterval keeps a period the chart does not offer
// from ever reaching the exchange.
func TestAlertKlinesRejectsUnknownInterval(t *testing.T) {
	svc := klineService(t)
	if _, err := svc.AlertKlines(context.Background(), "BTCUSDT", "1d", 100, time.Time{}); err == nil {
		t.Fatal("1d is not one of the chart periods")
	}
}

// TestKlineCacheMemo covers the memo the chart polling relies on: a repeated query
// is answered from memory, while another period, size, symbol or cursor is not.
func TestKlineCacheMemo(t *testing.T) {
	svc := klineService(t)
	until := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	key := klineCacheKey("DOGEUSDT", "5m", 120, until)
	if _, ok := svc.cachedKlines(key); ok {
		t.Fatal("an unseen key must miss")
	}
	svc.rememberKlines(key, domain.KlineView{Symbol: "DOGEUSDT", Interval: "5m", HasMore: true})
	cached, ok := svc.cachedKlines(key)
	if !ok || cached.Symbol != "DOGEUSDT" {
		t.Fatalf("the memoised page must come back: %+v ok=%v", cached, ok)
	}
	for other, label := range map[string]string{
		klineCacheKey("DOGEUSDT", "15m", 120, until):                 "period",
		klineCacheKey("DOGEUSDT", "5m", 121, until):                  "page size",
		klineCacheKey("SOLUSDT", "5m", 120, until):                   "symbol",
		klineCacheKey("DOGEUSDT", "5m", 120, until.Add(time.Minute)): "cursor",
	} {
		if other == key {
			t.Fatalf("a different %s must not share the cache entry", label)
		}
	}
	if klineCacheKey("DOGEUSDT", "5m", 120, until.Add(200*time.Millisecond)) != key {
		t.Fatal("sub-second cursor jitter must reuse the entry")
	}
	svc.klineMu.Lock()
	entry := svc.klineCache[key]
	entry.expiresAt = time.Now().UTC().Add(-time.Second)
	svc.klineCache[key] = entry
	svc.klineMu.Unlock()
	if _, ok := svc.cachedKlines(key); ok {
		t.Fatal("an expired page must not be served")
	}
}

// TestKlineCachePrunesExpiredEntries bounds the memo, so cycling symbols and
// cursors cannot grow it without limit.
func TestKlineCachePrunesExpiredEntries(t *testing.T) {
	svc := klineService(t)
	for index := 0; index < klineCacheSize; index++ {
		svc.rememberKlines(klineCacheKey("SYM"+strconv.Itoa(index), "1m", 100, time.Time{}), domain.KlineView{Symbol: "SYM"})
	}
	svc.klineMu.Lock()
	if len(svc.klineCache) != klineCacheSize {
		svc.klineMu.Unlock()
		t.Fatalf("the memo must hold the filled entries: %d", len(svc.klineCache))
	}
	for key, entry := range svc.klineCache {
		entry.expiresAt = time.Now().UTC().Add(-time.Second)
		svc.klineCache[key] = entry
	}
	svc.klineMu.Unlock()
	svc.rememberKlines("fresh", domain.KlineView{Symbol: "BTCUSDT"})
	svc.klineMu.Lock()
	defer svc.klineMu.Unlock()
	if len(svc.klineCache) != 1 {
		t.Fatalf("expired entries must be pruned before growing: %d", len(svc.klineCache))
	}
}

// TestAlertKlinesFallsBackToStoredMinutes keeps a transient Binance timeout
// from leaving the chart empty for BTC/ETH, whose one-minute candles are stored
// continuously by the main service.
func TestAlertKlinesFallsBackToStoredMinutes(t *testing.T) {
	svc := klineService(t)
	end := time.Date(2026, 10, 15, 0, 10, 0, 0, time.UTC)
	rows := make([]domain.Candle, 0, 10)
	for index := 0; index < 10; index++ {
		price := 2400 + float64(index)
		rows = append(rows, domain.Candle{Exchange: "binance", Symbol: "ETHUSDT", Time: end.Add(time.Duration(index-10) * time.Minute),
			Open: price, High: price + 2, Low: price - 1, Close: price + 1, VolumeUSD: 100 + float64(index)})
	}
	if err := svc.store.UpsertCandles(context.Background(), rows); err != nil {
		t.Fatal(err)
	}

	original := fetchAlertKlines
	fetchAlertKlines = func(context.Context, string, string, int, time.Time) ([]domain.MarketCandle, bool, error) {
		return nil, false, errors.New("upstream timeout")
	}
	t.Cleanup(func() { fetchAlertKlines = original })

	view, err := svc.AlertKlines(context.Background(), "ETHUSDT", "5m", 2, end)
	if err != nil {
		t.Fatal(err)
	}
	if view.Source != "binance_usdm_local_fallback" || len(view.Candles) != 2 {
		t.Fatalf("unexpected fallback view: %+v", view)
	}
	if view.Candles[0].Time != end.Add(-10*time.Minute) || view.Candles[1].Time != end.Add(-5*time.Minute) {
		t.Fatalf("fallback buckets are not aligned: %+v", view.Candles)
	}
	if view.Candles[0].Open != 2400 || view.Candles[1].Close != 2410 {
		t.Fatalf("fallback OHLC was not aggregated from stored minutes: %+v", view.Candles)
	}
}

func TestAlertKlinesRetriesTransientUpstreamTimeout(t *testing.T) {
	svc := klineService(t)
	original := fetchAlertKlines
	calls := 0
	fetchAlertKlines = func(_ context.Context, symbol, interval string, limit int, until time.Time) ([]domain.MarketCandle, bool, error) {
		calls++
		if calls == 1 {
			return nil, false, context.DeadlineExceeded
		}
		return []domain.MarketCandle{{Time: until.Add(-time.Minute), Open: 100, High: 101, Low: 99, Close: 100.5, VolumeUSD: 10}}, false, nil
	}
	t.Cleanup(func() { fetchAlertKlines = original })

	view, err := svc.AlertKlines(context.Background(), "ZECUSDT", "15m", 1, time.Date(2026, 10, 22, 3, 30, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || view.Source != "binance_usdm_klines" || len(view.Candles) != 1 {
		t.Fatalf("transient timeout was not retried: calls=%d view=%+v", calls, view)
	}
}

func TestAlertKlinesReportsBothUpstreamAndFallbackFailures(t *testing.T) {
	svc := klineService(t)
	original := fetchAlertKlines
	fetchAlertKlines = func(context.Context, string, string, int, time.Time) ([]domain.MarketCandle, bool, error) {
		return nil, false, errors.New("upstream timeout")
	}
	t.Cleanup(func() { fetchAlertKlines = original })

	_, err := svc.AlertKlines(context.Background(), "DOGEUSDT", "1m", 50, time.Time{})
	if err == nil || !strings.Contains(err.Error(), "upstream timeout") || !strings.Contains(err.Error(), "local candle fallback unavailable") {
		t.Fatalf("combined error=%v", err)
	}
}

func TestMarketLoadsUnconfiguredSymbolOnDemand(t *testing.T) {
	svc := klineService(t)
	original := fetchAlertKlines
	var calls []string
	fetchAlertKlines = func(_ context.Context, symbol, interval string, limit int, until time.Time) ([]domain.MarketCandle, bool, error) {
		calls = append(calls, symbol+":"+interval)
		step := time.Minute
		if interval == "15m" {
			step = 15 * time.Minute
		}
		if interval == "1h" {
			step = time.Hour
		}
		end := until.UTC().Truncate(step)
		rows := make([]domain.MarketCandle, 0, limit)
		for i := limit; i > 0; i-- {
			at := end.Add(-time.Duration(i) * step)
			price := 100 + float64(limit-i)
			rows = append(rows, domain.MarketCandle{Time: at, Open: price, High: price + 1, Low: price - 1, Close: price + .5, VolumeUSD: 10, ExchangeCount: 1, Complete: true})
		}
		return rows, false, nil
	}
	t.Cleanup(func() { fetchAlertKlines = original })

	view, err := svc.Market(context.Background(), "SOLUSDT", "15m", time.Date(2026, 10, 15, 1, 0, 0, 0, time.UTC), 10)
	if err != nil {
		t.Fatal(err)
	}
	if view.Source != "binance_usdm_ondemand" || len(view.Candles) != 10 || len(view.ModelSignals) != 0 || view.Summary.LastPrice <= 0 {
		t.Fatalf("unexpected on-demand view: %+v", view)
	}
	if len(calls) != 2 || calls[0] != "SOLUSDT:15m" || calls[1] != "SOLUSDT:1h" {
		t.Fatalf("unexpected upstream calls: %v", calls)
	}
	if _, err := svc.Market(context.Background(), "SOLUSDT", "15m", time.Date(2026, 10, 15, 1, 0, 0, 0, time.UTC), 10); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 {
		t.Fatalf("short market cache should avoid duplicate upstream calls: %v", calls)
	}
}

func TestUnconfiguredVolumeProfileIsUnavailable(t *testing.T) {
	svc := klineService(t)
	profile, err := svc.VolumeProfile(context.Background(), "SOLUSDT")
	if err != nil {
		t.Fatal(err)
	}
	if profile.State != "unavailable" || profile.Symbol != "SOLUSDT" {
		t.Fatalf("unexpected unconfigured volume profile: %+v", profile)
	}
}
