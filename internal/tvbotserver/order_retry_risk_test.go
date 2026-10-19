package server

import (
	"bytes"
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
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotstorage"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

func TestOrderRetryExecutesBinanceTPSLFromStoredRisk(t *testing.T) {
	srv := newTestServer(t)
	var mu sync.Mutex
	var orderForms []url.Values
	var algoForms []url.Values
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/fapi/v1/exchangeInfo":
			_, _ = w.Write([]byte(`{"symbols":[{"symbol":"BTCUSDT","status":"TRADING","pricePrecision":1,"quantityPrecision":3,"filters":[{"filterType":"PRICE_FILTER","tickSize":"0.1"},{"filterType":"LOT_SIZE","minQty":"0.001","stepSize":"0.001"}]}]}`))
		case "/fapi/v3/positionRisk":
			_, _ = w.Write([]byte(`[]`))
		case "/fapi/v1/openOrders":
			_, _ = w.Write([]byte(`[]`))
		case "/fapi/v1/marginType":
			_, _ = w.Write([]byte(`{"code":200,"msg":"success"}`))
		case "/fapi/v1/leverage":
			_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","leverage":8}`))
		case "/fapi/v1/ticker/bookTicker":
			if r.URL.Query().Get("symbol") != "BTCUSDT" {
				t.Fatalf("bad book ticker query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","bidPrice":"24999.9","bidQty":"1","askPrice":"25000.1","askQty":"2","time":1784880000000}`))
		case "/fapi/v1/premiumIndex":
			if r.URL.Query().Get("symbol") != "BTCUSDT" {
				t.Fatalf("bad premium index query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","markPrice":"25000","indexPrice":"25000","lastFundingRate":"0","time":1784880000000}`))
		case "/fapi/v1/order":
			mu.Lock()
			orderForms = append(orderForms, cloneValues(r.Form))
			mu.Unlock()
			_, _ = w.Write([]byte(`{"orderId":321,"symbol":"BTCUSDT","status":"NEW","clientOrderId":"entry","price":"0","origQty":"` + r.Form.Get("quantity") + `","executedQty":"0","type":"MARKET","side":"BUY"}`))
		case "/fapi/v1/algoOrder":
			mu.Lock()
			algoForms = append(algoForms, cloneValues(r.Form))
			mu.Unlock()
			_, _ = w.Write([]byte(`{"algoId":654,"clientAlgoId":"algo","orderType":"TAKE_PROFIT_MARKET","symbol":"BTCUSDT","side":"SELL","quantity":"0.002","algoStatus":"NEW","triggerPrice":"51000"}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer ts.Close()

	cfg := srv.ConfigStore.Get()
	cfg.Trading.BinanceDemoBaseURL = ts.URL
	cfg.Trading.Leverage = 5
	cfg.Trading.OrderType = string(trading.OrderTypeMarket)
	cfg.Trading.RiskType = string(trading.RiskTPSL)
	cfg.Trading.TakeProfitPct = 2
	cfg.Trading.StopLossPct = 1
	srv.ConfigStore = config.NewStore("", cfg)

	signal := validSignal(t, srv)
	signal.TargetExchange = trading.ExchangeBinance
	signal.Token = srv.Token.Generate(signal.CanonicalWebhookTokenPayload())
	body, err := json.Marshal(signal)
	if err != nil {
		t.Fatal(err)
	}
	first := httptest.NewRecorder()
	srv.ServeHTTP(first, httptest.NewRequest(http.MethodPost, "/tvorder", bytes.NewReader(body)))
	if first.Code != http.StatusAccepted {
		t.Fatalf("first status=%d body=%s", first.Code, first.Body.String())
	}
	var firstResp struct {
		SignalID string `json:"signal_id"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &firstResp); err != nil {
		t.Fatal(err)
	}
	waitOrderStatus(t, srv.Orders, firstResp.SignalID, storage.StatusSubmitted)
	select {
	case <-srv.Executor.(fakeExecutor).calls:
	case <-time.After(time.Second):
		t.Fatal("initial order was not executed")
	}
	if err := srv.Orders.MarkFailed(firstResp.SignalID, fmt.Errorf("binance failed"), srv.now()); err != nil {
		t.Fatal(err)
	}

	cfg = srv.ConfigStore.Get()
	cfg.Trading.Leverage = 8
	cfg.Trading.RiskType = string(trading.RiskNone)
	srv.ConfigStore = config.NewStore("", cfg)
	srv.Executor = ExchangeExecutor{
		Binance: binance.Trader{
			Credentials: binance.Credentials{APIKey: "key", SecretKey: "secret"},
			HTTPClient:  ts.Client(),
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/tvbot/orders/"+firstResp.SignalID+"/retry", nil)
	req.SetBasicAuth("admin", "Admin123")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("retry status=%d body=%s", rr.Code, rr.Body.String())
	}
	var retryResp struct {
		SignalID string `json:"signal_id"`
		Price    string `json:"price"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &retryResp); err != nil {
		t.Fatal(err)
	}
	if retryResp.Price != "25000" {
		t.Fatalf("retry should report refreshed Binance price, got %#v", retryResp)
	}
	waitOrderStatus(t, srv.Orders, retryResp.SignalID, storage.StatusSubmitted)

	mu.Lock()
	defer mu.Unlock()
	if len(orderForms) != 1 {
		t.Fatalf("expected one Binance main order, got %#v", orderForms)
	}
	if orderForms[0].Get("symbol") != "BTCUSDT" || orderForms[0].Get("side") != "BUY" || orderForms[0].Get("type") != "MARKET" || orderForms[0].Get("quantity") != "0.004" {
		t.Fatalf("bad retry order form: %#v", orderForms[0])
	}
	if len(algoForms) != 2 {
		t.Fatalf("retry should create TP and SL algo orders, got %#v", algoForms)
	}
	types := map[string]url.Values{}
	for _, form := range algoForms {
		types[form.Get("type")] = form
	}
	if types["TAKE_PROFIT_MARKET"].Get("side") != "SELL" || types["STOP_MARKET"].Get("side") != "SELL" {
		t.Fatalf("bad retry algo forms: %#v", algoForms)
	}
}
