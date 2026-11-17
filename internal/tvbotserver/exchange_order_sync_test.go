package server

import (
	"bytes"
	"context"
	"encoding/json"
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

func TestExchangeOrderInitialWindowForExchange(t *testing.T) {
	if got := exchangeOrderInitialWindowFor(trading.ExchangeBinance); got != 7*24*time.Hour {
		t.Fatalf("Binance initial history window=%s, want 7 days", got)
	}
	if got := exchangeOrderInitialWindowFor(trading.ExchangeOKX); got != 30*24*time.Hour {
		t.Fatalf("non-Binance initial history window=%s, want existing 30 days", got)
	}
}

func TestFetchBinanceExchangeOrdersStopsAtPerSymbolDuplicateAndContinuesOtherSymbols(t *testing.T) {
	srv := newTestServer(t)
	if _, err := srv.BinanceCredentials.SaveSingleAccount(binance.CredentialAccountUpdate{
		ID: "binance-main", Name: "test", Active: true,
		Credentials: binance.Credentials{APIKey: "fake-key", SecretKey: "fake-secret"},
	}); err != nil {
		t.Fatal(err)
	}
	to := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	from := to.Add(-exchangeOrderBinanceInitialWindow)
	type remoteOrder struct {
		Symbol  string `json:"symbol"`
		OrderID int64  `json:"orderId"`
		Status  string `json:"status"`
		Time    int64  `json:"time"`
	}
	base := to.Add(-24 * time.Hour)
	btcRows := make([]remoteOrder, exchangeOrderPageLimit)
	for i := range btcRows {
		btcRows[i] = remoteOrder{
			Symbol: "BTCUSDT", OrderID: int64(i + 1), Status: "FILLED",
			Time: base.Add(time.Duration(i) * time.Second).UnixMilli(),
		}
	}
	btcPayload, err := json.Marshal(btcRows)
	if err != nil {
		t.Fatal(err)
	}
	ethPayload, err := json.Marshal([]remoteOrder{{
		Symbol: "ETHUSDT", OrderID: 20, Status: "FILLED", Time: to.Add(-10 * time.Minute).UnixMilli(),
	}})
	if err != nil {
		t.Fatal(err)
	}
	localBoundaryTime := base.Add(994 * time.Second)
	if _, err := srv.Orders.UpsertExchangeOrders([]storage.ExchangeOrder{
		{Exchange: "binance", APIID: "binance-main", TradeEnv: "demo", RemoteOrderID: "995", InstID: "BTCUSDT", Status: "FILLED", CreatedAt: localBoundaryTime},
		{Exchange: "binance", APIID: "binance-main", TradeEnv: "demo", RemoteOrderID: "known-eth", InstID: "ETHUSDT", Status: "FILLED", CreatedAt: to.Add(-time.Hour)},
	}, to); err != nil {
		t.Fatal(err)
	}
	requested := map[string]int{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/fapi/v1/exchangeInfo":
			_, _ = w.Write([]byte(`{"symbols":[
				{"symbol":"BTCUSDT","status":"TRADING","quoteAsset":"USDT"},
				{"symbol":"ETHUSDT","status":"TRADING","quoteAsset":"USDT"},
				{"symbol":"SOLUSDT","status":"TRADING","quoteAsset":"USDT"}
			]}`))
		case "/fapi/v1/allOrders":
			query := r.URL.Query()
			if query.Get("startTime") != "" || query.Get("orderId") != "" {
				t.Fatalf("recent-page request should not use forward-only pagination: %s", r.URL.RawQuery)
			}
			endMS, err := strconv.ParseInt(query.Get("endTime"), 10, 64)
			if err != nil || time.UnixMilli(endMS).After(to) {
				t.Fatalf("invalid Binance endTime: %q", query.Get("endTime"))
			}
			symbol := query.Get("symbol")
			requested[symbol]++
			switch symbol {
			case "BTCUSDT":
				_, _ = w.Write(btcPayload)
			case "ETHUSDT":
				_, _ = w.Write(ethPayload)
			default:
				t.Fatalf("unexpected symbol request: %s", symbol)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	cfg := srv.ConfigStore.Get()
	cfg.Trading.BinanceDemoBaseURL = ts.URL
	srv.ConfigStore = config.NewStore("", cfg)
	srv.BinanceHTTPClient = ts.Client()
	target := exchangeOrderTarget{Exchange: trading.ExchangeBinance, APIID: "binance-main", TradeEnv: trading.TradeEnvDemo, Config: cfg}
	orders, cursor, err := srv.fetchBinanceExchangeOrders(context.Background(), target, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if requested["BTCUSDT"] != 1 || requested["ETHUSDT"] != 1 || requested["SOLUSDT"] != 0 {
		t.Fatalf("expected one request for each locally recorded symbol only, got %#v", requested)
	}
	if len(orders) != 7 {
		t.Fatalf("expected six BTC orders through the duplicate boundary and one ETH order; got %d", len(orders))
	}
	btcIDs := make(map[string]bool)
	for _, order := range orders {
		if order.InstID == "BTCUSDT" {
			btcIDs[order.RemoteOrderID] = true
		}
	}
	if !btcIDs["1000"] || !btcIDs["995"] || btcIDs["994"] || btcIDs["1"] {
		t.Fatalf("Binance did not stop at the known BTC boundary: %#v", btcIDs)
	}
	if cursor.BinanceMaxID != 1000 {
		t.Fatalf("unexpected maximum Binance order ID: %#v", cursor)
	}
}

func TestFetchBinanceExchangeOrdersMovesPageEndBackwardUntilDuplicate(t *testing.T) {
	srv := newTestServer(t)
	if _, err := srv.BinanceCredentials.SaveSingleAccount(binance.CredentialAccountUpdate{
		ID: "binance-main", Name: "test", Active: true,
		Credentials: binance.Credentials{APIKey: "fake-key", SecretKey: "fake-secret"},
	}); err != nil {
		t.Fatal(err)
	}
	to := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	from := to.Add(-exchangeOrderBinanceInitialWindow)
	oldestFirstPage := to.Add(-time.Hour)
	localBoundaryTime := to.Add(-2 * time.Hour)
	if _, err := srv.Orders.UpsertExchangeOrders([]storage.ExchangeOrder{{
		Exchange: "binance", APIID: "binance-main", TradeEnv: "demo", RemoteOrderID: "42",
		InstID: "BTCUSDT", Status: "FILLED", CreatedAt: localBoundaryTime,
	}}, to); err != nil {
		t.Fatal(err)
	}
	type remoteOrder struct {
		Symbol  string `json:"symbol"`
		OrderID int64  `json:"orderId"`
		Status  string `json:"status"`
		Time    int64  `json:"time"`
	}
	page := make([]remoteOrder, exchangeOrderPageLimit)
	for i := range page {
		page[i] = remoteOrder{
			Symbol: "BTCUSDT", OrderID: int64(1001 + i), Status: "FILLED",
			Time: oldestFirstPage.Add(time.Duration(i) * time.Second).UnixMilli(),
		}
	}
	pagePayload, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	boundaryPayload, err := json.Marshal([]remoteOrder{{
		Symbol: "BTCUSDT", OrderID: 42, Status: "CANCELED", Time: localBoundaryTime.UnixMilli(),
	}})
	if err != nil {
		t.Fatal(err)
	}
	var endTimes []time.Time
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/fapi/v1/exchangeInfo":
			_, _ = w.Write([]byte(`{"symbols":[{"symbol":"BTCUSDT","status":"TRADING","quoteAsset":"USDT"}]}`))
		case "/fapi/v1/allOrders":
			endMS, err := strconv.ParseInt(r.URL.Query().Get("endTime"), 10, 64)
			if err != nil {
				t.Fatalf("invalid Binance endTime: %q", r.URL.Query().Get("endTime"))
			}
			endTimes = append(endTimes, time.UnixMilli(endMS).UTC())
			if len(endTimes) == 1 {
				_, _ = w.Write(pagePayload)
				return
			}
			_, _ = w.Write(boundaryPayload)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	cfg := srv.ConfigStore.Get()
	cfg.Trading.BinanceDemoBaseURL = ts.URL
	srv.ConfigStore = config.NewStore("", cfg)
	srv.BinanceHTTPClient = ts.Client()
	target := exchangeOrderTarget{Exchange: trading.ExchangeBinance, APIID: "binance-main", TradeEnv: trading.TradeEnvDemo, Config: cfg}
	orders, _, err := srv.fetchBinanceExchangeOrders(context.Background(), target, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(endTimes) != 2 || !endTimes[0].Equal(to) || !endTimes[1].Equal(oldestFirstPage.Add(-time.Millisecond)) {
		t.Fatalf("page endTime did not move behind the previous page: %#v", endTimes)
	}
	if len(orders) != exchangeOrderPageLimit+1 || orders[len(orders)-1].RemoteOrderID != "42" {
		t.Fatalf("expected the recent page and one duplicate boundary order, got count=%d last=%#v", len(orders), orders[len(orders)-1])
	}
}

func TestFetchBinanceExchangeOrdersDiscoversRecentLocalSymbols(t *testing.T) {
	srv := newTestServer(t)
	if _, err := srv.BinanceCredentials.SaveSingleAccount(binance.CredentialAccountUpdate{
		ID: "binance-main", Name: "test", Active: true,
		Credentials: binance.Credentials{APIKey: "fake-key", SecretKey: "fake-secret"},
	}); err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 11, 9, 14, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)
	if _, err := srv.Orders.UpsertExchangeOrders([]storage.ExchangeOrder{{
		Exchange: "binance", APIID: "binance-main", TradeEnv: "demo", RemoteOrderID: "known-1",
		InstID: "USDCUSDT", Status: "FILLED", CreatedAt: from,
	}}, from); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.Orders.UpsertBinanceFills([]storage.BinanceFill{{
		APIID: "binance-main", Symbol: "ADAUSDT", TradeID: "trade-1", OrderID: "order-1", FillTime: from.Add(30 * time.Minute).UnixMilli(),
	}}, to); err != nil {
		t.Fatal(err)
	}
	adaPayload, err := json.Marshal([]struct {
		Symbol  string `json:"symbol"`
		OrderID int64  `json:"orderId"`
		Status  string `json:"status"`
		Time    int64  `json:"time"`
	}{{Symbol: "ADAUSDT", OrderID: 42, Status: "FILLED", Time: to.Add(-2 * 24 * time.Hour).UnixMilli()}})
	if err != nil {
		t.Fatal(err)
	}

	var endTimes = map[string][]time.Time{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/fapi/v1/exchangeInfo":
			_, _ = w.Write([]byte(`{"symbols":[{"symbol":"USDCUSDT","status":"TRADING","quoteAsset":"USDT"},{"symbol":"ADAUSDT","status":"TRADING","quoteAsset":"USDT"}]}`))
		case "/fapi/v1/allOrders":
			endMS, err := strconv.ParseInt(r.URL.Query().Get("endTime"), 10, 64)
			if err != nil {
				t.Fatalf("invalid Binance endTime: %q", r.URL.Query().Get("endTime"))
			}
			symbol := r.URL.Query().Get("symbol")
			endTimes[symbol] = append(endTimes[symbol], time.UnixMilli(endMS).UTC())
			if symbol == "ADAUSDT" {
				_, _ = w.Write(adaPayload)
				return
			}
			_, _ = w.Write([]byte(`[]`))
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
	orders, _, err := srv.fetchBinanceExchangeOrders(context.Background(), target, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if got := endTimes["USDCUSDT"]; len(got) == 0 || !got[0].Equal(to) {
		t.Fatalf("known symbol should query the newest page first, got %#v", got)
	}
	if got := endTimes["ADAUSDT"]; len(got) == 0 || !got[0].Equal(to) {
		t.Fatalf("recent fill symbol should query the newest page first, got %#v", got)
	}
	foundADA := false
	for _, order := range orders {
		if order.InstID == "ADAUSDT" && order.RemoteOrderID == "42" {
			foundADA = true
		}
	}
	if !foundADA {
		t.Fatalf("new local fill symbol was not backfilled from the recent history window: %#v", orders)
	}
}

func TestFetchBinanceExchangeOrdersDoesNotSweepUnrecordedUSDCQuoteSymbols(t *testing.T) {
	srv := newTestServer(t)
	if _, err := srv.BinanceCredentials.SaveSingleAccount(binance.CredentialAccountUpdate{
		ID: "binance-main", Name: "test", Active: true,
		Credentials: binance.Credentials{APIKey: "fake-key", SecretKey: "fake-secret"},
	}); err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 11, 9, 14, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)
	if _, err := srv.Orders.UpsertExchangeOrders([]storage.ExchangeOrder{{
		Exchange: "binance", APIID: "binance-main", TradeEnv: "demo", RemoteOrderID: "known-ethusdt",
		InstID: "ETHUSDT", Status: "FILLED", CreatedAt: from,
	}}, from); err != nil {
		t.Fatal(err)
	}
	knownSymbolPayload, err := json.Marshal([]struct {
		Symbol  string `json:"symbol"`
		OrderID int64  `json:"orderId"`
		Status  string `json:"status"`
		Time    int64  `json:"time"`
	}{{Symbol: "ETHUSDT", OrderID: 99, Status: "FILLED", Time: to.Add(-30 * time.Minute).UnixMilli()}})
	if err != nil {
		t.Fatal(err)
	}
	requested := map[string]int{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/fapi/v1/exchangeInfo":
			_, _ = w.Write([]byte(`{"symbols":[
				{"symbol":"ETHUSDC","status":"TRADING","quoteAsset":"USDC"},
				{"symbol":"ETHUSDT","status":"TRADING","quoteAsset":"USDT"},
				{"symbol":"ETHUSD","status":"TRADING","quoteAsset":"USD"},
				{"symbol":"OLDUSDC","status":"BREAK","quoteAsset":"USDC"}
			]}`))
		case "/fapi/v1/allOrders":
			symbol := r.URL.Query().Get("symbol")
			requested[symbol]++
			_, _ = w.Write(knownSymbolPayload)
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
	orders, _, err := srv.fetchBinanceExchangeOrders(context.Background(), target, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if requested["ETHUSDT"] != 1 {
		t.Fatalf("locally recorded Binance symbol was not queried: %#v", requested)
	}
	if requested["ETHUSDC"] != 0 || requested["ETHUSD"] != 0 || requested["OLDUSDC"] != 0 {
		t.Fatalf("unrecorded, unsupported, or inactive symbols were queried: %#v", requested)
	}
	if len(orders) != 1 || orders[0].InstID != "ETHUSDT" || orders[0].RemoteOrderID != "99" {
		t.Fatalf("unexpected locally scoped Binance order history: %#v", orders)
	}
}

func TestFetchBinanceExchangeOrdersSkipsRemoteDiscoveryWithoutLocalSymbols(t *testing.T) {
	srv := newTestServer(t)
	if _, err := srv.BinanceCredentials.SaveSingleAccount(binance.CredentialAccountUpdate{
		ID: "binance-main", Name: "test", Active: true,
		Credentials: binance.Credentials{APIKey: "fake-key", SecretKey: "fake-secret"},
	}); err != nil {
		t.Fatal(err)
	}
	remoteCalls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		remoteCalls++
		t.Fatalf("cold-start sync should not scan Binance symbols, got %s", r.URL.Path)
	}))
	defer ts.Close()
	cfg := srv.ConfigStore.Get()
	cfg.Trading.BinanceDemoBaseURL = ts.URL
	srv.ConfigStore = config.NewStore("", cfg)
	srv.BinanceHTTPClient = ts.Client()
	target := exchangeOrderTarget{Exchange: trading.ExchangeBinance, APIID: "binance-main", TradeEnv: trading.TradeEnvDemo, Config: cfg}
	from := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	if orders, _, err := srv.fetchBinanceExchangeOrders(context.Background(), target, from, from.Add(time.Hour)); err != nil || len(orders) != 0 {
		t.Fatalf("empty local symbol set should yield no remote orders, got orders=%#v err=%v", orders, err)
	}
	if remoteCalls != 0 {
		t.Fatalf("cold-start sync made %d remote calls", remoteCalls)
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
		{name: "successful sync remains fresh after old five minute cadence", ok: true, state: storage.ExchangeOrderSyncState{Status: "synced", LastSuccessAt: now.Add(-5 * time.Minute)}, want: false},
		{name: "stale success", ok: true, state: storage.ExchangeOrderSyncState{Status: "synced", LastSuccessAt: now.Add(-exchangeOrderSyncInterval)}, want: true},
		{name: "recent failure", ok: true, state: storage.ExchangeOrderSyncState{Status: "failed", LastAttemptedAt: now.Add(-time.Minute)}, want: false},
		{name: "recent failure after fresh success", ok: true, state: storage.ExchangeOrderSyncState{Status: "failed", LastSuccessAt: now.Add(-exchangeOrderSyncInterval), LastAttemptedAt: now.Add(-time.Minute)}, want: false},
		{name: "failure retries after five minutes", ok: true, state: storage.ExchangeOrderSyncState{Status: "failed", LastAttemptedAt: now.Add(-exchangeOrderSyncRetryInterval)}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := exchangeOrderSyncStateDue(tt.state, tt.ok, now); got != tt.want {
				t.Fatalf("exchangeOrderSyncStateDue()=%v, want %v", got, tt.want)
			}
		})
	}
}
