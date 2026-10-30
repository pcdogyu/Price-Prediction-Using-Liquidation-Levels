package gate

import (
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClientSignsGateV4Request(t *testing.T) {
	const body = `{"contract":"BTC_USDT","size":"2","price":"0","tif":"ioc"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v4/futures/usdt/orders" || r.URL.RawQuery != "foo=bar" {
			t.Fatalf("unexpected request: %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
		}
		gotBody, _ := io.ReadAll(r.Body)
		if string(gotBody) != body {
			t.Fatalf("body = %q", gotBody)
		}
		bodyHash := sha512.Sum512(gotBody)
		timestamp := "1700000000"
		expectedText := strings.Join([]string{"POST", "/api/v4/futures/usdt/orders", "foo=bar", hex.EncodeToString(bodyHash[:]), timestamp}, "\n")
		mac := hmac.New(sha512.New, []byte("secret"))
		_, _ = mac.Write([]byte(expectedText))
		if r.Header.Get("KEY") != "key" || r.Header.Get("Timestamp") != timestamp || r.Header.Get("SIGN") != hex.EncodeToString(mac.Sum(nil)) {
			t.Fatalf("signature headers do not match: %#v", r.Header)
		}
		_, _ = w.Write([]byte(`{"id":123}`))
	}))
	defer server.Close()
	client := Client{BaseURL: server.URL, Credentials: Credentials{APIKey: "key", SecretKey: "secret"}, HTTPClient: server.Client(), Now: func() time.Time { return time.Unix(1700000000, 0) }}
	var out Order
	err := client.Do(context.Background(), http.MethodPost, "/futures/usdt/orders", map[string][]string{"foo": {"bar"}}, json.RawMessage(body), true, &out)
	if err != nil {
		t.Fatal(err)
	}
	if out.IDString() != "123" {
		t.Fatalf("order id = %q", out.IDString())
	}
}

func TestClientDecodesNumericPositionFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v4/futures/usdt/positions" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`[{"contract":"BTC_USDT","size":2,"leverage":5,"value":134400.2,"margin":26880.04,"initial_margin":26880.04,"entry_price":67123.45,"mark_price":67200.1,"liq_price":62000,"unrealised_pnl":12.5,"mode":"single","pos_margin_mode":"cross","lever":5,"update_time":1761027808,"open_time":1761027700}]`))
	}))
	defer server.Close()

	client := Client{BaseURL: server.URL, Credentials: Credentials{APIKey: "key", SecretKey: "secret"}, HTTPClient: server.Client()}
	positions, err := client.Positions(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(positions) != 1 {
		t.Fatalf("positions = %#v", positions)
	}
	got := positions[0]
	if got.Contract != "BTC_USDT" || got.Size != "2" || got.Leverage != "5" || got.Value != "134400.2" || got.Margin != "26880.04" || got.InitialMargin != "26880.04" || got.EntryPrice != "67123.45" || got.MarkPrice != "67200.1" || got.LiquidationPrice != "62000" || got.UnrealisedPnl != "12.5" || got.MarginMode != "cross" || got.Lever != "5" || got.UpdateTime != "1761027808" || got.OpenTime != "1761027700" {
		t.Fatalf("decoded position = %#v", got)
	}
}

func TestClientDecodesNumericMyTradeFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v4/futures/usdt/my_trades" || r.URL.Query().Get("contract") != "BTC_USDT" || r.URL.Query().Get("limit") != "1000" {
			t.Fatalf("unexpected request: %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`[{"id":123,"contract":"BTC_USDT","order_id":456,"size":-14,"price":83096.5,"create_time":1761027700.25,"text":"t-entry","close_size":0,"trade_value":116.3,"fee":-0.02,"point_fee":0,"role":"maker"}]`))
	}))
	defer server.Close()

	client := Client{BaseURL: server.URL, Credentials: Credentials{APIKey: "key", SecretKey: "secret"}, HTTPClient: server.Client()}
	trades, err := client.MyTrades(context.Background(), "BTC_USDT")
	if err != nil {
		t.Fatal(err)
	}
	if len(trades) != 1 {
		t.Fatalf("trades = %#v", trades)
	}
	got := trades[0]
	if got.ID != "123" || got.Contract != "BTC_USDT" || got.OrderID != "456" || got.Size != "-14" || got.Price != "83096.5" || got.CreateTime != "1761027700.25" || got.Text != "t-entry" || got.CloseSize != "0" || got.TradeValue != "116.3" || got.Fee != "-0.02" || got.PointFee != "0" || got.Role != "maker" {
		t.Fatalf("decoded trade = %#v", got)
	}
}

func TestClientFetchesGateAnalysisTimeRangeData(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		query := r.URL.Query()
		switch r.URL.Path {
		case "/api/v4/futures/usdt/my_trades_timerange":
			if query.Get("contract") != "BTC_USDT" || query.Get("from") != "1761027600" || query.Get("to") != "1761028200" || query.Get("limit") != "3" || query.Get("offset") != "2" {
				t.Fatalf("unexpected trade query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`[{"time":1761027700,"contract":"BTC_USDT","order_id":456,"size":-2,"price":83000,"pnl":"1.2","pnl_fee":"-0.02"}]`))
		case "/api/v4/futures/usdt/position_close":
			if query.Get("contract") != "BTC_USDT" || query.Get("from") != "1761027600" || query.Get("to") != "1761028200" || query.Get("limit") != "3" || query.Get("offset") != "2" {
				t.Fatalf("unexpected position-close query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`[{"time":1761027700.5,"contract":"BTC_USDT","side":"long","pnl":1.2,"pnl_pnl":1.3,"pnl_fund":-0.08,"pnl_fee":-0.02,"max_size":2,"accum_size":2,"first_open_time":1761027600,"long_price":83000,"short_price":83600}]`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client := Client{BaseURL: server.URL, Credentials: Credentials{APIKey: "key", SecretKey: "secret"}, HTTPClient: server.Client()}
	from := time.Unix(1761027600, 0).UTC()
	to := time.Unix(1761028200, 0).UTC()
	trades, err := client.MyTradesTimerange(context.Background(), "BTC_USDT", from, to, 3, 2)
	if err != nil || len(trades) != 1 || trades[0].Contract != "BTC_USDT" {
		t.Fatalf("timerange trades=%#v err=%v", trades, err)
	}
	closes, err := client.PositionCloses(context.Background(), "BTC_USDT", from, to, 3, 2)
	if err != nil || len(closes) != 1 || closes[0].Pnl != "1.2" || closes[0].LongPrice != "83000" || closes[0].ShortPrice != "83600" {
		t.Fatalf("position closes=%#v err=%v", closes, err)
	}
}

func TestClientDecodesNumericOpenOrderFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v4/futures/usdt/orders" || r.URL.Query().Get("status") != "open" {
			t.Fatalf("unexpected request: %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`[{"id":123,"contract":"BTC_USDT","size":-8,"left":-8,"price":83065.8,"fill_price":0,"status":"open","finish_as":"-","text":"t-test","is_reduce_only":false,"is_close":false,"create_time":1761027807,"update_time":1761027808}]`))
	}))
	defer server.Close()

	client := Client{BaseURL: server.URL, Credentials: Credentials{APIKey: "key", SecretKey: "secret"}, HTTPClient: server.Client()}
	orders, err := client.OpenOrders(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(orders) != 1 {
		t.Fatalf("orders = %#v", orders)
	}
	got := orders[0]
	if got.IDString() != "123" || got.Contract != "BTC_USDT" || got.Size != "-8" || got.Left != "-8" || got.Price != "83065.8" || got.FillPrice != "0" || got.CreateTime != "1761027807" || got.UpdateTime != "1761027808" {
		t.Fatalf("decoded order = %#v", got)
	}
}

func TestClientFetchesOpenPriceTriggeredOrders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v4/futures/usdt/price_orders" || r.URL.Query().Get("status") != "open" || r.URL.Query().Get("contract") != "BTC_USDT" {
			t.Fatalf("unexpected request: %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`[
			{
				"initial":{"contract":"BTC_USDT","size":-2,"amount":"-2.5","price":"0","tif":"ioc","text":"entry-1SL","is_reduce_only":true,"is_close":true},
				"trigger":{"strategy_type":0,"price_type":1,"price":83000.5,"rule":2,"expiration":86400},
				"id":1234567890123,"id_string":"1234567890123","create_time":1761027807.25,"status":"open","order_type":"close-long-order","pos_margin_mode":"cross"
			}
		]`))
	}))
	defer server.Close()

	client := Client{BaseURL: server.URL, Credentials: Credentials{APIKey: "key", SecretKey: "secret"}, HTTPClient: server.Client()}
	orders, err := client.OpenPriceTriggeredOrders(context.Background(), "BTC_USDT")
	if err != nil {
		t.Fatal(err)
	}
	if len(orders) != 1 {
		t.Fatalf("orders = %#v", orders)
	}
	got := orders[0]
	if got.IDString() != "1234567890123" || got.Initial.Contract != "BTC_USDT" || got.Initial.Size != "-2" || got.Initial.Amount != "-2.5" || got.Initial.Price != "0" || got.Initial.Text != "entry-1SL" || !got.Initial.IsReduceOnly || !got.Initial.IsClose {
		t.Fatalf("decoded initial order = %#v", got.Initial)
	}
	if got.Trigger.Price != "83000.5" || got.Trigger.PriceType != 1 || got.Trigger.Rule != 2 || got.Trigger.Expiration != 86400 || got.CreateTime != "1761027807.25" || got.Status != "open" || got.OrderType != "close-long-order" || got.PosMarginMode != "cross" {
		t.Fatalf("decoded triggered order = %#v", got)
	}
}

func TestClientCreatesAmendsAndCancelsPriceTriggeredOrder(t *testing.T) {
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v4/futures/usdt/price_orders":
			var req PriceTriggeredOrderRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			if req.Initial.Contract != "ETH_USDT" || req.Initial.Size != "0" || req.Initial.Price != "0" || !req.Initial.Close || !req.Initial.ReduceOnly || req.OrderType != "close-short-order" || req.Trigger.Price != "2272.46" || req.Trigger.Rule != 1 {
				t.Fatalf("unexpected create request: %#v", req)
			}
			_, _ = w.Write([]byte(`{"id":123,"id_string":"123"}`))
		case r.Method == http.MethodPut && r.URL.Path == "/api/v4/futures/usdt/price_orders/amend/123":
			var req PriceTriggeredOrderAmendment
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			if req.OrderID != "123" || req.TriggerPx != "2272.50" || req.Price != "0" || req.Size != "0" || !req.Close {
				t.Fatalf("unexpected amend request: %#v", req)
			}
			_, _ = w.Write([]byte(`{"id":123,"id_string":"123"}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v4/futures/usdt/price_orders/123":
			_, _ = w.Write([]byte(`{"id":123,"id_string":"123","status":"finished"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := Client{BaseURL: server.URL, Credentials: Credentials{APIKey: "key", SecretKey: "secret"}, HTTPClient: server.Client()}

	created, err := client.PlacePriceTriggeredOrder(context.Background(), PriceTriggeredOrderRequest{
		Initial:   PriceTriggeredInitial{Contract: "ETH_USDT", Size: "0", Price: "0", Close: true, ReduceOnly: true},
		Trigger:   PriceTriggeredTrigger{Price: "2272.46", Rule: 1},
		OrderType: "close-short-order",
	})
	if err != nil || created.IDValue() != "123" {
		t.Fatalf("created=%#v err=%v", created, err)
	}
	amended, err := client.AmendPriceTriggeredOrder(context.Background(), "123", PriceTriggeredOrderAmendment{TriggerPx: "2272.50", Size: "0", Price: "0", Close: true})
	if err != nil || amended.IDValue() != "123" {
		t.Fatalf("amended=%#v err=%v", amended, err)
	}
	if _, err := client.CancelPriceTriggeredOrder(context.Background(), "123"); err != nil {
		t.Fatal(err)
	}
	if len(methods) != 3 || methods[0] != "POST /api/v4/futures/usdt/price_orders" || methods[1] != "PUT /api/v4/futures/usdt/price_orders/amend/123" || methods[2] != "DELETE /api/v4/futures/usdt/price_orders/123" {
		t.Fatalf("unexpected method sequence: %#v", methods)
	}
}
