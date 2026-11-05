package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tvbotconfig "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotconfig"
	tvbotgate "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotgate"
)

func configureGatePositionCloseTest(t *testing.T, srv *Server, baseURL string) {
	t.Helper()
	cfg := srv.ConfigStore.Get()
	cfg.Trading.GateDemoBaseURL = baseURL
	srv.ConfigStore = tvbotconfig.NewStore("", cfg)
	srv.GateHTTPClient = http.DefaultClient
	credentials, err := tvbotgate.NewCredentialStore(filepath.Join(t.TempDir(), "gate-credentials.json"), tvbotgate.Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	srv.GateCredentials = credentials
	if _, err := credentials.UpdateAccount(tvbotgate.CredentialAccountUpdate{
		ID: "gate-main", Active: true,
		Credentials: tvbotgate.Credentials{APIKey: "gate-key", SecretKey: "gate-secret"},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestTVBotGateFullLimitPositionClose(t *testing.T) {
	restore := isolatePositionCloseJobsForTest()
	defer restore()
	var orderBody map[string]any
	gateServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v4/futures/usdt/positions":
			_, _ = w.Write([]byte(`[{"contract":"BTC_USDT","size":"-20","entry_price":"100","mark_price":"100","pos_margin_mode":"isolated","mode":"single","open_time":"1784800000"}]`))
		case "/api/v4/futures/usdt/contracts/BTC_USDT":
			_, _ = w.Write([]byte(`{"name":"BTC_USDT","order_size_min":"1","order_price_round":"0.1","mark_price":"100","enable_decimal":false}`))
		case "/api/v4/futures/usdt/tickers":
			if r.URL.Query().Get("contract") != "BTC_USDT" {
				t.Fatalf("unexpected ticker query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`[{"contract":"BTC_USDT","last":"100","highest_bid":"99.9","lowest_ask":"100.1"}]`))
		case "/api/v4/futures/usdt/orders":
			switch r.Method {
			case http.MethodGet:
				_, _ = w.Write([]byte(`[]`))
			case http.MethodPost:
				if err := json.NewDecoder(r.Body).Decode(&orderBody); err != nil {
					t.Fatal(err)
				}
				_, _ = w.Write([]byte(`{"id":123,"contract":"BTC_USDT","size":0,"left":0,"price":"100.1","status":"open","text":"t-close","is_reduce_only":true,"is_close":true}`))
			default:
				t.Fatalf("unexpected order method: %s", r.Method)
			}
		case "/api/v4/futures/usdt/price_orders":
			if r.Method != http.MethodGet {
				t.Fatalf("unexpected Gate price order method: %s", r.Method)
			}
			_, _ = w.Write([]byte(`[]`))
		case "/api/v4/futures/usdt/my_trades":
			_, _ = w.Write([]byte(fmt.Sprintf(`[{"id":"entry-buy","contract":%q,"order_id":"entry-buy","size":"1000","create_time":"1784800000"},{"id":"entry-sell","contract":%q,"order_id":"entry-sell","size":"-1000","create_time":"1784800000"}]`, r.URL.Query().Get("contract"), r.URL.Query().Get("contract"))))
		default:
			t.Fatalf("unexpected Gate path %s", r.URL.Path)
		}
	}))
	defer gateServer.Close()

	srv := newTestServer(t)
	configureGatePositionCloseTest(t, srv, gateServer.URL)
	oldPoll, oldTimeout := positionClosePollInterval, positionCloseLimitTimeout
	positionClosePollInterval, positionCloseLimitTimeout = time.Hour, time.Hour
	t.Cleanup(func() {
		positionClosePollInterval, positionCloseLimitTimeout = oldPoll, oldTimeout
	})

	req := httptest.NewRequest(http.MethodPost, "/tvbot/positions/close", bytes.NewReader([]byte(`{"exchange":"gate","api_id":"gate-main","inst_id":"BTC_USDT","pos_side":"short","mode":"limit"}`)))
	req.SetBasicAuth("admin", "Admin123")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("Gate full limit close status=%d body=%s", rr.Code, rr.Body.String())
	}
	if orderBody["size"] != "0" || orderBody["price"] != "100.1" || orderBody["tif"] != "gtc" || orderBody["close"] != true || orderBody["reduce_only"] != true {
		t.Fatalf("bad Gate full limit close order: %#v", orderBody)
	}
	var response positionCloseResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.OK || response.Status != "running" || response.Mode != "limit" || response.Px != "100.1" || response.Sz != "20" {
		t.Fatalf("bad Gate full limit close response: %#v", response)
	}
}

func TestTVBotGateMarketPositionCloseUsesRequiredTextPrefix(t *testing.T) {
	var orderBody map[string]any
	gateServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v4/futures/usdt/positions":
			_, _ = w.Write([]byte(`[{"contract":"BTC_USDT","size":"-20","entry_price":"100","mark_price":"100","pos_margin_mode":"isolated","mode":"single","open_time":"1784800000"}]`))
		case "/api/v4/futures/usdt/orders":
			switch r.Method {
			case http.MethodGet:
				_, _ = w.Write([]byte(`[]`))
			case http.MethodPost:
				if err := json.NewDecoder(r.Body).Decode(&orderBody); err != nil {
					t.Fatal(err)
				}
				_, _ = w.Write([]byte(`{"id":789,"contract":"BTC_USDT","size":0,"left":0,"price":"0","status":"finished","text":"t-close","is_reduce_only":true,"is_close":true}`))
			default:
				t.Fatalf("unexpected order method: %s", r.Method)
			}
		case "/api/v4/futures/usdt/price_orders":
			if r.Method != http.MethodGet {
				t.Fatalf("unexpected Gate price order method: %s", r.Method)
			}
			_, _ = w.Write([]byte(`[]`))
		case "/api/v4/futures/usdt/my_trades":
			_, _ = w.Write([]byte(fmt.Sprintf(`[{"id":"entry-buy","contract":%q,"order_id":"entry-buy","size":"1000","create_time":"1784800000"},{"id":"entry-sell","contract":%q,"order_id":"entry-sell","size":"-1000","create_time":"1784800000"}]`, r.URL.Query().Get("contract"), r.URL.Query().Get("contract"))))
		default:
			t.Fatalf("unexpected Gate path %s", r.URL.Path)
		}
	}))
	defer gateServer.Close()

	srv := newTestServer(t)
	configureGatePositionCloseTest(t, srv, gateServer.URL)
	req := httptest.NewRequest(http.MethodPost, "/tvbot/positions/close", bytes.NewReader([]byte(`{"exchange":"gate","api_id":"gate-main","inst_id":"BTC_USDT","pos_side":"short","mode":"market"}`)))
	req.SetBasicAuth("admin", "Admin123")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("Gate market close status=%d body=%s", rr.Code, rr.Body.String())
	}
	text, ok := orderBody["text"].(string)
	if !ok || !strings.HasPrefix(text, "t-") {
		t.Fatalf("Gate market close text must start with t-: %#v", orderBody)
	}
	if orderBody["size"] != "0" || orderBody["price"] != "0" || orderBody["tif"] != "ioc" || orderBody["close"] != true || orderBody["reduce_only"] != true {
		t.Fatalf("bad Gate market close order: %#v", orderBody)
	}
}

func TestTVBotGateMarketPositionCloseFallsBackToMarketableIOCOnPriceProtection(t *testing.T) {
	var orderBodies []map[string]any
	gateServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v4/futures/usdt/positions":
			_, _ = w.Write([]byte(`[{"contract":"BTC_USDT","size":"-20","entry_price":"100","mark_price":"84.755","pos_margin_mode":"isolated","mode":"single","open_time":"1784800000"}]`))
		case "/api/v4/futures/usdt/contracts/BTC_USDT":
			_, _ = w.Write([]byte(`{"name":"BTC_USDT","order_size_min":"1","order_price_round":"0.1","mark_price":"84.755","enable_decimal":false}`))
		case "/api/v4/futures/usdt/tickers":
			if r.URL.Query().Get("contract") != "BTC_USDT" {
				t.Fatalf("unexpected ticker query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`[{"contract":"BTC_USDT","last":"87.2","highest_bid":"87.1","lowest_ask":"87.279"}]`))
		case "/api/v4/futures/usdt/orders":
			switch r.Method {
			case http.MethodGet:
				_, _ = w.Write([]byte(`[]`))
			case http.MethodPost:
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				orderBodies = append(orderBodies, body)
				if len(orderBodies) == 1 {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"label":"MARKET_PRICE_TOO_DEVIATED","message":"buy market order stop match price 86.4501 less than ask1 price 87.279 while mark_price 84.755 and slip ratio 0.05"}`))
					return
				}
				_, _ = w.Write([]byte(`{"id":790,"contract":"BTC_USDT","size":0,"left":0,"price":"87.3","status":"finished","text":"t-close","is_reduce_only":true,"is_close":true}`))
			default:
				t.Fatalf("unexpected order method: %s", r.Method)
			}
		case "/api/v4/futures/usdt/price_orders":
			if r.Method != http.MethodGet {
				t.Fatalf("unexpected Gate price order method: %s", r.Method)
			}
			_, _ = w.Write([]byte(`[]`))
		case "/api/v4/futures/usdt/my_trades":
			_, _ = w.Write([]byte(fmt.Sprintf(`[{"id":"entry-buy","contract":%q,"order_id":"entry-buy","size":"1000","create_time":"1784800000"},{"id":"entry-sell","contract":%q,"order_id":"entry-sell","size":"-1000","create_time":"1784800000"}]`, r.URL.Query().Get("contract"), r.URL.Query().Get("contract"))))
		default:
			t.Fatalf("unexpected Gate path %s", r.URL.Path)
		}
	}))
	defer gateServer.Close()

	srv := newTestServer(t)
	configureGatePositionCloseTest(t, srv, gateServer.URL)
	req := httptest.NewRequest(http.MethodPost, "/tvbot/positions/close", bytes.NewReader([]byte(`{"exchange":"gate","api_id":"gate-main","inst_id":"BTC_USDT","pos_side":"short","mode":"market"}`)))
	req.SetBasicAuth("admin", "Admin123")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("Gate market close fallback status=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(orderBodies) != 2 {
		t.Fatalf("expected market close and one fallback order, got %d: %#v", len(orderBodies), orderBodies)
	}
	if orderBodies[0]["price"] != "0" || orderBodies[0]["tif"] != "ioc" || orderBodies[0]["close"] != true || orderBodies[0]["reduce_only"] != true {
		t.Fatalf("bad initial Gate market close order: %#v", orderBodies[0])
	}
	if orderBodies[1]["size"] != "0" || orderBodies[1]["price"] != "87.3" || orderBodies[1]["tif"] != "ioc" || orderBodies[1]["close"] != true || orderBodies[1]["reduce_only"] != true {
		t.Fatalf("bad Gate marketable IOC fallback order: %#v", orderBodies[1])
	}
}

func TestTVBotGatePartialLimitPositionCloseUsesReduceOnlySize(t *testing.T) {
	restore := isolatePositionCloseJobsForTest()
	defer restore()
	var orderBody map[string]any
	gateServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v4/futures/usdt/positions":
			_, _ = w.Write([]byte(`[{"contract":"ETH_USDT","size":"2","entry_price":"100","mark_price":"100","pos_margin_mode":"cross","mode":"single","open_time":"1784800000"}]`))
		case "/api/v4/futures/usdt/contracts/ETH_USDT":
			_, _ = w.Write([]byte(`{"name":"ETH_USDT","order_size_min":"1","order_price_round":"0.1","mark_price":"100","enable_decimal":false}`))
		case "/api/v4/futures/usdt/tickers":
			_, _ = w.Write([]byte(`[{"contract":"ETH_USDT","last":"100","highest_bid":"99.9","lowest_ask":"100.1"}]`))
		case "/api/v4/futures/usdt/orders":
			switch r.Method {
			case http.MethodGet:
				_, _ = w.Write([]byte(`[]`))
			case http.MethodPost:
				if err := json.NewDecoder(r.Body).Decode(&orderBody); err != nil {
					t.Fatal(err)
				}
				_, _ = w.Write([]byte(`{"id":456,"contract":"ETH_USDT","size":-1,"left":-1,"price":"99.9","status":"open","text":"t-close","is_reduce_only":true,"is_close":false}`))
			default:
				t.Fatalf("unexpected order method: %s", r.Method)
			}
		case "/api/v4/futures/usdt/price_orders":
			if r.Method != http.MethodGet {
				t.Fatalf("unexpected Gate price order method: %s", r.Method)
			}
			_, _ = w.Write([]byte(`[]`))
		case "/api/v4/futures/usdt/my_trades":
			_, _ = w.Write([]byte(fmt.Sprintf(`[{"id":"entry-buy","contract":%q,"order_id":"entry-buy","size":"1000","create_time":"1784800000"},{"id":"entry-sell","contract":%q,"order_id":"entry-sell","size":"-1000","create_time":"1784800000"}]`, r.URL.Query().Get("contract"), r.URL.Query().Get("contract"))))
		default:
			t.Fatalf("unexpected Gate path %s", r.URL.Path)
		}
	}))
	defer gateServer.Close()

	srv := newTestServer(t)
	configureGatePositionCloseTest(t, srv, gateServer.URL)
	oldPoll, oldTimeout := positionClosePollInterval, positionCloseLimitTimeout
	positionClosePollInterval, positionCloseLimitTimeout = time.Hour, time.Hour
	t.Cleanup(func() {
		positionClosePollInterval, positionCloseLimitTimeout = oldPoll, oldTimeout
	})

	reqBody := []byte(`{"exchange":"gate","api_id":"gate-main","inst_id":"ETH_USDT","pos_side":"long","mode":"limit","ratio":0.5}`)
	req := httptest.NewRequest(http.MethodPost, "/tvbot/positions/close", bytes.NewReader(reqBody))
	req.SetBasicAuth("admin", "Admin123")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("Gate partial limit close status=%d body=%s", rr.Code, rr.Body.String())
	}
	if orderBody["size"] != "-1" || orderBody["price"] != "99.9" || orderBody["tif"] != "gtc" || orderBody["reduce_only"] != true {
		t.Fatalf("bad Gate partial limit close order: %#v", orderBody)
	}
	if _, ok := orderBody["close"]; ok {
		t.Fatalf("partial Gate close must not set close=true: %#v", orderBody)
	}
	var response positionCloseResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.OK || response.Status != "running" || response.Px != "99.9" || response.Sz != "1" {
		t.Fatalf("bad Gate partial limit close response: %#v", response)
	}
}
