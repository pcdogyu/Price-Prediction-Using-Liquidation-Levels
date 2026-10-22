package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	config "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotconfig"
	gate "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotgate"
)

func TestTVBotGatePendingOrdersIncludesPriceTriggeredOrders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v4/futures/usdt/orders":
			if r.Method != http.MethodGet || r.URL.Query().Get("status") != "open" {
				t.Fatalf("unexpected Gate open orders request: %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`[{"id":101,"contract":"BTC_USDT","size":2,"left":2,"price":"83000","status":"open","text":"entry-1","create_time":1761027807,"update_time":1761027808}]`))
		case "/api/v4/futures/usdt/price_orders":
			if r.Method != http.MethodGet || r.URL.Query().Get("status") != "open" {
				t.Fatalf("unexpected Gate price orders request: %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`[
				{"initial":{"contract":"BTC_USDT","size":-2,"price":"0","tif":"ioc","text":"entry-1TP","is_reduce_only":true,"is_close":true},"trigger":{"strategy_type":0,"price_type":1,"price":"86000","rule":1,"expiration":0},"id":201,"id_string":"201","create_time":1761027809,"status":"open","order_type":"close-long-order","pos_margin_mode":"cross"},
				{"initial":{"contract":"BTC_USDT","size":-2,"price":"0","tif":"ioc","text":"entry-1SL","is_reduce_only":true,"is_close":true},"trigger":{"strategy_type":0,"price_type":1,"price":"81000","rule":2,"expiration":0},"id":202,"id_string":"202","create_time":1761027810,"status":"open","order_type":"close-long-order","pos_margin_mode":"cross"}
			]`))
		default:
			t.Fatalf("unexpected Gate path: %s", r.URL.Path)
		}
	}))
	defer upstream.Close()

	srv := newTestServer(t)
	gateStore, err := gate.NewCredentialStore("", gate.Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gateStore.UpdateAccount(gate.CredentialAccountUpdate{
		ID: "gate-main", Active: true,
		Credentials: gate.Credentials{APIKey: "gate-key", SecretKey: "gate-secret"},
	}); err != nil {
		t.Fatal(err)
	}
	srv.GateCredentials = gateStore
	srv.GateHTTPClient = upstream.Client()
	cfg := srv.ConfigStore.Get()
	cfg.Trading.GateDemoBaseURL = upstream.URL
	srv.ConfigStore = config.NewStore("", cfg)

	req := httptest.NewRequest(http.MethodGet, "/tvbot/pending-orders?exchange=gate&env=demo&api_id=gate-main", nil)
	req.SetBasicAuth("admin", "Admin123")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("pending orders status=%d body=%s", rr.Code, rr.Body.String())
	}

	var resp pendingOrdersResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK || resp.Exchange != "gate" || resp.APIID != "gate-main" || resp.Count != 1 || resp.NormalCount != 1 || resp.AlgoCount != 2 || resp.TotalCount != 3 || len(resp.AlgoOrders) != 2 {
		t.Fatalf("bad Gate pending response: %#v", resp)
	}
	if resp.AlgoOrders[0].OrderGroup != "algo" || resp.AlgoOrders[0].AlgoID != "202" || resp.AlgoOrders[0].AlgoClOrdID != "entry-1SL" || resp.AlgoOrders[0].TriggerPx != "81000" || resp.AlgoOrders[0].Side != "sell" || resp.AlgoOrders[0].PosSide != "long" || resp.AlgoOrders[0].Chaseable || resp.AlgoOrders[0].ChaseUnavailableReason == "" {
		t.Fatalf("bad Gate stop-loss algo order: %#v", resp.AlgoOrders[0])
	}
}
