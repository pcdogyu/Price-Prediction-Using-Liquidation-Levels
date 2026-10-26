package app

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/alert"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/store"
)

var (
	errInvalidAlertTimelineLimit  = errors.New("invalid timeline limit")
	errInvalidAlertTimelineKind   = errors.New("invalid timeline kind")
	errInvalidAlertTimelineFilter = errors.New("invalid timeline filter")
)

type replayedTimelineRow struct {
	item   domain.AlertTimelineItem
	sortID string
}

// alertStrategyConfigForContext returns the current strategy for the request's
// tenant. The explicit replay and the chart must use the same effective config.
func (s *Service) alertStrategyConfigForContext(ctx context.Context, rtm *alertRuntime) (alert.Config, error) {
	cfg := rtm.engine.Config()
	if tenantID := tenantIDFromContext(ctx); tenantID != "" {
		strategy, found, err := s.store.AlertStrategySettingsForTenant(ctx, tenantID)
		if err != nil {
			return alert.Config{}, err
		}
		if found && validateAlertStrategySettings(strategy) == nil {
			cfg.Window = time.Duration(strategy.WindowMinutes) * time.Minute
			cfg.BTCUSD, cfg.ETHUSD, cfg.OtherUSD = strategy.BTCUSD, strategy.ETHUSD, strategy.OtherUSD
			cfg.VWAPRangeMinutes = strategy.VWAPRangeHours * 60
			cfg.VWAPMinDrawdown = strategy.VWAPMinDrawdownPct / 100
			cfg.VWAPDonchianFraction = strategy.VWAPDonchianPct / 100
			cfg.VWAPFilterEnabled = strategy.VWAPFilterEnabled
			cfg.DonchianFilterEnabled = strategy.DonchianFilterEnabled
			cfg.FilterSettingsSet = true
		}
	}
	return cfg, nil
}

func (s *Service) alertReplaySymbols(rtm *alertRuntime) []string {
	rtm.mu.RLock()
	watch := append([]domain.WatchSymbol(nil), rtm.watch...)
	rtm.mu.RUnlock()

	symbols := make([]string, 0, len(watch)+len(s.cfg.Symbols))
	seen := make(map[string]struct{}, len(watch)+len(s.cfg.Symbols))
	add := func(raw string) {
		symbol := strings.ToUpper(strings.TrimSpace(raw))
		if symbol == "" {
			return
		}
		if _, ok := seen[symbol]; ok {
			return
		}
		seen[symbol] = struct{}{}
		symbols = append(symbols, symbol)
	}
	for _, row := range watch {
		add(row.Symbol)
	}
	if len(symbols) == 0 {
		for _, symbol := range s.cfg.Symbols {
			add(symbol)
		}
	}
	return symbols
}

func replayedTimelineRows(view domain.AlertChartSignals) []replayedTimelineRow {
	rows := make([]replayedTimelineRow, 0, len(view.Alerts)+len(view.Signals))
	for _, alertRow := range view.Alerts {
		row := alertRow
		rows = append(rows, replayedTimelineRow{
			item:   domain.AlertTimelineItem{Type: "alert", EventAt: row.TriggeredAt, Alert: &row},
			sortID: "alert:" + row.ID,
		})
	}
	for _, signalRow := range view.Signals {
		row := signalRow
		rows = append(rows, replayedTimelineRow{
			item:   domain.AlertTimelineItem{Type: row.Kind, EventAt: row.CreatedAt, Signal: &row},
			sortID: "signal:" + row.ID,
		})
	}
	return rows
}

func sortReplayedTimelineRows(rows []replayedTimelineRow) {
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].item.EventAt.Equal(rows[j].item.EventAt) {
			return rows[i].sortID > rows[j].sortID
		}
		return rows[i].item.EventAt.After(rows[j].item.EventAt)
	})
}

func (s *Service) replayAlertTimeline(ctx context.Context, rtm *alertRuntime, cfg alert.Config, symbols []string, from, to time.Time) ([]replayedTimelineRow, int, int, error) {
	rows := make([]replayedTimelineRow, 0)
	alertCount, signalCount := 0, 0
	for _, symbol := range symbols {
		view, err := rtm.alertChartSignalsWithConfigAt(ctx, symbol, cfg, to, from)
		if err != nil {
			return nil, 0, 0, err
		}
		alertCount += len(view.Alerts)
		signalCount += len(view.Signals)
		rows = append(rows, replayedTimelineRows(view)...)
	}
	sortReplayedTimelineRows(rows)
	return rows, alertCount, signalCount, nil
}

func replayedTimelineFilterMatch(row replayedTimelineRow, filter string) bool {
	if filter == "" {
		return true
	}
	filtered := false
	gammaIgnored := false
	if row.item.Alert != nil {
		filtered = row.item.Alert.FilterApplied
		gammaIgnored = row.item.Alert.GammaIgnored
	} else if row.item.Signal != nil {
		filtered = row.item.Signal.FilterApplied
		gammaIgnored = row.item.Signal.GammaIgnored
	}
	return (filter == "filtered" && (filtered || gammaIgnored)) || (filter == "unfiltered" && !filtered && !gammaIgnored)
}

// AlertTimelineReplayed returns the last successful explicit signal replay. It
// does not touch persisted history and returns ready=false until a recalculation
// has completed for this runtime.
func (s *Service) AlertTimelineReplayed(ctx context.Context, symbol, kind string, limit int, cursor string) (page domain.AlertTimelinePage, ready bool, err error) {
	return s.AlertTimelineReplayedFiltered(ctx, symbol, kind, "", limit, cursor)
}

// AlertTimelineReplayedFiltered applies the same filter-state selection as the
// persisted timeline to the in-memory replay result.
func (s *Service) AlertTimelineReplayedFiltered(ctx context.Context, symbol, kind, filter string, limit int, cursor string) (page domain.AlertTimelinePage, ready bool, err error) {
	rtm, err := s.alertRuntimeForContext(ctx)
	if err != nil {
		return page, false, err
	}
	if limit < 1 || limit > 200 {
		return page, false, errInvalidAlertTimelineLimit
	}
	if kind != "" && kind != "alert" && kind != domain.SignalAggressive && kind != domain.SignalSteady {
		return page, false, errInvalidAlertTimelineKind
	}
	if filter != "" && filter != "filtered" && filter != "unfiltered" {
		return page, false, errInvalidAlertTimelineFilter
	}
	at, cursorID, err := store.DecodeCursor(cursor)
	if err != nil {
		return page, false, err
	}

	rtm.mu.RLock()
	ready = rtm.replayReady
	rows := append([]replayedTimelineRow(nil), rtm.replayTimeline...)
	replayedAt := rtm.replayTo
	rtm.mu.RUnlock()
	if !ready {
		return page, false, nil
	}

	needle := strings.ToUpper(strings.TrimSpace(symbol))
	selected := make([]replayedTimelineRow, 0, len(rows))
	for _, row := range rows {
		rowSymbol := ""
		if row.item.Alert != nil {
			rowSymbol = row.item.Alert.Symbol
		} else if row.item.Signal != nil {
			rowSymbol = row.item.Signal.Symbol
		}
		if needle != "" && needle != "ALL" && !strings.Contains(strings.ToUpper(rowSymbol), needle) {
			continue
		}
		if !replayedTimelineFilterMatch(row, filter) {
			continue
		}
		selected = append(selected, row)
	}

	// Counts intentionally match Store.AlertTimeline: the three type counters
	// cover the selected symbol, while TotalCount honors the kind filter.
	for _, row := range selected {
		switch row.item.Type {
		case "alert":
			page.AlertCount++
		case domain.SignalAggressive:
			page.AggressiveCount++
		case domain.SignalSteady:
			page.SteadyCount++
		}
	}
	filtered := make([]replayedTimelineRow, 0, len(selected))
	for _, row := range selected {
		if kind == "" || row.item.Type == kind {
			if !at.IsZero() && (row.item.EventAt.After(at) || (row.item.EventAt.Equal(at) && row.sortID >= cursorID)) {
				continue
			}
			filtered = append(filtered, row)
		}
	}
	page.TotalCount = page.AlertCount + page.AggressiveCount + page.SteadyCount
	switch kind {
	case "alert":
		page.TotalCount = page.AlertCount
	case domain.SignalAggressive:
		page.TotalCount = page.AggressiveCount
	case domain.SignalSteady:
		page.TotalCount = page.SteadyCount
	}
	if len(filtered) > limit {
		last := filtered[limit-1]
		page.NextCursor = store.EncodeCursor(last.item.EventAt, last.sortID)
		filtered = filtered[:limit]
	}
	page.Rows = make([]domain.AlertTimelineItem, 0, len(filtered))
	for _, row := range filtered {
		page.Rows = append(page.Rows, row.item)
	}
	page.Replayed = true
	page.ReplayedAt = &replayedAt
	page.Gamma = s.alertGammaForSymbol(ctx, symbol)
	return page, true, nil
}
