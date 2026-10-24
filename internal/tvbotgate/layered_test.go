package gate

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotconfig"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

func TestTraderPlacesSixLayeredGateOrdersFromOneTicker(t *testing.T) {
	var orders []PlaceOrderRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v4/futures/usdt/contracts/BTC_USDT":
			_, _ = w.Write([]byte(`{"name":"BTC_USDT","quanto_multiplier":"0.001","order_size_min":"1","order_size_max":"100000","order_price_round":"0.1","last_price":"100","status":"trading"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v4/futures/usdt/tickers":
			_, _ = w.Write([]byte(`[{"contract":"BTC_USDT","last":"100"}]`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v4/futures/usdt/positions/BTC_USDT/set_leverage":
			_, _ = w.Write([]byte(`{"contract":"BTC_USDT","lever":"5"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v4/futures/usdt/orders":
			var req PlaceOrderRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			orders = append(orders, req)
			_, _ = w.Write([]byte(`{"id":987654,"contract":"BTC_USDT","status":"open"}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	cfg := config.Default()
	cfg.Trading.GateDemoBaseURL = server.URL
	cfg.Trading.OrderType = string(trading.OrderTypeLayered)
	cfg.Trading.RiskType = string(trading.RiskTPSL)
	cfg.Trading.TakeProfitPct = 2
	cfg.Trading.StopLossPct = 1
	cfg.Normalize()
	signal := trading.Signal{Action: trading.ActionLong, TargetExchange: trading.ExchangeGate, Coinpair: "BTC", Price: trading.NewFlexibleFloat(105), Amount: trading.NewFlexibleFloat(10000), Leverage: 5, PositionEffect: trading.PositionEffectOpen}
	trader := Trader{Credentials: Credentials{APIKey: "key", SecretKey: "secret"}, HTTPClient: server.Client()}
	result, err := trader.ExecuteSignal(context.Background(), signal, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(orders) != 6 || len(result.Layers) != 6 || result.Partial {
		t.Fatalf("expected six complete layers, orders=%d result=%#v", len(orders), result)
	}
	if orders[0].Price != "0" || orders[0].TIF != "ioc" || orders[1].Price != "105" || orders[2].Price != "99.5" || orders[5].Price != "97" {
		t.Fatalf("unexpected layered Gate requests: %#v", orders)
	}
	if len(result.RiskOrders) != 12 {
		t.Fatalf("expected two risk orders for each Gate layer, got %#v", result.RiskOrders)
	}
	for i, order := range orders {
		if order.TPTriggerPrice == "" || order.SLTriggerPrice == "" {
			t.Fatalf("Gate layer %d is missing its TP/SL trigger prices: %#v", i, order)
		}
	}
	if !almostEqualGatePrice(orders[0].TPTriggerPrice, 100.4) || !almostEqualGatePrice(orders[0].SLTriggerPrice, 99.8) ||
		!almostEqualGatePrice(orders[1].TPTriggerPrice, 105.4) || !almostEqualGatePrice(orders[1].SLTriggerPrice, 104.8) ||
		!almostEqualGatePrice(orders[2].TPTriggerPrice, 99.9) || !almostEqualGatePrice(orders[2].SLTriggerPrice, 99.3) ||
		!almostEqualGatePrice(orders[5].TPTriggerPrice, 97.4) || !almostEqualGatePrice(orders[5].SLTriggerPrice, 96.8) {
		t.Fatalf("each Gate layer should calculate TP/SL from its own entry price: %#v", orders)
	}
	for i, layer := range result.Layers {
		if len(layer.RiskOrders) != 2 {
			t.Fatalf("Gate layer %d should have its own TP and SL result: %#v", i, layer)
		}
	}
}

func almostEqualGatePrice(raw string, want float64) bool {
	got, err := strconv.ParseFloat(raw, 64)
	return err == nil && math.Abs(got-want) < 1e-9
}

func TestGateLayerPriceFormattingRemovesBinaryFloatTail(t *testing.T) {
	got := formatGateLayerPrice(2465*(1-1.1/100), "0.01", trading.ActionLong)
	if got != "2437.88" {
		t.Fatalf("layer price should be formatted to the contract tick precision, got %q", got)
	}
	if got := formatStep(2465*(1+1.5/100), "0.01", false); got != "2501.98" {
		t.Fatalf("risk trigger should be formatted to the contract tick precision, got %q", got)
	}
}
