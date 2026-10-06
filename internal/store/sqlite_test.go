package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

func TestStoreRoundTripAndDedup(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	c := domain.Candle{Exchange: "binance", Symbol: "BTCUSDT", Time: time.Now().UTC().Truncate(time.Minute), Open: 1, High: 2, Low: .5, Close: 1.5, OpenInterestUSD: 10}
	if err = s.UpsertCandles(ctx, []domain.Candle{c}); err != nil {
		t.Fatal(err)
	}
	cs, err := s.Candles(ctx, "BTCUSDT", c.Time.Add(-time.Minute))
	if err != nil || len(cs) != 1 {
		t.Fatalf("candles=%d err=%v", len(cs), err)
	}
	e := domain.LiquidationEvent{ID: "same", Exchange: "binance", Symbol: "BTCUSDT", PositionSide: "long", EventTime: c.Time, ReceivedAt: c.Time, Price: 2, Quantity: 3, NotionalUSD: 6, Coverage: "sampled"}
	if err = s.InsertLiquidation(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err = s.InsertLiquidation(ctx, e); err != nil {
		t.Fatal(err)
	}
	long, short, err := s.LiquidationTotals(ctx, "BTCUSDT", c.Time.Add(-time.Minute))
	if err != nil || long != 6 || short != 0 {
		t.Fatalf("long=%v short=%v err=%v", long, short, err)
	}
}
