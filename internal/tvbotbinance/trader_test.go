package binance

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotconfig"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

func TestTraderPlacesLimitOrderAndTPSLAlgoOrders(t *testing.T) {
	var orderForm url.Values
	algoForms := []url.Values{}
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
			if r.Form.Get("marginType") != "ISOLATED" {
				t.Fatalf("bad margin type form: %#v", r.Form)
			}
			_, _ = w.Write([]byte(`{"code":200,"msg":"success"}`))
		case "/fapi/v1/leverage":
			if r.Form.Get("leverage") != "10" {
				t.Fatalf("bad leverage form: %#v", r.Form)
			}
			_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","leverage":10}`))
		case "/fapi/v1/premiumIndex":
			if r.URL.Query().Get("symbol") != "BTCUSDT" {
				t.Fatalf("bad premium index query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","markPrice":"50000","indexPrice":"50000","lastFundingRate":"0","time":1784880000000}`))
		case "/fapi/v1/order":
			orderForm = cloneValues(r.Form)
			_, _ = w.Write([]byte(`{"orderId":123,"symbol":"BTCUSDT","status":"NEW","clientOrderId":"entry","price":"49850","origQty":"0.002","executedQty":"0","type":"LIMIT","side":"BUY"}`))
		case "/fapi/v1/algoOrder":
			algoForms = append(algoForms, cloneValues(r.Form))
			orderType := r.Form.Get("type")
			triggerPrice := r.Form.Get("triggerPrice")
			_, _ = w.Write([]byte(`{"algoId":456,"clientAlgoId":"algo","orderType":"` + orderType + `","symbol":"BTCUSDT","side":"SELL","quantity":"0.002","algoStatus":"NEW","triggerPrice":"` + triggerPrice + `"}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer ts.Close()
	cfg := config.Default()
	cfg.Trading.BinanceDemoBaseURL = ts.URL
	cfg.Trading.OrderType = string(trading.OrderTypeLimit)
	cfg.Trading.LongLimitPriceMultiplier = 0.997
	cfg.Trading.RiskType = string(trading.RiskTPSL)
	cfg.Trading.TakeProfitPct = 2
	cfg.Trading.StopLossPct = 1
	signal := trading.Signal{
		Action:   trading.ActionLong,
		Coinpair: "BTC",
		Price:    trading.NewFlexibleFloat(50000),
		Leverage: 10,
		Amount:   trading.NewFlexibleFloat(100),
		Risk: trading.Risk{
			Type:  trading.RiskTPSL,
			TPPct: ptrFlexible(2),
			SLPct: ptrFlexible(1),
		},
	}
	signal.Normalize()
	trader := Trader{Credentials: Credentials{APIKey: "key", SecretKey: "secret"}, HTTPClient: ts.Client()}
	result, err := trader.ExecuteSignal(context.Background(), signal, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if result.TargetExchange != trading.ExchangeBinance || result.OrdID != "123" || result.Px != "49850" {
		t.Fatalf("bad result: %#v", result)
	}
	if len(result.RiskOrders) != 2 {
		t.Fatalf("expected TP/SL risk order metadata, got %#v", result.RiskOrders)
	}
	riskByType := map[string]trading.RiskOrderResult{}
	for _, order := range result.RiskOrders {
		riskByType[order.OrderType] = order
	}
	if riskByType["TAKE_PROFIT_MARKET"].AlgoID != "456" || riskByType["TAKE_PROFIT_MARKET"].ClientAlgoID == "" || riskByType["TAKE_PROFIT_MARKET"].TriggerPrice != "50000.1" {
		t.Fatalf("bad TP risk order metadata: %#v", riskByType["TAKE_PROFIT_MARKET"])
	}
	if riskByType["STOP_MARKET"].AlgoID != "456" || riskByType["STOP_MARKET"].ClientAlgoID == "" || riskByType["STOP_MARKET"].TriggerPrice != "49800" {
		t.Fatalf("bad SL risk order metadata: %#v", riskByType["STOP_MARKET"])
	}
	if orderForm.Get("symbol") != "BTCUSDT" || orderForm.Get("side") != "BUY" || orderForm.Get("type") != "LIMIT" || orderForm.Get("timeInForce") != "GTC" || orderForm.Get("quantity") != "0.002" || orderForm.Get("price") != "49850" {
		t.Fatalf("bad order form: %#v", orderForm)
	}
	if len(algoForms) != 2 {
		t.Fatalf("expected TP and SL algo orders, got %#v", algoForms)
	}
	types := map[string]url.Values{}
	for _, form := range algoForms {
		types[form.Get("type")] = form
	}
	tp := types["TAKE_PROFIT_MARKET"]
	sl := types["STOP_MARKET"]
	if tp.Get("side") != "SELL" || tp.Get("triggerPrice") != "50000.1" || tp.Get("workingType") != "MARK_PRICE" {
		t.Fatalf("bad TP form: %#v", tp)
	}
	if sl.Get("side") != "SELL" || sl.Get("triggerPrice") != "49800" || sl.Get("workingType") != "MARK_PRICE" {
		t.Fatalf("bad SL form: %#v", sl)
	}
}

func TestTraderPlacesMarketOrderAndTrailingAlgoOrder(t *testing.T) {
	var orderForm url.Values
	var trailingForm url.Values
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
			_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","leverage":10}`))
		case "/fapi/v1/order":
			orderForm = cloneValues(r.Form)
			_, _ = w.Write([]byte(`{"orderId":123,"symbol":"BTCUSDT","status":"NEW","clientOrderId":"entry","price":"0","origQty":"0.002","executedQty":"0","type":"MARKET","side":"BUY"}`))
		case "/fapi/v1/algoOrder":
			trailingForm = cloneValues(r.Form)
			_, _ = w.Write([]byte(`{"algoId":456,"clientAlgoId":"trail","algoType":"CONDITIONAL","orderType":"TRAILING_STOP_MARKET","symbol":"BTCUSDT","side":"SELL","positionSide":"BOTH","quantity":"0.002","algoStatus":"NEW","callbackRate":"1.5"}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer ts.Close()
	cfg := config.Default()
	cfg.Trading.BinanceDemoBaseURL = ts.URL
	cfg.Trading.RiskType = string(trading.RiskTrailing)
	cfg.Trading.TrailingPct = 1.5
	trader := Trader{Credentials: Credentials{APIKey: "key", SecretKey: "secret"}, HTTPClient: ts.Client()}
	result, err := trader.ExecuteSignal(context.Background(), trading.Signal{
		Action:   trading.ActionLong,
		Coinpair: "BTC",
		Price:    trading.NewFlexibleFloat(50000),
		Leverage: 10,
		Amount:   trading.NewFlexibleFloat(100),
	}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if result.TargetExchange != trading.ExchangeBinance || result.OrdID != "123" {
		t.Fatalf("bad result: %#v", result)
	}
	if orderForm.Get("type") != "MARKET" || orderForm.Get("quantity") != "0.002" {
		t.Fatalf("bad order form: %#v", orderForm)
	}
	if trailingForm.Get("type") != "TRAILING_STOP_MARKET" ||
		trailingForm.Get("side") != "SELL" ||
		trailingForm.Get("quantity") != "0.002" ||
		trailingForm.Get("callbackRate") != "1.5" ||
		trailingForm.Get("workingType") != "MARK_PRICE" ||
		trailingForm.Get("triggerPrice") != "" ||
		trailingForm.Get("activatePrice") != "" ||
		trailingForm.Get("reduceOnly") != "true" {
		t.Fatalf("bad trailing form: %#v", trailingForm)
	}
}

func TestTraderSplitsMarketOrderAboveBinanceMaxQty(t *testing.T) {
	orderForms := []url.Values{}
	algoForms := []url.Values{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/fapi/v1/exchangeInfo":
			_, _ = w.Write([]byte(`{"symbols":[{"symbol":"DYMUSDT","status":"TRADING","pricePrecision":4,"quantityPrecision":0,"filters":[{"filterType":"PRICE_FILTER","tickSize":"0.0001"},{"filterType":"LOT_SIZE","minQty":"1","maxQty":"50000","stepSize":"1"},{"filterType":"MARKET_LOT_SIZE","minQty":"1","maxQty":"10000","stepSize":"1"}]}]}`))
		case "/fapi/v3/positionRisk":
			_, _ = w.Write([]byte(`[]`))
		case "/fapi/v1/openOrders":
			_, _ = w.Write([]byte(`[]`))
		case "/fapi/v1/marginType":
			_, _ = w.Write([]byte(`{"code":200,"msg":"success"}`))
		case "/fapi/v1/leverage":
			_, _ = w.Write([]byte(`{"symbol":"DYMUSDT","leverage":10}`))
		case "/fapi/v1/premiumIndex":
			if r.URL.Query().Get("symbol") != "DYMUSDT" {
				t.Fatalf("bad premium index query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"symbol":"DYMUSDT","markPrice":"0.25","indexPrice":"0.25","lastFundingRate":"0","time":1784880000000}`))
		case "/fapi/v1/order":
			if compareDecimal(r.Form.Get("quantity"), "10000") > 0 {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"code":-4005,"msg":"Quantity greater than max quantity."}`))
				return
			}
			orderForms = append(orderForms, cloneValues(r.Form))
			_, _ = w.Write([]byte(`{"orderId":` + strconv.Itoa(800+len(orderForms)) + `,"symbol":"DYMUSDT","status":"NEW","clientOrderId":"entry","price":"0","origQty":"` + r.Form.Get("quantity") + `","executedQty":"0","type":"MARKET","side":"SELL"}`))
		case "/fapi/v1/algoOrder":
			if compareDecimal(r.Form.Get("quantity"), "10000") > 0 {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"code":-4005,"msg":"Quantity greater than max quantity."}`))
				return
			}
			algoForms = append(algoForms, cloneValues(r.Form))
			_, _ = w.Write([]byte(`{"algoId":` + strconv.Itoa(900+len(algoForms)) + `,"clientAlgoId":"algo","algoType":"CONDITIONAL","orderType":"` + r.Form.Get("type") + `","symbol":"DYMUSDT","side":"BUY","quantity":"` + r.Form.Get("quantity") + `","algoStatus":"NEW","triggerPrice":"` + r.Form.Get("triggerPrice") + `"}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer ts.Close()

	cfg := config.Default()
	cfg.Trading.BinanceDemoBaseURL = ts.URL
	cfg.Trading.RiskType = string(trading.RiskTPSL)
	cfg.Trading.TakeProfitPct = 2
	cfg.Trading.StopLossPct = 1
	trader := Trader{Credentials: Credentials{APIKey: "key", SecretKey: "secret"}, HTTPClient: ts.Client()}
	result, err := trader.ExecuteSignal(context.Background(), trading.Signal{
		Action:   trading.ActionShort,
		Coinpair: "DYMUSDT",
		Price:    trading.NewFlexibleFloat(0.25),
		Leverage: 10,
		Amount:   trading.NewFlexibleFloat(6250),
	}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if result.OrdID != "801 / 802 / 803" || result.ClOrdID == "" {
		t.Fatalf("bad split result: %#v", result)
	}
	if len(orderForms) != 3 {
		t.Fatalf("expected three split main orders, got %#v", orderForms)
	}
	wantQty := []string{"10000", "10000", "5000"}
	for i, form := range orderForms {
		if form.Get("symbol") != "DYMUSDT" || form.Get("side") != "SELL" || form.Get("type") != "MARKET" || form.Get("quantity") != wantQty[i] || form.Get("newClientOrderId") == "" {
			t.Fatalf("bad split main order %d: %#v", i, form)
		}
		if i > 0 && form.Get("newClientOrderId") == orderForms[i-1].Get("newClientOrderId") {
			t.Fatalf("split main order client ids should be unique: %#v", orderForms)
		}
	}
	if len(algoForms) != 6 {
		t.Fatalf("expected TP and SL for each split main order, got %#v", algoForms)
	}
	byTypeQty := map[string][]string{}
	for _, form := range algoForms {
		byTypeQty[form.Get("type")] = append(byTypeQty[form.Get("type")], form.Get("quantity"))
		if form.Get("symbol") != "DYMUSDT" || form.Get("side") != "BUY" || form.Get("workingType") != "MARK_PRICE" {
			t.Fatalf("bad split risk order: %#v", form)
		}
	}
	if !reflect.DeepEqual(byTypeQty["TAKE_PROFIT_MARKET"], wantQty) || !reflect.DeepEqual(byTypeQty["STOP_MARKET"], wantQty) {
		t.Fatalf("bad split risk quantities: %#v", byTypeQty)
	}
}

func TestTraderAdjustsBinanceTPSLAwayFromMarkPrice(t *testing.T) {
	const markPrice = "0.005866"
	algoForms := []url.Values{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/fapi/v1/exchangeInfo":
			_, _ = w.Write([]byte(`{"symbols":[{"symbol":"PENGUUSDC","status":"TRADING","pricePrecision":7,"quantityPrecision":0,"filters":[{"filterType":"PRICE_FILTER","tickSize":"0.0000010"},{"filterType":"LOT_SIZE","minQty":"1","maxQty":"200000000","stepSize":"1"},{"filterType":"MARKET_LOT_SIZE","minQty":"1","maxQty":"20000000","stepSize":"1"}]}]}`))
		case "/fapi/v3/positionRisk":
			_, _ = w.Write([]byte(`[]`))
		case "/fapi/v1/openOrders":
			_, _ = w.Write([]byte(`[]`))
		case "/fapi/v1/marginType":
			_, _ = w.Write([]byte(`{"code":200,"msg":"success"}`))
		case "/fapi/v1/leverage":
			_, _ = w.Write([]byte(`{"symbol":"PENGUUSDC","leverage":10}`))
		case "/fapi/v1/premiumIndex":
			if r.URL.Query().Get("symbol") != "PENGUUSDC" {
				t.Fatalf("bad premium index query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"symbol":"PENGUUSDC","markPrice":"` + markPrice + `","indexPrice":"0.00587242","lastFundingRate":"0","time":1784880000000}`))
		case "/fapi/v1/order":
			_, _ = w.Write([]byte(`{"orderId":123,"symbol":"PENGUUSDC","status":"NEW","clientOrderId":"entry","price":"0","origQty":"831117","executedQty":"0","type":"MARKET","side":"SELL"}`))
		case "/fapi/v1/algoOrder":
			form := cloneValues(r.Form)
			if form.Get("type") == "TAKE_PROFIT_MARKET" && compareDecimal(form.Get("triggerPrice"), markPrice) >= 0 {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"code":-2021,"msg":"Order would immediately trigger."}`))
				return
			}
			algoForms = append(algoForms, form)
			_, _ = w.Write([]byte(`{"algoId":456,"clientAlgoId":"algo","algoType":"CONDITIONAL","orderType":"` + form.Get("type") + `","symbol":"PENGUUSDC","side":"BUY","quantity":"831117","algoStatus":"NEW","triggerPrice":"` + form.Get("triggerPrice") + `"}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer ts.Close()

	cfg := config.Default()
	cfg.Trading.BinanceDemoBaseURL = ts.URL
	cfg.Trading.RiskType = string(trading.RiskTPSL)
	cfg.Trading.TakeProfitPct = 2
	cfg.Trading.StopLossPct = 1
	trader := Trader{Credentials: Credentials{APIKey: "key", SecretKey: "secret"}, HTTPClient: ts.Client()}
	result, err := trader.ExecuteSignal(context.Background(), trading.Signal{
		Action:   trading.ActionShort,
		Coinpair: "PENGUUSDC.P",
		Ticker:   "PENGUUSDC.P",
		Price:    trading.NewFlexibleFloat(0.006016),
		Leverage: 10,
		Amount:   trading.NewFlexibleFloat(5000),
	}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if result.InstID != "PENGUUSDC" || result.OrdID != "123" {
		t.Fatalf("bad result: %#v", result)
	}
	if len(algoForms) != 2 {
		t.Fatalf("expected adjusted TP and SL, got %#v", algoForms)
	}
	types := map[string]url.Values{}
	for _, form := range algoForms {
		types[form.Get("type")] = form
	}
	if types["TAKE_PROFIT_MARKET"].Get("triggerPrice") != "0.005865" || types["TAKE_PROFIT_MARKET"].Get("side") != "BUY" {
		t.Fatalf("bad adjusted TP form: %#v", types["TAKE_PROFIT_MARKET"])
	}
	if types["STOP_MARKET"].Get("triggerPrice") != "0.006023" || types["STOP_MARKET"].Get("side") != "BUY" {
		t.Fatalf("bad adjusted SL form: %#v", types["STOP_MARKET"])
	}
}

func TestSplitBinanceAlgoOrderUsesMarketMaxQty(t *testing.T) {
	reqs, err := splitBinanceAlgoOrderRequest(AlgoOrderRequest{
		Symbol:           "DYMUSDT",
		Side:             "BUY",
		Type:             "STOP_MARKET",
		Quantity:         "25000",
		NewClientOrderID: "TV1780000000000BASESL",
	}, TradingFilters{
		StepSize:       1,
		MinQty:         "1",
		MaxQty:         "50000",
		MarketStepSize: 1,
		MarketMinQty:   "1",
		MarketMaxQty:   "10000",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"10000", "10000", "5000"}
	if len(reqs) != len(want) {
		t.Fatalf("split len=%d reqs=%#v", len(reqs), reqs)
	}
	for i, req := range reqs {
		if req.Quantity != want[i] || req.NewClientOrderID == "" {
			t.Fatalf("bad split algo part %d: %#v", i, req)
		}
		if i > 0 && req.NewClientOrderID == reqs[i-1].NewClientOrderID {
			t.Fatalf("split algo client ids should be unique: %#v", reqs)
		}
	}
}

func TestTraderKeepsSameDirectionPositionAndOrderAsAdd(t *testing.T) {
	var entryForm url.Values
	var cancelCalled bool
	var algoQueried bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/fapi/v1/exchangeInfo":
			_, _ = w.Write([]byte(`{"symbols":[{"symbol":"BTCUSDT","status":"TRADING","pricePrecision":1,"quantityPrecision":3,"filters":[{"filterType":"PRICE_FILTER","tickSize":"0.1"},{"filterType":"LOT_SIZE","minQty":"0.001","stepSize":"0.001"}]}]}`))
		case "/fapi/v3/positionRisk":
			_, _ = w.Write([]byte(`[{"symbol":"BTCUSDT","positionSide":"BOTH","positionAmt":"0.1","entryPrice":"50000"}]`))
		case "/fapi/v1/openOrders":
			_, _ = w.Write([]byte(`[{"symbol":"BTCUSDT","orderId":100,"clientOrderId":"old-long","side":"BUY","positionSide":"BOTH","type":"LIMIT","status":"NEW","origQty":"0.1","executedQty":"0"}]`))
		case "/fapi/v1/openAlgoOrders":
			algoQueried = true
			_, _ = w.Write([]byte(`[]`))
		case "/fapi/v1/order":
			if r.Method == http.MethodDelete {
				cancelCalled = true
				_, _ = w.Write([]byte(`{"orderId":100,"status":"CANCELED"}`))
				return
			}
			entryForm = cloneValues(r.Form)
			_, _ = w.Write([]byte(`{"orderId":123,"symbol":"BTCUSDT","status":"NEW","clientOrderId":"entry","price":"0","origQty":"0.002","executedQty":"0","type":"MARKET","side":"BUY"}`))
		case "/fapi/v1/marginType":
			_, _ = w.Write([]byte(`{"code":200,"msg":"success"}`))
		case "/fapi/v1/leverage":
			_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","leverage":10}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer ts.Close()

	cfg := config.Default()
	cfg.Trading.BinanceDemoBaseURL = ts.URL
	cfg.Trading.RiskType = string(trading.RiskNone)
	trader := Trader{Credentials: Credentials{APIKey: "key", SecretKey: "secret"}, HTTPClient: ts.Client()}
	_, err := trader.ExecuteSignal(context.Background(), trading.Signal{
		Action:   trading.ActionLong,
		Coinpair: "BTC",
		Price:    trading.NewFlexibleFloat(50000),
		Leverage: 10,
		Amount:   trading.NewFlexibleFloat(100),
	}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if cancelCalled || algoQueried {
		t.Fatalf("same direction add should not cancel or query algos cancelCalled=%v algoQueried=%v", cancelCalled, algoQueried)
	}
	if entryForm.Get("side") != "BUY" || entryForm.Get("reduceOnly") != "" || entryForm.Get("quantity") != "0.002" {
		t.Fatalf("bad add entry form: %#v", entryForm)
	}
}

func TestTraderSkipsLeverageWhenBinanceRemoteMatches(t *testing.T) {
	var leverageCalled bool
	var orderForm url.Values
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/fapi/v1/exchangeInfo":
			_, _ = w.Write([]byte(`{"symbols":[{"symbol":"BTCUSDT","status":"TRADING","pricePrecision":1,"quantityPrecision":3,"filters":[{"filterType":"PRICE_FILTER","tickSize":"0.1"},{"filterType":"LOT_SIZE","minQty":"0.001","stepSize":"0.001"}]}]}`))
		case "/fapi/v3/positionRisk":
			_, _ = w.Write([]byte(`[{"symbol":"BTCUSDT","positionSide":"BOTH","positionAmt":"0","leverage":"10"}]`))
		case "/fapi/v1/openOrders":
			_, _ = w.Write([]byte(`[]`))
		case "/fapi/v1/marginType":
			_, _ = w.Write([]byte(`{"code":200,"msg":"success"}`))
		case "/fapi/v1/leverage":
			leverageCalled = true
			_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","leverage":10}`))
		case "/fapi/v1/order":
			orderForm = cloneValues(r.Form)
			_, _ = w.Write([]byte(`{"orderId":123,"symbol":"BTCUSDT","status":"NEW","clientOrderId":"entry","price":"0","origQty":"0.002","executedQty":"0","type":"MARKET","side":"BUY"}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer ts.Close()

	cfg := config.Default()
	cfg.Trading.BinanceDemoBaseURL = ts.URL
	cfg.Trading.RiskType = string(trading.RiskNone)
	trader := Trader{Credentials: Credentials{APIKey: "key", SecretKey: "secret"}, HTTPClient: ts.Client()}
	result, err := trader.ExecuteSignal(context.Background(), trading.Signal{
		Action:   trading.ActionLong,
		Coinpair: "BTC",
		Price:    trading.NewFlexibleFloat(50000),
		Leverage: 10,
		Amount:   trading.NewFlexibleFloat(100),
	}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if leverageCalled {
		t.Fatal("matching Binance remote leverage should not be set again")
	}
	if result.Leverage != 10 || orderForm.Get("side") != "BUY" {
		t.Fatalf("bad result/order after leverage skip: result=%#v form=%#v", result, orderForm)
	}
}

func TestTraderSetsLeverageWhenBinanceRemoteDiffers(t *testing.T) {
	var leverageForm url.Values
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/fapi/v1/exchangeInfo":
			_, _ = w.Write([]byte(`{"symbols":[{"symbol":"BTCUSDT","status":"TRADING","pricePrecision":1,"quantityPrecision":3,"filters":[{"filterType":"PRICE_FILTER","tickSize":"0.1"},{"filterType":"LOT_SIZE","minQty":"0.001","stepSize":"0.001"}]}]}`))
		case "/fapi/v3/positionRisk":
			_, _ = w.Write([]byte(`[{"symbol":"BTCUSDT","positionSide":"BOTH","positionAmt":"0","leverage":"5"}]`))
		case "/fapi/v1/openOrders":
			_, _ = w.Write([]byte(`[]`))
		case "/fapi/v1/marginType":
			_, _ = w.Write([]byte(`{"code":200,"msg":"success"}`))
		case "/fapi/v1/leverage":
			leverageForm = cloneValues(r.Form)
			_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","leverage":10}`))
		case "/fapi/v1/order":
			_, _ = w.Write([]byte(`{"orderId":123,"symbol":"BTCUSDT","status":"NEW","clientOrderId":"entry","price":"0","origQty":"0.002","executedQty":"0","type":"MARKET","side":"BUY"}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer ts.Close()

	cfg := config.Default()
	cfg.Trading.BinanceDemoBaseURL = ts.URL
	cfg.Trading.RiskType = string(trading.RiskNone)
	trader := Trader{Credentials: Credentials{APIKey: "key", SecretKey: "secret"}, HTTPClient: ts.Client()}
	if _, err := trader.ExecuteSignal(context.Background(), trading.Signal{
		Action:   trading.ActionLong,
		Coinpair: "BTC",
		Price:    trading.NewFlexibleFloat(50000),
		Leverage: 10,
		Amount:   trading.NewFlexibleFloat(100),
	}, cfg); err != nil {
		t.Fatal(err)
	}
	if leverageForm.Get("symbol") != "BTCUSDT" || leverageForm.Get("leverage") != "10" {
		t.Fatalf("bad leverage setup form: %#v", leverageForm)
	}
}

func TestBinanceLeverageAttemptsTryConfiguredDownThenUp(t *testing.T) {
	got := binanceLeverageAttempts(10)
	wantPrefix := []int{10, 9, 8, 7, 6, 5, 4, 3, 2, 1, 11, 12}
	if len(got) != maxBinanceLeverageFallback || !reflect.DeepEqual(got[:len(wantPrefix)], wantPrefix) || got[len(got)-1] != 50 {
		t.Fatalf("leverage attempts = %#v", got)
	}
}

func TestTraderFallsBackBinanceLeverageBeforeOrder(t *testing.T) {
	leverageAttempts := []string{}
	var orderForm url.Values
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/fapi/v1/exchangeInfo":
			_, _ = w.Write([]byte(`{"symbols":[{"symbol":"MONUSDT","status":"TRADING","pricePrecision":4,"quantityPrecision":0,"filters":[{"filterType":"PRICE_FILTER","tickSize":"0.0001"},{"filterType":"LOT_SIZE","minQty":"1","stepSize":"1"}]}]}`))
		case "/fapi/v3/positionRisk":
			_, _ = w.Write([]byte(`[]`))
		case "/fapi/v1/openOrders":
			_, _ = w.Write([]byte(`[]`))
		case "/fapi/v1/marginType":
			_, _ = w.Write([]byte(`{"code":200,"msg":"success"}`))
		case "/fapi/v1/leverage":
			leverage := r.Form.Get("leverage")
			leverageAttempts = append(leverageAttempts, leverage)
			if leverage != "6" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"code":-4028,"msg":"Leverage ` + leverage + ` is not valid"}`))
				return
			}
			_, _ = w.Write([]byte(`{"symbol":"MONUSDT","leverage":6}`))
		case "/fapi/v1/order":
			orderForm = cloneValues(r.Form)
			_, _ = w.Write([]byte(`{"orderId":123,"symbol":"MONUSDT","status":"NEW","clientOrderId":"entry","price":"0","origQty":"100","executedQty":"0","type":"MARKET","side":"BUY"}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer ts.Close()

	cfg := config.Default()
	cfg.Trading.BinanceDemoBaseURL = ts.URL
	cfg.Trading.RiskType = string(trading.RiskNone)
	trader := Trader{Credentials: Credentials{APIKey: "key", SecretKey: "secret"}, HTTPClient: ts.Client()}
	result, err := trader.ExecuteSignal(context.Background(), trading.Signal{
		Action:   trading.ActionLong,
		Coinpair: "MONUSDT",
		Price:    trading.NewFlexibleFloat(1),
		Leverage: 10,
		Amount:   trading.NewFlexibleFloat(100),
	}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(leverageAttempts, []string{"10", "9", "8", "7", "6"}) {
		t.Fatalf("leverage attempts = %#v", leverageAttempts)
	}
	if result.Leverage != 6 {
		t.Fatalf("result leverage = %d, want 6", result.Leverage)
	}
	if orderForm.Get("symbol") != "MONUSDT" || orderForm.Get("side") != "BUY" || orderForm.Get("quantity") != "100" {
		t.Fatalf("bad order form after leverage fallback: %#v", orderForm)
	}
}

func TestTraderFallsBackOrderLeverageAfterMaxPositionError(t *testing.T) {
	leverageAttempts := []string{}
	orderLeverages := []int{}
	currentLeverage := 0
	var orderForm url.Values
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/fapi/v1/exchangeInfo":
			_, _ = w.Write([]byte(`{"symbols":[{"symbol":"MONUSDT","status":"TRADING","pricePrecision":4,"quantityPrecision":0,"filters":[{"filterType":"PRICE_FILTER","tickSize":"0.0001"},{"filterType":"LOT_SIZE","minQty":"1","stepSize":"1"}]}]}`))
		case "/fapi/v3/positionRisk":
			_, _ = w.Write([]byte(`[]`))
		case "/fapi/v1/openOrders":
			_, _ = w.Write([]byte(`[]`))
		case "/fapi/v1/marginType":
			_, _ = w.Write([]byte(`{"code":200,"msg":"success"}`))
		case "/fapi/v1/leverage":
			leverage := r.Form.Get("leverage")
			leverageAttempts = append(leverageAttempts, leverage)
			currentLeverage = mustAtoi(t, leverage)
			_, _ = w.Write([]byte(`{"symbol":"MONUSDT","leverage":` + leverage + `}`))
		case "/fapi/v1/order":
			orderLeverages = append(orderLeverages, currentLeverage)
			if currentLeverage > 8 {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"code":-2027,"msg":"Exceeded the maximum allowable position at current leverage."}`))
				return
			}
			orderForm = cloneValues(r.Form)
			_, _ = w.Write([]byte(`{"orderId":123,"symbol":"MONUSDT","status":"NEW","clientOrderId":"entry","price":"0","origQty":"100","executedQty":"0","type":"MARKET","side":"SELL"}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer ts.Close()

	cfg := config.Default()
	cfg.Trading.BinanceDemoBaseURL = ts.URL
	cfg.Trading.RiskType = string(trading.RiskNone)
	trader := Trader{Credentials: Credentials{APIKey: "key", SecretKey: "secret"}, HTTPClient: ts.Client()}
	result, err := trader.ExecuteSignal(context.Background(), trading.Signal{
		Action:   trading.ActionShort,
		Coinpair: "MONUSDT",
		Price:    trading.NewFlexibleFloat(1),
		Leverage: 10,
		Amount:   trading.NewFlexibleFloat(100),
	}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(leverageAttempts, []string{"10", "9", "8"}) {
		t.Fatalf("leverage attempts = %#v", leverageAttempts)
	}
	if !reflect.DeepEqual(orderLeverages, []int{10, 9, 8}) {
		t.Fatalf("order leverage attempts = %#v", orderLeverages)
	}
	if result.Leverage != 8 {
		t.Fatalf("result leverage = %d, want 8", result.Leverage)
	}
	if orderForm.Get("symbol") != "MONUSDT" || orderForm.Get("side") != "SELL" || orderForm.Get("quantity") != "100" {
		t.Fatalf("bad order form after order leverage fallback: %#v", orderForm)
	}
}

func TestTraderDoesNotFallbackOrderLeverageForNonMaxPositionError(t *testing.T) {
	leverageAttempts := []string{}
	orderCalls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/fapi/v1/exchangeInfo":
			_, _ = w.Write([]byte(`{"symbols":[{"symbol":"MONUSDT","status":"TRADING","pricePrecision":4,"quantityPrecision":0,"filters":[{"filterType":"PRICE_FILTER","tickSize":"0.0001"},{"filterType":"LOT_SIZE","minQty":"1","stepSize":"1"}]}]}`))
		case "/fapi/v3/positionRisk":
			_, _ = w.Write([]byte(`[]`))
		case "/fapi/v1/openOrders":
			_, _ = w.Write([]byte(`[]`))
		case "/fapi/v1/marginType":
			_, _ = w.Write([]byte(`{"code":200,"msg":"success"}`))
		case "/fapi/v1/leverage":
			leverageAttempts = append(leverageAttempts, r.Form.Get("leverage"))
			_, _ = w.Write([]byte(`{"symbol":"MONUSDT","leverage":10}`))
		case "/fapi/v1/order":
			orderCalls++
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"code":-2019,"msg":"Margin is insufficient."}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer ts.Close()

	cfg := config.Default()
	cfg.Trading.BinanceDemoBaseURL = ts.URL
	cfg.Trading.RiskType = string(trading.RiskNone)
	trader := Trader{Credentials: Credentials{APIKey: "key", SecretKey: "secret"}, HTTPClient: ts.Client()}
	_, err := trader.ExecuteSignal(context.Background(), trading.Signal{
		Action:   trading.ActionLong,
		Coinpair: "MONUSDT",
		Price:    trading.NewFlexibleFloat(1),
		Leverage: 10,
		Amount:   trading.NewFlexibleFloat(100),
	}, cfg)
	if err == nil || !strings.Contains(err.Error(), "-2019") {
		t.Fatalf("expected non-2027 order error, got %v", err)
	}
	if !reflect.DeepEqual(leverageAttempts, []string{"10"}) || orderCalls != 1 {
		t.Fatalf("unexpected fallback on non-2027 error: leverages=%#v orderCalls=%d", leverageAttempts, orderCalls)
	}
}

func TestTraderFailsOrderLeverageFallbackAtOneWithoutRiskOrders(t *testing.T) {
	leverageAttempts := []string{}
	orderLeverages := []int{}
	currentLeverage := 0
	algoCalled := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/fapi/v1/exchangeInfo":
			_, _ = w.Write([]byte(`{"symbols":[{"symbol":"MONUSDT","status":"TRADING","pricePrecision":4,"quantityPrecision":0,"filters":[{"filterType":"PRICE_FILTER","tickSize":"0.0001"},{"filterType":"LOT_SIZE","minQty":"1","stepSize":"1"}]}]}`))
		case "/fapi/v3/positionRisk":
			_, _ = w.Write([]byte(`[]`))
		case "/fapi/v1/openOrders":
			_, _ = w.Write([]byte(`[]`))
		case "/fapi/v1/marginType":
			_, _ = w.Write([]byte(`{"code":200,"msg":"success"}`))
		case "/fapi/v1/leverage":
			leverage := r.Form.Get("leverage")
			leverageAttempts = append(leverageAttempts, leverage)
			currentLeverage = mustAtoi(t, leverage)
			_, _ = w.Write([]byte(`{"symbol":"MONUSDT","leverage":` + leverage + `}`))
		case "/fapi/v1/order":
			orderLeverages = append(orderLeverages, currentLeverage)
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"code":-2027,"msg":"Exceeded the maximum allowable position at current leverage."}`))
		case "/fapi/v1/algoOrder":
			algoCalled = true
			t.Fatalf("risk order should not be created after failed main order")
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer ts.Close()

	cfg := config.Default()
	cfg.Trading.BinanceDemoBaseURL = ts.URL
	cfg.Trading.RiskType = string(trading.RiskTPSL)
	cfg.Trading.TakeProfitPct = 2
	cfg.Trading.StopLossPct = 1
	trader := Trader{Credentials: Credentials{APIKey: "key", SecretKey: "secret"}, HTTPClient: ts.Client()}
	_, err := trader.ExecuteSignal(context.Background(), trading.Signal{
		Action:   trading.ActionLong,
		Coinpair: "MONUSDT",
		Price:    trading.NewFlexibleFloat(1),
		Leverage: 3,
		Amount:   trading.NewFlexibleFloat(100),
		Risk: trading.Risk{
			Type:  trading.RiskTPSL,
			TPPct: ptrFlexible(2),
			SLPct: ptrFlexible(1),
		},
	}, cfg)
	if err == nil || !strings.Contains(err.Error(), "3x, 2x, 1x") || !strings.Contains(err.Error(), "-2027") {
		t.Fatalf("expected clear 1x fallback failure, got %v", err)
	}
	if !reflect.DeepEqual(leverageAttempts, []string{"3", "2", "1"}) || !reflect.DeepEqual(orderLeverages, []int{3, 2, 1}) {
		t.Fatalf("bad fallback attempts: leverages=%#v orders=%#v", leverageAttempts, orderLeverages)
	}
	if algoCalled {
		t.Fatal("risk order should not be called")
	}
}

func TestTraderSkipsInvalidLeverageDuringOrderFallback(t *testing.T) {
	leverageAttempts := []string{}
	orderLeverages := []int{}
	currentLeverage := 0
	var orderForm url.Values
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/fapi/v1/exchangeInfo":
			_, _ = w.Write([]byte(`{"symbols":[{"symbol":"MONUSDT","status":"TRADING","pricePrecision":4,"quantityPrecision":0,"filters":[{"filterType":"PRICE_FILTER","tickSize":"0.0001"},{"filterType":"LOT_SIZE","minQty":"1","stepSize":"1"}]}]}`))
		case "/fapi/v3/positionRisk":
			_, _ = w.Write([]byte(`[]`))
		case "/fapi/v1/openOrders":
			_, _ = w.Write([]byte(`[]`))
		case "/fapi/v1/marginType":
			_, _ = w.Write([]byte(`{"code":200,"msg":"success"}`))
		case "/fapi/v1/leverage":
			leverage := r.Form.Get("leverage")
			leverageAttempts = append(leverageAttempts, leverage)
			if leverage == "9" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"code":-4028,"msg":"Leverage 9 is not valid"}`))
				return
			}
			currentLeverage = mustAtoi(t, leverage)
			_, _ = w.Write([]byte(`{"symbol":"MONUSDT","leverage":` + leverage + `}`))
		case "/fapi/v1/order":
			orderLeverages = append(orderLeverages, currentLeverage)
			if currentLeverage > 8 {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"code":-2027,"msg":"Exceeded the maximum allowable position at current leverage."}`))
				return
			}
			orderForm = cloneValues(r.Form)
			_, _ = w.Write([]byte(`{"orderId":123,"symbol":"MONUSDT","status":"NEW","clientOrderId":"entry","price":"0","origQty":"100","executedQty":"0","type":"MARKET","side":"BUY"}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer ts.Close()

	cfg := config.Default()
	cfg.Trading.BinanceDemoBaseURL = ts.URL
	cfg.Trading.RiskType = string(trading.RiskNone)
	trader := Trader{Credentials: Credentials{APIKey: "key", SecretKey: "secret"}, HTTPClient: ts.Client()}
	result, err := trader.ExecuteSignal(context.Background(), trading.Signal{
		Action:   trading.ActionLong,
		Coinpair: "MONUSDT",
		Price:    trading.NewFlexibleFloat(1),
		Leverage: 10,
		Amount:   trading.NewFlexibleFloat(100),
	}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(leverageAttempts, []string{"10", "9", "8"}) || !reflect.DeepEqual(orderLeverages, []int{10, 8}) {
		t.Fatalf("bad fallback attempts with invalid leverage: leverages=%#v orders=%#v", leverageAttempts, orderLeverages)
	}
	if result.Leverage != 8 || orderForm.Get("symbol") != "MONUSDT" {
		t.Fatalf("bad result/order after invalid leverage skip: result=%#v order=%#v", result, orderForm)
	}
}

func TestTraderCancelsReversePendingOrderBeforeNewDirection(t *testing.T) {
	var paths []string
	var canceledForm url.Values
	var entryForm url.Values
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
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
			_, _ = w.Write([]byte(`[{"symbol":"BTCUSDT","orderId":100,"clientOrderId":"old-long","side":"BUY","positionSide":"BOTH","type":"LIMIT","status":"NEW","origQty":"0.1","executedQty":"0"}]`))
		case "/fapi/v1/order":
			switch r.Method {
			case http.MethodDelete:
				canceledForm = cloneValues(r.Form)
				_, _ = w.Write([]byte(`{"orderId":100,"symbol":"BTCUSDT","status":"CANCELED","clientOrderId":"old-long"}`))
			case http.MethodPost:
				entryForm = cloneValues(r.Form)
				_, _ = w.Write([]byte(`{"orderId":123,"symbol":"BTCUSDT","status":"NEW","clientOrderId":"entry","price":"0","origQty":"0.002","executedQty":"0","type":"MARKET","side":"SELL"}`))
			default:
				t.Fatalf("unexpected order method %s", r.Method)
			}
		case "/fapi/v1/openAlgoOrders":
			_, _ = w.Write([]byte(`[]`))
		case "/fapi/v1/marginType":
			_, _ = w.Write([]byte(`{"code":200,"msg":"success"}`))
		case "/fapi/v1/leverage":
			_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","leverage":10}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer ts.Close()

	cfg := config.Default()
	cfg.Trading.BinanceDemoBaseURL = ts.URL
	cfg.Trading.RiskType = string(trading.RiskNone)
	trader := Trader{Credentials: Credentials{APIKey: "key", SecretKey: "secret"}, HTTPClient: ts.Client()}
	_, err := trader.ExecuteSignal(context.Background(), trading.Signal{
		Action:   trading.ActionShort,
		Coinpair: "BTC",
		Price:    trading.NewFlexibleFloat(50000),
		Leverage: 10,
		Amount:   trading.NewFlexibleFloat(100),
	}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if canceledForm.Get("orderId") != "100" {
		t.Fatalf("bad canceled order form: %#v", canceledForm)
	}
	if entryForm.Get("side") != "SELL" || entryForm.Get("reduceOnly") != "" || entryForm.Get("quantity") != "0.002" {
		t.Fatalf("bad reverse entry form: %#v", entryForm)
	}
	if pathIndex(paths, http.MethodDelete+" /fapi/v1/order") > pathIndex(paths, http.MethodPost+" /fapi/v1/order") {
		t.Fatalf("entry placed before reverse order cancellation: %#v", paths)
	}
}

func TestTraderClosesReversePositionBeforeNewDirection(t *testing.T) {
	var positionCalls int
	var canceledAlgo url.Values
	var orderForms []url.Values
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/fapi/v1/exchangeInfo":
			_, _ = w.Write([]byte(`{"symbols":[{"symbol":"BTCUSDT","status":"TRADING","pricePrecision":1,"quantityPrecision":3,"filters":[{"filterType":"PRICE_FILTER","tickSize":"0.1"},{"filterType":"LOT_SIZE","minQty":"0.001","stepSize":"0.001"}]}]}`))
		case "/fapi/v3/positionRisk":
			positionCalls++
			if positionCalls <= 2 {
				_, _ = w.Write([]byte(`[{"symbol":"BTCUSDT","positionSide":"BOTH","positionAmt":"0.1","entryPrice":"50000"}]`))
				return
			}
			_, _ = w.Write([]byte(`[]`))
		case "/fapi/v1/openOrders":
			_, _ = w.Write([]byte(`[]`))
		case "/fapi/v1/openAlgoOrders":
			_, _ = w.Write([]byte(`[{"algoId":900,"clientAlgoId":"old-long-tp","algoType":"CONDITIONAL","orderType":"TAKE_PROFIT_MARKET","symbol":"BTCUSDT","side":"SELL","positionSide":"BOTH","quantity":"0.1","algoStatus":"NEW","reduceOnly":true}]`))
		case "/fapi/v1/algoOrder":
			if r.Method != http.MethodDelete {
				t.Fatalf("unexpected algo method %s", r.Method)
			}
			canceledAlgo = cloneValues(r.Form)
			_, _ = w.Write([]byte(`{"algoId":900,"clientAlgoId":"old-long-tp","code":"200","msg":"success"}`))
		case "/fapi/v1/order":
			if r.Method != http.MethodPost {
				t.Fatalf("unexpected order method %s", r.Method)
			}
			orderForms = append(orderForms, cloneValues(r.Form))
			_, _ = w.Write([]byte(`{"orderId":123,"symbol":"BTCUSDT","status":"NEW","clientOrderId":"ok","price":"0","origQty":"0.1","executedQty":"0","type":"MARKET","side":"SELL"}`))
		case "/fapi/v1/marginType":
			_, _ = w.Write([]byte(`{"code":200,"msg":"success"}`))
		case "/fapi/v1/leverage":
			_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","leverage":10}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer ts.Close()

	cfg := config.Default()
	cfg.Trading.BinanceDemoBaseURL = ts.URL
	cfg.Trading.RiskType = string(trading.RiskNone)
	trader := Trader{Credentials: Credentials{APIKey: "key", SecretKey: "secret"}, HTTPClient: ts.Client()}
	_, err := trader.ExecuteSignal(context.Background(), trading.Signal{
		Action:   trading.ActionShort,
		Coinpair: "BTC",
		Price:    trading.NewFlexibleFloat(50000),
		Leverage: 10,
		Amount:   trading.NewFlexibleFloat(100),
	}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if canceledAlgo.Get("algoId") != "900" {
		t.Fatalf("bad canceled algo form: %#v", canceledAlgo)
	}
	if len(orderForms) != 2 {
		t.Fatalf("expected close and entry orders, got %#v", orderForms)
	}
	closeForm, entryForm := orderForms[0], orderForms[1]
	if closeForm.Get("side") != "SELL" || closeForm.Get("type") != "MARKET" || closeForm.Get("quantity") != "0.1" || closeForm.Get("reduceOnly") != "true" {
		t.Fatalf("bad close form: %#v", closeForm)
	}
	if entryForm.Get("side") != "SELL" || entryForm.Get("quantity") != "0.002" || entryForm.Get("reduceOnly") != "" {
		t.Fatalf("bad entry form after close: %#v", entryForm)
	}
}

func TestTraderContinuesWhenMarginTypeSetupBlockedByOpenOrders(t *testing.T) {
	var orderForm url.Values
	var leverageCalled bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/fapi/v1/exchangeInfo":
			_, _ = w.Write([]byte(`{"symbols":[{"symbol":"SXTUSDT","status":"TRADING","pricePrecision":6,"quantityPrecision":1,"filters":[{"filterType":"PRICE_FILTER","tickSize":"0.000001"},{"filterType":"LOT_SIZE","minQty":"0.1","stepSize":"0.1"}]}]}`))
		case "/fapi/v3/positionRisk":
			_, _ = w.Write([]byte(`[]`))
		case "/fapi/v1/openOrders":
			_, _ = w.Write([]byte(`[]`))
		case "/fapi/v1/marginType":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"code":-4067,"msg":"Position side cannot be changed if there exists open orders."}`))
		case "/fapi/v1/leverage":
			leverageCalled = true
			_, _ = w.Write([]byte(`{"symbol":"SXTUSDT","leverage":10}`))
		case "/fapi/v1/order":
			orderForm = cloneValues(r.Form)
			_, _ = w.Write([]byte(`{"orderId":321,"symbol":"SXTUSDT","status":"NEW","clientOrderId":"entry","price":"0.007312","origQty":"68231.4","executedQty":"0","type":"LIMIT","side":"BUY"}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer ts.Close()

	cfg := config.Default()
	cfg.Trading.BinanceDemoBaseURL = ts.URL
	cfg.Trading.RiskType = string(trading.RiskNone)
	cfg.Trading.OrderType = string(trading.OrderTypeLimit)
	trader := Trader{Credentials: Credentials{APIKey: "key", SecretKey: "secret"}, HTTPClient: ts.Client()}
	result, err := trader.ExecuteSignal(context.Background(), trading.Signal{
		Action:   trading.ActionLong,
		Coinpair: "SXTUSDT.P",
		Ticker:   "SXTUSDT.P",
		Price:    trading.NewFlexibleFloat(0.007335),
		Leverage: 10,
		Amount:   trading.NewFlexibleFloat(500),
	}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !leverageCalled {
		t.Fatal("expected leverage setup after margin setup was skipped")
	}
	if result.OrdID != "321" || result.InstID != "SXTUSDT" {
		t.Fatalf("bad order result: %#v", result)
	}
	if orderForm.Get("symbol") != "SXTUSDT" || orderForm.Get("side") != "BUY" || orderForm.Get("type") != "LIMIT" || orderForm.Get("quantity") == "" {
		t.Fatalf("bad order form: %#v", orderForm)
	}
}

func TestTraderRejectsOutOfRangeBinanceTrailingBeforeSubmitting(t *testing.T) {
	var called bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		t.Fatalf("unexpected request %s", r.URL.Path)
	}))
	defer ts.Close()
	cfg := config.Default()
	cfg.Trading.BinanceDemoBaseURL = ts.URL
	cfg.Trading.RiskType = string(trading.RiskTrailing)
	cfg.Trading.TrailingPct = 10.1
	trader := Trader{Credentials: Credentials{APIKey: "key", SecretKey: "secret"}, HTTPClient: ts.Client()}
	_, err := trader.ExecuteSignal(context.Background(), trading.Signal{
		Action:   trading.ActionLong,
		Coinpair: "BTC",
		Price:    trading.NewFlexibleFloat(50000),
		Leverage: 10,
		Amount:   trading.NewFlexibleFloat(100),
	}, cfg)
	if err == nil || !strings.Contains(err.Error(), "between 0.1 and 10") {
		t.Fatalf("expected trailing range error, got %v", err)
	}
	if called {
		t.Fatal("invalid trailing should fail before HTTP calls")
	}
}

func cloneValues(values url.Values) url.Values {
	out := url.Values{}
	for key, list := range values {
		for _, value := range list {
			out.Add(key, value)
		}
	}
	return out
}

func ptrFlexible(value float64) *trading.FlexibleFloat {
	v := trading.NewFlexibleFloat(value)
	return &v
}

func mustAtoi(t *testing.T, raw string) int {
	t.Helper()
	value, err := strconv.Atoi(raw)
	if err != nil {
		t.Fatalf("bad integer %q: %v", raw, err)
	}
	return value
}

func pathIndex(paths []string, want string) int {
	for i, path := range paths {
		if path == want {
			return i
		}
	}
	return len(paths) + 1
}
