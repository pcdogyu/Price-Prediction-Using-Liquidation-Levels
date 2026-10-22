package exchange

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

// binanceFuturesBase is the Binance USDⓈ-M market host. It is a package level
// variable so the kline proxy can be pointed at a local HTTP fixture in tests.
var binanceFuturesBase = "https://fapi.binance.com"

// KlineIntervals lists the periods offered by the on-demand chart loader.
// Binance does not provide 2m natively, so that period is built from 1m rows
// in BinanceKlines below.
var KlineIntervals = []string{"1m", "2m", "5m", "15m", "30m", "1h", "4h", "8h", "12h"}

// KlineIntervalMinutes maps those names to their length in minutes.
var KlineIntervalMinutes = map[string]int{"1m": 1, "2m": 2, "5m": 5, "15m": 15, "30m": 30, "1h": 60, "4h": 240, "8h": 480, "12h": 720}

// KlinePageLimit bounds one upstream request. Binance allows 1500 rows; one
// thousand keeps a page small enough to stay fast while still covering roughly
// seven hours of 1m candles and two years of 12h candles.
const KlinePageLimit = 1000

// BinanceKlines returns up to limit candles that opened strictly before until,
// oldest first, and reports whether older candles are probably still available.
// A zero until means "up to now".
func BinanceKlines(ctx context.Context, symbol, interval string, limit int, until time.Time) ([]domain.MarketCandle, bool, error) {
	if interval == "2m" {
		return binanceTwoMinuteKlines(ctx, symbol, limit, until)
	}
	return binanceKlines(ctx, symbol, interval, limit, until, false)
}

// BinanceDailyKlines is used only by the on-demand 24-hour chart period. The
// public alert-image proxy keeps its historical nine-period contract intact.
func BinanceDailyKlines(ctx context.Context, symbol string, limit int, until time.Time) ([]domain.MarketCandle, bool, error) {
	return binanceKlines(ctx, symbol, "1d", limit, until, true)
}

// binanceTwoMinuteKlines implements the chart's custom 2m period from Binance
// 1m candles. The upstream API rejects interval=2m, while the UI and alert
// history use 2m as a useful display period. Fetching whole 1m pages here also
// keeps the browser pagination contract in 2m candles.
func binanceTwoMinuteKlines(ctx context.Context, symbol string, limit int, until time.Time) ([]domain.MarketCandle, bool, error) {
	if limit < 1 {
		limit = 300
	}
	if limit > KlinePageLimit {
		limit = KlinePageLimit
	}
	step := 2 * time.Minute
	end := time.Now().UTC().Truncate(step).Add(step)
	if !until.IsZero() {
		end = until.UTC().Truncate(step)
	}

	required := limit * 2
	var raw []domain.MarketCandle
	cursor := end
	hasMore := false
	for len(raw) < required {
		pageLimit := required - len(raw)
		if pageLimit > KlinePageLimit {
			pageLimit = KlinePageLimit
		}
		page, more, err := binanceKlines(ctx, symbol, "1m", pageLimit, cursor, false)
		if err != nil {
			return nil, false, fmt.Errorf("binance klines %s 2m: %w", symbol, err)
		}
		if len(page) == 0 {
			break
		}
		raw = append(page, raw...)
		hasMore = more
		oldest := page[0].Time.UTC()
		if !more || oldest.IsZero() || !oldest.Before(cursor) {
			break
		}
		cursor = oldest
	}

	candles := aggregateKlines(raw, step, time.Now().UTC())
	if len(candles) > limit {
		candles = candles[len(candles)-limit:]
	}
	return candles, hasMore, nil
}

// aggregateKlines combines ordered lower-resolution Binance candles into a
// display candle. Missing minutes remain visible as incomplete rather than
// being silently presented as a complete two-minute bar.
func aggregateKlines(raw []domain.MarketCandle, interval time.Duration, now time.Time) []domain.MarketCandle {
	if len(raw) == 0 || interval <= 0 {
		return nil
	}
	sort.SliceStable(raw, func(i, j int) bool { return raw[i].Time.Before(raw[j].Time) })
	type bucket struct {
		c      domain.MarketCandle
		points int
	}
	buckets := make(map[int64]*bucket)
	keys := make([]int64, 0, len(raw))
	required := int(interval / time.Minute)
	for _, candle := range raw {
		key := candle.Time.UTC().Truncate(interval).Unix()
		b := buckets[key]
		if b == nil {
			b = &bucket{c: domain.MarketCandle{Time: time.Unix(key, 0).UTC(), Open: candle.Open, High: candle.High, Low: candle.Low}}
			buckets[key] = b
			keys = append(keys, key)
		}
		if b.points > 0 {
			if candle.High > b.c.High {
				b.c.High = candle.High
			}
			if candle.Low < b.c.Low {
				b.c.Low = candle.Low
			}
		}
		b.c.Close = candle.Close
		b.c.VolumeUSD += candle.VolumeUSD
		if candle.ExchangeCount > b.c.ExchangeCount {
			b.c.ExchangeCount = candle.ExchangeCount
		}
		b.points++
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	out := make([]domain.MarketCandle, 0, len(keys))
	for _, key := range keys {
		b := buckets[key]
		b.c.Complete = b.points >= required && !b.c.Time.Add(interval).After(now.UTC())
		out = append(out, b.c)
	}
	return out
}

func binanceKlines(ctx context.Context, symbol, interval string, limit int, until time.Time, allowDaily bool) ([]domain.MarketCandle, bool, error) {
	minutes, ok := KlineIntervalMinutes[interval]
	if interval == "1d" && allowDaily {
		minutes, ok = 1440, true
	}
	if !ok {
		return nil, false, fmt.Errorf("unsupported kline interval %q", interval)
	}
	if limit < 1 {
		limit = 300
	}
	if limit > KlinePageLimit {
		limit = KlinePageLimit
	}
	step := time.Duration(minutes) * time.Minute
	// The window is sized to exactly limit buckets and aligned to the interval.
	// Binance returns klines oldest first and stops at limit, so an unaligned right
	// edge would either drop the newest candle or come back one row short, and the
	// chart would read that short page as "no older data". A zero until keeps the
	// forming candle as the last bucket; a cursor ends strictly before it.
	end := time.Now().UTC().Truncate(step).Add(step)
	if !until.IsZero() {
		end = until.UTC().Truncate(step)
	}
	start := end.Add(-time.Duration(limit) * step)
	query := url.Values{"symbol": []string{symbol}, "interval": []string{interval}, "limit": []string{strconv.Itoa(limit)},
		"startTime": []string{strconv.FormatInt(start.UnixMilli(), 10)}, "endTime": []string{strconv.FormatInt(end.UnixMilli()-1, 10)}}
	var rows [][]any
	if e := newHTTP().get(ctx, binanceFuturesBase+"/fapi/v1/klines?"+query.Encode(), &rows); e != nil {
		return nil, false, fmt.Errorf("binance klines %s %s: %w", symbol, interval, e)
	}
	now := time.Now().UTC()
	out := make([]domain.MarketCandle, 0, len(rows))
	for _, row := range rows {
		if len(row) < 8 {
			continue
		}
		open := ms(row[0])
		if open.Before(start) || !open.Before(end) {
			continue
		}
		out = append(out, domain.MarketCandle{Time: open, Open: f(anyString(row[1])), High: f(anyString(row[2])), Low: f(anyString(row[3])),
			Close: f(anyString(row[4])), VolumeUSD: f(anyString(row[7])), ExchangeCount: 1, Complete: open.Add(step).Before(now)})
	}
	// A page that comes back full means the series continues before this window; a
	// short page means the candles simply start later, which is the answer the chart
	// needs to stop pulling older data. The window is sized to limit buckets, so the
	// oldest row rarely lands exactly on start: Binance floors startTime to the
	// interval, and comparing against it would silently end history too early.
	hasMore := len(rows) >= limit
	return out, hasMore, nil
}
