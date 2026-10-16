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
	rtm := s.alerts
	now := time.Now().UTC()
	view := domain.AlertsView{
		State:       "waiting_watch_list",
		Method:      domain.AlertMonitorMethod,
		Thresholds:  rtm.engine.Thresholds(),
		Monitor:     []domain.AlertMonitor{},
		Watch:       []domain.WatchSymbol{},
		Alerts:      []domain.LiquidationAlert{},
		Signals:     []domain.TradeSignal{},
		GeneratedAt: now,
	}
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
	return s.alerts.store.LiquidationAlerts(ctx, symbol, side, limit, cursor)
}

// AlertSignals reads stored order signals.
func (s *Service) AlertSignals(ctx context.Context, symbol, kind string, limit int, cursor string) (domain.SignalPage, error) {
	return s.alerts.store.TradeSignals(ctx, symbol, kind, limit, cursor)
}

// AlertTimeline reads the unified alert and signal history.
func (s *Service) AlertTimeline(ctx context.Context, symbol, kind string, limit int, cursor string) (domain.AlertTimelinePage, error) {
	return s.alerts.store.AlertTimeline(ctx, symbol, kind, limit, cursor)
}

// TriggerAlert sends one manually requested order signal for a persisted alert
// or signal. The record is read server-side so the browser cannot alter its
// symbol, direction, or reference price.
func (s *Service) TriggerAlert(ctx context.Context, recordType, recordID string) (any, error) {
	if s.alerts == nil {
		return nil, errors.New("alert engine unavailable")
	}
	return s.alerts.triggerWebhook(ctx, recordType, recordID)
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
