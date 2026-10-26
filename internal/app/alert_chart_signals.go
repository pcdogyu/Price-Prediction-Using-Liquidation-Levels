package app

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/alert"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

const (
	// The chart needs enough history to cover the retained signal window while
	// leaving a safety margin for a rolling window and an open confirmation cycle.
	alertChartReplayDays  = domain.AlertRetentionDays
	alertChartReplayLimit = 200000
)

// AlertChartSignals replays this symbol's raw liquidation events with the
// currently effective strategy configuration. Persisted alerts and signals are
// intentionally not rewritten: they remain the audit trail for the parameters
// that were active when they were produced.
func (rtm *alertRuntime) AlertChartSignals(ctx context.Context, symbol string) (domain.AlertChartSignals, error) {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	cfg := rtm.engine.Config()
	return rtm.alertChartSignalsWithConfig(ctx, symbol, cfg)
}

func (rtm *alertRuntime) AlertChartSignalsWithConfig(ctx context.Context, symbol string, cfg alert.Config) (domain.AlertChartSignals, error) {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	return rtm.alertChartSignalsWithConfig(ctx, symbol, cfg)
}

func (rtm *alertRuntime) AlertChartSignalsWithConfigSince(ctx context.Context, symbol string, cfg alert.Config, since time.Time) (domain.AlertChartSignals, error) {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	generatedAt := time.Now().UTC()
	if since.IsZero() {
		since = generatedAt.Add(-alertChartReplayDays * 24 * time.Hour)
	}
	if since.After(generatedAt) {
		since = generatedAt
	}
	return rtm.alertChartSignalsWithConfigAt(ctx, symbol, cfg, generatedAt, since)
}

func (rtm *alertRuntime) alertChartSignalsWithConfig(ctx context.Context, symbol string, cfg alert.Config) (domain.AlertChartSignals, error) {
	generatedAt := time.Now().UTC()
	return rtm.alertChartSignalsWithConfigAt(ctx, symbol, cfg, generatedAt, generatedAt.Add(-alertChartReplayDays*24*time.Hour))
}

func (rtm *alertRuntime) alertChartSignalsWithConfigAt(ctx context.Context, symbol string, cfg alert.Config, generatedAt, since time.Time) (domain.AlertChartSignals, error) {
	events, candles, err := rtm.alertChartReplayData(ctx, symbol, cfg, generatedAt, since)
	if err != nil {
		return domain.AlertChartSignals{}, err
	}
	return rtm.replayAlertChartSignals(ctx, symbol, cfg, generatedAt, since, events, candles), nil
}

// alertChartReplayData reads the raw event and candle windows once. Historical
// reports replay the same source data under several filter configurations, so
// keeping the I/O separate avoids four identical SQLite range scans per pair.
func (rtm *alertRuntime) alertChartReplayData(ctx context.Context, symbol string, cfg alert.Config, generatedAt, since time.Time) ([]domain.LiquidationEvent, []domain.Candle, error) {
	if generatedAt.IsZero() {
		generatedAt = time.Now().UTC()
	}
	if since.IsZero() {
		since = generatedAt.Add(-alertChartReplayDays * 24 * time.Hour)
	}
	if since.After(generatedAt) {
		since = generatedAt
	}
	source := rtm.sourceStore
	if source == nil {
		source = rtm.store
	}
	events, err := source.LiquidationEventsForSymbol(ctx, symbol, since, alertChartReplayLimit)
	if err != nil {
		return nil, nil, err
	}
	historyMinutes := alertMarketHistoryMinutes(cfg)
	candles, err := source.CandlesRange(ctx, symbol, since.Add(-time.Duration(historyMinutes)*time.Minute), generatedAt.Add(time.Minute))
	if err != nil {
		return nil, nil, err
	}
	return events, candles, nil
}

func (rtm *alertRuntime) replayAlertChartSignals(ctx context.Context, symbol string, cfg alert.Config, generatedAt, since time.Time, events []domain.LiquidationEvent, candles []domain.Candle) domain.AlertChartSignals {
	markets := replayAlertChartMarkets(events, cfg, candles)
	return rtm.replayAlertChartSignalsWithMarkets(ctx, symbol, cfg, generatedAt, since, events, markets, candles)
}

func (rtm *alertRuntime) replayAlertChartSignalsWithMarkets(ctx context.Context, symbol string, cfg alert.Config, generatedAt, since time.Time, events []domain.LiquidationEvent, markets []alert.MarketContext, candles []domain.Candle) domain.AlertChartSignals {
	replayHours := int(generatedAt.Sub(since).Hours())
	if replayHours < 1 {
		replayHours = 1
	}
	view := domain.AlertChartSignals{
		Symbol:      symbol,
		Thresholds:  cfg.Thresholds(),
		Zone:        rtm.alertChartZone(ctx, symbol, cfg, generatedAt),
		Alerts:      []domain.LiquidationAlert{},
		Signals:     []domain.TradeSignal{},
		ReplayHours: replayHours,
		GeneratedAt: generatedAt,
	}

	replay := alert.New(cfg)
	replay.Watch([]alert.WatchEntry{{Symbol: symbol, Rank: 1}})
	alertsByID := map[string]domain.LiquidationAlert{}
	signalsByID := map[string]domain.TradeSignal{}
	collect := func(result alert.Result) {
		for _, row := range result.Alerts {
			alertsByID[row.ID] = row
		}
		for _, row := range result.Signals {
			signalsByID[row.ID] = row
		}
	}
	for index, event := range events {
		market := alert.MarketContext{}
		if index < len(markets) {
			market = markets[index]
		} else {
			market = replayLiquidationMarketContext(event, cfg, candles)
		}
		replay.UpdateMarketContext(symbol, market)
		collect(replay.Tick(event.EventTime))
		collect(replay.ObserveWithContext(event, market))
	}
	// Let the final open cycle reach its current quiet/confirmation deadline so
	// a chart opened after a quiet period shows the same pending aggressive signal
	// that the live engine would emit on its next tick.
	if len(events) > 0 {
		market := replayLiquidationMarketContext(domain.LiquidationEvent{Symbol: symbol, EventTime: generatedAt}, cfg, candles)
		replay.UpdateMarketContext(symbol, market)
		collect(replay.Tick(view.GeneratedAt))
	}

	for _, row := range alertsByID {
		view.Alerts = append(view.Alerts, row)
	}
	for _, row := range signalsByID {
		view.Signals = append(view.Signals, row)
	}
	sort.Slice(view.Alerts, func(i, j int) bool {
		if view.Alerts[i].TriggeredAt.Equal(view.Alerts[j].TriggeredAt) {
			return view.Alerts[i].ID > view.Alerts[j].ID
		}
		return view.Alerts[i].TriggeredAt.After(view.Alerts[j].TriggeredAt)
	})
	sort.Slice(view.Signals, func(i, j int) bool {
		if view.Signals[i].CreatedAt.Equal(view.Signals[j].CreatedAt) {
			return view.Signals[i].ID > view.Signals[j].ID
		}
		return view.Signals[i].CreatedAt.After(view.Signals[j].CreatedAt)
	})
	return view
}

func replayAlertChartMarkets(events []domain.LiquidationEvent, cfg alert.Config, candles []domain.Candle) []alert.MarketContext {
	markets := make([]alert.MarketContext, len(events))
	rangeMinutes := cfg.VWAPRangeMinutes
	if rangeMinutes <= 0 {
		rangeMinutes = 24 * 60
	}
	historyMinutes := alertMarketHistoryMinutes(cfg)
	window := replayDonchianWindow{}
	candleEnd := 0
	for index, event := range events {
		bucket := event.EventTime.UTC().Truncate(time.Minute)
		for candleEnd < len(candles) && !candles[candleEnd].Time.After(bucket) {
			candleEnd++
		}
		high, low, ready := window.advance(candles, candleEnd, rangeMinutes)
		start := candleEnd - historyMinutes
		if start < 0 {
			start = 0
		}
		markets[index] = buildLiquidationMarketContextWithDonchian(event, cfg, candles[start:candleEnd], high, low, ready, true)
	}
	return markets
}

type replayDonchianWindow struct {
	high  []int
	low   []int
	valid []int
	next  int
}

func (w *replayDonchianWindow) advance(candles []domain.Candle, end, rangeMinutes int) (float64, float64, bool) {
	if end <= 0 || rangeMinutes <= 0 {
		return 0, 0, false
	}
	for w.next < end {
		index := w.next
		w.next++
		candle := candles[index]
		valid := candle.High > 0 && candle.Low > 0 && candle.High >= candle.Low
		if valid {
			for len(w.high) > 0 && candles[w.high[len(w.high)-1]].High <= candle.High {
				w.high = w.high[:len(w.high)-1]
			}
			w.high = append(w.high, index)
			for len(w.low) > 0 && candles[w.low[len(w.low)-1]].Low >= candle.Low {
				w.low = w.low[:len(w.low)-1]
			}
			w.low = append(w.low, index)
			w.valid = append(w.valid, index)
		}
		start := candle.Time.Add(-time.Duration(rangeMinutes) * time.Minute)
		w.removeBefore(candles, start)
	}
	endTime := candles[end-1].Time
	w.removeBefore(candles, endTime.Add(-time.Duration(rangeMinutes)*time.Minute))
	if len(w.valid) < rangeMinutes || len(w.high) == 0 || len(w.low) == 0 {
		return 0, 0, false
	}
	return candles[w.high[0]].High, candles[w.low[0]].Low, true
}

func (w *replayDonchianWindow) removeBefore(candles []domain.Candle, start time.Time) {
	for len(w.high) > 0 && candles[w.high[0]].Time.Before(start) {
		w.high = w.high[1:]
	}
	for len(w.low) > 0 && candles[w.low[0]].Time.Before(start) {
		w.low = w.low[1:]
	}
	for len(w.valid) > 0 && candles[w.valid[0]].Time.Before(start) {
		w.valid = w.valid[1:]
	}
}

// replayLiquidationMarketContext narrows the immutable candle history before
// applying the event-price overlay. Replay events are oldest-first, but a
// binary search keeps a sparse two-week backtest from scanning the full candle
// history backwards for every liquidation.
func replayLiquidationMarketContext(event domain.LiquidationEvent, cfg alert.Config, candles []domain.Candle) alert.MarketContext {
	bucket := event.EventTime.UTC().Truncate(time.Minute)
	end := sort.Search(len(candles), func(index int) bool {
		return candles[index].Time.After(bucket)
	})
	historyMinutes := alertMarketHistoryMinutes(cfg)
	start := end - historyMinutes
	if start < 0 {
		start = 0
	}
	return buildLiquidationMarketContext(event, cfg, candles[start:end])
}

// alertChartZone calculates the same latest 1m VWAP boundaries used by the
// live radar. A missing candle window leaves the chart usable while allowing
// the browser to fall back to its selected-interval candles.
func (rtm *alertRuntime) alertChartZone(ctx context.Context, symbol string, cfg alert.Config, generatedAt time.Time) domain.AlertChartZone {
	period := cfg.VWAPPeriodMinutes
	if period <= 0 {
		period = 240
	}
	threshold := cfg.VWAPZScore
	if threshold <= 0 {
		threshold = 0.8
	}
	zone := domain.AlertChartZone{PeriodMinutes: period, ZScoreThreshold: threshold,
		RangeHours: cfg.VWAPRangeMinutes / 60, MinDrawdownPct: cfg.VWAPMinDrawdown * 100, DonchianPct: cfg.VWAPDonchianFraction * 100}
	source := rtm.sourceStore
	if source == nil {
		source = rtm.store
	}
	if source == nil {
		return zone
	}
	historyMinutes := alertMarketHistoryMinutes(cfg)
	rows, err := source.CandlesRange(ctx, symbol, generatedAt.Add(-time.Duration(historyMinutes)*time.Minute), generatedAt.Add(time.Minute))
	if err != nil || len(rows) < period {
		return zone
	}
	if len(rows) > historyMinutes {
		rows = rows[len(rows)-historyMinutes:]
	}
	price := rows[len(rows)-1].Close
	vwap, sigma, z, ok := alert.VWAPZScore(rows, period, price)
	if !ok {
		return zone
	}
	high, low, rangeReady := alert.DonchianRange(rows, cfg.VWAPRangeMinutes)
	greenUpper, redLower, overlap, boundsReady := alert.VWAPZoneBounds(cfg, vwap, sigma, high, low)
	if !rangeReady || !boundsReady {
		return zone
	}
	zone.Ready = true
	zone.VWAP, zone.Sigma = vwap, sigma
	zone.CurrentPrice, zone.CurrentZScore = price, z
	zone.DonchianHigh, zone.DonchianLow = high, low
	zone.GreenZoneUpper, zone.RedZoneLower = greenUpper, redLower
	zone.UpperOpenPrice, zone.LowerOpenPrice = redLower, greenUpper
	zone.BoundaryOverlap = overlap
	return zone
}
