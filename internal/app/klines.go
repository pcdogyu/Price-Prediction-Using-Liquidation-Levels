package app

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/exchange"
)

// klineCacheTTL keeps an open K-line tab, or several of them, from sending more
// than one upstream request per query every five seconds. The chart refreshes on
// the same cadence, so a short TTL is enough to absorb the polling.
const klineCacheTTL = 5 * time.Second

// klineCacheSize bounds the memo so an attacker cannot grow it by cycling
// through symbols and pagination cursors.
const klineCacheSize = 128

type klineCacheEntry struct {
	view      domain.KlineView
	expiresAt time.Time
}

// AlertKlines proxies Binance klines for any monitored pair. The alert strategy
// watches the 50 busiest USDT perpetuals while only APP_SYMBOLS candles are
// stored locally, and the browser is not allowed to call Binance directly, so
// the chart has to read them through here. A zero until asks for the newest
// candles; passing the oldest candle time pages further into history.
func (s *Service) AlertKlines(ctx context.Context, symbol, interval string, limit int, until time.Time) (domain.KlineView, error) {
	minutes, ok := exchange.KlineIntervalMinutes[interval]
	if !ok {
		return domain.KlineView{}, fmt.Errorf("unsupported kline interval %q", interval)
	}
	if limit < 1 {
		limit = 300
	}
	if limit > exchange.KlinePageLimit {
		limit = exchange.KlinePageLimit
	}
	key := klineCacheKey(symbol, interval, limit, until)
	if cached, ok := s.cachedKlines(key); ok {
		return cached, nil
	}
	candles, hasMore, err := exchange.BinanceKlines(ctx, symbol, interval, limit, until)
	if err != nil {
		return domain.KlineView{}, err
	}
	view := domain.KlineView{Symbol: symbol, Interval: interval, IntervalMS: int64(minutes) * int64(time.Minute/time.Millisecond),
		Candles: candles, HasMore: hasMore, Source: "binance_usdm_klines", FetchedAt: time.Now().UTC()}
	if hasMore && len(candles) > 0 {
		next := candles[0].Time.UTC()
		view.NextBefore = &next
	}
	s.rememberKlines(key, view)
	return view, nil
}

// cachedKlines returns a fresh memoised page, dropping the entry once expired.
func (s *Service) cachedKlines(key string) (domain.KlineView, bool) {
	s.klineMu.Lock()
	defer s.klineMu.Unlock()
	entry, ok := s.klineCache[key]
	if !ok {
		return domain.KlineView{}, false
	}
	if time.Now().UTC().After(entry.expiresAt) {
		delete(s.klineCache, key)
		return domain.KlineView{}, false
	}
	return entry.view, true
}

// rememberKlines stores one page and prunes stale entries before growing.
func (s *Service) rememberKlines(key string, view domain.KlineView) {
	now := time.Now().UTC()
	s.klineMu.Lock()
	defer s.klineMu.Unlock()
	if len(s.klineCache) >= klineCacheSize {
		for existing, entry := range s.klineCache {
			if now.After(entry.expiresAt) {
				delete(s.klineCache, existing)
			}
		}
	}
	s.klineCache[key] = klineCacheEntry{view: view, expiresAt: now.Add(klineCacheTTL)}
}

// klineCacheKey identifies one upstream page. The cursor is rounded to whole
// seconds so a browser that re-requests inside the same second reuses the entry
// instead of fragmenting the memo.
func klineCacheKey(symbol, interval string, limit int, until time.Time) string {
	return symbol + "|" + interval + "|" + strconv.Itoa(limit) + "|" + until.UTC().Truncate(time.Second).Format(time.RFC3339)
}
