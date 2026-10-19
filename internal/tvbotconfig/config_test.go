package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestNormalizeDefaultTab(t *testing.T) {
	cfg := Default()
	cfg.UI.DefaultTab = ""
	cfg.Normalize()
	if cfg.UI.DefaultTab != DefaultHomeTab {
		t.Fatalf("blank default tab should fall back to %q, got %q", DefaultHomeTab, cfg.UI.DefaultTab)
	}

	cfg.UI.DefaultTab = "positions"
	cfg.Normalize()
	if cfg.UI.DefaultTab != "positions" {
		t.Fatalf("known default tab should be preserved, got %q", cfg.UI.DefaultTab)
	}

	cfg.UI.DefaultTab = "missing-tab"
	cfg.Normalize()
	if cfg.UI.DefaultTab != DefaultHomeTab {
		t.Fatalf("unknown default tab should fall back to %q, got %q", DefaultHomeTab, cfg.UI.DefaultTab)
	}
}

func TestNormalizeTableColumns(t *testing.T) {
	cfg := Default()
	cfg.UI.TableColumns = TableColumnsConfig{}
	cfg.Normalize()
	if !reflect.DeepEqual(cfg.UI.TableColumns.Positions, DefaultPositionTableColumns) {
		t.Fatalf("blank position columns should use defaults: %#v", cfg.UI.TableColumns.Positions)
	}
	if !reflect.DeepEqual(cfg.UI.TableColumns.PendingOrders, DefaultPendingOrderTableColumns) {
		t.Fatalf("blank pending order columns should use defaults: %#v", cfg.UI.TableColumns.PendingOrders)
	}
	if !reflect.DeepEqual(cfg.UI.TableColumns.Symbols, DefaultSymbolTableColumns) {
		t.Fatalf("blank symbol columns should use defaults: %#v", cfg.UI.TableColumns.Symbols)
	}

	cfg.UI.TableColumns = TableColumnsConfig{
		Positions:     []string{"upl", "unknown", "exchange", "upl"},
		PendingOrders: []string{"actions", "symbol", "bad", "actions"},
		Symbols:       []string{"turnover", "symbol", "bad", "turnover"},
	}
	cfg.Normalize()
	if len(cfg.UI.TableColumns.Positions) != len(DefaultPositionTableColumns) ||
		cfg.UI.TableColumns.Positions[0] != "upl" ||
		cfg.UI.TableColumns.Positions[1] != "exchange" {
		t.Fatalf("position columns should keep known unique order then append defaults: %#v", cfg.UI.TableColumns.Positions)
	}
	if len(cfg.UI.TableColumns.PendingOrders) != len(DefaultPendingOrderTableColumns) ||
		cfg.UI.TableColumns.PendingOrders[0] != "actions" ||
		cfg.UI.TableColumns.PendingOrders[1] != "symbol" {
		t.Fatalf("pending order columns should keep known unique order then append defaults: %#v", cfg.UI.TableColumns.PendingOrders)
	}
	if pendingAgeIndex := indexString(cfg.UI.TableColumns.PendingOrders, "pending_age"); pendingAgeIndex < 0 ||
		pendingAgeIndex+1 >= len(cfg.UI.TableColumns.PendingOrders) ||
		cfg.UI.TableColumns.PendingOrders[pendingAgeIndex+1] != "state" {
		t.Fatalf("missing pending_age should be inserted before state: %#v", cfg.UI.TableColumns.PendingOrders)
	}
	if len(cfg.UI.TableColumns.Symbols) != len(DefaultSymbolTableColumns) ||
		cfg.UI.TableColumns.Symbols[0] != "turnover" ||
		cfg.UI.TableColumns.Symbols[1] != "symbol" {
		t.Fatalf("symbol columns should keep known unique order then append defaults: %#v", cfg.UI.TableColumns.Symbols)
	}
	if containsString(cfg.UI.TableColumns.Positions, "unknown") || containsString(cfg.UI.TableColumns.PendingOrders, "bad") || containsString(cfg.UI.TableColumns.Symbols, "bad") {
		t.Fatalf("unknown columns should be removed: %#v", cfg.UI.TableColumns)
	}
	if countString(cfg.UI.TableColumns.Positions, "upl") != 1 || countString(cfg.UI.TableColumns.PendingOrders, "actions") != 1 || countString(cfg.UI.TableColumns.Symbols, "turnover") != 1 {
		t.Fatalf("duplicate columns should be removed: %#v", cfg.UI.TableColumns)
	}

	cfg.UI.TableColumns.PendingOrders = []string{"pending_age", "actions", "state"}
	cfg.Normalize()
	if cfg.UI.TableColumns.PendingOrders[0] != "pending_age" || cfg.UI.TableColumns.PendingOrders[1] != "actions" {
		t.Fatalf("explicit pending_age order should be preserved: %#v", cfg.UI.TableColumns.PendingOrders)
	}
}

func TestNormalizeBinanceBaseURLs(t *testing.T) {
	cfg := Default()
	cfg.Trading.BinanceBaseURL = "https://fapi.binance.com/"
	cfg.Trading.BinanceDemoBaseURL = ""
	cfg.Normalize()
	if cfg.Trading.BinanceBaseURL != "https://fapi.binance.com" {
		t.Fatalf("Binance base URL should be trimmed, got %q", cfg.Trading.BinanceBaseURL)
	}
	if cfg.Trading.BinanceDemoBaseURL != "https://demo-fapi.binance.com" {
		t.Fatalf("Binance demo base URL should default, got %q", cfg.Trading.BinanceDemoBaseURL)
	}
}

func TestMarketTurnoverScopeDefaultsPersistsAndValidates(t *testing.T) {
	cfg := Default()
	if cfg.Trading.MarketTurnoverScope != MarketTurnoverScopeTop100 {
		t.Fatalf("default market turnover scope=%q", cfg.Trading.MarketTurnoverScope)
	}

	legacyPath := filepath.Join(t.TempDir(), "legacy.json")
	if err := os.WriteFile(legacyPath, []byte(`{"trading":{"order_amount_usdt":100}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	legacy, err := Load(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	if legacy.Trading.MarketTurnoverScope != MarketTurnoverScopeTop100 {
		t.Fatalf("legacy scope=%q", legacy.Trading.MarketTurnoverScope)
	}

	for _, scope := range []string{MarketTurnoverScopeTop50, MarketTurnoverScopeTop100, MarketTurnoverScopeTop200, MarketTurnoverScopeTop500, MarketTurnoverScopeAll} {
		cfg.Trading.MarketTurnoverScope = scope
		if err := cfg.Validate(); err != nil {
			t.Fatalf("scope %q should validate: %v", scope, err)
		}
	}
	if limits := []int{
		MarketTurnoverScopeLimit(MarketTurnoverScopeTop50),
		MarketTurnoverScopeLimit(MarketTurnoverScopeTop100),
		MarketTurnoverScopeLimit(MarketTurnoverScopeTop200),
		MarketTurnoverScopeLimit(MarketTurnoverScopeTop500),
		MarketTurnoverScopeLimit(MarketTurnoverScopeAll),
	}; !reflect.DeepEqual(limits, []int{50, 100, 200, 500, 0}) {
		t.Fatalf("unexpected market turnover limits: %#v", limits)
	}

	cfg.Trading.MarketTurnoverScope = MarketTurnoverScopeTop200
	path := filepath.Join(t.TempDir(), "config.json")
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Trading.MarketTurnoverScope != MarketTurnoverScopeTop200 {
		t.Fatalf("persisted scope=%q", loaded.Trading.MarketTurnoverScope)
	}

	cfg.Trading.MarketTurnoverScope = "top75"
	if err := cfg.Validate(); err == nil {
		t.Fatal("unsupported market turnover scope should be rejected")
	}
}

func TestLossCooldownDefaultsMigrationAndValidation(t *testing.T) {
	cfg := Default()
	if !cfg.Trading.LossCooldown.Enabled || cfg.Trading.LossCooldown.Hours != 24 {
		t.Fatalf("unexpected loss cooldown defaults: %#v", cfg.Trading.LossCooldown)
	}

	legacyPath := filepath.Join(t.TempDir(), "legacy.json")
	if err := os.WriteFile(legacyPath, []byte(`{"trading":{"order_amount_usdt":100}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	legacy, err := Load(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	if !legacy.Trading.LossCooldown.Enabled || legacy.Trading.LossCooldown.Hours != 24 {
		t.Fatalf("legacy config did not receive loss cooldown defaults: %#v", legacy.Trading.LossCooldown)
	}

	cfg.Trading.LossCooldown.Enabled = false
	cfg.Trading.LossCooldown.Hours = 48
	path := filepath.Join(t.TempDir(), "config.json")
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Trading.LossCooldown.Enabled || loaded.Trading.LossCooldown.Hours != 48 {
		t.Fatalf("loss cooldown setting did not persist: %#v", loaded.Trading.LossCooldown)
	}

	cfg.Trading.LossCooldown.Hours = 8761
	if err := cfg.Validate(); err == nil {
		t.Fatal("loss cooldown above the maximum should be rejected")
	}
}

func TestIgnoredCoinpairsMigratePersistAndDeduplicate(t *testing.T) {
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, "legacy.json")
	if err := os.WriteFile(legacyPath, []byte(`{"trading":{"ignored_coinpair":"  syrup  "}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	legacy, err := Load(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(legacy.Trading.IgnoredCoinpairs, []string{"SYRUP"}) || legacy.Trading.IgnoredCoinpair != "SYRUP" {
		t.Fatalf("legacy ignored coinpair not migrated: %#v", legacy.Trading)
	}

	path := filepath.Join(dir, "config.json")
	cfg := Default()
	cfg.Trading.IgnoredCoinpairs = []string{" syrup ", "ETH-USDT", "ethusdt", "", "btc"}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded.Trading.IgnoredCoinpairs, []string{"SYRUP", "ETH-USDT", "BTC"}) {
		t.Fatalf("ignored coinpairs not normalized: %#v", loaded.Trading.IgnoredCoinpairs)
	}
	if loaded.Trading.IgnoredCoinpair != "SYRUP" {
		t.Fatalf("legacy compatibility mirror = %q, want SYRUP", loaded.Trading.IgnoredCoinpair)
	}

	missing, err := Load(filepath.Join(dir, "missing.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(missing.Trading.IgnoredCoinpairs) != 0 || missing.Trading.IgnoredCoinpair != "" {
		t.Fatalf("missing config should default filters disabled: %#v", missing.Trading)
	}
}

func TestStoreClonesIgnoredCoinpairs(t *testing.T) {
	cfg := Default()
	cfg.Trading.IgnoredCoinpairs = []string{"ETH"}
	store := NewStore("", cfg)
	got := store.Get()
	got.Trading.IgnoredCoinpairs[0] = "MUTATED"
	if current := store.Get().Trading.IgnoredCoinpairs; !reflect.DeepEqual(current, []string{"ETH"}) {
		t.Fatalf("Get shared ignored coinpair slice: %#v", current)
	}
	if _, err := store.Update(func(next *Config) error {
		next.Trading.IgnoredCoinpairs = append(next.Trading.IgnoredCoinpairs, "BTC")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if current := store.Get().Trading.IgnoredCoinpairs; !reflect.DeepEqual(current, []string{"ETH", "BTC"}) {
		t.Fatalf("Update lost ignored coinpair list: %#v", current)
	}
}

func TestNormalizePositionMonitorDefaultsAndValidation(t *testing.T) {
	cfg := Default()
	cfg.Trading.PositionMonitor = PositionMonitorConfig{}
	cfg.Normalize()
	if cfg.Trading.PositionMonitor.OKXEnabled || cfg.Trading.PositionMonitor.BinanceEnabled {
		t.Fatalf("position monitor should default disabled: %#v", cfg.Trading.PositionMonitor)
	}
	if cfg.Trading.PositionMonitor.PollIntervalSeconds != 300 || cfg.Trading.PositionMonitor.TakeProfitPct != 5 || cfg.Trading.PositionMonitor.StopLossPct != 8 {
		t.Fatalf("bad position monitor defaults: %#v", cfg.Trading.PositionMonitor)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("defaulted position monitor should validate: %v", err)
	}

	cfg.Trading.PositionMonitor.PollIntervalSeconds = 0
	if err := cfg.Validate(); err == nil {
		t.Fatal("zero position monitor interval should be rejected")
	}
	cfg.Trading.PositionMonitor.PollIntervalSeconds = 300
	cfg.Trading.PositionMonitor.TakeProfitPct = -1
	if err := cfg.Validate(); err == nil {
		t.Fatal("negative position monitor take profit should be rejected")
	}
	cfg.Trading.PositionMonitor.TakeProfitPct = 5
	cfg.Trading.PositionMonitor.StopLossPct = -1
	if err := cfg.Validate(); err == nil {
		t.Fatal("negative position monitor stop loss should be rejected")
	}
}

func containsString(values []string, want string) bool {
	return countString(values, want) > 0
}

func countString(values []string, want string) int {
	count := 0
	for _, value := range values {
		if value == want {
			count++
		}
	}
	return count
}

func indexString(values []string, want string) int {
	for i, value := range values {
		if value == want {
			return i
		}
	}
	return -1
}
