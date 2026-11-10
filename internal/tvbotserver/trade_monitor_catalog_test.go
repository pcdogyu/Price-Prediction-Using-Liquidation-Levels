package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	binance "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotbinance"
)

func TestCachedBinanceTradeMonitorCatalogFiltersAndFallsBack(t *testing.T) {
	resetBinanceTradeMonitorCatalogCacheForTest()
	defer resetBinanceTradeMonitorCatalogCacheForTest()
	var exchangeInfoRequests int32
	var fail atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/fapi/v1/exchangeInfo" {
			http.NotFound(w, r)
			return
		}
		atomic.AddInt32(&exchangeInfoRequests, 1)
		if fail.Load() {
			http.Error(w, "temporary failure", http.StatusBadGateway)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"symbols": []map[string]string{
			{"symbol": "BTCUSDT", "quoteAsset": "USDT", "contractType": "PERPETUAL", "status": "TRADING"},
			{"symbol": "ETHUSDT", "quoteAsset": "USDT", "contractType": "PERPETUAL", "status": "TRADING"},
			{"symbol": "CAPUSDT", "quoteAsset": "USDT", "contractType": "PERPETUAL", "status": "BREAK"},
			{"symbol": "BTCUSD", "quoteAsset": "USD", "contractType": "PERPETUAL", "status": "TRADING"},
		}})
	}))
	defer server.Close()
	client := binance.Client{BaseURL: server.URL, HTTPClient: server.Client()}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			snapshot, err := cachedBinanceTradeMonitorCatalog(t.Context(), client)
			if err != nil {
				t.Errorf("catalog load failed: %v", err)
				return
			}
			filtered := filterBinanceTradeMonitorSymbols([]string{"BTCUSDT", "CAPUSDT", "BTCUSD"}, snapshot)
			if len(filtered) != 1 || filtered[0] != "BTCUSDT" {
				t.Errorf("filtered symbols = %v, want [BTCUSDT]", filtered)
			}
		}()
	}
	wg.Wait()
	if got := atomic.LoadInt32(&exchangeInfoRequests); got != 1 {
		t.Fatalf("exchangeInfo requests = %d, want 1", got)
	}
	fail.Store(true)
	expireBinanceTradeMonitorCatalogForTest(server.URL)
	snapshot, err := cachedBinanceTradeMonitorCatalog(t.Context(), client)
	if err != nil || len(snapshot.symbols) == 0 {
		t.Fatalf("stale catalog fallback failed: snapshot=%#v err=%v", snapshot, err)
	}
	markBinanceTradeMonitorSymbolInvalid(client, "BTCUSDT")
	if filtered := currentFilteredTradeMonitorSymbols(client); len(filtered) != 1 || filtered[0] != "ETHUSDT" {
		t.Fatalf("negative cache filtered symbols = %v, want [ETHUSDT]", filtered)
	}
	fail.Store(false)
	expireBinanceTradeMonitorCatalogForTest(server.URL)
	snapshot, err = cachedBinanceTradeMonitorCatalog(t.Context(), client)
	if err != nil {
		t.Fatalf("catalog refresh failed: %v", err)
	}
	if filtered := filterBinanceTradeMonitorSymbols([]string{"BTCUSDT", "ETHUSDT"}, snapshot); len(filtered) != 2 {
		t.Fatalf("refreshed catalog filtered symbols = %v, want both symbols", filtered)
	}
	if got := atomic.LoadInt32(&exchangeInfoRequests); got != 3 {
		t.Fatalf("exchangeInfo requests after fallback/refresh = %d, want 3", got)
	}
}

func currentFilteredTradeMonitorSymbols(client binance.Client) []string {
	binanceTradeMonitorCatalogCache.Lock()
	defer binanceTradeMonitorCatalogCache.Unlock()
	return filterBinanceTradeMonitorSymbols([]string{"BTCUSDT", "ETHUSDT"}, cloneBinanceTradeMonitorCatalog(binanceTradeMonitorCatalogCache.entries[client.BaseURL]))
}

func expireBinanceTradeMonitorCatalogForTest(baseURL string) {
	binanceTradeMonitorCatalogCache.Lock()
	if entry := binanceTradeMonitorCatalogCache.entries[baseURL]; entry != nil {
		entry.fetchedAt = time.Now().Add(-binanceTradeMonitorCatalogTTL - time.Minute)
		entry.retryAt = time.Time{}
	}
	binanceTradeMonitorCatalogCache.Unlock()
}

func resetBinanceTradeMonitorCatalogCacheForTest() {
	binanceTradeMonitorCatalogCache.Lock()
	binanceTradeMonitorCatalogCache.entries = map[string]*binanceTradeMonitorCatalogEntry{}
	binanceTradeMonitorCatalogCache.Unlock()
}
