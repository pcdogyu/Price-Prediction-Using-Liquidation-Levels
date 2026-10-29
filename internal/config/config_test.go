package config

import (
	"reflect"
	"testing"
)

func TestLoadDefaultsContinuousBootstrapToAnchorSymbols(t *testing.T) {
	for _, key := range []string{"APP_SYMBOLS", "APP_DEPTH_SYMBOLS", "APP_BOOTSTRAP_SYMBOLS"} {
		t.Setenv(key, "")
	}
	cfg := Load()
	if !reflect.DeepEqual(cfg.BootstrapSymbols, []string{"BTCUSDT", "ETHUSDT"}) {
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
	if cfg.BackfillDays != 30 || cfg.RetentionDays != 180 {
		t.Fatalf("backfill=%d retention=%d", cfg.BackfillDays, cfg.RetentionDays)
	}
}
