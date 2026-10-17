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
	view := domain.AlertChartSignals{
		Symbol:      symbol,
		Thresholds:  cfg.Thresholds(),
		Alerts:      []domain.LiquidationAlert{},
		Signals:     []domain.TradeSignal{},
		GeneratedAt: time.Now().UTC(),
	}
	events, err := rtm.store.LiquidationEventsForSymbol(ctx, symbol, view.GeneratedAt.Add(-alertChartReplayDays*24*time.Hour), alertChartReplayLimit)
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
		collect(replay.Tick(event.EventTime))
		collect(replay.Observe(event))
	}
	// Let the final open cycle reach its current quiet/confirmation deadline so
	// a chart opened after a quiet period shows the same pending aggressive signal
	// that the live engine would emit on its next tick.
	if len(events) > 0 {
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
