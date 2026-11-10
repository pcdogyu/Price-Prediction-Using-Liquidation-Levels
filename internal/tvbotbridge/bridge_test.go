package tvbotbridge

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/config"
	tvbotbinance "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotbinance"
	tvbotconfig "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotconfig"
	tvbotgate "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotgate"
	tvbotokx "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotokx"
	tvbotsecurity "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotsecurity"
	tvbotserver "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotserver"
	tvbotstorage "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotstorage"
	tvbottrading "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
	_ "modernc.org/sqlite"
)

func TestResolveTVBotDatabaseFallsBackUntilTargetExists(t *testing.T) {
	dir := t.TempDir()
	mainDatabase := filepath.Join(dir, "liquidation.db")
	tvbotDatabase := filepath.Join(dir, "tvbot.db")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := config.Config{DatabasePath: mainDatabase, TVBotDatabasePath: tvbotDatabase}
	if got := resolveTVBotDatabase(cfg, logger); got != mainDatabase {
		t.Fatalf("unmarked TVBot target should use unified database, got %q", got)
	}
	db, err := sql.Open("sqlite", tvbotDatabase)
	if err != nil {
		t.Fatal(err)
	}
	mainAbs, err := filepath.Abs(mainDatabase)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `CREATE TABLE tvbot_migrations(source_path TEXT PRIMARY KEY,completed_ts INTEGER NOT NULL)`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `INSERT INTO tvbot_migrations(source_path,completed_ts) VALUES('legacy',1)`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if got := resolveTVBotDatabase(cfg, logger); got != mainDatabase {
		t.Fatalf("legacy-only TVBot target should use unified database, got %q", got)
	}
	db, err = sql.Open("sqlite", tvbotDatabase)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `INSERT INTO tvbot_migrations(source_path,completed_ts) VALUES(?,1)`, mainAbs); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if got := resolveTVBotDatabase(cfg, logger); got != tvbotDatabase {
		t.Fatalf("completed TVBot target should be selected, got %q", got)
	}
}

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
