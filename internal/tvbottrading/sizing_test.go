package trading

import "testing"

func TestSizeFromUSDTNotional(t *testing.T) {
	got, err := SizeFromUSDTNotional(100, 50000, 0.01, 0.01, 0.01)
	if err != nil {
		t.Fatal(err)
	}
	if got != "0.2" {
		t.Fatalf("size = %q, want 0.2", got)
	}
}

func TestSizeFromUSDTNotionalBelowMinimum(t *testing.T) {
	_, err := SizeFromUSDTNotional(1, 50000, 0.01, 0.01, 0.01)
	if err == nil {
		t.Fatal("expected below minimum error")
	}
}

func TestSizeFromContractsFloorsToLotSize(t *testing.T) {
	got, err := SizeFromContracts(20.09, 0.1, 0.1)
	if err != nil {
		t.Fatal(err)
	}
	if got != "20" {
		t.Fatalf("size = %q, want 20", got)
	}
}

func TestSizeFromContractsKeepsIntegerTrailingZero(t *testing.T) {
	got, err := SizeFromContracts(20, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got != "20" {
		t.Fatalf("size = %q, want 20", got)
	}
}
