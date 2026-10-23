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
	source := rtm.sourceStore
	if source == nil {
		source = rtm.store
	}
	events, err := source.LiquidationEventsForSymbol(ctx, symbol, since, alertChartReplayLimit)
	if err != nil {
		return view, err
	}
	period := cfg.VWAPPeriodMinutes
	if period <= 0 {
		period = 240
	}
	candles, err := source.CandlesRange(ctx, symbol, since.Add(-time.Duration(period+2)*time.Minute), generatedAt.Add(time.Minute))
	if err != nil {
		return view, err
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
	for _, event := range events {
		market := buildLiquidationMarketContext(event, cfg, candles)
		replay.UpdateMarketContext(symbol, market)
		collect(replay.Tick(event.EventTime))
		collect(replay.ObserveWithContext(event, market))
	}
	// Let the final open cycle reach its current quiet/confirmation deadline so
	// a chart opened after a quiet period shows the same pending aggressive signal
	// that the live engine would emit on its next tick.
	if len(events) > 0 {
		market := buildLiquidationMarketContext(domain.LiquidationEvent{Symbol: symbol, EventTime: generatedAt}, cfg, candles)
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
	return view, nil
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
	zone := domain.AlertChartZone{PeriodMinutes: period, ZScoreThreshold: threshold}
	source := rtm.sourceStore
	if source == nil {
		source = rtm.store
	}
	if source == nil {
		return zone
	}
	rows, err := source.CandlesRange(ctx, symbol, generatedAt.Add(-time.Duration(period+2)*time.Minute), generatedAt.Add(time.Minute))
	if err != nil || len(rows) < period {
		return zone
	}
	if len(rows) > period {
		rows = rows[len(rows)-period:]
	}
	price := rows[len(rows)-1].Close
	vwap, sigma, z, ok := alert.VWAPZScore(rows, period, price)
	if !ok {
		return zone
	}
	zone.Ready = true
	zone.VWAP, zone.Sigma = vwap, sigma
	zone.CurrentPrice, zone.CurrentZScore = price, z
	zone.UpperOpenPrice = vwap + threshold*sigma
	zone.LowerOpenPrice = vwap - threshold*sigma
	return zone
}
