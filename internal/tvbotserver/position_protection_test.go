package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotbinance"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotconfig"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotokx"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

func TestTVBotOKXPositionProtectionOrders(t *testing.T) {
	srv := newTestServer(t)
	var orderBodies []map[string]any
	okxServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v5/account/positions":
			if r.URL.Query().Get("instType") != "SWAP" {
				t.Fatalf("bad positions query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instType":"SWAP","instId":"BTC-USDT-SWAP","mgnMode":"isolated","posSide":"long","pos":"2","availPos":"2","avgPx":"100","markPx":"101","upl":"2","lever":"10","margin":"20"}]}`))
		case "/api/v5/public/instruments":
			if r.URL.Query().Get("instId") != "BTC-USDT-SWAP" {
				t.Fatalf("bad instruments query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instId":"BTC-USDT-SWAP","tickSz":"0.1","ctVal":"1","lotSz":"1","minSz":"1"}]}`))
		case "/api/v5/trade/order-algo":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			orderBodies = append(orderBodies, body)
			algoClOrdID, _ := body["algoClOrdId"].(string)
			_, _ = w.Write([]byte(fmt.Sprintf(`{"code":"0","msg":"","data":[{"algoId":"%d","algoClOrdId":%q,"sCode":"0","sMsg":""}]}`, len(orderBodies)+900, algoClOrdID)))
		default:
			t.Fatalf("unexpected OKX path %s", r.URL.Path)
		}
	}))
	defer okxServer.Close()
	cfg := srv.ConfigStore.Get()
	cfg.Trading.BaseURL = okxServer.URL
	cfg.Trading.TakeProfitPct = 2.5
	cfg.Trading.StopLossPct = 1.25
	cfg.Trading.TrailingPct = 3.5
	srv.ConfigStore = config.NewStore("", cfg)
	srv.OKXHTTPClient = okxServer.Client()
	if _, err := srv.OKXCredentials.UpdateAccount(okx.CredentialAccountUpdate{
		ID:          "default",
		Active:      true,
		Credentials: okx.Credentials{APIKey: "key", SecretKey: "secret", Passphrase: "pass"},
	}); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		kind      string
		triggerPx string
		callback  string
	}{
		{kind: "tp", triggerPx: "100.3"},
		{kind: "sl", triggerPx: "99.8"},
		{kind: "trailing", callback: "0.035"},
	} {
		body := []byte(`{"exchange":"okx","api_id":"default","inst_id":"BTC-USDT-SWAP","pos_side":"long","kind":"` + tc.kind + `"}`)
		req := httptest.NewRequest(http.MethodPost, "/tvbot/positions/protection", bytes.NewReader(body))
		req.SetBasicAuth("admin", "Admin123")
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s protection status=%d body=%s", tc.kind, rr.Code, rr.Body.String())
		}
		var resp positionProtectionResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if !resp.OK || resp.Exchange != trading.ExchangeOKX || resp.APIID != "default" || resp.Kind != tc.kind || resp.Sz != "2" || resp.TriggerPx != tc.triggerPx || resp.CallbackRatio != tc.callback {
			t.Fatalf("bad %s protection response: %#v", tc.kind, resp)
		}
	}
	if len(orderBodies) != 3 {
		t.Fatalf("expected three OKX algo orders, got %#v", orderBodies)
	}
	tp := orderBodies[0]
	if tp["instId"] != "BTC-USDT-SWAP" || tp["tdMode"] != "isolated" || tp["side"] != "sell" || tp["posSide"] != "long" || tp["ordType"] != "conditional" || tp["sz"] != "2" || tp["tpTriggerPx"] != "100.3" || tp["tpOrdPx"] != "-1" || tp["tpTriggerPxType"] != "mark" || tp["reduceOnly"] != true {
		t.Fatalf("bad OKX tp body: %#v", tp)
	}
	sl := orderBodies[1]
	if sl["side"] != "sell" || sl["ordType"] != "conditional" || sl["slTriggerPx"] != "99.8" || sl["slOrdPx"] != "-1" || sl["slTriggerPxType"] != "mark" || sl["reduceOnly"] != true {
		t.Fatalf("bad OKX sl body: %#v", sl)
	}
	trailing := orderBodies[2]
	if trailing["side"] != "sell" || trailing["ordType"] != "move_order_stop" || trailing["callbackRatio"] != "0.035" || trailing["reduceOnly"] != true {
		t.Fatalf("bad OKX trailing body: %#v", trailing)
	}
}

func TestTVBotBinancePositionProtectionOrders(t *testing.T) {
	srv := newTestServer(t)
	var algoForms []url.Values
	binanceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/fapi/v1/exchangeInfo" && r.Header.Get("X-MBX-APIKEY") != "binance-key" {
			t.Fatalf("missing Binance API key for %s", r.URL.Path)
		}
		switch r.URL.Path {
		case "/fapi/v3/positionRisk":
			if r.URL.Query().Get("symbol") != "ETHUSDT" {
				t.Fatalf("bad positions query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`[{"symbol":"ETHUSDT","positionSide":"SHORT","positionAmt":"-3","entryPrice":"100","markPrice":"99","unRealizedProfit":"3","notional":"297","marginAsset":"USDT","leverage":"10","marginType":"isolated","updateTime":1784880000000}]`))
		case "/fapi/v1/exchangeInfo":
			_, _ = w.Write([]byte(`{"symbols":[{"symbol":"ETHUSDT","status":"TRADING","pricePrecision":1,"quantityPrecision":3,"filters":[{"filterType":"PRICE_FILTER","tickSize":"0.1"},{"filterType":"LOT_SIZE","minQty":"0.001","stepSize":"0.001"}]}]}`))
		case "/fapi/v1/algoOrder":
			if r.Method != http.MethodPost {
				t.Fatalf("unexpected Binance algo method %s", r.Method)
			}
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			algoForms = append(algoForms, cloneValues(r.Form))
			_, _ = w.Write([]byte(fmt.Sprintf(`{"algoId":%d,"clientAlgoId":%q,"orderType":%q,"symbol":"ETHUSDT","side":"BUY","positionSide":"SHORT","quantity":"3","algoStatus":"NEW","triggerPrice":%q,"callbackRate":%q}`,
				len(algoForms)+700,
				r.Form.Get("clientAlgoId"),
				r.Form.Get("type"),
				r.Form.Get("triggerPrice"),
				r.Form.Get("callbackRate"),
			)))
		default:
			t.Fatalf("unexpected Binance path %s", r.URL.Path)
		}
	}))
	defer binanceServer.Close()
	cfg := srv.ConfigStore.Get()
	cfg.Trading.BinanceDemoBaseURL = binanceServer.URL
	cfg.Trading.TakeProfitPct = 2.5
	cfg.Trading.StopLossPct = 1
	cfg.Trading.TrailingPct = 2
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
		kind      string
		triggerPx string
		callback  string
	}{
		{kind: "tp", triggerPx: "99.7"},
		{kind: "sl", triggerPx: "100.1"},
		{kind: "trailing", callback: "2"},
	} {
		body := []byte(`{"exchange":"binance","api_id":"main","inst_id":"ETHUSDT","pos_side":"short","kind":"` + tc.kind + `"}`)
		req := httptest.NewRequest(http.MethodPost, "/tvbot/positions/protection", bytes.NewReader(body))
		req.SetBasicAuth("admin", "Admin123")
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s protection status=%d body=%s", tc.kind, rr.Code, rr.Body.String())
		}
		var resp positionProtectionResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if !resp.OK || resp.Exchange != trading.ExchangeBinance || resp.APIID != "main" || resp.Kind != tc.kind || resp.Sz != "3" || resp.TriggerPx != tc.triggerPx || resp.CallbackRatio != tc.callback {
			t.Fatalf("bad Binance %s protection response: %#v", tc.kind, resp)
		}
	}
	if len(algoForms) != 3 {
		t.Fatalf("expected three Binance algo orders, got %#v", algoForms)
	}
	tp := algoForms[0]
	if tp.Get("symbol") != "ETHUSDT" || tp.Get("side") != "BUY" || tp.Get("positionSide") != "SHORT" || tp.Get("type") != "TAKE_PROFIT_MARKET" || tp.Get("quantity") != "3" || tp.Get("triggerPrice") != "99.7" || tp.Get("workingType") != "MARK_PRICE" || tp.Get("reduceOnly") != "" {
		t.Fatalf("bad Binance tp form: %#v", tp)
	}
	sl := algoForms[1]
	if sl.Get("type") != "STOP_MARKET" || sl.Get("side") != "BUY" || sl.Get("triggerPrice") != "100.1" || sl.Get("positionSide") != "SHORT" {
		t.Fatalf("bad Binance sl form: %#v", sl)
	}
	trailing := algoForms[2]
	if trailing.Get("type") != "TRAILING_STOP_MARKET" || trailing.Get("side") != "BUY" || trailing.Get("callbackRate") != "2" || trailing.Get("triggerPrice") != "" || trailing.Get("positionSide") != "SHORT" {
		t.Fatalf("bad Binance trailing form: %#v", trailing)
	}
}

func TestTVBotPositionProtectionRejectsInvalidAndClosedInputs(t *testing.T) {
	srv := newTestServer(t)
	badKind := httptest.NewRequest(http.MethodPost, "/tvbot/positions/protection", bytes.NewReader([]byte(`{"inst_id":"BTC-USDT-SWAP","kind":"bad"}`)))
	badKind.SetBasicAuth("admin", "Admin123")
	badKindRR := httptest.NewRecorder()
	srv.ServeHTTP(badKindRR, badKind)
	if badKindRR.Code != http.StatusBadRequest {
		t.Fatalf("bad kind status=%d body=%s", badKindRR.Code, badKindRR.Body.String())
	}

	var orderAlgoCalled bool
	okxServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v5/account/positions":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[]}`))
		case "/api/v5/trade/order-algo":
			orderAlgoCalled = true
			t.Fatal("closed position should not create algo order")
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
	closedReq := httptest.NewRequest(http.MethodPost, "/tvbot/positions/protection", bytes.NewReader([]byte(`{"api_id":"default","inst_id":"BTC-USDT-SWAP","kind":"tp"}`)))
	closedReq.SetBasicAuth("admin", "Admin123")
	closedRR := httptest.NewRecorder()
	srv.ServeHTTP(closedRR, closedReq)
	if closedRR.Code != http.StatusConflict {
		t.Fatalf("closed position status=%d body=%s", closedRR.Code, closedRR.Body.String())
	}
	if orderAlgoCalled {
		t.Fatal("closed position created algo order")
	}
}

func TestTVBotPositionProtectionRejectsMissingEntryPrice(t *testing.T) {
	srv := newTestServer(t)
	var orderAlgoCalled bool
	okxServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v5/account/positions":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instType":"SWAP","instId":"BTC-USDT-SWAP","mgnMode":"isolated","posSide":"long","pos":"2","avgPx":"","markPx":"101"}]}`))
		case "/api/v5/public/instruments":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instId":"BTC-USDT-SWAP","tickSz":"0.1","ctVal":"1","lotSz":"1","minSz":"1"}]}`))
		case "/api/v5/trade/order-algo":
			orderAlgoCalled = true
			t.Fatal("missing entry price should not create algo order")
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
	req := httptest.NewRequest(http.MethodPost, "/tvbot/positions/protection", bytes.NewReader([]byte(`{"api_id":"default","inst_id":"BTC-USDT-SWAP","pos_side":"long","kind":"tp"}`)))
	req.SetBasicAuth("admin", "Admin123")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("missing entry status=%d body=%s", rr.Code, rr.Body.String())
	}
	if orderAlgoCalled {
		t.Fatal("missing entry price created algo order")
	}
}
