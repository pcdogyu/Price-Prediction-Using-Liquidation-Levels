package app

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/alert"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/config"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/store"
)

func alertTestService(t *testing.T, name string) (*Service, config.Config, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cfg := config.Config{
		Symbols:              []string{"BTCUSDT"},
		ModelPath:            filepath.Join(t.TempDir(), "model.json"),
		AlertWindow:          3 * time.Minute,
		AlertQuiet:           time.Minute,
		AlertConfirm:         30 * time.Minute,
		AlertThresholdBTCUSD: 100000,
	}
	return New(cfg, st, slog.New(slog.NewTextHandler(io.Discard, nil))), cfg, st
}

func alertTestEvent(id, symbol, side string, at time.Time, usd float64) domain.LiquidationEvent {
	return domain.LiquidationEvent{ID: id, Exchange: "binance", Symbol: symbol,
		PositionSide: side, EventTime: at, ReceivedAt: at, Price: 100,
		Quantity: usd / 100, NotionalUSD: usd, Coverage: "sampled"}
}

// TestRecordLiquidationDrivesAlertAndSignals covers the full pipeline the running
// service uses: RecordLiquidation feeds the engine, the tick timer ends the quiet
// period, an opposite liquidation confirms the steady signal, and every produced row
// reaches SQLite with the notification status the alert page shows.
func TestRecordLiquidationDrivesAlertAndSignals(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := alertTestService(t, "alerts.db")
	base := time.Now().UTC().Truncate(time.Second)
	svc.alerts.engine.SetClock(func() time.Time { return base })
	svc.alerts.applyWatch(
		[]alert.WatchEntry{{Symbol: "BTCUSDT", Rank: 1, QuoteVolumeUSD: 1e9}},
		[]domain.WatchSymbol{{Symbol: "BTCUSDT", Rank: 1, QuoteVolumeUSD: 1e9, UpdatedAt: base}},
		base, "")

	if err := svc.RecordLiquidation(ctx, alertTestEvent("long-1", "BTCUSDT", "long", base, 120000)); err != nil {
		t.Fatal(err)
	}
	alerts, err := svc.AlertHistory(ctx, "BTCUSDT", "", 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if alerts.AlertCount != 1 || alerts.Rows[0].Status != domain.AlertStateWatching {
		t.Fatalf("alert after breach = %+v", alerts.Rows)
	}

	// The quiet period ends without further liquidations, so the aggressive signal
	// is emitted in the direction of the liquidated positions.
	svc.alerts.persist(ctx, svc.alerts.engine.Tick(base.Add(2*time.Minute)), true)
	// An opposite liquidation inside the confirmation window raises the steady one.
	if err = svc.RecordLiquidation(ctx, alertTestEvent("short-1", "BTCUSDT", "short", base.Add(3*time.Minute), 5000)); err != nil {
		t.Fatal(err)
	}

	signals, err := svc.AlertSignals(ctx, "BTCUSDT", "", 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if signals.SignalCount != 2 || signals.AggressiveCount != 1 || signals.SteadyCount != 1 {
		t.Fatalf("signal counters = %+v rows=%+v", signals, signals.Rows)
	}
	for _, row := range signals.Rows {
		// Telegram is not configured, so both signals are stored as muted.
		if row.NotifyStatus != "muted" {
			t.Fatalf("signal %s notify status = %q", row.ID, row.NotifyStatus)
		}
	}
	alerts, err = svc.AlertHistory(ctx, "BTCUSDT", "", 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if alerts.Rows[0].Status != domain.AlertStateSteady || alerts.Rows[0].OppositeUSD != 5000 {
		t.Fatalf("alert after confirmation = %+v", alerts.Rows[0])
	}

	view, err := svc.Alerts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if view.AlertTotal != 1 || view.SignalTotal != 2 || len(view.Watch) != 1 {
		t.Fatalf("alert view = %+v", view)
	}
	// Telegram is not configured, so the page shows the worker as off.
	if view.Notifications.Configured || view.Notifications.Enabled {
		t.Fatalf("notification status = %+v", view.Notifications)
	}
}

func TestAlertPagesExposeFreshETHGammaAsCommonGate(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := alertTestService(t, "alert-gamma-view.db")
	now := time.Now().UTC()
	svc.dashboard.Lock()
	svc.dashboard.gamma["ETHUSDT"] = domain.GammaView{Symbol: "ETHUSDT", State: "ok", Time: now, NetGEXUSD: -123.45}
	svc.dashboard.Unlock()

	view, err := svc.Alerts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !view.Gamma.Ignored || view.Gamma.NetGEXUSD == nil || *view.Gamma.NetGEXUSD != -123.45 {
		t.Fatalf("alert overview gamma = %+v", view.Gamma)
	}
	page, err := svc.AlertTimeline(ctx, "", "", 20, "")
	if err != nil {
		t.Fatal(err)
	}
	if !page.Gamma.Ignored || page.Gamma.AnchorSymbol != "ETHUSDT" {
		t.Fatalf("timeline gamma = %+v", page.Gamma)
	}
}

func TestAlertPagesUseBTCGammaOnlyForBTC(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := alertTestService(t, "alert-gamma-anchors.db")
	now := time.Now().UTC()
	svc.dashboard.Lock()
	svc.dashboard.gamma["BTCUSDT"] = domain.GammaView{Symbol: "BTCUSDT", State: "ok", Time: now, NetGEXUSD: -10}
	svc.dashboard.gamma["ETHUSDT"] = domain.GammaView{Symbol: "ETHUSDT", State: "ok", Time: now, NetGEXUSD: 10}
	svc.dashboard.Unlock()

	btc, err := svc.AlertTimeline(ctx, "BTCUSDT", "", 20, "")
	if err != nil {
		t.Fatal(err)
	}
	if btc.Gamma.AnchorSymbol != "BTCUSDT" || !btc.Gamma.Ignored {
		t.Fatalf("BTC timeline must use BTC Gamma: %+v", btc.Gamma)
	}
	other, err := svc.AlertTimeline(ctx, "SOLUSDT", "", 20, "")
	if err != nil {
		t.Fatal(err)
	}
	if other.Gamma.AnchorSymbol != "ETHUSDT" || other.Gamma.Ignored {
		t.Fatalf("non-BTC timeline must use ETH Gamma: %+v", other.Gamma)
	}
}

func TestAlertHistoryKeepsGammaDecisionFromSignalTime(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := alertTestService(t, "alert-gamma-history.db")
	base := time.Now().UTC().Truncate(time.Second)
	svc.alerts.engine.SetClock(func() time.Time { return base })
	svc.alerts.applyWatch(
		[]alert.WatchEntry{{Symbol: "BTCUSDT", Rank: 1, QuoteVolumeUSD: 1e9}},
		[]domain.WatchSymbol{{Symbol: "BTCUSDT", Rank: 1, QuoteVolumeUSD: 1e9, UpdatedAt: base}},
		base, "")

	// The alert and aggressive signal are created while Gamma is positive.
	svc.dashboard.Lock()
	svc.dashboard.gamma["BTCUSDT"] = domain.GammaView{Symbol: "BTCUSDT", State: "ok", Time: time.Now().UTC(), NetGEXUSD: 1}
	svc.dashboard.Unlock()
	if err := svc.RecordLiquidation(ctx, alertTestEvent("gamma-long", "BTCUSDT", "long", base, 120000)); err != nil {
		t.Fatal(err)
	}
	svc.alerts.persist(ctx, svc.alerts.engine.Tick(base.Add(2*time.Minute)), true)

	// A later negative Gamma suppresses only the newly created steady signal.
	later := base.Add(3 * time.Minute)
	svc.dashboard.Lock()
	svc.dashboard.gamma["BTCUSDT"] = domain.GammaView{Symbol: "BTCUSDT", State: "ok", Time: time.Now().UTC(), NetGEXUSD: -1}
	svc.dashboard.Unlock()
	if err := svc.RecordLiquidation(ctx, alertTestEvent("gamma-short", "BTCUSDT", "short", later, 5000)); err != nil {
		t.Fatal(err)
	}

	page, err := svc.AlertTimeline(ctx, "BTCUSDT", "", 20, "")
	if err != nil {
		t.Fatal(err)
	}
	var alertIgnored, aggressiveIgnored, steadyIgnored *bool
	for _, row := range page.Rows {
		if row.Alert != nil {
			value := row.Alert.GammaIgnored
			alertIgnored = &value
		}
		if row.Signal == nil {
			continue
		}
		value := row.Signal.GammaIgnored
		switch row.Signal.Kind {
		case domain.SignalAggressive:
			aggressiveIgnored = &value
		case domain.SignalSteady:
			steadyIgnored = &value
		}
	}
	if alertIgnored == nil || *alertIgnored || aggressiveIgnored == nil || *aggressiveIgnored {
		t.Fatalf("positive-Gamma history was rewritten: alert=%v aggressive=%v", alertIgnored, aggressiveIgnored)
	}
	if steadyIgnored == nil || !*steadyIgnored {
		t.Fatalf("new negative-Gamma signal was not marked ignored: %v", steadyIgnored)
	}
	if !page.Gamma.Ignored {
		t.Fatalf("current Gamma should remain the live gate: %+v", page.Gamma)
	}
}

func TestAlertChartSignalsReplayCurrentStrategySettings(t *testing.T) {
	ctx := context.Background()
	svc, _, st := alertTestService(t, "chart-current-settings.db")
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	if err := st.InsertLiquidation(ctx, alertTestEvent("chart-long", "BTCUSDT", "long", base, 120000)); err != nil {
		t.Fatal(err)
	}

	view, err := svc.AlertChartSignals(ctx, "BTCUSDT")
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Alerts) != 1 || len(view.Signals) != 1 || view.Thresholds.BTCUSD != 100000 {
		t.Fatalf("default chart replay = %+v", view)
	}

	if _, err = svc.SaveAlertStrategySettings(ctx, domain.AlertStrategySettings{WindowMinutes: 3, BTCUSD: 200000, ETHUSD: 200000, OtherUSD: 100000}); err != nil {
		t.Fatal(err)
	}
	view, err = svc.AlertChartSignals(ctx, "BTCUSDT")
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Alerts) != 0 || len(view.Signals) != 0 || view.Thresholds.BTCUSD != 200000 {
		t.Fatalf("chart replay did not follow saved threshold = %+v", view)
	}
}

// TestAlertSettingsRoundTripAndRestartReplay checks that a saved form keeps the stored
// bot token when a later submission leaves it blank, and that the restart warm start
// replays stored liquidations without handing an existing signal to Telegram twice.
func TestAlertSettingsRoundTripAndRestartReplay(t *testing.T) {
	ctx := context.Background()
	stored, cfg, st := alertTestService(t, "settings.db")
	base := time.Now().UTC().Truncate(time.Second)
	stored.alerts.engine.SetClock(func() time.Time { return base })
	stored.alerts.applyWatch([]alert.WatchEntry{{Symbol: "BTCUSDT", Rank: 1}},
		[]domain.WatchSymbol{{Symbol: "BTCUSDT", Rank: 1, UpdatedAt: base}}, base, "")
	if err := stored.RecordLiquidation(ctx, alertTestEvent("restore-long", "BTCUSDT", "long", base, 120000)); err != nil {
		t.Fatal(err)
	}
	stored.alerts.persist(ctx, stored.alerts.engine.Tick(base.Add(2*time.Minute)), true)

	saved, err := stored.SaveAlertSettings(ctx, domain.NotificationSettings{
		TelegramEnabled: true, TelegramToken: "123456:secret-token-value", TelegramChatID: "42",
		NotifyAlerts: true, NotifyAggressive: true, NotifySteady: false})
	if err != nil {
		t.Fatal(err)
	}
	if !saved.HasToken || saved.TokenSource != "database" || saved.TokenMask == "" ||
		saved.TokenMask == "123456:secret-token-value" {
		t.Fatalf("saved settings leaked or hid the token: %+v", saved)
	}
	again, err := stored.SaveAlertSettings(ctx, domain.NotificationSettings{
		TelegramEnabled: true, TelegramChatID: "42", NotifyAlerts: true})
	if err != nil {
		t.Fatal(err)
	}
	if again.TokenMask != saved.TokenMask || again.TokenSource != "database" {
		t.Fatalf("blank token submission changed the stored token: %+v", again)
	}
	if _, err = stored.SaveAlertSettings(ctx, domain.NotificationSettings{
		TelegramEnabled: true, TelegramToken: "bad-token", TelegramChatID: "42"}); err == nil {
		t.Fatal("malformed token was accepted")
	}
	if err = st.SaveAlertStrategySettings(ctx, domain.AlertStrategySettings{WindowMinutes: 5, BTCUSD: 300000, ETHUSD: 200000, OtherUSD: 100000}); err != nil {
		t.Fatal(err)
	}

	// A restart builds a second service on the same database. Its warm start replays
	// the stored liquidation, and the deterministic IDs keep the existing rows from
	// being stored or notified a second time.
	restarted := New(cfg, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	restarted.alerts.restoreSettings(ctx)
	restarted.alerts.restoreStrategySettings(ctx)
	restarted.alerts.engine.Watch([]alert.WatchEntry{{Symbol: "BTCUSDT", Rank: 1}})
	restarted.alerts.seedFromHistory(ctx)
	if got := restarted.AlertSettings(); !got.TelegramEnabled || got.TokenMask != saved.TokenMask {
		t.Fatalf("restarted settings = %+v", got)
	}
	if got := restarted.AlertStrategySettings(); got.WindowMinutes != 5 || got.ETHUSD != 200000 || got.OtherUSD != 100000 {
		t.Fatalf("restarted strategy settings = %+v", got)
	}
	signals, err := restarted.AlertSignals(ctx, "", "", 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if signals.SignalCount != 1 || signals.Rows[0].NotifyStatus != "muted" {
		t.Fatalf("replayed signals = %+v", signals.Rows)
	}
	alerts, err := restarted.AlertHistory(ctx, "", "", 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if alerts.AlertCount != 1 {
		t.Fatalf("replayed alerts = %+v", alerts.Rows)
	}
}

func TestOrderSignalSettingsRoundTripAndOpeningCategoryFiltering(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := alertTestService(t, "order-signal-settings.db")
	initial := svc.AlertOrderSignalSettings()
	if !initial.Alerts || !initial.Aggressive || !initial.Steady {
		t.Fatalf("new order signal settings should default to enabled: %+v", initial)
	}
	saved, err := svc.SaveAlertOrderSignalSettings(ctx, domain.OrderSignalSettings{Alerts: false, Aggressive: true, Steady: false})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Alerts || !saved.Aggressive || saved.Steady {
		t.Fatalf("saved order signal settings = %+v", saved)
	}
	if !svc.alerts.orderSignalEnabled("alert") || !svc.alerts.orderSignalEnabled(domain.SignalAggressive) || svc.alerts.orderSignalEnabled(domain.SignalSteady) {
		t.Fatal("opening signal category filtering did not follow the saved settings")
	}
	restarted := New(svc.cfg, svc.store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	restarted.alerts.restoreSettings(ctx)
	if got := restarted.AlertOrderSignalSettings(); got.Alerts || !got.Aggressive || got.Steady {
		t.Fatalf("restarted order signal settings = %+v", got)
	}
}
