package app

import (
	"context"
	"errors"
	"fmt"
	"net"
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

// klineUpstreamAttempts absorbs a transient Binance edge timeout without
// making the browser request a second time or exposing a false invalid-symbol
// error for a valid perpetual pair.
const klineUpstreamAttempts = 2

const klineUpstreamRetryDelay = 100 * time.Millisecond

// klineCacheSize bounds the memo so an attacker cannot grow it by cycling
// through symbols and pagination cursors.
const klineCacheSize = 128

const onDemandMarketCacheTTL = 5 * time.Second

// fetchAlertKlines is replaceable in tests so the local-database fallback can
// be exercised without depending on Binance network availability.
var fetchAlertKlines = exchange.BinanceKlines

var fetchAlertDailyKlines = func(ctx context.Context, symbol, _ string, limit int, until time.Time) ([]domain.MarketCandle, bool, error) {
	return exchange.BinanceDailyKlines(ctx, symbol, limit, until)
}

type klineCacheEntry struct {
	view      domain.KlineView
	expiresAt time.Time
}

type marketCacheEntry struct {
	view      domain.MarketView
	expiresAt time.Time
}

// AlertKlines serves local candles first. Symbols without local coverage still
// use the Binance proxy as a synchronous fallback, while local symbols enqueue
// a slow background repair for missing or stale minutes.
func (s *Service) AlertKlines(ctx context.Context, symbol, interval string, limit int, until time.Time) (domain.KlineView, error) {
	if interval == "1d" {
		return domain.KlineView{}, fmt.Errorf("unsupported kline interval %q", interval)
	}
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
	if s.store != nil {
		if local, localErr := s.localAlertKlines(ctx, symbol, interval, limit, until); localErr == nil {
			end := until.UTC()
			if until.IsZero() {
				end = time.Now().UTC()
			}
			step := time.Duration(minutes) * time.Minute
			s.requestMarketSync(symbol, end.Truncate(step).Add(-time.Duration(limit)*step), end.Truncate(step).Add(step), interval)
			s.decorateKlineView(&local, symbol)
			return local, nil
		}
	}
	key := klineCacheKey(symbol, interval, limit, until)
	if cached, ok := s.cachedKlines(key); ok {
		return cached, nil
	}
	candles, hasMore, err := fetchAlertKlinesWithRetry(ctx, symbol, interval, limit, until)
	if err != nil {
		fallback, fallbackErr := s.localAlertKlines(ctx, symbol, interval, limit, until)
		if fallbackErr != nil {
			return domain.KlineView{}, fmt.Errorf("%w (local candle fallback unavailable: %v)", err, fallbackErr)
		}
		s.log.Warn("Binance K-line request failed; serving local candles", "symbol", symbol, "interval", interval, "error", err)
		s.decorateKlineView(&fallback, symbol)
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

func fetchAlertKlinesWithRetry(ctx context.Context, symbol, interval string, limit int, until time.Time) ([]domain.MarketCandle, bool, error) {
	var lastErr error
	for attempt := 0; attempt < klineUpstreamAttempts; attempt++ {
		upstreamCtx, cancel := context.WithTimeout(ctx, klineUpstreamTimeout)
		candles, hasMore, err := fetchAlertKlines(upstreamCtx, symbol, interval, limit, until)
		cancel()
		if err == nil {
			return candles, hasMore, nil
		}
		lastErr = err
		if attempt+1 >= klineUpstreamAttempts || ctx.Err() != nil || !retryableKlineError(err) {
			break
		}
		timer := time.NewTimer(klineUpstreamRetryDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
	return nil, false, lastErr
}

func retryableKlineError(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var networkErr net.Error
	return errors.As(err, &networkErr) && (networkErr.Timeout() || networkErr.Temporary())
}

// onDemandMarket serves a valid exchange pair that is not part of the local
// APP_SYMBOLS collection. Binance K-lines are fetched only for the selected
// chart; prediction and local volume-profile data are intentionally absent for
// this path.
func (s *Service) onDemandMarket(ctx context.Context, symbol, intervalName string, before time.Time, limit int) (domain.MarketView, error) {
	_, ok := marketview.ParseInterval(intervalName)
	if !ok {
		return domain.MarketView{}, errors.New("unsupported market interval")
	}
	now := time.Now().UTC()
	if before.IsZero() {
		before = now.Add(time.Minute)
	}
	cacheKey := symbol + "|" + intervalName + "|" + strconv.Itoa(limit) + "|" + before.UTC().Truncate(time.Second).Format(time.RFC3339)
	if cached, ok := s.cachedOnDemandMarket(cacheKey); ok {
		return s.attachOnDemandLiquidations(ctx, cached)
	}
	sourceInterval, multiplier := onDemandKlineSource(intervalName)
	required := limit * multiplier
	if required < 40 {
		required = 40
	}
	upstreamCtx, cancel := context.WithTimeout(ctx, klineUpstreamTimeout)
	defer cancel()
	raw, hasMore, err := fetchPagedAlertKlines(upstreamCtx, symbol, sourceInterval, required, before)
	if err != nil {
		return domain.MarketView{}, err
	}
	if len(raw) == 0 {
		return domain.MarketView{}, errors.New("market data unavailable")
	}

	// A one-hour page keeps the ticker summary useful even when the chart is
	// zoomed into a short interval. If that best-effort request fails, the chart
	// page itself is still valid and its selected-window candles are used.
	summaryRaw, _, summaryErr := fetchPagedAlertKlines(upstreamCtx, symbol, "1h", 25, before)
	if summaryErr != nil || len(summaryRaw) == 0 {
		summaryRaw = raw
	}
	rawCandles := marketCandlesToDomain(symbol, raw)
	summaryCandles := marketCandlesToDomain(symbol, summaryRaw)
	view, err := marketview.BuildPagedView(symbol, rawCandles, summaryCandles, nil, intervalName, before, limit, now, time.Time{}, true)
	if err != nil {
		return domain.MarketView{}, err
	}
	view.Source = "binance_usdm_ondemand"
	view.LiquidationMinimumUSD = domain.DefaultLiquidationMinimumUSD
	if hasMore && len(view.Candles) > 0 {
		cursor := view.Candles[0].Time.UTC()
		view.HasMore = true
		view.NextBefore = &cursor
	}
	s.rememberOnDemandMarket(cacheKey, view)
	return s.attachOnDemandLiquidations(ctx, view)
}

func (s *Service) attachOnDemandLiquidations(ctx context.Context, view domain.MarketView) (domain.MarketView, error) {
	if len(view.Candles) > 0 {
		interval, _ := marketview.ParseInterval(view.Interval)
		start := view.Candles[0].Time
		end := view.Candles[len(view.Candles)-1].Time.Add(interval)
		liquidations, truncated, liquidationErr := s.store.Liquidations(ctx, view.Symbol, start, end, 5000, domain.DefaultLiquidationMinimumUSD)
		if liquidationErr != nil {
			return domain.MarketView{}, liquidationErr
		}
		view.Liquidations = liquidations
		view.LiquidationsTruncated = truncated
	}
	return view, nil
}

func (s *Service) cachedOnDemandMarket(key string) (domain.MarketView, bool) {
	s.marketMu.Lock()
	defer s.marketMu.Unlock()
	entry, ok := s.marketCache[key]
	if !ok {
		return domain.MarketView{}, false
	}
	if time.Now().UTC().After(entry.expiresAt) {
		delete(s.marketCache, key)
		return domain.MarketView{}, false
	}
	return entry.view, true
}

func (s *Service) rememberOnDemandMarket(key string, view domain.MarketView) {
	now := time.Now().UTC()
	s.marketMu.Lock()
	defer s.marketMu.Unlock()
	if s.marketCache == nil {
		s.marketCache = make(map[string]marketCacheEntry)
	}
	if len(s.marketCache) >= 64 {
		for existing, entry := range s.marketCache {
			if now.After(entry.expiresAt) {
				delete(s.marketCache, existing)
			}
		}
	}
	s.marketCache[key] = marketCacheEntry{view: view, expiresAt: now.Add(onDemandMarketCacheTTL)}
}

func onDemandKlineSource(interval string) (string, int) {
	switch interval {
	case "2m":
		return "1m", 2
	case "24h":
		return "1d", 1
	default:
		return interval, 1
	}
}

func fetchPagedAlertKlines(ctx context.Context, symbol, interval string, required int, before time.Time) ([]domain.MarketCandle, bool, error) {
	if required < 1 {
		required = 1
	}
	all := make([]domain.MarketCandle, 0, required)
	cursor := before
	hasMore := false
	for len(all) < required {
		pageLimit := required - len(all)
		if pageLimit > exchange.KlinePageLimit {
			pageLimit = exchange.KlinePageLimit
		}
		fetch := fetchAlertKlines
		if interval == "1d" {
			fetch = fetchAlertDailyKlines
		}
		page, more, err := fetch(ctx, symbol, interval, pageLimit, cursor)
		if err != nil {
			return nil, false, err
		}
		if len(page) == 0 {
			break
		}
		all = append(page, all...)
		hasMore = more
		oldest := page[0].Time.UTC()
		if !more || oldest.IsZero() || (!cursor.IsZero() && !oldest.Before(cursor)) {
			break
		}
		cursor = oldest
	}
	return all, hasMore, nil
}

func marketCandlesToDomain(symbol string, rows []domain.MarketCandle) []domain.Candle {
	out := make([]domain.Candle, 0, len(rows))
	for _, row := range rows {
		out = append(out, domain.Candle{Exchange: "binance", Symbol: symbol, Time: row.Time, Open: row.Open, High: row.High, Low: row.Low, Close: row.Close, VolumeUSD: row.VolumeUSD})
	}
	return out
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
		HasMore: hasMore, Source: "binance_usdm_local", FetchedAt: now,
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
