package app

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/exchange"
	marketview "github.com/pcdogyu/price-prediction-liquidation-levels/internal/market"
)

// klineCacheTTL keeps an open K-line tab, or several of them, from sending more
// than one upstream request per query every five seconds. The chart refreshes on
// the same cadence, so a short TTL is enough to absorb the polling.
const klineCacheTTL = 5 * time.Second

// klineUpstreamTimeout fails over before an empty chart spends the full generic
// exchange-client timeout waiting on a temporarily unreachable Binance host.
const klineUpstreamTimeout = 8 * time.Second

// klineCacheSize bounds the memo so an attacker cannot grow it by cycling
// through symbols and pagination cursors.
const klineCacheSize = 128

// fetchAlertKlines is replaceable in tests so the local-database fallback can
// be exercised without depending on Binance network availability.
var fetchAlertKlines = exchange.BinanceKlines

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
	upstreamCtx, cancel := context.WithTimeout(ctx, klineUpstreamTimeout)
	candles, hasMore, err := fetchAlertKlines(upstreamCtx, symbol, interval, limit, until)
	cancel()
	if err != nil {
		fallback, fallbackErr := s.localAlertKlines(ctx, symbol, interval, limit, until)
		if fallbackErr != nil {
			return domain.KlineView{}, fmt.Errorf("%w (local candle fallback unavailable: %v)", err, fallbackErr)
		}
		s.log.Warn("Binance K-line request failed; serving local candles", "symbol", symbol, "interval", interval, "error", err)
		s.rememberKlines(key, fallback)
		return fallback, nil
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

// localAlertKlines rebuilds a Binance interval from the one-minute candles the
// service already stores for its configured core symbols. It is deliberately a
// fallback: the upstream native interval remains the normal source, while a
// transient Binance timeout no longer leaves BTC/ETH chart pages empty.
func (s *Service) localAlertKlines(ctx context.Context, symbol, interval string, limit int, until time.Time) (domain.KlineView, error) {
	minutes, ok := exchange.KlineIntervalMinutes[interval]
	if !ok {
		return domain.KlineView{}, fmt.Errorf("unsupported kline interval %q", interval)
	}
	step := time.Duration(minutes) * time.Minute
	now := time.Now().UTC()
	end := now.Truncate(step).Add(step)
	if !until.IsZero() {
		end = until.UTC().Truncate(step)
	}
	start := end.Add(-time.Duration(limit) * step)
	raw, err := s.store.CandlesRangeByExchange(ctx, "binance", symbol, start, end)
	if err != nil {
		return domain.KlineView{}, err
	}
	if len(raw) == 0 {
		return domain.KlineView{}, errors.New("no local candles in requested window")
	}
	candles := marketview.Aggregate(marketview.CompositeMinutes(raw), step, now)
	if len(candles) == 0 {
		return domain.KlineView{}, errors.New("no usable local candles in requested window")
	}
	if len(candles) > limit {
		candles = candles[len(candles)-limit:]
	}
	first, _, exists, err := s.store.CandleBounds(ctx, "binance", symbol)
	if err != nil {
		return domain.KlineView{}, err
	}
	hasMore := exists && first.Before(candles[0].Time)
	view := domain.KlineView{
		Symbol: symbol, Interval: interval, IntervalMS: int64(step / time.Millisecond), Candles: candles,
		HasMore: hasMore, Source: "binance_usdm_local_fallback", FetchedAt: now,
	}
	if hasMore {
		next := candles[0].Time.UTC()
		view.NextBefore = &next
	}
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
