package engine

import (
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

func syntheticCandles(n int) []domain.Candle {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	out := make([]domain.Candle, 0, n)
	for i := 0; i < n; i++ {
		p := 100000.0 + float64(i%7-3)*50
		oi := 1e9 + float64(i)*2e6
		out = append(out, domain.Candle{Exchange: "binance", Symbol: "BTCUSDT", Time: start.Add(time.Duration(i) * time.Minute), Open: p - 20, High: p + 150, Low: p - 150, Close: p, VolumeUSD: 5e7, TakerBuyUSD: 2.6e7, OpenInterestUSD: oi, LongShortRatio: 1.1})
	}
	return out
}

func TestBuildMapFindsBothWalls(t *testing.T) {
	m, err := BuildMap(syntheticCandles(180), DefaultMapConfig())
	if err != nil {
		t.Fatal(err)
	}
	if m.Upper == nil || m.Lower == nil {
		t.Fatalf("expected both walls: %#v", m)
	}
	if m.Upper.Price <= m.MarkPrice || m.Lower.Price >= m.MarkPrice {
		t.Fatalf("walls on wrong sides: %#v %#v", m.Upper, m.Lower)
	}
	if len(m.Bins) == 0 {
		t.Fatal("empty map")
	}
}

func TestLabelFuture(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	future := make([]domain.Candle, 61)
	for i := range future {
		future[i] = domain.Candle{Time: at.Add(time.Duration(i) * time.Minute), Open: 100, High: 101, Low: 99, Close: 100}
	}
	future[10].High = 111
	l, ok := LabelFuture(at, 110, 90, future)
	if !ok || l.Class != domain.UpperFirst {
		t.Fatalf("got %#v %v", l, ok)
	}
	future[10].Low = 89
	if _, ok = LabelFuture(at, 110, 90, future); ok {
		t.Fatal("same-minute double touch must be ambiguous")
	}
	if _, ok = LabelFuture(at, 110, 90, future[:30]); ok {
		t.Fatal("incomplete horizon must not be labeled neither")
	}
}
