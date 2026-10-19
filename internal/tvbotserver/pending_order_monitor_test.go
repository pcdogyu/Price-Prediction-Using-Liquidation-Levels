package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotbinance"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotconfig"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotokx"
)

func TestStalePendingOrderCancelOKXCancelsOnlyOldUnfilled(t *testing.T) {
	restore := isolatePendingOrderChaseJobsForTest()
	defer restore()

	srv := newTestServer(t)
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	srv.Now = func() time.Time { return now }
	var mu sync.Mutex
	var canceled []map[string]any
	okxServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v5/trade/orders-pending":
			_, _ = w.Write([]byte(fmt.Sprintf(`{"code":"0","msg":"","data":[
				{"instType":"SWAP","instId":"BTC-USDT-SWAP","ordId":"old-zero","side":"buy","posSide":"long","ordType":"limit","px":"100","sz":"1","accFillSz":"0","state":"live","cTime":"%d","uTime":"%d"},
				{"instType":"SWAP","instId":"ETH-USDT-SWAP","ordId":"young-zero","side":"buy","posSide":"long","ordType":"limit","px":"100","sz":"1","accFillSz":"0","state":"live","cTime":"%d","uTime":"%d"},
				{"instType":"SWAP","instId":"SOL-USDT-SWAP","ordId":"old-partial","side":"buy","posSide":"long","ordType":"limit","px":"100","sz":"1","accFillSz":"0.1","state":"live","cTime":"%d","uTime":"%d"}
			]}`,
				now.Add(-2*time.Hour-time.Minute).UnixMilli(), now.Add(-2*time.Hour-time.Minute).UnixMilli(),
				now.Add(-30*time.Minute).UnixMilli(), now.Add(-30*time.Minute).UnixMilli(),
				now.Add(-3*time.Hour).UnixMilli(), now.Add(-3*time.Hour).UnixMilli(),
			)))
		case "/api/v5/trade/cancel-order":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			canceled = append(canceled, body)
			mu.Unlock()
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"ordId":"old-zero","clOrdId":"","sCode":"0","sMsg":""}]}`))
		default:
			t.Fatalf("unexpected OKX path %s", r.URL.Path)
		}
	}))
	defer okxServer.Close()

	cfg := srv.ConfigStore.Get()
	cfg.Trading.BaseURL = okxServer.URL
	srv.ConfigStore = config.NewStore("", cfg)
	srv.OKXHTTPClient = okxServer.Client()
	if _, err := srv.OKXCredentials.UpdateAccount(okx.CredentialAccountUpdate{
		ID:          "default",
		Active:      true,
		Credentials: okx.Credentials{APIKey: "key", SecretKey: "secret", Passphrase: "pass"},
	}); err != nil {
		t.Fatal(err)
	}

	srv.cancelStalePendingOrders(context.Background())

	mu.Lock()
	defer mu.Unlock()
	if len(canceled) != 1 {
		t.Fatalf("expected one OKX stale pending order cancellation, got %#v", canceled)
	}
	if canceled[0]["instId"] != "BTC-USDT-SWAP" || canceled[0]["ordId"] != "old-zero" {
		t.Fatalf("bad OKX cancel request: %#v", canceled[0])
	}
}

func TestStalePendingOrderCancelBinanceCancelsOnlyOldUnfilled(t *testing.T) {
	restore := isolatePendingOrderChaseJobsForTest()
	defer restore()

	srv := newTestServer(t)
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	srv.Now = func() time.Time { return now }
	var mu sync.Mutex
	var canceled []url.Values
	binanceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("X-MBX-APIKEY") != "binance-key" {
			t.Fatalf("missing Binance API key for %s", r.URL.Path)
		}
		switch r.URL.Path {
		case "/fapi/v1/openOrders":
			_, _ = w.Write([]byte(fmt.Sprintf(`[
				{"symbol":"BTCUSDT","orderId":1001,"clientOrderId":"old-zero","side":"BUY","positionSide":"BOTH","type":"LIMIT","price":"100","origQty":"1","executedQty":"0","status":"NEW","time":%d,"updateTime":%d},
				{"symbol":"ETHUSDT","orderId":1002,"clientOrderId":"young-zero","side":"BUY","positionSide":"BOTH","type":"LIMIT","price":"100","origQty":"1","executedQty":"0","status":"NEW","time":%d,"updateTime":%d},
				{"symbol":"SOLUSDT","orderId":1003,"clientOrderId":"old-partial","side":"BUY","positionSide":"BOTH","type":"LIMIT","price":"100","origQty":"1","executedQty":"0.1","status":"NEW","time":%d,"updateTime":%d}
			]`,
				now.Add(-2*time.Hour-time.Minute).UnixMilli(), now.Add(-2*time.Hour-time.Minute).UnixMilli(),
				now.Add(-30*time.Minute).UnixMilli(), now.Add(-30*time.Minute).UnixMilli(),
				now.Add(-3*time.Hour).UnixMilli(), now.Add(-3*time.Hour).UnixMilli(),
			)))
		case "/fapi/v1/order":
			if r.Method != http.MethodDelete {
				t.Fatalf("expected DELETE cancel, got %s", r.Method)
			}
			mu.Lock()
			canceled = append(canceled, cloneValues(r.URL.Query()))
			mu.Unlock()
			_, _ = w.Write([]byte(`{"orderId":1001,"clientOrderId":"old-zero","symbol":"BTCUSDT","status":"CANCELED","price":"100","origQty":"1","executedQty":"0","type":"LIMIT","side":"BUY","positionSide":"BOTH"}`))
		default:
			t.Fatalf("unexpected Binance path %s", r.URL.Path)
		}
	}))
	defer binanceServer.Close()

	cfg := srv.ConfigStore.Get()
	cfg.Trading.BinanceDemoBaseURL = binanceServer.URL
	srv.ConfigStore = config.NewStore("", cfg)
	srv.BinanceHTTPClient = binanceServer.Client()
	if _, err := srv.BinanceCredentials.UpdateAccount(binance.CredentialAccountUpdate{
		ID:          "main",
		Active:      true,
		Credentials: binance.Credentials{APIKey: "binance-key", SecretKey: "binance-secret"},
	}); err != nil {
		t.Fatal(err)
	}

	srv.cancelStalePendingOrders(context.Background())

	mu.Lock()
	defer mu.Unlock()
	if len(canceled) != 1 {
		t.Fatalf("expected one Binance stale pending order cancellation, got %#v", canceled)
	}
	if canceled[0].Get("symbol") != "BTCUSDT" || canceled[0].Get("orderId") != "1001" {
		t.Fatalf("bad Binance cancel request: %#v", canceled[0])
	}
}

func TestStalePendingOrderCancelBinanceLiveGuardSkipsCancel(t *testing.T) {
	srv := newTestServer(t)
	var requests int
	binanceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		t.Fatalf("Binance live guard should skip stale pending order requests, got %s", r.URL.Path)
	}))
	defer binanceServer.Close()

	cfg := srv.ConfigStore.Get()
	cfg.Trading.Env = config.EnvLive
	cfg.Trading.AllowLiveTrading = false
	cfg.Trading.BinanceBaseURL = binanceServer.URL
	srv.ConfigStore = config.NewStore("", cfg)
	srv.BinanceHTTPClient = binanceServer.Client()
	if _, err := srv.BinanceCredentials.UpdateAccount(binance.CredentialAccountUpdate{
		ID:          "main",
		Active:      true,
		Credentials: binance.Credentials{APIKey: "binance-key", SecretKey: "binance-secret"},
	}); err != nil {
		t.Fatal(err)
	}

	srv.cancelStalePendingOrders(context.Background())
	if requests != 0 {
		t.Fatalf("expected no Binance requests, got %d", requests)
	}
}

func TestStaleUnfilledPendingOrderAge(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	old := strconv.FormatInt(now.Add(-2*time.Hour-time.Second).UnixMilli(), 10)
	young := strconv.FormatInt(now.Add(-time.Hour).UnixMilli(), 10)
	tests := []struct {
		name  string
		order okx.PendingOrder
		stale bool
	}{
		{name: "old unfilled", order: okx.PendingOrder{CTime: old, AccFillSz: "0"}, stale: true},
		{name: "young unfilled", order: okx.PendingOrder{CTime: young, AccFillSz: "0"}, stale: false},
		{name: "old partial", order: okx.PendingOrder{CTime: old, AccFillSz: "0.1"}, stale: false},
		{name: "fallback update time", order: okx.PendingOrder{UTime: old, AccFillSz: "0"}, stale: true},
		{name: "missing fill size", order: okx.PendingOrder{CTime: old}, stale: false},
		{name: "missing time", order: okx.PendingOrder{AccFillSz: "0"}, stale: false},
	}
	for _, tt := range tests {
		_, stale := staleUnfilledPendingOrderAge(tt.order, now, 2*time.Hour)
		if stale != tt.stale {
			t.Fatalf("%s stale=%v want %v", tt.name, stale, tt.stale)
		}
	}
}

func isolatePendingOrderChaseJobsForTest() func() {
	oldJobs := pendingOrderChaseJobs
	pendingOrderChaseJobs = newPendingOrderChaseRegistry()
	return func() {
		pendingOrderChaseJobs = oldJobs
	}
}
