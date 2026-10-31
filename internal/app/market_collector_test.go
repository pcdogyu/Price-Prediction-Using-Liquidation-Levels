package app

import (
	"testing"
)

func TestCollectorAggregateSymbolsIncludesAlertWatchList(t *testing.T) {
	svc := &Service{
		candidates:          []string{"ETHUSDT", "SOLUSDT", "ETHUSDT"},
		volumeStreamSymbols: map[string]struct{}{"BTCUSDT": {}, "ETHUSDT": {}},
	}
	got := svc.collectorAggregateSymbols()
	want := []string{"BTCUSDT", "ETHUSDT", "SOLUSDT"}
	if len(got) != len(want) {
		t.Fatalf("collector symbols=%v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("collector symbols=%v, want %v", got, want)
		}
	}
}
