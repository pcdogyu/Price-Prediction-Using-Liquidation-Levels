package config

import (
	"os"
	"strconv"
	"strings"
	"time"
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
}

func Load() Config {
	return Config{
		Address: env("APP_ADDRESS", ":9090"), DatabasePath: env("APP_DATABASE", "data/liquidation.db"),
		ModelPath: env("APP_MODEL", "models/model.json"), Symbols: split(env("APP_SYMBOLS", "BTCUSDT,ETHUSDT")),
		BasePath: env("APP_BASE_PATH", "/"), AuthUsername: strings.TrimSpace(os.Getenv("APP_AUTH_USERNAME")),
		AuthPasswordHash: strings.TrimSpace(os.Getenv("APP_AUTH_PASSWORD_HASH")), LogPath: strings.TrimSpace(os.Getenv("APP_LOG_PATH")),
		CoinGlassDebugURL: env("APP_COINGLASS_DEBUG_URL", "http://127.0.0.1:9222"), CoinGlassCaptureDir: env("APP_COINGLASS_CAPTURE_DIR", "data/coinglass-captures"), CoinGlassCaptureInterval: envDuration("APP_COINGLASS_CAPTURE_INTERVAL", 45*time.Minute),
		LogRetentionDays: envInt("APP_LOG_RETENTION_DAYS", 7),
		BackfillDays:     envInt("APP_BACKFILL_DAYS", 180), TrainingDays: envInt("APP_TRAINING_DAYS", 30), SnapshotInterval: envDuration("APP_SNAPSHOT_INTERVAL", 5*time.Minute),
		StaleAfter: envDuration("APP_STALE_AFTER", 90*time.Second),
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
