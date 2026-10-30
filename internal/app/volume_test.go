package app

import (
	"testing"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/config"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

func TestVolumeBootstrapSymbolsUsesBoundedConfiguredSet(t *testing.T) {
	svc := &Service{cfg: config.Config{BootstrapSymbols: []string{" btcusdt ", "ETHUSDT", "BTCUSDT", "SOLUSDT", ""}}}
	got := svc.volumeBootstrapSymbols()
	want := []string{"BTCUSDT", "ETHUSDT", "SOLUSDT"}
	if len(got) != len(want) {
		t.Fatalf("volume bootstrap symbols=%v, want=%v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("volume bootstrap symbols=%v, want=%v", got, want)
		}
	}
}

func TestVolumeBootstrapSymbolsFallsBackToBTCAndETH(t *testing.T) {
	svc := &Service{}
	got := svc.volumeBootstrapSymbols()
	want := []string{"BTCUSDT", "ETHUSDT"}
	if len(got) != len(want) {
		t.Fatalf("volume bootstrap fallback=%v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("volume bootstrap fallback=%v, want=%v", got, want)
		}
	}
}

func TestAggregateTradeGap(t *testing.T) {
	tests := []struct {
		name   string
		lastID int64
		ids    []int64
		want   bool
	}{
		{name: "contiguous", lastID: 9, ids: []int64{10, 11, 12}},
		{name: "duplicates", lastID: 10, ids: []int64{10, 11, 11, 12}},
		{name: "first gap", lastID: 9, ids: []int64{11, 12}, want: true},
		{name: "internal gap", lastID: 9, ids: []int64{10, 12}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows := make([]domain.AggregateTrade, len(tt.ids))
			for i, id := range tt.ids {
				rows[i].ID = id
			}
			if got := aggregateTradeGap(rows, tt.lastID); got != tt.want {
				t.Fatalf("aggregateTradeGap() = %v, want %v", got, tt.want)
			}
		})
	}
}
