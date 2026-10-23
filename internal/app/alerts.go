package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/alert"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/config"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/exchange"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/notify"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/store"
)

// alertRuntime owns the alert engine, the top volume watch list, persisted
// notification settings, and the Telegram and order-signal workers.
type alertRuntime struct {
	service                                     *Service
	engine                                      *alert.Engine
	store                                       *store.Store
	sourceStore                                 *store.Store
	tenantID                                    string
	log                                         *slog.Logger
	cfg                                         alert.Config
	sender                                      *notify.Telegram
	notifier                                    *notify.Dispatcher
	webhook                                     *notify.Dispatcher
	webhookSender                               *notify.Webhook
	webhookURL, webhookTokenNonce, webhookToken string
	webhookConfig                               config.Config

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

func (s *Service) newAlertRuntime() *alertRuntime {
	cfg := alert.Config{
		Window:               s.cfg.AlertWindow,
		Quiet:                s.cfg.AlertQuiet,
		Confirm:              s.cfg.AlertConfirm,
		Refresh:              s.cfg.AlertRefresh,
		BTCUSD:               s.cfg.AlertThresholdBTCUSD,
		ETHUSD:               s.cfg.AlertThresholdETHUSD,
		OtherUSD:             s.cfg.AlertThresholdOtherUSD,
		FollowUpMinimumUSD:   s.cfg.AlertFollowUpMinimumUSD,
		OppositeMinimumUSD:   s.cfg.AlertOppositeMinimumUSD,
		TopSymbols:           s.cfg.AlertTopSymbols,
		VWAPPeriodMinutes:    s.cfg.AlertVWAPPeriodMinutes,
		VWAPZScore:           s.cfg.AlertVWAPZScore,
		VWAPRangeMinutes:     s.cfg.AlertVWAPRangeHours * 60,
		VWAPMinDrawdown:      s.cfg.AlertVWAPMinDrawdownPct / 100,
		VWAPDonchianFraction: s.cfg.AlertVWAPDonchianPct / 100,
		PeakDecayWindow:      s.cfg.AlertPeakDecayWindow,
		PeakDecayRatio:       s.cfg.AlertPeakDecayRatio,
		AbsorptionWickRatio:  s.cfg.AlertAbsorptionWickRatio,
		DeepQuotaUSD:         s.cfg.AlertDeepQuotaUSD,
		DeepQuotaMultiplier:  s.cfg.AlertDeepQuotaMultiplier,
		ATRPeriod:            s.cfg.AlertATRPeriod,
		DeepDrawdownATR:      s.cfg.AlertDeepDrawdownATR,
		MinProfitSpread:      s.cfg.AlertMinProfitSpread,
		GridStep0Price:       s.cfg.AlertGridStep0Price,
		GridStep0Pct:         s.cfg.AlertGridStep0Pct,
		GridStepMultiplier:   s.cfg.AlertGridStepMultiplier,
		GridThresholdSlope:   s.cfg.AlertGridThresholdSlope,
	}
	rtm := &alertRuntime{
		service: s,
		engine:  alert.New(cfg), store: s.store, log: s.log, cfg: cfg,
		sender:   notify.NewTelegram(s.cfg.TelegramAPIBase),
		envToken: s.cfg.TelegramBotToken, envChat: s.cfg.TelegramChatID,
		webhookURL: s.cfg.SteadyWebhookURL, webhookTokenNonce: s.cfg.SteadyWebhookTokenNonce,
		webhookToken:  s.cfg.SteadyWebhookToken,
		webhookConfig: s.cfg,
	}
	rtm.settings = rtm.defaultSettings()
	rtm.notifier = notify.NewDispatcher(rtm.sender, rtm.notifierSettings, 128, alertNotifyCooldown, s.log)
	rtm.webhookSender = notify.NewWebhook()
	rtm.webhook = notify.NewWebhookDispatcher(rtm.webhookSender, rtm.webhookSettings, 128, alertNotifyCooldown, s.log)
	return rtm
}

func (rtm *alertRuntime) webhookSettings() (string, string, bool) {
	ok := strings.TrimSpace(rtm.webhookURL) != "" && strings.TrimSpace(rtm.webhookTokenNonce) != "" && strings.TrimSpace(rtm.webhookToken) != ""
	return rtm.webhookURL, "", ok
}

// defaultSettings builds the starting settings from the environment. A stored row
// replaces them at startup.
func (r *alertRuntime) defaultSettings() domain.NotificationSettings {
	return domain.NotificationSettings{
		TelegramEnabled:      r.envToken != "" && r.envChat != "",
		TelegramToken:        r.envToken,
		TelegramChatID:       r.envChat,
		NotifyAlerts:         true,
		NotifyAggressive:     true,
		NotifySteady:         true,
		OrderAlerts:          true,
		OrderAggressive:      true,
		OrderSteady:          true,
		OrderAlertsClose:     true,
		OrderAggressiveClose: true,
		OrderSteadyClose:     true,
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

const (
	minAlertWindowMinutes = 1
	maxAlertWindowMinutes = 60
	minAlertThresholdUSD  = 1
	maxAlertThresholdUSD  = 1_000_000_000
	minVWAPRangeHours     = 12
	maxVWAPRangeHours     = 24
	minVWAPMinDrawdownPct = 0.1
	maxVWAPMinDrawdownPct = 20
	minVWAPDonchianPct    = 1
	maxVWAPDonchianPct    = 49
)

func validateAlertStrategySettings(in domain.AlertStrategySettings) error {
	if in.WindowMinutes < minAlertWindowMinutes || in.WindowMinutes > maxAlertWindowMinutes {
		return fmt.Errorf("周期需在 %d 到 %d 分钟之间", minAlertWindowMinutes, maxAlertWindowMinutes)
	}
	for name, value := range map[string]float64{"BTCUSDT": in.BTCUSD, "ETHUSDT": in.ETHUSD, "其他币对": in.OtherUSD} {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < minAlertThresholdUSD || value > maxAlertThresholdUSD {
			return fmt.Errorf("%s 阈值需在 %g 到 %g 美元之间", name, float64(minAlertThresholdUSD), float64(maxAlertThresholdUSD))
		}
	}
	if in.VWAPRangeHours < minVWAPRangeHours || in.VWAPRangeHours > maxVWAPRangeHours {
		return fmt.Errorf("VWAP Donchian 窗口需在 %d 到 %d 小时之间", minVWAPRangeHours, maxVWAPRangeHours)
	}
	if math.IsNaN(in.VWAPMinDrawdownPct) || math.IsInf(in.VWAPMinDrawdownPct, 0) || in.VWAPMinDrawdownPct < minVWAPMinDrawdownPct || in.VWAPMinDrawdownPct > maxVWAPMinDrawdownPct {
		return fmt.Errorf("VWAP 最小回撤需在 %.1f%% 到 %.1f%% 之间", minVWAPMinDrawdownPct, float64(maxVWAPMinDrawdownPct))
	}
	if math.IsNaN(in.VWAPDonchianPct) || math.IsInf(in.VWAPDonchianPct, 0) || in.VWAPDonchianPct < minVWAPDonchianPct || in.VWAPDonchianPct > maxVWAPDonchianPct {
		return fmt.Errorf("VWAP Donchian 分位需在 %d%% 到 %d%% 之间", minVWAPDonchianPct, maxVWAPDonchianPct)
	}
	return nil
}

func fillStrategyVWAPDefaults(in, current domain.AlertStrategySettings) domain.AlertStrategySettings {
	if in.VWAPRangeHours == 0 {
		in.VWAPRangeHours = current.VWAPRangeHours
	}
	if in.VWAPMinDrawdownPct == 0 {
		in.VWAPMinDrawdownPct = current.VWAPMinDrawdownPct
	}
	if in.VWAPDonchianPct == 0 {
		in.VWAPDonchianPct = current.VWAPDonchianPct
	}
	return in
}

func strategySettingsFromConfig(cfg alert.Config) domain.AlertStrategySettings {
	return domain.AlertStrategySettings{
		WindowMinutes:      int(cfg.Window / time.Minute),
		BTCUSD:             cfg.BTCUSD,
		ETHUSD:             cfg.ETHUSD,
		OtherUSD:           cfg.OtherUSD,
		VWAPRangeHours:     cfg.VWAPRangeMinutes / 60,
		VWAPMinDrawdownPct: cfg.VWAPMinDrawdown * 100,
		VWAPDonchianPct:    cfg.VWAPDonchianFraction * 100,
	}
}

// restoreStrategySettings applies the page-persisted strategy values before the
// watch list and liquidation history are replayed on startup.
func (rtm *alertRuntime) restoreStrategySettings(ctx context.Context) {
	stored, found, err := rtm.store.AlertStrategySettings(ctx)
	if err != nil {
		rtm.log.Warn("load alert strategy settings failed", "error", err)
		return
	}
	if !found {
		return
	}
	if err := validateAlertStrategySettings(stored); err != nil {
		stored = fillStrategyVWAPDefaults(stored, rtm.snapshotStrategySettings())
		if err = validateAlertStrategySettings(stored); err != nil {
			rtm.log.Warn("ignore invalid alert strategy settings", "error", err)
			return
		}
	}
	cfg := rtm.engine.Config()
	cfg.Window = time.Duration(stored.WindowMinutes) * time.Minute
	cfg.BTCUSD, cfg.ETHUSD, cfg.OtherUSD = stored.BTCUSD, stored.ETHUSD, stored.OtherUSD
	cfg.VWAPRangeMinutes = stored.VWAPRangeHours * 60
	cfg.VWAPMinDrawdown = stored.VWAPMinDrawdownPct / 100
	cfg.VWAPDonchianFraction = stored.VWAPDonchianPct / 100
	rtm.engine.UpdateConfig(cfg)
}

func (rtm *alertRuntime) snapshotStrategySettings() domain.AlertStrategySettings {
	return strategySettingsFromConfig(rtm.engine.Config())
}

// updateStrategySettings persists validated values and applies them to the live
// engine without dropping the current watch list or open cycles.
func (rtm *alertRuntime) updateStrategySettings(ctx context.Context, in domain.AlertStrategySettings) (domain.AlertStrategySettings, error) {
	in = fillStrategyVWAPDefaults(in, rtm.snapshotStrategySettings())
	if err := validateAlertStrategySettings(in); err != nil {
		return domain.AlertStrategySettings{}, err
	}
	in.UpdatedAt = time.Now().UTC()
	if err := rtm.store.SaveAlertStrategySettings(ctx, in); err != nil {
		return domain.AlertStrategySettings{}, err
	}
	cfg := rtm.engine.Config()
	cfg.Window = time.Duration(in.WindowMinutes) * time.Minute
	cfg.BTCUSD, cfg.ETHUSD, cfg.OtherUSD = in.BTCUSD, in.ETHUSD, in.OtherUSD
	cfg.VWAPRangeMinutes = in.VWAPRangeHours * 60
	cfg.VWAPMinDrawdown = in.VWAPMinDrawdownPct / 100
	cfg.VWAPDonchianFraction = in.VWAPDonchianPct / 100
	rtm.engine.UpdateConfig(cfg)
	return rtm.snapshotStrategySettings(), nil
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
	rtm.restoreStrategySettings(ctx)
	if rows, err := rtm.store.WatchSymbols(ctx); err != nil {
		rtm.log.Warn("load alert watch list failed", "error", err)
	} else if len(rows) > 0 {
		rtm.applyWatch(rowsToEntries(rows), rows, time.Time{}, "")
	}
	rtm.refreshTopSymbols(ctx)
	rtm.seedFromHistory(ctx)
	go rtm.notifier.Start(ctx)
	go rtm.webhook.Start(ctx)
	go rtm.topSymbolLoop(ctx)
	go rtm.tickLoop(ctx)
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
			now := time.Now().UTC()
			storeCtx := context.WithoutCancel(ctx)
			if rtm.service != nil {
				cfg := rtm.engine.Config()
				for _, symbol := range rtm.engine.RadarSymbols() {
					market := rtm.service.liquidationMarketContext(storeCtx, domain.LiquidationEvent{Symbol: symbol, EventTime: now}, cfg)
					rtm.engine.UpdateMarketContext(symbol, market)
				}
			}
			rtm.persist(storeCtx, rtm.engine.Tick(now), true)
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
	if rtm.service != nil && rtm.service.alerts == rtm {
		rtm.service.syncTenantAlertWatches(rows, at, failure)
	}
}

// seedFromHistory replays stored liquidations so an alert cycle that was still
// waiting for its quiet period keeps working across a restart. Restored rows are
// persisted but never notified again.
func (rtm *alertRuntime) seedFromHistory(ctx context.Context) {
	cfg := rtm.engine.Config()
	since := time.Now().UTC().Add(-(cfg.Window + cfg.Quiet))
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
	if rtm.service != nil {
		market := rtm.service.liquidationMarketContext(ctx, event, rtm.engine.Config())
		// A missing candle window is a data-readiness problem. Keep the cycle
		// ARMED and let the tick loop retry the current market context; never
		// fall back to the legacy threshold-first signal path.
		rtm.persist(ctx, rtm.engine.ObserveWithContext(event, market), true)
		return
	}
	rtm.persist(ctx, rtm.engine.ObserveWithContext(event, alert.MarketContext{}), true)
}

// persist stores the rows an engine call produced and hands the enabled categories
// to the Telegram worker. Rows restored during the restart warm-up are stored
// without notifying, because their moment already passed.
func (rtm *alertRuntime) persist(ctx context.Context, res alert.Result, allowNotify bool) {
	for _, item := range res.Alerts {
		if allowNotify {
			// Capture the live decision once when the alert is produced. The store
			// preserves it across later cycle updates and history never consults
			// the current Gamma snapshot again.
			item.GammaIgnored = rtm.gammaIgnored(item.Symbol)
		}
		created, err := rtm.store.SaveLiquidationAlert(ctx, item)
		if err != nil {
			rtm.log.Error("store liquidation alert failed", "symbol", item.Symbol, "error", err)
			continue
		}
		if created {
			rtm.submitAlertWebhook(item, allowNotify)
		}
		if created && allowNotify && rtm.categoryEnabled("alert") {
			textTask := notify.Task{Key: "alert|" + item.Symbol + "|" + item.Side, Text: alertMessage(item)}
			if rtm.notifier.Submit(textTask) {
				rtm.submitChart(item.Symbol, item.Side, item.TriggeredAt, item.ReferencePrice)
			}
		}
	}
	for _, item := range res.Signals {
		if rtm.tenantID != "" {
			item.TenantID = rtm.tenantID
		}
		if allowNotify {
			item.GammaIgnored = rtm.gammaIgnored(item.Symbol)
		}
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
		rtm.submitSignalWebhook(item, allowNotify)
		if err = rtm.store.MarkTradeSignalNotified(ctx, item.ID, status, message); err != nil && ctx.Err() == nil {
			rtm.log.Warn("record signal notification status failed", "id", item.ID, "error", err)
		}
	}
}

func (rtm *alertRuntime) submitSignalWebhook(signal domain.TradeSignal, allowNotify bool) {
	if !allowNotify || (signal.Kind != domain.SignalAggressive && signal.Kind != domain.SignalSteady) {
		return
	}
	if (!signal.FilterApplied || signal.CloseAllowed) && !rtm.submitPrecloseWebhook(signal) {
		return
	}
	if signal.FilterApplied && !signal.OpenAllowed {
		return
	}
	if !rtm.orderSignalEnabled(signal.Kind) {
		return
	}
	payload, err := buildSignalWebhookPayload(signal, rtm.serviceConfig())
	if err != nil {
		rtm.log.Warn("signal webhook payload rejected", "kind", signal.Kind, "symbol", signal.Symbol, "error", err)
		return
	}
	if !rtm.webhook.Submit(notify.Task{Key: "signal-webhook|" + signal.Kind + "|" + signal.Symbol, Text: string(payload)}) {
		rtm.log.Warn("signal webhook delivery suppressed", "kind", signal.Kind, "symbol", signal.Symbol)
	}
}

// submitAlertWebhook sends the initial threshold alert as a direction switch:
// first close the opposite position, then queue the opening signal. The two
// requests share the same FIFO webhook worker so the receiver sees the preclose
// before the entry. The TVBot /tvorder entry point applies the tenant's Gamma
// policy consistently for this and every other webhook source.
func (rtm *alertRuntime) submitAlertWebhook(alertRow domain.LiquidationAlert, allowNotify bool) {
	if !allowNotify {
		return
	}
	signal := domain.TradeSignal{
		ID: alertRow.ID, AlertID: alertRow.ID, Symbol: alertRow.Symbol,
		TenantID: rtm.tenantID,
		Kind:     "alert", Side: alertRow.TradeSide, LiquidationSide: alertRow.Side,
		CreatedAt: alertRow.TriggeredAt, ReferencePrice: alertRow.ReferencePrice,
		AlertNotionalUSD: alertRow.NotionalUSD, ThresholdUSD: alertRow.ThresholdUSD,
		WindowMinutes: alertRow.WindowMinutes, FollowUps: alertRow.FollowUps,
	}
	if alertRow.FilterApplied {
		signal.FilterApplied = true
		signal.OpenAllowed = alertRow.OpenAllowed
		signal.CloseAllowed = alertRow.CloseAllowed
		signal.FilterReason = alertRow.FilterReason
		signal.MinProfitSpread = alertRow.MinProfitSpread
		if !alertRow.CloseAllowed && !alertRow.OpenAllowed {
			return
		}
	}
	if (!signal.FilterApplied || signal.CloseAllowed) && !rtm.submitPrecloseWebhook(signal) {
		return
	}
	if signal.FilterApplied && !signal.OpenAllowed {
		return
	}
	if !rtm.orderSignalEnabled(signal.Kind) {
		return
	}
	payload, err := buildAlertWebhookPayload(signal, rtm.serviceConfig())
	if err != nil {
		rtm.log.Warn("alert webhook payload rejected", "kind", signal.Kind, "symbol", signal.Symbol, "error", err)
		return
	}
	if !rtm.webhook.Submit(notify.Task{Key: "signal-webhook|alert|" + signal.Symbol, Text: string(payload)}) {
		rtm.log.Warn("alert webhook delivery suppressed", "kind", signal.Kind, "symbol", signal.Symbol)
	}
}

// submitPrecloseWebhook sends a close-only request before any optional opening
// request. A failed or suppressed preclose prevents the subsequent open from
// being queued, so a queue/backpressure issue cannot turn into an unprotected
// direction switch.
func (rtm *alertRuntime) submitPrecloseWebhook(signal domain.TradeSignal) bool {
	if !rtm.orderCloseEnabled(signal.Kind) {
		return true
	}
	if _, _, ok := rtm.webhookSettings(); !ok {
		return false
	}
	payload, err := buildPrecloseWebhookPayload(signal, rtm.serviceConfig())
	if err != nil {
		rtm.log.Warn("preclose webhook payload rejected", "kind", signal.Kind, "symbol", signal.Symbol, "error", err)
		return false
	}
	if !rtm.webhook.Submit(notify.Task{Key: "position-preclose|" + signal.Kind + "|" + signal.Symbol, Text: string(payload)}) {
		rtm.log.Warn("preclose webhook delivery suppressed", "kind", signal.Kind, "symbol", signal.Symbol)
		return false
	}
	return true
}

func (rtm *alertRuntime) serviceConfig() config.Config {
	cfg := rtm.webhookConfig
	if rtm.service != nil {
		if target, ok := rtm.service.webhookTargetForTenant(rtm.tenantID); ok {
			cfg.SteadyWebhookTargetExchange = target.Exchange
			cfg.SteadyWebhookAPIID = target.APIID
			cfg.SteadyWebhookTradeEnv = target.TradeEnv
			if target.Token != "" {
				cfg.SteadyWebhookToken = target.Token
			}
		}
	}
	return cfg
}

type manualTriggerResult struct {
	RecordType     string `json:"record_type"`
	RecordID       string `json:"record_id"`
	Symbol         string `json:"symbol"`
	Side           string `json:"side"`
	Status         string `json:"status"`
	TargetExchange string `json:"target_exchange"`
	TradeEnv       string `json:"trade_env"`
	APIID          string `json:"api_id,omitempty"`
}

type manualNotifyResult struct {
	RecordType string `json:"record_type"`
	RecordID   string `json:"record_id"`
	Symbol     string `json:"symbol"`
	Side       string `json:"side"`
	Status     string `json:"status"`
}

func (rtm *alertRuntime) triggerWebhook(ctx context.Context, recordType, recordID string) (manualTriggerResult, error) {
	if _, _, ok := rtm.webhookSettings(); !ok {
		return manualTriggerResult{}, errors.New("steady webhook is not configured")
	}
	var signal domain.TradeSignal
	switch recordType {
	case "alert":
		row, err := rtm.store.LiquidationAlertByID(ctx, recordID)
		if err != nil {
			return manualTriggerResult{}, err
		}
		signal = domain.TradeSignal{
			ID: row.ID, AlertID: row.ID, TenantID: rtm.tenantID, Symbol: row.Symbol, Side: row.TradeSide,
			LiquidationSide: row.Side, CreatedAt: time.Now().UTC(), ReferencePrice: row.ReferencePrice,
			AlertNotionalUSD: row.NotionalUSD, ThresholdUSD: row.ThresholdUSD, WindowMinutes: row.WindowMinutes,
		}
	case domain.SignalAggressive, domain.SignalSteady:
		row, err := rtm.store.TradeSignalByID(ctx, recordID)
		if err != nil {
			return manualTriggerResult{}, err
		}
		signal = row
		signal.CreatedAt = time.Now().UTC()
	default:
		return manualTriggerResult{}, errors.New("record type must be alert, aggressive or steady")
	}
	if signal.Symbol == "" || signal.ReferencePrice <= 0 {
		return manualTriggerResult{}, errors.New("record has no valid symbol or reference price")
	}
	payload, err := buildManualWebhookPayload(signal, rtm.serviceConfig())
	if err != nil {
		return manualTriggerResult{}, err
	}
	if err = rtm.webhookSender.Send(ctx, rtm.webhookURL, "", string(payload)); err != nil {
		return manualTriggerResult{}, err
	}
	cfg := rtm.serviceConfig()
	return manualTriggerResult{
		RecordType: recordType, RecordID: recordID, Symbol: signal.Symbol, Side: signal.Side, Status: "accepted",
		TargetExchange: cfg.SteadyWebhookTargetExchange, TradeEnv: cfg.SteadyWebhookTradeEnv, APIID: cfg.SteadyWebhookAPIID,
	}, nil
}

func (rtm *alertRuntime) notifyTelegram(ctx context.Context, recordType, recordID string) (manualNotifyResult, error) {
	token, chatID, ok := rtm.notifierSettings()
	if !ok {
		return manualNotifyResult{}, errors.New("Telegram 通知未启用，或 Token / Chat ID 未配置")
	}
	return rtm.notifyTelegramUsing(ctx, recordType, recordID, token, chatID)
}

func (rtm *alertRuntime) notifyTelegramWithSettings(ctx context.Context, recordType, recordID, tenantID string) (manualNotifyResult, error) {
	settings, found, err := rtm.tenantConfigStore().AlertSettingsForTenant(ctx, tenantID)
	if err != nil {
		return manualNotifyResult{}, err
	}
	if !found {
		settings = rtm.defaultSettings()
	}
	token, chatID, ok := rtm.credentialsForSettings(settings)
	if !ok {
		return manualNotifyResult{}, errors.New("Telegram 通知未启用，或 Token / Chat ID 未配置")
	}
	return rtm.notifyTelegramUsing(ctx, recordType, recordID, token, chatID)
}

func (rtm *alertRuntime) notifyTelegramUsing(ctx context.Context, recordType, recordID, token, chatID string) (manualNotifyResult, error) {
	var symbol, side, text string
	var markerAt time.Time
	var referencePrice float64
	switch recordType {
	case "alert":
		row, err := rtm.store.LiquidationAlertByID(ctx, recordID)
		if err != nil {
			return manualNotifyResult{}, err
		}
		symbol, side, text = row.Symbol, row.TradeSide, alertMessage(row)
		markerAt, referencePrice = row.TriggeredAt, row.ReferencePrice
	case domain.SignalAggressive, domain.SignalSteady:
		row, err := rtm.store.TradeSignalByID(ctx, recordID)
		if err != nil {
			return manualNotifyResult{}, err
		}
		symbol, side, text = row.Symbol, row.Side, signalMessage(row)
		markerAt, referencePrice = row.CreatedAt, row.ReferencePrice
	default:
		return manualNotifyResult{}, errors.New("record type must be alert, aggressive or steady")
	}
	if symbol == "" || text == "" {
		return manualNotifyResult{}, errors.New("record has no valid Telegram message")
	}
	send, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	// Manual quotation is an operator action that may happen long after the
	// alert. Capture the click-time chart task so its photo uses the newest
	// Binance candles, including the currently forming candle.
	photoTask := rtm.currentAlertChartTask(symbol, side, markerAt, referencePrice)
	if err := rtm.sender.Send(send, token, chatID, "📣 手动报警\n"+text); err != nil {
		return manualNotifyResult{}, err
	}
	photo, err := photoTask.Photo(send)
	if err != nil {
		return manualNotifyResult{}, fmt.Errorf("文字报警已发送，但 K 线截图生成失败: %w", err)
	}
	if err = rtm.sender.SendPhoto(send, token, chatID, photo, photoTask.PhotoName, photoTask.PhotoCaption); err != nil {
		return manualNotifyResult{}, fmt.Errorf("文字报警已发送，但 K 线截图发送失败: %w", err)
	}
	return manualNotifyResult{RecordType: recordType, RecordID: recordID, Symbol: symbol, Side: side, Status: "accepted"}, nil
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

// orderSignalEnabled reports whether a live liquidation category may send an
// opening request to the trading webhook.
func (rtm *alertRuntime) orderSignalEnabled(category string) bool {
	rtm.mu.RLock()
	defer rtm.mu.RUnlock()
	switch category {
	case "alert":
		return rtm.settings.OrderAlerts
	case domain.SignalAggressive:
		return rtm.settings.OrderAggressive
	case domain.SignalSteady:
		return rtm.settings.OrderSteady
	default:
		return false
	}
}

// orderCloseEnabled reports whether a live liquidation category may send the
// opposite-position preclose request. It is intentionally independent from the
// opening switch so a tenant can close risk without opening a new position.
func (rtm *alertRuntime) orderCloseEnabled(category string) bool {
	rtm.mu.RLock()
	defer rtm.mu.RUnlock()
	switch category {
	case "alert":
		return rtm.settings.OrderAlertsClose
	case domain.SignalAggressive:
		return rtm.settings.OrderAggressiveClose
	case domain.SignalSteady:
		return rtm.settings.OrderSteadyClose
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
	rtm.submitChart(signal.Symbol, signal.Side, signal.CreatedAt, signal.ReferencePrice)
	return "queued", ""
}

// submitChart queues the K-line image immediately after its corresponding text
// task. The dispatcher renders it after the text has been delivered, preserving
// the two-message order while keeping exchange I/O out of liquidation ingestion.
func (rtm *alertRuntime) submitChart(symbol, side string, at time.Time, referencePrice float64) {
	if rtm.service == nil {
		rtm.log.Warn("alert chart skipped: service unavailable", "symbol", symbol)
		return
	}
	if !rtm.notifier.Submit(rtm.alertChartTask(symbol, side, at, referencePrice)) {
		rtm.log.Warn("alert chart notification suppressed", "symbol", symbol, "side", side)
	}
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
		TelegramEnabled:      in.TelegramEnabled,
		NotifyAlerts:         in.NotifyAlerts,
		NotifyAggressive:     in.NotifyAggressive,
		NotifySteady:         in.NotifySteady,
		OrderAlerts:          in.OrderAlerts,
		OrderAggressive:      in.OrderAggressive,
		OrderSteady:          in.OrderSteady,
		OrderAlertsClose:     in.OrderAlertsClose,
		OrderAggressiveClose: in.OrderAggressiveClose,
		OrderSteadyClose:     in.OrderSteadyClose,
		TelegramChatID:       strings.TrimSpace(in.TelegramChatID),
		TelegramToken:        strings.TrimSpace(rtm.settings.TelegramToken),
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

func (rtm *alertRuntime) snapshotOrderSignalSettings() domain.OrderSignalSettings {
	rtm.mu.RLock()
	defer rtm.mu.RUnlock()
	return domain.OrderSignalSettings{
		Alerts: rtm.settings.OrderAlerts, Aggressive: rtm.settings.OrderAggressive,
		Steady: rtm.settings.OrderSteady, AlertsClose: rtm.settings.OrderAlertsClose,
		AggressiveClose: rtm.settings.OrderAggressiveClose, SteadyClose: rtm.settings.OrderSteadyClose,
		UpdatedAt: rtm.settings.UpdatedAt,
	}
}

func (rtm *alertRuntime) updateOrderSignalSettings(ctx context.Context, in domain.OrderSignalSettings) (domain.OrderSignalSettings, error) {
	rtm.mu.Lock()
	out := rtm.settings
	out.OrderAlerts, out.OrderAggressive, out.OrderSteady = in.Alerts, in.Aggressive, in.Steady
	out.OrderAlertsClose, out.OrderAggressiveClose, out.OrderSteadyClose = in.AlertsClose, in.AggressiveClose, in.SteadyClose
	out.UpdatedAt = time.Now().UTC()
	rtm.settings = out
	rtm.mu.Unlock()
	if err := rtm.store.SaveAlertSettings(ctx, out); err != nil {
		return domain.OrderSignalSettings{}, err
	}
	return rtm.snapshotOrderSignalSettings(), nil
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
