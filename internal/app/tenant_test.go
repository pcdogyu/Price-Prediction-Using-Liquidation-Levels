package app

import (
	"context"
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/alert"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

func TestTenantAlertSettingsAreIsolated(t *testing.T) {
	svc, _, _ := alertTestService(t, "tenant-settings.db")
	defer svc.Close()
	ctx := context.Background()
	alice := WithTenantID(ctx, "alice")
	bob := WithTenantID(ctx, "bob")

	if _, err := svc.SaveAlertSettingsForContext(alice, domain.NotificationSettings{
		TelegramEnabled: false, TelegramToken: "12345:alice-secret", TelegramChatID: "-1001",
		NotifyAlerts: true, NotifyAggressive: false, NotifySteady: true,
		OrderAlerts: false, OrderAggressive: false, OrderSteady: false,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SaveAlertStrategySettingsForContext(alice, domain.AlertStrategySettings{WindowMinutes: 7, BTCUSD: 300000, ETHUSD: 200000, OtherUSD: 150000}); err != nil {
		t.Fatal(err)
	}

	bobSettings, err := svc.AlertSettingsForContext(bob)
	if err != nil {
		t.Fatal(err)
	}
	if bobSettings.TelegramChatID == "-1001" || bobSettings.OrderAggressive != true {
		t.Fatalf("bob inherited alice settings: %+v", bobSettings)
	}
	bobStrategy, err := svc.AlertStrategySettingsForContext(bob)
	if err != nil {
		t.Fatal(err)
	}
	if bobStrategy.WindowMinutes == 7 || bobStrategy.BTCUSD == 300000 {
		t.Fatalf("bob inherited alice strategy: %+v", bobStrategy)
	}

	aliceSettings, err := svc.AlertSettingsForContext(alice)
	if err != nil {
		t.Fatal(err)
	}
	if aliceSettings.TelegramChatID != "-1001" || aliceSettings.NotifyAggressive || !aliceSettings.NotifySteady || aliceSettings.OrderAggressive {
		t.Fatalf("alice settings did not round trip: %+v", aliceSettings)
	}
	aliceStrategy, err := svc.AlertStrategySettingsForContext(alice)
	if err != nil {
		t.Fatal(err)
	}
	if aliceStrategy.WindowMinutes != 7 || aliceStrategy.BTCUSD != 300000 {
		t.Fatalf("alice strategy did not round trip: %+v", aliceStrategy)
	}
}

func TestTenantAlertHistoryIsGlobalButSettingsStayIsolated(t *testing.T) {
	svc, _, _ := alertTestService(t, "tenant-history.db")
	defer svc.Close()
	ctx := context.Background()
	alice := WithTenantID(ctx, "alice")
	bob := WithTenantID(ctx, "bob")

	aliceRuntime, err := svc.alertRuntimeForContext(alice)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Truncate(time.Second)
	aliceRuntime.persist(ctx, alert.Result{
		Alerts: []domain.LiquidationAlert{{
			ID: "global-alert", Symbol: "BTCUSDT", Side: "long", TradeSide: "long",
			TriggeredAt: at, ReferencePrice: 100, Status: domain.AlertStateWatching,
		}},
		Signals: []domain.TradeSignal{{
			ID: "global-signal", AlertID: "global-alert", Symbol: "BTCUSDT",
			Kind: domain.SignalAggressive, Side: "long", LiquidationSide: "long",
			CreatedAt: at, ReferencePrice: 100, TenantID: "alice",
		}},
	}, false)

	page, err := svc.AlertTimeline(bob, "", "", 20, "")
	if err != nil {
		t.Fatal(err)
	}
	if page.TotalCount != 2 || page.AlertCount != 1 || page.AggressiveCount != 1 {
		t.Fatalf("bob did not see alice's global history: %+v", page)
	}
	for _, row := range page.Rows {
		if row.Signal != nil && row.Signal.TenantID != "" {
			t.Fatalf("global signal leaked tenant identity: %+v", row.Signal)
		}
	}

	if _, err := svc.SaveAlertStrategySettingsForContext(alice, domain.AlertStrategySettings{WindowMinutes: 7, BTCUSD: 300000, ETHUSD: 200000, OtherUSD: 150000}); err != nil {
		t.Fatal(err)
	}
	bobStrategy, err := svc.AlertStrategySettingsForContext(bob)
	if err != nil {
		t.Fatal(err)
	}
	if bobStrategy.WindowMinutes == 7 || bobStrategy.BTCUSD == 300000 {
		t.Fatalf("global history change unexpectedly shared bob's settings: %+v", bobStrategy)
	}
}
