package app

import (
	"context"
	"errors"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

// Alerts returns the alert page payload: live monitoring rows, the newest alerts and
// signals, the effective thresholds and the notification status.
func (s *Service) Alerts(ctx context.Context) (domain.AlertsView, error) {
	rtm, err := s.alertRuntimeForContext(ctx)
	if err != nil {
		return domain.AlertsView{}, err
	}
	now := time.Now().UTC()
	view := domain.AlertsView{
		State:       "waiting_watch_list",
		Thresholds:  rtm.engine.Thresholds(),
		Gamma:       s.alertGamma(ctx),
		Monitor:     []domain.AlertMonitor{},
		Watch:       []domain.WatchSymbol{},
		Alerts:      []domain.LiquidationAlert{},
		Signals:     []domain.TradeSignal{},
		GeneratedAt: now,
	}
	view.Method = domain.AlertMonitorMethod(view.Thresholds.WindowMinutes, view.Thresholds.RefreshMinutes, view.Thresholds.TopSymbols)
	rtm.mu.RLock()
	view.Watch = append(view.Watch, rtm.watch...)
	view.WatchError = rtm.watchErr
	if !rtm.watchAt.IsZero() {
		at := rtm.watchAt
		view.WatchUpdatedAt = &at
	}
	rtm.mu.RUnlock()
	view.Monitor = rtm.engine.Snapshot(now)
	if len(view.Monitor) > 0 {
		view.State = "ok"
		// The volume ranking is stale when two refresh cycles passed without a
		// successful fetch, which the page shows instead of hiding the fact.
		if view.WatchUpdatedAt != nil && now.Sub(*view.WatchUpdatedAt) > 2*rtm.cfg.Refresh {
			view.State = "stale"
		}
	}
	alerts, err := rtm.store.LiquidationAlerts(ctx, "", "", 30, "")
	if err != nil {
		return view, err
	}
	signals, err := rtm.store.TradeSignals(ctx, "", "", 30, "")
	if err != nil {
		return view, err
	}
	view.Alerts, view.AlertTotal = alerts.Rows, alerts.AlertCount
	view.Signals, view.SignalTotal = signals.Rows, signals.SignalCount
	view.AggressiveTotal, view.SteadyTotal = signals.AggressiveCount, signals.SteadyCount
	oldest, err := rtm.store.AlertOldestTime(ctx)
	if err != nil {
		return view, err
	}
	if !oldest.IsZero() {
		view.AvailableFrom = &oldest
	}
	view.Notifications = rtm.notificationStatus()
	return view, nil
}

// AlertHistory reads stored alerts.
func (s *Service) AlertHistory(ctx context.Context, symbol, side string, limit int, cursor string) (domain.AlertPage, error) {
	rtm, err := s.alertRuntimeForContext(ctx)
	if err != nil {
		return domain.AlertPage{}, err
	}
	page, err := rtm.store.LiquidationAlerts(ctx, symbol, side, limit, cursor)
	if err != nil {
		return page, err
	}
	page.Gamma = s.alertGammaForSymbol(ctx, symbol)
	return page, nil
}

// AlertSignals reads stored order signals.
func (s *Service) AlertSignals(ctx context.Context, symbol, kind string, limit int, cursor string) (domain.SignalPage, error) {
	rtm, err := s.alertRuntimeForContext(ctx)
	if err != nil {
		return domain.SignalPage{}, err
	}
	page, err := rtm.store.TradeSignals(ctx, symbol, kind, limit, cursor)
	if err != nil {
		return page, err
	}
	page.Gamma = s.alertGammaForSymbol(ctx, symbol)
	return page, nil
}

// AlertTimeline reads the unified alert and signal history.
func (s *Service) AlertTimeline(ctx context.Context, symbol, kind string, limit int, cursor string) (domain.AlertTimelinePage, error) {
	rtm, err := s.alertRuntimeForContext(ctx)
	if err != nil {
		return domain.AlertTimelinePage{}, err
	}
	page, err := rtm.store.AlertTimeline(ctx, symbol, kind, limit, cursor)
	if err != nil {
		return page, err
	}
	page.Gamma = s.alertGammaForSymbol(ctx, symbol)
	return page, nil
}

// AlertChartSignals replays the selected symbol with the currently saved alert
// parameters so the K-line signal layer follows the settings form immediately.
func (s *Service) AlertChartSignals(ctx context.Context, symbol string) (domain.AlertChartSignals, error) {
	rtm, err := s.alertRuntimeForContext(ctx)
	if err != nil {
		return domain.AlertChartSignals{}, err
	}
	return rtm.AlertChartSignals(ctx, symbol)
}

// AlertChartSignalsForContextWindow replays one symbol with the current
// tenant strategy and a bounded history window. It is used by the explicit
// signal-recalculation action so the chart can show the freshly replayed 48h
// result without changing the persisted audit trail.
func (s *Service) AlertChartSignalsForContextWindow(ctx context.Context, symbol string, window time.Duration) (domain.AlertChartSignals, error) {
	rtm, err := s.alertRuntimeForContext(ctx)
	if err != nil {
		return domain.AlertChartSignals{}, err
	}
	cfg, err := s.alertStrategyConfigForContext(ctx, rtm)
	if err != nil {
		return domain.AlertChartSignals{}, err
	}
	return rtm.AlertChartSignalsWithConfigSince(ctx, symbol, cfg, time.Now().UTC().Add(-window))
}

const alertSignalRecalculationWindow = 48 * time.Hour

// RecalculateAlertSignalsForContext performs an in-memory replay for every
// currently monitored pair over the last 48 hours. Historical alert and signal
// rows remain unchanged because they are the audit trail for past parameters.
func (s *Service) RecalculateAlertSignalsForContext(ctx context.Context) (domain.AlertSignalRecalculation, error) {
	rtm, err := s.alertRuntimeForContext(ctx)
	if err != nil {
		return domain.AlertSignalRecalculation{}, err
	}
	to := time.Now().UTC()
	from := to.Add(-alertSignalRecalculationWindow)
	cfg, err := s.alertStrategyConfigForContext(ctx, rtm)
	if err != nil {
		return domain.AlertSignalRecalculation{}, err
	}
	symbols := s.alertReplaySymbols(rtm)

	result := domain.AlertSignalRecalculation{
		WindowHours:    int(alertSignalRecalculationWindow / time.Hour),
		From:           from,
		To:             to,
		RecalculatedAt: to,
		SymbolCount:    len(symbols),
	}
	replayed, alertCount, signalCount, replayErr := s.replayAlertTimeline(ctx, rtm, cfg, symbols, from, to)
	if replayErr != nil {
		return domain.AlertSignalRecalculation{}, replayErr
	}
	result.AlertCount = alertCount
	result.SignalCount = signalCount
	rtm.mu.Lock()
	rtm.replayTimeline = replayed
	rtm.replayTo = to
	rtm.replayReady = true
	rtm.mu.Unlock()
	return result, nil
}

// TriggerAlert sends one manually requested order signal for a persisted alert
// or signal. The record is read server-side so the browser cannot alter its
// symbol, direction, or reference price.
func (s *Service) TriggerAlert(ctx context.Context, recordType, recordID string) (any, error) {
	rtm, err := s.alertRuntimeForContext(ctx)
	if err != nil {
		return nil, err
	}
	return rtm.triggerWebhook(ctx, recordType, recordID)
}

// NotifyAlert sends one manually requested alert or signal to Telegram. The
// record is loaded from SQLite so the browser cannot forge its contents.
func (s *Service) NotifyAlert(ctx context.Context, recordType, recordID string) (any, error) {
	rtm, err := s.alertRuntimeForContext(ctx)
	if err != nil {
		return nil, err
	}
	return rtm.notifyTelegram(ctx, recordType, recordID)
}

// AlertSettings returns the masked notification settings.
func (s *Service) AlertSettings() domain.NotificationSettings {
	return s.alerts.snapshotSettings()
}

// SaveAlertSettings validates and stores the notification settings.
func (s *Service) SaveAlertSettings(ctx context.Context, in domain.NotificationSettings) (domain.NotificationSettings, error) {
	if s.alerts == nil {
		return domain.NotificationSettings{}, errors.New("alert engine unavailable")
	}
	return s.alerts.updateSettings(ctx, in)
}

// AlertOrderSignalSettings returns the close/open switches currently used by
// the trading webhook for each live signal category.
func (s *Service) AlertOrderSignalSettings() domain.OrderSignalSettings {
	return s.alerts.snapshotOrderSignalSettings()
}

// SaveAlertOrderSignalSettings persists the live order-signal category switches.
func (s *Service) SaveAlertOrderSignalSettings(ctx context.Context, in domain.OrderSignalSettings) (domain.OrderSignalSettings, error) {
	if s.alerts == nil {
		return domain.OrderSignalSettings{}, errors.New("alert engine unavailable")
	}
	return s.alerts.updateOrderSignalSettings(ctx, in)
}

// AlertStrategySettings returns the effective editable alert parameters.
func (s *Service) AlertStrategySettings() domain.AlertStrategySettings {
	return s.alerts.snapshotStrategySettings()
}

// SaveAlertStrategySettings validates, persists and applies the editable alert
// parameters to the running engine.
func (s *Service) SaveAlertStrategySettings(ctx context.Context, in domain.AlertStrategySettings) (domain.AlertStrategySettings, error) {
	if s.alerts == nil {
		return domain.AlertStrategySettings{}, errors.New("alert engine unavailable")
	}
	return s.alerts.updateStrategySettings(ctx, in)
}

// TelegramTest sends one test message and returns the delivery error, if any.
func (s *Service) TelegramTest(ctx context.Context) error {
	if s.alerts == nil {
		return errors.New("alert engine unavailable")
	}
	return s.alerts.sendTest(ctx)
}

// AlertWatchSnapshot exposes the current watch list and its failure text, used by
// the health endpoint.
func (s *Service) AlertWatchSnapshot() ([]domain.WatchSymbol, time.Time, string) {
	rtm := s.alerts
	rtm.mu.RLock()
	defer rtm.mu.RUnlock()
	rows := make([]domain.WatchSymbol, len(rtm.watch))
	copy(rows, rtm.watch)
	return rows, rtm.watchAt, rtm.watchErr
}
