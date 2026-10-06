package engine

import (
	"sort"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/ml"
)

type pendingSample struct {
	sample       ml.Sample
	upper, lower float64
}

// HistoricalSamples replays the map without looking ahead. Missing historical
// liquidation streams remain zero rather than being synthesized.
func HistoricalSamples(symbol string, candles []domain.Candle, cfg MapConfig) []ml.Sample {
	cs := append([]domain.Candle(nil), candles...)
	sort.Slice(cs, func(i, j int) bool {
		if cs[i].Time.Equal(cs[j].Time) {
			return cs[i].Exchange < cs[j].Exchange
		}
		return cs[i].Time.Before(cs[j].Time)
	})
	lastPrice, lastOI := map[string]float64{}, map[string]float64{}
	var levels []level
	var history []domain.Candle
	var pending []pendingSample
	for i := 0; i < len(cs); {
		minute := cs[i].Time.Truncate(time.Minute)
		j := i
		for j < len(cs) && cs[j].Time.Truncate(time.Minute).Equal(minute) {
			j++
		}
		group := cs[i:j]
		agg := aggregateCandles(group)[0]
		kept := levels[:0]
		for _, v := range levels {
			if !(agg.Low <= v.price && agg.High >= v.price) {
				kept = append(kept, v)
			}
		}
		levels = kept
		for _, c := range group {
			delta := c.OpenInterestUSD - lastOI[c.Exchange]
			if lastOI[c.Exchange] > 0 && delta > 0 {
				buyShare := .5
				if c.VolumeUSD > 0 {
					buyShare = .25 + .5*clamp(c.TakerBuyUSD/c.VolumeUSD, 0, 1)
				}
				if c.LongShortRatio > 0 {
					buyShare = .65*buyShare + .35*c.LongShortRatio/(1+c.LongShortRatio)
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
				for k, lev := range cfg.Leverages {
					levels = append(levels, level{entry * (1 - 1/lev + cfg.MaintenanceMargin), delta * cfg.Weights[k] * buyShare, 0, c.Time, cfg.HalfLivesHours[k]}, level{entry * (1 + 1/lev - cfg.MaintenanceMargin), 0, delta * cfg.Weights[k] * (1 - buyShare), c.Time, cfg.HalfLivesHours[k]})
				}
			}
			lastOI[c.Exchange] = c.OpenInterestUSD
			lastPrice[c.Exchange] = c.Close
		}
		history = append(history, agg)
		if minute.Minute()%5 == 0 && len(history) >= 61 {
			recent := history
			if len(recent) > 61 {
				recent = recent[len(recent)-61:]
			}
			atr := ATR(recent, 14)
			if atr > 0 {
				m := mapFromLevels(levels, minute, agg.Close, atr, cfg)
				if m.Upper != nil && m.Lower != nil {
					fv, e := BuildFeatures(symbol, recent, m, 0, 0)
					if e == nil {
						pending = append(pending, pendingSample{ml.Sample{Time: minute, Symbol: symbol, X: fv.Values}, m.Upper.Price, m.Lower.Price})
					}
				}
			}
		}
		i = j
	}
	var out []ml.Sample
	for _, p := range pending {
		label, ok := LabelFuture(p.sample.Time, p.upper, p.lower, history)
		if !ok {
			continue
		}
		switch label.Class {
		case domain.UpperFirst:
			p.sample.Y = 0
		case domain.LowerFirst:
			p.sample.Y = 1
		default:
			p.sample.Y = 2
		}
		out = append(out, p.sample)
	}
	return out
}
