package ml

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

func WalkForward(symbol string, samples []Sample, names []string) (domain.BacktestReport, domain.ModelArtifact, error) {
	if len(samples) < 300 {
		return domain.BacktestReport{}, domain.ModelArtifact{}, errors.New("not enough samples for walk-forward backtest")
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i].Time.Before(samples[j].Time) })
	start := samples[0].Time.Truncate(24 * time.Hour)
	var allP []map[string]float64
	var allY []int
	var priorP, wallP, marketP, noProfileP []map[string]float64
	var bestModel domain.ModelArtifact
	selectedLambda := .01
	for fold := 0; fold < 4; fold++ {
		trainEnd := start.Add(time.Duration(14+3*fold) * 24 * time.Hour)
		valEnd := trainEnd.Add(3 * 24 * time.Hour)
		testEnd := valEnd.Add(3 * 24 * time.Hour)
		train := between(samples, time.Time{}, trainEnd.Add(-time.Hour))
		val := between(samples, trainEnd.Add(time.Hour), valEnd.Add(-time.Hour))
		test := between(samples, valEnd.Add(time.Hour), testEnd)
		if len(train) < 30 || len(val) < 10 || len(test) < 10 {
			continue
		}
		bestLoss := math.Inf(1)
		var foldModel domain.ModelArtifact
		for _, l := range []float64{1e-4, 1e-3, 1e-2, 1e-1, 1} {
			a, e := Train(train, names, l)
			if e != nil {
				continue
			}
			CalibrateTemperature(&a, val)
			score := logLoss(a, val)
			if score < bestLoss {
				bestLoss = score
				foldModel = a
				selectedLambda = l
			}
		}
		if len(foldModel.Weights) == 0 {
			continue
		}
		bestModel = foldModel
		for _, s := range test {
			p, _ := Predict(foldModel, s.X)
			allP = append(allP, p)
			allY = append(allY, s.Y)
		}
		prior := classPrior(train)
		for range test {
			priorP = append(priorP, prior)
		}
		wallP = append(wallP, baselinePredictions(train, val, test, []int{0, 1})...)
		marketP = append(marketP, baselinePredictions(train, val, test, []int{11, 12, 13, 14, 17, 18, 19, 20, 21, 22, 23, 24, 25})...)
		indices := make([]int, 27)
		for i := range indices {
			indices[i] = i
		}
		noProfileP = append(noProfileP, baselinePredictions(train, val, test, indices)...)
	}
	if len(allY) == 0 {
		return domain.BacktestReport{}, domain.ModelArtifact{}, errors.New("no complete folds in 30-day window")
	}
	ll, br, ece := metrics(allP, allY)
	priorLoss, _, _ := metrics(priorP, allY)
	wallLoss, _, _ := metrics(wallP, allY)
	marketLoss, _, _ := metrics(marketP, allY)
	noProfileLoss, _, _ := metrics(noProfileP, allY)
	base := math.Min(priorLoss, math.Min(wallLoss, math.Min(marketLoss, noProfileLoss)))
	improvement := (base - ll) / base
	report := domain.BacktestReport{Symbol: symbol, DataSource: domain.DataSourceBinanceUSDM, GeneratedAt: time.Now().UTC(), Samples: len(allY), LogLoss: ll, BrierScore: br, ECE: ece, BaselineLogLoss: base, Baselines: map[string]float64{"class_prior": priorLoss, "wall_distance": wallLoss, "market_without_map": marketLoss, "binance_without_volume_profile": noProfileLoss}, Improvement: improvement, PromotionEligible: improvement >= .03 && ece <= .08, Note: "four expanding 14d/3d/3d folds with a 60m embargo"}
	// Fit the production artifact on all but the final three days and reserve
	// those days for temperature calibration.
	cut := samples[len(samples)-1].Time.Add(-3 * 24 * time.Hour)
	fit, calibration := between(samples, time.Time{}, cut), between(samples, cut.Add(time.Hour), samples[len(samples)-1].Time.Add(time.Minute))
	if final, err := Train(fit, names, selectedLambda); err == nil {
		if len(calibration) > 0 {
			CalibrateTemperature(&final, calibration)
		}
		bestModel = final
	}
	bestModel.Experimental = !report.PromotionEligible
	return report, bestModel, nil
}

func baselinePredictions(train, val, test []Sample, indices []int) []map[string]float64 {
	tr, va, te := subset(train, indices), subset(val, indices), subset(test, indices)
	names := make([]string, len(indices))
	for i := range names {
		names[i] = fmt.Sprintf("x%d", indices[i])
	}
	a, err := Train(tr, names, .01)
	if err != nil {
		prior := classPrior(train)
		out := make([]map[string]float64, len(test))
		for i := range out {
			out[i] = prior
		}
		return out
	}
	CalibrateTemperature(&a, va)
	out := make([]map[string]float64, 0, len(te))
	for _, s := range te {
		p, _ := Predict(a, s.X)
		out = append(out, p)
	}
	return out
}

func subset(in []Sample, indices []int) []Sample {
	out := make([]Sample, len(in))
	for i, s := range in {
		out[i] = s
		out[i].X = make([]float64, len(indices))
		for j, k := range indices {
			if k < len(s.X) {
				out[i].X[j] = s.X[k]
			}
		}
	}
	return out
}

func between(s []Sample, a, b time.Time) []Sample {
	var out []Sample
	for _, x := range s {
		if (a.IsZero() || !x.Time.Before(a)) && x.Time.Before(b) {
			out = append(out, x)
		}
	}
	return out
}
func logLoss(a domain.ModelArtifact, s []Sample) float64 {
	v := 0.0
	for _, x := range s {
		p, _ := Predict(a, x.X)
		v -= math.Log(math.Max(p[Classes[x.Y]], 1e-15))
	}
	return v / float64(len(s))
}
func classPrior(s []Sample) map[string]float64 {
	n := []float64{1, 1, 1}
	for _, x := range s {
		n[x.Y]++
	}
	sum := n[0] + n[1] + n[2]
	return map[string]float64{Classes[0]: n[0] / sum, Classes[1]: n[1] / sum, Classes[2]: n[2] / sum}
}
func metrics(ps []map[string]float64, ys []int) (ll, brier, ece float64) {
	binsN := 10
	counts, conf, acc := make([]int, binsN), make([]float64, binsN), make([]float64, binsN)
	for i, p := range ps {
		ll -= math.Log(math.Max(p[Classes[ys[i]]], 1e-15))
		for k, c := range Classes {
			y := 0.0
			if k == ys[i] {
				y = 1
			}
			d := p[c] - y
			brier += d * d
		}
		pred, mc := 0, p[Classes[0]]
		for k := 1; k < 3; k++ {
			if p[Classes[k]] > mc {
				pred, mc = k, p[Classes[k]]
			}
		}
		b := int(mc * float64(binsN))
		if b == binsN {
			b--
		}
		counts[b]++
		conf[b] += mc
		if pred == ys[i] {
			acc[b]++
		}
	}
	n := float64(len(ys))
	ll /= n
	brier /= n
	for b, c := range counts {
		if c > 0 {
			ece += float64(c) / n * math.Abs(conf[b]/float64(c)-acc[b]/float64(c))
		}
	}
	return
}
