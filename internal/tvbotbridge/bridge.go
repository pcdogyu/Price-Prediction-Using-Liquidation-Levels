package tvbotbridge

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/config"
	tvbotbinance "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotbinance"
	tvbotconfig "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotconfig"
	tvbotenv "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotenv"
	tvbotgate "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotgate"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotmigration"
	tvbotokx "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotokx"
	tvbotsecurity "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotsecurity"
	tvbotserver "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotserver"
	tvbotstorage "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotstorage"
	tvbotupgrade "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotupgrade"
)

// Bridge owns the TradingView bot runtime inside the liquidation service. The
// order store intentionally points at the liquidation database so the two
// applications share one durable history and one backup boundary.
type Bridge struct {
	Handler       *tvbotserver.Server
	ConfigStore   *tvbotconfig.Store
	Orders        *tvbotstorage.OrderStore
	Tenants       *tenantManager
	Upgrade       *tvbotupgrade.Manager
	upgradeStatus string
}

// TradeTargetForUser exposes the tenant-scoped API-key selection to the
// liquidation alert service without coupling the application package to the
// TVBot tenant manager.
func (b *Bridge) TradeTargetForUser(username string) (string, string, string, bool) {
	if b == nil || b.Tenants == nil {
		return "", "", "", false
	}
	return b.Tenants.TradeTargetForUser(username)
}

// WebhookTokenForUser generates the tenant-scoped token used by liquidation
// alert webhooks. The alert service sends the authenticated username as its
// tenant id, while TVBot templates use the encoded tenant id; the former is
// intentional because the webhook router accepts both representations.
func (b *Bridge) WebhookTokenForUser(username, tokenNonce string) (string, bool) {
	if b == nil || b.Tenants == nil || b.Handler == nil {
		return "", false
	}
	username = strings.TrimSpace(username)
	tokenNonce = strings.TrimSpace(tokenNonce)
	if username == "" || tokenNonce == "" {
		return "", false
	}
	scope, err := b.Tenants.ForUserScope(username)
	if err != nil || scope.ConfigStore == nil {
		return "", false
	}
	tradingCfg := scope.ConfigStore.Get().Trading
	exchange := tvbottrading.NormalizeExchange(tradingCfg.TargetExchange)
	if !tvbottrading.ValidTargetExchange(exchange) {
		return "", false
	}
	tradeEnv := strings.ToLower(strings.TrimSpace(tradingCfg.TargetTradeEnv))
	if tradeEnv != tvbottrading.TradeEnvDemo && tradeEnv != tvbottrading.TradeEnvLive {
		tradeEnv = strings.ToLower(strings.TrimSpace(tradingCfg.Env))
	}
	if tradeEnv != tvbottrading.TradeEnvDemo && tradeEnv != tvbottrading.TradeEnvLive {
		return "", false
	}
	signal := tvbottrading.Signal{
		TenantID:       username,
		TargetExchange: exchange,
		APIID:          strings.TrimSpace(tradingCfg.TargetAPIID),
		TradeEnv:       tradeEnv,
		TokenNonce:     tokenNonce,
	}
	return b.Handler.Token.Generate(signal.CanonicalNonceWebhookTokenPayload()), true
}

func New(cfg config.Config, logger *slog.Logger, build tvbotserver.BuildInfo) (*Bridge, error) {
	tvCfg, err := loadConfig(cfg.TVBotConfigPath)
	if err != nil {
		return nil, err
	}
	tvCfg.DatabaseFile = cfg.DatabasePath
	if strings.TrimSpace(tvCfg.Server.Addr) == "" {
		tvCfg.Server.Addr = cfg.Address
	}
	legacyDatabase := resolvePath(cfg.TVBotLegacyDataPath, cfg.TVBotLegacyDatabasePath)
	if migrated, err := tvbotmigration.MigrateSQLite(context.Background(), cfg.DatabasePath, legacyDatabase); err != nil {
		return nil, err
	} else if migrated && logger != nil {
		logger.Info("legacy tvbot database migrated", "source", legacyDatabase)
	}

	legacyOrders := ""
	if strings.TrimSpace(tvCfg.DataFile) != "" {
		legacyOrders = resolvePath(cfg.TVBotLegacyDataPath, tvCfg.DataFile)
	}
	orders, err := tvbotstorage.NewSQLiteOrderStore(cfg.DatabasePath, legacyOrders)
	if err != nil {
		return nil, err
	}

	secrets := tvbotenv.Load()
	credentialsDir := cfg.TVBotCredentialsDir
	if strings.TrimSpace(credentialsDir) == "" {
		credentialsDir = cfg.TVBotDataPath
	}
	okxFile := filepath.Join(credentialsDir, "okx-credentials.json")
	binanceFile := filepath.Join(credentialsDir, "binance-credentials.json")
	gateFile := filepath.Join(credentialsDir, "gate-credentials.json")
	// A caller that constructs Config manually without a unified directory may
	// still opt into the legacy environment file locations.
	if strings.TrimSpace(cfg.TVBotCredentialsDir) == "" {
		if value := strings.TrimSpace(os.Getenv("OKX_CREDENTIALS_FILE")); value != "" {
			okxFile = value
		}
		if value := strings.TrimSpace(os.Getenv("BINANCE_CREDENTIALS_FILE")); value != "" {
			binanceFile = value
		}
		if value := strings.TrimSpace(os.Getenv("GATE_CREDENTIALS_FILE")); value != "" {
			gateFile = value
		}
	}
	if err := copyLegacyCredential(filepath.Join(cfg.TVBotLegacyCredentialsDir, "okx-credentials.json"), okxFile); err != nil {
		return nil, err
	}
	if err := copyLegacyCredential(filepath.Join(cfg.TVBotLegacyCredentialsDir, "binance-credentials.json"), binanceFile); err != nil {
		return nil, err
	}
	if err := copyLegacyCredential(filepath.Join(cfg.TVBotLegacyCredentialsDir, "gate-credentials.json"), gateFile); err != nil {
		return nil, err
	}
	configuredEnv := strings.ToLower(strings.TrimSpace(tvCfg.Trading.Env))
	if configuredEnv != "live" {
		configuredEnv = "demo"
	}
	okxDemoFile := environmentCredentialPath(okxFile, "demo")
	okxLiveFile := environmentCredentialPath(okxFile, "live")
	binanceDemoFile := environmentCredentialPath(binanceFile, "demo")
	binanceLiveFile := environmentCredentialPath(binanceFile, "live")
	gateDemoFile := environmentCredentialPath(gateFile, "demo")
	gateLiveFile := environmentCredentialPath(gateFile, "live")
	if configuredEnv == "live" {
		if err := copyLegacyCredential(okxFile, okxLiveFile); err != nil {
			return nil, err
		}
		if err := copyLegacyCredential(binanceFile, binanceLiveFile); err != nil {
			return nil, err
		}
		if err := copyLegacyCredential(gateFile, gateLiveFile); err != nil {
			return nil, err
		}
	} else {
		if err := copyLegacyCredential(okxFile, okxDemoFile); err != nil {
			return nil, err
		}
		if err := copyLegacyCredential(binanceFile, binanceDemoFile); err != nil {
			return nil, err
		}
		if err := copyLegacyCredential(gateFile, gateDemoFile); err != nil {
			return nil, err
		}
	}
	okxInitial := tvbotokx.Credentials{}
	binanceInitial := tvbotbinance.Credentials{}
	gateInitial := tvbotgate.Credentials{}
	if configuredEnv == "live" {
		okxInitial = tvbotokx.Credentials{APIKey: secrets.OKXAPIKey, SecretKey: secrets.OKXSecretKey, Passphrase: secrets.OKXPassphrase}
		binanceInitial = tvbotbinance.Credentials{APIKey: secrets.BinanceAPIKey, SecretKey: secrets.BinanceSecretKey}
		gateInitial = tvbotgate.Credentials{APIKey: secrets.GateAPIKey, SecretKey: secrets.GateSecretKey}
	}
	okxCredentials, err := tvbotokx.NewCredentialStore(okxDemoFile, okxInitial)
	if err != nil {
		_ = orders.Close()
		return nil, err
	}
	okxLiveCredentials, err := tvbotokx.NewCredentialStore(okxLiveFile, func() tvbotokx.Credentials {
		if configuredEnv == "live" {
			return tvbotokx.Credentials{APIKey: secrets.OKXAPIKey, SecretKey: secrets.OKXSecretKey, Passphrase: secrets.OKXPassphrase}
		}
		return tvbotokx.Credentials{}
	}())
	if err != nil {
		_ = orders.Close()
		return nil, err
	}
	binanceCredentials, err := tvbotbinance.NewCredentialStore(binanceDemoFile, binanceInitial)
	if err != nil {
		_ = orders.Close()
		return nil, err
	}
	binanceLiveCredentials, err := tvbotbinance.NewCredentialStore(binanceLiveFile, func() tvbotbinance.Credentials {
		if configuredEnv == "live" {
			return tvbotbinance.Credentials{APIKey: secrets.BinanceAPIKey, SecretKey: secrets.BinanceSecretKey}
		}
		return tvbotbinance.Credentials{}
	}())
	if err != nil {
		_ = orders.Close()
		return nil, err
	}
	gateCredentials, err := tvbotgate.NewCredentialStore(gateDemoFile, gateInitial)
	if err != nil {
		_ = orders.Close()
		return nil, err
	}
	gateLiveCredentials, err := tvbotgate.NewCredentialStore(gateLiveFile, func() tvbotgate.Credentials {
		if configuredEnv == "live" {
			return tvbotgate.Credentials{APIKey: secrets.GateAPIKey, SecretKey: secrets.GateSecretKey}
		}
		return tvbotgate.Credentials{}
	}())
	if err != nil {
		_ = orders.Close()
		return nil, err
	}
	userCredentials, err := newUserCredentialResolver(
		credentialsDir,
		legacyOwner(cfg, secrets.AdminUser),
		okxFile,
		binanceFile,
		gateFile,
		tvbotokx.Credentials{APIKey: secrets.OKXAPIKey, SecretKey: secrets.OKXSecretKey, Passphrase: secrets.OKXPassphrase},
		tvbotbinance.Credentials{APIKey: secrets.BinanceAPIKey, SecretKey: secrets.BinanceSecretKey},
		tvbotgate.Credentials{APIKey: secrets.GateAPIKey, SecretKey: secrets.GateSecretKey},
		tvCfg.Trading.Env,
	)
	if err != nil {
		_ = orders.Close()
		return nil, err
	}
	rootConfigStore := tvbotconfig.NewStore(cfg.TVBotConfigPath, tvCfg)
	tenantDataPath := strings.TrimSpace(cfg.TVBotTenantDataPath)
	if tenantDataPath == "" {
		tenantDataPath = filepath.Join(cfg.TVBotDataPath, "tenants")
	}
	tenants := newTenantManager(tenantDataPath, legacyOwner(cfg, secrets.AdminUser), rootConfigStore, orders, userCredentials)

	runner, err := tvbotupgrade.NewShellRunnerFromEnv()
	if err != nil {
		_ = orders.Close()
		return nil, err
	}
	if strings.TrimSpace(cfg.UpgradeWorkDir) != "" {
		runner.WorkDir = cfg.UpgradeWorkDir
	}
	if strings.TrimSpace(cfg.UpgradeBinaryPath) != "" {
		runner.BinaryPath = cfg.UpgradeBinaryPath
	} else if runner.WorkDir != "" {
		runner.BinaryPath = filepath.Join(runner.WorkDir, "liquidation-predictor")
	}
	if strings.TrimSpace(cfg.UpgradeServiceName) != "" {
		runner.ServiceName = cfg.UpgradeServiceName
	}
	// The old tvbot environment may contain a restart command for the retired
	// service. Unified upgrades always restart liquidation-predictor instead.
	runner.RestartCommand = ""
	runner.BuildPackage = "./cmd/server"
	statusPath := cfg.UpgradeStatusPath
	if strings.TrimSpace(statusPath) == "" {
		statusPath = filepath.Join(cfg.TVBotDataPath, "upgrade-status.json")
	}
	upgrade := tvbotupgrade.NewManager(runner, tvbotupgrade.WithStatusFile(statusPath))

	handler := &tvbotserver.Server{
		ConfigStore: rootConfigStore,
		Orders:      orders,
		Token:       tvbotsecurity.NewTokenService(secrets.TVTokenSecret),
		Executor: tvbotserver.ExchangeExecutor{
			OKX:         tvbotokx.Trader{CredentialProvider: okxCredentials, HTTPClient: &http.Client{Timeout: 15 * time.Second}, Logger: logger},
			OKXLive:     tvbotokx.Trader{CredentialProvider: okxLiveCredentials, HTTPClient: &http.Client{Timeout: 15 * time.Second}, Logger: logger},
			Binance:     tvbotbinance.Trader{CredentialProvider: binanceCredentials, HTTPClient: &http.Client{Timeout: 15 * time.Second}, Logger: logger},
			BinanceLive: tvbotbinance.Trader{CredentialProvider: binanceLiveCredentials, HTTPClient: &http.Client{Timeout: 15 * time.Second}, Logger: logger},
			Gate:        tvbotgate.Trader{CredentialProvider: gateCredentials, HTTPClient: &http.Client{Timeout: 15 * time.Second}, Logger: logger},
			GateLive:    tvbotgate.Trader{CredentialProvider: gateLiveCredentials, HTTPClient: &http.Client{Timeout: 15 * time.Second}, Logger: logger},
		},
		OKXCredentials: okxCredentials, OKXLiveCredentials: okxLiveCredentials, BinanceCredentials: binanceCredentials, BinanceLiveCredentials: binanceLiveCredentials, GateCredentials: gateCredentials, GateLiveCredentials: gateLiveCredentials,
		UserCredentialResolver: tenants,
		TenantRouter:           tenants,
		OKXHTTPClient:          &http.Client{Timeout: 15 * time.Second}, BinanceHTTPClient: &http.Client{Timeout: 15 * time.Second}, GateHTTPClient: &http.Client{Timeout: 15 * time.Second},
		AdminToken: secrets.AdminToken, AdminUser: secrets.AdminUser, AdminPass: secrets.AdminPassword,
		Logger: logger, Upgrade: upgrade, BuildInfo: build,
	}
	return &Bridge{Handler: handler, ConfigStore: handler.ConfigStore, Orders: orders, Tenants: tenants, Upgrade: upgrade, upgradeStatus: statusPath}, nil
}

func (b *Bridge) Start(ctx context.Context) {
	if b == nil || b.Handler == nil {
		return
	}
	if b.Tenants != nil {
		b.Tenants.SetStart(func(scope tvbotserver.UserScope) {
			b.startMonitors(ctx, b.Handler.WithUserScope(scope))
		})
	}
	b.startMonitors(ctx, b.Handler)
}

func (b *Bridge) startMonitors(ctx context.Context, handler *tvbotserver.Server) {
	if handler == nil {
		return
	}
	handler.StartUSDTBalanceSampler(ctx)
	handler.StartLowMarginPositionMonitor(ctx)
	handler.StartPositionMonitor(ctx)
	handler.StartStalePendingOrderCancelMonitor(ctx)
	handler.StartTradeFillMonitor(ctx)
	handler.StartCoinpairBlockCleaner(ctx)
	handler.StartSymbolCatalogSyncer(ctx)
}

func (b *Bridge) Close() error {
	if b == nil || b.Orders == nil {
		return nil
	}
	var first error
	if b.Tenants != nil {
		first = b.Tenants.Close()
	}
	if err := b.Orders.Close(); err != nil && first == nil {
		first = err
	}
	return first
}

func legacyOwner(cfg config.Config, fallback string) string {
	if owner := strings.TrimSpace(cfg.AuthUsername); owner != "" {
		return owner
	}
	if owner := strings.TrimSpace(fallback); owner != "" {
		return owner
	}
	return "admin"
}

func loadConfig(path string) (tvbotconfig.Config, error) {
	if strings.TrimSpace(path) == "" {
		return tvbotconfig.Default(), nil
	}
	cfg, err := tvbotconfig.Load(path)
	if err != nil && errors.Is(err, os.ErrNotExist) {
		return tvbotconfig.Default(), nil
	}
	return cfg, err
}

func resolvePath(root, value string) string {
	if filepath.IsAbs(value) || strings.TrimSpace(root) == "" {
		return value
	}
	return filepath.Join(root, value)
}

func environmentCredentialPath(path, env string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return path
	}
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(path, ext)
	if ext == "" {
		ext = ".json"
	}
	return base + "-" + env + ext
}

func copyLegacyCredential(source, target string) error {
	source, target = strings.TrimSpace(source), strings.TrimSpace(target)
	if source == "" || target == "" || sameFilePath(source, target) {
		return nil
	}
	if _, err := os.Stat(target); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	data, err := os.ReadFile(source)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(target, data, 0o600); err != nil {
		return err
	}
	return nil
}

func sameFilePath(a, b string) bool {
	aa, errA := filepath.Abs(a)
	bb, errB := filepath.Abs(b)
	return errA == nil && errB == nil && strings.EqualFold(filepath.Clean(aa), filepath.Clean(bb))
}
