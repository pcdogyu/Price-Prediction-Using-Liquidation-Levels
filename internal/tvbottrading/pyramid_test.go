package trading

import "testing"

func TestPyramidDefaultsAmountResolutionAndPriceRules(t *testing.T) {
	defaults := DefaultPyramidOrderSettings()
	if defaults.Enabled || defaults.MaxAdditionalEntries != 3 || len(defaults.AddAmountPcts) != 3 || defaults.MinFavorableMovePct != 1 {
		t.Fatalf("unexpected pyramid defaults: %#v", defaults)
	}
	if normalized := (PyramidOrderSettings{}).Normalize(); normalized.MaxAdditionalEntries != 3 || len(normalized.AddAmountPcts) != 3 {
		t.Fatalf("zero pyramid settings should receive compatibility defaults: %#v", normalized)
	}
	enabledZero := PyramidOrderSettings{Enabled: true}.Normalize()
	if enabledZero.MaxAdditionalEntries != 0 || len(enabledZero.AddAmountPcts) != 0 {
		t.Fatalf("explicit zero-entry pyramid should remain disabled for additions: %#v", enabledZero)
	}

	if amount, fromEquity := ResolveOrderAmount(OrderAmountModeEquityPct, 100, 20, 1000); amount != 200 || !fromEquity {
		t.Fatalf("equity percentage amount=%v from_equity=%v", amount, fromEquity)
	}
	if amount, fromEquity := ResolveOrderAmount(OrderAmountModeEquityPct, 100, 20, 0); amount != 100 || fromEquity {
		t.Fatalf("balance failure should fall back to fixed amount=%v from_equity=%v", amount, fromEquity)
	}
	if !FavorablePyramidPrice(ActionLong, 101, 100, 1) || FavorablePyramidPrice(ActionLong, 100.99, 100, 1) {
		t.Fatal("long pyramid price threshold is incorrect")
	}
	if !FavorablePyramidPrice(ActionShort, 99, 100, 1) || FavorablePyramidPrice(ActionShort, 99.01, 100, 1) {
		t.Fatal("short pyramid price threshold is incorrect")
	}
}

func TestPyramidSettingsValidate(t *testing.T) {
	settings := DefaultPyramidOrderSettings()
	if err := settings.Validate(); err != nil {
		t.Fatal(err)
	}
	settings.AddAmountPcts = []float64{5}
	if err := settings.Validate(); err == nil {
		t.Fatal("expected pyramid level count validation error")
	}
	settings = DefaultPyramidOrderSettings()
	settings.AddAmountPcts[1] = 101
	if err := settings.Validate(); err == nil {
		t.Fatal("expected pyramid amount percentage validation error")
	}
	settings = DefaultPyramidOrderSettings()
	settings.MinFavorableMovePct = -1
	if err := settings.Validate(); err == nil {
		t.Fatal("expected pyramid threshold validation error")
	}
}
