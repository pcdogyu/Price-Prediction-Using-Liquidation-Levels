package app

import (
	"context"
	"sort"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

const (
	alertBacktestWindow        = 14 * 24 * time.Hour
	alertBacktestHolding       = 15 * time.Minute
	alertBacktestInitial       = 100000.0
	alertBacktestEntryNotional = 10000.0
	alertBacktestAddOnNotional = 5000.0
)

type alertBacktestFilter struct {
	key      string
	label    string
	vwap     bool
	donchian bool
}

var alertBacktestFilters = []alertBacktestFilter{
	{key: "none", label: "无"},
	{key: "vwap", label: "VWAP", vwap: true},
	{key: "donchian", label: "Donchian", donchian: true},
	{key: "vwap_donchian", label: "VWAP + Donchian", vwap: true, donchian: true},
}

var alertBacktestSignals = []struct {
	key   string
	label string
}{
	{key: "alert", label: "预警"},
	{key: "aggressive", label: "激进"},
	{key: "steady", label: "稳健"},
}

type alertBacktestEntry struct {
	at        time.Time
	price     float64
	side      string
	additions int
	candles   []domain.Candle
}

type alertBacktestBucket struct {
	entries []alertBacktestEntry
}

// AlertBacktestForContext replays the current tenant's raw liquidation stream
// over the last two weeks under each opening-zone filter combination. It is an
// ephemeral research report: no alert history, strategy settings, orders, or
// notifications are written or changed.
func (s *Service) AlertBacktestForContext(ctx context.Context) (domain.AlertBacktestReport, error) {
	rtm, err := s.alertRuntimeForContext(ctx)
	if err != nil {
		return domain.AlertBacktestReport{}, err
	}
	to := time.Now().UTC()
	from := to.Add(-alertBacktestWindow)
	cfg, err := s.alertStrategyConfigForContext(ctx, rtm)
	if err != nil {
		return domain.AlertBacktestReport{}, err
	}
	// Match the explicit historical signal recalculation path. Peak-Decay is a
	// live timing gate and cannot be inferred reliably from sparse sampled
	// liquidations; the market/VWAP/Donchian gates remain active in the replay.
	cfg.SkipPeakDecay = true
	symbols := s.alertReplaySymbols(rtm)
	buckets := make(map[string]*alertBacktestBucket, len(alertBacktestSignals)*len(alertBacktestFilters))
	for _, signal := range alertBacktestSignals {
		for _, filter := range alertBacktestFilters {
			buckets[alertBacktestBucketKey(signal.key, filter.key)] = &alertBacktestBucket{}
		}
	}

	for _, symbol := range symbols {
		// Include the forward holding window in the candle read so the last
		// completed entries are scored without another query per signal.
		events, candles, replayErr := rtm.alertChartReplayData(ctx, symbol, cfg, to.Add(alertBacktestHolding), from)
		if replayErr != nil {
			return domain.AlertBacktestReport{}, replayErr
		}
		for _, filter := range alertBacktestFilters {
			replayCfg := cfg
			replayCfg.VWAPFilterEnabled = filter.vwap
			replayCfg.DonchianFilterEnabled = filter.donchian
			replayCfg.FilterSettingsSet = true
			view := rtm.replayAlertChartSignals(ctx, symbol, replayCfg, to, from, events, candles)
			for _, row := range view.Alerts {
				if !backtestEntryAllowed(row.FilterApplied, row.OpenAllowed) {
					continue
				}
				bucket := buckets[alertBacktestBucketKey("alert", filter.key)]
				bucket.entries = append(bucket.entries, alertBacktestEntry{at: row.TriggeredAt, price: row.ReferencePrice, side: row.TradeSide, additions: row.FollowUps, candles: candles})
			}
			for _, row := range view.Signals {
				if !backtestEntryAllowed(row.FilterApplied, row.OpenAllowed) {
					continue
				}
				bucket := buckets[alertBacktestBucketKey(row.Kind, filter.key)]
				bucket.entries = append(bucket.entries, alertBacktestEntry{at: row.CreatedAt, price: row.ReferencePrice, side: row.Side, additions: row.FollowUps, candles: candles})
			}
		}
	}

	report := domain.AlertBacktestReport{
		WindowDays:       int(alertBacktestWindow / (24 * time.Hour)),
		HoldingMinutes:   int(alertBacktestHolding / time.Minute),
		InitialEquity:    alertBacktestInitial,
		EntryNotionalUSD: alertBacktestEntryNotional,
		AddOnNotionalUSD: alertBacktestAddOnNotional,
		From:             from,
		To:               to,
		GeneratedAt:      to,
		SymbolCount:      len(symbols),
		Rows:             make([]domain.AlertBacktestRow, 0, len(alertBacktestSignals)*len(alertBacktestFilters)),
	}
	for _, signal := range alertBacktestSignals {
		for _, filter := range alertBacktestFilters {
			metrics := scoreAlertBacktest(buckets[alertBacktestBucketKey(signal.key, filter.key)].entries)
			report.Rows = append(report.Rows, backtestRow(signal.key, filter.key, metrics))
		}
	}
	return report, nil
}

func alertBacktestBucketKey(signal, filter string) string {
	return signal + "\x00" + filter
}

func backtestEntryAllowed(applied, openAllowed bool) bool {
	return !applied || openAllowed
}

type alertBacktestMetrics struct {
	trades      int
	additions   int
	wins        int
	grossProfit float64
	grossLoss   float64
	maxDrawdown float64
	finalEquity float64
}

// scoreAlertBacktest is kept separate from the replay so the trade accounting
// is deterministic and easy to unit test. Each entry carries the ordered
// candle window for its symbol, and the first close at or after the holding
// deadline is used.
func scoreAlertBacktest(entries []alertBacktestEntry) alertBacktestMetrics {
	metrics := alertBacktestMetrics{finalEquity: alertBacktestInitial}
	if len(entries) == 0 {
		return metrics
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].at.Before(entries[j].at) })
	peak := alertBacktestInitial
	for _, entry := range entries {
		if entry.at.IsZero() || entry.price <= 0 || (entry.side != "long" && entry.side != "short") {
			continue
		}
		candles := entry.candles
		if len(candles) == 0 {
			continue
		}
		exitAt := entry.at.Add(alertBacktestHolding)
		index := sort.Search(len(candles), func(i int) bool { return !candles[i].Time.Before(exitAt) })
		if index >= len(candles) || candles[index].Close <= 0 {
			continue
		}
		change := (candles[index].Close - entry.price) / entry.price
		if entry.side == "short" {
			change = -change
		}
		notional := alertBacktestEntryNotional + float64(maxInt(entry.additions, 0))*alertBacktestAddOnNotional
		pnl := change * notional
		metrics.trades++
		metrics.additions += maxInt(entry.additions, 0)
		if pnl > 0 {
			metrics.wins++
			metrics.grossProfit += pnl
		} else if pnl < 0 {
			metrics.grossLoss -= pnl
		}
		metrics.finalEquity += pnl
		if metrics.finalEquity > peak {
			peak = metrics.finalEquity
		}
		if peak > 0 {
			drawdown := (peak - metrics.finalEquity) / peak * 100
			if drawdown > metrics.maxDrawdown {
				metrics.maxDrawdown = drawdown
			}
		}
	}
	return metrics
}

func backtestRow(signal, filter string, metrics alertBacktestMetrics) domain.AlertBacktestRow {
	row := domain.AlertBacktestRow{
		Signal:      signal,
		Filter:      filter,
		Trades:      metrics.trades,
		Additions:   metrics.additions,
		MaxDrawdown: metrics.maxDrawdown,
		FinalEquity: metrics.finalEquity,
	}
	if metrics.trades > 0 {
		row.WinRate = float64(metrics.wins) / float64(metrics.trades) * 100
	}
	if metrics.grossLoss > 0 {
		row.ProfitFactor = metrics.grossProfit / metrics.grossLoss
	}
	return row
}
