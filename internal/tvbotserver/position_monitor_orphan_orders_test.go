package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	binance "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotbinance"
	config "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotconfig"
	gate "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotgate"
	okx "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotokx"
)

func TestPositionMonitorCancelsOrphanOKXOrders(t *testing.T) {
	srv := newTestServer(t)
	var mu sync.Mutex
	normalOpen, algoOpen := true, true
	var canceledNormal, canceledAlgo bool
	okxServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v5/account/positions":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instType":"SWAP","instId":"ETH-USDT-SWAP","mgnMode":"isolated","posSide":"long","pos":"2","avgPx":"100","markPx":"100","upl":"0","uplRatio":"0.01","lever":"5","notionalUsd":"200","margin":"40"}]}`))
		case "/api/v5/trade/orders-pending":
			mu.Lock()
			open := normalOpen
			mu.Unlock()
			if open {
				_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instId":"BTC-USDT-SWAP","ordId":"orphan-normal","clOrdId":"orphan-normal-client","state":"live"}]}`))
			} else {
				_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[]}`))
			}
		case "/api/v5/trade/orders-algo-pending":
			mu.Lock()
			open := algoOpen
			mu.Unlock()
			if open {
				_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instId":"BTC-USDT-SWAP","algoId":"orphan-algo","algoClOrdId":"orphan-algo-client","ordType":"conditional","state":"live","tpTriggerPx":"105","side":"sell"}]}`))
			} else {
				_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[]}`))
			}
		case "/api/v5/trade/cancel-order":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["ordId"] != "orphan-normal" {
				t.Fatalf("unexpected OKX normal cancellation: %#v", body)
			}
			mu.Lock()
			normalOpen, canceledNormal = false, true
			mu.Unlock()
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"ordId":"orphan-normal","sCode":"0","sMsg":""}]}`))
		case "/api/v5/trade/cancel-algos":
			var body []map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if len(body) != 1 || body[0]["algoId"] != "orphan-algo" {
				t.Fatalf("unexpected OKX algorithm cancellation: %#v", body)
			}
			mu.Lock()
			algoOpen, canceledAlgo = false, true
			mu.Unlock()
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"algoId":"orphan-algo","sCode":"0","sMsg":""}]}`))
		default:
			t.Fatalf("unexpected OKX path %s", r.URL.Path)
		}
	}))
	defer okxServer.Close()

	cfg := srv.ConfigStore.Get()
	cfg.Trading.BaseURL = okxServer.URL
	cfg.Trading.PositionMonitor.OKXEnabled = true
	cfg.Trading.PositionMonitor.BinanceEnabled = false
	cfg.Trading.PositionMonitor.GateEnabled = false
	srv.ConfigStore = config.NewStore("", cfg)
	srv.OKXHTTPClient = okxServer.Client()
	if _, err := srv.OKXCredentials.UpdateAccount(okx.CredentialAccountUpdate{
		ID: "default", Active: true,
		Credentials: okx.Credentials{APIKey: "key", SecretKey: "secret", Passphrase: "pass"},
	}); err != nil {
		t.Fatal(err)
	}

	srv.scanPositionMonitor(context.Background())

	mu.Lock()
	defer mu.Unlock()
	if !canceledNormal || !canceledAlgo || normalOpen || algoOpen {
		t.Fatalf("OKX orphan orders were not fully canceled: normal=%v/%v algo=%v/%v", canceledNormal, normalOpen, canceledAlgo, algoOpen)
	}
}

func TestPositionMonitorCancelsOrphanBinanceOrders(t *testing.T) {
	srv := newTestServer(t)
	var mu sync.Mutex
	normalOpen, algoOpen := true, true
	var canceledNormal, canceledAlgo bool
	binanceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/fapi/v3/positionRisk":
			_, _ = w.Write([]byte(`[{"symbol":"ETHUSDT","positionSide":"BOTH","positionAmt":"2","entryPrice":"100","markPrice":"100","unRealizedProfit":"0","notional":"200","marginAsset":"USDT","leverage":"5","marginType":"isolated","updateTime":1784880000000}]`))
		case "/fapi/v1/openOrders":
			mu.Lock()
			open := normalOpen
			mu.Unlock()
			if open {
				_, _ = w.Write([]byte(`[{"orderId":101,"symbol":"BTCUSDT","clientOrderId":"orphan-normal","status":"NEW","price":"105","origQty":"1","executedQty":"0","type":"LIMIT","side":"SELL","positionSide":"BOTH"}]`))
			} else {
				_, _ = w.Write([]byte(`[]`))
			}
		case "/fapi/v1/openAlgoOrders":
			mu.Lock()
			open := algoOpen
			mu.Unlock()
			if open {
				_, _ = w.Write([]byte(`[{"algoId":202,"clientAlgoId":"orphan-algo","symbol":"BTCUSDT","algoType":"CONDITIONAL","orderType":"TAKE_PROFIT_MARKET","side":"SELL","positionSide":"BOTH","quantity":"1","algoStatus":"NEW","reduceOnly":true,"triggerPrice":"105"}]`))
			} else {
				_, _ = w.Write([]byte(`[]`))
			}
		case "/fapi/v1/order":
			if r.Method != http.MethodDelete {
				t.Fatalf("unexpected Binance order method: %s", r.Method)
			}
			if r.URL.Query().Get("orderId") != "101" {
				t.Fatalf("unexpected Binance normal cancellation: %s", r.URL.RawQuery)
			}
			mu.Lock()
			normalOpen, canceledNormal = false, true
			mu.Unlock()
			_, _ = w.Write([]byte(`{"orderId":101,"symbol":"BTCUSDT","status":"CANCELED","clientOrderId":"orphan-normal"}`))
		case "/fapi/v1/algoOrder":
			if r.Method != http.MethodDelete || r.URL.Query().Get("algoId") != "202" {
				t.Fatalf("unexpected Binance algorithm cancellation: %s %s", r.Method, r.URL.RawQuery)
			}
			mu.Lock()
			algoOpen, canceledAlgo = false, true
			mu.Unlock()
			_, _ = w.Write([]byte(`{"algoId":202,"clientAlgoId":"orphan-algo","code":"200","msg":"success"}`))
		default:
			t.Fatalf("unexpected Binance path %s", r.URL.Path)
		}
	}))
	defer binanceServer.Close()

	cfg := srv.ConfigStore.Get()
	cfg.Trading.BinanceDemoBaseURL = binanceServer.URL
	cfg.Trading.PositionMonitor.OKXEnabled = false
	cfg.Trading.PositionMonitor.BinanceEnabled = true
	cfg.Trading.PositionMonitor.GateEnabled = false
	srv.ConfigStore = config.NewStore("", cfg)
	srv.BinanceHTTPClient = binanceServer.Client()
	if _, err := srv.BinanceCredentials.UpdateAccount(binance.CredentialAccountUpdate{
		ID: "main", Active: true,
		Credentials: binance.Credentials{APIKey: "binance-key", SecretKey: "binance-secret"},
	}); err != nil {
		t.Fatal(err)
	}

	srv.scanPositionMonitor(context.Background())

	mu.Lock()
	defer mu.Unlock()
	if !canceledNormal || !canceledAlgo || normalOpen || algoOpen {
		t.Fatalf("Binance orphan orders were not fully canceled: normal=%v/%v algo=%v/%v", canceledNormal, normalOpen, canceledAlgo, algoOpen)
	}
}

func TestPositionMonitorCancelsOrphanGateOrders(t *testing.T) {
	srv := newTestServer(t)
	var mu sync.Mutex
	normalOpen, algoOpen := true, true
	var canceledNormal, canceledAlgo bool
	gateServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v4/futures/usdt/positions":
			_, _ = w.Write([]byte(`[{"contract":"ETH_USDT","size":"2","entry_price":"100","mark_price":"100","unrealised_pnl":"0","margin":"10"}]`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v4/futures/usdt/orders":
			mu.Lock()
			open := normalOpen
			mu.Unlock()
			if open {
				_, _ = w.Write([]byte(`[{"id":301,"contract":"BTC_USDT","size":"1","left":"1","status":"open"}]`))
			} else {
				_, _ = w.Write([]byte(`[]`))
			}
		case r.Method == http.MethodGet && r.URL.Path == "/api/v4/futures/usdt/price_orders":
			mu.Lock()
			open := algoOpen
			mu.Unlock()
			if open {
				_, _ = w.Write([]byte(`[{"initial":{"contract":"BTC_USDT","size":"-1","price":"0","is_reduce_only":true,"is_close":true},"trigger":{"price":"105","rule":2},"id":302,"id_string":"302","status":"open"}]`))
			} else {
				_, _ = w.Write([]byte(`[]`))
			}
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v4/futures/usdt/orders/301":
			mu.Lock()
			normalOpen, canceledNormal = false, true
			mu.Unlock()
			_, _ = w.Write([]byte(`{"id":301,"contract":"BTC_USDT","status":"finished"}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v4/futures/usdt/price_orders":
			if r.URL.Query().Get("contract") != "BTC_USDT" {
				t.Fatalf("unexpected Gate algorithm cancellation query: %s", r.URL.RawQuery)
			}
			mu.Lock()
			algoOpen, canceledAlgo = false, true
			mu.Unlock()
			_, _ = w.Write([]byte(`[]`))
		default:
			t.Fatalf("unexpected Gate request %s %s", r.Method, r.URL.String())
		}
	}))
	defer gateServer.Close()

	cfg := srv.ConfigStore.Get()
	cfg.Trading.GateDemoBaseURL = gateServer.URL
	cfg.Trading.PositionMonitor.OKXEnabled = false
	cfg.Trading.PositionMonitor.BinanceEnabled = false
	cfg.Trading.PositionMonitor.GateEnabled = true
	srv.ConfigStore = config.NewStore("", cfg)
	srv.GateHTTPClient = gateServer.Client()
	credentials, err := gate.NewCredentialStore(filepath.Join(t.TempDir(), "gate-credentials.json"), gate.Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	srv.GateCredentials = credentials
	if _, err := credentials.UpdateAccount(gate.CredentialAccountUpdate{
		ID: "gate-main", Active: true,
		Credentials: gate.Credentials{APIKey: "gate-key", SecretKey: "gate-secret"},
	}); err != nil {
		t.Fatal(err)
	}

	srv.scanPositionMonitor(context.Background())

	mu.Lock()
	defer mu.Unlock()
	if !canceledNormal || !canceledAlgo || normalOpen || algoOpen {
		t.Fatalf("Gate orphan orders were not fully canceled: normal=%v/%v algo=%v/%v", canceledNormal, normalOpen, canceledAlgo, algoOpen)
	}
}
