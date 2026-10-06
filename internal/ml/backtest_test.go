package ml

import (
	"testing"
	"time"
)

func TestWalkForwardIncludesAllBaselines(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	names := make([]string, 27)
	for i := range names {
		names[i] = "feature"
	}
	var samples []Sample
	for i := 0; i < 30*24; i++ {
		y := i % 3
		x := make([]float64, len(names))
		x[0], x[1], x[11], x[17] = float64(y), float64(2-y), float64(i%7), float64(y)
		samples = append(samples, Sample{Time: start.Add(time.Duration(i) * time.Hour), Symbol: "BTCUSDT", X: x, Y: y})
	}
	r, a, err := WalkForward("BTCUSDT", samples, names)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"class_prior", "wall_distance", "market_without_map"} {
		if _, ok := r.Baselines[name]; !ok {
			t.Fatalf("missing baseline %s: %#v", name, r.Baselines)
		}
	}
	if len(a.Weights) != 3 || r.Samples == 0 {
		t.Fatalf("bad artifact/report: %#v %#v", a, r)
	}
	if _, err = Predict(a, samples[len(samples)-1].X); err != nil {
		t.Fatal(err)
	}
}
