package okx

import (
	"strconv"
	"testing"
	"time"

	trading "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

func TestValueAreaReverseRespectsMinimumHolding(t *testing.T) {
	now := time.Date(2026, time.November, 12, 1, 30, 0, 0, time.UTC)
	position := Position{
		InstID:  "IDOL-USDT-SWAP",
		PosSide: "short",
		Pos:     "100",
		CTime:   strconv.FormatInt(now.Add(-time.Minute).UnixMilli(), 10),
	}
	for _, condition := range []string{"value_area_signal", "value_area_manual"} {
		signal := trading.Signal{Condition: condition, PositionEffect: trading.PositionEffectOpen}
		if got := reversePositionHoldingMinutes(signal); got != 15 {
			t.Fatalf("condition=%q holding minutes=%d want 15", condition, got)
		}
		if err := ensureOKXReversePositionHolding(position, reversePositionHoldingMinutes(signal), now); err == nil {
			t.Fatalf("condition=%q should block reverse before 15 minutes", condition)
		}
		position.CTime = strconv.FormatInt(now.Add(-15*time.Minute-time.Second).UnixMilli(), 10)
		if err := ensureOKXReversePositionHolding(position, reversePositionHoldingMinutes(signal), now); err != nil {
			t.Fatalf("condition=%q should allow reverse after 15 minutes: %v", condition, err)
		}
		position.CTime = strconv.FormatInt(now.Add(-time.Minute).UnixMilli(), 10)
	}

	ordinary := trading.Signal{Condition: "liquidation_alert", PositionEffect: trading.PositionEffectOpen}
	if got := reversePositionHoldingMinutes(ordinary); got != 15 {
		t.Fatalf("ordinary holding minutes=%d want 15", got)
	}
	if err := ensureOKXReversePositionHolding(position, reversePositionHoldingMinutes(ordinary), now); err == nil {
		t.Fatal("ordinary reverse should remain blocked before 15 minutes")
	}
}
