package exchange

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBinanceMarketMetricUsesBulkCaches(t *testing.T) {
	originalBase := binanceFuturesBase
	resetBinanceMarketInfoCaches()
	t.Cleanup(func() {
		binanceFuturesBase = originalBase
		resetBinanceMarketInfoCaches()
	})
	var premiumRequests, tickerRequests, bookRequests int32
	var openInterestRequests, accountRatioRequests, topRatioRequests int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/fapi/v1/premiumIndex":
			atomic.AddInt32(&premiumRequests, 1)
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"symbol": "BTCUSDT", "markPrice": "100", "lastFundingRate": "0.001", "nextFundingTime": time.Now().Add(time.Hour).UnixMilli()},
				{"symbol": "ETHUSDT", "markPrice": "50", "lastFundingRate": "0.002", "nextFundingTime": time.Now().Add(time.Hour).UnixMilli()},
			})
		case "/fapi/v1/ticker/24hr":
			atomic.AddInt32(&tickerRequests, 1)
			_ = json.NewEncoder(w).Encode([]map[string]string{
				{"symbol": "BTCUSDT", "priceChangePercent": "2", "quoteVolume": "1000", "highPrice": "110", "lowPrice": "90"},
				{"symbol": "ETHUSDT", "priceChangePercent": "-1", "quoteVolume": "500", "highPrice": "55", "lowPrice": "45"},
			})
		case "/fapi/v1/ticker/bookTicker":
			atomic.AddInt32(&bookRequests, 1)
			_ = json.NewEncoder(w).Encode([]map[string]string{
				{"symbol": "BTCUSDT", "bidPrice": "99", "askPrice": "101"},
				{"symbol": "ETHUSDT", "bidPrice": "49", "askPrice": "51"},
			})
		case "/fapi/v1/openInterest":
			atomic.AddInt32(&openInterestRequests, 1)
			_ = json.NewEncoder(w).Encode(map[string]string{"openInterest": "10"})
		case "/futures/data/globalLongShortAccountRatio":
			atomic.AddInt32(&accountRatioRequests, 1)
			_ = json.NewEncoder(w).Encode([]map[string]any{{"longShortRatio": "1.2", "timestamp": time.Now().Add(-time.Minute).UnixMilli()}})
		case "/futures/data/topLongShortPositionRatio":
			atomic.AddInt32(&topRatioRequests, 1)
			_ = json.NewEncoder(w).Encode([]map[string]any{{"longShortRatio": "1.3", "timestamp": time.Now().Add(-time.Minute).UnixMilli()}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	binanceFuturesBase = server.URL
	var wg sync.WaitGroup
	for _, symbol := range []string{"BTCUSDT", "ETHUSDT"} {
		wg.Add(1)
		go func(symbol string) {
			defer wg.Done()
			metric := BinanceMarketMetric(context.Background(), symbol)
			if len(metric.Warnings) != 0 {
				t.Errorf("%s warnings: %v", symbol, metric.Warnings)
			}
			if metric.MarkPrice == nil || metric.BestBid == nil || metric.High24h == nil {
				t.Errorf("%s missing bulk metric values: %+v", symbol, metric)
			}
		}(symbol)
	}
	wg.Wait()
	if got := atomic.LoadInt32(&premiumRequests); got != 1 {
		t.Fatalf("premiumIndex requests = %d, want 1", got)
	}
	if got := atomic.LoadInt32(&tickerRequests); got != 1 {
		t.Fatalf("24h ticker requests = %d, want 1", got)
	}
	if got := atomic.LoadInt32(&bookRequests); got != 1 {
		t.Fatalf("book ticker requests = %d, want 1", got)
	}
	if got := atomic.LoadInt32(&openInterestRequests); got != 2 {
		t.Fatalf("open interest requests = %d, want 2", got)
	}
	if got := atomic.LoadInt32(&accountRatioRequests); got != 2 {
		t.Fatalf("account ratio requests = %d, want 2", got)
	}
	if got := atomic.LoadInt32(&topRatioRequests); got != 2 {
		t.Fatalf("top ratio requests = %d, want 2", got)
	}
}

func TestBinanceCurrentReusesBulkPremiumCache(t *testing.T) {
	originalBase := binanceFuturesBase
	resetBinanceMarketInfoCaches()
	t.Cleanup(func() {
		binanceFuturesBase = originalBase
		resetBinanceMarketInfoCaches()
	})
	var premiumRequests int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/fapi/v1/premiumIndex":
			atomic.AddInt32(&premiumRequests, 1)
			_ = json.NewEncoder(w).Encode([]map[string]string{
				{"symbol": "BTCUSDT", "lastFundingRate": "0.001"},
				{"symbol": "ETHUSDT", "lastFundingRate": "0.002"},
			})
		case "/fapi/v1/klines":
			_ = json.NewEncoder(w).Encode([][]any{{float64(time.Now().UnixMilli()), "1", "2", "0.5", "1.5", "0", "0", "10", "0", "0", "5"}})
		case "/fapi/v1/openInterest":
			_ = json.NewEncoder(w).Encode(map[string]string{"openInterest": "10"})
		case "/futures/data/globalLongShortAccountRatio":
			_ = json.NewEncoder(w).Encode([]map[string]string{{"longShortRatio": "1.1"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	binanceFuturesBase = server.URL
	client := &Binance{newHTTP()}
	var wg sync.WaitGroup
	for _, symbol := range []string{"BTCUSDT", "ETHUSDT"} {
		wg.Add(1)
		go func(symbol string) {
			defer wg.Done()
			if _, err := client.Current(context.Background(), symbol); err != nil {
				t.Errorf("%s current candle failed: %v", symbol, err)
			}
		}(symbol)
	}
	wg.Wait()
	if got := atomic.LoadInt32(&premiumRequests); got != 1 {
		t.Fatalf("premiumIndex requests = %d, want 1 bulk request", got)
	}
}

func resetBinanceMarketInfoCaches() {
	binanceLiveMarketCache.Lock()
	binanceLiveMarketCache.premium = nil
	binanceLiveMarketCache.premiumAt = time.Time{}
	binanceLiveMarketCache.premiumLoad = nil
	binanceLiveMarketCache.premiumRetry = time.Time{}
	binanceLiveMarketCache.premiumErr = nil
	binanceLiveMarketCache.book = nil
	binanceLiveMarketCache.bookAt = time.Time{}
	binanceLiveMarketCache.bookLoad = nil
	binanceLiveMarketCache.bookRetry = time.Time{}
	binanceLiveMarketCache.bookErr = nil
	binanceLiveMarketCache.Unlock()
	binancePublicMetadataCache.Lock()
	binancePublicMetadataCache.tickers = nil
	binancePublicMetadataCache.tickersAt = time.Time{}
	binancePublicMetadataCache.tickerLoading = nil
	binancePublicMetadataCache.Unlock()
}
