package exchange

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

func number(value string) *float64 {
	v, e := strconv.ParseFloat(value, 64)
	if e != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	return &v
}
func EstimateNetPosition(oi, ratio *float64) *float64 {
	if oi == nil || ratio == nil || *oi < 0 || *ratio <= 0 {
		return nil
	}
	v := *oi * (*ratio - 1) / (*ratio + 1)
	return &v
}
func BinanceMarketMetric(ctx context.Context, symbol string) domain.MarketMetric {
	m := domain.MarketMetric{Symbol: symbol, Time: time.Now().UTC().Truncate(time.Minute), Warnings: []string{}}
	h := newHTTP()
	get := func(path string, target any) bool {
		if err := h.get(ctx, "https://fapi.binance.com"+path, target); err != nil {
			m.Warnings = append(m.Warnings, err.Error())
			return false
		}
		return true
	}
	var premium struct {
		Mark    string `json:"markPrice"`
		Funding string `json:"lastFundingRate"`
		Next    int64  `json:"nextFundingTime"`
	}
	if get("/fapi/v1/premiumIndex?symbol="+symbol, &premium) {
		m.MarkPrice = number(premium.Mark)
		m.FundingRate = number(premium.Funding)
		if premium.Next > 0 {
			t := time.UnixMilli(premium.Next).UTC()
			m.NextFunding = &t
		}
	}
	var ticker struct {
		Change string `json:"priceChangePercent"`
		Volume string `json:"quoteVolume"`
		High   string `json:"highPrice"`
		Low    string `json:"lowPrice"`
	}
	if get("/fapi/v1/ticker/24hr?symbol="+symbol, &ticker) {
		m.Change24h = number(ticker.Change)
		if m.Change24h != nil {
			*m.Change24h /= 100
		}
		m.Volume24hUSD = number(ticker.Volume)
		m.High24h = number(ticker.High)
		m.Low24h = number(ticker.Low)
	}
	var book struct {
		Bid string `json:"bidPrice"`
		Ask string `json:"askPrice"`
	}
	if get("/fapi/v1/ticker/bookTicker?symbol="+symbol, &book) {
		m.BestBid = number(book.Bid)
		m.BestAsk = number(book.Ask)
	}
	var oi struct {
		Quantity string `json:"openInterest"`
	}
	if get("/fapi/v1/openInterest?symbol="+symbol, &oi) {
		m.OIQuantity = number(oi.Quantity)
		if m.OIQuantity != nil && m.MarkPrice != nil {
			v := *m.OIQuantity * *m.MarkPrice
			m.OIUSD = &v
		}
	}
	for _, top := range []bool{false, true} {
		path := "globalLongShortAccountRatio"
		if top {
			path = "topLongShortPositionRatio"
		}
		var ratios []struct {
			Ratio     string `json:"longShortRatio"`
			Timestamp int64  `json:"timestamp"`
		}
		if get("/futures/data/"+path+"?symbol="+symbol+"&period=5m&limit=1", &ratios) && len(ratios) > 0 && time.Since(time.UnixMilli(ratios[0].Timestamp)) < 10*time.Minute {
			v := number(ratios[0].Ratio)
			if top {
				m.TopPositionRatio = v
			} else {
				m.AccountRatio = v
			}
		} else {
			m.Warnings = append(m.Warnings, path+": 数据缺失或超过 10 分钟")
		}
	}
	m.NetPositionUSD = EstimateNetPosition(m.OIUSD, m.TopPositionRatio)
	return m
}

func BinanceMetricHistory(ctx context.Context, symbol string, sink func(context.Context, domain.MarketMetric) error) error {
	h := newHTTP()
	until := time.Now().UTC().Truncate(5 * time.Minute)
	from := until.AddDate(0, 0, -30)
	for _, kind := range []string{"openInterestHist", "globalLongShortAccountRatio", "topLongShortPositionRatio"} {
		for start := from; start.Before(until); {
			end := start.Add(499 * 5 * time.Minute)
			if end.After(until) {
				end = until
			}
			var rows []struct {
				Time     int64  `json:"timestamp"`
				OI       string `json:"sumOpenInterestValue"`
				Quantity string `json:"sumOpenInterest"`
				Ratio    string `json:"longShortRatio"`
			}
			url := fmt.Sprintf("https://fapi.binance.com/futures/data/%s?symbol=%s&period=5m&limit=500&startTime=%d&endTime=%d", kind, symbol, start.UnixMilli(), end.UnixMilli())
			if err := h.get(ctx, url, &rows); err != nil {
				return err
			}
			if len(rows) == 0 {
				break
			}
			for _, r := range rows {
				m := domain.MarketMetric{Symbol: symbol, Time: time.UnixMilli(r.Time).UTC()}
				switch kind {
				case "openInterestHist":
					m.OIUSD = number(r.OI)
					m.OIQuantity = number(r.Quantity)
				case "globalLongShortAccountRatio":
					m.AccountRatio = number(r.Ratio)
				case "topLongShortPositionRatio":
					m.TopPositionRatio = number(r.Ratio)
				}
				if err := sink(ctx, m); err != nil {
					return err
				}
			}
			next := time.UnixMilli(rows[len(rows)-1].Time).UTC().Add(time.Millisecond)
			if !next.After(start) {
				break
			}
			start = next
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(150 * time.Millisecond):
			}
		}
	}
	return nil
}

type OptionContract struct {
	Symbol     string  `json:"symbol"`
	Underlying string  `json:"underlying"`
	Side       string  `json:"side"`
	Strike     string  `json:"strikePrice"`
	Unit       float64 `json:"unit"`
	Expiry     int64   `json:"expiryDate"`
	Status     string  `json:"status"`
}
type OptionExposure struct {
	Contract OptionContract
	Gamma    float64
	OI       float64
}

func BuildGamma(symbol string, spot float64, rows []OptionExposure, at time.Time) domain.GammaView {
	g := domain.GammaView{Symbol: symbol, State: "ok", Time: at, SpotPrice: spot, Levels: []domain.GammaLevel{}, Expiries: []domain.GammaExpiry{}, Warnings: []string{}, Method: "Binance Options Gamma × OI × 合约单位 × 标的指数价格² × 1%；CALL 正、PUT 负。公开期权链估算，不代表做市商真实净仓。"}
	levels := map[float64]domain.GammaLevel{}
	expiries := map[string]domain.GammaExpiry{}
	for _, r := range rows {
		strike := f(r.Contract.Strike)
		if strike <= 0 || r.Contract.Unit <= 0 || r.Gamma < 0 || r.OI < 0 {
			continue
		}
		v := r.Gamma * r.OI * r.Contract.Unit * spot * spot * .01
		sign := 1.
		if r.Contract.Side == "PUT" {
			sign = -1
		}
		l := levels[strike]
		l.Strike = strike
		if sign > 0 {
			l.CallOI += r.OI * r.Contract.Unit
		} else {
			l.PutOI += r.OI * r.Contract.Unit
		}
		l.NetGEXUSD += v * sign
		l.AbsoluteGEXUSD += v
		levels[strike] = l
		exp := time.UnixMilli(r.Contract.Expiry).UTC().Format("2006-01-02")
		e := expiries[exp]
		e.Expiry = exp
		e.Contracts++
		e.NetGEXUSD += v * sign
		expiries[exp] = e
		g.Contracts++
		g.NetGEXUSD += v * sign
		g.AbsoluteGEXUSD += v
	}
	maxExposure := 0.
	for _, l := range levels {
		g.Levels = append(g.Levels, l)
		if l.AbsoluteGEXUSD > maxExposure || (l.AbsoluteGEXUSD == maxExposure && g.GammaWall != nil && l.Strike < *g.GammaWall) {
			v := l.Strike
			g.GammaWall = &v
			maxExposure = l.AbsoluteGEXUSD
		}
	}
	for _, e := range expiries {
		g.Expiries = append(g.Expiries, e)
	}
	sort.Slice(g.Levels, func(i, j int) bool { return g.Levels[i].Strike < g.Levels[j].Strike })
	sort.Slice(g.Expiries, func(i, j int) bool { return g.Expiries[i].Expiry < g.Expiries[j].Expiry })
	if g.Contracts == 0 {
		g.State = "unavailable"
	}
	return g
}
func BinanceGamma(ctx context.Context, symbol string) (domain.GammaView, error) {
	h := newHTTP()
	at := time.Now().UTC()
	var meta struct {
		Symbols []OptionContract `json:"optionSymbols"`
	}
	if err := h.get(ctx, "https://eapi.binance.com/eapi/v1/exchangeInfo", &meta); err != nil {
		return domain.GammaView{}, err
	}
	contracts := []OptionContract{}
	expirations := map[string]bool{}
	for _, c := range meta.Symbols {
		if c.Underlying == symbol && c.Status == "TRADING" && c.Expiry > at.UnixMilli() {
			contracts = append(contracts, c)
			expirations[time.UnixMilli(c.Expiry).UTC().Format("060102")] = true
		}
	}
	var index struct {
		Price string `json:"indexPrice"`
	}
	if err := h.get(ctx, "https://eapi.binance.com/eapi/v1/index?underlying="+symbol, &index); err != nil {
		return domain.GammaView{}, err
	}
	spot := f(index.Price)
	if spot <= 0 {
		return domain.GammaView{}, fmt.Errorf("invalid option index for %s", symbol)
	}
	var marks []struct {
		Symbol string `json:"symbol"`
		Gamma  string `json:"gamma"`
	}
	if err := h.get(ctx, "https://eapi.binance.com/eapi/v1/mark", &marks); err != nil {
		return domain.GammaView{}, err
	}
	gammas := map[string]float64{}
	for _, m := range marks {
		if v := number(m.Gamma); v != nil {
			gammas[m.Symbol] = *v
		}
	}
	oi := map[string]float64{}
	warnings := []string{}
	for exp := range expirations {
		var rows []struct {
			Symbol string `json:"symbol"`
			OI     string `json:"sumOpenInterest"`
			Time   string `json:"timestamp"`
		}
		url := "https://eapi.binance.com/eapi/v1/openInterest?underlyingAsset=" + strings.TrimSuffix(symbol, "USDT") + "&expiration=" + exp
		if err := h.get(ctx, url, &rows); err != nil {
			warnings = append(warnings, "到期日 "+exp+" 持仓量不可用")
			continue
		}
		for _, r := range rows {
			if v := number(r.OI); v != nil {
				stamp, _ := strconv.ParseInt(r.Time, 10, 64)
				if stamp > 0 && at.Sub(time.UnixMilli(stamp)) < 15*time.Minute {
					oi[r.Symbol] = *v
				}
			}
		}
	}
	rows := []OptionExposure{}
	for _, c := range contracts {
		gamma, gm := gammas[c.Symbol]
		quantity, ok := oi[c.Symbol]
		if gm && ok {
			rows = append(rows, OptionExposure{c, gamma, quantity})
		}
	}
	g := BuildGamma(symbol, spot, rows, at)
	g.ExpectedContracts = len(contracts)
	g.Warnings = warnings
	if len(rows) < len(contracts) {
		g.Warnings = append(g.Warnings, fmt.Sprintf("期权链不完整：%d/%d 个合约", len(rows), len(contracts)))
		if g.Contracts > 0 {
			g.State = "partial"
		}
	}
	return g, nil
}
