package config

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

type Config struct {
	Address, DatabasePath, ModelPath string
	BasePath                         string
	TVBotConfigPath                  string
	TVBotDataPath                    string
	TVBotLegacyDatabasePath          string
	TVBotLegacyDataPath              string
	TVBotLegacyCredentialsDir        string
	TVBotCredentialsDir              string
	TVBotTenantDataPath              string
	AlertTenantDataPath              string
	UpgradeWorkDir                   string
	UpgradeBinaryPath                string
	UpgradeServiceName               string
	UpgradeStatusPath                string
	MarketCollectorSocket            string
	AuthUsername                     string
	AuthPasswordHash                 string
	LogPath                          string
	MarketCollectorLogPath           string
	CoinGlassDebugURL                string
	CoinGlassCaptureDir              string
	CoinGlassCaptureInterval         time.Duration
	Symbols                          []string
	// DepthSymbols controls the expensive per-symbol order-book streams. It
	// may be narrower than Symbols, which keeps all configured symbols eligible
	// for candle/alert collection without opening hundreds of depth sockets.
	DepthSymbols []string
	// BootstrapSymbols receive large historical backfills during startup.
	// Other configured symbols are repaired on demand when their first alert arrives.
	BootstrapSymbols             []string
	BackfillDays                 int
	RetentionDays                int
	TrainingDays                 int
	LogRetentionDays             int
	SnapshotInterval, StaleAfter time.Duration
	MarketPollInterval           time.Duration
	MarketInfoInterval           time.Duration
	MarketFetchConcurrency       int
	// Liquidation alert strategy (top volume watch list plus alert engine).
	AlertTopSymbols int
	AlertWindow     time.Duration
	AlertQuiet      time.Duration
	AlertConfirm    time.Duration
	AlertRefresh    time.Duration
	// Minimum liquidation notional that keeps an alert cycle in its quiet
	// observation period, and the minimum opposite notional that turns an
	// aggressive signal into a steady one. Zero counts any liquidation.
	AlertFollowUpMinimumUSD, AlertOppositeMinimumUSD float64
	AlertThresholdBTCUSD, AlertThresholdETHUSD       float64
	AlertThresholdOtherUSD                           float64
	// Liquidation radar filters. The initial values come from the environment;
	// the alert page can persist the three VWAP/Donchian boundary settings per tenant.
	AlertVWAPPeriodMinutes                           int
	AlertVWAPZScore                                  float64
	AlertVWAPRangeHours                              int
	AlertVWAPMinDrawdownPct                          float64
	AlertVWAPDonchianPct                             float64
	AlertPeakDecayWindow                             time.Duration
	AlertPeakDecayRatio                              float64
	AlertAbsorptionWickRatio                         float64
	AlertDeepQuotaUSD, AlertDeepQuotaMultiplier      float64
	AlertMinProfitSpread                             float64
	AlertGridStep0Price, AlertGridStep0Pct           float64
	AlertGridStepMultiplier, AlertGridThresholdSlope float64
	// Telegram notification defaults; the alert settings page overrides them.
	TelegramBotToken, TelegramChatID, TelegramAPIBase string
	// Steady signals are optionally delivered to the configured TradingView-style
	// webhook. Credentials stay in the environment and are never persisted.
	SteadyWebhookURL, SteadyWebhookAPIID, SteadyWebhookTargetExchange   string
	SteadyWebhookTradeEnv, SteadyWebhookExchange, SteadyWebhookInterval string
	SteadyWebhookTokenNonce, SteadyWebhookToken                         string
}

func Load() Config {
	symbols := split(env("APP_SYMBOLS", "BTCUSDT,ETHUSDT"))
	depthSymbols := split(env("APP_DEPTH_SYMBOLS", ""))
	if len(depthSymbols) == 0 {
		depthSymbols = append([]string(nil), symbols...)
	}
	bootstrapSymbols := split(env("APP_BOOTSTRAP_SYMBOLS", ""))
	if len(bootstrapSymbols) == 0 {
		bootstrapSymbols = []string{"ETHUSDT"}
	}
	return Config{
		Address: env("APP_ADDRESS", ":9090"), DatabasePath: env("APP_DATABASE", "data/liquidation.db"),
		ModelPath: env("APP_MODEL", "models/model.json"), Symbols: symbols, DepthSymbols: depthSymbols, BootstrapSymbols: bootstrapSymbols,
		BasePath: env("APP_BASE_PATH", "/"), AuthUsername: strings.TrimSpace(os.Getenv("APP_AUTH_USERNAME")),
		AuthPasswordHash: strings.TrimSpace(os.Getenv("APP_AUTH_PASSWORD_HASH")), LogPath: strings.TrimSpace(os.Getenv("APP_LOG_PATH")), MarketCollectorLogPath: strings.TrimSpace(os.Getenv("APP_MARKET_COLLECTOR_LOG_PATH")),
		TVBotConfigPath:           env("APP_TVBOT_CONFIG", "data/tvbot-config.json"),
		TVBotDataPath:             env("APP_TVBOT_DATA", "data"),
		TVBotLegacyDatabasePath:   env("APP_TVBOT_LEGACY_DATABASE", "tvbot.db"),
		TVBotLegacyDataPath:       env("APP_TVBOT_LEGACY_DATA", "data"),
		TVBotLegacyCredentialsDir: env("APP_TVBOT_LEGACY_CREDENTIALS_DIR", "data"),
		TVBotCredentialsDir:       env("APP_TVBOT_CREDENTIALS_DIR", "data"),
		TVBotTenantDataPath:       env("APP_TVBOT_TENANTS", "data/tvbot-tenants"),
		AlertTenantDataPath:       env("APP_ALERT_TENANTS", "data/alert-tenants"),
		UpgradeWorkDir:            env("APP_UPGRADE_WORKDIR", ""),
		UpgradeBinaryPath:         env("APP_UPGRADE_BINARY", ""),
		UpgradeServiceName:        env("APP_UPGRADE_SERVICE", "liquidation-predictor.service"),
		UpgradeStatusPath:         env("APP_UPGRADE_STATUS", ""),
		MarketCollectorSocket:     env("APP_MARKET_COLLECTOR_SOCKET", "data/market-collector.sock"),
		CoinGlassDebugURL:         env("APP_COINGLASS_DEBUG_URL", "http://127.0.0.1:9222"), CoinGlassCaptureDir: env("APP_COINGLASS_CAPTURE_DIR", "data/coinglass-captures"), CoinGlassCaptureInterval: envDuration("APP_COINGLASS_CAPTURE_INTERVAL", 10*time.Minute),
		LogRetentionDays: envInt("APP_LOG_RETENTION_DAYS", 7),
		BackfillDays:     envInt("APP_BACKFILL_DAYS", 1), RetentionDays: envInt("APP_RETENTION_DAYS", 180), TrainingDays: envInt("APP_TRAINING_DAYS", 30), SnapshotInterval: envDuration("APP_SNAPSHOT_INTERVAL", 5*time.Minute),
		StaleAfter:               envDuration("APP_STALE_AFTER", 90*time.Second),
		MarketPollInterval:       envDuration("APP_MARKET_POLL_INTERVAL", 5*time.Minute),
		MarketInfoInterval:       envDuration("APP_MARKET_INFO_INTERVAL", 5*time.Minute),
		MarketFetchConcurrency:   envInt("APP_MARKET_FETCH_CONCURRENCY", 5),
		AlertTopSymbols:          envInt("APP_ALERT_TOP_SYMBOLS", domain.AlertTopSymbolsDefault),
		AlertWindow:              envDuration("APP_ALERT_WINDOW", 3*time.Minute),
		AlertQuiet:               envDuration("APP_ALERT_QUIET", 10*time.Minute),
		AlertConfirm:             envDuration("APP_ALERT_CONFIRM", 30*time.Minute),
		AlertRefresh:             envDuration("APP_ALERT_REFRESH", 10*time.Minute),
		AlertFollowUpMinimumUSD:  envFloat("APP_ALERT_FOLLOW_UP_MIN_USD", 0),
		AlertOppositeMinimumUSD:  envFloat("APP_ALERT_OPPOSITE_MIN_USD", 0),
		AlertThresholdBTCUSD:     envFloat("APP_ALERT_THRESHOLD_BTC_USD", 300000),
		AlertThresholdETHUSD:     envFloat("APP_ALERT_THRESHOLD_ETH_USD", 200000),
		AlertThresholdOtherUSD:   envFloat("APP_ALERT_THRESHOLD_OTHER_USD", 100000),
		AlertVWAPPeriodMinutes:   envInt("APP_ALERT_VWAP_PERIOD_MINUTES", 240),
		AlertVWAPZScore:          envFloat("APP_ALERT_VWAP_ZSCORE", 0.8),
		AlertVWAPRangeHours:      envInt("APP_ALERT_VWAP_RANGE_HOURS", 24),
		AlertVWAPMinDrawdownPct:  envFloat("APP_ALERT_VWAP_MIN_DRAWDOWN_PCT", 2.2),
		AlertVWAPDonchianPct:     envFloat("APP_ALERT_VWAP_DONCHIAN_PCT", 35),
		AlertPeakDecayWindow:     envDuration("APP_ALERT_PEAK_DECAY_WINDOW", 30*time.Second),
		AlertPeakDecayRatio:      envFloat("APP_ALERT_PEAK_DECAY_RATIO", 0.4),
		AlertAbsorptionWickRatio: envFloat("APP_ALERT_ABSORPTION_WICK_RATIO", 0.35),
		AlertDeepQuotaUSD:        envFloat("APP_ALERT_DEEP_QUOTA_USD", 0),
		AlertDeepQuotaMultiplier: envFloat("APP_ALERT_DEEP_QUOTA_MULTIPLIER", 2),
		AlertMinProfitSpread:     envFloat("APP_ALERT_MIN_PROFIT_SPREAD", 0.007),
		AlertGridStep0Price:      envFloat("APP_ALERT_GRID_STEP0_PRICE", 0),
		AlertGridStep0Pct:        envFloat("APP_ALERT_GRID_STEP0_PCT", 0.003),
		AlertGridStepMultiplier:  envFloat("APP_ALERT_GRID_STEP_MULTIPLIER", 1.4),
		AlertGridThresholdSlope:  envFloat("APP_ALERT_GRID_THRESHOLD_SLOPE", 0.5),
		TelegramBotToken:         strings.TrimSpace(os.Getenv("APP_TELEGRAM_BOT_TOKEN")),
		TelegramChatID:           strings.TrimSpace(os.Getenv("APP_TELEGRAM_CHAT_ID")),
		TelegramAPIBase:          env("APP_TELEGRAM_API_BASE", "https://api.telegram.org"),
		SteadyWebhookURL:         env("APP_STEADY_WEBHOOK_URL", "https://tvbot.lmitis.com/tvorder"),
		// The liquidation alert webhook is an order source for the Binance demo
		// account. An empty API ID lets tvbot resolve its active Binance account.
		SteadyWebhookAPIID:          env("APP_STEADY_WEBHOOK_API_ID", ""),
		SteadyWebhookTargetExchange: env("APP_STEADY_WEBHOOK_TARGET_EXCHANGE", "binance"),
		SteadyWebhookTradeEnv:       env("APP_STEADY_WEBHOOK_TRADE_ENV", "demo"),
		SteadyWebhookExchange:       env("APP_STEADY_WEBHOOK_EXCHANGE", "binance"),
		SteadyWebhookInterval:       env("APP_STEADY_WEBHOOK_INTERVAL", "3m"),
		SteadyWebhookTokenNonce:     strings.TrimSpace(os.Getenv("APP_STEADY_WEBHOOK_TOKEN_NONCE")),
		SteadyWebhookToken:          strings.TrimSpace(os.Getenv("APP_STEADY_WEBHOOK_TOKEN")),
	}
}

func env(k, d string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return d
}
func split(s string) []string {
	var out []string
	for _, v := range strings.Split(s, ",") {
		if v = strings.ToUpper(strings.TrimSpace(v)); v != "" {
			out = append(out, v)
		}
	}
	return out
}
func envInt(k string, d int) int {
	v, e := strconv.Atoi(env(k, ""))
	if e == nil && v > 0 {
		return v
	}
	return d
}
func envDuration(k string, d time.Duration) time.Duration {
	v, e := time.ParseDuration(env(k, ""))
	if e == nil && v > 0 {
		return v
	}
	return d
}

// envFloat accepts zero so a threshold can deliberately be disabled, and only
// falls back to the default for a missing, unparsable or negative value.
func envFloat(k string, d float64) float64 {
	v, e := strconv.ParseFloat(env(k, ""), 64)
	if e != nil || v < 0 {
		return d
	}
	return v
}
