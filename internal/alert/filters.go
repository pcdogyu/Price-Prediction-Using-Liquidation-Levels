package alert

import (
	"math"
	"strings"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

// MarketContext is the optional market/position snapshot used by the radar
// filters. It is deliberately separate from LiquidationEvent so the engine can
// remain deterministic in unit tests and can fail closed when live market data is
// unavailable.
type MarketContext struct {
	Ready   bool
	Price   float64
	Candles []domain.Candle
	// VWAP fields are optional replay-time cache values. A live caller can
	// leave VWAPReady false and filterDecision will calculate them from Candles.
	VWAP                  float64
	Sigma                 float64
	ZScore                float64
	VWAPReady             bool
	CurrentCandle         domain.Candle
	LongAvgPrice          float64
	ShortAvgPrice         float64
	LongPositionCount     int
	ShortPositionCount    int
	PositionDataAvailable bool
	LastLongEntryPrice    float64
	LastShortEntryPrice   float64
	DonchianHigh          float64
	DonchianLow           float64
	DonchianReady         bool
	DonchianComputed      bool
}

// FilterDecision is persisted with every context-aware alert/signal so an
// operator can tell whether a row opened, only closed, or remained blocked.
type FilterDecision struct {
	Applied                 bool
	OpenAllowed             bool
	CloseAllowed            bool
	Ready                   bool
	Reason                  string
	FilterTags              []string
	VWAP                    float64
	Sigma                   float64
	ZScore                  float64
	PeakRateUSDPerSecond    float64
	CurrentRateUSDPerSecond float64
	PeakDecayRatio          float64
	AbsorptionConfirmed     bool
	CumulativeLongUSD       float64
	DeepQuotaUSD            float64
	RequiredGridStepPrice   float64
	RequiredThresholdUSD    float64
	MinProfitSpread         float64
	DonchianHigh            float64
	DonchianLow             float64
	GreenZoneUpper          float64
	RedZoneLower            float64
	BoundaryOverlap         bool
	DonchianReady           bool
}

// VWAPZScore returns the volume-weighted close mean, volume-weighted standard
// deviation and current price z-score over the latest period candles. It uses
// equal weights only when all supplied volumes are zero, which keeps synthetic
// tests useful without silently discarding real volume data.
func VWAPZScore(candles []domain.Candle, period int, price float64) (vwap, sigma, z float64, ok bool) {
	return vwapZScore(candles, period, price, 0, false)
}

// VWAPZScoreWithLastClose applies a replay event price to the current candle
// without copying the full candle window for every event.
func VWAPZScoreWithLastClose(candles []domain.Candle, period int, price, lastClose float64) (vwap, sigma, z float64, ok bool) {
	return vwapZScore(candles, period, price, lastClose, true)
}

func vwapZScore(candles []domain.Candle, period int, price, lastClose float64, overrideLastClose bool) (vwap, sigma, z float64, ok bool) {
	if period <= 0 || price <= 0 || len(candles) < period {
		return 0, 0, 0, false
	}
	window := candles[len(candles)-period:]
	weight := 0.0
	for index, candle := range window {
		close := candle.Close
		if overrideLastClose && index == len(window)-1 && lastClose > 0 {
			close = lastClose
		}
		if close <= 0 {
			continue
		}
		w := candle.VolumeUSD
		if w < 0 || math.IsNaN(w) || math.IsInf(w, 0) {
			w = 0
		}
		weight += w
		vwap += close * w
	}
	volumeWeighted := weight > 0
	if !volumeWeighted {
		for index, candle := range window {
			close := candle.Close
			if overrideLastClose && index == len(window)-1 && lastClose > 0 {
				close = lastClose
			}
			if close > 0 {
				vwap += close
				weight++
			}
		}
	}
	if weight <= 0 {
		return 0, 0, 0, false
	}
	vwap /= weight
	varianceWeight := 0.0
	for index, candle := range window {
		close := candle.Close
		if overrideLastClose && index == len(window)-1 && lastClose > 0 {
			close = lastClose
		}
		if close <= 0 {
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
		varianceWeight += w * (close - vwap) * (close - vwap)
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

// DonchianRange returns the high/low of the complete trailing 1m range. The
// caller must provide enough candles to cover every minute in the configured
// window; incomplete history is deliberately reported as unavailable so an
// opening decision cannot silently fall back to the old VWAP-only boundary.
func DonchianRange(candles []domain.Candle, rangeMinutes int) (high, low float64, ok bool) {
	if rangeMinutes <= 0 || len(candles) == 0 {
		return 0, 0, false
	}
	end := candles[len(candles)-1].Time
	if end.IsZero() {
		return 0, 0, false
	}
	start := end.Add(-time.Duration(rangeMinutes) * time.Minute)
	count := 0
	for _, candle := range candles {
		if candle.Time.Before(start) || candle.Time.After(end) || candle.High <= 0 || candle.Low <= 0 || candle.High < candle.Low {
			continue
		}
		if count == 0 || candle.High > high {
			high = candle.High
		}
		if count == 0 || candle.Low < low {
			low = candle.Low
		}
		count++
	}
	if count < rangeMinutes || high <= 0 || low <= 0 || high < low {
		return 0, 0, false
	}
	return high, low, true
}

// VWAPZoneBounds combines the short VWAP band with the long Donchian range.
// A true overlap means the requested opening zones have no safe separation;
// callers must then keep the whole range close-only.
func vwapFilterBounds(cfg Config, vwap, sigma float64) (greenUpper, redLower float64, ok bool) {
	if vwap <= 0 || sigma <= 0 || cfg.VWAPZScore <= 0 {
		return 0, 0, false
	}
	return vwap - cfg.VWAPZScore*sigma, vwap + cfg.VWAPZScore*sigma, true
}

func donchianFilterBounds(cfg Config, high, low float64) (greenUpper, redLower float64, ok bool) {
	if high <= 0 || low <= 0 || high < low || cfg.VWAPMinDrawdown <= 0 || cfg.VWAPMinDrawdown >= 1 || cfg.VWAPDonchianFraction <= 0 || cfg.VWAPDonchianFraction >= .5 {
		return 0, 0, false
	}
	greenUpper = math.Min(high*(1-cfg.VWAPMinDrawdown), low+cfg.VWAPDonchianFraction*(high-low))
	redLower = math.Max(low*(1+cfg.VWAPMinDrawdown), high-cfg.VWAPDonchianFraction*(high-low))
	return greenUpper, redLower, true
}

func VWAPZoneBounds(cfg Config, vwap, sigma, high, low float64) (greenUpper, redLower float64, overlap, ok bool) {
	vwapGreen, vwapRed, vwapOK := vwapFilterBounds(cfg, vwap, sigma)
	donGreen, donRed, donOK := donchianFilterBounds(cfg, high, low)
	if !vwapOK || !donOK {
		return 0, 0, false, false
	}
	greenUpper = math.Min(vwapGreen, donGreen)
	redLower = math.Max(vwapRed, donRed)
	return greenUpper, redLower, greenUpper >= redLower, true
}

// candleAbsorbed confirms that the current candle is absorbing the side of the
// cascade that was liquidated. Long liquidations use a lower wick; short
// liquidations use the mirrored upper wick.
func candleAbsorbed(side string, candle domain.Candle, price, wickRatio float64) bool {
	if price <= 0 {
		price = candle.Close
	}
	if candle.Low <= 0 || candle.High < candle.Low {
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
	if strings.EqualFold(strings.TrimSpace(side), "short") {
		return (candle.High-close)/rangeSize >= wickRatio
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
	d := FilterDecision{Applied: true, MinProfitSpread: cfg.MinProfitSpread, FilterTags: []string{}}
	price := ctx.Price
	if price <= 0 {
		price = ctx.CurrentCandle.Close
	}
	if !ctx.Ready || price <= 0 {
		d.Reason = "market_context_unavailable"
		return d
	}

	// Compute both data sets when available so the history/chart payload remains
	// useful, but only require data for components whose server-side switch is on.
	var vwapOK bool
	if ctx.VWAPReady {
		d.VWAP, d.Sigma, d.ZScore, vwapOK = ctx.VWAP, ctx.Sigma, ctx.ZScore, true
	} else {
		d.VWAP, d.Sigma, d.ZScore, vwapOK = VWAPZScore(ctx.Candles, cfg.VWAPPeriodMinutes, price)
	}
	threshold := cfg.VWAPZScore
	high := vwapOK && d.ZScore > threshold
	low := vwapOK && d.ZScore < -threshold
	rangeHigh, rangeLow, rangeReady := ctx.DonchianHigh, ctx.DonchianLow, ctx.DonchianReady
	if !rangeReady && !ctx.DonchianComputed {
		rangeHigh, rangeLow, rangeReady = DonchianRange(ctx.Candles, cfg.VWAPRangeMinutes)
	}
	d.DonchianHigh, d.DonchianLow, d.DonchianReady = rangeHigh, rangeLow, rangeReady

	vwapGreen, vwapRed, vwapBoundsReady := vwapFilterBounds(cfg, d.VWAP, d.Sigma)
	donGreen, donRed, donBoundsReady := donchianFilterBounds(cfg, rangeHigh, rangeLow)
	vwapReady := !cfg.VWAPFilterEnabled || (vwapOK && vwapBoundsReady)
	donchianReady := !cfg.DonchianFilterEnabled || (rangeReady && donBoundsReady)
	zoneReady := true
	greenUpper, redLower := 0.0, 0.0
	if cfg.VWAPFilterEnabled {
		if vwapReady {
			greenUpper, redLower = vwapGreen, vwapRed
		} else {
			zoneReady = false
		}
	}
	if cfg.DonchianFilterEnabled {
		if donchianReady {
			if cfg.VWAPFilterEnabled {
				greenUpper = math.Min(greenUpper, donGreen)
				redLower = math.Max(redLower, donRed)
			} else {
				greenUpper, redLower = donGreen, donRed
			}
		} else {
			zoneReady = false
		}
	}
	if cfg.VWAPFilterEnabled || cfg.DonchianFilterEnabled {
		d.GreenZoneUpper, d.RedZoneLower = greenUpper, redLower
		d.BoundaryOverlap = zoneReady && greenUpper >= redLower
	} else {
		// Both server-side zone filters are disabled: keep the other radar gates
		// active and allow every price through this opening-zone stage.
		zoneReady = true
	}
	openingZoneReady := zoneReady && (!cfg.VWAPFilterEnabled || vwapReady) && (!cfg.DonchianFilterEnabled || donchianReady)
	if cfg.VWAPFilterEnabled || cfg.DonchianFilterEnabled {
		openingZoneReady = openingZoneReady && d.GreenZoneUpper > 0 && d.RedZoneLower > 0
	}

	addTag := func(tag string) {
		for _, existing := range d.FilterTags {
			if existing == tag {
				return
			}
		}
		d.FilterTags = append(d.FilterTags, tag)
	}
	longVWAPBlocked := cfg.VWAPFilterEnabled && (!vwapReady || price > vwapGreen)
	shortVWAPBlocked := cfg.VWAPFilterEnabled && (!vwapReady || price < vwapRed)
	longDonchianBlocked := cfg.DonchianFilterEnabled && (!donchianReady || price > donGreen)
	shortDonchianBlocked := cfg.DonchianFilterEnabled && (!donchianReady || price < donRed)

	// High: short-liquidation opens short; long-liquidation only closes short.
	// Middle: neither side opens; the liquidation direction may close the
	// profitable opposite position. Low is the mirror image for long entries.
	side = strings.ToLower(strings.TrimSpace(side))
	if side == "long" {
		d.OpenAllowed = openingZoneReady && !d.BoundaryOverlap && (!cfg.VWAPFilterEnabled || price <= vwapGreen) && (!cfg.DonchianFilterEnabled || price <= donGreen)
		d.CloseAllowed = !d.OpenAllowed
		if !vwapReady && cfg.VWAPFilterEnabled {
			addTag("vwap")
			d.Reason = "vwap_zscore_unavailable_long_liquidation_close_only"
		} else if !donchianReady && cfg.DonchianFilterEnabled {
			addTag("donchian")
			if !rangeReady {
				d.Reason = "donchian_range_unavailable_long_liquidation_close_only"
			} else {
				d.Reason = "vwap_zone_unavailable_long_liquidation_close_only"
			}
		} else if d.BoundaryOverlap {
			if cfg.VWAPFilterEnabled {
				addTag("vwap")
			}
			if cfg.DonchianFilterEnabled {
				addTag("donchian")
			}
			d.Reason = "vwap_donchian_zone_overlap_long_liquidation_close_only"
		} else if d.OpenAllowed {
			if !cfg.VWAPFilterEnabled && !cfg.DonchianFilterEnabled {
				d.Reason = "zone_filters_disabled_long_liquidation_open_long"
			} else if low {
				d.Reason = "low_zscore_long_liquidation_open_long"
			} else {
				d.Reason = "donchian_long_liquidation_open_long"
			}
		} else {
			if longVWAPBlocked {
				addTag("vwap")
			}
			if longDonchianBlocked {
				addTag("donchian")
			}
			if high {
				d.Reason = "high_zscore_long_liquidation_close_only"
			} else {
				d.Reason = "neutral_zscore_long_liquidation_close_only"
			}
		}
	} else if side == "short" {
		d.OpenAllowed = openingZoneReady && !d.BoundaryOverlap && (!cfg.VWAPFilterEnabled || price >= vwapRed) && (!cfg.DonchianFilterEnabled || price >= donRed)
		d.CloseAllowed = !d.OpenAllowed
		if !vwapReady && cfg.VWAPFilterEnabled {
			addTag("vwap")
			d.Reason = "vwap_zscore_unavailable_short_liquidation_close_only"
		} else if !donchianReady && cfg.DonchianFilterEnabled {
			addTag("donchian")
			if !rangeReady {
				d.Reason = "donchian_range_unavailable_short_liquidation_close_only"
			} else {
				d.Reason = "vwap_zone_unavailable_short_liquidation_close_only"
			}
		} else if d.BoundaryOverlap {
			if cfg.VWAPFilterEnabled {
				addTag("vwap")
			}
			if cfg.DonchianFilterEnabled {
				addTag("donchian")
			}
			d.Reason = "vwap_donchian_zone_overlap_short_liquidation_close_only"
		} else if d.OpenAllowed {
			if !cfg.VWAPFilterEnabled && !cfg.DonchianFilterEnabled {
				d.Reason = "zone_filters_disabled_short_liquidation_open_short"
			} else if high {
				d.Reason = "high_zscore_short_liquidation_open_short"
			} else {
				d.Reason = "donchian_short_liquidation_open_short"
			}
		} else {
			if shortVWAPBlocked {
				addTag("vwap")
			}
			if shortDonchianBlocked {
				addTag("donchian")
			}
			if low {
				d.Reason = "low_zscore_short_liquidation_close_only"
			} else {
				d.Reason = "neutral_zscore_short_liquidation_close_only"
			}
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
	if side == "long" && d.OpenAllowed {
		if d.CumulativeLongUSD < d.DeepQuotaUSD {
			d.OpenAllowed = false
			d.Reason += ";deep_liquidation_quota_not_ready"
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
