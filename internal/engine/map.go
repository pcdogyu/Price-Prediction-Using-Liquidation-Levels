package engine

import (
	"errors"
	"math"
	"sort"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

type MapConfig struct {
	Leverages, Weights, HalfLivesHours []float64
	MaintenanceMargin, RangeFraction   float64
}

func DefaultMapConfig() MapConfig {
	return MapConfig{Leverages: []float64{5, 10, 20, 50, 100}, Weights: []float64{.08, .22, .32, .25, .13}, HalfLivesHours: []float64{168, 120, 72, 36, 18}, MaintenanceMargin: .005, RangeFraction: .05}
}

type level struct {
	price, longUSD, shortUSD float64
	born                     time.Time
	halfLife                 float64
}

type MapResult struct {
	DataSource string          `json:"data_source"`
	Bins       []domain.MapBin `json:"bins"`
	MarkPrice  float64         `json:"mark_price"`
	ATR        float64         `json:"atr"`
	BinWidth   float64         `json:"bin_width"`
	Upper      *domain.Wall    `json:"upper_wall,omitempty"`
	Lower      *domain.Wall    `json:"lower_wall,omitempty"`
}

func BuildMap(candles []domain.Candle, cfg MapConfig) (MapResult, error) {
	if len(candles) < 20 {
		return MapResult{}, errors.New("at least 20 candles are required")
	}
	cs := append([]domain.Candle(nil), candles...)
	sort.Slice(cs, func(i, j int) bool {
		if cs[i].Time.Equal(cs[j].Time) {
			return cs[i].Exchange < cs[j].Exchange
		}
		return cs[i].Time.Before(cs[j].Time)
	})
	lastPrice := map[string]float64{}
	lastOI := map[string]float64{}
	var levels []level
	for _, c := range cs {
		if c.Close <= 0 {
			continue
		}
		// A price crossing invalidates hypothetical positions at that level.
		kept := levels[:0]
		for _, v := range levels {
			if !(c.Low <= v.price && c.High >= v.price) {
				kept = append(kept, v)
			}
		}
		levels = kept
		delta := c.OpenInterestUSD - lastOI[c.Exchange]
		if lastOI[c.Exchange] > 0 && delta > 0 {
			buyShare := .5
			if c.VolumeUSD > 0 {
				buyShare = .25 + .5*clamp(c.TakerBuyUSD/c.VolumeUSD, 0, 1)
			}
			if c.LongShortRatio > 0 {
				ratioShare := c.LongShortRatio / (1 + c.LongShortRatio)
				buyShare = .65*buyShare + .35*ratioShare
			}
			if p := lastPrice[c.Exchange]; p > 0 {
				if c.Close > p {
					buyShare += .05
				} else if c.Close < p {
					buyShare -= .05
				}
			}
			buyShare = clamp(buyShare-c.FundingRate*20, .1, .9)
			entry := (c.High + c.Low + c.Close) / 3
			for i, lev := range cfg.Leverages {
				w := cfg.Weights[i]
				hl := cfg.HalfLivesHours[i]
				longLiq := entry * (1 - 1/lev + cfg.MaintenanceMargin)
				shortLiq := entry * (1 + 1/lev - cfg.MaintenanceMargin)
				levels = append(levels, level{longLiq, delta * w * buyShare, 0, c.Time, hl}, level{shortLiq, 0, delta * w * (1 - buyShare), c.Time, hl})
			}
		}
		lastOI[c.Exchange] = c.OpenInterestUSD
		lastPrice[c.Exchange] = c.Close
	}
	mark := cs[len(cs)-1].Close
	now := cs[len(cs)-1].Time
	atr := ATR(aggregateCandles(cs), 14)
	if atr <= 0 {
		return MapResult{}, errors.New("ATR unavailable")
	}
	return mapFromLevels(levels, now, mark, atr, cfg), nil
}

func mapFromLevels(levels []level, now time.Time, mark, atr float64, cfg MapConfig) MapResult {
	width := math.Max(mark*.0005, atr*.1)
	lo, hi := mark*(1-cfg.RangeFraction), mark*(1+cfg.RangeFraction)
	n := int(math.Ceil((hi-lo)/width)) + 1
	bins := make([]domain.MapBin, n)
	for i := range bins {
		bins[i].Price = lo + float64(i)*width
	}
	for _, v := range levels {
		if v.price < lo || v.price > hi {
			continue
		}
		decay := math.Pow(.5, now.Sub(v.born).Hours()/v.halfLife)
		i := int(math.Round((v.price - lo) / width))
		if i >= 0 && i < n {
			bins[i].LongUSD += v.longUSD * decay
			bins[i].ShortUSD += v.shortUSD * decay
		}
	}
	// Three-tap Gaussian approximation suppresses single-bin noise.
	longs, shorts := make([]float64, n), make([]float64, n)
	for i := range bins {
		for k, w := range []float64{.274, .452, .274} {
			j := i + k - 1
			if j >= 0 && j < n {
				longs[i] += bins[j].LongUSD * w
				shorts[i] += bins[j].ShortUSD * w
			}
		}
	}
	for i := range bins {
		bins[i].LongUSD = longs[i]
		bins[i].ShortUSD = shorts[i]
		bins[i].TotalUSD = longs[i] + shorts[i]
	}
	upper := selectWall(bins, mark, atr, "upper")
	lower := selectWall(bins, mark, atr, "lower")
	return MapResult{DataSource: domain.DataSourceBinanceUSDM, Bins: bins, MarkPrice: mark, ATR: atr, BinWidth: width, Upper: upper, Lower: lower}
}

func selectWall(b []domain.MapBin, mark, atr float64, side string) *domain.Wall {
	var best *domain.Wall
	for i := 1; i < len(b)-1; i++ {
		d := (b[i].Price - mark) / atr
		intensity := b[i].ShortUSD
		if side == "lower" {
			d = (mark - b[i].Price) / atr
			intensity = b[i].LongUSD
		}
		if d < .25 || d > 2.5 || intensity <= 0 {
			continue
		}
		prev, next := b[i-1].ShortUSD, b[i+1].ShortUSD
		if side == "lower" {
			prev, next = b[i-1].LongUSD, b[i+1].LongUSD
		}
		if intensity < prev || intensity < next {
			continue
		}
		score := intensity / (d + .25)
		if best == nil || score > best.Score {
			best = &domain.Wall{Side: side, Price: b[i].Price, IntensityUSD: intensity, DistanceATR: d, Score: score}
		}
	}
	return best
}

func ATR(cs []domain.Candle, period int) float64 {
	if len(cs) < period+1 {
		return 0
	}
	start := len(cs) - period
	sum := 0.0
	for i := start; i < len(cs); i++ {
		prev := cs[i-1].Close
		tr := math.Max(cs[i].High-cs[i].Low, math.Max(math.Abs(cs[i].High-prev), math.Abs(cs[i].Low-prev)))
		sum += tr
	}
	return sum / float64(period)
}

func aggregateCandles(cs []domain.Candle) []domain.Candle {
	type agg struct {
		c domain.Candle
		n int
	}
	m := map[int64]*agg{}
	var keys []int64
	for _, c := range cs {
		k := c.Time.Unix() / 60
		a := m[k]
		if a == nil {
			a = &agg{c: domain.Candle{Symbol: c.Symbol, Time: c.Time.Truncate(time.Minute), Open: c.Open, High: c.High, Low: c.Low}}
			m[k] = a
			keys = append(keys, k)
		}
		if a.n > 0 {
			a.c.High = math.Max(a.c.High, c.High)
			a.c.Low = math.Min(a.c.Low, c.Low)
		}
		a.c.Close += c.Close
		a.c.VolumeUSD += c.VolumeUSD
		a.c.TakerBuyUSD += c.TakerBuyUSD
		a.c.OpenInterestUSD += c.OpenInterestUSD
		a.c.FundingRate += c.FundingRate
		a.c.LongShortRatio += c.LongShortRatio
		a.n++
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	out := make([]domain.Candle, 0, len(keys))
	for _, k := range keys {
		a := m[k]
		n := float64(a.n)
		a.c.Close /= n
		a.c.FundingRate /= n
		a.c.LongShortRatio /= n
		out = append(out, a.c)
	}
	return out
}
func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
