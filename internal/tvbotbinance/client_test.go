package binance

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestClientSignsPrivateBalanceRequest(t *testing.T) {
	const secret = "unit-secret"
	now := time.Date(2026, 7, 24, 3, 0, 0, 0, time.UTC)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/fapi/v3/balance" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if r.Header.Get("X-MBX-APIKEY") != "unit-key" {
			t.Fatalf("missing API key header")
		}
		q := r.URL.Query()
		if q.Get("timestamp") != strconv.FormatInt(now.UnixMilli(), 10) {
			t.Fatalf("bad timestamp: %s", r.URL.RawQuery)
		}
		payload, gotSig := splitSignatureTail(t, r.URL.RawQuery)
		if payload != qWithoutSignature(q).Encode() {
			t.Fatalf("signature payload does not match request query got=%s query=%s", payload, qWithoutSignature(q).Encode())
		}
		if gotSig != sign(payload, secret) {
			t.Fatalf("bad signature got=%s payload=%s", gotSig, payload)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"asset":"USDT","balance":"123.45","availableBalance":"120.00","crossWalletBalance":"122.00","updateTime":1784880000000}]`))
	}))
	defer ts.Close()

	client := Client{
		BaseURL:     ts.URL,
		Credentials: Credentials{APIKey: "unit-key", SecretKey: secret},
		HTTPClient:  ts.Client(),
		Now:         func() time.Time { return now },
	}
	balances, err := client.AccountBalance(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	usdt, ok := USDTBalanceFromAccount(balances)
	if !ok || usdt.Balance != "123.45" || usdt.AvailableBalance != "120.00" {
		t.Fatalf("bad balance: %#v ok=%v", usdt, ok)
	}
}

func TestClientUserTradesSignsPrivateRequest(t *testing.T) {
	const secret = "unit-secret"
	now := time.Date(2026, 7, 24, 3, 0, 0, 0, time.UTC)
	start := now.Add(-2 * time.Hour)
	end := now.Add(-time.Hour)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/fapi/v1/userTrades" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if r.Header.Get("X-MBX-APIKEY") != "unit-key" {
			t.Fatalf("missing API key header")
		}
		q := r.URL.Query()
		if q.Get("symbol") != "BTCUSDT" || q.Get("startTime") != strconv.FormatInt(start.UnixMilli(), 10) || q.Get("endTime") != strconv.FormatInt(end.UnixMilli(), 10) || q.Get("limit") != "1000" {
			t.Fatalf("bad user trades query: %s", r.URL.RawQuery)
		}
		if q.Get("timestamp") != strconv.FormatInt(now.UnixMilli(), 10) {
			t.Fatalf("bad timestamp: %s", r.URL.RawQuery)
		}
		payload, gotSig := splitSignatureTail(t, r.URL.RawQuery)
		if payload != qWithoutSignature(q).Encode() {
			t.Fatalf("signature payload does not match request query got=%s query=%s", payload, qWithoutSignature(q).Encode())
		}
		if gotSig != sign(payload, secret) {
			t.Fatalf("bad signature got=%s payload=%s", gotSig, payload)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"symbol":"BTCUSDT","side":"BUY","positionSide":"BOTH","qty":"0.2","time":1784880000000,"id":100,"orderId":200}]`))
	}))
	defer ts.Close()

	client := Client{
		BaseURL:     ts.URL,
		Credentials: Credentials{APIKey: "unit-key", SecretKey: secret},
		HTTPClient:  ts.Client(),
		Now:         func() time.Time { return now },
	}
	trades, err := client.UserTrades(context.Background(), "btcusdt", start, end, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(trades) != 1 || trades[0].Symbol != "BTCUSDT" || trades[0].Side != "BUY" || trades[0].Qty != "0.2" {
		t.Fatalf("bad user trades: %#v", trades)
	}
}

func TestClientIncomeHistorySignsPrivateRequest(t *testing.T) {
	const secret = "unit-secret"
	now := time.Date(2026, 7, 24, 3, 0, 0, 0, time.UTC)
	start := now.Add(-24 * time.Hour)
	end := now
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/fapi/v1/income" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if r.Header.Get("X-MBX-APIKEY") != "unit-key" {
			t.Fatalf("missing API key header")
		}
		q := r.URL.Query()
		if q.Get("symbol") != "BTCUSDT" || q.Get("incomeType") != "FUNDING_FEE" || q.Get("startTime") != strconv.FormatInt(start.UnixMilli(), 10) || q.Get("endTime") != strconv.FormatInt(end.UnixMilli(), 10) || q.Get("limit") != "1000" {
			t.Fatalf("bad income query: %s", r.URL.RawQuery)
		}
		if q.Get("timestamp") != strconv.FormatInt(now.UnixMilli(), 10) {
			t.Fatalf("bad timestamp: %s", r.URL.RawQuery)
		}
		payload, gotSig := splitSignatureTail(t, r.URL.RawQuery)
		if payload != qWithoutSignature(q).Encode() {
			t.Fatalf("signature payload does not match request query got=%s query=%s", payload, qWithoutSignature(q).Encode())
		}
		if gotSig != sign(payload, secret) {
			t.Fatalf("bad signature got=%s payload=%s", gotSig, payload)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"symbol":"BTCUSDT","incomeType":"FUNDING_FEE","income":"-0.12","asset":"USDT","time":1784880000000,"tranId":123}]`))
	}))
	defer ts.Close()

	client := Client{
		BaseURL:     ts.URL,
		Credentials: Credentials{APIKey: "unit-key", SecretKey: secret},
		HTTPClient:  ts.Client(),
		Now:         func() time.Time { return now },
	}
	incomes, err := client.IncomeHistory(context.Background(), "btcusdt", "funding_fee", start, end, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(incomes) != 1 || incomes[0].Symbol != "BTCUSDT" || incomes[0].IncomeType != "FUNDING_FEE" || incomes[0].Income != "-0.12" {
		t.Fatalf("bad incomes: %#v", incomes)
	}
}

func TestClientParsesAPIError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":-2015,"msg":"invalid api-key"}`))
	}))
	defer ts.Close()

	client := Client{
		BaseURL:     ts.URL,
		Credentials: Credentials{APIKey: "key", SecretKey: "secret"},
		HTTPClient:  ts.Client(),
		Now:         func() time.Time { return time.Date(2026, 7, 24, 3, 0, 0, 0, time.UTC) },
	}
	_, err := client.AccountBalance(context.Background())
	var apiErr APIError
	if !errors.As(err, &apiErr) || apiErr.Code != -2015 || apiErr.Msg != "invalid api-key" {
		t.Fatalf("bad api error: %#v", err)
	}
}

func TestIsExecutionStatusUnknown(t *testing.T) {
	if !IsExecutionStatusUnknown(APIError{Code: -1007, Msg: "Timeout waiting for response from backend server. Send status unknown; execution status unknown."}) {
		t.Fatal("expected -1007 to be treated as unknown execution status")
	}
	if IsExecutionStatusUnknown(APIError{Code: -2022, Msg: "ReduceOnly Order is rejected."}) {
		t.Fatal("reduce-only rejection should not be treated as unknown execution status")
	}
}

func TestClientTradingEndpoints(t *testing.T) {
	const secret = "secret"
	seen := map[string]url.Values{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			rawBody := string(body)
			if rawBody != "" {
				payload, gotSig := splitSignatureTail(t, rawBody)
				if gotSig != sign(payload, secret) {
					t.Fatalf("bad body signature got=%s payload=%s", gotSig, payload)
				}
			}
			r.Body = io.NopCloser(strings.NewReader(rawBody))
		}
		if r.Method == http.MethodDelete {
			payload, gotSig := splitSignatureTail(t, r.URL.RawQuery)
			if gotSig != sign(payload, secret) {
				t.Fatalf("bad delete signature got=%s payload=%s", gotSig, payload)
			}
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		seen[r.Method+" "+r.URL.Path] = r.Form
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/fapi/v3/positionRisk":
			_, _ = w.Write([]byte(`[{"symbol":"BTCUSDT","positionAmt":"0.1","entryPrice":"50000","markPrice":"50100","unRealizedProfit":"10","positionSide":"BOTH"}]`))
		case "/fapi/v1/openOrders":
			_, _ = w.Write([]byte(`[{"symbol":"BTCUSDT","orderId":123,"clientOrderId":"cid","price":"50000","origQty":"0.1","executedQty":"0","side":"BUY","type":"LIMIT","status":"NEW","time":1784880000000}]`))
		case "/fapi/v1/ticker/bookTicker":
			if r.URL.Query().Get("symbol") != "BTCUSDT" {
				t.Fatalf("bad book ticker query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","bidPrice":"49999.9","bidQty":"1","askPrice":"50000.1","askQty":"2","time":1784880000000}`))
		case "/fapi/v1/premiumIndex":
			if r.URL.Query().Get("symbol") != "BTCUSDT" {
				t.Fatalf("bad premium index query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","markPrice":"50000.0","indexPrice":"50001.0","lastFundingRate":"0.0001","time":1784880000000}`))
		case "/fapi/v1/order":
			switch r.Method {
			case http.MethodGet:
				_, _ = w.Write([]byte(`{"orderId":456,"symbol":"BTCUSDT","status":"FILLED","clientOrderId":"entry","price":"50000","origQty":"0.1","executedQty":"0.1","type":"LIMIT","side":"BUY","positionSide":"BOTH"}`))
			case http.MethodPost:
				_, _ = w.Write([]byte(`{"orderId":456,"symbol":"BTCUSDT","status":"NEW","clientOrderId":"entry","price":"50000","origQty":"0.1","executedQty":"0","type":"LIMIT","side":"BUY"}`))
			case http.MethodPut:
				_, _ = w.Write([]byte(`{"orderId":456,"symbol":"BTCUSDT","status":"NEW","clientOrderId":"entry","price":"49999.9","origQty":"0.1","executedQty":"0","type":"LIMIT","side":"BUY"}`))
			case http.MethodDelete:
				_, _ = w.Write([]byte(`{"orderId":456,"symbol":"BTCUSDT","status":"CANCELED","clientOrderId":"entry","price":"49999.9","origQty":"0.1","executedQty":"0","type":"LIMIT","side":"BUY"}`))
			default:
				t.Fatalf("unexpected order method %s", r.Method)
			}
		case "/fapi/v1/allOpenOrders":
			_, _ = w.Write([]byte(`{"code":200,"msg":"The operation of cancel all open order is done."}`))
		case "/fapi/v1/openAlgoOrders":
			_, _ = w.Write([]byte(`[{"algoId":790,"clientAlgoId":"tp-open","algoType":"CONDITIONAL","orderType":"TAKE_PROFIT_MARKET","symbol":"BTCUSDT","side":"SELL","positionSide":"BOTH","quantity":"0.1","algoStatus":"NEW","reduceOnly":true,"triggerPrice":"51000","activatePrice":"0","callbackRate":"1.5","workingType":"MARK_PRICE"}]`))
		case "/fapi/v1/algoOrder":
			switch r.Method {
			case http.MethodPost:
				_, _ = w.Write([]byte(`{"algoId":789,"clientAlgoId":"tp","orderType":"TAKE_PROFIT_MARKET","symbol":"BTCUSDT","side":"SELL","quantity":"0.1","algoStatus":"NEW","triggerPrice":"51000"}`))
			case http.MethodDelete:
				_, _ = w.Write([]byte(`{"algoId":790,"clientAlgoId":"tp-open","code":"200","msg":"success"}`))
			default:
				t.Fatalf("unexpected algo method %s", r.Method)
			}
		case "/fapi/v1/algoOpenOrders":
			_, _ = w.Write([]byte(`{"code":200,"msg":"The operation of cancel all open order is done."}`))
		case "/fapi/v1/leverage":
			_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","leverage":10}`))
		case "/fapi/v1/marginType":
			_, _ = w.Write([]byte(`{"code":200,"msg":"success"}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer ts.Close()
	client := Client{BaseURL: ts.URL, Credentials: Credentials{APIKey: "key", SecretKey: secret}, HTTPClient: ts.Client(), Now: func() time.Time {
		return time.Date(2026, 7, 24, 3, 0, 0, 0, time.UTC)
	}}

	positions, err := client.Positions(context.Background(), "BTCUSDT")
	if err != nil || len(positions) != 1 || positions[0].Symbol != "BTCUSDT" {
		t.Fatalf("bad positions err=%v positions=%#v", err, positions)
	}
	orders, err := client.OpenOrders(context.Background(), "BTCUSDT")
	if err != nil || len(orders) != 1 || orders[0].OrderID != 123 {
		t.Fatalf("bad open orders err=%v orders=%#v", err, orders)
	}
	if err := client.SetLeverage(context.Background(), "BTCUSDT", 10); err != nil {
		t.Fatal(err)
	}
	if err := client.ChangeMarginType(context.Background(), "BTCUSDT", "ISOLATED"); err != nil {
		t.Fatal(err)
	}
	ticker, err := client.BookTicker(context.Background(), "btcusdt")
	if err != nil || ticker.Symbol != "BTCUSDT" || ticker.BidPrice != "49999.9" || ticker.AskPrice != "50000.1" {
		t.Fatalf("bad book ticker err=%v ticker=%#v", err, ticker)
	}
	premium, err := client.PremiumIndex(context.Background(), "btcusdt")
	if err != nil || premium.Symbol != "BTCUSDT" || premium.MarkPrice != "50000.0" || premium.LastFundingRate != "0.0001" {
		t.Fatalf("bad premium index err=%v premium=%#v", err, premium)
	}
	ack, err := client.PlaceOrder(context.Background(), PlaceOrderRequest{
		Symbol:           "BTCUSDT",
		Side:             "BUY",
		Type:             "LIMIT",
		TimeInForce:      "GTC",
		Quantity:         "0.1",
		Price:            "50000",
		NewClientOrderID: "entry",
	})
	if err != nil || ack.OrderID != 456 {
		t.Fatalf("bad order ack err=%v ack=%#v", err, ack)
	}
	queried, err := client.QueryOrder(context.Background(), QueryOrderRequest{
		Symbol:            "BTCUSDT",
		OrigClientOrderID: "entry",
	})
	if err != nil || queried.OrderID != 456 || queried.Status != "FILLED" {
		t.Fatalf("bad queried order err=%v order=%#v", err, queried)
	}
	modified, err := client.ModifyOrder(context.Background(), ModifyOrderRequest{
		Symbol:   "BTCUSDT",
		Side:     "BUY",
		Quantity: "0.1",
		Price:    "49999.9",
		OrderID:  "456",
	})
	if err != nil || modified.Price != "49999.9" {
		t.Fatalf("bad modify ack err=%v ack=%#v", err, modified)
	}
	canceled, err := client.CancelOrder(context.Background(), CancelOrderRequest{
		Symbol:            "BTCUSDT",
		OrigClientOrderID: "entry",
	})
	if err != nil || canceled.Status != "CANCELED" {
		t.Fatalf("bad cancel ack err=%v ack=%#v", err, canceled)
	}
	algo, err := client.NewAlgoOrder(context.Background(), AlgoOrderRequest{
		Symbol:           "BTCUSDT",
		Side:             "SELL",
		Type:             "TAKE_PROFIT_MARKET",
		Quantity:         "0.1",
		TriggerPrice:     "51000",
		WorkingType:      "MARK_PRICE",
		NewClientOrderID: "tp",
		ReduceOnly:       true,
		ClosePosition:    true,
	})
	if err != nil || algo.AlgoID != 789 {
		t.Fatalf("bad algo ack err=%v ack=%#v", err, algo)
	}
	openAlgos, err := client.OpenAlgoOrders(context.Background(), "BTCUSDT")
	if err != nil || len(openAlgos) != 1 || openAlgos[0].AlgoID != 790 || openAlgos[0].TriggerPrice != "51000" || openAlgos[0].CallbackRate != "1.5" || openAlgos[0].WorkingType != "MARK_PRICE" {
		t.Fatalf("bad open algo orders err=%v orders=%#v", err, openAlgos)
	}
	canceledAlgo, err := client.CancelAlgoOrder(context.Background(), 790, "")
	if err != nil || canceledAlgo.AlgoID != 790 || canceledAlgo.Code != "200" {
		t.Fatalf("bad cancel algo err=%v ack=%#v", err, canceledAlgo)
	}
	if err := client.CancelAllOpenOrders(context.Background(), "BTCUSDT"); err != nil {
		t.Fatalf("cancel all open orders: %v", err)
	}
	if err := client.CancelAllAlgoOpenOrders(context.Background(), "BTCUSDT"); err != nil {
		t.Fatalf("cancel all algo open orders: %v", err)
	}
	if seen[http.MethodPost+" /fapi/v1/order"].Get("timeInForce") != "GTC" || seen[http.MethodPost+" /fapi/v1/order"].Get("newClientOrderId") != "entry" {
		t.Fatalf("bad order form: %#v", seen[http.MethodPost+" /fapi/v1/order"])
	}
	if seen[http.MethodGet+" /fapi/v1/order"].Get("origClientOrderId") != "entry" || seen[http.MethodGet+" /fapi/v1/order"].Get("symbol") != "BTCUSDT" {
		t.Fatalf("bad query order form: %#v", seen[http.MethodGet+" /fapi/v1/order"])
	}
	if seen[http.MethodPut+" /fapi/v1/order"].Get("price") != "49999.9" || seen[http.MethodPut+" /fapi/v1/order"].Get("orderId") != "456" {
		t.Fatalf("bad modify order form: %#v", seen[http.MethodPut+" /fapi/v1/order"])
	}
	if seen[http.MethodDelete+" /fapi/v1/order"].Get("origClientOrderId") != "entry" {
		t.Fatalf("bad cancel order form: %#v", seen[http.MethodDelete+" /fapi/v1/order"])
	}
	if seen[http.MethodPost+" /fapi/v1/algoOrder"].Get("algoType") != "CONDITIONAL" || seen[http.MethodPost+" /fapi/v1/algoOrder"].Get("reduceOnly") != "true" || seen[http.MethodPost+" /fapi/v1/algoOrder"].Get("closePosition") != "true" {
		t.Fatalf("bad algo form: %#v", seen[http.MethodPost+" /fapi/v1/algoOrder"])
	}
	if seen[http.MethodGet+" /fapi/v1/openAlgoOrders"].Get("algoType") != "CONDITIONAL" || seen[http.MethodGet+" /fapi/v1/openAlgoOrders"].Get("symbol") != "BTCUSDT" {
		t.Fatalf("bad open algo form: %#v", seen[http.MethodGet+" /fapi/v1/openAlgoOrders"])
	}
	if seen[http.MethodDelete+" /fapi/v1/algoOrder"].Get("algoId") != "790" {
		t.Fatalf("bad cancel algo form: %#v", seen[http.MethodDelete+" /fapi/v1/algoOrder"])
	}
	if seen[http.MethodDelete+" /fapi/v1/allOpenOrders"].Get("symbol") != "BTCUSDT" {
		t.Fatalf("bad cancel all orders form: %#v", seen[http.MethodDelete+" /fapi/v1/allOpenOrders"])
	}
	if seen[http.MethodDelete+" /fapi/v1/algoOpenOrders"].Get("symbol") != "BTCUSDT" {
		t.Fatalf("bad cancel all algo form: %#v", seen[http.MethodDelete+" /fapi/v1/algoOpenOrders"])
	}
}

func TestClientTicker24hrParsesPublicRequest(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/fapi/v1/ticker/24hr" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if r.Header.Get("X-MBX-APIKEY") != "" {
			t.Fatal("24hr ticker request should not be signed")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"symbol":"BTCUSDT","lastPrice":"64000.1","volume":"12.3","quoteVolume":"787201.23","closeTime":1784880000000},
			{"symbol":"ETHUSDT","lastPrice":"3000","volume":"20","quoteVolume":"60000","closeTime":1784880001000}
		]`))
	}))
	defer ts.Close()

	client := Client{BaseURL: ts.URL, HTTPClient: ts.Client()}
	tickers, err := client.Ticker24hr(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tickers) != 2 || tickers[0].Symbol != "BTCUSDT" || tickers[0].QuoteVolume != "787201.23" || tickers[0].CloseTime != 1784880000000 {
		t.Fatalf("bad 24hr tickers: %#v", tickers)
	}
}

func splitSignatureTail(t *testing.T, raw string) (string, string) {
	t.Helper()
	const marker = "&signature="
	idx := strings.LastIndex(raw, marker)
	if idx < 0 || idx+len(marker) >= len(raw) {
		t.Fatalf("signature must be appended at the end of request params: %s", raw)
	}
	payload := raw[:idx]
	signature := raw[idx+len(marker):]
	if strings.Contains(payload, "signature=") || strings.Contains(signature, "&") {
		t.Fatalf("signature must appear exactly once at the end of request params: %s", raw)
	}
	return payload, signature
}

func qWithoutSignature(values url.Values) url.Values {
	out := make(url.Values, len(values))
	for key, vals := range values {
		if key == "signature" {
			continue
		}
		out[key] = append([]string(nil), vals...)
	}
	return out
}

func TestExchangeInfoFiltersAndSymbolDerivation(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/fapi/v1/exchangeInfo" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"symbols":[{"symbol":"BTCUSDT","status":"TRADING","pricePrecision":2,"quantityPrecision":3,"filters":[{"filterType":"PRICE_FILTER","tickSize":"0.10"},{"filterType":"LOT_SIZE","minQty":"0.001","maxQty":"100","stepSize":"0.001"},{"filterType":"MARKET_LOT_SIZE","minQty":"0.01","maxQty":"50","stepSize":"0.01"}]}]}`))
	}))
	defer ts.Close()
	client := Client{BaseURL: ts.URL, HTTPClient: ts.Client()}
	info, err := client.SymbolInfo(context.Background(), "BTCUSDT")
	if err != nil {
		t.Fatal(err)
	}
	filters, err := info.TradingFilters()
	if err != nil {
		t.Fatal(err)
	}
	if filters.TickSize != 0.10 || filters.StepSize != 0.001 || filters.MinQty != "0.001" || filters.MaxQty != "100" || filters.MarketStepSize != 0.01 || filters.MarketMinQty != "0.01" || filters.MarketMaxQty != "50" {
		t.Fatalf("bad filters: %#v", filters)
	}
	if filters.StepSizeForOrderType("MARKET") != 0.01 || filters.MinQtyForOrderType("MARKET") != "0.01" || filters.MaxQtyForOrderType("MARKET") != "50" || filters.MaxQtyForOrderType("LIMIT") != "100" {
		t.Fatalf("bad order-specific filters: %#v", filters)
	}
	cases := []struct {
		coinpair string
		ticker   string
		want     string
	}{
		{coinpair: "BINANCE:ETHUSDT.P", want: "ETHUSDT"},
		{coinpair: "PENGUUSDC", want: "PENGUUSDC"},
		{coinpair: "BINANCE:PENGUUSDC.P", want: "PENGUUSDC"},
		{coinpair: "PENGU-USDC-SWAP", want: "PENGUUSDC"},
		{coinpair: "PENGU/USDC", want: "PENGUUSDC"},
		{coinpair: "PENGU", want: "PENGUUSDT"},
	}
	for _, tc := range cases {
		symbol, err := DeriveUSDMSymbol(tc.coinpair, tc.ticker)
		if err != nil || symbol != tc.want {
			t.Fatalf("DeriveUSDMSymbol(%q, %q)=%q err=%v, want %q", tc.coinpair, tc.ticker, symbol, err, tc.want)
		}
	}
}
