package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
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
