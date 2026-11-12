package binance

import (
	"testing"
	"time"

	trading "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

func TestValueAreaReverseBypassesMinimumHolding(t *testing.T) {
	now := time.Date(2026, time.November, 12, 1, 30, 0, 0, time.UTC)
	position := Position{
		Symbol:       "IDOLUSDT",
		PositionSide: "SHORT",
		PositionAmt:  "-100",
		UpdateTime:   now.Add(-time.Minute).UnixMilli(),
	}
	for _, condition := range []string{"value_area_signal", "value_area_manual"} {
		signal := trading.Signal{Condition: condition, PositionEffect: trading.PositionEffectOpen}
		if got := reversePositionHoldingMinutes(signal); got != 0 {
			t.Fatalf("condition=%q holding minutes=%d want 0", condition, got)
		}
		if err := ensureBinanceReversePositionHolding(position, reversePositionHoldingMinutes(signal), now); err != nil {
			t.Fatalf("condition=%q should allow immediate reverse: %v", condition, err)
		}
	}

	ordinary := trading.Signal{Condition: "liquidation_alert", PositionEffect: trading.PositionEffectOpen}
	if got := reversePositionHoldingMinutes(ordinary); got != 15 {
		t.Fatalf("ordinary holding minutes=%d want 15", got)
	}
	if err := ensureBinanceReversePositionHolding(position, reversePositionHoldingMinutes(ordinary), now); err == nil {
		t.Fatal("ordinary reverse should remain blocked before 15 minutes")
	}
}
