package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/notify"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/store"
)

type tenantContextKey struct{}

// WithTenantID carries the authenticated username through alert service calls.
// The store keeps it as an opaque tenant key; it is never accepted from a
// browser request body.
func WithTenantID(ctx context.Context, tenantID string) context.Context {
	return context.WithValue(ctx, tenantContextKey{}, strings.TrimSpace(tenantID))
}

func tenantIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	value, _ := ctx.Value(tenantContextKey{}).(string)
	return strings.TrimSpace(value)
}

func (s *Service) AlertSettingsForContext(ctx context.Context) (domain.NotificationSettings, error) {
	return s.alertSettingsForContext(ctx)
}

func (s *Service) alertRuntimeForContext(ctx context.Context) (*alertRuntime, error) {
	tenantID := tenantIDFromContext(ctx)
	if tenantID == "" {
		return s.alerts, nil
	}
	return s.ensureTenantAlertRuntime(ctx, tenantID)
}

func (s *Service) ensureTenantAlertRuntime(ctx context.Context, tenantID string) (*alertRuntime, error) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return s.alerts, nil
	}
	s.alertTenantsMu.Lock()
	if rtm, ok := s.alertTenants[tenantID]; ok {
		s.alertTenantsMu.Unlock()
		return rtm, nil
	}
	root := strings.TrimSpace(s.cfg.AlertTenantDataPath)
	if root == "" {
		root = filepath.Join(filepath.Dir(s.cfg.DatabasePath), "alert-tenants")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		s.alertTenantsMu.Unlock()
		return nil, err
	}
	hash := sha256.Sum256([]byte(tenantID))
	tenantStore, err := store.Open(filepath.Join(root, hex.EncodeToString(hash[:8])+".db"))
	if err != nil {
		s.alertTenantsMu.Unlock()
		return nil, fmt.Errorf("open alert tenant store: %w", err)
	}
	rtm := s.newAlertRuntime()
	rtm.store = tenantStore
	rtm.sourceStore = s.store
	rtm.tenantID = tenantID
	if settings, found, settingsErr := s.store.AlertSettingsForTenant(ctx, tenantID); settingsErr != nil {
		_ = tenantStore.Close()
		s.alertTenantsMu.Unlock()
		return nil, settingsErr
	} else if found {
		rtm.settings = settings
	} else if s.isLegacyAlertTenant(tenantID) {
		legacy, legacyFound, legacyErr := s.store.AlertSettings(ctx)
		if legacyErr != nil {
			_ = tenantStore.Close()
			s.alertTenantsMu.Unlock()
			return nil, legacyErr
		}
		if legacyFound {
			rtm.settings = legacy
			if err := s.store.SaveAlertSettingsForTenant(ctx, tenantID, legacy); err != nil {
				_ = tenantStore.Close()
				s.alertTenantsMu.Unlock()
				return nil, err
			}
		}
	}
	if strategy, found, strategyErr := s.store.AlertStrategySettingsForTenant(ctx, tenantID); strategyErr != nil {
		_ = tenantStore.Close()
		s.alertTenantsMu.Unlock()
		return nil, strategyErr
	} else if found && validateAlertStrategySettings(strategy) == nil {
		cfg := rtm.engine.Config()
		cfg.Window = time.Duration(strategy.WindowMinutes) * time.Minute
		cfg.BTCUSD, cfg.ETHUSD, cfg.OtherUSD = strategy.BTCUSD, strategy.ETHUSD, strategy.OtherUSD
		cfg.VWAPRangeMinutes = strategy.VWAPRangeHours * 60
		cfg.VWAPMinDrawdown = strategy.VWAPMinDrawdownPct / 100
		cfg.VWAPDonchianFraction = strategy.VWAPDonchianPct / 100
		rtm.engine.UpdateConfig(cfg)
	} else if s.isLegacyAlertTenant(tenantID) {
		legacy, legacyFound, legacyErr := s.store.AlertStrategySettings(ctx)
		if legacyErr != nil {
			_ = tenantStore.Close()
			s.alertTenantsMu.Unlock()
			return nil, legacyErr
		}
		if legacyFound && validateAlertStrategySettings(legacy) == nil {
			cfg := rtm.engine.Config()
			cfg.Window = time.Duration(legacy.WindowMinutes) * time.Minute
			cfg.BTCUSD, cfg.ETHUSD, cfg.OtherUSD = legacy.BTCUSD, legacy.ETHUSD, legacy.OtherUSD
			cfg.VWAPRangeMinutes = legacy.VWAPRangeHours * 60
			cfg.VWAPMinDrawdown = legacy.VWAPMinDrawdownPct / 100
			cfg.VWAPDonchianFraction = legacy.VWAPDonchianPct / 100
			rtm.engine.UpdateConfig(cfg)
			if err := s.store.SaveAlertStrategySettingsForTenant(ctx, tenantID, legacy); err != nil {
				_ = tenantStore.Close()
				s.alertTenantsMu.Unlock()
				return nil, err
			}
		}
	}
	s.alerts.mu.RLock()
	watch := append([]domain.WatchSymbol(nil), s.alerts.watch...)
	watchAt, watchErr := s.alerts.watchAt, s.alerts.watchErr
	s.alerts.mu.RUnlock()
	if len(watch) > 0 {
		rtm.applyWatch(rowsToEntries(watch), watch, watchAt, watchErr)
	}
	if events, historyErr := s.store.RecentLiquidationEvents(ctx, time.Now().UTC().Add(-(rtm.engine.Config().Window + rtm.cfg.Quiet)), 60000); historyErr == nil && len(events) > 0 {
		rtm.persist(context.WithoutCancel(ctx), rtm.engine.Seed(events), false)
	}
	s.alertTenants[tenantID] = rtm
	startCtx := s.alertContext
	s.alertTenantsMu.Unlock()
	if startCtx != nil {
		go rtm.notifier.Start(startCtx)
		go rtm.webhook.Start(startCtx)
		go rtm.tickLoop(startCtx)
	}
	return rtm, nil
}

func (s *Service) isLegacyAlertTenant(tenantID string) bool {
	owner := strings.TrimSpace(s.cfg.AuthUsername)
	return owner != "" && strings.EqualFold(owner, strings.TrimSpace(tenantID))
}

func (s *Service) syncTenantAlertWatches(rows []domain.WatchSymbol, at time.Time, failure string) {
	s.alertTenantsMu.Lock()
	runtimes := make([]*alertRuntime, 0, len(s.alertTenants))
	for _, rtm := range s.alertTenants {
		runtimes = append(runtimes, rtm)
	}
	s.alertTenantsMu.Unlock()
	entries := rowsToEntries(rows)
	for _, rtm := range runtimes {
		rtm.applyWatch(entries, rows, at, failure)
	}
}

func (s *Service) SaveAlertSettingsForContext(ctx context.Context, in domain.NotificationSettings) (domain.NotificationSettings, error) {
	return s.saveAlertSettingsForContext(ctx, in)
}

func (s *Service) AlertOrderSignalSettingsForContext(ctx context.Context) (domain.OrderSignalSettings, error) {
	return s.alertOrderSignalSettingsForContext(ctx)
}

func (s *Service) SaveAlertOrderSignalSettingsForContext(ctx context.Context, in domain.OrderSignalSettings) (domain.OrderSignalSettings, error) {
	return s.saveAlertOrderSignalSettingsForContext(ctx, in)
}

func (s *Service) AlertStrategySettingsForContext(ctx context.Context) (domain.AlertStrategySettings, error) {
	return s.alertStrategySettingsForContext(ctx)
}

func (s *Service) SaveAlertStrategySettingsForContext(ctx context.Context, in domain.AlertStrategySettings) (domain.AlertStrategySettings, error) {
	return s.saveAlertStrategySettingsForContext(ctx, in)
}

func (s *Service) TelegramTestForContext(ctx context.Context) error {
	return s.telegramTestForContext(ctx)
}

func (s *Service) NotifyAlertForContext(ctx context.Context, recordType, recordID string) (any, error) {
	return s.notifyAlertForContext(ctx, recordType, recordID)
}

func (s *Service) AlertChartSignalsForContext(ctx context.Context, symbol string) (domain.AlertChartSignals, error) {
	rtm, err := s.alertRuntimeForContext(ctx)
	if err != nil {
		return domain.AlertChartSignals{}, err
	}
	tenantID := tenantIDFromContext(ctx)
	if tenantID == "" {
		return rtm.AlertChartSignals(ctx, symbol)
	}
	cfg := rtm.engine.Config()
	strategy, found, err := s.store.AlertStrategySettingsForTenant(ctx, tenantID)
	if err != nil {
		return domain.AlertChartSignals{}, err
	}
	if found && validateAlertStrategySettings(strategy) == nil {
		cfg.Window = time.Duration(strategy.WindowMinutes) * time.Minute
		cfg.BTCUSD, cfg.ETHUSD, cfg.OtherUSD = strategy.BTCUSD, strategy.ETHUSD, strategy.OtherUSD
		cfg.VWAPRangeMinutes = strategy.VWAPRangeHours * 60
		cfg.VWAPMinDrawdown = strategy.VWAPMinDrawdownPct / 100
		cfg.VWAPDonchianFraction = strategy.VWAPDonchianPct / 100
	}
	return s.alerts.AlertChartSignalsWithConfig(ctx, symbol, cfg)
}

func (s *Service) alertSettingsForContext(ctx context.Context) (domain.NotificationSettings, error) {
	rtm, err := s.alertRuntimeForContext(ctx)
	if err != nil {
		return domain.NotificationSettings{}, err
	}
	return rtm.snapshotSettings(), nil
}

func (s *Service) saveAlertSettingsForContext(ctx context.Context, in domain.NotificationSettings) (domain.NotificationSettings, error) {
	tenantID := tenantIDFromContext(ctx)
	if tenantID == "" {
		return s.SaveAlertSettings(ctx, in)
	}
	rtm, err := s.alertRuntimeForContext(ctx)
	if err != nil {
		return domain.NotificationSettings{}, err
	}
	return rtm.updateSettingsForTenant(ctx, tenantID, in)
}

func (s *Service) alertOrderSignalSettingsForContext(ctx context.Context) (domain.OrderSignalSettings, error) {
	rtm, err := s.alertRuntimeForContext(ctx)
	if err != nil {
		return domain.OrderSignalSettings{}, err
	}
	tenantID := tenantIDFromContext(ctx)
	if tenantID == "" {
		return rtm.snapshotOrderSignalSettings(), nil
	}
	settings, err := s.tenantRawSettings(ctx, tenantID)
	if err != nil {
		return domain.OrderSignalSettings{}, err
	}
	return domain.OrderSignalSettings{Alerts: settings.OrderAlerts, Aggressive: settings.OrderAggressive, Steady: settings.OrderSteady, AlertsClose: settings.OrderAlertsClose, AggressiveClose: settings.OrderAggressiveClose, SteadyClose: settings.OrderSteadyClose, UpdatedAt: settings.UpdatedAt}, nil
}

func (s *Service) saveAlertOrderSignalSettingsForContext(ctx context.Context, in domain.OrderSignalSettings) (domain.OrderSignalSettings, error) {
	tenantID := tenantIDFromContext(ctx)
	if tenantID == "" {
		return s.SaveAlertOrderSignalSettings(ctx, in)
	}
	rtm, err := s.alertRuntimeForContext(ctx)
	if err != nil {
		return domain.OrderSignalSettings{}, err
	}
	return rtm.updateOrderSignalSettingsForTenant(ctx, tenantID, in)
}

func (s *Service) alertStrategySettingsForContext(ctx context.Context) (domain.AlertStrategySettings, error) {
	rtm, err := s.alertRuntimeForContext(ctx)
	if err != nil {
		return domain.AlertStrategySettings{}, err
	}
	tenantID := tenantIDFromContext(ctx)
	if tenantID == "" {
		return rtm.snapshotStrategySettings(), nil
	}
	stored, found, err := s.store.AlertStrategySettingsForTenant(ctx, tenantID)
	if err != nil {
		return domain.AlertStrategySettings{}, err
	}
	if !found {
		return rtm.snapshotStrategySettings(), nil
	}
	return stored, nil
}

func (s *Service) saveAlertStrategySettingsForContext(ctx context.Context, in domain.AlertStrategySettings) (domain.AlertStrategySettings, error) {
	tenantID := tenantIDFromContext(ctx)
	if tenantID == "" {
		return s.SaveAlertStrategySettings(ctx, in)
	}
	rtm, err := s.alertRuntimeForContext(ctx)
	if err != nil {
		return domain.AlertStrategySettings{}, err
	}
	return rtm.updateStrategySettingsForTenant(ctx, tenantID, in)
}

func (s *Service) telegramTestForContext(ctx context.Context) error {
	tenantID := tenantIDFromContext(ctx)
	if tenantID == "" {
		return s.TelegramTest(ctx)
	}
	rtm, err := s.alertRuntimeForContext(ctx)
	if err != nil {
		return err
	}
	settings, err := s.tenantRawSettings(ctx, tenantID)
	if err != nil {
		return err
	}
	token, chatID, ok := rtm.credentialsForSettings(settings)
	if !ok {
		return errors.New("Telegram 通知未启用，或 Token / Chat ID 未配置")
	}
	send, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	thresholds := s.alerts.engine.Thresholds()
	strategy := s.alerts.strategyForTenant(ctx, tenantID)
	thresholds.WindowMinutes, thresholds.BTCUSD, thresholds.ETHUSD, thresholds.OtherUSD = strategy.WindowMinutes, strategy.BTCUSD, strategy.ETHUSD, strategy.OtherUSD
	return rtm.sender.Send(send, token, chatID, testMessage(chatID, thresholds))
}

func (s *Service) notifyAlertForContext(ctx context.Context, recordType, recordID string) (any, error) {
	tenantID := tenantIDFromContext(ctx)
	if tenantID == "" {
		return s.NotifyAlert(ctx, recordType, recordID)
	}
	rtm, err := s.alertRuntimeForContext(ctx)
	if err != nil {
		return nil, err
	}
	return rtm.notifyTelegramWithSettings(ctx, recordType, recordID, tenantID)
}

func (s *Service) tenantRawSettings(ctx context.Context, tenantID string) (domain.NotificationSettings, error) {
	settings, found, err := s.store.AlertSettingsForTenant(ctx, tenantID)
	if err != nil {
		return domain.NotificationSettings{}, err
	}
	if !found {
		settings = s.alerts.defaultSettings()
	}
	return settings, nil
}

func (rtm *alertRuntime) credentialsForSettings(settings domain.NotificationSettings) (string, string, bool) {
	token, chatID := rtm.envToken, rtm.envChat
	if stored := strings.TrimSpace(settings.TelegramToken); stored != "" {
		token = stored
	}
	if stored := strings.TrimSpace(settings.TelegramChatID); stored != "" {
		chatID = stored
	}
	return token, chatID, settings.TelegramEnabled && notify.ValidToken(token) && notify.ValidChatID(chatID)
}

func (rtm *alertRuntime) tenantConfigStore() *store.Store {
	if rtm.sourceStore != nil {
		return rtm.sourceStore
	}
	return rtm.store
}

func (rtm *alertRuntime) maskSettings(settings domain.NotificationSettings) domain.NotificationSettings {
	storedToken := strings.TrimSpace(settings.TelegramToken)
	token, chatID, _ := rtm.credentialsForSettings(settings)
	settings.TelegramToken = ""
	settings.TelegramChatID = chatID
	settings.HasToken = token != ""
	settings.TokenMask = notify.MaskToken(token)
	switch {
	case storedToken != "":
		settings.TokenSource = "database"
	case strings.TrimSpace(rtm.envToken) != "":
		settings.TokenSource = "environment"
	default:
		settings.TokenSource = "unset"
	}
	return settings
}

func (rtm *alertRuntime) strategyForTenant(ctx context.Context, tenantID string) domain.AlertStrategySettings {
	stored, found, err := rtm.tenantConfigStore().AlertStrategySettingsForTenant(ctx, tenantID)
	if err == nil && found {
		return stored
	}
	return rtm.snapshotStrategySettings()
}

func (rtm *alertRuntime) updateSettingsForTenant(ctx context.Context, tenantID string, in domain.NotificationSettings) (domain.NotificationSettings, error) {
	current, found, err := rtm.tenantConfigStore().AlertSettingsForTenant(ctx, tenantID)
	if err != nil {
		return domain.NotificationSettings{}, err
	}
	if !found {
		current = rtm.defaultSettings()
	}
	out := domain.NotificationSettings{TelegramEnabled: in.TelegramEnabled, NotifyAlerts: in.NotifyAlerts, NotifyAggressive: in.NotifyAggressive, NotifySteady: in.NotifySteady, OrderAlerts: in.OrderAlerts, OrderAggressive: in.OrderAggressive, OrderSteady: in.OrderSteady, OrderAlertsClose: in.OrderAlertsClose, OrderAggressiveClose: in.OrderAggressiveClose, OrderSteadyClose: in.OrderSteadyClose, TelegramChatID: strings.TrimSpace(in.TelegramChatID), TelegramToken: strings.TrimSpace(current.TelegramToken)}
	if out.TelegramChatID != "" && !notify.ValidChatID(out.TelegramChatID) {
		return domain.NotificationSettings{}, errors.New("Telegram Chat ID 需为数字 ID 或 @频道名")
	}
	if token := strings.TrimSpace(in.TelegramToken); token != "" {
		if !notify.ValidToken(token) {
			return domain.NotificationSettings{}, errors.New("Telegram Token 格式应为 bot_id:secret")
		}
		out.TelegramToken = token
	}
	if out.TelegramEnabled {
		if _, _, ok := rtm.credentialsForSettings(out); !ok {
			return domain.NotificationSettings{}, errors.New("启用通知需要有效的 Telegram Token 与 Chat ID，可在下方填写或配置环境变量")
		}
	}
	out.UpdatedAt = time.Now().UTC()
	if err := rtm.tenantConfigStore().SaveAlertSettingsForTenant(ctx, tenantID, out); err != nil {
		return domain.NotificationSettings{}, err
	}
	rtm.mu.Lock()
	rtm.settings = out
	rtm.mu.Unlock()
	return rtm.maskSettings(out), nil
}

func (rtm *alertRuntime) updateOrderSignalSettingsForTenant(ctx context.Context, tenantID string, in domain.OrderSignalSettings) (domain.OrderSignalSettings, error) {
	settings, found, err := rtm.tenantConfigStore().AlertSettingsForTenant(ctx, tenantID)
	if err != nil {
		return domain.OrderSignalSettings{}, err
	}
	if !found {
		settings = rtm.defaultSettings()
	}
	settings.OrderAlerts, settings.OrderAggressive, settings.OrderSteady = in.Alerts, in.Aggressive, in.Steady
	settings.OrderAlertsClose, settings.OrderAggressiveClose, settings.OrderSteadyClose = in.AlertsClose, in.AggressiveClose, in.SteadyClose
	settings.UpdatedAt = time.Now().UTC()
	if err := rtm.tenantConfigStore().SaveAlertSettingsForTenant(ctx, tenantID, settings); err != nil {
		return domain.OrderSignalSettings{}, err
	}
	rtm.mu.Lock()
	rtm.settings = settings
	rtm.mu.Unlock()
	return domain.OrderSignalSettings{Alerts: settings.OrderAlerts, Aggressive: settings.OrderAggressive, Steady: settings.OrderSteady, AlertsClose: settings.OrderAlertsClose, AggressiveClose: settings.OrderAggressiveClose, SteadyClose: settings.OrderSteadyClose, UpdatedAt: settings.UpdatedAt}, nil
}

func (rtm *alertRuntime) updateStrategySettingsForTenant(ctx context.Context, tenantID string, in domain.AlertStrategySettings) (domain.AlertStrategySettings, error) {
	in = fillStrategyVWAPDefaults(in, rtm.snapshotStrategySettings())
	if err := validateAlertStrategySettings(in); err != nil {
		return domain.AlertStrategySettings{}, err
	}
	in.UpdatedAt = time.Now().UTC()
	if err := rtm.tenantConfigStore().SaveAlertStrategySettingsForTenant(ctx, tenantID, in); err != nil {
		return domain.AlertStrategySettings{}, err
	}
	cfg := rtm.engine.Config()
	cfg.Window = time.Duration(in.WindowMinutes) * time.Minute
	cfg.BTCUSD, cfg.ETHUSD, cfg.OtherUSD = in.BTCUSD, in.ETHUSD, in.OtherUSD
	cfg.VWAPRangeMinutes = in.VWAPRangeHours * 60
	cfg.VWAPMinDrawdown = in.VWAPMinDrawdownPct / 100
	cfg.VWAPDonchianFraction = in.VWAPDonchianPct / 100
	rtm.engine.UpdateConfig(cfg)
	return in, nil
}
