package trading

import (
	"math"
	"testing"
)

func TestLayeredOrderSettingsDefaultsAndPlan(t *testing.T) {
	settings := DefaultLayeredOrderSettings()
	if err := settings.Validate(); err != nil {
		t.Fatal(err)
	}
	long, err := settings.BuildPlan(ActionLong, 10000, 100, 110)
	if err != nil {
		t.Fatal(err)
	}
	if len(long) != 6 || long[0].OrderType != OrderTypeMarket || long[1].Price != 100 {
		t.Fatalf("unexpected long plan: %#v", long)
	}
	if long[2].Price != 109.45 || long[5].Price != 106.7 {
		t.Fatalf("unexpected long offset prices: %#v", long)
	}
	short, err := settings.BuildPlan(ActionShort, 10000, 100, 110)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(short[2].Price-110.55) > 1e-9 || math.Abs(short[5].Price-113.3) > 1e-9 {
		t.Fatalf("unexpected short offset prices: %#v", short)
	}
	for _, leg := range long {
		if leg.AmountPct != 10 && leg.AmountPct != 18 {
			t.Fatalf("unexpected amount percentage: %#v", leg)
		}
	}
}

func TestLayeredOrderSettingsRejectsInvalidTotalsAndOffsets(t *testing.T) {
	settings := DefaultLayeredOrderSettings()
	settings.PriceLayers[0].AmountPct = 17
	if err := settings.Validate(); err == nil {
		t.Fatal("expected percentage total validation error")
	}
	settings = DefaultLayeredOrderSettings()
	settings.PriceLayers[0].OffsetPct = -0.1
	if err := settings.Validate(); err == nil {
		t.Fatal("expected offset validation error")
	}
}
