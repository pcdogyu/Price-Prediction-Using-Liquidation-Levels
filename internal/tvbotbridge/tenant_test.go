package tvbotbridge

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	appstore "github.com/pcdogyu/price-prediction-liquidation-levels/internal/store"
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

func TestTenantManagerPrunesOpenAndInactiveTenantStores(t *testing.T) {
	root := t.TempDir()
	rootConfig := tvbotconfig.NewStore(filepath.Join(root, "root-config.json"), tvbotconfig.Default())
	rootOrders, err := tvbotstorage.NewSQLiteOrderStore(filepath.Join(root, "root.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer rootOrders.Close()
	credentials, err := newUserCredentialResolver(filepath.Join(root, "credentials"), "admin", "", "", "", tvbotokx.Credentials{}, tvbotbinance.Credentials{}, tvbotgate.Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	manager := newTenantManager(filepath.Join(root, "tenants"), "admin", rootConfig, rootOrders, credentials)
	alice, err := manager.ForUserScope("alice")
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()

	old := time.Now().UTC().Add(-48 * time.Hour)
	cutoff := time.Now().UTC().Add(-24 * time.Hour)
	insertEvent := func(store *tvbotstorage.OrderStore, id string, at time.Time) {
		t.Helper()
		if err := store.InsertTradeMonitorEvent(tvbotstorage.TradeMonitorEvent{
			EventID: id, EventTime: at, Exchange: tvbottrading.ExchangeBinance, APIID: "main", Symbol: "BTCUSDT", EventType: "poll_error",
		}); err != nil {
			t.Fatal(err)
		}
	}
	insertEvent(alice.Orders, "alice-old", old)
	if err := alice.Orders.CachePayload("alice-old", map[string]string{"value": "old"}, old); err != nil {
		t.Fatal(err)
	}

	charlieDir := filepath.Join(root, "tenants", userDirectoryName("charlie"))
	if err := os.MkdirAll(charlieDir, 0o700); err != nil {
		t.Fatal(err)
	}
	charlie, err := tvbotstorage.NewSQLiteOrderStore(filepath.Join(charlieDir, "tvbot.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	insertEvent(charlie, "charlie-old", old)
	if err := charlie.CachePayload("charlie-old", map[string]string{"value": "old"}, old); err != nil {
		t.Fatal(err)
	}
	if err := charlie.Close(); err != nil {
		t.Fatal(err)
	}

	events, cache, err := manager.PruneRetention(context.Background(), cutoff, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	if events != 2 || cache != 2 {
		t.Fatalf("removed events=%d cache=%d, want 2 each", events, cache)
	}
	if events, err := alice.Orders.ListTradeMonitorEvents(tvbotstorage.TradeMonitorFilter{}, 10); err != nil || len(events) != 0 {
		t.Fatalf("alice old events remain: %d err=%v", len(events), err)
	}
	if _, found, err := alice.Orders.CachedPayload("alice-old"); err != nil || found {
		t.Fatalf("alice old cache found=%v err=%v", found, err)
	}
	charlie, err = tvbotstorage.NewSQLiteOrderStore(filepath.Join(charlieDir, "tvbot.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer charlie.Close()
	if events, err := charlie.ListTradeMonitorEvents(tvbotstorage.TradeMonitorFilter{}, 10); err != nil || len(events) != 0 {
		t.Fatalf("charlie old events remain: %d err=%v", len(events), err)
	}
	if _, found, err := charlie.CachedPayload("charlie-old"); err != nil || found {
		t.Fatalf("charlie old cache found=%v err=%v", found, err)
	}
}

func TestConfiguredBinanceSymbolsPrefersCoinpairAndFiltersNonUSDT(t *testing.T) {
	cfg := tvbotconfig.Config{Symbols: map[string]tvbotconfig.SymbolConfig{
		"btc-key":          {Coinpair: "BTC-USDT-SWAP"},
		"ETHUSDT":          {},
		"analysis:SOLUSDT": {Coinpair: "SOLUSDT"},
		"DOGEUSDC":         {Coinpair: "DOGEUSDC"},
	}}
	got := configuredBinanceSymbols(cfg)
	want := []string{"BTCUSDT", "ETHUSDT", "SOLUSDT"}
	if len(got) != len(want) {
		t.Fatalf("symbols=%v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("symbols=%v, want %v", got, want)
		}
	}
}

func TestNormalizeBinanceCandidateSymbolRejectsNonPerpetualForms(t *testing.T) {
	for _, raw := range []string{"BTCUSDT", "eth-usdt", "SOL/USDT"} {
		if got := normalizeBinanceCandidateSymbol(raw); got == "" {
			t.Fatalf("normalize(%q) returned empty", raw)
		}
	}
	for _, raw := range []string{"BTCUSD", "BTCUSDC", "analysis:BTCUSDT", "USDT"} {
		if got := normalizeBinanceCandidateSymbol(raw); got != "" {
			t.Fatalf("normalize(%q)=%q, want empty", raw, got)
		}
	}
}

func TestReadOnlyTenantConfigsSkipsDisabledAndDoesNotCreateLazyTenant(t *testing.T) {
	root := t.TempDir()
	rootConfig := tvbotconfig.NewStore(filepath.Join(root, "root-config.json"), tvbotconfig.Default())
	rootOrders, err := tvbotstorage.NewSQLiteOrderStore(filepath.Join(root, "root.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer rootOrders.Close()
	appDB, err := appstore.Open(filepath.Join(root, "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer appDB.Close()
	ctx := context.Background()
	if err := appDB.CreateAuthUser(ctx, "alice", "hash", "user"); err != nil {
		t.Fatal(err)
	}
	if err := appDB.CreateAuthUser(ctx, "disabled", "hash", "user"); err != nil {
		t.Fatal(err)
	}
	if err := appDB.SetAuthUserState(ctx, "disabled", "user", true); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "tenants", userDirectoryName("alice"), "tvbot-config.json")
	if err := tvbotconfig.Save(configPath, tvbotconfig.Default()); err != nil {
		t.Fatal(err)
	}
	credentials, err := newUserCredentialResolver(filepath.Join(root, "credentials"), "admin", "", "", "", tvbotokx.Credentials{}, tvbotbinance.Credentials{}, tvbotgate.Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	manager := newTenantManager(filepath.Join(root, "tenants"), "admin", rootConfig, rootOrders, credentials, appDB)
	rows, err := manager.readOnlyTenantConfigs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Username != "admin" || rows[1].Username != "alice" || !rows[1].Exists {
		t.Fatalf("tenant rows=%+v", rows)
	}
	if _, err := os.Stat(filepath.Join(root, "tenants", userDirectoryName("disabled"))); !os.IsNotExist(err) {
		t.Fatalf("disabled tenant directory unexpectedly exists: %v", err)
	}
	if _, err := manager.readOnlyScope("alice", tvbotconfig.Default()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "credentials", "users", userDirectoryName("alice"))); !os.IsNotExist(err) {
		t.Fatalf("read-only credential lookup unexpectedly created a directory: %v", err)
	}
}
