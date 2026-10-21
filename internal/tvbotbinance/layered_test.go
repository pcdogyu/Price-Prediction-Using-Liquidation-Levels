package binance

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotconfig"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

func TestTraderPlacesSixLayeredBinanceOrdersFromOneTicker(t *testing.T) {
	var orders []url.Values
	orderID := 100
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/fapi/v1/exchangeInfo":
			_, _ = w.Write([]byte(`{"symbols":[{"symbol":"BTCUSDT","status":"TRADING","pricePrecision":1,"quantityPrecision":3,"filters":[{"filterType":"PRICE_FILTER","tickSize":"0.1"},{"filterType":"LOT_SIZE","minQty":"0.001","stepSize":"0.001"}]}]}`))
		case "/fapi/v3/positionRisk", "/fapi/v1/openOrders":
			_, _ = w.Write([]byte(`[]`))
		case "/fapi/v1/ticker/bookTicker":
			_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","bidPrice":"99","askPrice":"101","bidQty":"1","askQty":"1"}`))
		case "/fapi/v1/marginType":
			_, _ = w.Write([]byte(`{"code":200,"msg":"success"}`))
		case "/fapi/v1/leverage":
			_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","leverage":5}`))
		case "/fapi/v1/order":
			orders = append(orders, cloneValues(r.Form))
			orderID++
			_, _ = w.Write([]byte(`{"orderId":` + strconv.Itoa(orderID) + `,"symbol":"BTCUSDT","status":"NEW","clientOrderId":"layer","price":"0","origQty":"1","executedQty":"0","type":"` + r.Form.Get("type") + `","side":"BUY"}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer ts.Close()

	cfg := config.Default()
	cfg.Trading.BinanceDemoBaseURL = ts.URL
	cfg.Trading.OrderType = string(trading.OrderTypeLayered)
	cfg.Trading.RiskType = string(trading.RiskNone)
	cfg.Normalize()
	signal := trading.Signal{Action: trading.ActionLong, Coinpair: "BTC", Price: trading.NewFlexibleFloat(105), Amount: trading.NewFlexibleFloat(10000), Leverage: 5, PositionEffect: trading.PositionEffectOpen}
	trader := Trader{Credentials: Credentials{APIKey: "key", SecretKey: "secret"}, HTTPClient: ts.Client()}
	result, err := trader.ExecuteSignal(context.Background(), signal, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(orders) != 6 || len(result.Layers) != 6 || result.Partial {
		t.Fatalf("expected six complete layers, orders=%d result=%#v", len(orders), result)
	}
	if orders[0].Get("type") != "MARKET" || orders[1].Get("type") != "LIMIT" || orders[1].Get("price") != "105" || orders[2].Get("price") != "99.5" || orders[5].Get("price") != "97" {
		t.Fatalf("unexpected layered Binance requests: %#v", orders)
	}
}

func TestTraderPlacesTPSLAlgoOrdersForEachBinanceLayer(t *testing.T) {
	entryForms := []url.Values{}
	algoForms := []url.Values{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/fapi/v1/exchangeInfo":
			_, _ = w.Write([]byte(`{"symbols":[{"symbol":"BTCUSDT","status":"TRADING","pricePrecision":1,"quantityPrecision":3,"filters":[{"filterType":"PRICE_FILTER","tickSize":"0.1"},{"filterType":"LOT_SIZE","minQty":"0.001","stepSize":"0.001"}]}]}`))
		case "/fapi/v3/positionRisk", "/fapi/v1/openOrders":
			_, _ = w.Write([]byte(`[]`))
		case "/fapi/v1/ticker/bookTicker":
			_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","bidPrice":"99","askPrice":"101","bidQty":"1","askQty":"1"}`))
		case "/fapi/v1/marginType":
			_, _ = w.Write([]byte(`{"code":200,"msg":"success"}`))
		case "/fapi/v1/leverage":
			_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","leverage":5}`))
		case "/fapi/v1/premiumIndex":
			_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","markPrice":"100","indexPrice":"100","lastFundingRate":"0","time":1784880000000}`))
		case "/fapi/v1/order":
			entryForms = append(entryForms, cloneValues(r.Form))
			_, _ = w.Write([]byte(`{"orderId":123,"symbol":"BTCUSDT","status":"NEW","clientOrderId":"entry","price":"0","origQty":"1","executedQty":"0","type":"MARKET","side":"BUY"}`))
		case "/fapi/v1/algoOrder":
			algoForms = append(algoForms, cloneValues(r.Form))
			_, _ = w.Write([]byte(`{"algoId":456,"clientAlgoId":"algo","algoType":"CONDITIONAL","orderType":"` + r.Form.Get("type") + `","symbol":"BTCUSDT","side":"SELL","quantity":"1","algoStatus":"NEW","triggerPrice":"` + r.Form.Get("triggerPrice") + `"}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer ts.Close()

	cfg := config.Default()
	cfg.Trading.BinanceDemoBaseURL = ts.URL
	cfg.Trading.OrderType = string(trading.OrderTypeLayered)
	cfg.Trading.RiskType = string(trading.RiskTPSL)
	cfg.Trading.TakeProfitPct = 2
	cfg.Trading.StopLossPct = 1
	cfg.Normalize()
	signal := trading.Signal{Action: trading.ActionLong, Coinpair: "BTC", Price: trading.NewFlexibleFloat(105), Amount: trading.NewFlexibleFloat(10000), Leverage: 5, PositionEffect: trading.PositionEffectOpen}
	trader := Trader{Credentials: Credentials{APIKey: "key", SecretKey: "secret"}, HTTPClient: ts.Client()}
	result, err := trader.ExecuteSignal(context.Background(), signal, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(entryForms) != 6 || len(algoForms) != 12 || len(result.Layers) != 6 || result.Partial {
		t.Fatalf("expected two risk orders for each of six layers, entries=%d algos=%d result=%#v", len(entryForms), len(algoForms), result)
	}
	entryIDs := map[string]bool{}
	for _, form := range entryForms {
		id := form.Get("newClientOrderId")
		if id == "" || entryIDs[id] {
			t.Fatalf("layer entry client ids should be unique: %#v", entryForms)
		}
		entryIDs[id] = true
	}
	algosByEntry := map[string]int{}
	for _, form := range algoForms {
		id := form.Get("clientAlgoId")
		parent := strings.TrimSuffix(strings.TrimSuffix(id, "TP"), "SL")
		if !entryIDs[parent] {
			t.Fatalf("algo %q is not associated with a layered entry: entries=%v", id, entryIDs)
		}
		algosByEntry[parent]++
		if form.Get("quantity") == "" || form.Get("triggerPrice") == "" {
			t.Fatalf("layer risk order missing quantity or trigger: %#v", form)
		}
	}
	for entryID := range entryIDs {
		if algosByEntry[entryID] != 2 {
			t.Fatalf("entry %q should have exactly TP and SL, got %d: %#v", entryID, algosByEntry[entryID], algoForms)
		}
	}
	if len(result.RiskOrders) != 12 {
		t.Fatalf("expected twelve risk order results, got %#v", result.RiskOrders)
	}
	for i, layer := range result.Layers {
		if len(layer.RiskOrders) != 2 {
			t.Fatalf("layer %d should have its own TP and SL result: %#v", i, layer)
		}
	}
}

func TestBinanceLayeredRiskAndSplitClientIDsRemainDistinct(t *testing.T) {
	entryID := strings.Repeat("A", 25) + "-L01"
	tpID := binanceClientIDWithSuffix(entryID, "TP")
	slID := binanceClientIDWithSuffix(entryID, "SL")
	tpPartID := splitBinanceClientOrderID(tpID, 1)
	slPartID := splitBinanceClientOrderID(slID, 1)
	ids := []string{tpID, slID, tpPartID, slPartID}
	seen := map[string]bool{}
	for _, id := range ids {
		if len(id) > 32 || id == "" || seen[id] {
			t.Fatalf("layered risk ids should be unique and Binance-compatible: %#v", ids)
		}
		seen[id] = true
	}
	if !strings.HasSuffix(tpID, "TP") || !strings.HasSuffix(slID, "SL") {
		t.Fatalf("risk ids lost their TP/SL suffixes: %#v", ids)
	}
}
