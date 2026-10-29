package tvbotbridge

import (
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/exchange"
)

func TestAppendProtectedCandidatesKeepsPositionAndOrderSymbols(t *testing.T) {
	now := time.Now().UTC()
	snapshot := domain.BackgroundSymbolSnapshot{
		Symbols:   []domain.WatchSymbol{{Symbol: "BTCUSDT", Rank: 1, QuoteVolumeUSD: 100}},
		BaseCount: 1,
	}
	rows := []exchange.TopSymbol{{Symbol: "ETHUSDT", QuoteVolumeUSD: 80}}
	appendProtectedCandidates(&snapshot, rows,
		map[string]struct{}{"BTCUSDT": {}},
		map[string]struct{}{"BTCUSDT": {}, "ETHUSDT": {}, "XRPUSDT": {}}, now)
	if len(snapshot.Symbols) != 3 || len(snapshot.ExceptionSymbols) != 2 {
		t.Fatalf("protected snapshot=%+v", snapshot)
	}
	if snapshot.ExceptionSymbols[0] != "ETHUSDT" || snapshot.ExceptionSymbols[1] != "XRPUSDT" {
		t.Fatalf("protected symbols=%v", snapshot.ExceptionSymbols)
	}
	if snapshot.Symbols[1].Symbol != "ETHUSDT" || snapshot.Symbols[1].QuoteVolumeUSD != 80 || snapshot.Symbols[2].Symbol != "XRPUSDT" {
		t.Fatalf("protected rows=%v", snapshot.Symbols)
	}
}
