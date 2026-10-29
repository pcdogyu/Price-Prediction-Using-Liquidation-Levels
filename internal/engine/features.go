package engine

import (
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

// FeatureNames remains available for compatibility with stored model metadata.
// New local prediction features are not built while local ATR/wall logic is
// disabled.
var FeatureNames = []string{"upper_distance_atr", "lower_distance_atr", "log_upper_intensity", "log_lower_intensity", "wall_skew", "mass_up_0_5atr", "mass_down_0_5atr", "mass_up_1atr", "mass_down_1atr", "mass_up_2atr", "mass_down_2atr", "oi_change", "funding", "long_short_ratio", "taker_imbalance", "long_liq_15m", "short_liq_15m", "momentum_5m", "momentum_15m", "momentum_60m", "atr_fraction", "realized_volatility", "symbol_eth", "missing_oi", "missing_long_short_ratio", "missing_taker_flow", "missing_liquidations", "price_above_val_atr", "vah_above_price_atr", "value_area_width_atr", "inside_value_area", "missing_volume_profile"}

func BuildFeatures(_ string, _ []domain.Candle, _ MapResult, _, _ float64) (domain.FeatureVector, error) {
	return domain.FeatureVector{}, ErrLocalPredictionDisabled
}

func BuildFeaturesWithProfile(_ string, _ []domain.Candle, _ MapResult, _, _ float64, _ *domain.VolumeProfileSnapshot) (domain.FeatureVector, error) {
	return domain.FeatureVector{}, ErrLocalPredictionDisabled
}

var (
	ErrLocalPredictionDisabled = errorsNew("local ATR/wall prediction disabled")
	ErrWallsMissing            = errorsNew("both liquidation walls are required")
	ErrHistoryMissing          = errorsNew("insufficient price history")
)

type simpleError string

func (e simpleError) Error() string { return string(e) }
func errorsNew(s string) error      { return simpleError(s) }

// LabelFuture is a generic price-label helper retained for compatibility. It
// does not calculate ATR or liquidation walls.
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
