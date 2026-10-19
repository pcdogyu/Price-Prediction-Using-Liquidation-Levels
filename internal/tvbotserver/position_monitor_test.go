package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotbinance"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotconfig"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotokx"
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
	positionClosePollInterval = time.Hour
	positionCloseLimitTimeout = time.Hour
	positionCloseJobs = newPositionCloseRegistry()
	return func() {
		positionClosePollInterval = oldPoll
		positionCloseLimitTimeout = oldTimeout
		positionCloseJobs = oldJobs
	}
}
