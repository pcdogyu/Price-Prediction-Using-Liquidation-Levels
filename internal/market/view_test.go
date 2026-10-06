package market

import (
	"math"
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

func TestCompositeMinutesUsesMedianAndSumsVolume(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	raw := []domain.Candle{{Exchange: "binance", Time: at, Open: 100, High: 103, Low: 99, Close: 101, VolumeUSD: 10}, {Exchange: "bybit", Time: at, Open: 102, High: 104, Low: 100, Close: 102, VolumeUSD: 20}, {Exchange: "okx", Time: at, Open: 10000, High: 11000, Low: 9000, Close: 10000, VolumeUSD: 30}}
	got := CompositeMinutes(raw)
	if len(got) != 1 {
		t.Fatalf("len=%d", len(got))
	}
	c := got[0]
	if c.Open != 102 || c.High != 104 || c.Low != 100 || c.Close != 102 {
		t.Fatalf("median candle=%#v", c)
	}
	if c.VolumeUSD != 60 || c.ExchangeCount != 3 {
		t.Fatalf("coverage=%#v", c)
	}
}

func TestAggregate15mCompletenessAndUTC(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var in []domain.MarketCandle
	for i := 0; i < 15; i++ {
		p := 100 + float64(i)
		in = append(in, domain.MarketCandle{Time: start.Add(time.Duration(i) * time.Minute), Open: p, High: p + 1, Low: p - 1, Close: p + .5, VolumeUSD: 10, ExchangeCount: 3})
	}
	out := Aggregate15m(in, start.Add(16*time.Minute))
	if len(out) != 1 || !out[0].Complete {
		t.Fatalf("aggregate=%#v", out)
	}
	if out[0].Open != 100 || out[0].Close != 114.5 || out[0].VolumeUSD != 150 {
		t.Fatalf("OHLC=%#v", out[0])
	}
}

func TestDetectPatterns(t *testing.T) {
	cs := []domain.MarketCandle{{Open: 102, High: 103, Low: 99, Close: 100}, {Open: 99.5, High: 104, Low: 99, Close: 103}, {Open: 101, High: 101.6, Low: 99, Close: 101.5}, {Open: 100, High: 103, Low: 99.9, Close: 100.5}, {Open: 100, High: 101, Low: 99, Close: 100.05}}
	DetectPatterns(cs)
	assertPattern(t, cs[1], "bullish_engulfing")
	assertPattern(t, cs[2], "hammer")
	assertPattern(t, cs[3], "shooting_star")
	assertPattern(t, cs[4], "doji")
}
func assertPattern(t *testing.T, c domain.MarketCandle, name string) {
	t.Helper()
	for _, p := range c.Patterns {
		if p.Name == name {
			return
		}
	}
	t.Fatalf("%s not found in %#v", name, c.Patterns)
}

func TestEnrichPredictionTriggerStates(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	p := domain.Prediction{Time: at, HorizonMinutes: 60, State: "ok", MarkPrice: 100, ATR: 10, UpperWall: &domain.Wall{Side: "upper", Price: 105}, LowerWall: &domain.Wall{Side: "lower", Price: 95}, Probabilities: map[string]float64{domain.UpperFirst: .6, domain.LowerFirst: .2, domain.Neither: .2}}
	minutes := []domain.MarketCandle{{Time: at.Add(time.Minute), Open: 100, High: 104, Low: 96, Close: 102}, {Time: at.Add(2 * time.Minute), Open: 102, High: 106, Low: 94, Close: 100}}
	got := EnrichPrediction(p, minutes, at.Add(3*time.Minute))
	if got.TriggerOrder != "ambiguous" || got.UpperTrigger.Status != "triggered" || got.LowerTrigger.Status != "triggered" {
		t.Fatalf("prediction=%#v", got)
	}
	if got.LeadingClass != domain.UpperFirst {
		t.Fatalf("leading=%s", got.LeadingClass)
	}
	if math.Abs(got.UpperTrigger.DistancePercent-5) > 1e-9 {
		t.Fatalf("distance=%v", got.UpperTrigger.DistancePercent)
	}
}

func TestEnrichPredictionApproachingAndExpired(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	p := domain.Prediction{Time: at, HorizonMinutes: 60, State: "ok", MarkPrice: 100, ATR: 10, UpperWall: &domain.Wall{Price: 104}, LowerWall: &domain.Wall{Price: 90}}
	near := EnrichPrediction(p, nil, at.Add(30*time.Minute))
	if near.UpperTrigger.Status != "approaching" || near.LowerTrigger.Status != "armed" {
		t.Fatalf("near=%#v", near)
	}
	expired := EnrichPrediction(p, nil, at.Add(61*time.Minute))
	if expired.UpperTrigger.Status != "expired" || expired.TriggerOrder != domain.Neither {
		t.Fatalf("expired=%#v", expired)
	}
}
