package tvbotbridge

import (
	"testing"
	"time"

	tvbotbinance "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotbinance"
	tvbotconfig "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotconfig"
	tvbotgate "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotgate"
	tvbotokx "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotokx"
	tvbotstorage "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotstorage"
	tvbottrading "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

func TestTenantManagerSeparatesConfigAndOrderData(t *testing.T) {
	root := t.TempDir()
	rootConfig := tvbotconfig.NewStore(root+"/root-config.json", tvbotconfig.Default())
	rootOrders, err := tvbotstorage.NewSQLiteOrderStore(root+"/root.db", "")
	if err != nil {
		t.Fatal(err)
	}
	defer rootOrders.Close()
	credentials, err := newUserCredentialResolver(root+"/credentials", "admin", "", "", "", tvbotokx.Credentials{}, tvbotbinance.Credentials{}, tvbotgate.Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	manager := newTenantManager(root+"/tenants", "admin", rootConfig, rootOrders, credentials)
	alice, err := manager.ForUserScope("alice")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := manager.ForUserScope("bob")
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if alice.TenantID == bob.TenantID || alice.ConfigStore == bob.ConfigStore || alice.Orders == bob.Orders {
		t.Fatal("users must have distinct tenant resources")
	}
	if _, err := alice.ConfigStore.Update(func(cfg *tvbotconfig.Config) error {
		cfg.Trading.OrderAmountUSDT = 321
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if bob.ConfigStore.Get().Trading.OrderAmountUSDT == 321 {
		t.Fatal("bob inherited alice's order settings")
	}
	if _, _, err := alice.Orders.RecordAccepted(tvbottrading.Signal{Action: tvbottrading.ActionLong, Coinpair: "BTCUSDT"}, "alice-order", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if len(alice.Orders.List(20)) != 1 || len(bob.Orders.List(20)) != 0 {
		t.Fatalf("order history crossed tenants: alice=%d bob=%d", len(alice.Orders.List(20)), len(bob.Orders.List(20)))
	}
	if got := manager.DefaultTenantID(); got != tenantIDForUser("admin") {
		t.Fatalf("default tenant=%q", got)
	}
	routed, err := manager.ForTenantID("alice")
	if err != nil {
		t.Fatalf("username tenant route: %v", err)
	}
	if routed.Orders != alice.Orders || routed.ConfigStore != alice.ConfigStore {
		t.Fatal("username tenant route did not resolve alice resources")
	}
}
