package app

import (
	"context"
	"errors"
	"math"
	"sort"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/exchange"
)

var MarketRanges = map[string]time.Duration{"5m": 5 * time.Minute, "15m": 15 * time.Minute, "1h": time.Hour, "4h": 4 * time.Hour, "8h": 8 * time.Hour, "12h": 12 * time.Hour, "24h": 24 * time.Hour, "2d": 48 * time.Hour, "3d": 72 * time.Hour, "7d": 7 * 24 * time.Hour}

func (s *Service) marketInfoLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		for _, sym := range s.cfg.Symbols {
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
		for _, sym := range s.cfg.Symbols {
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
func (s *Service) backfillMarketMetrics(ctx context.Context) {
	for _, symbol := range s.cfg.Symbols {
		if err := exchange.BinanceMetricHistory(ctx, symbol, s.store.SaveMarketMetric); err != nil && ctx.Err() == nil {
			s.log.Warn("market metrics backfill failed", "symbol", symbol, "error", err)
		}
	}
}
func ptr(v float64) *float64 { return &v }
func metricAt(ms []domain.MarketMetric, at time.Time) *domain.MarketMetric {
	i := sort.Search(len(ms), func(i int) bool { return ms[i].Time.After(at) }) - 1
	if i < 0 || at.Sub(ms[i].Time) > 10*time.Minute {
		return nil
	}
	m := ms[i]
	m.NetPositionUSD = exchange.EstimateNetPosition(m.OIUSD, m.TopPositionRatio)
	return &m
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
		expected := int(end.Sub(at) / time.Minute)
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
		if expected > 0 && count >= expected {
			p.BuyUSD = ptr(buy)
			p.SellUSD = ptr(sell)
			p.DeltaUSD = ptr(buy - sell)
			cvd += buy - sell
			p.CVDUSD = ptr(cvd)
		} else {
			cvd = 0
		}
		out = append(out, p)
	}
	// Once a gap occurs, subsequent cumulative values cannot claim complete CVD.
	complete := true
	for i := range out {
		if out[i].DeltaUSD == nil {
			complete = false
		}
		if !complete {
			out[i].CVDUSD = nil
		}
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
	if !exists && len(metrics) > 0 {
		current = metrics[len(metrics)-1]
		exists = true
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
	for _, w := range []struct {
		label    string
		duration time.Duration
	}{{"5m", 5 * time.Minute}, {"1h", time.Hour}, {"4h", 4 * time.Hour}, {"24h", 24 * time.Hour}} {
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
		if win.OIDeltaUSD != nil && win.CVDDeltaUSD != nil {
			flow := "主动买入占优"
			if *win.CVDDeltaUSD < 0 {
				flow = "主动卖出占优"
			}
			change := "持仓稳定"
			if *win.OIDeltaUSD > 0 {
				change = "增仓"
			} else if *win.OIDeltaUSD < 0 {
				change = "减仓"
			}
			win.Analysis = change + " · " + flow
		}
		out.Windows = append(out.Windows, win)
	}
	return out, nil
}
