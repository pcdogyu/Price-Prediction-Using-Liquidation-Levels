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
	// Keep the four leverage bands used by the target liquidation-map view.
	// These values remain for configuration and API compatibility; local map
	// construction is disabled and CoinGlass is the sole wall source.
	return MapConfig{Leverages: []float64{10, 25, 50, 100}, Weights: []float64{.30, .32, .25, .13}, HalfLivesHours: []float64{120, 72, 36, 18}, MaintenanceMargin: .005, RangeFraction: .05}
}

type MapResult struct {
	DataSource           string                   `json:"data_source"`
	Leverages            []float64                `json:"leverages"`
	Bins                 []domain.MapBin          `json:"bins"`
	MarkPrice            float64                  `json:"mark_price"`
	ATR                  float64                  `json:"atr"`
	BinWidth             float64                  `json:"bin_width"`
	Upper                *domain.Wall             `json:"upper_wall,omitempty"`
	Lower                *domain.Wall             `json:"lower_wall,omitempty"`
	CapturedAt           *time.Time               `json:"captured_at,omitempty"`
	TopLong              []domain.LiquidationPeak `json:"top_long_liquidations,omitempty"`
	TopShort             []domain.LiquidationPeak `json:"top_short_liquidations,omitempty"`
	LiquidationAboveUSD  float64                  `json:"liquidation_above_usd"`
	LiquidationBelowUSD  float64                  `json:"liquidation_below_usd"`
	LiquidationDirection string                   `json:"liquidation_direction,omitempty"`
}

// ErrLocalMapDisabled makes the old Binance-candle liquidation-map path
// explicit. Runtime callers must use the CoinGlass map instead.
var ErrLocalMapDisabled = errors.New("local liquidation map disabled; use CoinGlass")

// BuildMap is retained as a compatibility boundary for callers that still
// compile against the old engine API. It must never calculate a local map.
func BuildMap(_ []domain.Candle, _ MapConfig) (MapResult, error) {
	return MapResult{}, ErrLocalMapDisabled
}

// aggregateCandles remains a generic candle normalization helper used by the
// non-prediction candle/label code. It does not calculate ATR or liquidation
// walls.
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
