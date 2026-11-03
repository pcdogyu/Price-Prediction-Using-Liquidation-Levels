package valuearea

import (
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

func TestCrossingRearmsInsideValueArea(t *testing.T) {
	at := time.Date(2026, 10, 26, 1, 0, 0, 0, time.UTC)
	profile := testProfile(at, 100, 110)
	gamma := testGamma(at, 1)
	engine := New()

	if _, ok := engine.Process(tick(at, 105, 1), profile, gamma); ok {
		t.Fatal("baseline must not signal")
	}
	signal, ok := engine.Process(tick(at.Add(time.Second), 99, 2), profile, gamma)
	if !ok || signal.Boundary != "val" || signal.Side != "long" || signal.PositionEffect != "open" {
		t.Fatalf("lower crossing = %#v, %v", signal, ok)
	}
	if _, ok = engine.Process(tick(at.Add(2*time.Second), 98, 3), profile, gamma); ok {
		t.Fatal("continuous price below VAL must not repeat")
	}
	if _, ok = engine.Process(tick(at.Add(3*time.Second), 105, 4), profile, gamma); ok {
		t.Fatal("return into value area must only rearm")
	}
	signal, ok = engine.Process(tick(at.Add(4*time.Second), 111, 5), profile, gamma)
	if !ok || signal.Boundary != "vah" || signal.Side != "short" {
		t.Fatalf("upper crossing after rearm = %#v, %v", signal, ok)
	}
}

func TestNegativeGammaOnlyClosesAndInvalidGammaDoesNotSignal(t *testing.T) {
	at := time.Date(2026, 10, 26, 1, 0, 0, 0, time.UTC)
	profile := testProfile(at, 100, 110)
	engine := New()
	if _, ok := engine.Process(tick(at, 105, 7), profile, testGamma(at, -1)); ok {
		t.Fatal("baseline must not signal")
	}
	signal, ok := engine.Process(tick(at.Add(time.Second), 99, 8), profile, testGamma(at, -1))
	if !ok || signal.PositionEffect != "close" || !signal.GammaIgnored {
		t.Fatalf("negative gamma signal = %#v, %v", signal, ok)
	}
	engine = New()
	if _, ok := engine.Process(tick(at, 105, 9), profile, testGammaState(at, 1, "stale")); ok {
		t.Fatal("stale gamma baseline must not signal")
	}
	if _, ok := engine.Process(tick(at.Add(time.Second), 99, 10), profile, testGammaState(at, 1, "stale")); ok {
		t.Fatal("stale gamma must not signal")
	}
}

func TestProfileAndSessionChangesRebaseAndIDsAreDeterministic(t *testing.T) {
	at := time.Date(2026, 10, 26, 1, 0, 0, 0, time.UTC)
	profile := testProfile(at, 100, 110)
	gamma := testGamma(at, 1)
	engine := New()
	engine.Process(tick(at, 105, 20), profile, gamma)
	changed := testProfile(at, 101, 111)
	if _, ok := engine.Process(tick(at.Add(time.Second), 100, 21), changed, gamma); ok {
		t.Fatal("changed boundaries must rebase")
	}
	engine = New()
	engine.Process(tick(at, 105, 22), profile, gamma)
	first, ok := engine.Process(tick(at.Add(time.Second), 99, 23), profile, gamma)
	if !ok {
		t.Fatal("expected lower crossing")
	}
	engine = New()
	engine.Process(tick(at, 105, 22), profile, gamma)
	second, ok := engine.Process(tick(at.Add(time.Second), 99, 23), profile, gamma)
	if !ok || first.ID != second.ID {
		t.Fatalf("same tick must produce deterministic ID: %q vs %q", first.ID, second.ID)
	}
	if first.ID == "" || first.AlertID != "value-area:"+first.ID {
		t.Fatalf("invalid deterministic identifiers: %#v", first)
	}
}

func TestCrossingMustStartInsideValueArea(t *testing.T) {
	at := time.Date(2026, 10, 26, 1, 0, 0, 0, time.UTC)
	profile := testProfile(at, 100, 110)
	gamma := testGamma(at, 1)
	engine := New()
	engine.Process(tick(at, 120, 30), profile, gamma)
	if _, ok := engine.Process(tick(at.Add(time.Second), 99, 31), profile, gamma); ok {
		t.Fatal("a jump from above VAH to below VAL is not an inside-to-below crossing")
	}
	engine = New()
	engine.Process(tick(at, 90, 32), profile, gamma)
	if _, ok := engine.Process(tick(at.Add(time.Second), 111, 33), profile, gamma); ok {
		t.Fatal("a jump from below VAL to above VAH is not an inside-to-above crossing")
	}
}

func TestAdaptivePositiveGammaTradesReentryAndRearmsAfterExcursion(t *testing.T) {
	at := time.Date(2026, 10, 26, 1, 0, 0, 0, time.UTC)
	profile := testProfile(at, 100, 110)
	engine := New()

	if _, ok := engine.ProcessWithMode(tick(at, 105, 40), profile, testGamma(at, 1), StrategyModeAdaptive); ok {
		t.Fatal("adaptive baseline must not signal")
	}
	if _, ok := engine.ProcessWithMode(tick(at.Add(time.Second), 111, 41), profile, testGamma(at, 1), StrategyModeAdaptive); ok {
		t.Fatal("positive Gamma must wait for reentry from above VAH")
	}
	signal, ok := engine.ProcessWithMode(tick(at.Add(2*time.Second), 110, 42), profile, testGamma(at, 1), StrategyModeAdaptive)
	if !ok || signal.Side != "short" || signal.Boundary != "vah" || signal.PositionEffect != "open" {
		t.Fatalf("positive Gamma VAH reentry = %#v, %v", signal, ok)
	}
	if _, ok = engine.ProcessWithMode(tick(at.Add(3*time.Second), 109, 43), profile, testGamma(at, 1), StrategyModeAdaptive); ok {
		t.Fatal("continuous price inside value area must not repeat")
	}
	if _, ok = engine.ProcessWithMode(tick(at.Add(4*time.Second), 111, 44), profile, testGamma(at, 1), StrategyModeAdaptive); ok {
		t.Fatal("leaving the value area only rearms the short side")
	}
	if signal, ok = engine.ProcessWithMode(tick(at.Add(5*time.Second), 110, 45), profile, testGamma(at, 1), StrategyModeAdaptive); !ok || signal.Side != "short" {
		t.Fatalf("positive Gamma VAH reentry after rearm = %#v, %v", signal, ok)
	}

	engine = New()
	engine.ProcessWithMode(tick(at, 105, 46), profile, testGamma(at, 1), StrategyModeAdaptive)
	if _, ok = engine.ProcessWithMode(tick(at.Add(time.Second), 99, 47), profile, testGamma(at, 1), StrategyModeAdaptive); ok {
		t.Fatal("positive Gamma must wait for reentry from below VAL")
	}
	if signal, ok = engine.ProcessWithMode(tick(at.Add(2*time.Second), 100, 48), profile, testGamma(at, 1), StrategyModeAdaptive); !ok || signal.Side != "long" || signal.Boundary != "val" {
		t.Fatalf("positive Gamma VAL reentry = %#v, %v", signal, ok)
	}
}

func TestAdaptiveNegativeGammaTradesBreakoutsInTrendDirection(t *testing.T) {
	at := time.Date(2026, 10, 26, 1, 0, 0, 0, time.UTC)
	profile := testProfile(at, 100, 110)
	engine := New()
	engine.ProcessWithMode(tick(at, 105, 50), profile, testGamma(at, -1), StrategyModeAdaptive)

	signal, ok := engine.ProcessWithMode(tick(at.Add(time.Second), 111, 51), profile, testGamma(at, -1), StrategyModeAdaptive)
	if !ok || signal.Side != "long" || signal.Boundary != "vah" || signal.PositionEffect != "open" {
		t.Fatalf("negative Gamma VAH breakout = %#v, %v", signal, ok)
	}
	if _, ok = engine.ProcessWithMode(tick(at.Add(2*time.Second), 112, 52), profile, testGamma(at, -1), StrategyModeAdaptive); ok {
		t.Fatal("continuous price above VAH must not repeat")
	}
	if _, ok = engine.ProcessWithMode(tick(at.Add(3*time.Second), 109, 53), profile, testGamma(at, -1), StrategyModeAdaptive); ok {
		t.Fatal("returning inside only rearms the next breakout")
	}
	if signal, ok = engine.ProcessWithMode(tick(at.Add(4*time.Second), 111, 54), profile, testGamma(at, -1), StrategyModeAdaptive); !ok || signal.Side != "long" {
		t.Fatalf("negative Gamma VAH breakout after rearm = %#v, %v", signal, ok)
	}

	engine = New()
	engine.ProcessWithMode(tick(at, 105, 55), profile, testGamma(at, -1), StrategyModeAdaptive)
	if signal, ok = engine.ProcessWithMode(tick(at.Add(time.Second), 99, 56), profile, testGamma(at, -1), StrategyModeAdaptive); !ok || signal.Side != "short" || signal.Boundary != "val" {
		t.Fatalf("negative Gamma VAL breakout = %#v, %v", signal, ok)
	}
}

func TestAdaptiveGammaFallbackUsesPositiveMeanReversion(t *testing.T) {
	at := time.Date(2026, 10, 26, 1, 0, 0, 0, time.UTC)
	profile := testProfile(at, 100, 110)
	for _, tc := range []struct {
		name  string
		gamma domain.AlertGamma
	}{
		{name: "unavailable", gamma: domain.AlertGamma{State: "unavailable"}},
		{name: "zero", gamma: testGamma(at, 0)},
		{name: "stale", gamma: testGammaState(at.Add(-domain.OptionGammaStaleAfter-time.Second), -1, "ok")},
		{name: "unhealthy", gamma: testGammaState(at, -1, "partial-but-invalid")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine := New()
			if _, ok := engine.ProcessWithMode(tick(at, 105, 60), profile, tc.gamma, StrategyModeAdaptive); ok {
				t.Fatal("fallback baseline must not signal")
			}
			if _, ok := engine.ProcessWithMode(tick(at.Add(time.Second), 99, 61), profile, tc.gamma, StrategyModeAdaptive); ok {
				t.Fatal("fallback must wait for positive-mode reentry")
			}
			signal, ok := engine.ProcessWithMode(tick(at.Add(2*time.Second), 100, 62), profile, tc.gamma, StrategyModeAdaptive)
			if !ok || signal.Side != "long" || signal.PositionEffect != "open" || signal.GammaValue != nil {
				t.Fatalf("fallback signal = %#v, %v", signal, ok)
			}
		})
	}
}

func TestAdaptiveGammaDoesNotSignalWhenOneTickSkipsValueArea(t *testing.T) {
	at := time.Date(2026, 10, 26, 1, 0, 0, 0, time.UTC)
	profile := testProfile(at, 100, 110)
	engine := New()
	engine.ProcessWithMode(tick(at, 120, 70), profile, testGamma(at, 1), StrategyModeAdaptive)
	if _, ok := engine.ProcessWithMode(tick(at.Add(time.Second), 90, 71), profile, testGamma(at, 1), StrategyModeAdaptive); ok {
		t.Fatal("a tick that skips the whole value area must not signal")
	}
}

func tick(at time.Time, price float64, id int64) domain.PriceTick {
	return domain.PriceTick{TradeID: id, Symbol: "BTCUSDT", Time: at, Price: price}
}

func testProfile(at time.Time, val, vah float64) domain.VolumeProfile {
	return domain.VolumeProfile{Symbol: "BTCUSDT", State: "ok", SessionStart: at.Add(-time.Hour), VAL: &val, VAH: &vah}
}

func testGamma(at time.Time, value float64) domain.AlertGamma {
	return testGammaState(at, value, "ok")
}

func testGammaState(at time.Time, value float64, state string) domain.AlertGamma {
	return domain.AlertGamma{AnchorSymbol: "BTCUSDT", State: state, Gamma: &value, UpdatedAt: &at}
}
