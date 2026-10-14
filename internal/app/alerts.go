package app

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/alert"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/exchange"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/notify"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/store"
)

// alertRuntime owns the alert engine, the top volume watch list, the persisted
// notification settings and the Telegram worker.
type alertRuntime struct {
	engine   *alert.Engine
	store    *store.Store
	log      *slog.Logger
	cfg      alert.Config
	sender   *notify.Telegram
	notifier *notify.Dispatcher

	mu       sync.RWMutex
	settings domain.NotificationSettings
	envToken string
	envChat  string
	watch    []domain.WatchSymbol
	watchAt  time.Time
	watchErr string
}

// alertNotifyCooldown suppresses repeated messages for the same symbol and signal
// kind so a burst cannot flood the chat.
const alertNotifyCooldown = 5 * time.Minute

// alertSeedOverlap is how much stored liquidation history the engine replays after
// a restart. Window plus quiet period is enough to restore the rolling totals and
// every cycle that could still produce a signal.
var alertSeedOverlap = 13 * time.Minute

func (s *Service) newAlertRuntime() *alertRuntime {
	cfg := alert.Config{
		Window:             s.cfg.AlertWindow,
		Quiet:              s.cfg.AlertQuiet,
		Confirm:            s.cfg.AlertConfirm,
		Refresh:            s.cfg.AlertRefresh,
		BTCUSD:             s.cfg.AlertThresholdBTCUSD,
		ETHUSD:             s.cfg.AlertThresholdETHUSD,
		OtherUSD:           s.cfg.AlertThresholdOtherUSD,
		FollowUpMinimumUSD: s.cfg.AlertFollowUpMinimumUSD,
		OppositeMinimumUSD: s.cfg.AlertOppositeMinimumUSD,
		TopSymbols:         s.cfg.AlertTopSymbols,
	}
	rtm := &alertRuntime{engine: alert.New(cfg), store: s.store, log: s.log, cfg: cfg, sender: notify.NewTelegram(s.cfg.TelegramAPIBase), envToken: s.cfg.TelegramBotToken, envChat: s.cfg.TelegramChatID}
	rtm.settings = rtm.defaultSettings()
	rtm.notifier = notify.NewDispatcher(rtm.sender, rtm.notifierSettings, 128, alertNotifyCooldown, s.log)
	return rtm
}

// defaultSettings builds the starting settings from the environment. A stored row
// replaces them at startup.
func (r *alertRuntime) defaultSettings() domain.NotificationSettings {
	return domain.NotificationSettings{
		TelegramEnabled:  r.envToken != "" && r.envChat != "",
		TelegramToken:    r.envToken,
		TelegramChatID:   r.envChat,
		NotifyAlerts:     true,
		NotifyAggressive: true,
		NotifySteady:     true,
	}
}

// restoreSettings loads the stored notification settings over the environment
// defaults, so a restart keeps what the alert page saved. A missing row simply keeps
// the environment defaults.
func (rtm *alertRuntime) restoreSettings(ctx context.Context) {
	stored, found, err := rtm.store.AlertSettings(ctx)
	if err != nil {
		rtm.log.Warn("load alert settings failed", "error", err)
		return
	}
	if !found {
		return
	}
	rtm.mu.Lock()
	stored.TelegramToken = strings.TrimSpace(stored.TelegramToken)
	rtm.settings = stored
	rtm.mu.Unlock()
}

// notifierSettings is the closure the Telegram worker uses, so a settings change
// applies to the next message without restarting the worker.
func (r *alertRuntime) notifierSettings() (string, string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if !r.settings.TelegramEnabled {
		return "", "", false
	}
	token, chatID := r.effectiveCredentials()
	return token, chatID, token != "" && chatID != ""
}

// effectiveCredentials resolves the stored values with the environment as fallback.
// Callers must hold r.mu.
func (r *alertRuntime) effectiveCredentials() (token, chatID string) {
	token, chatID = r.envToken, r.envChat
	if stored := strings.TrimSpace(r.settings.TelegramToken); stored != "" {
		token = stored
	}
	if stored := strings.TrimSpace(r.settings.TelegramChatID); stored != "" {
		chatID = stored
	}
	return token, chatID
}

// startAlerts restores persisted state, replays recent liquidations so open cycles
// survive a restart, and starts the refresh, tick and notification loops.
func (s *Service) startAlerts(ctx context.Context) {
	rtm := s.alerts
	rtm.restoreSettings(ctx)
	if rows, err := rtm.store.WatchSymbols(ctx); err != nil {
		rtm.log.Warn("load alert watch list failed", "error", err)
	} else if len(rows) > 0 {
		rtm.applyWatch(rowsToEntries(rows), rows, time.Time{}, "")
	}
	rtm.refreshTopSymbols(ctx)
	rtm.seedFromHistory(ctx)
	go rtm.notifier.Start(ctx)
	go rtm.topSymbolLoop(ctx)
	go rtm.tickLoop(ctx)
	go rtm.retentionLoop(ctx)
	s.log.Info("liquidation alert engine started", "watch_symbols", rtm.engine.WatchCount(), "window", rtm.cfg.Window, "quiet", rtm.cfg.Quiet, "confirm", rtm.cfg.Confirm)
}

// topSymbolLoop keeps the watch list in step with the 24 hour volume ranking.
func (rtm *alertRuntime) topSymbolLoop(ctx context.Context) {
	ticker := time.NewTicker(rtm.cfg.Refresh)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rtm.refreshTopSymbols(ctx)
		}
	}
}

// tickLoop advances the quiet and confirmation deadlines. One second granularity
// keeps a signal close to its deadline without depending on new events.
func (rtm *alertRuntime) tickLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rtm.persist(context.WithoutCancel(ctx), rtm.engine.Tick(time.Now().UTC()), true)
		}
	}
}

// retentionLoop prunes alert and signal history once an hour.
func (rtm *alertRuntime) retentionLoop(ctx context.Context) {
	prune := func() {
		cutoff := time.Now().UTC().AddDate(0, 0, -domain.AlertRetentionDays)
		if err := rtm.store.PruneAlerts(ctx, cutoff); err != nil && ctx.Err() == nil {
			rtm.log.Warn("alert retention cleanup failed", "error", err)
		}
	}
	prune()
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			prune()
		}
	}
}

// refreshTopSymbols ranks Binance USDT perpetuals by 24 hour quote volume, hands the
// result to the engine and persists it so the page still shows the list offline.
func (rtm *alertRuntime) refreshTopSymbols(ctx context.Context) {
	fetch, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	rows, err := exchange.BinanceTopUSDTPerpetuals(fetch, rtm.cfg.TopSymbols)
	if err != nil {
		rtm.log.Warn("top symbol refresh failed", "error", err)
		rtm.mu.Lock()
		rtm.watchErr = err.Error()
		rtm.mu.Unlock()
		return
	}
	now := time.Now().UTC()
	watch := make([]domain.WatchSymbol, 0, len(rows))
	for _, row := range rows {
		watch = append(watch, domain.WatchSymbol{Symbol: row.Symbol, Rank: row.Rank, QuoteVolumeUSD: row.QuoteVolumeUSD, UpdatedAt: now})
	}
	rtm.applyWatch(topSymbolEntries(rows), watch, now, "")
	storeCtx, storeCancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer storeCancel()
	if err = rtm.store.ReplaceWatchSymbols(storeCtx, watch); err != nil {
		rtm.log.Warn("persist alert watch list failed", "error", err)
	}
}

// applyWatch installs a watch list in the engine and in the runtime snapshot. An
// error keeps the previous list running, which is what a Binance outage should do.
func (rtm *alertRuntime) applyWatch(entries []alert.WatchEntry, rows []domain.WatchSymbol, at time.Time, failure string) {
	rtm.engine.Watch(entries)
	rtm.mu.Lock()
	if len(rows) > 0 {
		rtm.watch = rows
	}
	if !at.IsZero() {
		rtm.watchAt = at
	}
	rtm.watchErr = failure
	rtm.mu.Unlock()
}

// seedFromHistory replays stored liquidations so an alert cycle that was still
// waiting for its quiet period keeps working across a restart. Restored rows are
// persisted but never notified again.
func (rtm *alertRuntime) seedFromHistory(ctx context.Context) {
	since := time.Now().UTC().Add(-alertSeedOverlap)
	events, err := rtm.store.RecentLiquidationEvents(ctx, since, 60000)
	if err != nil {
		rtm.log.Warn("alert warm-up read failed", "error", err)
		return
	}
	if len(events) == 0 {
		return
	}
	res := rtm.engine.Seed(events)
	rtm.persist(context.WithoutCancel(ctx), res, false)
	if len(res.Alerts) > 0 || len(res.Signals) > 0 {
		rtm.log.Info("alert engine warm start restored", "events", len(events), "alerts", len(res.Alerts), "signals", len(res.Signals))
	}
}

// rowsToEntries converts a persisted watch list into engine entries.
func rowsToEntries(rows []domain.WatchSymbol) []alert.WatchEntry {
	out := make([]alert.WatchEntry, 0, len(rows))
	for _, row := range rows {
		out = append(out, alert.WatchEntry{Symbol: row.Symbol, Rank: row.Rank, QuoteVolumeUSD: row.QuoteVolumeUSD})
	}
	return out
}

// topSymbolEntries converts a freshly ranked list into engine entries.
func topSymbolEntries(rows []exchange.TopSymbol) []alert.WatchEntry {
	out := make([]alert.WatchEntry, 0, len(rows))
	for _, row := range rows {
		out = append(out, alert.WatchEntry{Symbol: row.Symbol, Rank: row.Rank, QuoteVolumeUSD: row.QuoteVolumeUSD})
	}
	return out
}

// observe feeds one recorded liquidation into the engine and handles the result.
func (rtm *alertRuntime) observe(ctx context.Context, event domain.LiquidationEvent) {
	rtm.persist(ctx, rtm.engine.Observe(event), true)
}

// persist stores the rows an engine call produced and hands the enabled categories
// to the Telegram worker. Rows restored during the restart warm-up are stored
// without notifying, because their moment already passed.
func (rtm *alertRuntime) persist(ctx context.Context, res alert.Result, allowNotify bool) {
	for _, item := range res.Alerts {
		created, err := rtm.store.SaveLiquidationAlert(ctx, item)
		if err != nil {
			rtm.log.Error("store liquidation alert failed", "symbol", item.Symbol, "error", err)
			continue
		}
		if created && allowNotify && rtm.categoryEnabled("alert") {
			rtm.notifier.Submit(notify.Task{Key: "alert|" + item.Symbol + "|" + item.Side, Text: alertMessage(item)})
		}
	}
	for _, item := range res.Signals {
		created, err := rtm.store.SaveTradeSignal(ctx, item)
		if err != nil {
			rtm.log.Error("store trade signal failed", "symbol", item.Symbol, "error", err)
			continue
		}
		if !created {
			// A replay after a restart reproduced a signal that is already stored.
			continue
		}
		status, message := rtm.submitSignal(item, allowNotify)
		if err = rtm.store.MarkTradeSignalNotified(ctx, item.ID, status, message); err != nil && ctx.Err() == nil {
			rtm.log.Warn("record signal notification status failed", "id", item.ID, "error", err)
		}
	}
}

// categoryEnabled reports whether a notification category may be sent.
func (rtm *alertRuntime) categoryEnabled(category string) bool {
	rtm.mu.RLock()
	defer rtm.mu.RUnlock()
	if !rtm.settings.TelegramEnabled {
		return false
	}
	switch category {
	case "alert":
		return rtm.settings.NotifyAlerts
	case domain.SignalAggressive:
		return rtm.settings.NotifyAggressive
	case domain.SignalSteady:
		return rtm.settings.NotifySteady
	default:
		return false
	}
}

// submitSignal queues a signal message and reports the decision the page shows.
// The worker coalesces bursts per symbol and kind, so a suppressed submission is a
// normal outcome rather than a failure.
func (rtm *alertRuntime) submitSignal(signal domain.TradeSignal, allowNotify bool) (string, string) {
	if !allowNotify {
		return "restored", "重启回放，不重复通知"
	}
	if !rtm.categoryEnabled(signal.Kind) {
		return "muted", "Telegram 通知未启用或该类别已关闭"
	}
	task := notify.Task{Key: signal.Kind + "|" + signal.Symbol, Text: signalMessage(signal)}
	if !rtm.notifier.Submit(task) {
		return "suppressed", "已抑制：冷却期内、未配置或队列已满"
	}
	return "queued", ""
}

// snapshotSettings returns the masked settings view. The bot token itself is never
// part of the result.
func (rtm *alertRuntime) snapshotSettings() domain.NotificationSettings {
	rtm.mu.RLock()
	defer rtm.mu.RUnlock()
	out := rtm.settings
	out.TelegramToken = ""
	token, chatID := rtm.effectiveCredentials()
	out.TelegramChatID = chatID
	out.HasToken = token != ""
	out.TokenMask = notify.MaskToken(token)
	switch {
	case strings.TrimSpace(rtm.settings.TelegramToken) != "":
		out.TokenSource = "database"
	case strings.TrimSpace(rtm.envToken) != "":
		out.TokenSource = "environment"
	default:
		out.TokenSource = "unset"
	}
	return out
}

// updateSettings validates a form submission, keeps the stored token when the form
// left it empty, persists the result and returns the masked view.
func (rtm *alertRuntime) updateSettings(ctx context.Context, in domain.NotificationSettings) (domain.NotificationSettings, error) {
	rtm.mu.Lock()
	out := domain.NotificationSettings{
		TelegramEnabled:  in.TelegramEnabled,
		NotifyAlerts:     in.NotifyAlerts,
		NotifyAggressive: in.NotifyAggressive,
		NotifySteady:     in.NotifySteady,
		TelegramChatID:   strings.TrimSpace(in.TelegramChatID),
		TelegramToken:    strings.TrimSpace(rtm.settings.TelegramToken),
	}
	if out.TelegramChatID != "" && !notify.ValidChatID(out.TelegramChatID) {
		rtm.mu.Unlock()
		return domain.NotificationSettings{}, errors.New("Telegram Chat ID 需为数字 ID 或 @频道名")
	}
	if token := strings.TrimSpace(in.TelegramToken); token != "" {
		if !notify.ValidToken(token) {
			rtm.mu.Unlock()
			return domain.NotificationSettings{}, errors.New("Telegram Token 格式应为 bot_id:secret")
		}
		out.TelegramToken = token
	}
	token, chatID := out.TelegramToken, out.TelegramChatID
	if token == "" {
		token = strings.TrimSpace(rtm.envToken)
	}
	if chatID == "" {
		chatID = strings.TrimSpace(rtm.envChat)
	}
	if out.TelegramEnabled && (!notify.ValidToken(token) || !notify.ValidChatID(chatID)) {
		rtm.mu.Unlock()
		return domain.NotificationSettings{}, errors.New("启用通知需要有效的 Telegram Token 与 Chat ID，可在下方填写或配置环境变量")
	}
	out.UpdatedAt = time.Now().UTC()
	rtm.settings = out
	rtm.mu.Unlock()
	if err := rtm.store.SaveAlertSettings(ctx, out); err != nil {
		return domain.NotificationSettings{}, err
	}
	return rtm.snapshotSettings(), nil
}

// sendTest posts one message synchronously so the settings form shows the real
// Telegram answer instead of only queueing a task.
func (rtm *alertRuntime) sendTest(ctx context.Context) error {
	token, chatID, ok := rtm.notifierSettings()
	if !ok {
		return errors.New("Telegram 通知未启用，或 Token / Chat ID 未配置")
	}
	send, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	return rtm.sender.Send(send, token, chatID, testMessage(chatID, rtm.engine.Thresholds()))
}

// notificationStatus combines the settings state with the worker counters.
func (rtm *alertRuntime) notificationStatus() domain.NotificationStatus {
	settings := rtm.snapshotSettings()
	status, depth := rtm.notifier.Snapshot()
	out := domain.NotificationStatus{Enabled: settings.TelegramEnabled, Configured: settings.HasToken && settings.TelegramChatID != "", QueueDepth: depth, Sent: status.Sent, Suppressed: status.Suppressed, Failed: status.Failed, LastError: status.LastError}
	if !status.LastAttemptAt.IsZero() {
		at := status.LastAttemptAt.UTC()
		out.LastAttemptAt = &at
	}
	if !status.LastSuccessAt.IsZero() {
		at := status.LastSuccessAt.UTC()
		out.LastSuccessAt = &at
	}
	return out
}
