package app

import (
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

func TestScoreAlertBacktestUsesHoldingCloseAndAddOns(t *testing.T) {
	base := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	metrics := scoreAlertBacktest([]alertBacktestEntry{
		{
			at:        base,
			price:     100,
			side:      "long",
			additions: 2,
			candles: []domain.Candle{
				{Time: base.Add(10 * time.Minute), Close: 120},
				{Time: base.Add(alertBacktestHolding), Close: 110},
			},
		},
		{
			at:    base.Add(time.Minute),
			price: 100,
			side:  "short",
			candles: []domain.Candle{
				{Time: base.Add(16 * time.Minute), Close: 90},
			},
		},
	})

	if metrics.trades != 2 || metrics.additions != 2 || metrics.wins != 2 {
		t.Fatalf("unexpected trade accounting: %+v", metrics)
	}
	if metrics.finalEquity != 103000 || metrics.grossProfit != 3000 || metrics.maxDrawdown != 0 {
		t.Fatalf("unexpected score: %+v", metrics)
	}
}
