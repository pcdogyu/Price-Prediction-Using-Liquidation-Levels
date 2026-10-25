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

func TestTenantAlertHistoryIsIsolated(t *testing.T) {
	svc, _, _ := alertTestService(t, "tenant-history.db")
	defer svc.Close()
	ctx := context.Background()
	alice := WithTenantID(ctx, "alice")
	bob := WithTenantID(ctx, "bob")
	rtm, err := svc.alertRuntimeForContext(alice)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Truncate(time.Second)
	rtm.persist(ctx, alert.Result{
		Alerts:  []domain.LiquidationAlert{{ID: "alice-alert", Symbol: "BTCUSDT", Side: "long", TradeSide: "long", TriggeredAt: at, Status: domain.AlertStateWatching}},
		Signals: []domain.TradeSignal{{ID: "alice-signal", AlertID: "alice-alert", Symbol: "BTCUSDT", Kind: domain.SignalAggressive, Side: "long", LiquidationSide: "long", CreatedAt: at}},
	}, false)

	alicePage, err := svc.AlertTimeline(alice, "", "", 20, "")
	if err != nil {
		t.Fatal(err)
	}
	bobPage, err := svc.AlertTimeline(bob, "", "", 20, "")
	if err != nil {
		t.Fatal(err)
	}
	if alicePage.TotalCount != 2 || bobPage.TotalCount != 0 {
		t.Fatalf("tenant history was not isolated: alice=%+v bob=%+v", alicePage, bobPage)
	}
}

func TestTenantAlertHistoryIsDerivedFromSharedMarketEvents(t *testing.T) {
	svc, _, st := alertTestService(t, "tenant-derived-history.db")
	defer svc.Close()
	ctx := context.Background()
	at := time.Now().UTC().Add(-30 * time.Second).Truncate(time.Second)
	svc.alerts.applyWatch(
		[]alert.WatchEntry{{Symbol: "BTCUSDT", Rank: 1, QuoteVolumeUSD: 1e9}},
		[]domain.WatchSymbol{{Symbol: "BTCUSDT", Rank: 1, QuoteVolumeUSD: 1e9, UpdatedAt: at}},
		at, "")
	if err := st.InsertLiquidation(ctx, alertTestEvent("shared-market-event", "BTCUSDT", "short", at, 150000)); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveAlertStrategySettingsForTenant(ctx, "alice", domain.AlertStrategySettings{
		WindowMinutes: 3, BTCUSD: 100000, ETHUSD: 200000, OtherUSD: 100000,
		VWAPRangeHours: 24, VWAPMinDrawdownPct: 2.2, VWAPDonchianPct: 35,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveAlertStrategySettingsForTenant(ctx, "bob", domain.AlertStrategySettings{
		WindowMinutes: 3, BTCUSD: 200000, ETHUSD: 200000, OtherUSD: 100000,
		VWAPRangeHours: 24, VWAPMinDrawdownPct: 2.2, VWAPDonchianPct: 35,
	}); err != nil {
		t.Fatal(err)
	}

	alice := WithTenantID(ctx, "alice")
	page, err := svc.AlertTimeline(alice, "BTCUSDT", "", 20, "")
	if err != nil {
		t.Fatal(err)
	}
	if page.TotalCount != 1 || len(page.Rows) != 1 || page.Rows[0].Alert == nil || page.Rows[0].Alert.ThresholdUSD != 100000 {
		t.Fatalf("alice did not derive a private alert from the shared event: %+v", page)
	}

	bob := WithTenantID(ctx, "bob")
	bobPage, err := svc.AlertTimeline(bob, "BTCUSDT", "", 20, "")
	if err != nil {
		t.Fatal(err)
	}
	if bobPage.TotalCount != 0 {
		t.Fatalf("bob incorrectly derived an alert below its threshold: %+v", bobPage)
	}

	publicPage, err := svc.AlertTimeline(ctx, "BTCUSDT", "", 20, "")
	if err != nil {
		t.Fatal(err)
	}
	if publicPage.TotalCount != 0 {
		t.Fatalf("public market store unexpectedly exposed tenant alert history: %+v", publicPage)
	}
}
