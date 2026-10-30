package config

import (
	"reflect"
	"testing"
	"time"
)

func TestLoadDefaultsContinuousBootstrapToAnchorSymbols(t *testing.T) {
	for _, key := range []string{"APP_SYMBOLS", "APP_DEPTH_SYMBOLS", "APP_BOOTSTRAP_SYMBOLS"} {
		t.Setenv(key, "")
	}
	cfg := Load()
	if !reflect.DeepEqual(cfg.BootstrapSymbols, []string{"ETHUSDT"}) {
		t.Fatalf("default bootstrap symbols=%v", cfg.BootstrapSymbols)
	}
}

func TestLoadHonorsExplicitBootstrapSymbols(t *testing.T) {
	t.Setenv("APP_SYMBOLS", "BTCUSDT,ETHUSDT,SOLUSDT")
	t.Setenv("APP_BOOTSTRAP_SYMBOLS", "SOLUSDT,ETHUSDT")
	cfg := Load()
	if !reflect.DeepEqual(cfg.BootstrapSymbols, []string{"SOLUSDT", "ETHUSDT"}) {
		t.Fatalf("explicit bootstrap symbols=%v", cfg.BootstrapSymbols)
	}
}

func TestLoadUsesSeparateBackfillAndRetentionDefaults(t *testing.T) {
	t.Setenv("APP_BACKFILL_DAYS", "")
	t.Setenv("APP_RETENTION_DAYS", "")
	cfg := Load()
	if cfg.BackfillDays != 1 || cfg.RetentionDays != 180 {
		t.Fatalf("backfill=%d retention=%d", cfg.BackfillDays, cfg.RetentionDays)
	}
	if cfg.MarketPollInterval != 5*time.Minute || cfg.MarketInfoInterval != 5*time.Minute || cfg.MarketFetchConcurrency != 5 {
		t.Fatalf("market poll=%s info=%s concurrency=%d", cfg.MarketPollInterval, cfg.MarketInfoInterval, cfg.MarketFetchConcurrency)
	}
}

func TestLoadHonorsMarketLoadSettings(t *testing.T) {
	t.Setenv("APP_BACKFILL_DAYS", "2")
	t.Setenv("APP_MARKET_POLL_INTERVAL", "2m")
	t.Setenv("APP_MARKET_INFO_INTERVAL", "3m")
	t.Setenv("APP_MARKET_FETCH_CONCURRENCY", "7")
	cfg := Load()
	if cfg.BackfillDays != 2 || cfg.MarketPollInterval != 2*time.Minute || cfg.MarketInfoInterval != 3*time.Minute || cfg.MarketFetchConcurrency != 7 {
		t.Fatalf("backfill=%d poll=%s info=%s concurrency=%d", cfg.BackfillDays, cfg.MarketPollInterval, cfg.MarketInfoInterval, cfg.MarketFetchConcurrency)
	}
}
