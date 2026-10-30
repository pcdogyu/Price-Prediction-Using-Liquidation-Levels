package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotbinance"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotconfig"
	tvbotgate "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotgate"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotokx"
	tvbottrading "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

func TestPositionMonitorOKXStartsLimitCloseAtThresholds(t *testing.T) {
	restore := isolatePositionCloseJobsForTest()
	defer restore()

	srv := newTestServer(t)
	var mu sync.Mutex
	var orders []map[string]any
	okxServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v5/account/positions":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[
				{"instType":"SWAP","instId":"BTC-USDT-SWAP","mgnMode":"isolated","posSide":"long","pos":"2","availPos":"2","avgPx":"100","markPx":"106","upl":"12","uplRatio":"0.06","lever":"5","notionalUsd":"212","margin":"42.4"},
				{"instType":"SWAP","instId":"ETH-USDT-SWAP","mgnMode":"isolated","posSide":"short","pos":"3","availPos":"3","avgPx":"100","markPx":"109","upl":"-27","uplRatio":"-0.09","lever":"5","notionalUsd":"327","margin":"65.4"},
				{"instType":"SWAP","instId":"DOGE-USDT-SWAP","mgnMode":"isolated","posSide":"long","pos":"100","availPos":"100","avgPx":"0.1","markPx":"0.101","upl":"0.1","uplRatio":"0.01","lever":"5","notionalUsd":"10.1","margin":"2.02"},
				{"instType":"SWAP","instId":"ALGO-USDT-SWAP","mgnMode":"isolated","posSide":"net","pos":"-1098","availPos":"1098","avgPx":"0.0800","markPx":"0.0794","upl":"6.588","lever":"10","notionalUsd":"870.85","margin":"87.84"}
			]}`))
		case "/api/v5/public/instruments":
			_, _ = w.Write([]byte(fmt.Sprintf(`{"code":"0","msg":"","data":[{"instId":%q,"tickSz":"0.1","ctVal":"1","lotSz":"1","minSz":"1"}]}`, r.URL.Query().Get("instId"))))
		case "/api/v5/market/ticker":
			_, _ = w.Write([]byte(fmt.Sprintf(`{"code":"0","msg":"","data":[{"instId":%q,"bidPx":"99.9","askPx":"100.1","last":"100","ts":"1784880000000"}]}`, r.URL.Query().Get("instId"))))
		case "/api/v5/trade/orders-pending", "/api/v5/trade/orders-algo-pending":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[]}`))
		case "/api/v5/trade/order":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			orders = append(orders, body)
			mu.Unlock()
			_, _ = w.Write([]byte(fmt.Sprintf(`{"code":"0","msg":"","data":[{"ordId":"auto-%d","clOrdId":"close","sCode":"0","sMsg":""}]}`, len(orders))))
		default:
			t.Fatalf("unexpected OKX path %s", r.URL.Path)
		}
	}))
	defer okxServer.Close()

	cfg := srv.ConfigStore.Get()
	cfg.Trading.BaseURL = okxServer.URL
	cfg.Trading.PositionMonitor.OKXEnabled = true
	cfg.Trading.PositionMonitor.BinanceEnabled = false
	cfg.Trading.PositionMonitor.PollIntervalSeconds = 300
	cfg.Trading.PositionMonitor.TakeProfitPct = 5
	cfg.Trading.PositionMonitor.StopLossPct = 8
	srv.ConfigStore = config.NewStore("", cfg)
	srv.OKXHTTPClient = okxServer.Client()
	if _, err := srv.OKXCredentials.UpdateAccount(okx.CredentialAccountUpdate{
		ID:          "default",
		Active:      true,
		Credentials: okx.Credentials{APIKey: "key", SecretKey: "secret", Passphrase: "pass"},
	}); err != nil {
		t.Fatal(err)
	}

	srv.scanPositionMonitor(context.Background())

	mu.Lock()
	defer mu.Unlock()
	if len(orders) != 3 {
		t.Fatalf("expected three OKX auto close orders, got %#v", orders)
	}
	if orders[0]["instId"] != "BTC-USDT-SWAP" || orders[0]["side"] != "sell" || orders[0]["ordType"] != "limit" || orders[0]["sz"] != "2" || orders[0]["posSide"] != "long" {
		t.Fatalf("bad OKX take-profit close order: %#v", orders[0])
	}
	if orders[1]["instId"] != "ETH-USDT-SWAP" || orders[1]["side"] != "buy" || orders[1]["ordType"] != "limit" || orders[1]["sz"] != "3" || orders[1]["posSide"] != "short" {
		t.Fatalf("bad OKX stop-loss close order: %#v", orders[1])
	}
	if orders[2]["instId"] != "ALGO-USDT-SWAP" || orders[2]["side"] != "buy" || orders[2]["ordType"] != "limit" || orders[2]["sz"] != "1098" {
		t.Fatalf("bad OKX margin-derived take-profit close order: %#v", orders[2])
	}
	blocks, err := srv.Orders.ListActiveCoinpairBlocks(srv.now())
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 || blocks[0].Keyword != "ETH" || blocks[0].TriggerPrice != "109" || blocks[0].Source != "position_monitor" {
		t.Fatalf("OKX stop-loss cooldown not recorded correctly: %#v", blocks)
	}
}

func TestPositionMonitorBinanceStartsLimitCloseAtThresholds(t *testing.T) {
	restore := isolatePositionCloseJobsForTest()
	defer restore()

	srv := newTestServer(t)
	var mu sync.Mutex
	var orderForms []url.Values
	binanceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/fapi/v1/exchangeInfo" && r.URL.Path != "/fapi/v1/ticker/bookTicker" && r.Header.Get("X-MBX-APIKEY") != "binance-key" {
			t.Fatalf("missing Binance API key for %s", r.URL.Path)
		}
		switch r.URL.Path {
		case "/fapi/v3/positionRisk":
			_, _ = w.Write([]byte(`[
				{"symbol":"BTCUSDT","positionSide":"BOTH","positionAmt":"2","entryPrice":"100","markPrice":"106","unRealizedProfit":"12","notional":"212","marginAsset":"USDT","leverage":"5","marginType":"isolated","updateTime":1784880000000},
				{"symbol":"ETHUSDT","positionSide":"BOTH","positionAmt":"-3","entryPrice":"100","markPrice":"109","unRealizedProfit":"-27","notional":"-327","marginAsset":"USDT","leverage":"5","marginType":"isolated","updateTime":1784880000000},
				{"symbol":"DOGEUSDT","positionSide":"BOTH","positionAmt":"100","entryPrice":"0.1","markPrice":"0.101","unRealizedProfit":"0.1","notional":"10.1","marginAsset":"USDT","leverage":"5","marginType":"isolated","updateTime":1784880000000}
			]`))
		case "/fapi/v1/exchangeInfo":
			_, _ = w.Write([]byte(`{"symbols":[
				{"symbol":"BTCUSDT","status":"TRADING","pricePrecision":1,"quantityPrecision":3,"filters":[{"filterType":"PRICE_FILTER","tickSize":"0.1"},{"filterType":"LOT_SIZE","minQty":"0.001","stepSize":"0.001"}]},
				{"symbol":"ETHUSDT","status":"TRADING","pricePrecision":1,"quantityPrecision":3,"filters":[{"filterType":"PRICE_FILTER","tickSize":"0.1"},{"filterType":"LOT_SIZE","minQty":"0.001","stepSize":"0.001"}]}
			]}`))
		case "/fapi/v1/ticker/bookTicker":
			symbol := r.URL.Query().Get("symbol")
			_, _ = w.Write([]byte(fmt.Sprintf(`{"symbol":%q,"bidPrice":"99.9","bidQty":"1","askPrice":"100.1","askQty":"1"}`, symbol)))
		case "/fapi/v1/openOrders", "/fapi/v1/openAlgoOrders":
			_, _ = w.Write([]byte(`[]`))
		case "/fapi/v1/order":
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			orderForms = append(orderForms, cloneValues(r.Form))
			idx := len(orderForms)
			mu.Unlock()
			_, _ = w.Write([]byte(fmt.Sprintf(`{"orderId":%d,"clientOrderId":%q,"symbol":%q,"status":"NEW","price":%q,"origQty":%q,"executedQty":"0","type":"LIMIT","side":%q,"positionSide":"BOTH"}`,
				idx,
				r.Form.Get("newClientOrderId"),
				r.Form.Get("symbol"),
				r.Form.Get("price"),
				r.Form.Get("quantity"),
				r.Form.Get("side"),
			)))
		default:
			t.Fatalf("unexpected Binance path %s", r.URL.Path)
		}
	}))
	defer binanceServer.Close()

	cfg := srv.ConfigStore.Get()
	cfg.Trading.BinanceDemoBaseURL = binanceServer.URL
	cfg.Trading.PositionMonitor.OKXEnabled = false
	cfg.Trading.PositionMonitor.BinanceEnabled = true
	cfg.Trading.PositionMonitor.PollIntervalSeconds = 300
	cfg.Trading.PositionMonitor.TakeProfitPct = 5
	cfg.Trading.PositionMonitor.StopLossPct = 8
	srv.ConfigStore = config.NewStore("", cfg)
	srv.BinanceHTTPClient = binanceServer.Client()
	if _, err := srv.BinanceCredentials.UpdateAccount(binance.CredentialAccountUpdate{
		ID:          "main",
		Active:      true,
		Credentials: binance.Credentials{APIKey: "binance-key", SecretKey: "binance-secret"},
	}); err != nil {
		t.Fatal(err)
	}

	srv.scanPositionMonitor(context.Background())

	mu.Lock()
	defer mu.Unlock()
	if len(orderForms) != 2 {
		t.Fatalf("expected two Binance auto close orders, got %#v", orderForms)
	}
	if orderForms[0].Get("symbol") != "BTCUSDT" || orderForms[0].Get("side") != "SELL" || orderForms[0].Get("type") != "LIMIT" || orderForms[0].Get("quantity") != "2" || orderForms[0].Get("reduceOnly") != "true" {
		t.Fatalf("bad Binance take-profit close order: %#v", orderForms[0])
	}
	if orderForms[1].Get("symbol") != "ETHUSDT" || orderForms[1].Get("side") != "BUY" || orderForms[1].Get("type") != "LIMIT" || orderForms[1].Get("quantity") != "3" || orderForms[1].Get("reduceOnly") != "true" {
		t.Fatalf("bad Binance stop-loss close order: %#v", orderForms[1])
	}
	blocks, err := srv.Orders.ListActiveCoinpairBlocks(srv.now())
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 || blocks[0].Keyword != "ETH" || blocks[0].TriggerPrice != "109" || blocks[0].Source != "position_monitor" {
		t.Fatalf("Binance stop-loss cooldown not recorded correctly: %#v", blocks)
	}
}

func TestPositionMonitorGateSubmitsFullMarketCloseAtThresholds(t *testing.T) {
	restore := isolatePositionCloseJobsForTest()
	defer restore()

	srv := newTestServer(t)
	var mu sync.Mutex
	var orders []map[string]any
	positionOpen := map[string]bool{"BTC_USDT": true, "ETH_USDT": true}
	pending := map[string]bool{"BTC_USDT": true, "ETH_USDT": true}
	var canceledContracts []string
	cancelDone := make(chan string, 2)
	gateServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("KEY") != "gate-key" {
			t.Fatalf("missing Gate API key for %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v4/futures/usdt/positions":
			mu.Lock()
			btcOpen, ethOpen := positionOpen["BTC_USDT"], positionOpen["ETH_USDT"]
			mu.Unlock()
			positions := "["
			if btcOpen {
				positions += `{"contract":"BTC_USDT","size":"2","entry_price":"100","mark_price":"106","unrealised_pnl":"12","margin":"10"},`
			}
			if ethOpen {
				positions += `{"contract":"ETH_USDT","size":"-3","entry_price":"100","mark_price":"109","unrealised_pnl":"-27","margin":"10"},`
			}
			positions += `{"contract":"DOGE_USDT","size":"1","entry_price":"0.1","mark_price":"0.101","unrealised_pnl":"0.1","margin":"10"},`
			positions += `{"contract":"SOL_USDT","size":"0","entry_price":"100","mark_price":"100","unrealised_pnl":"0","margin":"10"}]`
			_, _ = w.Write([]byte(positions))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v4/futures/usdt/orders":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			orders = append(orders, body)
			if contract, ok := body["contract"].(string); ok {
				positionOpen[contract] = false
			}
			idx := len(orders)
			mu.Unlock()
			_, _ = w.Write([]byte(fmt.Sprintf(`{"id":"gate-auto-%d","contract":%q,"text":%q}`, idx, body["contract"], body["text"])))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v4/futures/usdt/orders":
			contract := r.URL.Query().Get("contract")
			mu.Lock()
			open := pending[contract]
			mu.Unlock()
			if open {
				_, _ = w.Write([]byte(fmt.Sprintf(`[{"id":"pending-%s","contract":%q,"size":"1","left":"1","status":"open"}]`, strings.ToLower(contract), contract)))
			} else {
				_, _ = w.Write([]byte(`[]`))
			}
		case r.Method == http.MethodGet && r.URL.Path == "/api/v4/futures/usdt/price_orders":
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/v4/futures/usdt/orders/"):
			contract := r.URL.Query().Get("contract")
			mu.Lock()
			pending[contract] = false
			canceledContracts = append(canceledContracts, contract)
			mu.Unlock()
			cancelDone <- contract
			_, _ = w.Write([]byte(fmt.Sprintf(`{"id":%q,"contract":%q,"status":"finished"}`, strings.TrimPrefix(r.URL.Path, "/api/v4/futures/usdt/orders/"), contract)))
		default:
			t.Fatalf("unexpected Gate path %s", r.URL.Path)
		}
	}))
	defer gateServer.Close()

	cfg := srv.ConfigStore.Get()
	cfg.Trading.GateDemoBaseURL = gateServer.URL
	cfg.Trading.PositionMonitor.OKXEnabled = false
	cfg.Trading.PositionMonitor.BinanceEnabled = false
	cfg.Trading.PositionMonitor.GateEnabled = true
	cfg.Trading.PositionMonitor.PollIntervalSeconds = 300
	cfg.Trading.PositionMonitor.TakeProfitPct = 5
	cfg.Trading.PositionMonitor.StopLossPct = 8
	srv.ConfigStore = config.NewStore("", cfg)
	srv.GateHTTPClient = gateServer.Client()
	gateCredentials, err := tvbotgate.NewCredentialStore(filepath.Join(t.TempDir(), "gate-credentials.json"), tvbotgate.Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	srv.GateCredentials = gateCredentials
	if _, err := gateCredentials.UpdateAccount(tvbotgate.CredentialAccountUpdate{
		ID: "gate-main", Active: true,
		Credentials: tvbotgate.Credentials{APIKey: "gate-key", SecretKey: "gate-secret"},
	}); err != nil {
		t.Fatal(err)
	}

	srv.scanPositionMonitor(context.Background())
	for range 2 {
		select {
		case <-cancelDone:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for Gate auto-close order cleanup")
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(orders) != 2 {
		t.Fatalf("expected two Gate auto close orders, got %#v", orders)
	}
	seenContracts := map[string]bool{}
	for _, order := range orders {
		contract, _ := order["contract"].(string)
		seenContracts[contract] = true
		if order["size"] != "0" || order["price"] != "0" || order["tif"] != "ioc" || order["close"] != true || order["reduce_only"] != true {
			t.Fatalf("bad Gate full market close order: %#v", order)
		}
		if text, _ := order["text"].(string); strings.TrimSpace(text) == "" {
			t.Fatalf("Gate close order missing client text: %#v", order)
		}
	}
	if !seenContracts["BTC_USDT"] || !seenContracts["ETH_USDT"] || seenContracts["DOGE_USDT"] || seenContracts["SOL_USDT"] {
		t.Fatalf("unexpected Gate close contracts: %#v", seenContracts)
	}
	if len(canceledContracts) != 2 || pending["BTC_USDT"] || pending["ETH_USDT"] {
		t.Fatalf("Gate auto-close cleanup did not cancel both contracts: canceled=%#v pending=%#v", canceledContracts, pending)
	}
	blocks, err := srv.Orders.ListActiveCoinpairBlocks(srv.now())
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 || blocks[0].Keyword != "ETH" || blocks[0].TriggerPrice != "109" || blocks[0].Source != "position_monitor" || blocks[0].Exchange != tvbottrading.ExchangeGate {
		t.Fatalf("Gate stop-loss cooldown not recorded correctly: %#v", blocks)
	}
}

func TestPositionMonitorGateLiveGuardSkipsAutoClose(t *testing.T) {
	restore := isolatePositionCloseJobsForTest()
	defer restore()

	srv := newTestServer(t)
	var orders int
	gateServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v4/futures/usdt/orders" {
			orders++
			t.Fatal("Gate live guard should prevent auto close orders")
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/v4/futures/usdt/positions" {
			t.Fatalf("unexpected Gate path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`[{"contract":"BTC_USDT","size":"2","entry_price":"100","mark_price":"106","unrealised_pnl":"12","margin":"10"}]`))
	}))
	defer gateServer.Close()

	cfg := srv.ConfigStore.Get()
	cfg.Trading.Env = config.EnvLive
	cfg.Trading.AllowLiveTrading = false
	cfg.Trading.GateBaseURL = gateServer.URL
	cfg.Trading.PositionMonitor.GateEnabled = true
	cfg.Trading.PositionMonitor.PollIntervalSeconds = 300
	cfg.Trading.PositionMonitor.TakeProfitPct = 5
	cfg.Trading.PositionMonitor.StopLossPct = 8
	srv.ConfigStore = config.NewStore("", cfg)
	srv.GateHTTPClient = gateServer.Client()
	gateCredentials, err := tvbotgate.NewCredentialStore(filepath.Join(t.TempDir(), "gate-live-credentials.json"), tvbotgate.Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	srv.GateLiveCredentials = gateCredentials
	if _, err := gateCredentials.UpdateAccount(tvbotgate.CredentialAccountUpdate{
		ID: "gate-live", Active: true,
		Credentials: tvbotgate.Credentials{APIKey: "gate-key", SecretKey: "gate-secret"},
	}); err != nil {
		t.Fatal(err)
	}

	srv.scanPositionMonitor(context.Background())
	if orders != 0 {
		t.Fatalf("expected no Gate orders, got %d", orders)
	}
}

func TestPositionMonitorBinanceLiveGuardSkipsAutoClose(t *testing.T) {
	restore := isolatePositionCloseJobsForTest()
	defer restore()

	srv := newTestServer(t)
	var orders int
	binanceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/fapi/v3/positionRisk":
			_, _ = w.Write([]byte(`[{"symbol":"BTCUSDT","positionSide":"BOTH","positionAmt":"2","entryPrice":"100","markPrice":"106","unRealizedProfit":"12","notional":"212","marginAsset":"USDT","leverage":"5","marginType":"isolated","updateTime":1784880000000}]`))
		case "/fapi/v1/order":
			orders++
			t.Fatal("Binance live guard should prevent auto close orders")
		default:
			t.Fatalf("unexpected Binance path %s", r.URL.Path)
		}
	}))
	defer binanceServer.Close()

	cfg := srv.ConfigStore.Get()
	cfg.Trading.Env = config.EnvLive
	cfg.Trading.AllowLiveTrading = false
	cfg.Trading.BinanceBaseURL = binanceServer.URL
	cfg.Trading.PositionMonitor.BinanceEnabled = true
	cfg.Trading.PositionMonitor.PollIntervalSeconds = 300
	cfg.Trading.PositionMonitor.TakeProfitPct = 5
	cfg.Trading.PositionMonitor.StopLossPct = 8
	srv.ConfigStore = config.NewStore("", cfg)
	srv.BinanceHTTPClient = binanceServer.Client()
	if _, err := srv.BinanceCredentials.UpdateAccount(binance.CredentialAccountUpdate{
		ID:          "main",
		Active:      true,
		Credentials: binance.Credentials{APIKey: "binance-key", SecretKey: "binance-secret"},
	}); err != nil {
		t.Fatal(err)
	}

	srv.scanPositionMonitor(context.Background())
	if orders != 0 {
		t.Fatalf("expected no Binance orders, got %d", orders)
	}
}

func isolatePositionCloseJobsForTest() func() {
	oldPoll := positionClosePollInterval
	oldTimeout := positionCloseLimitTimeout
	oldJobs := positionCloseJobs
	oldGateOperations := gateContractOperationJobs
	positionClosePollInterval = time.Hour
	positionCloseLimitTimeout = time.Hour
	positionCloseJobs = newPositionCloseRegistry()
	gateContractOperationJobs = newGateContractOperationRegistry()
	return func() {
		positionClosePollInterval = oldPoll
		positionCloseLimitTimeout = oldTimeout
		positionCloseJobs = oldJobs
		gateContractOperationJobs = oldGateOperations
	}
}
