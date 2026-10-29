package tvbotbridge

import (
	"testing"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/exchange"
)

func TestMergeConfiguredCandidatesDeduplicatesBeforeGlobalLimit(t *testing.T) {
	first := make([]exchange.TopSymbol, 0, 3)
	second := make([]exchange.TopSymbol, 0, 3)
	for i, volume := range []float64{100, 90, 80} {
		first = append(first, exchange.TopSymbol{Symbol: string(rune('A'+i)) + "USDT", QuoteVolumeUSD: volume})
	}
	second = append(second,
		exchange.TopSymbol{Symbol: "AUSDT", QuoteVolumeUSD: 110},
		exchange.TopSymbol{Symbol: "DUSDT", QuoteVolumeUSD: 95},
		exchange.TopSymbol{Symbol: "EUSDT", QuoteVolumeUSD: 70},
	)
	got := mergeConfiguredCandidates([][]exchange.TopSymbol{first, second}, 4)
	if len(got) != 4 {
		t.Fatalf("merged=%+v", got)
	}
	want := []string{"AUSDT", "DUSDT", "BUSDT", "CUSDT"}
	for i, symbol := range want {
		if got[i].Symbol != symbol || got[i].Rank != i+1 {
			t.Fatalf("merged=%+v, want symbols=%v with sequential ranks", got, want)
		}
	}
}
