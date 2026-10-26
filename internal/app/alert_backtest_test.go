package app

import (
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

func TestScoreAlertBacktestUsesNextCloseAndCapsAddOns(t *testing.T) {
	base := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	params := DefaultAlertBacktestParameters()
	params.InitialEquity = 10000
	params.Leverage = 2
	metrics := scoreAlertBacktest([]alertBacktestEntry{
		{
			at:        base,
			price:     100,
			side:      "long",
			additions: 2, // capped to one add-on
			candles: []domain.Candle{
				{Time: base.Add(5 * time.Minute), Close: 101},
				{Time: base.Add(10 * time.Minute), Close: 102}, // TP triggers
				{Time: base.Add(15 * time.Minute), Close: 103}, // next close exits
			},
		},
		{
			at:    base.Add(time.Minute),
			price: 100,
			side:  "short",
			candles: []domain.Candle{
				{Time: base.Add(6 * time.Minute), Close: 98},  // TP triggers
				{Time: base.Add(11 * time.Minute), Close: 99}, // next close exits
			},
		},
	}, params)

	if metrics.trades != 2 || metrics.additions != 1 || metrics.wins != 2 {
		t.Fatalf("unexpected trade accounting: %+v", metrics)
	}
	if metrics.finalEquity != 10140 || metrics.grossProfit != 140 || metrics.maxDrawdown != 0 {
		t.Fatalf("unexpected score: %+v", metrics)
	}
}

func TestScoreAlertBacktestStopLossAndMissingNextCandle(t *testing.T) {
	base := time.Date(2026, 10, 26, 1, 0, 0, 0, time.UTC)
	params := DefaultAlertBacktestParameters()
	metrics := scoreAlertBacktest([]alertBacktestEntry{
		{
			at:    base,
			price: 100,
			side:  "long",
			candles: []domain.Candle{
				{Time: base.Add(5 * time.Minute), Close: 98}, // SL triggers
				{Time: base.Add(6 * time.Minute), Close: 99}, // next close exits
			},
		},
		{
			at:    base.Add(time.Minute),
			price: 100,
			side:  "short",
			candles: []domain.Candle{
				{Time: base.Add(16 * time.Minute), Close: 90}, // no next candle
			},
		},
	}, params)
	if metrics.trades != 1 || metrics.finalEquity != 9990 || metrics.grossLoss != 10 || metrics.maxDrawdown != 0.1 {
		t.Fatalf("unexpected stop-loss/missing-candle score: %+v", metrics)
	}
}

func TestScoreAlertBacktestUsesWeightedAverageAndLeverage(t *testing.T) {
	base := time.Date(2026, 10, 26, 2, 0, 0, 0, time.UTC)
	params := DefaultAlertBacktestParameters()
	params.InitialEquity = 10000
	params.EntryNotionalUSD = 1000
	params.Leverage = 3
	params.MaxAddOns = 1
	metrics := scoreAlertBacktest([]alertBacktestEntry{{
		at:          base,
		price:       100,
		side:        "long",
		additions:   1,
		addOnPrices: []float64{90},
		candles: []domain.Candle{
			{Time: base.Add(15 * time.Minute), Close: 100},
			{Time: base.Add(16 * time.Minute), Close: 101},
		},
	}}, params)
	// Each order is $1,000 margin at 3x: 30 units at 100 and 33.333... at 90.
	// The quantity-weighted entry is 94.7368..., so the $101 exit earns 396.67.
	if metrics.trades != 1 || metrics.additions != 1 || metrics.finalEquity < 10396.66 || metrics.finalEquity > 10396.67 {
		t.Fatalf("unexpected weighted-average/leverage score: %+v", metrics)
	}
}

func TestValueAreaBacktestUsesGammaAndRearmsAfterCloseOnlyCrossing(t *testing.T) {
	base := time.Date(2026, 10, 26, 3, 0, 0, 0, time.UTC)
	val, vah := 100.0, 110.0
	snapshots := []domain.VolumeProfileSnapshot{{
		Symbol: "BTCUSDT", Time: base.Add(-time.Minute), SessionStart: base.Add(-time.Hour),
		VAL: val, VAH: vah, Complete: true,
	}}
	negative, positive := -0.5, 0.5
	gamma := []domain.OptionGammaPoint{
		{Symbol: "BTCUSDT", Time: base.Add(-time.Minute), Gamma: &negative, State: "ok"},
		{Symbol: "BTCUSDT", Time: base.Add(2 * time.Minute), Gamma: &positive, State: "ok"},
	}
	candles := []domain.Candle{
		{Time: base, Close: 105},
		{Time: base.Add(time.Minute), Close: 99},      // Gamma < 0: close-only.
		{Time: base.Add(2 * time.Minute), Close: 105}, // Re-arm inside the value area.
		{Time: base.Add(3 * time.Minute), Close: 111}, // Gamma > 0: open short.
	}
	signals := valueAreaBacktestSignals("BTCUSDT", candles, snapshots, gamma, base, base.Add(3*time.Minute))
	if len(signals) != 2 || signals[0].PositionEffect != "close" || signals[1].PositionEffect != "open" || signals[1].Side != "short" {
		t.Fatalf("unexpected value-area backtest signals: %+v", signals)
	}
}
