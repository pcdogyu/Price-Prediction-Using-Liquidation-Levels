package alert

import (
	"math"
	"strings"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

// MarketContext is the optional market/position snapshot used by the radar
// filters. It is deliberately separate from LiquidationEvent so the engine can
// remain deterministic in unit tests and can fail closed when live market data is
// unavailable.
type MarketContext struct {
	Ready                 bool
	Price                 float64
	Candles               []domain.Candle
	CurrentCandle         domain.Candle
	LongAvgPrice          float64
	ShortAvgPrice         float64
	LongPositionCount     int
	ShortPositionCount    int
	PositionDataAvailable bool
	LastLongEntryPrice    float64
	LastShortEntryPrice   float64
}

// FilterDecision is persisted with every context-aware alert/signal so an
// operator can tell whether a row opened, only closed, or remained blocked.
type FilterDecision struct {
	Applied                 bool
	OpenAllowed             bool
	CloseAllowed            bool
	Ready                   bool
	Reason                  string
	VWAP                    float64
	Sigma                   float64
	ZScore                  float64
	PeakRateUSDPerSecond    float64
	CurrentRateUSDPerSecond float64
	PeakDecayRatio          float64
	AbsorptionConfirmed     bool
	CumulativeLongUSD       float64
	DeepQuotaUSD            float64
	DrawdownATR             float64
	RequiredGridStepPrice   float64
	RequiredThresholdUSD    float64
	MinProfitSpread         float64
}

// VWAPZScore returns the volume-weighted close mean, volume-weighted standard
// deviation and current price z-score over the latest period candles. It uses
// equal weights only when all supplied volumes are zero, which keeps synthetic
// tests useful without silently discarding real volume data.
func VWAPZScore(candles []domain.Candle, period int, price float64) (vwap, sigma, z float64, ok bool) {
	if period <= 0 || price <= 0 || len(candles) < period {
		return 0, 0, 0, false
	}
	window := candles[len(candles)-period:]
	weight := 0.0
	for _, candle := range window {
		if candle.Close <= 0 {
			continue
		}
		w := candle.VolumeUSD
		if w < 0 || math.IsNaN(w) || math.IsInf(w, 0) {
			w = 0
		}
		weight += w
		vwap += candle.Close * w
	}
	volumeWeighted := weight > 0
	if !volumeWeighted {
		for _, candle := range window {
			if candle.Close > 0 {
				vwap += candle.Close
				weight++
			}
		}
	}
	if weight <= 0 {
		return 0, 0, 0, false
	}
	vwap /= weight
	varianceWeight := 0.0
	for _, candle := range window {
		if candle.Close <= 0 {
			continue
		}
		w := candle.VolumeUSD
		if volumeWeighted {
			if w < 0 || math.IsNaN(w) || math.IsInf(w, 0) {
				w = 0
			}
		}
		if !volumeWeighted {
			w = 1
		}
		varianceWeight += w * (candle.Close - vwap) * (candle.Close - vwap)
	}
	if varianceWeight <= 0 {
		return vwap, 0, 0, false
	}
	sigma = math.Sqrt(varianceWeight / weight)
	if sigma <= 0 || math.IsNaN(sigma) || math.IsInf(sigma, 0) {
		return vwap, sigma, 0, false
	}
	z = (price - vwap) / sigma
	return vwap, sigma, z, !math.IsNaN(z) && !math.IsInf(z, 0)
}

// ATR returns the simple average true range over the latest period candles.
func ATR(candles []domain.Candle, period int) float64 {
	if period <= 0 || len(candles) < 2 {
		return 0
	}
	start := len(candles) - period
	if start < 1 {
		start = 1
	}
	var total float64
	var count int
	for i := start; i < len(candles); i++ {
		c, previous := candles[i], candles[i-1]
		if c.High <= 0 || c.Low <= 0 || previous.Close <= 0 || c.High < c.Low {
			continue
		}
		tr := math.Max(c.High-c.Low, math.Max(math.Abs(c.High-previous.Close), math.Abs(c.Low-previous.Close)))
		if tr > 0 && !math.IsNaN(tr) && !math.IsInf(tr, 0) {
			total += tr
			count++
		}
	}
	if count == 0 {
		return 0
	}
	return total / float64(count)
}

func candleAbsorbed(candle domain.Candle, price, wickRatio float64) bool {
	if price <= 0 {
		price = candle.Close
	}
	if candle.Low <= 0 {
		return false
	}
	if price < candle.Low {
		return false
	}
	rangeSize := candle.High - candle.Low
	if rangeSize <= 0 {
		return true
	}
	close := candle.Close
	if close <= 0 {
		close = price
	}
	return (close-candle.Low)/rangeSize >= wickRatio
}

func scaledThreshold(base float64, layers int, slope float64) float64 {
	if base <= 0 || layers <= 0 {
		return base
	}
	if slope < 0 {
		slope = 0
	}
	return base * math.Pow(1+slope*float64(layers), 2)
}

func filterDecision(cfg Config, side string, ctx MarketContext, state symbolState, baseThreshold float64) FilterDecision {
	d := FilterDecision{Applied: true, MinProfitSpread: cfg.MinProfitSpread}
	price := ctx.Price
	if price <= 0 {
		price = ctx.CurrentCandle.Close
	}
	if !ctx.Ready || price <= 0 {
		d.Reason = "market_context_unavailable"
		return d
	}
	period := cfg.VWAPPeriodMinutes
	var vwapOK bool
	d.VWAP, d.Sigma, d.ZScore, vwapOK = VWAPZScore(ctx.Candles, period, price)
	if !vwapOK {
		d.Reason = "vwap_zscore_unavailable"
		return d
	}

	threshold := cfg.VWAPZScore
	high := d.ZScore > threshold
	low := d.ZScore < -threshold
	// High: short-liquidation opens short; long-liquidation only closes short.
	// Middle: neither side opens; the liquidation direction may close the
	// profitable opposite position. Low is the mirror image for long entries.
	side = strings.ToLower(strings.TrimSpace(side))
	if side == "long" {
		d.OpenAllowed = low
		d.CloseAllowed = !low
		if high {
			d.Reason = "high_zscore_long_liquidation_close_only"
		} else if low {
			d.Reason = "low_zscore_long_liquidation_open_long"
		} else {
			d.Reason = "neutral_zscore_long_liquidation_close_only"
		}
	} else if side == "short" {
		d.OpenAllowed = high
		d.CloseAllowed = !high
		if high {
			d.Reason = "high_zscore_short_liquidation_open_short"
		} else if low {
			d.Reason = "low_zscore_short_liquidation_close_only"
		} else {
			d.Reason = "neutral_zscore_short_liquidation_close_only"
		}
	} else {
		d.Reason = "unsupported_liquidation_side"
		return d
	}

	if ctx.PositionDataAvailable {
		if side == "long" {
			d.CloseAllowed = ctx.ShortPositionCount > 0 && ctx.ShortAvgPrice > 0 && price <= ctx.ShortAvgPrice*(1-cfg.MinProfitSpread)
		} else {
			d.CloseAllowed = ctx.LongPositionCount > 0 && ctx.LongAvgPrice > 0 && price >= ctx.LongAvgPrice*(1+cfg.MinProfitSpread)
		}
	}

	d.DeepQuotaUSD = cfg.DeepQuotaUSD
	if d.DeepQuotaUSD <= 0 {
		d.DeepQuotaUSD = baseThreshold * cfg.DeepQuotaMultiplier
	}
	d.CumulativeLongUSD = state.longLiqCumulative
	atr := ATR(ctx.Candles, cfg.ATRPeriod)
	if state.highWaterPrice > 0 && atr > 0 {
		d.DrawdownATR = (state.highWaterPrice - price) / atr
	}
	if side == "long" && d.OpenAllowed {
		if d.CumulativeLongUSD < d.DeepQuotaUSD || d.DrawdownATR < cfg.DeepDrawdownATR {
			d.OpenAllowed = false
			d.Reason += ";deep_liquidation_quota_or_atr_not_ready"
		}
	}

	layers := state.longEntryCount
	lastEntry := state.lastLongEntryPrice
	if ctx.PositionDataAvailable && ctx.LongPositionCount > layers {
		layers = ctx.LongPositionCount
	}
	if ctx.LastLongEntryPrice > 0 {
		lastEntry = ctx.LastLongEntryPrice
	}
	if side == "long" && d.OpenAllowed && layers > 0 {
		step := cfg.GridStep0Price
		if step <= 0 {
			step = lastEntry * cfg.GridStep0Pct
		}
		step *= math.Pow(cfg.GridStepMultiplier, float64(layers-1))
		d.RequiredGridStepPrice = step
		if lastEntry <= 0 || price > lastEntry-step {
			d.OpenAllowed = false
			d.Reason += ";exponential_long_grid_not_ready"
		}
	}
	d.RequiredThresholdUSD = scaledThreshold(baseThreshold, layers, cfg.GridThresholdSlope)
	d.Ready = d.OpenAllowed || d.CloseAllowed
	return d
}
