package app

import (
	"context"
	"errors"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/exchange"
)

var MarketRanges = map[string]time.Duration{"5m": 5 * time.Minute, "15m": 15 * time.Minute, "1h": time.Hour, "4h": 4 * time.Hour, "8h": 8 * time.Hour, "12h": 12 * time.Hour, "24h": 24 * time.Hour, "2d": 48 * time.Hour, "3d": 72 * time.Hour, "7d": 7 * 24 * time.Hour}

var marketWindowSpecs = []struct {
	label    string
	duration time.Duration
}{
	{"5m", 5 * time.Minute},
	{"1h", time.Hour},
	{"4h", 4 * time.Hour},
	{"12h", 12 * time.Hour},
	{"24h", 24 * time.Hour},
}

func (s *Service) marketInfoLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		for _, sym := range s.bootstrapSymbols() {
			cc, cancel := context.WithTimeout(ctx, 45*time.Second)
			m := exchange.BinanceMarketMetric(cc, sym)
			cancel()
			if ctx.Err() != nil {
				return
			}
			s.dashboard.Lock()
			s.dashboard.metrics[sym] = m
			s.dashboard.Unlock()
			if err := s.store.SaveMarketMetric(ctx, m); err != nil {
				s.log.Error("save market metrics", "error", err)
			}
			if m.MarkPrice == nil {
				s.health.Fail("binance_market_info_"+sym, errors.New("mark price unavailable"))
			} else {
				s.health.Touch("binance_market_info_"+sym, 1)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (s *Service) gammaLoop(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		for _, sym := range s.gammaSymbols() {
			cc, cancel := context.WithTimeout(ctx, 2*time.Minute)
			g, err := exchange.BinanceGamma(cc, sym)
			cancel()
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				s.health.Fail("binance_options_"+sym, err)
				s.log.Warn("options collection failed", "symbol", sym, "error", err)
				continue
			}
			if err = s.store.SaveGamma(ctx, g); err != nil {
				s.log.Error("store gamma failed", "error", err)
				continue
			}
			s.dashboard.Lock()
			s.dashboard.gamma[sym] = g
			s.dashboard.Unlock()
			s.health.Touch("binance_options_"+sym, 1)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Gamma is an anchor signal for the liquidation radar, not a per-symbol
// futures feed. Keep this list fixed so expanding APP_SYMBOLS cannot fan out
// hundreds of option API requests and trigger exchange rate limits.
func (s *Service) gammaSymbols() []string {
	return []string{alertGammaBTCAnchor, alertGammaETHAnchor}
}

func (s *Service) backfillMarketMetrics(ctx context.Context) {
	for _, symbol := range s.bootstrapSymbols() {
		if err := exchange.BinanceMetricHistory(ctx, symbol, s.store.SaveMarketMetric); err != nil && ctx.Err() == nil {
			s.log.Warn("market metrics backfill failed", "symbol", symbol, "error", err)
		}
	}
}
func ptr(v float64) *float64 { return &v }
func metricAt(ms []domain.MarketMetric, at time.Time) *domain.MarketMetric {
	end := sort.Search(len(ms), func(i int) bool { return ms[i].Time.After(at) })
	if end == 0 {
		return nil
	}
	var merged *domain.MarketMetric
	for i := end - 1; i >= 0; i-- {
		age := at.Sub(ms[i].Time)
		if age < 0 {
			continue
		}
		if age > 10*time.Minute {
			break
		}
		if merged == nil {
			value := ms[i]
			merged = &value
			continue
		}
		mergeMarketMetricFields(merged, ms[i])
	}
	if merged == nil {
		return nil
	}
	merged.NetPositionUSD = exchange.EstimateNetPosition(merged.OIUSD, merged.TopPositionRatio)
	return merged
}

// Historical Binance endpoints are sampled on the same nominal 5-minute
// cadence, but their latest timestamps can differ by one bucket. The store
// keeps each endpoint's partial row so it can merge exact timestamps; metricAt
// also fills missing fields from nearby rows to avoid treating a timestamp
// skew as a complete loss of OI or position-ratio history.
func mergeMarketMetricFields(dst *domain.MarketMetric, src domain.MarketMetric) {
	if dst.MarkPrice == nil {
		dst.MarkPrice = src.MarkPrice
	}
	if dst.Change24h == nil {
		dst.Change24h = src.Change24h
	}
	if dst.Volume24hUSD == nil {
		dst.Volume24hUSD = src.Volume24hUSD
	}
	if dst.High24h == nil {
		dst.High24h = src.High24h
	}
	if dst.Low24h == nil {
		dst.Low24h = src.Low24h
	}
	if dst.BestBid == nil {
		dst.BestBid = src.BestBid
	}
	if dst.BestAsk == nil {
		dst.BestAsk = src.BestAsk
	}
	if dst.OIQuantity == nil {
		dst.OIQuantity = src.OIQuantity
	}
	if dst.OIUSD == nil {
		dst.OIUSD = src.OIUSD
	}
	if dst.FundingRate == nil {
		dst.FundingRate = src.FundingRate
	}
	if dst.NextFunding == nil {
		dst.NextFunding = src.NextFunding
	}
	if dst.AccountRatio == nil {
		dst.AccountRatio = src.AccountRatio
	}
	if dst.TopPositionRatio == nil {
		dst.TopPositionRatio = src.TopPositionRatio
	}
	if len(dst.Warnings) == 0 && len(src.Warnings) > 0 {
		dst.Warnings = append([]string(nil), src.Warnings...)
	}
}

func marketWindowAnalysis(window domain.MarketWindow) string {
	missing := []string{}
	if window.OIDeltaUSD == nil {
		missing = append(missing, "OI历史数据")
	}
	if window.NetPositionDeltaUSD == nil {
		missing = append(missing, "净仓估算历史数据")
	}
	if window.CVDDeltaUSD == nil {
		missing = append(missing, "主动买卖历史数据")
	}
	if len(missing) > 0 {
		return "数据不足：" + strings.Join(missing, "、")
	}
	flow := "主动买入占优"
	if *window.CVDDeltaUSD < 0 {
		flow = "主动卖出占优"
	}
	change := "持仓稳定"
	if *window.OIDeltaUSD > 0 {
		change = "增仓"
	} else if *window.OIDeltaUSD < 0 {
		change = "减仓"
	}
	return change + " · " + flow
}
func BuildMarketSeries(candles []domain.Candle, metrics []domain.MarketMetric, from, to time.Time, step time.Duration) []domain.MarketPoint {
	out := []domain.MarketPoint{}
	cvd := 0.
	ci := 0
	for at := from; at.Before(to); at = at.Add(step) {
		end := at.Add(step)
		if end.After(to) {
			end = to
		}
		p := domain.MarketPoint{Time: at}
		if m := metricAt(metrics, end.Add(-time.Millisecond)); m != nil {
			p.OIUSD = m.OIUSD
			p.AccountRatio = m.AccountRatio
			p.TopPositionRatio = m.TopPositionRatio
		}
		buy, sell := 0., 0.
		count := 0
		for ci < len(candles) && candles[ci].Time.Before(end) {
			c := candles[ci]
			ci++
			if c.Time.Before(at) {
				continue
			}
			buy += c.TakerBuyUSD
			sell += math.Max(0, c.VolumeUSD-c.TakerBuyUSD)
			count++
		}
		// Keep CVD useful when an individual 1-minute candle is missing.  The
		// exchange data we do have still represents real aggressive flow, and a
		// later complete bucket should be able to resume from the last known
		// cumulative value instead of losing the rest of the selected window.
		if count > 0 {
			p.BuyUSD = ptr(buy)
			p.SellUSD = ptr(sell)
			p.DeltaUSD = ptr(buy - sell)
			cvd += buy - sell
			p.CVDUSD = ptr(cvd)
		}
		out = append(out, p)
	}
	return out
}
func (s *Service) MarketInfo(ctx context.Context, symbol, rangeName string) (domain.MarketInfo, error) {
	duration, ok := MarketRanges[rangeName]
	if !ok {
		return domain.MarketInfo{}, errors.New("unsupported market range")
	}
	now := time.Now().UTC()
	to := now.Truncate(time.Minute)
	from := to.Add(-duration)
	readFrom := to.Add(-24*time.Hour - 10*time.Minute)
	if from.Before(readFrom) {
		readFrom = from.Add(-10 * time.Minute)
	}
	metrics, err := s.store.MarketMetrics(ctx, symbol, readFrom, now.Add(time.Minute))
	if err != nil {
		return domain.MarketInfo{}, err
	}
	candles, err := s.store.CandlesRange(ctx, symbol, readFrom, to)
	if err != nil {
		return domain.MarketInfo{}, err
	}
	out := domain.MarketInfo{Symbol: symbol, Range: rangeName, State: "unavailable", Series: []domain.MarketPoint{}, Windows: []domain.MarketWindow{}, Gamma: domain.GammaView{State: "unavailable", Symbol: symbol, Levels: []domain.GammaLevel{}, Expiries: []domain.GammaExpiry{}}}
	s.dashboard.RLock()
	current, exists := s.dashboard.metrics[symbol]
	g, hasGamma := s.dashboard.gamma[symbol]
	s.dashboard.RUnlock()
	if !exists {
		if stored := metricAt(metrics, to); stored != nil {
			current = *stored
			exists = true
		}
	} else if stored := metricAt(metrics, to); stored != nil {
		// A transient live REST failure can leave one field nil even though a
		// recent historical sample still has it. Keep the live timestamp and
		// prefer its non-nil fields, filling only the gaps from history.
		mergeMarketMetricFields(&current, *stored)
	}
	if exists {
		current.NetPositionUSD = exchange.EstimateNetPosition(current.OIUSD, current.TopPositionRatio)
		out.Current = &current
		out.State = "ok"
		if current.MarkPrice == nil || current.OIUSD == nil || current.TopPositionRatio == nil || current.AccountRatio == nil || current.FundingRate == nil || len(current.Warnings) > 0 {
			out.State = "partial"
		}
		if now.Sub(current.Time) > 2*time.Minute {
			out.State = "stale"
		}
	}
	if hasGamma {
		out.Gamma = g
		if now.Sub(g.Time) > 15*time.Minute {
			out.Gamma.State = "stale"
		}
	}
	step := 5 * time.Minute
	if duration >= 48*time.Hour {
		step = 15 * time.Minute
	}
	if duration >= 7*24*time.Hour {
		step = time.Hour
	}
	out.Series = BuildMarketSeries(candles, metrics, from, to, step)
	for _, w := range marketWindowSpecs {
		win := domain.MarketWindow{Label: w.label, Analysis: "数据不足"}
		past := metricAt(metrics, to.Add(-w.duration))
		if past != nil && exists && out.State != "stale" {
			if current.OIUSD != nil && past.OIUSD != nil {
				win.OIDeltaUSD = ptr(*current.OIUSD - *past.OIUSD)
			}
			past.NetPositionUSD = exchange.EstimateNetPosition(past.OIUSD, past.TopPositionRatio)
			if current.NetPositionUSD != nil && past.NetPositionUSD != nil {
				win.NetPositionDeltaUSD = ptr(*current.NetPositionUSD - *past.NetPositionUSD)
			}
		}
		series := BuildMarketSeries(candles, metrics, to.Add(-w.duration), to, w.duration)
		if len(series) > 0 {
			win.CVDDeltaUSD = series[0].CVDUSD
		}
		win.Analysis = marketWindowAnalysis(win)
		out.Windows = append(out.Windows, win)
	}
	return out, nil
}
