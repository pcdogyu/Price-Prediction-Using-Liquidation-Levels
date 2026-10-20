package app

import (
	"context"
	"testing"

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
