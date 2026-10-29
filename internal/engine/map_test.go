package engine

import (
	"reflect"
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

func TestDefaultMapConfigUsesFourRequestedLeverages(t *testing.T) {
	cfg := DefaultMapConfig()
	if !reflect.DeepEqual(cfg.Leverages, []float64{10, 25, 50, 100}) {
		t.Fatalf("leverages=%v", cfg.Leverages)
	}
	if !reflect.DeepEqual(cfg.Weights, []float64{.30, .32, .25, .13}) || !reflect.DeepEqual(cfg.HalfLivesHours, []float64{120, 72, 36, 18}) {
		t.Fatalf("weights=%v half_lives=%v", cfg.Weights, cfg.HalfLivesHours)
	}
}

func TestBuildMapIsDisabled(t *testing.T) {
	if _, err := BuildMap(nil, DefaultMapConfig()); err != ErrLocalMapDisabled {
		t.Fatalf("expected local map to be disabled, got %v", err)
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
