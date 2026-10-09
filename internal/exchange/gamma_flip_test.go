package exchange

import (
	"math"
	"testing"
	"time"
)

func flipFixture(side, strike string, oi, unit, iv, rate float64, at time.Time, expiry time.Duration) OptionExposure {
	return OptionExposure{Contract: OptionContract{Side: side, Strike: strike, Unit: unit, Expiry: at.Add(expiry).UnixMilli()}, OI: oi, Gamma: .01, IV: &iv, InterestRate: &rate}
}

func TestGammaFlipAnalyticRootIncludesOIUnitsIVAndRate(t *testing.T) {
	at := time.Date(2026, 10, 9, 8, 0, 0, 0, time.UTC)
	year := 365 * 24 * time.Hour
	rows := []OptionExposure{flipFixture("CALL", "110", 1, 2, .2, .07, at, year), flipFixture("PUT", "90", 1, 1, .2, .07, at, year)}
	g := BuildGamma("ETHUSDT", 100, rows, at)
	want := math.Sqrt(110*90) * math.Exp(-(.07+.5*.2*.2)-.2*.2*math.Log(2)/math.Log(110.0/90))
	if g.GammaFlip == nil || math.Abs(*g.GammaFlip-want) > 1e-6 || g.FlipState != "ok" || len(g.GammaFlips) != 1 || g.FlipContracts != 2 || g.FlipExpectedContracts != 2 {
		t.Fatalf("got=%+v want=%v", g, want)
	}
	if g.FlipRangeLow != 50 || g.FlipRangeHigh != 150 {
		t.Fatal("wrong search range", g)
	}
	// This crossing is a repriced chain zero, not an interpolation of strike bars.
	if g.Levels[0].NetGEXUSD != -1 || g.Levels[1].NetGEXUSD != 2 {
		t.Fatal("existing GEX values changed", g.Levels)
	}
}

func TestGammaFlipMultipleRootsChooseNearestAndPartialCoverage(t *testing.T) {
	at := time.Date(2026, 10, 9, 8, 0, 0, 0, time.UTC)
	expiry := 365 * 24 * time.Hour / 10
	rows := []OptionExposure{flipFixture("CALL", "80", 1, 1, .12, 0, at, expiry), flipFixture("PUT", "100", 2, 1, .12, 0, at, expiry), flipFixture("CALL", "120", 1, 1, .12, 0, at, expiry)}
	g := BuildGamma("BTCUSDT", 105, rows, at)
	if len(g.GammaFlips) != 2 || g.GammaFlip == nil || *g.GammaFlip != g.GammaFlips[1] || g.FlipState != "ok" {
		t.Fatalf("gamma=%+v", g)
	}
	extra := flipFixture("CALL", "130", .01, 1, .12, 0, at, expiry)
	extra.IV = nil
	rows = append(rows, extra)
	g = BuildGamma("BTCUSDT", 105, rows, at)
	if g.FlipState != "partial" || g.FlipContracts != 3 || g.FlipExpectedContracts != 4 || g.GammaFlip == nil {
		t.Fatal(g)
	}
}

func TestGammaFlipNeverInventsMissingOrBalancedPrices(t *testing.T) {
	at := time.Date(2026, 10, 9, 8, 0, 0, 0, time.UTC)
	year := 365 * 24 * time.Hour
	call := flipFixture("CALL", "100", 1, 1, .5, 0, at, year)
	put := flipFixture("PUT", "100", 1, 1, .5, 0, at, year)
	for _, tc := range []struct {
		name, state string
		rows        []OptionExposure
	}{
		{"balanced everywhere", "no_crossing", []OptionExposure{call, put}},
		{"calls only", "no_crossing", []OptionExposure{call}},
		{"puts only", "no_crossing", []OptionExposure{put}},
		{"empty", "unavailable", nil},
		{"missing IV", "unavailable", []OptionExposure{{Contract: call.Contract, OI: 1, Gamma: .01}}},
		{"expired", "unavailable", []OptionExposure{flipFixture("CALL", "100", 1, 1, .5, 0, at, -time.Second)}},
		{"no open interest", "unavailable", []OptionExposure{flipFixture("CALL", "100", 0, 1, .5, 0, at, year)}},
		{"zero IV", "unavailable", []OptionExposure{flipFixture("CALL", "100", 1, 1, 0, 0, at, year)}},
		{"outside search range", "no_crossing", []OptionExposure{flipFixture("CALL", "110", 1, 1, .8, 0, at, year), flipFixture("PUT", "90", 4, 1, .8, 0, at, year)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := BuildGamma("ETHUSDT", 100, tc.rows, at)
			if g.GammaFlip != nil || len(g.GammaFlips) != 0 || g.FlipState != tc.state {
				t.Fatal(g)
			}
		})
	}
}

func TestGammaFlipResolvesNearExpiryPeaksAndAvoidsUnderflow(t *testing.T) {
	at := time.Date(2026, 10, 9, 8, 0, 0, 0, time.UTC)
	rows := []OptionExposure{flipFixture("CALL", "99.8", 1, 1, .2, 0, at, time.Second), flipFixture("PUT", "100", 2, 1, .2, 0, at, time.Second), flipFixture("CALL", "100.2", 1, 1, .2, 0, at, time.Second)}
	g := BuildGamma("ETHUSDT", 100, rows, at)
	if len(g.GammaFlips) != 2 || g.GammaFlip == nil {
		t.Fatal("narrow peaks were skipped", g)
	}
	if value := flipRatio(0, []flipContract{{center: 20, width: .01, logWeight: 0, sign: 1}}); value != 1 {
		t.Fatal("underflow became zero", value)
	}
}
