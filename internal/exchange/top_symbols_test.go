package exchange

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func ticker(symbol, quoteVolume string) topSymbolTicker {
	return topSymbolTicker{Symbol: symbol, QuoteVolume: quoteVolume, Volume: "1", LastPrice: "1", Change: "0"}
}

// TestRankUSDTOrdersByQuoteVolume covers the ranking rule that decides the alert watch
// list, including the filters that keep delivery contracts and dead tickers out.
func TestRankUSDTOrdersByQuoteVolume(t *testing.T) {
	at := time.Date(2026, 10, 14, 3, 0, 0, 0, time.UTC)
	tradable := map[string]string{"BTCUSDT": "BTC", "ETHUSDT": "ETH", "SOLUSDT": "SOL", "ADAUSDT": "ADA", "DOGEUSDT": "DOGE"}
	tickers := []topSymbolTicker{
		ticker("ETHUSDT", "4000000000"),
		ticker("btcusdt", "9000000000"), // Binance uses upper case; the filter must not depend on it.
		ticker("SOLUSDT", "1000000000"),
		ticker("ADAUSDT", "0"),    // no volume today, cannot be ranked
		ticker("DOGEUSDT", "bad"), // unparsable volume is treated as zero
		ticker("BTCUSDT_230630", "8000000000"),
		ticker("BTCBUSD", "7000000000"),
		ticker("USDT", "5000000000"),
	}
	rows := rankUSDT(tickers, tradable, 3, at)
	if len(rows) != 3 {
		t.Fatalf("expected the top three pairs, got %+v", rows)
	}
	if rows[0].Symbol != "BTCUSDT" || rows[1].Symbol != "ETHUSDT" || rows[2].Symbol != "SOLUSDT" {
		t.Fatalf("ranking must follow quote volume: %+v", rows)
	}
	for i, row := range rows {
		if row.Rank != i+1 {
			t.Fatalf("ranks must be sequential from one: %+v", rows)
		}
		if !row.RefreshedAt.Equal(at) {
			t.Fatalf("the refresh time must be recorded: %+v", row)
		}
	}
	if rows[0].BaseAsset != "BTC" {
		t.Fatalf("the base asset must come from exchange info: %+v", rows[0])
	}
	if names := TopSymbolNames(rows); len(names) != 3 || names[0] != "BTCUSDT" {
		t.Fatalf("unexpected name list: %v", names)
	}
}

func TestRankConfiguredUSDTFiltersBeforeApplyingLimit(t *testing.T) {
	rows := []TopSymbol{
		{Symbol: "BTCUSDT", QuoteVolumeUSD: 100},
		{Symbol: "ETHUSDT", QuoteVolumeUSD: 90},
		{Symbol: "SOLUSDT", QuoteVolumeUSD: 80},
	}
	got := rankConfiguredUSDT(rows, map[string]struct{}{"ETHUSDT": {}, "SOLUSDT": {}}, 1)
	if len(got) != 1 || got[0].Symbol != "ETHUSDT" || got[0].Rank != 1 {
		t.Fatalf("configured ranking=%+v", got)
	}
}

func TestRankConfiguredUSDTDoesNotAddAlwaysWatch(t *testing.T) {
	rows := rankConfiguredUSDT([]TopSymbol{{Symbol: "SOLUSDT", QuoteVolumeUSD: 80}}, map[string]struct{}{"SOLUSDT": {}}, 50)
	if len(rows) != 1 || rows[0].Symbol != "SOLUSDT" {
		t.Fatalf("configured ranking unexpectedly added symbols=%+v", rows)
	}
}

// TestRankUSDTKeepsAlwaysWatch proves BTC and ETH cannot drop out of the watch list,
// because the alert thresholds are defined for them.
func TestRankUSDTKeepsAlwaysWatch(t *testing.T) {
	at := time.Now().UTC()
	tickers := []topSymbolTicker{
		ticker("SOLUSDT", "9000000000"),
		ticker("XRPUSDT", "8000000000"),
		ticker("DOGEUSDT", "7000000000"),
	}
	rows := rankUSDT(tickers, nil, 2, at)
	if len(rows) != 4 {
		t.Fatalf("top two plus the two required symbols expected: %+v", rows)
	}
	seen := map[string]bool{}
	for _, row := range rows {
		seen[row.Symbol] = true
	}
	for _, required := range AlwaysWatch {
		if !seen[required] {
			t.Fatalf("%s must always stay on the watch list: %+v", required, rows)
		}
	}
	// The list stays ordered by volume, so the required pairs rank last with no volume.
	if rows[0].Symbol != "SOLUSDT" || rows[len(rows)-1].QuoteVolumeUSD != 0 {
		t.Fatalf("unexpected order: %+v", rows)
	}
	// Without exchange info the suffix filter still keeps the ranking usable, and the
	// required pairs are appended on top of the ranked ones.
	if got := rankUSDT(tickers, map[string]string{}, 0, at); len(got) != len(tickers)+len(AlwaysWatch) {
		t.Fatalf("an empty tradable map must disable the filter instead of dropping everything: %+v", got)
	}
}

// TestTradableUSDTFilter documents which exchange info entries qualify, so a delivery
// contract or a non-USDT quote never reaches the alert engine.
func TestTradableUSDTFilter(t *testing.T) {
	info := topSymbolExchangeInfo{Symbols: []topSymbolEntry{
		{Symbol: "BTCUSDT", BaseAsset: "btc", QuoteAsset: "USDT", ContractType: "PERPETUAL", Status: "TRADING", Permission: "MARKET"},
		{Symbol: "BTCUSDT_230630", BaseAsset: "BTC", QuoteAsset: "USDT", ContractType: "CURRENT_QUARTER", Status: "TRADING", Permission: "MARKET"},
		{Symbol: "BTCBUSD", BaseAsset: "BTC", QuoteAsset: "BUSD", ContractType: "PERPETUAL", Status: "TRADING", Permission: "MARKET"},
		{Symbol: "ETHUSDT", BaseAsset: "ETH", QuoteAsset: "USDT", ContractType: "PERPETUAL", Status: "SETTLING", Permission: "MARKET"},
		{Symbol: "SOLUSDT", BaseAsset: "SOL", QuoteAsset: "USDT", ContractType: "PERPETUAL", Status: "TRADING", Permission: "ABSENT"},
	}}
	got := tradableUSDT(info)
	if len(got) != 1 || got["BTCUSDT"] != "BTC" {
		t.Fatalf("only the tradable USDT perpetual must survive: %+v", got)
	}
}

func TestBinanceUSDTPerpetualSymbolsReturnsSortedCatalog(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/fapi/v1/exchangeInfo" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"symbols":[
			{"symbol":"ETHUSDT","baseAsset":"ETH","quoteAsset":"USDT","contractType":"PERPETUAL","status":"TRADING","permissionType":"MARKET"},
			{"symbol":"BTCUSDT_2609","baseAsset":"BTC","quoteAsset":"USDT","contractType":"CURRENT_MONTH","status":"TRADING","permissionType":"MARKET"},
			{"symbol":"BTCUSDT","baseAsset":"BTC","quoteAsset":"USDT","contractType":"PERPETUAL","status":"TRADING","permissionType":"MARKET"},
			{"symbol":"XRPBUSD","baseAsset":"XRP","quoteAsset":"BUSD","contractType":"PERPETUAL","status":"TRADING","permissionType":"MARKET"},
			{"symbol":"SOLUSDT","baseAsset":"SOL","quoteAsset":"USDT","contractType":"PERPETUAL","status":"SETTLING","permissionType":"MARKET"}
		]}`))
	}))
	defer server.Close()
	original := binanceFuturesBase
	binanceFuturesBase = server.URL
	t.Cleanup(func() { binanceFuturesBase = original })

	rows, err := BinanceUSDTPerpetualSymbols(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Symbol != "BTCUSDT" || rows[1].Symbol != "ETHUSDT" || rows[0].BaseAsset != "BTC" {
		t.Fatalf("unexpected catalog: %+v", rows)
	}
}

// TestBinanceTopUSDTPerpetualsRefusesEmptyResponses keeps a broken upstream from
// producing a silently empty watch list.
func TestBinanceTopUSDTPerpetualsRefusesEmptyResponses(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := BinanceTopUSDTPerpetuals(cancelled, 0); err == nil {
		t.Fatal("a cancelled context must fail instead of returning a fabricated list")
	}
}
