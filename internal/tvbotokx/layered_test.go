package okx

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotconfig"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

func TestTraderPlacesSixLayeredOKXOrdersFromOneTicker(t *testing.T) {
	var orders []PlaceOrderRequest
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v5/account/positions", "/api/v5/trade/orders-pending":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[]}`))
		case "/api/v5/market/ticker":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instId":"BTC-USDT-SWAP","bidPx":"99","askPx":"101","last":"100"}]}`))
		case "/api/v5/account/set-leverage":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{}]}`))
		case "/api/v5/trade/order":
			var req PlaceOrderRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			orders = append(orders, req)
			_, _ = fmt.Fprintf(w, `{"code":"0","msg":"","data":[{"clOrdId":%q,"ordId":%q,"sCode":"0","sMsg":""}]}`, req.ClOrdID, fmt.Sprintf("%d", len(orders)))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer ts.Close()

	cfg := config.Default()
	cfg.Trading.BaseURL = ts.URL
	cfg.Trading.OrderType = string(trading.OrderTypeLayered)
	cfg.Trading.RiskType = string(trading.RiskTPSL)
	cfg.Trading.TakeProfitPct = 2
	cfg.Trading.StopLossPct = 1
	cfg.Normalize()
	signal := trading.Signal{Action: trading.ActionLong, Coinpair: "BTC", Price: trading.NewFlexibleFloat(105), Amount: trading.NewFlexibleFloat(10000), Leverage: 5, PositionEffect: trading.PositionEffectOpen}
	trader := Trader{Credentials: Credentials{APIKey: "key", SecretKey: "secret", Passphrase: "pass"}, HTTPClient: ts.Client()}
	result, err := trader.ExecuteSignal(context.Background(), signal, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(orders) != 6 || len(result.Layers) != 6 || result.Partial {
		t.Fatalf("expected six complete layers, orders=%d result=%#v", len(orders), result)
	}
	if orders[0].OrdType != "market" || orders[1].OrdType != "limit" || orders[1].Px != "105" || orders[2].Px != "99.5" || orders[5].Px != "97" {
		t.Fatalf("unexpected layered OKX requests: %#v", orders)
	}
	if len(result.RiskOrders) != 6 {
		t.Fatalf("expected one combined TP/SL algorithm order for each OKX layer, got %#v", result.RiskOrders)
	}
	seenAlgoIDs := map[string]bool{}
	for i, order := range orders {
		if len(order.AttachAlgoOrds) != 1 {
			t.Fatalf("OKX layer %d should carry its own TP/SL algo order: %#v", i, order)
		}
		attach := order.AttachAlgoOrds[0]
		if attach["tpTriggerRatio"] == "" || attach["slTriggerRatio"] == "" || seenAlgoIDs[attach["attachAlgoClOrdId"]] {
			t.Fatalf("OKX layer %d has invalid or duplicate TP/SL algo metadata: %#v", i, attach)
		}
		seenAlgoIDs[attach["attachAlgoClOrdId"]] = true
	}
	for i, layer := range result.Layers {
		if len(layer.RiskOrders) != 1 {
			t.Fatalf("OKX layer %d should have its own risk result: %#v", i, layer)
		}
	}
}
