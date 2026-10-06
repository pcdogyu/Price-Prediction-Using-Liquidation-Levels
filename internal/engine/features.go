package engine

import (
	"math"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

var FeatureNames = []string{"upper_distance_atr", "lower_distance_atr", "log_upper_intensity", "log_lower_intensity", "wall_skew", "mass_up_0_5atr", "mass_down_0_5atr", "mass_up_1atr", "mass_down_1atr", "mass_up_2atr", "mass_down_2atr", "oi_change", "funding", "long_short_ratio", "taker_imbalance", "long_liq_15m", "short_liq_15m", "momentum_5m", "momentum_15m", "momentum_60m", "atr_fraction", "realized_volatility", "symbol_eth", "missing_oi", "missing_long_short_ratio", "missing_taker_flow", "missing_liquidations"}

func BuildFeatures(symbol string, cs []domain.Candle, m MapResult, longLiq, shortLiq float64) (domain.FeatureVector, error) {
	vals := make([]float64, len(FeatureNames))
	missing := map[string]bool{}
	if m.Upper == nil || m.Lower == nil {
		return domain.FeatureVector{}, ErrWallsMissing
	}
	vals[0], vals[1] = m.Upper.DistanceATR, m.Lower.DistanceATR
	vals[2], vals[3] = math.Log1p(m.Upper.IntensityUSD), math.Log1p(m.Lower.IntensityUSD)
	vals[4] = (m.Upper.IntensityUSD - m.Lower.IntensityUSD) / (m.Upper.IntensityUSD + m.Lower.IntensityUSD + 1)
	for _, x := range []struct {
		r        float64
		up, down int
	}{{.5, 5, 6}, {1, 7, 8}, {2, 9, 10}} {
		for _, b := range m.Bins {
			d := (b.Price - m.MarkPrice) / m.ATR
			if d >= 0 && d <= x.r {
				vals[x.up] += b.ShortUSD
			}
			if d < 0 && -d <= x.r {
				vals[x.down] += b.LongUSD
			}
		}
		vals[x.up] = math.Log1p(vals[x.up])
		vals[x.down] = math.Log1p(vals[x.down])
	}
	latest := latestAggregate(cs)
	if len(latest) < 2 {
		return domain.FeatureVector{}, ErrHistoryMissing
	}
	last := latest[len(latest)-1]
	prev := latest[len(latest)-2]
	if prev.OpenInterestUSD > 0 {
		vals[11] = (last.OpenInterestUSD - prev.OpenInterestUSD) / prev.OpenInterestUSD
	} else {
		missing[FeatureNames[11]] = true
		vals[23] = 1
	}
	vals[12] = last.FundingRate
	vals[13] = last.LongShortRatio
	if last.LongShortRatio <= 0 {
		vals[24] = 1
		missing[FeatureNames[13]] = true
	}
	if last.VolumeUSD > 0 {
		vals[14] = 2*last.TakerBuyUSD/last.VolumeUSD - 1
	} else {
		missing[FeatureNames[14]] = true
		vals[25] = 1
	}
	vals[15], vals[16] = math.Log1p(longLiq), math.Log1p(shortLiq)
	if longLiq+shortLiq == 0 {
		vals[26] = 1
		missing["liquidations"] = true
	}
	vals[17] = momentum(latest, 5)
	vals[18] = momentum(latest, 15)
	vals[19] = momentum(latest, 60)
	vals[20] = m.ATR / m.MarkPrice
	vals[21] = realizedVol(latest, 60)
	if symbol == "ETHUSDT" {
		vals[22] = 1
	}
	return domain.FeatureVector{Time: last.Time, Symbol: symbol, Names: append([]string(nil), FeatureNames...), Values: vals, Missing: missing}, nil
}

var ErrWallsMissing = errorsNew("both liquidation walls are required")
var ErrHistoryMissing = errorsNew("insufficient price history")

type simpleError string

func (e simpleError) Error() string { return string(e) }
func errorsNew(s string) error      { return simpleError(s) }

func latestAggregate(cs []domain.Candle) []domain.Candle { return aggregateCandles(cs) }
func momentum(cs []domain.Candle, n int) float64 {
	if len(cs) <= n || cs[len(cs)-1-n].Close <= 0 {
		return 0
	}
	return cs[len(cs)-1].Close/cs[len(cs)-1-n].Close - 1
}
func realizedVol(cs []domain.Candle, n int) float64 {
	if len(cs) < 2 {
		return 0
	}
	start := len(cs) - n
	if start < 1 {
		start = 1
	}
	var xs []float64
	for i := start; i < len(cs); i++ {
		if cs[i-1].Close > 0 && cs[i].Close > 0 {
			xs = append(xs, math.Log(cs[i].Close/cs[i-1].Close))
		}
	}
	if len(xs) < 2 {
		return 0
	}
	var mean float64
	for _, x := range xs {
		mean += x
	}
	mean /= float64(len(xs))
	var v float64
	for _, x := range xs {
		v += (x - mean) * (x - mean)
	}
	return math.Sqrt(v / float64(len(xs)-1))
}

func LabelFuture(at time.Time, upper, lower float64, future []domain.Candle) (domain.Label, bool) {
	cs := aggregateCandles(future)
	if len(cs) == 0 || cs[len(cs)-1].Time.Before(at.Add(60*time.Minute)) {
		return domain.Label{}, false
	}
	for _, c := range cs {
		if !c.Time.After(at) {
			continue
		}
		if c.Time.After(at.Add(60 * time.Minute)) {
			break
		}
		u, l := c.High >= upper, c.Low <= lower
		if u && l {
			return domain.Label{}, false
		}
		if u {
			return domain.Label{Class: domain.UpperFirst, Time: at}, true
		}
		if l {
			return domain.Label{Class: domain.LowerFirst, Time: at}, true
		}
	}
	return domain.Label{Class: domain.Neither, Time: at}, true
}
