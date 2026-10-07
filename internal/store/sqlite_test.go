package store

import (
	"context"
	"encoding/json"
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
	outlier := c
	outlier.Exchange, outlier.Close = "okx", 999
	if err = s.UpsertCandles(ctx, []domain.Candle{c, outlier}); err != nil {
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
	e.ID, e.Exchange, e.NotionalUSD = "okx-event", "okx", 1000
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

func TestVolumeProfileTradesAreAtomicAndDeduplicated(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	start := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	trades := []domain.AggregateTrade{{ID: 10, Symbol: "BTCUSDT", Time: start.Add(time.Second), Price: 100, PriceText: "100.0", Quantity: 2}, {ID: 11, Symbol: "BTCUSDT", Time: start.Add(2 * time.Second), Price: 101, PriceText: "101.0", Quantity: 3}}
	if err = s.ApplyAggregateTrades(ctx, "BTCUSDT", start, trades, true); err != nil {
		t.Fatal(err)
	}
	if err = s.ApplyAggregateTrades(ctx, "BTCUSDT", start, trades, true); err != nil {
		t.Fatal(err)
	}
	levels, lastID, through, complete, err := s.VolumeProfileLevels(ctx, "BTCUSDT", start)
	if err != nil || len(levels) != 2 || lastID != 11 || !through.Equal(start.Add(2*time.Second)) || !complete {
		t.Fatalf("levels=%#v last=%d through=%v complete=%v err=%v", levels, lastID, through, complete, err)
	}
	if levels[0].VolumeUSD != 200 || levels[1].VolumeUSD != 303 {
		t.Fatalf("levels=%#v", levels)
	}
}

func TestRangesPredictionsAndRetention(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 4; i++ {
		c := domain.Candle{Exchange: "binance", Symbol: "BTCUSDT", Time: start.Add(time.Duration(i) * time.Minute), Open: 1, High: 2, Low: .5, Close: 1.5}
		if err = s.UpsertCandles(ctx, []domain.Candle{c}); err != nil {
			t.Fatal(err)
		}
	}
	cs, err := s.CandlesRange(ctx, "BTCUSDT", start.Add(time.Minute), start.Add(3*time.Minute))
	if err != nil || len(cs) != 2 || !cs[0].Time.Equal(start.Add(time.Minute)) || !cs[1].Time.Equal(start.Add(2*time.Minute)) {
		t.Fatalf("range=%#v err=%v", cs, err)
	}
	first, last, ok, err := s.CandleBounds(ctx, "binance", "BTCUSDT")
	if err != nil || !ok || !first.Equal(start) || !last.Equal(start.Add(3*time.Minute)) {
		t.Fatalf("bounds=%v %v %v err=%v", first, last, ok, err)
	}
	for i := 0; i < 3; i++ {
		p := domain.Prediction{Symbol: "BTCUSDT", Time: start.Add(time.Duration(i) * time.Minute), State: "ok"}
		if err = s.SavePrediction(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	legacy, err := json.Marshal(domain.Prediction{Symbol: "BTCUSDT", Time: start.Add(3 * time.Minute), State: "ok"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `INSERT INTO predictions(symbol,ts,source,payload) VALUES(?,?,?,?)`, "BTCUSDT", start.Add(3*time.Minute).UnixMilli(), "", legacy); err != nil {
		t.Fatal(err)
	}
	ps, err := s.Predictions(ctx, "BTCUSDT", start.Add(time.Minute), start.Add(4*time.Minute))
	if err != nil || len(ps) != 2 {
		t.Fatalf("predictions=%#v err=%v", ps, err)
	}
	if err = s.PruneBefore(ctx, start.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	cs, _ = s.Candles(ctx, "BTCUSDT", start)
	ps, _ = s.Predictions(ctx, "BTCUSDT", start, start.Add(4*time.Minute))
	if len(cs) != 2 || len(ps) != 1 {
		t.Fatalf("after prune candles=%d predictions=%d", len(cs), len(ps))
	}
}
