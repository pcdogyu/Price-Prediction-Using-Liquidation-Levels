package server

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	binance "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotbinance"
	config "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotconfig"
	storage "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotstorage"
	trading "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

func TestExchangeOrdersEndpointReadsTargetTenantDataForUser(t *testing.T) {
	srv := newTestServer(t)
	if _, err := srv.BinanceCredentials.SaveSingleAccount(binance.CredentialAccountUpdate{
		ID: "binance-main", Name: "test", Active: true,
		Credentials: binance.Credentials{APIKey: "fake-key", SecretKey: "fake-secret"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.ConfigStore.UpdateSettings(func(cfg *config.Config) error {
		cfg.Trading.TargetExchange = trading.ExchangeBinance
		cfg.Trading.TargetAPIID = "binance-main"
		cfg.Trading.TargetTradeEnv = trading.TradeEnvLive
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	created := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	if _, err := srv.Orders.UpsertExchangeOrders([]storage.ExchangeOrder{{
		Exchange: "binance", APIID: "binance-main", TradeEnv: "live", RemoteOrderID: "remote-42",
		InstID: "ETHUSDT", Status: "FILLED", CreatedAt: created,
	}}, created); err != nil {
		t.Fatal(err)
	}
	adminChecks := 0
	srv.RequireUnifiedUser = func(http.ResponseWriter, *http.Request) bool { return true }
	srv.RequireUnifiedAdmin = func(http.ResponseWriter, *http.Request) bool { adminChecks++; return false }
	srv.CurrentUser = func(*http.Request) (string, bool) { return "test-user", true }

	req := httptest.NewRequest(http.MethodGet, "/tvbot/exchange-orders?status=filled", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || !bytes.Contains(rr.Body.Bytes(), []byte("remote-42")) || !bytes.Contains(rr.Body.Bytes(), []byte("ETHUSDT")) {
		t.Fatalf("exchange order response status=%d body=%s", rr.Code, rr.Body.String())
	}
	if adminChecks != 0 {
		t.Fatalf("user exchange order read unexpectedly required admin auth: %d", adminChecks)
	}
}

func TestExchangeOrderSyncEndpointsAreUserAccessibleOnlyForDeclaredMethods(t *testing.T) {
	request := func(method, path string) *http.Request { return httptest.NewRequest(method, path, nil) }
	if requiresAdminEndpoint("/exchange-orders", request(http.MethodGet, "/exchange-orders")) || requiresAdminEndpoint("/exchange-orders/sync", request(http.MethodPost, "/exchange-orders/sync")) {
		t.Fatal("exchange order read and sync should be user-scoped")
	}
	if requiresAdminEndpoint("/exchange-orders", request(http.MethodPost, "/exchange-orders")) || requiresAdminEndpoint("/exchange-orders/sync", request(http.MethodGet, "/exchange-orders/sync")) {
		t.Fatal("exchange order route method checks should happen in the handlers")
	}
	if requiresAdminEndpoint("/orders", request(http.MethodPost, "/orders")) == false {
		t.Fatal("existing order mutations should remain admin-scoped")
	}
}

func TestFetchBinanceExchangeOrdersSplitsWindowsAtBinanceLimit(t *testing.T) {
	srv := newTestServer(t)
	if _, err := srv.BinanceCredentials.SaveSingleAccount(binance.CredentialAccountUpdate{
		ID: "binance-main", Name: "test", Active: true,
		Credentials: binance.Credentials{APIKey: "fake-key", SecretKey: "fake-secret"},
	}); err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(30 * 24 * time.Hour)
	type orderWindow struct {
		start time.Time
		end   time.Time
	}
	var windows []orderWindow
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/fapi/v1/exchangeInfo":
			_, _ = w.Write([]byte(`{"symbols":[{"symbol":"BTCUSDT","status":"TRADING","quoteAsset":"USDT"}]}`))
		case "/fapi/v1/allOrders":
			startMS, err := strconv.ParseInt(r.URL.Query().Get("startTime"), 10, 64)
			if err != nil {
				t.Fatalf("invalid Binance startTime: %q", r.URL.Query().Get("startTime"))
			}
			endMS, err := strconv.ParseInt(r.URL.Query().Get("endTime"), 10, 64)
			if err != nil {
				t.Fatalf("invalid Binance endTime: %q", r.URL.Query().Get("endTime"))
			}
			windows = append(windows, orderWindow{start: time.UnixMilli(startMS).UTC(), end: time.UnixMilli(endMS).UTC()})
			if endMS-startMS >= int64((7 * 24 * time.Hour).Milliseconds()) {
				t.Fatalf("Binance time window is too large: start=%d end=%d", startMS, endMS)
			}
			_, _ = w.Write([]byte(`[{"symbol":"BTCUSDT","orderId":1,"status":"FILLED","time":1790812800000}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	cfg := srv.ConfigStore.Get()
	cfg.Trading.BinanceDemoBaseURL = ts.URL
	srv.ConfigStore = config.NewStore("", cfg)
	srv.BinanceHTTPClient = ts.Client()
	target := exchangeOrderTarget{
		Exchange: trading.ExchangeBinance,
		APIID:    "binance-main",
		TradeEnv: trading.TradeEnvDemo,
		Config:   cfg,
	}
	orders, cursor, err := srv.fetchBinanceExchangeOrders(context.Background(), target, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(windows) != 5 {
		t.Fatalf("Binance 30-day sync should use 5 windows, got %d: %#v", len(windows), windows)
	}
	if !windows[0].start.Equal(from) || !windows[len(windows)-1].end.Equal(to) {
		t.Fatalf("Binance windows do not cover the requested range: %#v", windows)
	}
	for i, window := range windows {
		if window.end.Sub(window.start) >= 7*24*time.Hour {
			t.Fatalf("window %d exceeds Binance limit: %#v", i, window)
		}
		if i > 0 && window.start.Sub(windows[i-1].end) != time.Millisecond {
			t.Fatalf("Binance windows are not contiguous at %d: previous=%#v current=%#v", i, windows[i-1], window)
		}
	}
	if len(orders) != len(windows) || cursor.BinanceMaxID != 1 {
		t.Fatalf("unexpected Binance sync result: orders=%d cursor=%#v", len(orders), cursor)
	}
}

func TestExchangeOrderSyncStateDueHonorsFreshAndInFlightStates(t *testing.T) {
	now := time.Date(2026, 11, 4, 15, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		state storage.ExchangeOrderSyncState
		ok    bool
		want  bool
	}{
		{name: "missing state", want: true},
		{name: "in flight", ok: true, state: storage.ExchangeOrderSyncState{Status: "syncing"}, want: true},
		{name: "fresh success", ok: true, state: storage.ExchangeOrderSyncState{Status: "synced", LastSuccessAt: now.Add(-time.Minute)}, want: false},
		{name: "stale success", ok: true, state: storage.ExchangeOrderSyncState{Status: "synced", LastSuccessAt: now.Add(-exchangeOrderSyncInterval)}, want: true},
		{name: "recent failure", ok: true, state: storage.ExchangeOrderSyncState{Status: "failed", LastAttemptedAt: now.Add(-time.Minute)}, want: false},
		{name: "recent failure after fresh success", ok: true, state: storage.ExchangeOrderSyncState{Status: "failed", LastSuccessAt: now.Add(-exchangeOrderSyncInterval), LastAttemptedAt: now.Add(-time.Minute)}, want: false},
		{name: "stale failure", ok: true, state: storage.ExchangeOrderSyncState{Status: "failed", LastAttemptedAt: now.Add(-exchangeOrderSyncInterval)}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := exchangeOrderSyncStateDue(tt.state, tt.ok, now); got != tt.want {
				t.Fatalf("exchangeOrderSyncStateDue()=%v, want %v", got, tt.want)
			}
		})
	}
}
