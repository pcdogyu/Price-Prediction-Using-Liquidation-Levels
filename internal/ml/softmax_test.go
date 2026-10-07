package ml

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestTrainPredictProbabilities(t *testing.T) {
	var s []Sample
	for i := 0; i < 180; i++ {
		y := i % 3
		x := []float64{float64(y), float64(i % 5)}
		s = append(s, Sample{Time: time.Unix(int64(i), 0), X: x, Y: y})
	}
	a, err := Train(s, []string{"class_hint", "noise"}, .01)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(a.Version, ModelVersionPrefix) {
		t.Fatalf("model version=%q", a.Version)
	}
	p, err := Predict(a, []float64{2, 1})
	if err != nil {
		t.Fatal(err)
	}
	sum := 0.0
	for _, v := range p {
		if v < 0 || v > 1 {
			t.Fatalf("bad probability %v", v)
		}
		sum += v
	}
	if math.Abs(sum-1) > 1e-9 {
		t.Fatalf("probability sum %v", sum)
	}
	if p[Classes[2]] <= p[Classes[0]] {
		t.Fatalf("model failed to learn separable data: %#v", p)
	}
}
