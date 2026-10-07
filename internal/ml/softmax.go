package ml

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
	"gonum.org/v1/gonum/optimize"
)

var Classes = []string{domain.UpperFirst, domain.LowerFirst, domain.Neither}

type Sample struct {
	Time   time.Time
	Symbol string
	X      []float64
	Y      int
}

func Train(samples []Sample, names []string, lambda float64) (domain.ModelArtifact, error) {
	if len(samples) < 30 {
		return domain.ModelArtifact{}, errors.New("at least 30 samples required")
	}
	d := len(names)
	if d == 0 {
		return domain.ModelArtifact{}, errors.New("features required")
	}
	means, std := stats(samples, d)
	x := standardize(samples, means, std)
	counts := make([]int, 3)
	for _, s := range samples {
		if s.Y < 0 || s.Y >= 3 {
			return domain.ModelArtifact{}, errors.New("invalid class")
		}
		counts[s.Y]++
	}
	cw := make([]float64, 3)
	for i, n := range counts {
		if n == 0 {
			return domain.ModelArtifact{}, errors.New("each class needs samples")
		}
		cw[i] = float64(len(samples)) / (3 * float64(n))
		if cw[i] > 4 {
			cw[i] = 4
		}
	}
	npar := 3 * (d + 1)
	problem := optimize.Problem{}
	problem.Func = func(p []float64) float64 { loss, _ := lossGrad(p, x, cw, d, lambda, false); return loss }
	problem.Grad = func(g, p []float64) { _, gg := lossGrad(p, x, cw, d, lambda, true); copy(g, gg) }
	res, err := optimize.Minimize(problem, make([]float64, npar), &optimize.Settings{GradientThreshold: 1e-7, MajorIterations: 500}, &optimize.LBFGS{})
	if err != nil {
		return domain.ModelArtifact{}, err
	}
	a := domain.ModelArtifact{Version: "softmax-v2-binance-volume-profile-" + time.Now().UTC().Format("20060102T150405Z"), DataSource: domain.DataSourceBinanceUSDM, FeatureNames: append([]string(nil), names...), Means: means, StdDevs: std, Lambda: lambda, Temperature: 1, TrainedAt: time.Now().UTC(), Experimental: true, Weights: make([][]float64, 3), Biases: make([]float64, 3)}
	for k := 0; k < 3; k++ {
		a.Weights[k] = append([]float64(nil), res.X[k*(d+1):k*(d+1)+d]...)
		a.Biases[k] = res.X[k*(d+1)+d]
	}
	return a, nil
}

func lossGrad(p []float64, s []Sample, cw []float64, d int, lambda float64, wantGrad bool) (float64, []float64) {
	g := make([]float64, len(p))
	loss := 0.0
	for _, row := range s {
		logits := make([]float64, 3)
		for k := 0; k < 3; k++ {
			o := k * (d + 1)
			logits[k] = p[o+d]
			for j := 0; j < d; j++ {
				logits[k] += p[o+j] * row.X[j]
			}
		}
		probs := softmax(logits, 1)
		w := cw[row.Y]
		loss -= w * math.Log(math.Max(probs[row.Y], 1e-15))
		if wantGrad {
			for k := 0; k < 3; k++ {
				delta := w * (probs[k] - boolFloat(k == row.Y))
				o := k * (d + 1)
				for j := 0; j < d; j++ {
					g[o+j] += delta * row.X[j]
				}
				g[o+d] += delta
			}
		}
	}
	n := float64(len(s))
	loss /= n
	for i := range p {
		if (i+1)%(d+1) != 0 {
			loss += .5 * lambda * p[i] * p[i]
			if wantGrad {
				g[i] = g[i]/n + lambda*p[i]
			}
		} else if wantGrad {
			g[i] /= n
		}
	}
	return loss, g
}
func boolFloat(v bool) float64 {
	if v {
		return 1
	}
	return 0
}

func Predict(a domain.ModelArtifact, x []float64) (map[string]float64, error) {
	if len(x) != len(a.FeatureNames) || len(a.Weights) != 3 {
		return nil, errors.New("model shape mismatch")
	}
	z := make([]float64, len(x))
	for i := range x {
		s := a.StdDevs[i]
		if s == 0 {
			s = 1
		}
		z[i] = (x[i] - a.Means[i]) / s
	}
	logits := make([]float64, 3)
	for k := 0; k < 3; k++ {
		logits[k] = a.Biases[k]
		for j := range z {
			logits[k] += a.Weights[k][j] * z[j]
		}
	}
	p := softmax(logits, a.Temperature)
	return map[string]float64{Classes[0]: p[0], Classes[1]: p[1], Classes[2]: p[2]}, nil
}

func Contributions(a domain.ModelArtifact, x []float64) map[string]float64 {
	out := map[string]float64{}
	if len(x) != len(a.FeatureNames) {
		return out
	}
	for j, n := range a.FeatureNames {
		s := a.StdDevs[j]
		if s == 0 {
			s = 1
		}
		z := (x[j] - a.Means[j]) / s
		out[n] = z * (a.Weights[0][j] - a.Weights[1][j])
	}
	return out
}

func CalibrateTemperature(a *domain.ModelArtifact, val []Sample) {
	bestT, best := 1.0, math.Inf(1)
	for t := .5; t <= 3; t += .05 {
		loss := 0.0
		for _, s := range val {
			b := *a
			b.Temperature = t
			p, _ := Predict(b, s.X)
			loss -= math.Log(math.Max(p[Classes[s.Y]], 1e-15))
		}
		if loss < best {
			best, bestT = loss, t
		}
	}
	a.Temperature = bestT
}

func Save(path string, a domain.ModelArtifact) error {
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return err
	}
	b, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err = os.WriteFile(tmp, b, 0640); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
func Load(path string) (domain.ModelArtifact, error) {
	b, e := os.ReadFile(path)
	if e != nil {
		return domain.ModelArtifact{}, e
	}
	var a domain.ModelArtifact
	e = json.Unmarshal(b, &a)
	return a, e
}

func stats(s []Sample, d int) ([]float64, []float64) {
	m, v := make([]float64, d), make([]float64, d)
	for _, r := range s {
		for j := 0; j < d; j++ {
			m[j] += r.X[j]
		}
	}
	for j := range m {
		m[j] /= float64(len(s))
	}
	for _, r := range s {
		for j := 0; j < d; j++ {
			z := r.X[j] - m[j]
			v[j] += z * z
		}
	}
	for j := range v {
		v[j] = math.Sqrt(v[j] / float64(len(s)))
		if v[j] < 1e-12 {
			v[j] = 1
		}
	}
	return m, v
}
func standardize(s []Sample, m, sd []float64) []Sample {
	out := make([]Sample, len(s))
	for i, r := range s {
		out[i] = r
		out[i].X = make([]float64, len(r.X))
		for j := range r.X {
			out[i].X[j] = (r.X[j] - m[j]) / sd[j]
		}
	}
	return out
}
func softmax(x []float64, t float64) []float64 {
	if t <= 0 {
		t = 1
	}
	mx := x[0] / t
	for _, v := range x[1:] {
		if v/t > mx {
			mx = v / t
		}
	}
	out := make([]float64, len(x))
	sum := 0.0
	for i, v := range x {
		out[i] = math.Exp(v/t - mx)
		sum += out[i]
	}
	for i := range out {
		out[i] /= sum
	}
	return out
}
