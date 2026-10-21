package tvbotbridge

import (
	"path/filepath"
	"reflect"
	"testing"

	tvbotbinance "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotbinance"
	tvbotconfig "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotconfig"
	tvbotgate "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotgate"
	tvbotokx "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotokx"
	tvbotsecurity "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotsecurity"
	tvbotserver "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotserver"
	tvbotstorage "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotstorage"
	tvbottrading "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

func TestWebhookTokenForUserMatchesUsernameScopedPayload(t *testing.T) {
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
	defer manager.Close()
	scope, err := manager.ForUserScope("wyx")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scope.ConfigStore.Update(func(cfg *tvbotconfig.Config) error {
		cfg.Trading.TargetExchange = tvbottrading.ExchangeGate
		cfg.Trading.TargetAPIID = "wyx"
		cfg.Trading.TargetTradeEnv = tvbottrading.TradeEnvDemo
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	bridge := &Bridge{
		Handler: &tvbotserver.Server{Token: tvbotsecurity.NewTokenService("webhook-token-test-secret")},
		Tenants: manager,
	}
	token, ok := bridge.WebhookTokenForUser("wyx", "nonce-value")
	if !ok || token == "" {
		t.Fatalf("tenant webhook token unavailable: ok=%v", ok)
	}
	signal := tvbottrading.Signal{
		TenantID:       "wyx",
		TargetExchange: tvbottrading.ExchangeGate,
		APIID:          "wyx",
		TradeEnv:       tvbottrading.TradeEnvDemo,
		TokenNonce:     "nonce-value",
	}
	if !bridge.Handler.Token.Validate(signal.CanonicalNonceWebhookTokenPayload(), token) {
		t.Fatal("tenant webhook token does not validate for the username-scoped payload")
	}
}

func TestTenantConfigPersistsTableColumnsForUser(t *testing.T) {
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
	scope, err := manager.ForUserScope("wyx")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"actions", "avg_price", "exchange"}
	if _, err := scope.ConfigStore.Update(func(cfg *tvbotconfig.Config) error {
		cfg.UI.TableColumns.Positions = want
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}

	persisted, err := tvbotconfig.Load(filepath.Join(root, "tenants", userDirectoryName("wyx"), "tvbot-config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted.UI.TableColumns.Positions) < len(want) || !reflect.DeepEqual(persisted.UI.TableColumns.Positions[:len(want)], want) {
		t.Fatalf("user table column order was not persisted: %#v", persisted.UI.TableColumns.Positions)
	}
}
