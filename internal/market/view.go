package market

import (
	"errors"
	"math"
	"sort"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

const (
	SourceName  = "median_composite"
	Interval15m = 15 * time.Minute
)

var intervals = map[string]time.Duration{
	"1m": time.Minute, "2m": 2 * time.Minute, "3m": 3 * time.Minute,
	"5m": 5 * time.Minute, "10m": 10 * time.Minute, "15m": 15 * time.Minute,
	"30m": 30 * time.Minute, "1h": time.Hour, "4h": 4 * time.Hour,
	"8h": 8 * time.Hour, "12h": 12 * time.Hour, "24h": 24 * time.Hour,
}

func ParseInterval(value string) (time.Duration, bool) {
	d, ok := intervals[value]
	return d, ok
}

// CompositeMinutes takes the median OHLC across available exchanges for each
// UTC minute. Venue USD volume is additive and is intentionally not averaged.
func CompositeMinutes(raw []domain.Candle) []domain.MarketCandle {
	groups := map[int64][]domain.Candle{}
	var keys []int64
	for _, c := range raw {
		if c.Open <= 0 || c.High <= 0 || c.Low <= 0 || c.Close <= 0 {
			continue
		}
		key := c.Time.UTC().Truncate(time.Minute).Unix()
		if _, ok := groups[key]; !ok {
			keys = append(keys, key)
		}
		groups[key] = append(groups[key], c)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	out := make([]domain.MarketCandle, 0, len(keys))
	for _, key := range keys {
		rows := groups[key]
		opens, highs, lows, closes := make([]float64, 0, len(rows)), make([]float64, 0, len(rows)), make([]float64, 0, len(rows)), make([]float64, 0, len(rows))
		volume := 0.0
		venues := map[string]struct{}{}
		for _, c := range rows {
			opens = append(opens, c.Open)
			highs = append(highs, c.High)
			lows = append(lows, c.Low)
			closes = append(closes, c.Close)
			volume += math.Max(0, c.VolumeUSD)
			venues[c.Exchange] = struct{}{}
		}
		o, h, l, cl := median(opens), median(highs), median(lows), median(closes)
		h = math.Max(h, math.Max(o, cl))
		l = math.Min(l, math.Min(o, cl))
		out = append(out, domain.MarketCandle{Time: time.Unix(key, 0).UTC(), Open: o, High: h, Low: l, Close: cl, VolumeUSD: volume, ExchangeCount: len(venues), Complete: true})
	}
	return out
}

func BuildView(symbol string, raw []domain.Candle, now time.Time) (domain.MarketView, error) {
	return BuildPagedView(symbol, raw, raw, nil, "15m", now.Add(time.Minute), 120, now, time.Time{}, false)
}

func Aggregate15m(minutes []domain.MarketCandle, now time.Time) []domain.MarketCandle {
	return Aggregate(minutes, Interval15m, now)
}

func Aggregate(minutes []domain.MarketCandle, interval time.Duration, now time.Time) []domain.MarketCandle {
	type bucket struct {
		c        domain.MarketCandle
		points   int
		venueMax int
	}
	m := map[int64]*bucket{}
	var keys []int64
	for _, c := range minutes {
		k := bucketTime(c.Time, interval).Unix()
		b := m[k]
		if b == nil {
			b = &bucket{c: domain.MarketCandle{Time: time.Unix(k, 0).UTC(), Open: c.Open, High: c.High, Low: c.Low}}
			m[k] = b
			keys = append(keys, k)
		}
		if b.points > 0 {
			b.c.High = math.Max(b.c.High, c.High)
			b.c.Low = math.Min(b.c.Low, c.Low)
		}
		b.c.Close = c.Close
		b.c.VolumeUSD += c.VolumeUSD
		b.points++
		if c.ExchangeCount > b.venueMax {
			b.venueMax = c.ExchangeCount
		}
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	out := make([]domain.MarketCandle, 0, len(keys))
	expected := int(interval / time.Minute)
	required := int(math.Ceil(float64(expected) * .8))
	for _, k := range keys {
		b := m[k]
		b.c.ExchangeCount = b.venueMax
		b.c.Complete = b.points >= required && !b.c.Time.Add(interval).After(now.UTC())
		out = append(out, b.c)
	}
	return out
}

func BuildPagedView(symbol string, raw, summaryRaw []domain.Candle, predictions []domain.Prediction, intervalName string, before time.Time, limit int, now, availableFrom time.Time, backfillComplete bool) (domain.MarketView, error) {
	interval, ok := ParseInterval(intervalName)
	if !ok {
		return domain.MarketView{}, errors.New("unsupported market interval")
	}
	now, before = now.UTC(), before.UTC()
	minutes := CompositeMinutes(raw)
	if len(minutes) == 0 {
		return domain.MarketView{}, errors.New("market data unavailable")
	}
	candles := Aggregate(minutes, interval, now)
	end := sort.Search(len(candles), func(i int) bool { return !candles[i].Time.Before(before) })
	candles = candles[:end]
	if len(candles) == 0 {
		return domain.MarketView{}, errors.New("market candles unavailable")
	}
	DetectPatterns(candles)
	if limit < 1 {
		limit = 120
	}
	if len(candles) > limit {
		candles = candles[len(candles)-limit:]
	}
	summaryMinutes := CompositeMinutes(summaryRaw)
	if len(summaryMinutes) == 0 {
		summaryMinutes = minutes
	}
	first := candles[0].Time
	hasMore := !availableFrom.IsZero() && availableFrom.Before(first)
	var next *time.Time
	if hasMore {
		cursor := first
		next = &cursor
	}
	view := domain.MarketView{
		Symbol: symbol, Interval: intervalName, Source: SourceName,
		Summary: summarize(summaryMinutes, now), Candles: candles,
		HasMore: hasMore, NextBefore: next, AvailableFrom: availableFrom,
		BackfillComplete: backfillComplete, ModelSignals: DirectionalSignals(predictions, interval),
	}
	return view, nil
}

func DirectionalSignals(predictions []domain.Prediction, interval time.Duration) []domain.DirectionalSignal {
	type key struct {
		bucket int64
		side   string
	}
	best := make(map[key]domain.DirectionalSignal)
	for _, p := range predictions {
		if p.State != "ok" {
			continue
		}
		class := p.LeadingClass
		if class == "" {
			class = leading(p.Probabilities)
		}
		var side string
		switch class {
		case domain.UpperFirst:
			side = "long"
		case domain.LowerFirst:
			side = "short"
		default:
			continue
		}
		probability := p.Probabilities[class]
		if probability < .45 {
			continue
		}
		candleTime := bucketTime(p.Time, interval)
		signal := domain.DirectionalSignal{Time: p.Time, CandleTime: candleTime, Side: side, Probability: probability, Price: p.MarkPrice, ModelVersion: p.ModelVersion}
		k := key{bucket: candleTime.Unix(), side: side}
		if previous, ok := best[k]; !ok || signal.Probability > previous.Probability {
			best[k] = signal
		}
	}
	out := make([]domain.DirectionalSignal, 0, len(best))
	for _, signal := range best {
		out = append(out, signal)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CandleTime.Equal(out[j].CandleTime) {
			return out[i].Side < out[j].Side
		}
		return out[i].CandleTime.Before(out[j].CandleTime)
	})
	return out
}

func bucketTime(value time.Time, interval time.Duration) time.Time {
	seconds := int64(interval / time.Second)
	unix := value.UTC().Unix()
	return time.Unix(unix-unix%seconds, 0).UTC()
}

func DetectPatterns(cs []domain.MarketCandle) {
	for i := range cs {
		c := cs[i]
		if !c.Complete {
			continue
		}
		span := c.High - c.Low
		if span <= 0 {
			continue
		}
		body := math.Abs(c.Close - c.Open)
		upper := c.High - math.Max(c.Open, c.Close)
		lower := math.Min(c.Open, c.Close) - c.Low
		var patterns []domain.CandlePattern
		if body <= span*.1 {
			patterns = append(patterns, domain.CandlePattern{Name: "doji", Bias: "neutral"})
		}
		if lower >= 2*body && upper <= body && math.Min(c.Open, c.Close) >= c.Low+span*2/3 {
			patterns = append(patterns, domain.CandlePattern{Name: "hammer", Bias: "bullish"})
		}
		if upper >= 2*body && lower <= body && math.Max(c.Open, c.Close) <= c.Low+span/3 {
			patterns = append(patterns, domain.CandlePattern{Name: "shooting_star", Bias: "bearish"})
		}
		if i > 0 && cs[i-1].Complete {
			p := cs[i-1]
			if p.Close < p.Open && c.Close > c.Open && c.Open <= p.Close && c.Close >= p.Open {
				patterns = append(patterns, domain.CandlePattern{Name: "bullish_engulfing", Bias: "bullish"})
			}
			if p.Close > p.Open && c.Close < c.Open && c.Open >= p.Close && c.Close <= p.Open {
				patterns = append(patterns, domain.CandlePattern{Name: "bearish_engulfing", Bias: "bearish"})
			}
		}
		cs[i].Patterns = patterns
	}
}

func EnrichPrediction(p domain.Prediction, minutes []domain.MarketCandle, now time.Time) domain.Prediction {
	now = now.UTC()
	current := p.MarkPrice
	if len(minutes) > 0 {
		current = minutes[len(minutes)-1].Close
	}
	p.MarkPrice = current
	p.LeadingClass = leading(p.Probabilities)
	expires := p.Time.Add(time.Duration(p.HorizonMinutes) * time.Minute)
	upperAt, lowerAt := firstTouches(minutes, p.Time, expires, p.UpperWall, p.LowerWall)
	p.UpperTrigger = trigger("upper", p.UpperWall, current, p.ATR, p.Probabilities[domain.UpperFirst], p.Time, expires, upperAt, now, p.State)
	p.LowerTrigger = trigger("lower", p.LowerWall, current, p.ATR, p.Probabilities[domain.LowerFirst], p.Time, expires, lowerAt, now, p.State)
	if upperAt != nil && lowerAt != nil {
		if upperAt.Equal(*lowerAt) {
			p.TriggerOrder = "ambiguous"
		} else if upperAt.Before(*lowerAt) {
			p.TriggerOrder = domain.UpperFirst
		} else {
			p.TriggerOrder = domain.LowerFirst
		}
	} else if upperAt != nil {
		p.TriggerOrder = domain.UpperFirst
	} else if lowerAt != nil {
		p.TriggerOrder = domain.LowerFirst
	} else if now.After(expires) {
		p.TriggerOrder = domain.Neither
	} else {
		p.TriggerOrder = "pending"
	}
	return p
}

func firstTouches(minutes []domain.MarketCandle, start, end time.Time, upper, lower *domain.Wall) (*time.Time, *time.Time) {
	var up, down *time.Time
	for _, c := range minutes {
		if !c.Time.After(start) || c.Time.After(end) {
			continue
		}
		t := c.Time
		if up == nil && upper != nil && c.High >= upper.Price {
			x := t
			up = &x
		}
		if down == nil && lower != nil && c.Low <= lower.Price {
			x := t
			down = &x
		}
	}
	return up, down
}
func trigger(side string, wall *domain.Wall, current, atr, prob float64, start, expires time.Time, touched *time.Time, now time.Time, predictionState string) *domain.TriggerInfo {
	v := &domain.TriggerInfo{Side: side, Status: "unavailable", CurrentPrice: current, Probability: prob, PredictedAt: start, ExpiresAt: expires, TriggeredAt: touched, UpdatedAt: now}
	if wall == nil || current <= 0 || predictionState == "data_insufficient" {
		return v
	}
	v.TargetPrice = wall.Price
	v.DistancePrice = math.Abs(wall.Price - current)
	v.DistancePercent = v.DistancePrice / current * 100
	if atr > 0 {
		v.DistanceATR = v.DistancePrice / atr
	}
	switch {
	case touched != nil:
		v.Status = "triggered"
	case now.After(expires):
		v.Status = "expired"
	case atr > 0 && v.DistanceATR <= .5:
		v.Status = "approaching"
	default:
		v.Status = "armed"
	}
	return v
}
func leading(p map[string]float64) string {
	if len(p) == 0 {
		return ""
	}
	best := ""
	value := -1.0
	for _, k := range []string{domain.UpperFirst, domain.LowerFirst, domain.Neither} {
		if p[k] > value {
			best, value = k, p[k]
		}
	}
	return best
}
func summarize(minutes []domain.MarketCandle, now time.Time) domain.PriceSummary {
	cutoff := now.Add(-24 * time.Hour)
	start := sort.Search(len(minutes), func(i int) bool { return !minutes[i].Time.Before(cutoff) })
	xs := minutes[start:]
	if len(xs) == 0 {
		xs = minutes
	}
	s := domain.PriceSummary{LastPrice: xs[len(xs)-1].Close, High24h: xs[0].High, Low24h: xs[0].Low, UpdatedAt: xs[len(xs)-1].Time}
	first := xs[0].Open
	for _, c := range xs {
		s.High24h = math.Max(s.High24h, c.High)
		s.Low24h = math.Min(s.Low24h, c.Low)
		s.Volume24hUSD += c.VolumeUSD
		if c.ExchangeCount > s.ExchangeCount {
			s.ExchangeCount = c.ExchangeCount
		}
	}
	s.Change24h = s.LastPrice - first
	if first > 0 {
		s.ChangePct24h = s.Change24h / first * 100
	}
	return s
}
func median(xs []float64) float64 {
	sort.Float64s(xs)
	n := len(xs)
	if n%2 == 1 {
		return xs[n/2]
	}
	return (xs[n/2-1] + xs[n/2]) / 2
}
