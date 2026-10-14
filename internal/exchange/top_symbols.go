package exchange

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"
)

// TopSymbol is one Binance USD-margined perpetual pair ranked by rolling 24 hour
// quote volume. It drives which symbols the liquidation alert engine watches.
type TopSymbol struct {
	Symbol         string    `json:"symbol"`
	BaseAsset      string    `json:"base_asset"`
	QuoteVolumeUSD float64   `json:"quote_volume_usd"`
	TradeVolume    float64   `json:"trade_volume"`
	ChangePercent  float64   `json:"change_percent"`
	LastPrice      float64   `json:"last_price"`
	Rank           int       `json:"rank"`
	RefreshedAt    time.Time `json:"refreshed_at"`
}

// AlwaysWatch stays on the alert watch list regardless of volume rank, because
// the alert thresholds are defined for BTC and ETH explicitly.
var AlwaysWatch = []string{"BTCUSDT", "ETHUSDT"}

var errNoTopSymbols = errors.New("binance returned no rankable USDT perpetual ticker")

type topSymbolEntry struct {
	Symbol       string `json:"symbol"`
	BaseAsset    string `json:"baseAsset"`
	QuoteAsset   string `json:"quoteAsset"`
	ContractType string `json:"contractType"`
	Status       string `json:"status"`
	Permission   string `json:"permissionType"`
}

type topSymbolExchangeInfo struct {
	Symbols []topSymbolEntry `json:"symbols"`
}

type topSymbolTicker struct {
	Symbol      string `json:"symbol"`
	QuoteVolume string `json:"quoteVolume"`
	Volume      string `json:"volume"`
	LastPrice   string `json:"lastPrice"`
	Change      string `json:"priceChangePercent"`
}

// tradableUSDT returns the USDT quoted perpetual symbols Binance currently
// allows trading. Optional fields are only honored when present so delisted and
// delivery contracts drop out without a hardcoded list.
func tradableUSDT(info topSymbolExchangeInfo) map[string]string {
	out := map[string]string{}
	for _, s := range info.Symbols {
		symbol := strings.ToUpper(strings.TrimSpace(s.Symbol))
		if !strings.HasSuffix(symbol, "USDT") || s.QuoteAsset != "USDT" {
			continue
		}
		if s.ContractType != "" && s.ContractType != "PERPETUAL" {
			continue
		}
		if s.Status != "" && s.Status != "TRADING" {
			continue
		}
		if s.Permission != "" && s.Permission != "MARKET" {
			continue
		}
		out[symbol] = strings.ToUpper(s.BaseAsset)
	}
	return out
}

// rankUSDT turns raw 24 hour tickers into ranked USDT perpetual entries. It is
// pure so the ranking rule is testable without a network call.
func rankUSDT(tickers []topSymbolTicker, tradable map[string]string, limit int, at time.Time) []TopSymbol {
	rows := make([]TopSymbol, 0, len(tickers))
	for _, t := range tickers {
		symbol := strings.ToUpper(strings.TrimSpace(t.Symbol))
		if !strings.HasSuffix(symbol, "USDT") || symbol == "USDT" {
			continue
		}
		if len(tradable) > 0 {
			if _, ok := tradable[symbol]; !ok {
				continue
			}
		}
		quote := f(t.QuoteVolume)
		if quote <= 0 {
			continue
		}
		rows = append(rows, TopSymbol{Symbol: symbol, BaseAsset: tradable[symbol], QuoteVolumeUSD: quote, TradeVolume: f(t.Volume), ChangePercent: f(t.Change), LastPrice: f(t.LastPrice)})
	}
	rows = sortTopSymbols(rows)
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	// AlwaysWatch is re-added after the cut so BTCUSDT and ETHUSDT survive a
	// volume collapse, then the list is re-ordered and re-ranked.
	rows = appendAlwaysWatch(rows, at)
	for i := range rows {
		rows[i].Rank = i + 1
		rows[i].RefreshedAt = at.UTC()
	}
	return rows
}

// appendAlwaysWatch keeps BTCUSDT and ETHUSDT present even when their 24 hour
// volume dropped out of the ranking, then restores volume order.
func appendAlwaysWatch(rows []TopSymbol, at time.Time) []TopSymbol {
	for _, required := range AlwaysWatch {
		found := false
		for _, r := range rows {
			if r.Symbol == required {
				found = true
				break
			}
		}
		if !found {
			rows = append(rows, TopSymbol{Symbol: required, BaseAsset: strings.TrimSuffix(required, "USDT"), RefreshedAt: at.UTC()})
		}
	}
	sortTopSymbols(rows)
	return rows
}

func sortTopSymbols(rows []TopSymbol) []TopSymbol {
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].QuoteVolumeUSD != rows[j].QuoteVolumeUSD {
			return rows[i].QuoteVolumeUSD > rows[j].QuoteVolumeUSD
		}
		return rows[i].Symbol < rows[j].Symbol
	})
	return rows
}

// BinanceTopUSDTPerpetuals ranks Binance USDT quoted perpetuals by rolling 24
// hour quote volume and returns the top limit entries plus AlwaysWatch. Both
// endpoints are public; a failure is returned instead of falling back to a stale
// hardcoded list so the alert page can show the real data state.
func BinanceTopUSDTPerpetuals(ctx context.Context, limit int) ([]TopSymbol, error) {
	if limit <= 0 {
		limit = 50
	}
	h := newHTTP()
	var info topSymbolExchangeInfo
	tradable := map[string]string{}
	// exchangeInfo only refines the tradable filter, so its failure is not fatal.
	if err := h.get(ctx, "https://fapi.binance.com/fapi/v1/exchangeInfo", &info); err == nil {
		tradable = tradableUSDT(info)
	}
	var tickers []topSymbolTicker
	if err := h.get(ctx, "https://fapi.binance.com/fapi/v1/ticker/24hr", &tickers); err != nil {
		return nil, err
	}
	rows := rankUSDT(tickers, tradable, limit, time.Now())
	if len(rows) == 0 {
		return nil, errNoTopSymbols
	}
	return rows, nil
}

// TopSymbolNames returns the symbol list of a ranked set.
func TopSymbolNames(rows []TopSymbol) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Symbol)
	}
	return out
}
