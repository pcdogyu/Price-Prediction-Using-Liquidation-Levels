package gate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotconfig"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

func TestTraderConvertsUSDTNotionalToGateContracts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v4/futures/usdt/contracts/BTC_USDT":
			_, _ = w.Write([]byte(`{"name":"BTC_USDT","quanto_multiplier":"0.001","order_size_min":"1","order_size_max":"100000","order_price_round":"0.1","status":"trading"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v4/futures/usdt/positions/BTC_USDT/set_leverage":
			if r.URL.Query().Get("leverage") != "5" || r.URL.Query().Get("margin_mode") != "isolated" {
				t.Fatalf("unexpected leverage query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"contract":"BTC_USDT","lever":"5"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v4/futures/usdt/orders":
			var req PlaceOrderRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			if req.Contract != "BTC_USDT" || req.Size != "2" || req.Price != "0" || req.TIF != "ioc" || !strings.HasPrefix(req.Text, "t-") {
				t.Fatalf("unexpected Gate order: %#v", req)
			}
			_, _ = w.Write([]byte(`{"id":987654,"contract":"BTC_USDT","status":"open"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	cfg := config.Default()
	cfg.Trading.Env = config.EnvDemo
	cfg.Trading.GateDemoBaseURL = server.URL
	cfg.Trading.RiskType = string(trading.RiskNone)
	cfg.Normalize()
	trader := Trader{Credentials: Credentials{APIKey: "key", SecretKey: "secret"}, HTTPClient: server.Client()}
	signal := trading.Signal{Action: trading.ActionLong, TargetExchange: trading.ExchangeGate, Coinpair: "BTC", Price: trading.NewFlexibleFloat(50000), Amount: trading.NewFlexibleFloat(100), Leverage: 5, PositionEffect: trading.PositionEffectOpen, PositionSide: trading.PositionSideLong}
	result, err := trader.ExecuteSignal(context.Background(), signal, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if result.TargetExchange != trading.ExchangeGate || result.OrdID != "987654" || result.InstID != "BTC_USDT" {
		t.Fatalf("unexpected result: %#v", result)
	}
}
