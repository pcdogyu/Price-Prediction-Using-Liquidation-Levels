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
	Upgrade       *tvbotupgrade.Manager
	upgradeStatus string
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
	okxCredentials, err := tvbotokx.NewCredentialStore(okxFile, tvbotokx.Credentials{
		APIKey: secrets.OKXAPIKey, SecretKey: secrets.OKXSecretKey, Passphrase: secrets.OKXPassphrase,
	})
	if err != nil {
		_ = orders.Close()
		return nil, err
	}
	binanceCredentials, err := tvbotbinance.NewCredentialStore(binanceFile, tvbotbinance.Credentials{
		APIKey: secrets.BinanceAPIKey, SecretKey: secrets.BinanceSecretKey,
	})
	if err != nil {
		_ = orders.Close()
		return nil, err
	}
	gateCredentials, err := tvbotgate.NewCredentialStore(gateFile, tvbotgate.Credentials{
		APIKey: secrets.GateAPIKey, SecretKey: secrets.GateSecretKey,
	})
	if err != nil {
		_ = orders.Close()
		return nil, err
	}
	userCredentials, err := newUserCredentialResolver(
		credentialsDir,
		cfg.AuthUsername,
		okxFile,
		binanceFile,
		gateFile,
		tvbotokx.Credentials{APIKey: secrets.OKXAPIKey, SecretKey: secrets.OKXSecretKey, Passphrase: secrets.OKXPassphrase},
		tvbotbinance.Credentials{APIKey: secrets.BinanceAPIKey, SecretKey: secrets.BinanceSecretKey},
		tvbotgate.Credentials{APIKey: secrets.GateAPIKey, SecretKey: secrets.GateSecretKey},
	)
	if err != nil {
		_ = orders.Close()
		return nil, err
	}

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
		ConfigStore: tvbotconfig.NewStore(cfg.TVBotConfigPath, tvCfg),
		Orders:      orders,
		Token:       tvbotsecurity.NewTokenService(secrets.TVTokenSecret),
		Executor: tvbotserver.ExchangeExecutor{
			OKX:     tvbotokx.Trader{CredentialProvider: okxCredentials, HTTPClient: &http.Client{Timeout: 15 * time.Second}, Logger: logger},
			Binance: tvbotbinance.Trader{CredentialProvider: binanceCredentials, HTTPClient: &http.Client{Timeout: 15 * time.Second}, Logger: logger},
			Gate:    tvbotgate.Trader{CredentialProvider: gateCredentials, HTTPClient: &http.Client{Timeout: 15 * time.Second}, Logger: logger},
		},
		OKXCredentials: okxCredentials, BinanceCredentials: binanceCredentials, GateCredentials: gateCredentials,
		UserCredentialResolver: userCredentials,
		OKXHTTPClient:          &http.Client{Timeout: 15 * time.Second}, BinanceHTTPClient: &http.Client{Timeout: 15 * time.Second}, GateHTTPClient: &http.Client{Timeout: 15 * time.Second},
		AdminToken: secrets.AdminToken, AdminUser: secrets.AdminUser, AdminPass: secrets.AdminPassword,
		Logger: logger, Upgrade: upgrade, BuildInfo: build,
	}
	return &Bridge{Handler: handler, ConfigStore: handler.ConfigStore, Orders: orders, Upgrade: upgrade, upgradeStatus: statusPath}, nil
}

func (b *Bridge) Start(ctx context.Context) {
	if b == nil || b.Handler == nil {
		return
	}
	b.Handler.StartUSDTBalanceSampler(ctx)
	b.Handler.StartLowMarginPositionMonitor(ctx)
	b.Handler.StartPositionMonitor(ctx)
	b.Handler.StartStalePendingOrderCancelMonitor(ctx)
	b.Handler.StartTradeFillMonitor(ctx)
	b.Handler.StartCoinpairBlockCleaner(ctx)
	b.Handler.StartSymbolCatalogSyncer(ctx)
}

func (b *Bridge) Close() error {
	if b == nil || b.Orders == nil {
		return nil
	}
	return b.Orders.Close()
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
