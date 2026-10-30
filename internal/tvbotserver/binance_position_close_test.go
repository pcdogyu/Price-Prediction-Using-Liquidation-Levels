package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotbinance"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotconfig"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotokx"
)

func TestBinancePositionToOKXDerivesLeverageAndNotional(t *testing.T) {
	position := binancePositionToOKX(binance.Position{
		Symbol:                "DOGEUSDT",
		PositionSide:          "BOTH",
		PositionAmt:           "2500",
		EntryPrice:            "0.1",
		MarkPrice:             "0.12",
		UnRealizedProfit:      "50",
		Leverage:              "0",
		MarginType:            "isolated",
		LiquidationPrice:      "0.08",
		MarginAsset:           "USDT",
		IsolatedMargin:        "37",
		PositionInitialMargin: "30",
	})
	if position.NotionalUsd != "300" || position.Lever != "10" || position.Margin != "37" {
		t.Fatalf("expected derived notional/leverage, got %#v", position)
	}
}

func TestBinanceConflictingReduceOnlyCloseOrderIncludesConditionalOrders(t *testing.T) {
	req := binance.PlaceOrderRequest{Symbol: "BTCUSDT", Side: "BUY", PositionSide: "BOTH", ReduceOnly: true}
	tests := []struct {
		name         string
		order        binance.OpenOrder
		wantConflict bool
	}{
		{name: "limit reduce only", order: binance.OpenOrder{Symbol: "BTCUSDT", Side: "BUY", PositionSide: "BOTH", Type: "LIMIT", ReduceOnly: true}, wantConflict: true},
		{name: "stop reduce only", order: binance.OpenOrder{Symbol: "BTCUSDT", Side: "BUY", PositionSide: "BOTH", Type: "STOP_MARKET", ReduceOnly: true}, wantConflict: true},
		{name: "close position conditional", order: binance.OpenOrder{Symbol: "BTCUSDT", Side: "BUY", PositionSide: "BOTH", Type: "STOP_MARKET", ClosePosition: true}, wantConflict: true},
		{name: "managed close without flags", order: binance.OpenOrder{Symbol: "BTCUSDT", Side: "BUY", PositionSide: "BOTH", Type: "LIMIT", ClientOrderID: "PC123"}, wantConflict: true},
		{name: "managed protection without flags", order: binance.OpenOrder{Symbol: "BTCUSDT", Side: "BUY", PositionSide: "BOTH", Type: "STOP_MARKET", ClientOrderID: "PP123SL"}, wantConflict: true},
		{name: "same side entry", order: binance.OpenOrder{Symbol: "BTCUSDT", Side: "BUY", PositionSide: "BOTH", Type: "LIMIT"}, wantConflict: false},
		{name: "opposite side close", order: binance.OpenOrder{Symbol: "BTCUSDT", Side: "SELL", PositionSide: "BOTH", Type: "LIMIT", ReduceOnly: true}, wantConflict: false},
		{name: "different symbol close", order: binance.OpenOrder{Symbol: "ETHUSDT", Side: "BUY", PositionSide: "BOTH", Type: "LIMIT", ReduceOnly: true}, wantConflict: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := binanceConflictingReduceOnlyCloseOrder(tc.order, req); got != tc.wantConflict {
				t.Fatalf("conflict=%v, want %v for %#v", got, tc.wantConflict, tc.order)
			}
		})
	}
}

func TestTVBotBinancePositionCloseMarketAndLimit(t *testing.T) {
	oldPoll := positionClosePollInterval
	oldTimeout := positionCloseLimitTimeout
	oldJobs := positionCloseJobs
	positionClosePollInterval = time.Hour
	positionCloseLimitTimeout = time.Hour
	positionCloseJobs = newPositionCloseRegistry()
	t.Cleanup(func() {
		positionClosePollInterval = oldPoll
		positionCloseLimitTimeout = oldTimeout
		positionCloseJobs = oldJobs
	})

	srv := newTestServer(t)
	var orderForms []url.Values
	binanceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/fapi/v1/exchangeInfo" && r.URL.Path != "/fapi/v1/ticker/bookTicker" && r.Header.Get("X-MBX-APIKEY") != "binance-key" {
			t.Fatalf("missing Binance API key for %s", r.URL.Path)
		}
		switch r.URL.Path {
		case "/fapi/v3/positionRisk":
			if r.URL.Query().Get("symbol") != "BTCUSDT" {
				t.Fatalf("bad positions query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`[{"symbol":"BTCUSDT","positionSide":"BOTH","positionAmt":"-2","entryPrice":"100","markPrice":"100.1","unRealizedProfit":"-0.2","liquidationPrice":"120","isolatedMargin":"20","notional":"200.2","marginAsset":"USDT","leverage":"10","marginType":"isolated","updateTime":1784880000000}]`))
		case "/fapi/v1/exchangeInfo":
			_, _ = w.Write([]byte(`{"symbols":[{"symbol":"BTCUSDT","status":"TRADING","pricePrecision":1,"quantityPrecision":3,"filters":[{"filterType":"PRICE_FILTER","tickSize":"0.1"},{"filterType":"LOT_SIZE","minQty":"0.001","stepSize":"0.001"}]}]}`))
		case "/fapi/v1/ticker/bookTicker":
			if r.URL.Query().Get("symbol") != "BTCUSDT" {
				t.Fatalf("bad book ticker query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","bidPrice":"100","bidQty":"1","askPrice":"100.2","askQty":"1","time":1784880000000}`))
		case "/fapi/v1/openOrders":
			_, _ = w.Write([]byte(`[]`))
		case "/fapi/v1/openAlgoOrders":
			_, _ = w.Write([]byte(`[]`))
		case "/fapi/v1/order":
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			orderForms = append(orderForms, cloneValues(r.Form))
			_, _ = w.Write([]byte(`{"orderId":789,"symbol":"BTCUSDT","status":"NEW","clientOrderId":"close-1","price":"0","origQty":"2","executedQty":"0","type":"MARKET","side":"BUY","positionSide":"BOTH"}`))
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

	for _, tc := range []struct {
		mode  string
		ratio string
		code  int
	}{
		{mode: "market", code: http.StatusOK},
		{mode: "limit", ratio: `,"ratio":0.1`, code: http.StatusAccepted},
	} {
		body := []byte(`{"exchange":"binance","api_id":"main","inst_id":"BTCUSDT","pos_side":"net","mode":"` + tc.mode + `"` + tc.ratio + `}`)
		req := httptest.NewRequest(http.MethodPost, "/tvbot/positions/close", bytes.NewReader(body))
		req.SetBasicAuth("admin", "Admin123")
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		if rr.Code != tc.code {
			t.Fatalf("%s close status=%d body=%s", tc.mode, rr.Code, rr.Body.String())
		}
		var resp positionCloseResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if !resp.OK || resp.APIID != "main" || resp.InstID != "BTCUSDT" || resp.OrdID != "789" {
			t.Fatalf("bad %s close response: %#v", tc.mode, resp)
		}
	}
	if len(orderForms) != 2 {
		t.Fatalf("expected two close orders, got %#v", orderForms)
	}
	market := orderForms[0]
	if market.Get("symbol") != "BTCUSDT" || market.Get("side") != "BUY" || market.Get("type") != "MARKET" || market.Get("quantity") != "2" || market.Get("positionSide") != "BOTH" || market.Get("reduceOnly") != "true" {
		t.Fatalf("bad market close form: %#v", market)
	}
	limit := orderForms[1]
	if limit.Get("symbol") != "BTCUSDT" || limit.Get("side") != "BUY" || limit.Get("type") != "LIMIT" || limit.Get("timeInForce") != "GTC" || limit.Get("quantity") != "0.2" || limit.Get("price") != "100.2" || limit.Get("positionSide") != "BOTH" || limit.Get("reduceOnly") != "true" {
		t.Fatalf("bad limit close form: %#v", limit)
	}
}

func TestTVBotBinancePartialPositionCloseUsesCloseTimeoutForSlowRequests(t *testing.T) {
	oldTimeout := binancePositionCloseTimeout
	binancePositionCloseTimeout = 2 * time.Second
	t.Cleanup(func() { binancePositionCloseTimeout = oldTimeout })

	srv := newTestServer(t)
	binanceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(25 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/fapi/v3/positionRisk":
			_, _ = w.Write([]byte(`[{"symbol":"BTCUSDT","positionSide":"BOTH","positionAmt":"-2","entryPrice":"100","markPrice":"100.1","unRealizedProfit":"-0.2","liquidationPrice":"120","isolatedMargin":"20","notional":"200.2","marginAsset":"USDT","leverage":"10","marginType":"isolated","updateTime":1784880000000}]`))
		case "/fapi/v1/exchangeInfo":
			_, _ = w.Write([]byte(`{"symbols":[{"symbol":"BTCUSDT","status":"TRADING","pricePrecision":1,"quantityPrecision":3,"filters":[{"filterType":"PRICE_FILTER","tickSize":"0.1"},{"filterType":"LOT_SIZE","minQty":"0.001","stepSize":"0.001"}]}]}`))
		case "/fapi/v1/ticker/bookTicker":
			_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","bidPrice":"100","bidQty":"1","askPrice":"100.2","askQty":"1","time":1784880000000}`))
		case "/fapi/v1/openOrders", "/fapi/v1/openAlgoOrders":
			_, _ = w.Write([]byte(`[]`))
		case "/fapi/v1/order":
			_, _ = w.Write([]byte(`{"orderId":789,"symbol":"BTCUSDT","status":"NEW","clientOrderId":"close-slow","price":"100.2","origQty":"0.2","executedQty":"0","type":"LIMIT","side":"BUY","positionSide":"BOTH"}`))
		default:
			t.Fatalf("unexpected Binance path %s", r.URL.Path)
		}
	}))
	defer binanceServer.Close()
	cfg := srv.ConfigStore.Get()
	cfg.Trading.BinanceDemoBaseURL = binanceServer.URL
	srv.ConfigStore = config.NewStore("", cfg)
	// This is shorter than one deliberately slow request. The Binance close
	// client must replace it with the close-operation timeout.
	srv.BinanceHTTPClient = &http.Client{Timeout: 10 * time.Millisecond}
	if _, err := srv.BinanceCredentials.UpdateAccount(binance.CredentialAccountUpdate{
		ID: "main", Active: true,
		Credentials: binance.Credentials{APIKey: "binance-key", SecretKey: "binance-secret"},
	}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/tvbot/positions/close", bytes.NewReader([]byte(`{"exchange":"binance","api_id":"main","inst_id":"BTCUSDT","pos_side":"net","mode":"limit","ratio":0.1}`)))
	req.SetBasicAuth("admin", "Admin123")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("slow partial close status=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp positionCloseResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK || resp.Sz != "0.2" || resp.OrdID != "789" {
		t.Fatalf("bad slow partial close response: %#v", resp)
	}
}

func TestTVBotBinanceLimitPositionCloseSplitsWhenQuantityExceedsMaxQty(t *testing.T) {
	oldPoll := positionClosePollInterval
	oldTimeout := positionCloseLimitTimeout
	oldJobs := positionCloseJobs
	positionClosePollInterval = time.Hour
	positionCloseLimitTimeout = time.Hour
	positionCloseJobs = newPositionCloseRegistry()
	t.Cleanup(func() {
		positionClosePollInterval = oldPoll
		positionCloseLimitTimeout = oldTimeout
		positionCloseJobs = oldJobs
	})

	srv := newTestServer(t)
	var orderForms []url.Values
	binanceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/fapi/v1/exchangeInfo" && r.URL.Path != "/fapi/v1/ticker/bookTicker" && r.Header.Get("X-MBX-APIKEY") != "binance-key" {
			t.Fatalf("missing Binance API key for %s", r.URL.Path)
		}
		switch r.URL.Path {
		case "/fapi/v3/positionRisk":
			_, _ = w.Write([]byte(`[{"symbol":"MANTAUSDT","positionSide":"BOTH","positionAmt":"25000","entryPrice":"0.06","markPrice":"0.059","unRealizedProfit":"-25","isolatedMargin":"150","notional":"1475","marginAsset":"USDT","leverage":"10","marginType":"isolated","updateTime":1784880000000}]`))
		case "/fapi/v1/exchangeInfo":
			_, _ = w.Write([]byte(`{"symbols":[{"symbol":"MANTAUSDT","status":"TRADING","pricePrecision":4,"quantityPrecision":0,"filters":[{"filterType":"PRICE_FILTER","tickSize":"0.0001"},{"filterType":"LOT_SIZE","minQty":"1","maxQty":"10000","stepSize":"1"},{"filterType":"MARKET_LOT_SIZE","minQty":"1","maxQty":"10000","stepSize":"1"}]}]}`))
		case "/fapi/v1/ticker/bookTicker":
			_, _ = w.Write([]byte(`{"symbol":"MANTAUSDT","bidPrice":"0.0589","bidQty":"1","askPrice":"0.0591","askQty":"1","time":1784880000000}`))
		case "/fapi/v1/openOrders", "/fapi/v1/openAlgoOrders":
			_, _ = w.Write([]byte(`[]`))
		case "/fapi/v1/order":
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			form := cloneValues(r.Form)
			orderForms = append(orderForms, form)
			if form.Get("quantity") == "25000" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"code":-4005,"msg":"Quantity greater than max quantity."}`))
				return
			}
			orderID := strconv.Itoa(800 + len(orderForms))
			_, _ = w.Write([]byte(`{"orderId":` + orderID + `,"symbol":"MANTAUSDT","status":"NEW","clientOrderId":"` + form.Get("newClientOrderId") + `","price":"0.0589","origQty":"` + form.Get("quantity") + `","executedQty":"0","type":"LIMIT","side":"SELL","positionSide":"BOTH"}`))
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

	req := httptest.NewRequest(http.MethodPost, "/tvbot/positions/close", bytes.NewReader([]byte(`{"exchange":"binance","api_id":"main","inst_id":"MANTAUSDT","pos_side":"net","mode":"limit"}`)))
	req.SetBasicAuth("admin", "Admin123")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("limit close status=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp positionCloseResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK || resp.Status != "running" || resp.Sz != "25000" || resp.OrdID == "" || resp.ClOrdID == "" {
		t.Fatalf("bad split close response: %#v", resp)
	}
	if len(orderForms) != 4 {
		t.Fatalf("expected original failed order plus three split orders, got %#v", orderForms)
	}
	for i, want := range []string{"25000", "10000", "10000", "5000"} {
		if orderForms[i].Get("symbol") != "MANTAUSDT" ||
			orderForms[i].Get("side") != "SELL" ||
			orderForms[i].Get("type") != "LIMIT" ||
			orderForms[i].Get("quantity") != want ||
			orderForms[i].Get("price") != "0.0589" ||
			orderForms[i].Get("positionSide") != "BOTH" || orderForms[i].Get("reduceOnly") != "true" {
			t.Fatalf("bad split order %d: %#v", i, orderForms[i])
		}
	}
}

func TestTVBotBinanceMarketCloseRecoversUnknownStatusByClientOrderID(t *testing.T) {
	srv := newTestServer(t)
	var orderForms []url.Values
	var queryForms []url.Values
	closeClientID := ""
	binanceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("X-MBX-APIKEY") != "binance-key" {
			t.Fatalf("missing Binance API key for %s", r.URL.Path)
		}
		switch r.URL.Path {
		case "/fapi/v3/positionRisk":
			if r.URL.Query().Get("symbol") != "BTCUSDT" {
				t.Fatalf("bad positions query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`[{"symbol":"BTCUSDT","positionSide":"BOTH","positionAmt":"-2","entryPrice":"100","markPrice":"100.1","unRealizedProfit":"-0.2","isolatedMargin":"20","notional":"200.2","marginAsset":"USDT","leverage":"10","marginType":"isolated","updateTime":1784880000000}]`))
		case "/fapi/v1/openOrders", "/fapi/v1/openAlgoOrders":
			_, _ = w.Write([]byte(`[]`))
		case "/fapi/v1/order":
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			switch r.Method {
			case http.MethodPost:
				orderForms = append(orderForms, cloneValues(r.Form))
				closeClientID = r.Form.Get("newClientOrderId")
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"code":-1007,"msg":"Timeout waiting for response from backend server. Send status unknown; execution status unknown."}`))
			case http.MethodGet:
				queryForms = append(queryForms, cloneValues(r.Form))
				if r.Form.Get("origClientOrderId") != closeClientID {
					t.Fatalf("bad query client id: %s want %s", r.Form.Get("origClientOrderId"), closeClientID)
				}
				_, _ = w.Write([]byte(`{"orderId":990,"symbol":"BTCUSDT","status":"FILLED","clientOrderId":"` + closeClientID + `","price":"0","origQty":"2","executedQty":"2","type":"MARKET","side":"BUY","positionSide":"BOTH"}`))
			default:
				t.Fatalf("unexpected Binance order method %s", r.Method)
			}
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

	body := []byte(`{"exchange":"binance","api_id":"main","inst_id":"BTCUSDT","pos_side":"net","mode":"market"}`)
	req := httptest.NewRequest(http.MethodPost, "/tvbot/positions/close", bytes.NewReader(body))
	req.SetBasicAuth("admin", "Admin123")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("market close recovery status=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp positionCloseResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK || resp.Status != "submitted" || resp.OrdID != "990" || resp.ClOrdID != closeClientID {
		t.Fatalf("bad recovery response: %#v", resp)
	}
	if len(orderForms) != 1 || len(queryForms) != 1 {
		t.Fatalf("expected one order and one query, orders=%#v queries=%#v", orderForms, queryForms)
	}
}

func TestTVBotBinanceMarketCloseReturnsUnknownWhenTimeoutCannotBeResolved(t *testing.T) {
	oldAttempts := binanceUnknownOrderLookupAttempts
	oldDelay := binanceUnknownOrderLookupDelay
	binanceUnknownOrderLookupAttempts = 2
	binanceUnknownOrderLookupDelay = time.Millisecond
	t.Cleanup(func() {
		binanceUnknownOrderLookupAttempts = oldAttempts
		binanceUnknownOrderLookupDelay = oldDelay
	})

	srv := newTestServer(t)
	queryCount := 0
	closeClientID := ""
	binanceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("X-MBX-APIKEY") != "binance-key" {
			t.Fatalf("missing Binance API key for %s", r.URL.Path)
		}
		switch r.URL.Path {
		case "/fapi/v3/positionRisk":
			_, _ = w.Write([]byte(`[{"symbol":"BTCUSDT","positionSide":"BOTH","positionAmt":"-2","entryPrice":"100","markPrice":"100.1","unRealizedProfit":"-0.2","isolatedMargin":"20","notional":"200.2","marginAsset":"USDT","leverage":"10","marginType":"isolated","updateTime":1784880000000}]`))
		case "/fapi/v1/openOrders", "/fapi/v1/openAlgoOrders":
			_, _ = w.Write([]byte(`[]`))
		case "/fapi/v1/order":
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			switch r.Method {
			case http.MethodPost:
				closeClientID = r.Form.Get("newClientOrderId")
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"code":-1007,"msg":"Timeout waiting for response from backend server. Send status unknown; execution status unknown."}`))
			case http.MethodGet:
				queryCount++
				if r.Form.Get("origClientOrderId") != closeClientID {
					t.Fatalf("bad query client id: %s want %s", r.Form.Get("origClientOrderId"), closeClientID)
				}
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"code":-2013,"msg":"Order does not exist."}`))
			default:
				t.Fatalf("unexpected Binance order method %s", r.Method)
			}
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

	body := []byte(`{"exchange":"binance","api_id":"main","inst_id":"BTCUSDT","pos_side":"net","mode":"market"}`)
	req := httptest.NewRequest(http.MethodPost, "/tvbot/positions/close", bytes.NewReader(body))
	req.SetBasicAuth("admin", "Admin123")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("market close unknown status=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp positionCloseResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK || resp.Status != "unknown" || resp.ClOrdID != closeClientID || resp.OrdID != "" {
		t.Fatalf("bad unknown response: %#v", resp)
	}
	if queryCount != 2 {
		t.Fatalf("expected two recovery queries, got %d", queryCount)
	}
}

func TestTVBotBinanceLimitCloseRetriesAfterReduceOnlyConflict(t *testing.T) {
	oldPoll := positionClosePollInterval
	oldTimeout := positionCloseLimitTimeout
	oldJobs := positionCloseJobs
	positionClosePollInterval = time.Hour
	positionCloseLimitTimeout = time.Hour
	positionCloseJobs = newPositionCloseRegistry()
	t.Cleanup(func() {
		positionClosePollInterval = oldPoll
		positionCloseLimitTimeout = oldTimeout
		positionCloseJobs = oldJobs
	})

	srv := newTestServer(t)
	var orderForms []url.Values
	var cancelForms []url.Values
	regularOrdersCanceled := false
	binanceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/fapi/v1/exchangeInfo" && r.URL.Path != "/fapi/v1/ticker/bookTicker" && r.Header.Get("X-MBX-APIKEY") != "binance-key" {
			t.Fatalf("missing Binance API key for %s", r.URL.Path)
		}
		switch r.URL.Path {
		case "/fapi/v3/positionRisk":
			if r.URL.Query().Get("symbol") != "AVAXUSDT" {
				t.Fatalf("bad positions query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`[{"symbol":"AVAXUSDT","positionSide":"BOTH","positionAmt":"-15","entryPrice":"6.816","markPrice":"6.6284","unRealizedProfit":"2.814317","isolatedMargin":"33.141895","notional":"99.426583","marginAsset":"USDT","leverage":"3","marginType":"isolated","updateTime":1784880000000}]`))
		case "/fapi/v1/exchangeInfo":
			_, _ = w.Write([]byte(`{"symbols":[{"symbol":"AVAXUSDT","status":"TRADING","pricePrecision":4,"quantityPrecision":0,"filters":[{"filterType":"PRICE_FILTER","tickSize":"0.0001"},{"filterType":"LOT_SIZE","minQty":"1","stepSize":"1"}]}]}`))
		case "/fapi/v1/ticker/bookTicker":
			if r.URL.Query().Get("symbol") != "AVAXUSDT" {
				t.Fatalf("bad book ticker query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"symbol":"AVAXUSDT","bidPrice":"6.6283","bidQty":"1","askPrice":"6.6285","askQty":"1","time":1784880000000}`))
		case "/fapi/v1/openOrders":
			if r.URL.Query().Get("symbol") != "AVAXUSDT" {
				t.Fatalf("bad open orders query: %s", r.URL.RawQuery)
			}
			if regularOrdersCanceled {
				_, _ = w.Write([]byte(`[]`))
				return
			}
			_, _ = w.Write([]byte(`[
				{"symbol":"AVAXUSDT","orderId":100,"clientOrderId":"old-close","price":"6.6285","origQty":"15","executedQty":"0","side":"BUY","positionSide":"BOTH","type":"STOP_MARKET","status":"NEW","time":1784880000000,"updateTime":1784880000000,"reduceOnly":true},
				{"symbol":"AVAXUSDT","orderId":101,"clientOrderId":"entry","price":"6.5","origQty":"2","executedQty":"0","side":"BUY","positionSide":"BOTH","type":"LIMIT","status":"NEW","time":1784880000000,"updateTime":1784880000000,"reduceOnly":false},
				{"symbol":"AVAXUSDT","orderId":102,"clientOrderId":"other-side","price":"7","origQty":"1","executedQty":"0","side":"SELL","positionSide":"BOTH","type":"LIMIT","status":"NEW","time":1784880000000,"updateTime":1784880000000,"reduceOnly":true}
			]`))
		case "/fapi/v1/openAlgoOrders":
			if r.URL.Query().Get("symbol") != "AVAXUSDT" || r.URL.Query().Get("algoType") != "CONDITIONAL" {
				t.Fatalf("bad open algo orders query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`[]`))
		case "/fapi/v1/order":
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			switch r.Method {
			case http.MethodPost:
				orderForms = append(orderForms, cloneValues(r.Form))
				if len(orderForms) == 1 {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"code":-2022,"msg":"ReduceOnly Order is rejected."}`))
					return
				}
				_, _ = w.Write([]byte(`{"orderId":789,"symbol":"AVAXUSDT","status":"NEW","clientOrderId":"close-1","price":"6.6285","origQty":"15","executedQty":"0","type":"LIMIT","side":"BUY","positionSide":"BOTH"}`))
			case http.MethodDelete:
				cancelForms = append(cancelForms, cloneValues(r.Form))
				regularOrdersCanceled = true
				_, _ = w.Write([]byte(`{"orderId":100,"symbol":"AVAXUSDT","status":"CANCELED","clientOrderId":"old-close","price":"6.6285","origQty":"15","executedQty":"0","type":"STOP_MARKET","side":"BUY","positionSide":"BOTH"}`))
			default:
				t.Fatalf("unexpected Binance order method %s", r.Method)
			}
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

	body := []byte(`{"exchange":"binance","api_id":"main","inst_id":"AVAXUSDT","pos_side":"net","mode":"limit"}`)
	req := httptest.NewRequest(http.MethodPost, "/tvbot/positions/close", bytes.NewReader(body))
	req.SetBasicAuth("admin", "Admin123")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("limit close retry status=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(orderForms) != 2 {
		t.Fatalf("expected failed order and retry, got %#v", orderForms)
	}
	if len(cancelForms) != 3 {
		t.Fatalf("expected every regular order for the symbol canceled before close, got %#v", cancelForms)
	}
	retry := orderForms[1]
	if retry.Get("symbol") != "AVAXUSDT" || retry.Get("side") != "BUY" || retry.Get("type") != "LIMIT" || retry.Get("quantity") != "15" || retry.Get("price") != "6.6285" || retry.Get("positionSide") != "BOTH" || retry.Get("reduceOnly") != "true" {
		t.Fatalf("bad retry close form: %#v", retry)
	}
}

func TestTVBotBinanceLimitCloseRetriesAfterReduceOnlyAlgoConflict(t *testing.T) {
	oldPoll := positionClosePollInterval
	oldTimeout := positionCloseLimitTimeout
	oldJobs := positionCloseJobs
	positionClosePollInterval = time.Hour
	positionCloseLimitTimeout = time.Hour
	positionCloseJobs = newPositionCloseRegistry()
	t.Cleanup(func() {
		positionClosePollInterval = oldPoll
		positionCloseLimitTimeout = oldTimeout
		positionCloseJobs = oldJobs
	})

	srv := newTestServer(t)
	var orderForms []url.Values
	var cancelAlgoForms []url.Values
	algoOrdersCanceled := false
	binanceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/fapi/v1/exchangeInfo" && r.URL.Path != "/fapi/v1/ticker/bookTicker" && r.Header.Get("X-MBX-APIKEY") != "binance-key" {
			t.Fatalf("missing Binance API key for %s", r.URL.Path)
		}
		switch r.URL.Path {
		case "/fapi/v3/positionRisk":
			if r.URL.Query().Get("symbol") != "AVAXUSDT" {
				t.Fatalf("bad positions query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`[{"symbol":"AVAXUSDT","positionSide":"BOTH","positionAmt":"-15","entryPrice":"6.816","markPrice":"6.6471","unRealizedProfit":"2.533067","isolatedMargin":"33.235645","notional":"99.706933","marginAsset":"USDT","leverage":"3","marginType":"isolated","updateTime":1784880000000}]`))
		case "/fapi/v1/exchangeInfo":
			_, _ = w.Write([]byte(`{"symbols":[{"symbol":"AVAXUSDT","status":"TRADING","pricePrecision":4,"quantityPrecision":0,"filters":[{"filterType":"PRICE_FILTER","tickSize":"0.0001"},{"filterType":"LOT_SIZE","minQty":"1","stepSize":"1"}]}]}`))
		case "/fapi/v1/ticker/bookTicker":
			if r.URL.Query().Get("symbol") != "AVAXUSDT" {
				t.Fatalf("bad book ticker query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"symbol":"AVAXUSDT","bidPrice":"6.6470","bidQty":"1","askPrice":"6.6472","askQty":"1","time":1784880000000}`))
		case "/fapi/v1/openOrders":
			if r.URL.Query().Get("symbol") != "AVAXUSDT" {
				t.Fatalf("bad open orders query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`[]`))
		case "/fapi/v1/openAlgoOrders":
			if r.URL.Query().Get("symbol") != "AVAXUSDT" || r.URL.Query().Get("algoType") != "CONDITIONAL" {
				t.Fatalf("bad open algo orders query: %s", r.URL.RawQuery)
			}
			if algoOrdersCanceled {
				_, _ = w.Write([]byte(`[]`))
				return
			}
			_, _ = w.Write([]byte(`[
				{"symbol":"AVAXUSDT","algoId":900,"clientAlgoId":"old-avx-sl","side":"BUY","positionSide":"BOTH","orderType":"STOP_MARKET","algoStatus":"NEW","quantity":"15","reduceOnly":true},
				{"symbol":"AVAXUSDT","algoId":901,"clientAlgoId":"entry-signal","side":"BUY","positionSide":"BOTH","orderType":"STOP_MARKET","algoStatus":"NEW","quantity":"2","reduceOnly":false},
				{"symbol":"AVAXUSDT","algoId":902,"clientAlgoId":"long-protect","side":"SELL","positionSide":"BOTH","orderType":"TAKE_PROFIT_MARKET","algoStatus":"NEW","quantity":"1","reduceOnly":true}
			]`))
		case "/fapi/v1/algoOrder":
			if r.Method != http.MethodDelete {
				t.Fatalf("unexpected Binance algo method %s", r.Method)
			}
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			cancelAlgoForms = append(cancelAlgoForms, cloneValues(r.Form))
			algoOrdersCanceled = true
			_, _ = w.Write([]byte(`{"algoId":900,"clientAlgoId":"old-avx-sl","code":"200","msg":"success"}`))
		case "/fapi/v1/order":
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if r.Method != http.MethodPost {
				t.Fatalf("unexpected Binance order method %s", r.Method)
			}
			orderForms = append(orderForms, cloneValues(r.Form))
			if len(orderForms) == 1 {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"code":-2022,"msg":"ReduceOnly Order is rejected."}`))
				return
			}
			_, _ = w.Write([]byte(`{"orderId":789,"symbol":"AVAXUSDT","status":"NEW","clientOrderId":"close-1","price":"6.6472","origQty":"15","executedQty":"0","type":"LIMIT","side":"BUY","positionSide":"BOTH"}`))
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

	body := []byte(`{"exchange":"binance","api_id":"main","inst_id":"AVAXUSDT","pos_side":"net","mode":"limit"}`)
	req := httptest.NewRequest(http.MethodPost, "/tvbot/positions/close", bytes.NewReader(body))
	req.SetBasicAuth("admin", "Admin123")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("limit close retry status=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(orderForms) != 2 {
		t.Fatalf("expected failed order and retry, got %#v", orderForms)
	}
	if len(cancelAlgoForms) != 3 {
		t.Fatalf("expected every algorithm order for the symbol canceled before close, got %#v", cancelAlgoForms)
	}
	retry := orderForms[1]
	if retry.Get("symbol") != "AVAXUSDT" || retry.Get("side") != "BUY" || retry.Get("type") != "LIMIT" || retry.Get("quantity") != "15" || retry.Get("price") != "6.6472" || retry.Get("positionSide") != "BOTH" || retry.Get("reduceOnly") != "true" {
		t.Fatalf("bad retry close form: %#v", retry)
	}
}

func TestBinanceReduceOnlyCloseRetriesWhenConflictSnapshotIsEmpty(t *testing.T) {
	oldDelay := binanceReduceOnlyRetryDelay
	binanceReduceOnlyRetryDelay = 0
	t.Cleanup(func() { binanceReduceOnlyRetryDelay = oldDelay })

	orderAttempts := 0
	regularChecks := 0
	algoChecks := 0
	binanceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/fapi/v1/openOrders":
			regularChecks++
			_, _ = w.Write([]byte(`[]`))
		case "/fapi/v1/openAlgoOrders":
			algoChecks++
			_, _ = w.Write([]byte(`[]`))
		case "/fapi/v1/order":
			if r.Method != http.MethodPost {
				t.Fatalf("unexpected Binance order method %s", r.Method)
			}
			orderAttempts++
			if orderAttempts == 1 {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"code":-2022,"msg":"ReduceOnly Order is rejected."}`))
				return
			}
			_, _ = w.Write([]byte(`{"orderId":789,"symbol":"ZECUSDT","status":"NEW","clientOrderId":"close-zec","price":"1149.1","origQty":"0.432","executedQty":"0","type":"LIMIT","side":"SELL","positionSide":"BOTH"}`))
		default:
			t.Fatalf("unexpected Binance path %s", r.URL.Path)
		}
	}))
	defer binanceServer.Close()
	client := binance.Client{BaseURL: binanceServer.URL, Credentials: binance.Credentials{APIKey: strings.Repeat("k", 4), SecretKey: strings.Repeat("s", 8)}, HTTPClient: binanceServer.Client()}
	req := binance.PlaceOrderRequest{
		Symbol:           "ZECUSDT",
		Side:             "SELL",
		PositionSide:     "BOTH",
		Type:             "LIMIT",
		TimeInForce:      "GTC",
		Quantity:         "0.432",
		Price:            "1149.1",
		NewClientOrderID: "close-zec",
		ReduceOnly:       true,
	}
	_, firstErr := client.PlaceOrder(context.Background(), req)
	if firstErr == nil || !binance.IsAPIErrorCode(firstErr, -2022) {
		t.Fatalf("expected initial ReduceOnly rejection, got %v", firstErr)
	}
	ack, unknown, err := retryBinanceReduceOnlyPositionClose(context.Background(), client, req, firstErr)
	if err != nil || unknown || ack.OrderID != 789 {
		t.Fatalf("retry result ack=%#v unknown=%v err=%v", ack, unknown, err)
	}
	if orderAttempts != 2 || regularChecks != 1 || algoChecks != 1 {
		t.Fatalf("expected one initial and one retry with both order snapshots, got orders=%d regular=%d algo=%d", orderAttempts, regularChecks, algoChecks)
	}
}

func TestBinanceLimitCloseUsesMarketReduceOnlyFallbackAfterPersistentConflict(t *testing.T) {
	oldDelay := binanceReduceOnlyRetryDelay
	binanceReduceOnlyRetryDelay = 0
	t.Cleanup(func() { binanceReduceOnlyRetryDelay = oldDelay })

	var orderForms []url.Values
	positionQueries := 0
	binanceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/fapi/v3/positionRisk":
			positionQueries++
			positionAmt := "4.327"
			if positionQueries > 1 {
				positionAmt = "4.319"
			}
			_, _ = fmt.Fprintf(w, `[{"symbol":"ZECUSDT","positionSide":"BOTH","positionAmt":%q,"entryPrice":"1073.1994","markPrice":"1145.8","unRealizedProfit":"313.2","liquidationPrice":"980","isolatedMargin":"765","notional":"4959","marginAsset":"USDT","leverage":"10","marginType":"isolated","updateTime":1784880000000}]`, positionAmt)
		case "/fapi/v1/exchangeInfo":
			_, _ = w.Write([]byte(`{"symbols":[{"symbol":"ZECUSDT","status":"TRADING","pricePrecision":2,"quantityPrecision":3,"filters":[{"filterType":"PRICE_FILTER","tickSize":"0.01"},{"filterType":"LOT_SIZE","minQty":"0.001","stepSize":"0.001"}]}]}`))
		case "/fapi/v1/ticker/bookTicker":
			_, _ = w.Write([]byte(`{"symbol":"ZECUSDT","bidPrice":"1145.7","bidQty":"1","askPrice":"1145.9","askQty":"1","time":1784880000000}`))
		case "/fapi/v1/openOrders":
			_, _ = w.Write([]byte(`[]`))
		case "/fapi/v1/openAlgoOrders":
			_, _ = w.Write([]byte(`[]`))
		case "/fapi/v1/order":
			if r.Method != http.MethodPost {
				t.Fatalf("unexpected Binance order method %s", r.Method)
			}
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			form := cloneValues(r.Form)
			orderForms = append(orderForms, form)
			if form.Get("type") == "LIMIT" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"code":-2022,"msg":"ReduceOnly Order is rejected."}`))
				return
			}
			if form.Get("reduceOnly") == "true" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"code":-2022,"msg":"ReduceOnly Order is rejected."}`))
				return
			}
			_, _ = w.Write([]byte(`{"orderId":900,"symbol":"ZECUSDT","status":"NEW","clientOrderId":"market-fallback","price":"0","origQty":"0.431","executedQty":"0","type":"MARKET","side":"SELL","positionSide":"BOTH"}`))
		default:
			t.Fatalf("unexpected Binance path %s", r.URL.Path)
		}
	}))
	defer binanceServer.Close()

	client := binance.Client{BaseURL: binanceServer.URL, Credentials: binance.Credentials{APIKey: strings.Repeat("k", 4), SecretKey: strings.Repeat("s", 8)}, HTTPClient: binanceServer.Client()}
	position := okx.Position{InstID: "ZECUSDT", Pos: "4.327", PosSide: "net"}
	order, err := placeBinancePositionClose(context.Background(), client, position, "limit", "0.432")
	if err != nil {
		t.Fatalf("expected market fallback to close, got %v", err)
	}
	if !order.MarketFallback || !order.NonReduceOnlyFallback || order.Ack.OrdID != "900" || order.CloseSz != "0.431" {
		t.Fatalf("bad market fallback result: %#v", order)
	}
	if len(orderForms) != 9 {
		t.Fatalf("expected four rejected limit attempts, four rejected reduce-only market attempts, and one non-reduce-only fallback, got %#v", orderForms)
	}
	market := orderForms[len(orderForms)-1]
	if market.Get("type") != "MARKET" || market.Get("side") != "SELL" || market.Get("quantity") != "0.431" || market.Get("positionSide") != "BOTH" || market.Get("reduceOnly") != "" || market.Get("price") != "" || market.Get("timeInForce") != "" {
		t.Fatalf("bad market fallback form: %#v", market)
	}
	for _, reduceOnlyOrder := range orderForms[:len(orderForms)-1] {
		if reduceOnlyOrder.Get("reduceOnly") != "true" {
			t.Fatalf("non-reduce-only order was submitted before the final fallback: %#v", reduceOnlyOrder)
		}
	}
}

func TestTVBotBinanceLimitCloseFallsBackToMarketRemaining(t *testing.T) {
	oldPoll := positionClosePollInterval
	oldTimeout := positionCloseLimitTimeout
	oldJobs := positionCloseJobs
	positionClosePollInterval = time.Hour
	positionCloseLimitTimeout = 20 * time.Millisecond
	positionCloseJobs = newPositionCloseRegistry()
	t.Cleanup(func() {
		positionClosePollInterval = oldPoll
		positionCloseLimitTimeout = oldTimeout
		positionCloseJobs = oldJobs
	})

	srv := newTestServer(t)
	var mu sync.Mutex
	var orderForms []url.Values
	var cancelForms []url.Values
	limitOrderPending := false
	limitOrderCancelled := false
	marketSeen := make(chan struct{}, 1)
	binanceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/fapi/v1/exchangeInfo" && r.URL.Path != "/fapi/v1/ticker/bookTicker" && r.Header.Get("X-MBX-APIKEY") != "binance-key" {
			t.Fatalf("missing Binance API key for %s", r.URL.Path)
		}
		switch r.URL.Path {
		case "/fapi/v3/positionRisk":
			_, _ = w.Write([]byte(`[{"symbol":"BTCUSDT","positionSide":"BOTH","positionAmt":"-10","entryPrice":"100","markPrice":"100.1","unRealizedProfit":"-1","isolatedMargin":"100","notional":"1001","marginAsset":"USDT","leverage":"10","marginType":"isolated","updateTime":1784880000000}]`))
		case "/fapi/v1/exchangeInfo":
			_, _ = w.Write([]byte(`{"symbols":[{"symbol":"BTCUSDT","status":"TRADING","pricePrecision":1,"quantityPrecision":3,"filters":[{"filterType":"PRICE_FILTER","tickSize":"0.1"},{"filterType":"LOT_SIZE","minQty":"0.001","stepSize":"0.001"}]}]}`))
		case "/fapi/v1/ticker/bookTicker":
			_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","bidPrice":"100","bidQty":"1","askPrice":"100.2","askQty":"1","time":1784880000000}`))
		case "/fapi/v1/openOrders":
			mu.Lock()
			pending := limitOrderPending && !limitOrderCancelled
			mu.Unlock()
			if pending {
				_, _ = w.Write([]byte(`[{"symbol":"BTCUSDT","orderId":100,"clientOrderId":"limit-close","price":"100.2","origQty":"10","executedQty":"2","side":"BUY","positionSide":"BOTH","type":"LIMIT","status":"NEW","time":1784880000000,"updateTime":1784880000000,"reduceOnly":true}]`))
			} else {
				_, _ = w.Write([]byte(`[]`))
			}
		case "/fapi/v1/openAlgoOrders":
			_, _ = w.Write([]byte(`[]`))
		case "/fapi/v1/order":
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			switch r.Method {
			case http.MethodPost:
				form := cloneValues(r.Form)
				mu.Lock()
				orderForms = append(orderForms, form)
				if form.Get("type") == "LIMIT" {
					limitOrderPending = true
				}
				mu.Unlock()
				if form.Get("type") == "MARKET" {
					select {
					case marketSeen <- struct{}{}:
					default:
					}
					_, _ = w.Write([]byte(`{"orderId":101,"symbol":"BTCUSDT","status":"NEW","clientOrderId":"market-close","origQty":"8","executedQty":"0","type":"MARKET","side":"BUY","positionSide":"BOTH"}`))
					return
				}
				_, _ = w.Write([]byte(`{"orderId":100,"symbol":"BTCUSDT","status":"NEW","clientOrderId":"limit-close","price":"100.2","origQty":"10","executedQty":"0","type":"LIMIT","side":"BUY","positionSide":"BOTH"}`))
			case http.MethodDelete:
				mu.Lock()
				cancelForms = append(cancelForms, cloneValues(r.Form))
				limitOrderCancelled = true
				mu.Unlock()
				_, _ = w.Write([]byte(`{"orderId":100,"symbol":"BTCUSDT","status":"CANCELED","clientOrderId":"limit-close","price":"100.2","origQty":"10","executedQty":"2","type":"LIMIT","side":"BUY","positionSide":"BOTH"}`))
			default:
				t.Fatalf("unexpected Binance order method %s", r.Method)
			}
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

	body := []byte(`{"exchange":"binance","api_id":"main","inst_id":"BTCUSDT","pos_side":"net","mode":"limit"}`)
	req := httptest.NewRequest(http.MethodPost, "/tvbot/positions/close", bytes.NewReader(body))
	req.SetBasicAuth("admin", "Admin123")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("limit close status=%d body=%s", rr.Code, rr.Body.String())
	}
	select {
	case <-marketSeen:
	case <-time.After(time.Second):
		t.Fatal("expected Binance market fallback")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(cancelForms) != 1 || cancelForms[0].Get("orderId") != "100" {
		t.Fatalf("expected limit close cancel, got %#v", cancelForms)
	}
	if len(orderForms) < 2 {
		t.Fatalf("expected initial limit and fallback market orders, got %#v", orderForms)
	}
	market := orderForms[len(orderForms)-1]
	if market.Get("type") != "MARKET" || market.Get("side") != "BUY" || market.Get("quantity") != "8" || market.Get("positionSide") != "BOTH" || market.Get("reduceOnly") != "true" {
		t.Fatalf("bad market fallback form: %#v", market)
	}
}
