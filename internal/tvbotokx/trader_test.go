package okx

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotconfig"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

type credentialProviderFunc func(string) (Credentials, string, error)

func (f credentialProviderFunc) OKXCredentials(apiID string) (Credentials, string, error) {
	return f(apiID)
}

func TestAttachAlgoOrdersBuildsTPSLAndTrailing(t *testing.T) {
	tp := trading.NewFlexibleFloat(2)
	sl := trading.NewFlexibleFloat(1)
	shortTPSL := AttachAlgoOrders(trading.ActionShort, trading.Risk{Type: trading.RiskTPSL, TPPct: &tp, SLPct: &sl}, "CLIENT100", 10)
	if len(shortTPSL) != 1 {
		t.Fatalf("short tp/sl attach length = %d", len(shortTPSL))
	}
	if shortTPSL[0]["attachAlgoClOrdId"] != "CLIENT100A" || shortTPSL[0]["tpTriggerRatio"] != "-0.002" || shortTPSL[0]["slTriggerRatio"] != "0.001" || shortTPSL[0]["tpOrdPx"] != "-1" || shortTPSL[0]["slOrdPx"] != "-1" {
		t.Fatalf("bad short tp/sl attach: %#v", shortTPSL[0])
	}

	longTPSL := AttachAlgoOrders(trading.ActionLong, trading.Risk{Type: trading.RiskTPSL, TPPct: &tp, SLPct: &sl}, "CLIENT101", 10)
	if len(longTPSL) != 1 {
		t.Fatalf("long tp/sl attach length = %d", len(longTPSL))
	}
	if longTPSL[0]["attachAlgoClOrdId"] != "CLIENT101A" || longTPSL[0]["tpTriggerRatio"] != "0.002" || longTPSL[0]["slTriggerRatio"] != "-0.001" || longTPSL[0]["tpOrdPx"] != "-1" || longTPSL[0]["slOrdPx"] != "-1" {
		t.Fatalf("bad long tp/sl attach: %#v", longTPSL[0])
	}
	if longTPSL[0]["tpTriggerPxType"] != "last" || longTPSL[0]["slTriggerPxType"] != "last" {
		t.Fatalf("dynamic tp/sl should use last price triggers: %#v", longTPSL[0])
	}

	trailingPct := trading.NewFlexibleFloat(1.5)
	trailing := AttachAlgoOrders(trading.ActionLong, trading.Risk{Type: trading.RiskTrailing, TrailingPct: &trailingPct}, "CLIENT200", 10)
	if len(trailing) != 1 {
		t.Fatalf("trailing attach length = %d", len(trailing))
	}
	if trailing[0]["attachAlgoClOrdId"] != "CLIENT200T" || trailing[0]["ordType"] != "move_order_stop" || trailing[0]["callbackRatio"] != "0.015" {
		t.Fatalf("bad trailing attach: %#v", trailing[0])
	}
}

func TestAttachAlgoOrdersQuantizesRatiosToOKXStep(t *testing.T) {
	tp := trading.NewFlexibleFloat(1.23456)
	sl := trading.NewFlexibleFloat(0.005)
	tpsl := AttachAlgoOrders(trading.ActionLong, trading.Risk{Type: trading.RiskTPSL, TPPct: &tp, SLPct: &sl}, "CLIENT300", 10)
	if len(tpsl) != 1 {
		t.Fatalf("tp/sl attach length = %d", len(tpsl))
	}
	if tpsl[0]["tpTriggerRatio"] != "0.0012" || tpsl[0]["slTriggerRatio"] != "-0.0001" {
		t.Fatalf("bad quantized tp/sl attach: %#v", tpsl[0])
	}
}

func TestTraderExecuteSignalPlacesDefaultMarketOrderWithTPSL(t *testing.T) {
	var leverageSeen bool
	var orderReq PlaceOrderRequest
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v5/account/positions":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[]}`))
		case "/api/v5/trade/orders-pending":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[]}`))
		case "/api/v5/account/set-leverage":
			leverageSeen = true
			var req SetLeverageRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			if req.InstID != "BTC-USDT-SWAP" || req.Lever != "5" || req.MgnMode != "isolated" {
				t.Fatalf("bad leverage request: %#v", req)
			}
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{}]}`))
		case "/api/v5/trade/order":
			if r.Header.Get("x-simulated-trading") != "1" {
				t.Fatal("missing demo header on order")
			}
			if err := json.NewDecoder(r.Body).Decode(&orderReq); err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"clOrdId":"x","ordId":"123","sCode":"0","sMsg":""}]}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer ts.Close()

	cfg := config.Default()
	cfg.Trading.BaseURL = ts.URL
	tp := trading.NewFlexibleFloat(2)
	sl := trading.NewFlexibleFloat(1)
	signal := trading.Signal{
		Action:   trading.ActionLong,
		Coinpair: "BTC",
		Price:    trading.NewFlexibleFloat(50000),
		SentAt:   "2026-07-24T03:00:00Z",
		Ticker:   "BTCUSDT",
		Leverage: 5,
		Amount:   trading.NewFlexibleFloat(100),
		Risk:     trading.Risk{Type: trading.RiskTPSL, TPPct: &tp, SLPct: &sl},
	}
	trader := Trader{
		Credentials: Credentials{APIKey: "key", SecretKey: "secret", Passphrase: "pass"},
		HTTPClient:  ts.Client(),
	}
	result, err := trader.ExecuteSignal(context.Background(), signal, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !leverageSeen {
		t.Fatal("expected leverage request")
	}
	if result.OrdID != "123" {
		t.Fatalf("ord id = %q", result.OrdID)
	}
	if orderReq.InstID != "BTC-USDT-SWAP" || orderReq.Side != "buy" || orderReq.OrdType != "market" || orderReq.Px != "" || orderReq.Sz != "0.2" {
		t.Fatalf("bad order request: %#v", orderReq)
	}
	if result.OrdType != "market" || result.Px != "" {
		t.Fatalf("bad order result: %#v", result)
	}
	if len(orderReq.AttachAlgoOrds) != 1 {
		t.Fatalf("attach algo length = %d", len(orderReq.AttachAlgoOrds))
	}
	attach := orderReq.AttachAlgoOrds[0]
	if attach["tpTriggerRatio"] != "0.004" || attach["slTriggerRatio"] != "-0.002" || attach["tpOrdPx"] != "-1" || attach["slOrdPx"] != "-1" {
		t.Fatalf("bad attach algo: %#v", attach)
	}
	if attach["tpTriggerPxType"] != "last" || attach["slTriggerPxType"] != "last" {
		t.Fatalf("dynamic tp/sl should use last price triggers: %#v", attach)
	}
}

func TestTraderExecuteSignalRetriesTPSLWithFixedTriggerPricesOnOKX54079(t *testing.T) {
	var orderReqs []PlaceOrderRequest
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v5/public/instruments":
			if r.URL.Query().Get("instId") != "BNB-USDT-SWAP" {
				t.Fatalf("bad instruments query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instId":"BNB-USDT-SWAP","ctVal":"1","tickSz":"0.1","lotSz":"0.1","minSz":"0.1"}]}`))
		case "/api/v5/account/positions":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[]}`))
		case "/api/v5/trade/orders-pending":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[]}`))
		case "/api/v5/account/set-leverage":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{}]}`))
		case "/api/v5/trade/order":
			var req PlaceOrderRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			orderReqs = append(orderReqs, req)
			if len(orderReqs) == 1 {
				_, _ = w.Write([]byte(`{"code":"1","msg":"All operations failed","data":[{"sCode":"54079","sMsg":"Dynamic change is available only for futures trading in futures mode or multi-currency mode. Note that when selecting dynamic change, the trigger price can only be calculated using the last price."}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"clOrdId":"x","ordId":"789","sCode":"0","sMsg":""}]}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer ts.Close()

	cfg := config.Default()
	cfg.Symbols = map[string]config.SymbolConfig{}
	cfg.Trading.BaseURL = ts.URL
	tp := trading.NewFlexibleFloat(1.5)
	sl := trading.NewFlexibleFloat(2)
	signal := trading.Signal{
		Action:   trading.ActionShort,
		Coinpair: "BNB-USDT-SWAP",
		Price:    trading.NewFlexibleFloat(603),
		SentAt:   "2026-08-08T19:50:03Z",
		Ticker:   "BNB-USDT-SWAP",
		Leverage: 10,
		Amount:   trading.NewFlexibleFloat(500),
		Risk:     trading.Risk{Type: trading.RiskTPSL, TPPct: &tp, SLPct: &sl},
	}
	trader := Trader{
		Credentials: Credentials{APIKey: "key", SecretKey: "secret", Passphrase: "pass"},
		HTTPClient:  ts.Client(),
	}
	result, err := trader.ExecuteSignal(context.Background(), signal, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if result.OrdID != "789" {
		t.Fatalf("ord id = %q", result.OrdID)
	}
	if len(orderReqs) != 2 {
		t.Fatalf("expected TP/SL fallback retry, got %#v", orderReqs)
	}
	firstAttach := orderReqs[0].AttachAlgoOrds[0]
	if firstAttach["tpTriggerRatio"] != "-0.0015" || firstAttach["slTriggerRatio"] != "0.002" || firstAttach["tpTriggerPxType"] != "last" || firstAttach["slTriggerPxType"] != "last" {
		t.Fatalf("bad first tp/sl attach: %#v", firstAttach)
	}
	secondAttach := orderReqs[1].AttachAlgoOrds[0]
	if secondAttach["tpTriggerRatio"] != "" || secondAttach["slTriggerRatio"] != "" || secondAttach["tpTriggerPx"] != "602" || secondAttach["slTriggerPx"] != "604.3" {
		t.Fatalf("bad fallback tp/sl attach: %#v", secondAttach)
	}
	if secondAttach["tpOrdPx"] != "-1" || secondAttach["slOrdPx"] != "-1" || secondAttach["tpTriggerPxType"] != "last" || secondAttach["slTriggerPxType"] != "last" {
		t.Fatalf("bad fallback tp/sl order fields: %#v", secondAttach)
	}
	if orderReqs[1].ClOrdID != orderReqs[0].ClOrdID || orderReqs[1].Sz != orderReqs[0].Sz {
		t.Fatalf("fallback should retry same entry details: first=%#v second=%#v", orderReqs[0], orderReqs[1])
	}
}

func TestTraderExecuteSignalRetriesTrailingWithCallbackSpreadOnOKX54079(t *testing.T) {
	var orderReqs []PlaceOrderRequest
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v5/account/positions":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[]}`))
		case "/api/v5/trade/orders-pending":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[]}`))
		case "/api/v5/account/set-leverage":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{}]}`))
		case "/api/v5/trade/order":
			var req PlaceOrderRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			orderReqs = append(orderReqs, req)
			if len(orderReqs) == 1 {
				_, _ = w.Write([]byte(`{"code":"1","msg":"All operations failed","data":[{"sCode":"54079","sMsg":"Dynamic change is available only for futures trading in futures mode or multi-currency mode."}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"clOrdId":"x","ordId":"456","sCode":"0","sMsg":""}]}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer ts.Close()

	cfg := config.Default()
	cfg.Trading.BaseURL = ts.URL
	trailingPct := trading.NewFlexibleFloat(1.5)
	signal := trading.Signal{
		Action:   trading.ActionLong,
		Coinpair: "BTC",
		Price:    trading.NewFlexibleFloat(50000),
		SentAt:   "2026-07-24T03:00:00Z",
		Ticker:   "BTCUSDT",
		Leverage: 5,
		Amount:   trading.NewFlexibleFloat(100),
		Risk:     trading.Risk{Type: trading.RiskTrailing, TrailingPct: &trailingPct},
	}
	trader := Trader{
		Credentials: Credentials{APIKey: "key", SecretKey: "secret", Passphrase: "pass"},
		HTTPClient:  ts.Client(),
	}
	result, err := trader.ExecuteSignal(context.Background(), signal, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if result.OrdID != "456" {
		t.Fatalf("ord id = %q", result.OrdID)
	}
	if len(orderReqs) != 2 {
		t.Fatalf("expected trailing fallback retry, got %#v", orderReqs)
	}
	firstAttach := orderReqs[0].AttachAlgoOrds[0]
	if firstAttach["ordType"] != "move_order_stop" || firstAttach["callbackRatio"] != "0.015" || firstAttach["callbackSpread"] != "" {
		t.Fatalf("bad first trailing attach: %#v", firstAttach)
	}
	secondAttach := orderReqs[1].AttachAlgoOrds[0]
	if secondAttach["ordType"] != "move_order_stop" || secondAttach["callbackRatio"] != "" || secondAttach["callbackSpread"] != "750" {
		t.Fatalf("bad fallback trailing attach: %#v", secondAttach)
	}
	if orderReqs[1].ClOrdID != orderReqs[0].ClOrdID || orderReqs[1].Sz != orderReqs[0].Sz {
		t.Fatalf("fallback should retry same entry details: first=%#v second=%#v", orderReqs[0], orderReqs[1])
	}
}

func TestTraderExecuteSignalResolvesTradingViewTickerWithoutConfiguredSymbol(t *testing.T) {
	var orderReq PlaceOrderRequest
	var instrumentSeen bool
	var tickerSeen bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v5/public/instruments":
			instrumentSeen = true
			if r.URL.Query().Get("instId") != "ETH-USDT-SWAP" {
				t.Fatalf("instId query = %q", r.URL.Query().Get("instId"))
			}
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instId":"ETH-USDT-SWAP","ctVal":"0.1","lotSz":"0.01","minSz":"0.01"}]}`))
		case "/api/v5/market/ticker":
			tickerSeen = true
			if r.URL.Query().Get("instId") != "ETH-USDT-SWAP" {
				t.Fatalf("ticker instId query = %q", r.URL.Query().Get("instId"))
			}
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instId":"ETH-USDT-SWAP","bidPx":"2499","askPx":"2501","last":"2500"}]}`))
		case "/api/v5/account/positions":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[]}`))
		case "/api/v5/trade/orders-pending":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[]}`))
		case "/api/v5/account/set-leverage":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{}]}`))
		case "/api/v5/trade/order":
			if err := json.NewDecoder(r.Body).Decode(&orderReq); err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"clOrdId":"x","ordId":"123","sCode":"0","sMsg":""}]}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer ts.Close()

	cfg := config.Default()
	cfg.Symbols = map[string]config.SymbolConfig{}
	cfg.Trading.BaseURL = ts.URL
	cfg.Trading.OrderType = string(trading.OrderTypeLimit)
	signal := trading.Signal{
		Action:   trading.ActionShort,
		Coinpair: "OKX:ETHUSDT.P",
		Price:    trading.NewFlexibleFloat(2000),
		SentAt:   "2026-07-24T03:00:00Z",
		Ticker:   "OKX:ETHUSDT.P",
		Leverage: 3,
		Amount:   trading.NewFlexibleFloat(100),
	}
	trader := Trader{
		Credentials: Credentials{APIKey: "key", SecretKey: "secret", Passphrase: "pass"},
		HTTPClient:  ts.Client(),
	}
	if _, err := trader.ExecuteSignal(context.Background(), signal, cfg); err != nil {
		t.Fatal(err)
	}
	if !instrumentSeen {
		t.Fatal("expected public instrument lookup")
	}
	if !tickerSeen {
		t.Fatal("expected public ticker lookup")
	}
	if orderReq.InstID != "ETH-USDT-SWAP" || orderReq.Side != "sell" || orderReq.OrdType != "limit" || orderReq.Px != "2507.5" || orderReq.Sz != "0.39" {
		t.Fatalf("bad dynamic order request: %#v", orderReq)
	}
}

func TestTraderExecuteSignalRejectsMismatchedOKXUnderlyingBeforeOrder(t *testing.T) {
	var unexpectedRequests []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v5/public/instruments" {
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instId":"STRK-USDT-SWAP","uly":"TSLA-USDT","instFamily":"STRK-USDT","ctVal":"0.01","tickSz":"0.000002","lotSz":"1","minSz":"1"}]}`))
			return
		}
		unexpectedRequests = append(unexpectedRequests, r.URL.Path)
		_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[]}`))
	}))
	defer ts.Close()

	cfg := config.Default()
	cfg.Symbols = map[string]config.SymbolConfig{}
	cfg.Trading.BaseURL = ts.URL
	signal := trading.Signal{
		Action:         trading.ActionLong,
		Coinpair:       "STRKUSDT.P",
		Price:          trading.NewFlexibleFloat(0.02579),
		SentAt:         "2026-08-30T04:10:00Z",
		Ticker:         "OKX:STRKUSDT.P",
		Leverage:       10,
		Amount:         trading.NewFlexibleFloat(1000),
		PositionEffect: trading.PositionEffectOpen,
	}
	trader := Trader{
		Credentials: Credentials{APIKey: "key", SecretKey: "secret", Passphrase: "pass"},
		HTTPClient:  ts.Client(),
	}
	_, err := trader.ExecuteSignal(context.Background(), signal, cfg)
	if err == nil || !strings.Contains(err.Error(), "unsafe OKX instrument mapping") || !strings.Contains(err.Error(), "TSLA-USDT") {
		t.Fatalf("mismatched underlying error = %v", err)
	}
	if len(unexpectedRequests) != 0 {
		t.Fatalf("mismatched underlying reached private/order endpoints: %#v", unexpectedRequests)
	}
}

func TestTraderExecuteSignalRefreshesStaleShortLimitPriceFromOKXTicker(t *testing.T) {
	var orderReq PlaceOrderRequest
	var tickerSeen bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v5/public/instruments":
			if r.URL.Query().Get("instId") != "STRK-USDT-SWAP" {
				t.Fatalf("instId query = %q", r.URL.Query().Get("instId"))
			}
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instId":"STRK-USDT-SWAP","ctVal":"1","tickSz":"0.0001","lotSz":"1","minSz":"1"}]}`))
		case "/api/v5/market/ticker":
			tickerSeen = true
			if r.URL.Query().Get("instId") != "STRK-USDT-SWAP" {
				t.Fatalf("ticker instId query = %q", r.URL.Query().Get("instId"))
			}
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instId":"STRK-USDT-SWAP","bidPx":"239.3","askPx":"239.5","last":"200"}]}`))
		case "/api/v5/account/positions":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[]}`))
		case "/api/v5/trade/orders-pending":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[]}`))
		case "/api/v5/account/set-leverage":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{}]}`))
		case "/api/v5/trade/order":
			if err := json.NewDecoder(r.Body).Decode(&orderReq); err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"clOrdId":"x","ordId":"123","sCode":"0","sMsg":""}]}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer ts.Close()

	cfg := config.Default()
	cfg.Symbols = map[string]config.SymbolConfig{}
	cfg.Trading.BaseURL = ts.URL
	cfg.Trading.OrderType = string(trading.OrderTypeLimit)
	signal := trading.Signal{
		Action:   trading.ActionShort,
		Coinpair: "STRKUSDT.P",
		Price:    trading.NewFlexibleFloat(200),
		SentAt:   "2026-08-10T07:00:05Z",
		Ticker:   "OKX:STRKUSDT.P",
		Leverage: 1,
		Amount:   trading.NewFlexibleFloat(500),
	}
	trader := Trader{
		Credentials: Credentials{APIKey: "key", SecretKey: "secret", Passphrase: "pass"},
		HTTPClient:  ts.Client(),
	}
	if _, err := trader.ExecuteSignal(context.Background(), signal, cfg); err != nil {
		t.Fatal(err)
	}
	if !tickerSeen {
		t.Fatal("expected public ticker lookup")
	}
	if orderReq.InstID != "STRK-USDT-SWAP" || orderReq.Side != "sell" || orderReq.OrdType != "limit" || orderReq.Px != "240.1182" || orderReq.Sz != "2" {
		t.Fatalf("bad refreshed limit order request: %#v", orderReq)
	}
}

func TestTraderExecuteSignalUsesSelectedAPIIDCredentials(t *testing.T) {
	var seenAPIKey string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v5/account/positions":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[]}`))
		case "/api/v5/trade/orders-pending":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[]}`))
		case "/api/v5/account/set-leverage":
			seenAPIKey = r.Header.Get("OK-ACCESS-KEY")
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{}]}`))
		case "/api/v5/trade/order":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"clOrdId":"x","ordId":"123","sCode":"0","sMsg":""}]}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer ts.Close()

	cfg := config.Default()
	cfg.Trading.BaseURL = ts.URL
	signal := trading.Signal{
		Action:   trading.ActionLong,
		APIID:    "backup",
		Coinpair: "BTC",
		Price:    trading.NewFlexibleFloat(50000),
		SentAt:   "2026-07-24T03:00:00Z",
		Ticker:   "BTCUSDT",
		Leverage: 5,
		Amount:   trading.NewFlexibleFloat(100),
	}
	trader := Trader{
		CredentialProvider: credentialProviderFunc(func(apiID string) (Credentials, string, error) {
			if apiID != "backup" {
				return Credentials{}, apiID, fmt.Errorf("unexpected api id %q", apiID)
			}
			return Credentials{APIKey: "backup-key", SecretKey: "backup-secret", Passphrase: "backup-pass"}, apiID, nil
		}),
		HTTPClient: ts.Client(),
	}
	result, err := trader.ExecuteSignal(context.Background(), signal, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if result.APIID != "backup" || seenAPIKey != "backup-key" {
		t.Fatalf("api result=%q header=%q", result.APIID, seenAPIKey)
	}
}

func TestTraderExecuteSignalFallsBackWhenLeverageExceedsMaximum(t *testing.T) {
	var tried []string
	var orderReq PlaceOrderRequest
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v5/account/positions":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[]}`))
		case "/api/v5/trade/orders-pending":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[]}`))
		case "/api/v5/account/set-leverage":
			var req SetLeverageRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			tried = append(tried, req.Lever)
			if req.Lever != "7" {
				_, _ = w.Write([]byte(`{"code":"1","msg":"All operations failed","data":[{"sCode":"59102","sMsg":"Leverage exceeds the maximum limit. Please lower the leverage."}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{}]}`))
		case "/api/v5/trade/order":
			if err := json.NewDecoder(r.Body).Decode(&orderReq); err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"clOrdId":"x","ordId":"123","sCode":"0","sMsg":""}]}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer ts.Close()

	cfg := config.Default()
	cfg.Trading.BaseURL = ts.URL
	signal := trading.Signal{
		Action:   trading.ActionShort,
		Coinpair: "BTC",
		Price:    trading.NewFlexibleFloat(50000),
		SentAt:   "2026-07-24T03:00:00Z",
		Ticker:   "BTCUSDT",
		Leverage: 10,
		Amount:   trading.NewFlexibleFloat(100),
	}
	trader := Trader{
		Credentials: Credentials{APIKey: "key", SecretKey: "secret", Passphrase: "pass"},
		HTTPClient:  ts.Client(),
	}
	result, err := trader.ExecuteSignal(context.Background(), signal, cfg)
	if err != nil {
		t.Fatal(err)
	}
	wantTried := []string{"10", "9", "8", "7"}
	if fmt.Sprint(tried) != fmt.Sprint(wantTried) {
		t.Fatalf("leverage attempts = %#v, want %#v", tried, wantTried)
	}
	if result.Leverage != 7 {
		t.Fatalf("result leverage = %d", result.Leverage)
	}
	if orderReq.InstID != "BTC-USDT-SWAP" || orderReq.Side != "sell" {
		t.Fatalf("bad order request after leverage fallback: %#v", orderReq)
	}
}

func TestTraderExecuteSignalRetriesWithOKXMaxPositionSizeLimit(t *testing.T) {
	var orderReqs []PlaceOrderRequest
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v5/public/instruments":
			if r.URL.Query().Get("instId") != "ZAMA-USDT-SWAP" {
				t.Fatalf("bad instruments query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instId":"ZAMA-USDT-SWAP","ctVal":"1","tickSz":"0.0001","lotSz":"1","minSz":"1"}]}`))
		case "/api/v5/account/positions":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[]}`))
		case "/api/v5/trade/orders-pending":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[]}`))
		case "/api/v5/account/set-leverage":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{}]}`))
		case "/api/v5/trade/order":
			var req PlaceOrderRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			orderReqs = append(orderReqs, req)
			if len(orderReqs) == 1 {
				_, _ = w.Write([]byte(`{"code":"1","msg":"All operations failed","data":[{"sCode":"51004","sMsg":"Order failed. For buy/sell mode of ZAMA-USDT-SWAP, the sum of current buy order size, position quantity, and pending buy orders can't be more than 20(contracts) which is the maximum position amount under current leverage. Please lower the leverage or use a new sub-account to place the order again (current leverage: 5x, current buy order size: 216 contracts, position quantity: 0 contracts, pending buy orders: 0 contracts)."}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"clOrdId":"x","ordId":"zama-20","sCode":"0","sMsg":""}]}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer ts.Close()

	cfg := config.Default()
	cfg.Symbols = map[string]config.SymbolConfig{}
	cfg.Trading.BaseURL = ts.URL
	signal := trading.Signal{
		Action:   trading.ActionLong,
		Coinpair: "ZAMAUSDT.P",
		Price:    trading.NewFlexibleFloat(4.62),
		SentAt:   "2026-08-12T06:15:03Z",
		Ticker:   "OKX:ZAMAUSDT.P",
		Leverage: 5,
		Amount:   trading.NewFlexibleFloat(1000),
	}
	trader := Trader{
		Credentials: Credentials{APIKey: "key", SecretKey: "secret", Passphrase: "pass"},
		HTTPClient:  ts.Client(),
	}
	result, err := trader.ExecuteSignal(context.Background(), signal, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if result.OrdID != "zama-20" {
		t.Fatalf("ord id = %q", result.OrdID)
	}
	if len(orderReqs) != 2 {
		t.Fatalf("expected max position retry, got %#v", orderReqs)
	}
	if orderReqs[0].Sz != "216" || orderReqs[1].Sz != "20" {
		t.Fatalf("bad retry sizes: %#v", orderReqs)
	}
	if orderReqs[1].ClOrdID != orderReqs[0].ClOrdID || orderReqs[1].Side != "buy" {
		t.Fatalf("retry should preserve order identity and side: first=%#v second=%#v", orderReqs[0], orderReqs[1])
	}
}

func TestTraderExecuteSignalRefreshesStaleConfiguredInstrumentAfterOKX51001(t *testing.T) {
	var orderReqs []PlaceOrderRequest
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v5/account/positions", "/api/v5/trade/orders-pending":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[]}`))
		case "/api/v5/account/set-leverage":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{}]}`))
		case "/api/v5/public/instruments":
			if r.URL.Query().Get("instId") != "PLUME-USDT-SWAP" {
				t.Fatalf("instrument refresh query = %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instId":"PLUME-USDT-SWAP","ctVal":"10","tickSz":"0.000001","lotSz":"1","minSz":"1"}]}`))
		case "/api/v5/trade/order":
			var req PlaceOrderRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			orderReqs = append(orderReqs, req)
			if len(orderReqs) == 1 {
				if req.InstID != "PLUME-OLD-USDT-SWAP" {
					t.Fatalf("initial configured instrument = %#v", req)
				}
				_, _ = w.Write([]byte(`{"code":"1","msg":"All operations failed","data":[{"sCode":"51001","sMsg":"Instrument ID, Instrument ID code, or Spread ID doesn't exist."}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"clOrdId":"x","ordId":"plume-order","sCode":"0","sMsg":""}]}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer ts.Close()

	cfg := config.Default()
	cfg.Trading.BaseURL = ts.URL
	cfg.Trading.RiskType = string(trading.RiskNone)
	cfg.Symbols = map[string]config.SymbolConfig{
		"PLUMEUSDT.P": {
			Coinpair: "PLUMEUSDT.P",
			InstID:   "PLUME-OLD-USDT-SWAP",
			CtVal:    1,
			TickSz:   0.000001,
			LotSz:    1,
			MinSz:    1,
		},
	}
	trader := Trader{Credentials: Credentials{APIKey: "key", SecretKey: "secret", Passphrase: "pass"}, HTTPClient: ts.Client()}
	result, err := trader.ExecuteSignal(context.Background(), trading.Signal{
		Action:   trading.ActionLong,
		Coinpair: "PLUMEUSDT.P",
		Ticker:   "OKX:PLUMEUSDT.P",
		Price:    trading.NewFlexibleFloat(2),
		Leverage: 5,
		Amount:   trading.NewFlexibleFloat(100),
	}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if result.InstID != "PLUME-USDT-SWAP" || result.OrdID != "plume-order" {
		t.Fatalf("bad refreshed result: %#v", result)
	}
	if len(orderReqs) != 2 || orderReqs[1].InstID != "PLUME-USDT-SWAP" || orderReqs[1].Sz != "5" {
		t.Fatalf("expected stale instrument retry with refreshed size, got %#v", orderReqs)
	}
}

func TestTraderExecuteSignalRetriesMarketOrderAsProtectedLimitAfterOKX51006(t *testing.T) {
	var orderReqs []PlaceOrderRequest
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v5/public/instruments":
			if r.URL.Query().Get("instId") != "H-USDT-SWAP" {
				t.Fatalf("instrument query = %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instId":"H-USDT-SWAP","ctVal":"100","tickSz":"0.00001","lotSz":"0.1","minSz":"0.1"}]}`))
		case "/api/v5/account/positions", "/api/v5/trade/orders-pending":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[]}`))
		case "/api/v5/account/set-leverage":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{}]}`))
		case "/api/v5/market/ticker":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instId":"H-USDT-SWAP","bidPx":"0.0653","askPx":"0.0654","last":"0.06535"}]}`))
		case "/api/v5/trade/order":
			var req PlaceOrderRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			orderReqs = append(orderReqs, req)
			if len(orderReqs) == 1 {
				if req.OrdType != "market" || req.Px != "" || req.Sz != "500" {
					t.Fatalf("unexpected initial market order: %#v", req)
				}
				_, _ = w.Write([]byte(`{"code":"1","msg":"All operations failed","data":[{"sCode":"51006","sMsg":"Order price is not within the price limit (max buy price: 0.0655, min sell price: 0.0595)"}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"clOrdId":"x","ordId":"h-limit-order","sCode":"0","sMsg":""}]}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer ts.Close()

	cfg := config.Default()
	cfg.Symbols = map[string]config.SymbolConfig{}
	cfg.Trading.BaseURL = ts.URL
	cfg.Trading.RiskType = string(trading.RiskNone)
	cfg.Trading.OrderType = string(trading.OrderTypeMarket)
	trader := Trader{Credentials: Credentials{APIKey: "key", SecretKey: "secret", Passphrase: "pass"}, HTTPClient: ts.Client()}
	result, err := trader.ExecuteSignal(context.Background(), trading.Signal{
		Action:   trading.ActionLong,
		Coinpair: "HUSDT.P",
		Ticker:   "OKX:HUSDT.P",
		Price:    trading.NewFlexibleFloat(0.06),
		Leverage: 10,
		Amount:   trading.NewFlexibleFloat(3000),
	}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if result.OrdType != "limit" || result.Px != "0.0654" || result.OrdID != "h-limit-order" {
		t.Fatalf("bad protected limit result: %#v", result)
	}
	if len(orderReqs) != 2 || orderReqs[1].OrdType != "limit" || orderReqs[1].Px != "0.0654" || orderReqs[1].Sz != "458.7" {
		t.Fatalf("expected protected limit retry, got %#v", orderReqs)
	}
}

func TestParseOKXMaxPositionLimit(t *testing.T) {
	msg := "Order failed. For buy/sell mode of ZAMA-USDT-SWAP, the sum of current buy order size, position quantity, and pending buy orders can't be more than 20(contracts) which is the maximum position amount under current leverage. Please lower the leverage or use a new sub-account to place the order again (current leverage: 5x, current buy order size: 216 contracts, position quantity: 0 contracts, pending buy orders: 0 contracts)."
	limit, ok := parseOKXMaxPositionLimit(msg)
	if !ok {
		t.Fatal("expected max position limit")
	}
	if limit.Max != 20 || limit.CurrentOrder != 216 || limit.Position != 0 || limit.Pending != 0 {
		t.Fatalf("bad limit: %#v", limit)
	}
	apiErr := APIError{Code: "1", Msg: "All operations failed", Data: json.RawMessage(`[{"sCode":"51004","sMsg":` + strconv.Quote(msg) + `}]`)}
	limitFromErr, ok := okxMaxPositionLimitFromError(apiErr)
	if !ok {
		t.Fatal("expected max position limit from error")
	}
	if limitFromErr.Max != 20 || limitFromErr.CurrentOrder != 216 || limitFromErr.Position != 0 || limitFromErr.Pending != 0 {
		t.Fatalf("bad error limit: %#v", limitFromErr)
	}
	nextSize, err := trading.SizeFromContracts(limitFromErr.Max-limitFromErr.Position-limitFromErr.Pending, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if nextSize != "20" {
		t.Fatalf("direct fallback size = %q, want 20", nextSize)
	}
	fallbackReq, ok := maxPositionFallbackRequest(PlaceOrderRequest{Sz: "216"}, apiErr, 1, 1)
	if !ok {
		t.Fatal("expected max position fallback request")
	}
	if fallbackReq.Sz != "20" {
		t.Fatalf("fallback size = %q, want 20", fallbackReq.Sz)
	}
}

func TestTraderExecuteSignalKeepsSameDirectionPositionAndOrderAsAdd(t *testing.T) {
	var entryReq PlaceOrderRequest
	var cancelCalled bool
	var algoQueried bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v5/account/positions":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instType":"SWAP","instId":"BTC-USDT-SWAP","mgnMode":"isolated","posSide":"net","pos":"1","availPos":"1"}]}`))
		case "/api/v5/trade/orders-pending":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instType":"SWAP","instId":"BTC-USDT-SWAP","ordId":"100","tdMode":"isolated","side":"buy","posSide":"net","ordType":"limit","sz":"1","accFillSz":"0","state":"live"}]}`))
		case "/api/v5/trade/orders-algo-pending":
			algoQueried = true
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[]}`))
		case "/api/v5/trade/cancel-order", "/api/v5/trade/cancel-algos":
			cancelCalled = true
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[]}`))
		case "/api/v5/account/set-leverage":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{}]}`))
		case "/api/v5/trade/order":
			if err := json.NewDecoder(r.Body).Decode(&entryReq); err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"clOrdId":"entry","ordId":"123","sCode":"0","sMsg":""}]}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer ts.Close()

	cfg := config.Default()
	cfg.Trading.BaseURL = ts.URL
	cfg.Trading.RiskType = string(trading.RiskNone)
	trader := Trader{Credentials: Credentials{APIKey: "key", SecretKey: "secret", Passphrase: "pass"}, HTTPClient: ts.Client()}
	_, err := trader.ExecuteSignal(context.Background(), trading.Signal{
		Action:   trading.ActionLong,
		Coinpair: "BTC",
		Price:    trading.NewFlexibleFloat(50000),
		Leverage: 5,
		Amount:   trading.NewFlexibleFloat(100),
	}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if cancelCalled || algoQueried {
		t.Fatalf("same direction add should not cancel or query algos cancelCalled=%v algoQueried=%v", cancelCalled, algoQueried)
	}
	if entryReq.Side != "buy" || entryReq.ReduceOnly || entryReq.Sz != "0.2" {
		t.Fatalf("bad add entry request: %#v", entryReq)
	}
}

func TestTraderExecuteSignalSkipsLeverageWhenOKXRemoteMatches(t *testing.T) {
	var leverageCalled bool
	var entryReq PlaceOrderRequest
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v5/account/positions":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instType":"SWAP","instId":"BTC-USDT-SWAP","mgnMode":"isolated","posSide":"net","pos":"0","availPos":"0","lever":"5"}]}`))
		case "/api/v5/trade/orders-pending":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[]}`))
		case "/api/v5/account/set-leverage":
			leverageCalled = true
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{}]}`))
		case "/api/v5/trade/order":
			if err := json.NewDecoder(r.Body).Decode(&entryReq); err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"clOrdId":"entry","ordId":"123","sCode":"0","sMsg":""}]}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer ts.Close()

	cfg := config.Default()
	cfg.Trading.BaseURL = ts.URL
	cfg.Trading.RiskType = string(trading.RiskNone)
	trader := Trader{Credentials: Credentials{APIKey: "key", SecretKey: "secret", Passphrase: "pass"}, HTTPClient: ts.Client()}
	result, err := trader.ExecuteSignal(context.Background(), trading.Signal{
		Action:   trading.ActionLong,
		Coinpair: "BTC",
		Price:    trading.NewFlexibleFloat(50000),
		Leverage: 5,
		Amount:   trading.NewFlexibleFloat(100),
	}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if leverageCalled {
		t.Fatal("matching OKX remote leverage should not be set again")
	}
	if result.Leverage != 5 || entryReq.Side != "buy" {
		t.Fatalf("bad result/order after leverage skip: result=%#v req=%#v", result, entryReq)
	}
}

func TestTraderExecuteSignalSetsLeverageWhenOKXRemoteDiffers(t *testing.T) {
	var leverageReq SetLeverageRequest
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v5/account/positions":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instType":"SWAP","instId":"BTC-USDT-SWAP","mgnMode":"isolated","posSide":"net","pos":"0","availPos":"0","lever":"3"}]}`))
		case "/api/v5/trade/orders-pending":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[]}`))
		case "/api/v5/account/set-leverage":
			if err := json.NewDecoder(r.Body).Decode(&leverageReq); err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{}]}`))
		case "/api/v5/trade/order":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"clOrdId":"entry","ordId":"123","sCode":"0","sMsg":""}]}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer ts.Close()

	cfg := config.Default()
	cfg.Trading.BaseURL = ts.URL
	cfg.Trading.RiskType = string(trading.RiskNone)
	trader := Trader{Credentials: Credentials{APIKey: "key", SecretKey: "secret", Passphrase: "pass"}, HTTPClient: ts.Client()}
	if _, err := trader.ExecuteSignal(context.Background(), trading.Signal{
		Action:   trading.ActionLong,
		Coinpair: "BTC",
		Price:    trading.NewFlexibleFloat(50000),
		Leverage: 5,
		Amount:   trading.NewFlexibleFloat(100),
	}, cfg); err != nil {
		t.Fatal(err)
	}
	if leverageReq.InstID != "BTC-USDT-SWAP" || leverageReq.Lever != "5" || leverageReq.MgnMode != "isolated" {
		t.Fatalf("bad OKX leverage setup request: %#v", leverageReq)
	}
}

func TestTraderExecuteSignalCancelsReversePendingOrderBeforeNewDirection(t *testing.T) {
	var paths []string
	var canceled CancelOrderRequest
	var entryReq PlaceOrderRequest
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v5/account/positions":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[]}`))
		case "/api/v5/trade/orders-pending":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instType":"SWAP","instId":"BTC-USDT-SWAP","ordId":"100","tdMode":"isolated","side":"buy","posSide":"net","ordType":"limit","sz":"1","accFillSz":"0","state":"live"}]}`))
		case "/api/v5/trade/cancel-order":
			if err := json.NewDecoder(r.Body).Decode(&canceled); err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"ordId":"100","sCode":"0","sMsg":""}]}`))
		case "/api/v5/trade/orders-algo-pending":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[]}`))
		case "/api/v5/account/set-leverage":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{}]}`))
		case "/api/v5/trade/order":
			if err := json.NewDecoder(r.Body).Decode(&entryReq); err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"clOrdId":"entry","ordId":"123","sCode":"0","sMsg":""}]}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer ts.Close()

	cfg := config.Default()
	cfg.Trading.BaseURL = ts.URL
	cfg.Trading.RiskType = string(trading.RiskNone)
	trader := Trader{Credentials: Credentials{APIKey: "key", SecretKey: "secret", Passphrase: "pass"}, HTTPClient: ts.Client()}
	_, err := trader.ExecuteSignal(context.Background(), trading.Signal{
		Action:   trading.ActionShort,
		Coinpair: "BTC",
		Price:    trading.NewFlexibleFloat(50000),
		Leverage: 5,
		Amount:   trading.NewFlexibleFloat(100),
	}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if canceled.InstID != "BTC-USDT-SWAP" || canceled.OrdID != "100" {
		t.Fatalf("bad canceled order: %#v", canceled)
	}
	if entryReq.Side != "sell" || entryReq.ReduceOnly {
		t.Fatalf("bad reverse entry request: %#v", entryReq)
	}
	if pathIndex(paths, "/api/v5/trade/cancel-order") > pathIndex(paths, "/api/v5/trade/order") {
		t.Fatalf("entry placed before reverse order cancellation: %#v", paths)
	}
}

func TestTraderExecuteSignalClosesReversePositionBeforeNewDirection(t *testing.T) {
	var positionCalls int
	var cancelAlgoReq []CancelAlgoOrderRequest
	var orderReqs []PlaceOrderRequest
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v5/account/positions":
			positionCalls++
			if positionCalls <= 2 {
				_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instType":"SWAP","instId":"BTC-USDT-SWAP","mgnMode":"isolated","posSide":"net","pos":"2","availPos":"2"}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[]}`))
		case "/api/v5/trade/orders-pending":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[]}`))
		case "/api/v5/trade/orders-algo-pending":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instType":"SWAP","instId":"BTC-USDT-SWAP","algoId":"900","side":"sell","posSide":"net","ordType":"conditional","sz":"2","state":"live"}]}`))
		case "/api/v5/trade/cancel-algos":
			if err := json.NewDecoder(r.Body).Decode(&cancelAlgoReq); err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"algoId":"900","sCode":"0","sMsg":""}]}`))
		case "/api/v5/trade/order":
			var req PlaceOrderRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			orderReqs = append(orderReqs, req)
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"clOrdId":"ok","ordId":"123","sCode":"0","sMsg":""}]}`))
		case "/api/v5/account/set-leverage":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{}]}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer ts.Close()

	cfg := config.Default()
	cfg.Trading.BaseURL = ts.URL
	cfg.Trading.RiskType = string(trading.RiskNone)
	trader := Trader{Credentials: Credentials{APIKey: "key", SecretKey: "secret", Passphrase: "pass"}, HTTPClient: ts.Client()}
	_, err := trader.ExecuteSignal(context.Background(), trading.Signal{
		Action:   trading.ActionShort,
		Coinpair: "BTC",
		Price:    trading.NewFlexibleFloat(50000),
		Leverage: 5,
		Amount:   trading.NewFlexibleFloat(100),
	}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(cancelAlgoReq) != 1 || cancelAlgoReq[0].AlgoID != "900" {
		t.Fatalf("bad cancel algo request: %#v", cancelAlgoReq)
	}
	if len(orderReqs) != 2 {
		t.Fatalf("expected close and entry orders, got %#v", orderReqs)
	}
	closeReq, entryReq := orderReqs[0], orderReqs[1]
	if closeReq.Side != "sell" || closeReq.OrdType != "market" || closeReq.Sz != "2" || !closeReq.ReduceOnly || len(closeReq.AttachAlgoOrds) != 0 {
		t.Fatalf("bad close request: %#v", closeReq)
	}
	if entryReq.Side != "sell" || entryReq.ReduceOnly || entryReq.Sz != "0.2" {
		t.Fatalf("bad entry request after close: %#v", entryReq)
	}
}

func TestDeriveSwapInstrumentID(t *testing.T) {
	tests := map[string]string{
		"BTC":             "BTC-USDT-SWAP",
		"BTCUSDT":         "BTC-USDT-SWAP",
		"OKX:ETHUSDT.P":   "ETH-USDT-SWAP",
		"BINANCE:SOLUSDT": "SOL-USDT-SWAP",
		"ETH-USDT":        "ETH-USDT-SWAP",
	}
	for raw, want := range tests {
		got, _, err := DeriveSwapInstrumentID(raw, "")
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if got != want {
			t.Fatalf("%s => %s, want %s", raw, got, want)
		}
	}
}

func pathIndex(paths []string, want string) int {
	for i, path := range paths {
		if path == want {
			return i
		}
	}
	return len(paths) + 1
}
