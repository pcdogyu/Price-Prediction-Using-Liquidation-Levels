package exchange

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
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

// ContractSymbol describes one currently tradable Binance USDⓈ-M perpetual
// contract. The bubble page uses this catalog for selection; it deliberately
// does not imply that the contract is part of the background APP_SYMBOLS
// monitoring set.
type ContractSymbol struct {
	Symbol    string `json:"symbol"`
	BaseAsset string `json:"base_asset"`
}

// AlwaysWatch stays on the alert watch list regardless of volume rank, because
// the alert thresholds are defined for BTC and ETH explicitly.
var AlwaysWatch = []string{"BTCUSDT", "ETHUSDT"}

var errNoTopSymbols = errors.New("binance returned no rankable USDT perpetual ticker")

const (
	binanceExchangeInfoCacheTTL = 30 * time.Minute
	binanceTickerCacheTTL       = 30 * time.Second
)

var binancePublicMetadataCache struct {
	sync.Mutex
	info          topSymbolExchangeInfo
	infoAt        time.Time
	infoLoading   chan struct{}
	tickers       []topSymbolTicker
	tickersAt     time.Time
	tickerLoading chan struct{}
}

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
		if !strings.HasSuffix(symbol, "USDT") || !strings.EqualFold(strings.TrimSpace(s.QuoteAsset), "USDT") {
			continue
		}
		if s.ContractType != "" && !strings.EqualFold(strings.TrimSpace(s.ContractType), "PERPETUAL") {
			continue
		}
		if s.Status != "" && !strings.EqualFold(strings.TrimSpace(s.Status), "TRADING") {
			continue
		}
		if s.Permission != "" && !strings.EqualFold(strings.TrimSpace(s.Permission), "MARKET") {
			continue
		}
		out[symbol] = strings.ToUpper(s.BaseAsset)
	}
	return out
}

// BinanceUSDTPerpetualSymbols returns the current exchange-wide selection
// catalog used by the bubble page. The endpoint is public and the result is
// intentionally not used to expand background streams or model training.
func BinanceUSDTPerpetualSymbols(ctx context.Context) ([]ContractSymbol, error) {
	info, err := cachedBinanceExchangeInfo(ctx)
	if err != nil {
		return nil, err
	}
	tradable := tradableUSDT(info)
	rows := make([]ContractSymbol, 0, len(tradable))
	for symbol, base := range tradable {
		rows = append(rows, ContractSymbol{Symbol: symbol, BaseAsset: base})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Symbol < rows[j].Symbol })
	if len(rows) == 0 {
		return nil, errNoTopSymbols
	}
	return rows, nil
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
	tradable := map[string]string{}
	// exchangeInfo only refines the tradable filter, so its failure is not fatal.
	if info, err := cachedBinanceExchangeInfo(ctx); err == nil {
		tradable = tradableUSDT(info)
	}
	tickers, err := cachedBinanceTickers(ctx)
	if err != nil {
		return nil, err
	}
	rows := rankUSDT(tickers, tradable, limit, time.Now())
	if len(rows) == 0 {
		return nil, errNoTopSymbols
	}
	return rows, nil
}

func cachedBinanceExchangeInfo(ctx context.Context) (topSymbolExchangeInfo, error) {
	for {
		binancePublicMetadataCache.Lock()
		if time.Since(binancePublicMetadataCache.infoAt) < binanceExchangeInfoCacheTTL && len(binancePublicMetadataCache.info.Symbols) > 0 {
			value := binancePublicMetadataCache.info
			binancePublicMetadataCache.Unlock()
			return value, nil
		}
		if binancePublicMetadataCache.infoLoading != nil {
			loading := binancePublicMetadataCache.infoLoading
			binancePublicMetadataCache.Unlock()
			select {
			case <-ctx.Done():
				return topSymbolExchangeInfo{}, ctx.Err()
			case <-loading:
				continue
			}
		}
		binancePublicMetadataCache.infoLoading = make(chan struct{})
		loading := binancePublicMetadataCache.infoLoading
		binancePublicMetadataCache.Unlock()

		var value topSymbolExchangeInfo
		err := newHTTP().get(ctx, binanceFuturesBase+"/fapi/v1/exchangeInfo", &value)
		binancePublicMetadataCache.Lock()
		if err == nil {
			binancePublicMetadataCache.info = value
			binancePublicMetadataCache.infoAt = time.Now().UTC()
		}
		close(loading)
		binancePublicMetadataCache.infoLoading = nil
		binancePublicMetadataCache.Unlock()
		return value, err
	}
}

func cachedBinanceTickers(ctx context.Context) ([]topSymbolTicker, error) {
	for {
		binancePublicMetadataCache.Lock()
		if time.Since(binancePublicMetadataCache.tickersAt) < binanceTickerCacheTTL && len(binancePublicMetadataCache.tickers) > 0 {
			value := append([]topSymbolTicker(nil), binancePublicMetadataCache.tickers...)
			binancePublicMetadataCache.Unlock()
			return value, nil
		}
		if binancePublicMetadataCache.tickerLoading != nil {
			loading := binancePublicMetadataCache.tickerLoading
			binancePublicMetadataCache.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-loading:
				continue
			}
		}
		binancePublicMetadataCache.tickerLoading = make(chan struct{})
		loading := binancePublicMetadataCache.tickerLoading
		binancePublicMetadataCache.Unlock()

		var value []topSymbolTicker
		err := newHTTP().get(ctx, "https://fapi.binance.com/fapi/v1/ticker/24hr", &value)
		binancePublicMetadataCache.Lock()
		if err == nil {
			binancePublicMetadataCache.tickers = append([]topSymbolTicker(nil), value...)
			binancePublicMetadataCache.tickersAt = time.Now().UTC()
		}
		close(loading)
		binancePublicMetadataCache.tickerLoading = nil
		binancePublicMetadataCache.Unlock()
		return value, err
	}
}

// TopSymbolNames returns the symbol list of a ranked set.
func TopSymbolNames(rows []TopSymbol) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Symbol)
	}
	return out
}
