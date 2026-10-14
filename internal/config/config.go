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
	AuthUsername                     string
	AuthPasswordHash                 string
	LogPath                          string
	CoinGlassDebugURL                string
	CoinGlassCaptureDir              string
	CoinGlassCaptureInterval         time.Duration
	Symbols                          []string
	BackfillDays                     int
	TrainingDays                     int
	LogRetentionDays                 int
	SnapshotInterval, StaleAfter     time.Duration
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
	// Telegram notification defaults; the alert settings page overrides them.
	TelegramBotToken, TelegramChatID, TelegramAPIBase string
}

func Load() Config {
	return Config{
		Address: env("APP_ADDRESS", ":9090"), DatabasePath: env("APP_DATABASE", "data/liquidation.db"),
		ModelPath: env("APP_MODEL", "models/model.json"), Symbols: split(env("APP_SYMBOLS", "BTCUSDT,ETHUSDT")),
		BasePath: env("APP_BASE_PATH", "/"), AuthUsername: strings.TrimSpace(os.Getenv("APP_AUTH_USERNAME")),
		AuthPasswordHash: strings.TrimSpace(os.Getenv("APP_AUTH_PASSWORD_HASH")), LogPath: strings.TrimSpace(os.Getenv("APP_LOG_PATH")),
		CoinGlassDebugURL: env("APP_COINGLASS_DEBUG_URL", "http://127.0.0.1:9222"), CoinGlassCaptureDir: env("APP_COINGLASS_CAPTURE_DIR", "data/coinglass-captures"), CoinGlassCaptureInterval: envDuration("APP_COINGLASS_CAPTURE_INTERVAL", 10*time.Minute),
		LogRetentionDays: envInt("APP_LOG_RETENTION_DAYS", 7),
		BackfillDays:     envInt("APP_BACKFILL_DAYS", 180), TrainingDays: envInt("APP_TRAINING_DAYS", 30), SnapshotInterval: envDuration("APP_SNAPSHOT_INTERVAL", 5*time.Minute),
		StaleAfter:              envDuration("APP_STALE_AFTER", 90*time.Second),
		AlertTopSymbols:         envInt("APP_ALERT_TOP_SYMBOLS", domain.AlertTopSymbolsDefault),
		AlertWindow:             envDuration("APP_ALERT_WINDOW", 3*time.Minute),
		AlertQuiet:              envDuration("APP_ALERT_QUIET", 10*time.Minute),
		AlertConfirm:            envDuration("APP_ALERT_CONFIRM", 30*time.Minute),
		AlertRefresh:            envDuration("APP_ALERT_REFRESH", 10*time.Minute),
		AlertFollowUpMinimumUSD: envFloat("APP_ALERT_FOLLOW_UP_MIN_USD", 0),
		AlertOppositeMinimumUSD: envFloat("APP_ALERT_OPPOSITE_MIN_USD", 0),
		AlertThresholdBTCUSD:    envFloat("APP_ALERT_THRESHOLD_BTC_USD", 300000),
		AlertThresholdETHUSD:    envFloat("APP_ALERT_THRESHOLD_ETH_USD", 250000),
		AlertThresholdOtherUSD:  envFloat("APP_ALERT_THRESHOLD_OTHER_USD", 300000),
		TelegramBotToken:        strings.TrimSpace(os.Getenv("APP_TELEGRAM_BOT_TOKEN")),
		TelegramChatID:          strings.TrimSpace(os.Getenv("APP_TELEGRAM_CHAT_ID")),
		TelegramAPIBase:         env("APP_TELEGRAM_API_BASE", "https://api.telegram.org"),
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
