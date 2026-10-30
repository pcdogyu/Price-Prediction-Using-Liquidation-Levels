package alert

import (
	"strings"
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

var testStart = time.Date(2026, 10, 14, 3, 0, 0, 0, time.UTC)

// testEvent builds a liquidation event with an explicit time so every transition is
// reproducible without a wall clock.
func testEvent(symbol, side string, at time.Time, usd float64) domain.LiquidationEvent {
	return domain.LiquidationEvent{ID: symbol + "-" + side + "-" + at.Format("150405"), Exchange: "binance", Symbol: symbol, PositionSide: side, EventTime: at, Price: 100, Quantity: usd / 100, NotionalUSD: usd}
}

func testEngine(t *testing.T, cfg Config, symbols ...string) *Engine {
	t.Helper()
	engine := New(cfg)
	entries := make([]WatchEntry, 0, len(symbols))
	for i, symbol := range symbols {
		entries = append(entries, WatchEntry{Symbol: symbol, Rank: i + 1, QuoteVolumeUSD: 1e9 - float64(i)})
	}
	engine.Watch(entries)
	return engine
}

func kinds(signals []domain.TradeSignal) []string {
	out := make([]string, 0, len(signals))
	for _, signal := range signals {
		out = append(out, signal.Kind)
	}
	return out
}

// TestBelowThresholdRaisesNothing covers rule 1's negative case: totals inside the
// window that stay under the threshold must not create an alert.
func TestBelowThresholdRaisesNothing(t *testing.T) {
	engine := testEngine(t, Defaults(), "BTCUSDT")
	res := engine.Observe(testEvent("BTCUSDT", "long", testStart, 200000))
	if len(res.Alerts) != 0 || len(res.Signals) != 0 {
		t.Fatalf("expected no output below threshold, got alerts %d signals %v", len(res.Alerts), kinds(res.Signals))
	}
	res = engine.Observe(testEvent("BTCUSDT", "long", testStart.Add(30*time.Second), 50000))
	if len(res.Alerts) != 0 {
		t.Fatalf("250k below the 300k threshold must not alert, got %d", len(res.Alerts))
	}
	snapshot := engine.Snapshot(testStart.Add(time.Minute))
	if len(snapshot) != 1 || snapshot[0].LongUSD != 250000 || snapshot[0].LongCount != 2 {
		t.Fatalf("snapshot window wrong: %+v", snapshot)
	}
}

// TestWarningAggressiveThenSteady walks the full happy path for a long-liquidation
// cycle, whose recommended order direction is short.
func TestWarningAggressiveThenSteady(t *testing.T) {
	engine := testEngine(t, Defaults(), "BTCUSDT")
	res := engine.Observe(testEvent("BTCUSDT", "long", testStart, 400000))
	if len(res.Alerts) != 1 {
		t.Fatalf("expected one alert, got %d", len(res.Alerts))
	}
	warning := res.Alerts[0]
	if warning.Symbol != "BTCUSDT" || warning.Side != "long" || warning.TradeSide != "short" || warning.Status != domain.AlertStateWatching {
		t.Fatalf("unexpected warning: %+v", warning)
	}
	if !warning.QuietUntil.Equal(testStart.Add(10 * time.Minute)) {
		t.Fatalf("quiet deadline should be trigger plus quiet period, got %v", warning.QuietUntil)
	}
	if got := engine.Tick(testStart.Add(9 * time.Minute)); len(got.Signals) != 0 {
		t.Fatalf("signal fired before the quiet period elapsed: %v", kinds(got.Signals))
	}
	ticked := engine.Tick(testStart.Add(10 * time.Minute))
	if len(ticked.Signals) != 1 || ticked.Signals[0].Kind != domain.SignalAggressive {
		t.Fatalf("expected one aggressive signal, got %v", kinds(ticked.Signals))
	}
	aggressive := ticked.Signals[0]
	if aggressive.Side != "short" || aggressive.LiquidationSide != "long" || aggressive.QuietElapsedSeconds != 600 || aggressive.AlertNotionalUSD != 400000 {
		t.Fatalf("unexpected aggressive signal: %+v", aggressive)
	}
	if len(ticked.Alerts) != 1 || ticked.Alerts[0].Status != domain.AlertStateAggressive || ticked.Alerts[0].AggressiveID != aggressive.ID {
		t.Fatalf("the warning row must follow the signal: %+v", ticked.Alerts)
	}
	confirmed := engine.Observe(testEvent("BTCUSDT", "short", testStart.Add(14*time.Minute), 120000))
	if len(confirmed.Signals) != 1 || confirmed.Signals[0].Kind != domain.SignalSteady {
		t.Fatalf("expected one steady signal, got %v", kinds(confirmed.Signals))
	}
	steady := confirmed.Signals[0]
	if steady.Side != "short" || steady.LiquidationSide != "long" || steady.OppositeUSD != 120000 || steady.OppositeSeconds != 240 {
		t.Fatalf("unexpected steady signal: %+v", steady)
	}
	if len(confirmed.Alerts) != 1 || confirmed.Alerts[0].Status != domain.AlertStateSteady {
		t.Fatalf("warning must end as steady: %+v", confirmed.Alerts)
	}
	if later := engine.Tick(testStart.Add(90 * time.Minute)); !later.empty() {
		t.Fatalf("a closed cycle produced output: alerts %d signals %v", len(later.Alerts), kinds(later.Signals))
	}
}

// TestSameSideBurstExtendsQuiet covers rule 2: continuing liquidations on the warned
// side delay the aggressive signal instead of producing one.
func TestSameSideBurstExtendsQuiet(t *testing.T) {
	engine := testEngine(t, Defaults(), "ETHUSDT")
	res := engine.Observe(testEvent("ETHUSDT", "long", testStart, 300000))
	if len(res.Alerts) != 1 {
		t.Fatalf("expected an ETH warning above the 250k threshold: %+v", res.Alerts)
	}
	follow := engine.Observe(testEvent("ETHUSDT", "long", testStart.Add(4*time.Minute), 1000))
	if len(follow.Signals) != 0 {
		t.Fatalf("a continuing burst must not signal: %v", kinds(follow.Signals))
	}
	if len(follow.Alerts) != 1 || !follow.Alerts[0].QuietUntil.Equal(testStart.Add(14*time.Minute)) || follow.Alerts[0].FollowUps != 1 {
		t.Fatalf("the quiet deadline must move with the follow-up: %+v", follow.Alerts)
	}
	if got := engine.Tick(testStart.Add(13 * time.Minute)); len(got.Signals) != 0 {
		t.Fatalf("a signal fired during the extended quiet period")
	}
	got := engine.Tick(testStart.Add(14 * time.Minute))
	if len(got.Signals) != 1 || got.Signals[0].Kind != domain.SignalAggressive || got.Signals[0].QuietElapsedSeconds != 600 || got.Signals[0].FollowUps != 1 {
		t.Fatalf("expected one delayed aggressive signal: %+v", got.Signals)
	}
}

// TestShortSideIsSymmetric checks that a short liquidation wave is traded long.
func TestShortSideIsSymmetric(t *testing.T) {
	engine := testEngine(t, Defaults(), "BTCUSDT")
	res := engine.Observe(testEvent("BTCUSDT", "short", testStart, 400000))
	if len(res.Alerts) != 1 || res.Alerts[0].Side != "short" || res.Alerts[0].TradeSide != "long" {
		t.Fatalf("a short warning should trade long: %+v", res.Alerts)
	}
	ticked := engine.Tick(testStart.Add(10 * time.Minute))
	if len(ticked.Signals) != 1 || ticked.Signals[0].Side != "long" {
		t.Fatalf("expected a short aggressive signal: %+v", ticked.Signals)
	}
	confirmed := engine.Observe(testEvent("BTCUSDT", "long", testStart.Add(11*time.Minute), 90000))
	if len(confirmed.Signals) != 1 || confirmed.Signals[0].Side != "long" || confirmed.Signals[0].Kind != domain.SignalSteady {
		t.Fatalf("expected a short steady signal: %+v", confirmed.Signals)
	}
}

// TestConfirmationWindowExpiry keeps only the aggressive signal when no opposite
// liquidation arrives in time, and frees the symbol for the next warning.
func TestConfirmationWindowExpiry(t *testing.T) {
	engine := testEngine(t, Defaults(), "BTCUSDT")
	if res := engine.Observe(testEvent("BTCUSDT", "long", testStart, 350000)); len(res.Alerts) != 1 {
		t.Fatalf("expected a warning")
	}
	engine.Tick(testStart.Add(10 * time.Minute))
	if got := engine.Tick(testStart.Add(39*time.Minute + 59*time.Second)); len(got.Alerts) != 0 {
		t.Fatalf("the cycle expired too early: %+v", got.Alerts)
	}
	got := engine.Tick(testStart.Add(40 * time.Minute))
	if len(got.Alerts) != 1 || got.Alerts[0].Status != domain.AlertStateExpired || len(got.Signals) != 0 {
		t.Fatalf("expected an expired warning without a new signal: %+v %v", got.Alerts, kinds(got.Signals))
	}
	if res := engine.Observe(testEvent("BTCUSDT", "long", testStart.Add(41*time.Minute), 400000)); len(res.Alerts) != 1 || res.Alerts[0].Status != domain.AlertStateWatching {
		t.Fatalf("a fresh breach after expiry must alert again: %+v", res.Alerts)
	}
}

// TestMinimumsGateBothPhases checks FollowUpMinimumUSD and OppositeMinimumUSD, the two
// settings that default to zero so any liquidation counts.
func TestMinimumsGateBothPhases(t *testing.T) {
	cfg := Defaults()
	cfg.FollowUpMinimumUSD = 50000
	cfg.OppositeMinimumUSD = 50000
	engine := testEngine(t, cfg, "BTCUSDT")
	if res := engine.Observe(testEvent("BTCUSDT", "long", testStart, 400000)); len(res.Alerts) != 1 {
		t.Fatalf("expected a warning")
	}
	// A same side liquidation below the follow-up minimum must leave the quiet
	// deadline alone, so the aggressive signal still fires on schedule.
	small := engine.Observe(testEvent("BTCUSDT", "long", testStart.Add(5*time.Minute), 10000))
	if len(small.Signals) != 0 {
		t.Fatalf("a below minimum follow-up must not signal: %v", kinds(small.Signals))
	}
	if !small.empty() {
		t.Fatalf("a below minimum follow-up must not change the warning row: %+v", small.Alerts)
	}
	ticked := engine.Tick(testStart.Add(10 * time.Minute))
	if len(ticked.Signals) != 1 || ticked.Signals[0].FollowUps != 0 {
		t.Fatalf("expected one aggressive signal without counted follow-ups: %+v", ticked.Signals)
	}
	if below := engine.Observe(testEvent("BTCUSDT", "short", testStart.Add(11*time.Minute), 20000)); len(below.Signals) != 0 {
		t.Fatalf("a below minimum opposite liquidation must not confirm: %v", kinds(below.Signals))
	}
	confirmed := engine.Observe(testEvent("BTCUSDT", "short", testStart.Add(12*time.Minute), 60000))
	if len(confirmed.Signals) != 1 || confirmed.Signals[0].Kind != domain.SignalSteady {
		t.Fatalf("the first opposite liquidation above the minimum must confirm: %v", kinds(confirmed.Signals))
	}
	if confirmed.Signals[0].OppositeUSD != 80000 || confirmed.Signals[0].OppositeCount != 2 {
		t.Fatalf("both opposite events should be accumulated for reporting: %+v", confirmed.Signals[0])
	}
}

// TestUnwatchedAndInvalidEventsAreIgnored keeps the engine from tracking symbols that
// left the watch list and from trusting malformed events.
func TestUnwatchedAndInvalidEventsAreIgnored(t *testing.T) {
	engine := testEngine(t, Defaults(), "BTCUSDT")
	if res := engine.Observe(testEvent("SOLUSDT", "long", testStart, 5000000)); !res.empty() {
		t.Fatalf("an unwatched symbol must be ignored: %+v", res)
	}
	if res := engine.Observe(testEvent("BTCUSDT", "long", testStart, 0)); !res.empty() {
		t.Fatalf("a zero notional event must be ignored")
	}
	if res := engine.Observe(testEvent("BTCUSDT", "both", testStart, 500000)); !res.empty() {
		t.Fatalf("an unknown position side must be ignored")
	}
	snapshot := engine.Snapshot(testStart)
	if len(snapshot) != 1 || snapshot[0].Symbol != "BTCUSDT" || snapshot[0].State != "idle" {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}
}

// TestDroppedSymbolStopsTracking matters because the top 20 list rotates: a symbol
// that falls out must not keep emitting alerts.
func TestDroppedSymbolStopsTracking(t *testing.T) {
	engine := testEngine(t, Defaults(), "BTCUSDT", "ETHUSDT")
	engine.Watch([]WatchEntry{{Symbol: "ETHUSDT", Rank: 1}})
	if res := engine.Observe(testEvent("BTCUSDT", "long", testStart, 400000)); !res.empty() {
		t.Fatalf("a dropped symbol must be ignored: %+v", res)
	}
	snapshot := engine.Snapshot(testStart)
	if len(snapshot) != 1 || snapshot[0].Symbol != "ETHUSDT" || snapshot[0].Rank != 1 {
		t.Fatalf("unexpected watch list after rotation: %+v", snapshot)
	}
}

// TestSeedReplayIsIdempotent proves a restart replay reproduces the same row IDs, so
// the store can drop duplicates instead of notifying twice.
func TestSeedReplayIsIdempotent(t *testing.T) {
	cfg := Defaults()
	cfg.VWAPPeriodMinutes = 3
	first := testEngine(t, cfg, "BTCUSDT")
	second := testEngine(t, cfg, "BTCUSDT")
	trigger := testEvent("BTCUSDT", "long", testStart, 400000)
	a, b := first.Seed([]domain.LiquidationEvent{trigger}), second.Seed([]domain.LiquidationEvent{trigger})
	if len(a.Alerts) != 1 || len(b.Alerts) != 1 || a.Alerts[0].ID != b.Alerts[0].ID {
		t.Fatalf("warning IDs must be deterministic: %v vs %v", a.Alerts, b.Alerts)
	}
	first.UpdateMarketContext("BTCUSDT", radarContext(103, radarCandles(100, 101, 102)))
	second.UpdateMarketContext("BTCUSDT", radarContext(103, radarCandles(100, 101, 102)))
	ta := first.Tick(testStart.Add(10 * time.Minute))
	tb := second.Tick(testStart.Add(10 * time.Minute))
	if len(ta.Signals) != 1 || len(tb.Signals) != 1 || ta.Signals[0].ID != tb.Signals[0].ID {
		t.Fatalf("signal IDs must be deterministic: %v vs %v", ta.Signals, tb.Signals)
	}
	confirm := testEvent("BTCUSDT", "short", testStart.Add(12*time.Minute), 150000)
	ca, cb := first.Observe(confirm), second.Observe(confirm)
	if len(ca.Signals) != 1 || len(cb.Signals) != 1 || ca.Signals[0].ID != cb.Signals[0].ID {
		t.Fatalf("steady IDs must be deterministic: %v vs %v", ca.Signals, cb.Signals)
	}
}

// TestZeroConfigKeepsProductionDefaults protects callers that build a Config by hand,
// such as an empty config.Config in a test server.
func TestZeroConfigKeepsProductionDefaults(t *testing.T) {
	engine := testEngine(t, Config{}, "BTCUSDT")
	got := engine.Config()
	if got.Window != 3*time.Minute || got.Quiet != 10*time.Minute || got.Confirm != 30*time.Minute || got.BTCUSD != 300000 || got.ETHUSD != 200000 || got.OtherUSD != 100000 || got.VWAPRangeMinutes != 1440 || got.VWAPMinDrawdown != .022 || got.VWAPDonchianFraction != .35 || !got.VWAPFilterEnabled || !got.DonchianFilterEnabled {
		t.Fatalf("zero values must fall back to the documented defaults: %+v", got)
	}
	thresholds := engine.Thresholds()
	if thresholds.WindowMinutes != 3 || thresholds.QuietMinutes != 10 || thresholds.ConfirmMinutes != 30 || thresholds.TopSymbols != domain.AlertTopSymbolsDefault {
		t.Fatalf("the threshold view must publish minutes and the top N: %+v", thresholds)
	}
	if got.ThresholdFor("ethusdt") != 200000 || got.ThresholdFor("SOLUSDT") != 100000 {
		t.Fatalf("threshold lookup must normalize the symbol: %+v", got)
	}
}

func TestVWAPZoneBoundsUseSymmetricDonchianGates(t *testing.T) {
	cfg := Defaults()
	green, red, overlap, ok := VWAPZoneBounds(cfg, 83000, 100, 84800, 81300)
	if !ok || overlap {
		t.Fatalf("valid separated bounds must be available: green=%v red=%v overlap=%v", green, red, overlap)
	}
	if green != 82525 || red != 83575 {
		t.Fatalf("unexpected effective bounds: green=%v red=%v", green, red)
	}
	if _, _, ready := DonchianRange(radarCandles(100, 101), 3); ready {
		t.Fatal("an incomplete Donchian window must fail closed")
	}
}

func TestRadarMiddleZoneBlocksBothOpeningDirections(t *testing.T) {
	cfg := Defaults()
	cfg.VWAPPeriodMinutes = 3
	cfg.VWAPRangeMinutes = 3
	candles := radarCandles(98, 100, 102)
	ctx := radarContext(100, candles)

	long := filterDecision(cfg, "long", ctx, symbolState{}, 100)
	if long.OpenAllowed || !long.CloseAllowed {
		t.Fatalf("middle zone must keep long liquidation close-only: %+v", long)
	}
	short := filterDecision(cfg, "short", ctx, symbolState{}, 100)
	if short.OpenAllowed || !short.CloseAllowed {
		t.Fatalf("middle zone must keep short liquidation close-only: %+v", short)
	}
}

func TestRadarZoneFilterSwitchesAreIndependentAndCanonical(t *testing.T) {
	candles := radarCandles(98, 100, 102)
	ctx := radarShortContext(100, candles)

	cases := []struct {
		name     string
		vwap     bool
		donchian bool
		wantOpen bool
		wantTags string
	}{
		{name: "both enabled", vwap: true, donchian: true, wantTags: "vwap,donchian"},
		{name: "only vwap", vwap: true, donchian: false, wantTags: "vwap"},
		{name: "only donchian", vwap: false, donchian: true, wantTags: "donchian"},
		{name: "both disabled", vwap: false, donchian: false, wantOpen: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Defaults()
			cfg.VWAPPeriodMinutes = 3
			cfg.VWAPRangeMinutes = 3
			cfg.VWAPFilterEnabled, cfg.DonchianFilterEnabled = tc.vwap, tc.donchian
			cfg.FilterSettingsSet = true
			decision := filterDecision(cfg, "short", ctx, symbolState{}, 100)
			if decision.OpenAllowed != tc.wantOpen {
				t.Fatalf("open allowed = %v, want %v: %+v", decision.OpenAllowed, tc.wantOpen, decision)
			}
			if got := strings.Join(decision.FilterTags, ","); got != tc.wantTags {
				t.Fatalf("filter tags = %q, want %q: %+v", got, tc.wantTags, decision)
			}
		})
	}
}

func TestRadarDisabledComponentIgnoresMissingData(t *testing.T) {
	validDonchian := MarketContext{
		Ready: true, Price: 100, Candles: radarCandles(100, 101),
		CurrentCandle: domain.Candle{Time: testStart, Close: 100},
		DonchianHigh:  102, DonchianLow: 98, DonchianReady: true,
	}
	cfg := Defaults()
	cfg.VWAPFilterEnabled = false
	cfg.DonchianFilterEnabled = true
	cfg.FilterSettingsSet = true
	donchianOnly := filterDecision(cfg, "short", validDonchian, symbolState{}, 100)
	if donchianOnly.OpenAllowed || strings.Join(donchianOnly.FilterTags, ",") != "donchian" {
		t.Fatalf("missing VWAP data must not add a VWAP tag: %+v", donchianOnly)
	}

	validVWAP := radarShortContext(100, radarCandles(98, 100, 102))
	validVWAP.DonchianReady = false
	validVWAP.DonchianHigh, validVWAP.DonchianLow = 0, 0
	cfg.VWAPFilterEnabled = true
	cfg.DonchianFilterEnabled = false
	vwapOnly := filterDecision(cfg, "short", validVWAP, symbolState{}, 100)
	if vwapOnly.OpenAllowed || strings.Join(vwapOnly.FilterTags, ",") != "vwap" {
		t.Fatalf("missing Donchian data must not add a Donchian tag: %+v", vwapOnly)
	}
}

func TestRadarRiskBlockDoesNotAddZoneFilterTags(t *testing.T) {
	cfg := Defaults()
	cfg.VWAPFilterEnabled = false
	cfg.DonchianFilterEnabled = false
	cfg.FilterSettingsSet = true
	decision := filterDecision(cfg, "long", radarContext(100, radarCandles(98, 100, 102)), symbolState{}, 100)
	if decision.OpenAllowed || len(decision.FilterTags) != 0 || !strings.Contains(decision.Reason, "deep_liquidation_quota_not_ready") {
		t.Fatalf("non-zone risk block must not carry VWAP/Donchian tags: %+v", decision)
	}
}

func TestRadarInvalidOpeningZoneFailsClosedForBothDirections(t *testing.T) {
	cfg := Defaults()
	cfg.VWAPPeriodMinutes = 3
	cfg.VWAPRangeMinutes = 3
	cfg.VWAPDonchianFraction = 0
	ctx := radarContext(100, radarCandles(98, 100, 102))

	for _, side := range []string{"long", "short"} {
		decision := filterDecision(cfg, side, ctx, symbolState{}, 100)
		if decision.OpenAllowed || !decision.CloseAllowed {
			t.Fatalf("invalid VWAP zone must fail closed for %s liquidation: %+v", side, decision)
		}
		if decision.Reason != "vwap_zone_unavailable_"+side+"_liquidation_close_only" {
			t.Fatalf("invalid VWAP zone reason for %s = %q", side, decision.Reason)
		}
	}
}

func radarCandles(closes ...float64) []domain.Candle {
	out := make([]domain.Candle, 0, len(closes))
	for i, close := range closes {
		out = append(out, domain.Candle{
			Time: testStart.Add(time.Duration(i) * time.Minute),
			Open: close - .25, High: close + .5, Low: close - .5, Close: close, VolumeUSD: 1,
		})
	}
	return out
}

func radarContext(price float64, candles []domain.Candle) MarketContext {
	current := domain.Candle{Time: testStart, Open: price - 1, High: price + 1, Low: price - 2, Close: price}
	high, low, ready := DonchianRange(candles, len(candles))
	return MarketContext{Ready: true, Price: price, Candles: candles, CurrentCandle: current, DonchianHigh: high, DonchianLow: low, DonchianReady: ready}
}

func radarShortContext(price float64, candles []domain.Candle) MarketContext {
	current := domain.Candle{Time: testStart, Open: price - 1, High: price + 2, Low: price - 1, Close: price}
	high, low, ready := DonchianRange(candles, len(candles))
	return MarketContext{Ready: true, Price: price, Candles: candles, CurrentCandle: current, DonchianHigh: high, DonchianLow: low, DonchianReady: ready}
}

func TestRadarArmedPeakDecayAndHighZoneShortEntry(t *testing.T) {
	cfg := Defaults()
	cfg.Window = 3 * time.Minute
	cfg.OtherUSD = 100
	cfg.VWAPPeriodMinutes = 3
	cfg.PeakDecayWindow = 30 * time.Second
	engine := testEngine(t, cfg, "SOLUSDT")
	candles := radarCandles(100, 101, 102)
	ctx := radarShortContext(103, candles)
	first := testEvent("SOLUSDT", "short", testStart, 150)
	first.Price = 103
	armed := engine.ObserveWithContext(first, ctx)
	if len(armed.Signals) != 0 || len(armed.Alerts) != 1 || !armed.Alerts[0].FilterApplied {
		t.Fatalf("threshold breach must only arm the radar: %+v", armed)
	}
	if got := engine.Tick(testStart.Add(20 * time.Second)); len(got.Signals) != 0 {
		t.Fatalf("radar fired before the decay window: %+v", got.Signals)
	}
	fired := engine.Tick(testStart.Add(31 * time.Second))
	if len(fired.Signals) != 1 || fired.Signals[0].Side != "long" || !fired.Signals[0].OpenAllowed || fired.Signals[0].PeakDecayRatio >= .4 {
		t.Fatalf("decayed high-zone short signal=%+v", fired.Signals)
	}
}

func TestHistoricalRecalculationSkipsPeakDecay(t *testing.T) {
	cfg := Defaults()
	cfg.OtherUSD = 100
	cfg.VWAPPeriodMinutes = 3
	cfg.PeakDecayWindow = 30 * time.Second
	cfg.SkipPeakDecay = true
	engine := testEngine(t, cfg, "SOLUSDT")
	candles := radarCandles(100, 101, 102)
	ctx := radarShortContext(103, candles)
	first := testEvent("SOLUSDT", "short", testStart, 150)
	first.Price = 103
	result := engine.ObserveWithContext(first, ctx)
	if len(result.Signals) != 1 || result.Signals[0].Kind != domain.SignalAggressive {
		t.Fatalf("historical recalculation should fire without peak-decay: %+v", result)
	}
	signal := result.Signals[0]
	if signal.CreatedAt != testStart || signal.QuietElapsedSeconds != 0 || signal.PeakDecayRatio != 0 || !signal.AbsorptionConfirmed {
		t.Fatalf("historical signal should record skipped peak-decay gates: %+v", signal)
	}
	if strings.Contains(signal.FilterReason, "peak_decay") || strings.Contains(signal.FilterReason, "absorption") {
		t.Fatalf("historical signal should not carry peak-decay blockers: %+v", signal)
	}
}

func TestRadarContextUnavailableNeverFallsBackToStaticSignal(t *testing.T) {
	cfg := Defaults()
	cfg.OtherUSD = 100
	cfg.VWAPPeriodMinutes = 3
	engine := testEngine(t, cfg, "SOLUSDT")
	first := testEvent("SOLUSDT", "long", testStart, 150)
	first.Price = 99
	armed := engine.ObserveWithContext(first, MarketContext{})
	if len(armed.Signals) != 0 || len(armed.Alerts) != 1 || armed.Alerts[0].Phase != domain.AlertPhaseArmed {
		t.Fatalf("missing market context must remain ARMED: %+v", armed)
	}
	if got := engine.Tick(testStart.Add(31 * time.Second)); len(got.Signals) != 0 {
		t.Fatalf("missing market context must not create a static signal: %+v", got.Signals)
	}
	engine.UpdateMarketContext("SOLUSDT", radarContext(103, radarCandles(100, 101, 102)))
	fired := engine.Tick(testStart.Add(62 * time.Second))
	if len(fired.Signals) != 1 || fired.Signals[0].Phase != domain.AlertPhaseFired || fired.Signals[0].PeakDecayRatio >= .4 || !fired.Signals[0].AbsorptionConfirmed {
		t.Fatalf("fresh context should allow the decayed signal: %+v", fired.Signals)
	}
}

func TestRadarShortUsesUpperWickAbsorption(t *testing.T) {
	if !candleAbsorbed("short", domain.Candle{High: 110, Low: 100, Close: 106}, 106, .35) {
		t.Fatal("short cascade should accept a clear upper wick")
	}
	if candleAbsorbed("short", domain.Candle{High: 110, Low: 100, Close: 109}, 109, .35) {
		t.Fatal("short cascade should reject a candle without upper absorption")
	}
}

func TestRadarDeepQuotaBlocksEarlyLongThenUnlocks(t *testing.T) {
	cfg := Defaults()
	cfg.Window = 3 * time.Minute
	cfg.OtherUSD = 100
	cfg.VWAPPeriodMinutes = 3
	cfg.PeakDecayWindow = 30 * time.Second
	cfg.DeepQuotaMultiplier = 2
	engine := testEngine(t, cfg, "SOLUSDT")
	candles := radarCandles(100, 100.5, 101)
	ctx := radarContext(99, candles)
	first := testEvent("SOLUSDT", "long", testStart, 120)
	first.Price = 99
	armed := engine.ObserveWithContext(first, ctx)
	if len(armed.Signals) != 0 {
		t.Fatalf("the shallow liquidation must not open: %+v", armed.Signals)
	}
	if got := engine.Tick(testStart.Add(31 * time.Second)); len(got.Signals) != 0 {
		t.Fatalf("quota should keep the first long cycle armed: %+v", got.Signals)
	}
	second := testEvent("SOLUSDT", "long", testStart.Add(31*time.Second), 120)
	second.Price = 99
	engine.ObserveWithContext(second, ctx)
	fired := engine.Tick(testStart.Add(62 * time.Second))
	if len(fired.Signals) != 1 || !fired.Signals[0].OpenAllowed || fired.Signals[0].CumulativeLongLiquidationUSD < 240 {
		t.Fatalf("deep quota should unlock the long signal: %+v", fired.Signals)
	}
}

func TestRadarNeutralZoneAndMinProfitSpread(t *testing.T) {
	cfg := Defaults()
	cfg.VWAPPeriodMinutes = 3
	candles := radarCandles(98, 100, 102)
	ctx := radarContext(100, candles)
	ctx.PositionDataAvailable = true
	ctx.ShortPositionCount = 1
	ctx.ShortAvgPrice = 100
	d := filterDecision(cfg, "short", ctx, symbolState{}, 100)
	if d.OpenAllowed || d.CloseAllowed {
		t.Fatalf("neutral zone must not open or close without the profit spread: %+v", d)
	}
	ctx.Price = 99.4
	d = filterDecision(cfg, "short", ctx, symbolState{}, 100)
	if d.OpenAllowed || d.CloseAllowed {
		t.Fatalf("0.6%% short profit is below the configured 0.7%% gate at this price: %+v", d)
	}
	ctx.Price = 99.2
	d = filterDecision(cfg, "short", ctx, symbolState{}, 100)
	if d.OpenAllowed || !d.CloseAllowed {
		t.Fatalf("the short close should unlock only after the minimum spread: %+v", d)
	}
}
