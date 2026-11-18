package app

import (
	"context"
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
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

func TestEnqueueAggregateTradeOnlyQueuesRequestedProfiles(t *testing.T) {
	svc := &Service{
		latestPrices:    make(map[string]domain.PriceTick),
		volumeRequested: map[string]struct{}{"BTCUSDT": {}},
	}
	marketInput := make(chan domain.AggregateTrade, 2)
	volumeInput := make(chan domain.AggregateTrade, 2)
	trade := domain.AggregateTrade{ID: 1, Symbol: "SOLUSDT", Time: time.Now().UTC(), Price: 100, Quantity: 1}

	if err := svc.enqueueAggregateTrade(context.Background(), marketInput, volumeInput, trade); err != nil {
		t.Fatal(err)
	}
	if len(marketInput) != 1 {
		t.Fatalf("market candle queue length=%d, want 1", len(marketInput))
	}
	if len(volumeInput) != 0 {
		t.Fatalf("unrequested profile queue length=%d, want 0", len(volumeInput))
	}
}

func TestEnqueueAggregateTradeDoesNotBlockWhenQueuesAreFull(t *testing.T) {
	svc := &Service{
		latestPrices:    make(map[string]domain.PriceTick),
		volumeRequested: map[string]struct{}{"BTCUSDT": {}},
	}
	trade := domain.AggregateTrade{ID: 2, Symbol: "BTCUSDT", Time: time.Now().UTC(), Price: 65000, Quantity: 1}
	marketInput := make(chan domain.AggregateTrade, 1)
	volumeInput := make(chan domain.AggregateTrade, 1)
	marketInput <- trade
	volumeInput <- trade

	if err := svc.enqueueAggregateTrade(context.Background(), marketInput, volumeInput, trade); err != nil {
		t.Fatal(err)
	}
	if got := svc.latestPrices[trade.Symbol]; got.TradeID != trade.ID {
		t.Fatalf("latest price tick=%+v, want trade id %d", got, trade.ID)
	}
}
