package app

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strconv"
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
