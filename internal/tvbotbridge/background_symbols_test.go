package tvbotbridge

import (
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/app"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/exchange"
	tvbotconfig "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotconfig"
)

func TestOrderScopeBackfillRankLimitIsBoundedToFifty(t *testing.T) {
	checks := []struct {
		name      string
		scopes    []string
		fallback  string
		wantLimit int
	}{
		{name: "small scope", scopes: []string{tvbotconfig.MarketTurnoverScopeTop5}, fallback: tvbotconfig.MarketTurnoverScopeTop100, wantLimit: 5},
		{name: "largest tenant scope wins", scopes: []string{tvbotconfig.MarketTurnoverScopeTop10, tvbotconfig.MarketTurnoverScopeTop20}, fallback: tvbotconfig.MarketTurnoverScopeTop5, wantLimit: 20},
		{name: "all is capped", scopes: []string{tvbotconfig.MarketTurnoverScopeAll}, fallback: tvbotconfig.MarketTurnoverScopeTop5, wantLimit: app.VolumeBackfillCandidateLimitForProvider()},
		{name: "fallback scope", scopes: nil, fallback: tvbotconfig.MarketTurnoverScopeTop20, wantLimit: 20},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if got := orderScopeBackfillRankLimit(check.scopes, check.fallback); got != check.wantLimit {
				t.Fatalf("rank limit=%d, want %d", got, check.wantLimit)
			}
		})
	}
}

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
