package exchange

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

type Client interface {
	Name() string
	History(context.Context, string, time.Time, time.Time) ([]domain.Candle, error)
	Current(context.Context, string) (domain.Candle, error)
}

type oiRow struct {
	Value     string `json:"sumOpenInterestValue"`
	Timestamp int64  `json:"timestamp"`
}

type metricPoint struct {
	Timestamp int64
	Value     float64
}

type Health struct {
	Connected   bool      `json:"connected"`
	LastMessage time.Time `json:"last_message"`
	LastError   string    `json:"last_error,omitempty"`
	Coverage    float64   `json:"coverage"`
}
type HealthRegistry struct {
	mu sync.RWMutex
	m  map[string]Health
}

func NewHealthRegistry() *HealthRegistry            { return &HealthRegistry{m: map[string]Health{}} }
func (h *HealthRegistry) Set(name string, v Health) { h.mu.Lock(); defer h.mu.Unlock(); h.m[name] = v }
func (h *HealthRegistry) Touch(name string, coverage float64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	v := h.m[name]
	v.Connected = true
	v.LastMessage = time.Now().UTC()
	v.LastError = ""
	v.Coverage = coverage
	h.m[name] = v
}
func (h *HealthRegistry) Fail(name string, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	v := h.m[name]
	v.Connected = false
	v.LastError = err.Error()
	h.m[name] = v
}
func (h *HealthRegistry) Snapshot() map[string]Health {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := map[string]Health{}
	for k, v := range h.m {
		out[k] = v
	}
	return out
}

type httpClient struct{ c *http.Client }

func newHTTP() httpClient { return httpClient{&http.Client{Timeout: 20 * time.Second}} }
func (h httpClient) get(ctx context.Context, raw string, v any) error {
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if e != nil {
		return e
	}
	req.Header.Set("User-Agent", "liquidation-predictor/1.0")
	resp, e := h.c.Do(req)
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("GET %s: %s: %s", raw, resp.Status, string(b))
	}
	return json.NewDecoder(resp.Body).Decode(v)
}
func f(s string) float64 { v, _ := strconv.ParseFloat(s, 64); return v }
func ms(v any) time.Time {
	switch x := v.(type) {
	case float64:
		return time.UnixMilli(int64(x)).UTC()
	case string:
		i, _ := strconv.ParseInt(x, 10, 64)
		return time.UnixMilli(i).UTC()
	}
	return time.Time{}
}

type Binance struct{ httpClient }

func NewBinance() Client      { return &Binance{newHTTP()} }
func (*Binance) Name() string { return "binance" }
func (b *Binance) History(ctx context.Context, symbol string, since, until time.Time) ([]domain.Candle, error) {
	var out []domain.Candle
	start := since.UnixMilli()
	for {
		var rows [][]any
		u := "https://fapi.binance.com/fapi/v1/klines?symbol=" + url.QueryEscape(symbol) + "&interval=1m&limit=1500&startTime=" + strconv.FormatInt(start, 10) + "&endTime=" + strconv.FormatInt(until.Add(-time.Millisecond).UnixMilli(), 10)
		if e := b.get(ctx, u, &rows); e != nil {
			return out, e
		}
		if len(rows) == 0 {
			break
		}
		for _, r := range rows {
			if len(r) < 11 {
				continue
			}
			c := domain.Candle{Exchange: b.Name(), Symbol: symbol, Time: ms(r[0]), Open: f(anyString(r[1])), High: f(anyString(r[2])), Low: f(anyString(r[3])), Close: f(anyString(r[4])), VolumeUSD: f(anyString(r[7])), TakerBuyUSD: f(anyString(r[10]))}
			out = append(out, c)
		}
		next := out[len(out)-1].Time.Add(time.Minute).UnixMilli()
		if next <= start || len(rows) < 1500 || next >= until.UnixMilli() {
			break
		}
		start = next
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		case <-time.After(80 * time.Millisecond):
		}
	}
	// Binance exposes 5m derivatives metrics for approximately the latest month.
	metricSince := since
	if cutoff := time.Now().UTC().AddDate(0, 0, -30); metricSince.Before(cutoff) {
		metricSince = cutoff
	}
	if !metricSince.Before(until) {
		return out, nil
	}
	var oi []oiRow
	for st := metricSince.UnixMilli(); st < until.UnixMilli(); {
		var rows []oiRow
		end := minInt64(st+500*5*60*1000, until.UnixMilli())
		u := "https://fapi.binance.com/futures/data/openInterestHist?symbol=" + symbol + "&period=5m&limit=500&startTime=" + strconv.FormatInt(st, 10) + "&endTime=" + strconv.FormatInt(end, 10)
		if e := b.get(ctx, u, &rows); e != nil {
			break
		}
		if len(rows) == 0 {
			break
		}
		oi = append(oi, rows...)
		st = rows[len(rows)-1].Timestamp + 1
		time.Sleep(30 * time.Millisecond)
	}
	mergeOI(out, oi)
	var funding []struct {
		Rate      string `json:"fundingRate"`
		Timestamp int64  `json:"fundingTime"`
	}
	if e := b.get(ctx, "https://fapi.binance.com/fapi/v1/fundingRate?symbol="+symbol+"&limit=1000&startTime="+strconv.FormatInt(metricSince.UnixMilli(), 10)+"&endTime="+strconv.FormatInt(until.UnixMilli(), 10), &funding); e == nil {
		points := make([]metricPoint, 0, len(funding))
		for _, p := range funding {
			points = append(points, metricPoint{p.Timestamp, f(p.Rate)})
		}
		mergeMetric(out, points, func(c *domain.Candle, v float64) { c.FundingRate = v })
	}
	var ratios []metricPoint
	for st := metricSince.UnixMilli(); st < until.UnixMilli(); {
		var rows []struct {
			Ratio     string `json:"longShortRatio"`
			Timestamp int64  `json:"timestamp"`
		}
		end := minInt64(st+500*5*60*1000, until.UnixMilli())
		u := "https://fapi.binance.com/futures/data/globalLongShortAccountRatio?symbol=" + symbol + "&period=5m&limit=500&startTime=" + strconv.FormatInt(st, 10) + "&endTime=" + strconv.FormatInt(end, 10)
		if e := b.get(ctx, u, &rows); e != nil || len(rows) == 0 {
			break
		}
		for _, p := range rows {
			ratios = append(ratios, metricPoint{p.Timestamp, f(p.Ratio)})
		}
		next := rows[len(rows)-1].Timestamp + 1
		if next <= st {
			break
		}
		st = next
		time.Sleep(30 * time.Millisecond)
	}
	mergeMetric(out, ratios, func(c *domain.Candle, v float64) { c.LongShortRatio = v })
	return out, nil
}
func (b *Binance) Current(ctx context.Context, symbol string) (domain.Candle, error) {
	var k [][]any
	if e := b.get(ctx, "https://fapi.binance.com/fapi/v1/klines?symbol="+symbol+"&interval=1m&limit=2", &k); e != nil || len(k) == 0 {
		return domain.Candle{}, firstErr(e, errors.New("empty Binance kline"))
	}
	r := k[len(k)-1]
	c := domain.Candle{Exchange: b.Name(), Symbol: symbol, Time: ms(r[0]), Open: f(anyString(r[1])), High: f(anyString(r[2])), Low: f(anyString(r[3])), Close: f(anyString(r[4])), VolumeUSD: f(anyString(r[7])), TakerBuyUSD: f(anyString(r[10]))}
	var oi struct {
		OpenInterest string `json:"openInterest"`
	}
	if e := b.get(ctx, "https://fapi.binance.com/fapi/v1/openInterest?symbol="+symbol, &oi); e == nil {
		c.OpenInterestUSD = f(oi.OpenInterest) * c.Close
	}
	var p struct {
		Funding string `json:"lastFundingRate"`
	}
	if e := b.get(ctx, "https://fapi.binance.com/fapi/v1/premiumIndex?symbol="+symbol, &p); e == nil {
		c.FundingRate = f(p.Funding)
	}
	var ratio []struct {
		R string `json:"longShortRatio"`
	}
	if e := b.get(ctx, "https://fapi.binance.com/futures/data/globalLongShortAccountRatio?symbol="+symbol+"&period=5m&limit=1", &ratio); e == nil && len(ratio) > 0 {
		c.LongShortRatio = f(ratio[0].R)
	}
	return c, nil
}

type Bybit struct{ httpClient }

func NewBybit() Client      { return &Bybit{newHTTP()} }
func (*Bybit) Name() string { return "bybit" }
func (b *Bybit) History(ctx context.Context, symbol string, since, until time.Time) ([]domain.Candle, error) {
	var out []domain.Candle
	end := until.UnixMilli()
	for end > since.UnixMilli() {
		var res struct {
			RetCode int `json:"retCode"`
			Result  struct {
				List [][]string `json:"list"`
			} `json:"result"`
		}
		u := fmt.Sprintf("https://api.bybit.com/v5/market/kline?category=linear&symbol=%s&interval=1&limit=1000&end=%d", symbol, end)
		if e := b.get(ctx, u, &res); e != nil {
			return out, e
		}
		if res.RetCode != 0 || len(res.Result.List) == 0 {
			break
		}
		old := end
		for _, r := range res.Result.List {
			if len(r) < 7 {
				continue
			}
			t := ms(r[0])
			if t.Before(since) || !t.Before(until) {
				continue
			}
			close := f(r[4])
			out = append(out, domain.Candle{Exchange: b.Name(), Symbol: symbol, Time: t, Open: f(r[1]), High: f(r[2]), Low: f(r[3]), Close: close, VolumeUSD: f(r[6])})
			if t.UnixMilli() < old {
				old = t.UnixMilli()
			}
		}
		if old >= end {
			break
		}
		end = old - 1
		time.Sleep(80 * time.Millisecond)
	}
	reverseCandles(out)
	metricSince := since
	if cutoff := time.Now().UTC().AddDate(0, 0, -30); metricSince.Before(cutoff) {
		metricSince = cutoff
	}
	if !metricSince.Before(until) {
		return out, nil
	}
	type bybitOI struct {
		OI        string `json:"openInterest"`
		Timestamp string `json:"timestamp"`
	}
	var points []bybitOI
	for end := until.UnixMilli(); end > metricSince.UnixMilli(); {
		var res struct {
			Result struct {
				List []bybitOI `json:"list"`
			} `json:"result"`
		}
		u := fmt.Sprintf("https://api.bybit.com/v5/market/open-interest?category=linear&symbol=%s&intervalTime=5min&limit=200&endTime=%d", symbol, end)
		if e := b.get(ctx, u, &res); e != nil || len(res.Result.List) == 0 {
			break
		}
		oldest := end
		for _, p := range res.Result.List {
			points = append(points, p)
			ts, _ := strconv.ParseInt(p.Timestamp, 10, 64)
			if ts < oldest {
				oldest = ts
			}
		}
		if oldest >= end || oldest <= metricSince.UnixMilli() {
			break
		}
		end = oldest - 1
		time.Sleep(40 * time.Millisecond)
	}
	sort.Slice(points, func(i, j int) bool { return points[i].Timestamp < points[j].Timestamp })
	j := 0
	for i := range out {
		for j+1 < len(points) && !ms(points[j+1].Timestamp).After(out[i].Time) {
			j++
		}
		if len(points) > 0 && !ms(points[j].Timestamp).After(out[i].Time) {
			out[i].OpenInterestUSD = f(points[j].OI) * out[i].Close
		}
	}
	var funding struct {
		Result struct {
			List []struct {
				Rate      string `json:"fundingRate"`
				Timestamp string `json:"fundingRateTimestamp"`
			} `json:"list"`
		} `json:"result"`
	}
	if e := b.get(ctx, "https://api.bybit.com/v5/market/funding/history?category=linear&symbol="+symbol+"&limit=200", &funding); e == nil {
		var ps []metricPoint
		for _, p := range funding.Result.List {
			ts, _ := strconv.ParseInt(p.Timestamp, 10, 64)
			if ts >= metricSince.UnixMilli() && ts < until.UnixMilli() {
				ps = append(ps, metricPoint{ts, f(p.Rate)})
			}
		}
		mergeMetric(out, ps, func(c *domain.Candle, v float64) { c.FundingRate = v })
	}
	var ratioPoints []metricPoint
	for end := until.UnixMilli(); end > metricSince.UnixMilli(); {
		var res struct {
			Result struct {
				List []struct {
					Buy       string `json:"buyRatio"`
					Sell      string `json:"sellRatio"`
					Timestamp string `json:"timestamp"`
				} `json:"list"`
			} `json:"result"`
		}
		u := fmt.Sprintf("https://api.bybit.com/v5/market/account-ratio?category=linear&symbol=%s&period=5min&limit=500&endTime=%d", symbol, end)
		if e := b.get(ctx, u, &res); e != nil || len(res.Result.List) == 0 {
			break
		}
		oldest := end
		for _, p := range res.Result.List {
			ts, _ := strconv.ParseInt(p.Timestamp, 10, 64)
			buy, sell := f(p.Buy), f(p.Sell)
			if sell > 0 {
				ratioPoints = append(ratioPoints, metricPoint{ts, buy / sell})
			}
			if ts < oldest {
				oldest = ts
			}
		}
		if oldest >= end || oldest <= metricSince.UnixMilli() {
			break
		}
		end = oldest - 1
		time.Sleep(40 * time.Millisecond)
	}
	mergeMetric(out, ratioPoints, func(c *domain.Candle, v float64) { c.LongShortRatio = v })
	return out, nil
}
func (b *Bybit) Current(ctx context.Context, symbol string) (domain.Candle, error) {
	var raw struct {
		RetCode int `json:"retCode"`
		Result  struct {
			List []map[string]string `json:"list"`
		} `json:"result"`
	}
	if e := b.get(ctx, "https://api.bybit.com/v5/market/tickers?category=linear&symbol="+symbol, &raw); e != nil || len(raw.Result.List) == 0 {
		return domain.Candle{}, firstErr(e, errors.New("empty Bybit ticker"))
	}
	x := raw.Result.List[0]
	price := f(x["markPrice"])
	c := domain.Candle{Exchange: b.Name(), Symbol: symbol, Time: time.Now().UTC().Truncate(time.Minute), Open: price, High: price, Low: price, Close: price, OpenInterestUSD: f(x["openInterest"]) * price, FundingRate: f(x["fundingRate"])}
	var k struct {
		Result struct {
			List [][]string `json:"list"`
		} `json:"result"`
	}
	if e := b.get(ctx, "https://api.bybit.com/v5/market/kline?category=linear&symbol="+symbol+"&interval=1&limit=1", &k); e == nil && len(k.Result.List) > 0 && len(k.Result.List[0]) >= 7 {
		r := k.Result.List[0]
		c.Time, c.Open, c.High, c.Low, c.Close, c.VolumeUSD = ms(r[0]), f(r[1]), f(r[2]), f(r[3]), f(r[4]), f(r[6])
	}
	var ratio struct {
		Result struct {
			List []struct {
				Buy  string `json:"buyRatio"`
				Sell string `json:"sellRatio"`
			} `json:"list"`
		} `json:"result"`
	}
	if e := b.get(ctx, "https://api.bybit.com/v5/market/account-ratio?category=linear&symbol="+symbol+"&period=5min&limit=1", &ratio); e == nil && len(ratio.Result.List) > 0 {
		sell := f(ratio.Result.List[0].Sell)
		if sell > 0 {
			c.LongShortRatio = f(ratio.Result.List[0].Buy) / sell
		}
	}
	return c, nil
}

type OKX struct{ httpClient }

func NewOKX() Client            { return &OKX{newHTTP()} }
func (*OKX) Name() string       { return "okx" }
func okxSymbol(s string) string { return strings.TrimSuffix(s, "USDT") + "-USDT-SWAP" }
func (o *OKX) History(ctx context.Context, symbol string, since, until time.Time) ([]domain.Candle, error) {
	var out []domain.Candle
	after := strconv.FormatInt(until.UnixMilli(), 10)
	for {
		var r struct {
			Code string     `json:"code"`
			Data [][]string `json:"data"`
		}
		u := "https://www.okx.com/api/v5/market/history-candles?instId=" + okxSymbol(symbol) + "&bar=1m&limit=300"
		u += "&after=" + after
		if e := o.get(ctx, u, &r); e != nil {
			return out, e
		}
		if len(r.Data) == 0 {
			break
		}
		oldest := int64(mathMaxInt)
		for _, x := range r.Data {
			if len(x) < 8 {
				continue
			}
			t := ms(x[0])
			if t.Before(since) || !t.Before(until) {
				continue
			}
			out = append(out, domain.Candle{Exchange: o.Name(), Symbol: symbol, Time: t, Open: f(x[1]), High: f(x[2]), Low: f(x[3]), Close: f(x[4]), VolumeUSD: f(x[7])})
			if t.UnixMilli() < oldest {
				oldest = t.UnixMilli()
			}
		}
		if oldest == mathMaxInt || oldest <= since.UnixMilli() {
			break
		}
		after = strconv.FormatInt(oldest, 10)
		time.Sleep(80 * time.Millisecond)
	}
	reverseCandles(out)
	var funding struct {
		Data []map[string]string `json:"data"`
	}
	if e := o.get(ctx, "https://www.okx.com/api/v5/public/funding-rate-history?instId="+okxSymbol(symbol)+"&limit=100", &funding); e == nil {
		var ps []metricPoint
		for _, p := range funding.Data {
			ts, _ := strconv.ParseInt(p["fundingTime"], 10, 64)
			if ts >= since.UnixMilli() && ts < until.UnixMilli() {
				ps = append(ps, metricPoint{ts, f(p["fundingRate"])})
			}
		}
		mergeMetric(out, ps, func(c *domain.Candle, v float64) { c.FundingRate = v })
	}
	return out, nil
}
func (o *OKX) Current(ctx context.Context, symbol string) (domain.Candle, error) {
	inst := okxSymbol(symbol)
	var t struct {
		Data []map[string]string `json:"data"`
	}
	if e := o.get(ctx, "https://www.okx.com/api/v5/market/ticker?instId="+inst, &t); e != nil || len(t.Data) == 0 {
		return domain.Candle{}, firstErr(e, errors.New("empty OKX ticker"))
	}
	price := f(t.Data[0]["last"])
	c := domain.Candle{Exchange: o.Name(), Symbol: symbol, Time: time.Now().UTC().Truncate(time.Minute), Open: price, High: price, Low: price, Close: price}
	var k struct {
		Data [][]string `json:"data"`
	}
	if e := o.get(ctx, "https://www.okx.com/api/v5/market/candles?instId="+inst+"&bar=1m&limit=1", &k); e == nil && len(k.Data) > 0 && len(k.Data[0]) >= 8 {
		r := k.Data[0]
		c.Time, c.Open, c.High, c.Low, c.Close, c.VolumeUSD = ms(r[0]), f(r[1]), f(r[2]), f(r[3]), f(r[4]), f(r[7])
	}
	var oi struct {
		Data []map[string]string `json:"data"`
	}
	if e := o.get(ctx, "https://www.okx.com/api/v5/public/open-interest?instType=SWAP&instId="+inst, &oi); e == nil && len(oi.Data) > 0 {
		c.OpenInterestUSD = f(oi.Data[0]["oiUsd"])
	}
	var fr struct {
		Data []map[string]string `json:"data"`
	}
	if e := o.get(ctx, "https://www.okx.com/api/v5/public/funding-rate?instId="+inst, &fr); e == nil && len(fr.Data) > 0 {
		c.FundingRate = f(fr.Data[0]["fundingRate"])
	}
	return c, nil
}

const mathMaxInt = int64(^uint64(0) >> 1)

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func anyString(v any) string {
	if v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	default:
		return fmt.Sprint(v)
	}
}
func firstErr(a, b error) error {
	if a != nil {
		return a
	}
	return b
}
func reverseCandles(s []domain.Candle) {
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
}
func mergeOI(cs []domain.Candle, rows []oiRow) {
	j := 0
	for i := range cs {
		for j+1 < len(rows) && rows[j+1].Timestamp <= cs[i].Time.UnixMilli() {
			j++
		}
		if len(rows) > 0 && rows[j].Timestamp <= cs[i].Time.UnixMilli() {
			cs[i].OpenInterestUSD = f(rows[j].Value)
		}
	}
}

func mergeMetric(cs []domain.Candle, points []metricPoint, set func(*domain.Candle, float64)) {
	if len(points) == 0 {
		return
	}
	sort.Slice(points, func(i, j int) bool { return points[i].Timestamp < points[j].Timestamp })
	j := 0
	for i := range cs {
		for j+1 < len(points) && points[j+1].Timestamp <= cs[i].Time.UnixMilli() {
			j++
		}
		if points[j].Timestamp <= cs[i].Time.UnixMilli() {
			set(&cs[i], points[j].Value)
		}
	}
}
