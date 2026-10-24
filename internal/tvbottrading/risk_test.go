package trading

import "testing"

func TestFixedTPSLTriggerPricesUseLeverage(t *testing.T) {
	tp, sl, err := FixedTPSLTriggerPrices(ActionLong, 100, 20, 21, 20)
	if err != nil {
		t.Fatal(err)
	}
	if tp != 101 || sl != 98.95 {
		t.Fatalf("long triggers = %v, %v; want 101, 98.95", tp, sl)
	}

	tp, sl, err = FixedTPSLTriggerPrices(ActionShort, 100, 20, 21, 20)
	if err != nil {
		t.Fatal(err)
	}
	if tp != 99 || sl != 101.05 {
		t.Fatalf("short triggers = %v, %v; want 99, 101.05", tp, sl)
	}
}

func TestFixedTPSLTriggerPricesRejectInvalidLeverage(t *testing.T) {
	if _, _, err := FixedTPSLTriggerPrices(ActionLong, 100, 2, 1, 0); err == nil {
		t.Fatal("expected invalid leverage error")
	}
}
