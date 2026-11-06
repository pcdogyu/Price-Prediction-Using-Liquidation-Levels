package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	tvbotbinance "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotbinance"
	tvbotconfig "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotconfig"
	tvbotgate "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotgate"
	tvbotokx "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotokx"
	tvbottrading "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

func TestPrimaryExchangeResolutionUsesExplicitTargetOrUniqueConfiguredExchange(t *testing.T) {
	srv := newTestServer(t)
	if _, err := srv.BinanceCredentials.UpdateAccount(tvbotbinance.CredentialAccountUpdate{
		ID: "binance-main", Active: true, Credentials: tvbotbinance.Credentials{APIKey: "binance-key", SecretKey: "binance-secret"},
	}); err != nil {
		t.Fatal(err)
	}
	if got := srv.primaryExchangeForConfig(srv.ConfigStore.Get()); got != tvbottrading.ExchangeBinance {
		t.Fatalf("unique configured exchange=%q, want Binance", got)
	}
	if _, err := srv.OKXCredentials.UpdateAccount(tvbotokx.CredentialAccountUpdate{
		ID: "okx-main", Active: true, Credentials: tvbotokx.Credentials{APIKey: "okx-key", SecretKey: "okx-secret", Passphrase: "okx-pass"},
	}); err != nil {
		t.Fatal(err)
	}
	if got := srv.primaryExchangeForConfig(srv.ConfigStore.Get()); got != "" {
		t.Fatalf("multiple configured exchanges without target=%q, want read-only/no primary", got)
	}
	cfg := srv.ConfigStore.Get()
	cfg.Trading.TargetExchange = tvbottrading.ExchangeGate
	if got := srv.primaryExchangeForConfig(cfg); got != tvbottrading.ExchangeGate {
		t.Fatalf("explicit target=%q, want Gate", got)
	}
}

func TestAPIKeySelectionClearsOtherExchangeActivesAcrossEnvironments(t *testing.T) {
	srv := newTestServer(t)
	gateLive, err := tvbotgate.NewCredentialStore(filepath.Join(t.TempDir(), "gate-live.json"), tvbotgate.Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	binanceLive, err := tvbotbinance.NewCredentialStore(filepath.Join(t.TempDir(), "binance-live.json"), tvbotbinance.Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	srv.GateCredentials = gateLive
	srv.GateLiveCredentials = gateLive
	srv.BinanceLiveCredentials = binanceLive
	for _, update := range []tvbotbinance.CredentialAccountUpdate{
		{ID: "binance-demo", Active: true, Credentials: tvbotbinance.Credentials{APIKey: "binance-demo-key", SecretKey: "binance-demo-secret"}},
		{ID: "binance-live", Active: true, Credentials: tvbotbinance.Credentials{APIKey: "binance-live-key", SecretKey: "binance-live-secret"}},
	} {
		store := srv.BinanceCredentials
		if update.ID == "binance-live" {
			store = binanceLive
		}
		if _, err := store.UpdateAccount(update); err != nil {
			t.Fatal(err)
		}
	}
	for _, update := range []tvbotgate.CredentialAccountUpdate{
		{ID: "gate-demo", Active: true, Credentials: tvbotgate.Credentials{APIKey: "gate-demo-key", SecretKey: "gate-demo-secret"}},
		{ID: "gate-live", Active: true, Credentials: tvbotgate.Credentials{APIKey: "gate-live-key", SecretKey: "gate-live-secret"}},
	} {
		if _, err := gateLive.UpdateAccount(update); err != nil {
			t.Fatal(err)
		}
	}
	if err := srv.syncAPIKeyTradeTarget(tvbottrading.ExchangeGate, tvbotconfig.EnvDemo, "gate-demo", true); err != nil {
		t.Fatal(err)
	}
	if srv.BinanceCredentials.Status().ActiveID != "" || binanceLive.Status().ActiveID != "" {
		t.Fatalf("other exchange active IDs were not cleared: demo=%#v live=%#v", srv.BinanceCredentials.Status(), binanceLive.Status())
	}
	if _, _, err := srv.BinanceCredentials.BinanceCredentials("binance-demo"); err != nil {
		t.Fatalf("clearing active marker must preserve Binance demo credentials: %v", err)
	}
	if _, _, err := binanceLive.BinanceCredentials("binance-live"); err != nil {
		t.Fatalf("clearing active marker must preserve Binance live credentials: %v", err)
	}
	if got := srv.ConfigStore.Get().Trading.TargetExchange; got != tvbottrading.ExchangeGate {
		t.Fatalf("target exchange=%q, want Gate", got)
	}
	if err := srv.syncAPIKeyTradeTarget(tvbottrading.ExchangeGate, tvbotconfig.EnvLive, "gate-live", false); err != nil {
		t.Fatal(err)
	}
	if srv.ConfigStore.Get().Trading.TargetExchange != "" || gateLive.Status().ActiveID != "" {
		t.Fatalf("unchecking primary should clear global target and active markers: cfg=%#v gate=%#v", srv.ConfigStore.Get().Trading, gateLive.Status())
	}
	if _, _, err := gateLive.GateCredentials("gate-demo"); err != nil {
		t.Fatalf("unchecking primary must preserve Gate credentials: %v", err)
	}
}

func TestNonPrimaryPositionsAndBalanceRowsAreStoppedBeforeExchangeCalls(t *testing.T) {
	srv := newTestServer(t)
	if _, err := srv.OKXCredentials.UpdateAccount(tvbotokx.CredentialAccountUpdate{
		ID: "okx-main", Active: true, Credentials: tvbotokx.Credentials{APIKey: "okx-key", SecretKey: "okx-secret", Passphrase: "okx-pass"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.BinanceCredentials.UpdateAccount(tvbotbinance.CredentialAccountUpdate{
		ID: "binance-main", Active: true, Credentials: tvbotbinance.Credentials{APIKey: "binance-key", SecretKey: "binance-secret"},
	}); err != nil {
		t.Fatal(err)
	}
	cfg := srv.ConfigStore.Get()
	cfg.Trading.TargetExchange = tvbottrading.ExchangeOKX
	primaryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(primaryServer.Close)
	cfg.Trading.BaseURL = primaryServer.URL
	srv.ConfigStore = tvbotconfig.NewStore("", cfg)
	srv.OKXHTTPClient = primaryServer.Client()
	positionsReq := httptest.NewRequest(http.MethodGet, "/tvbot/positions?exchange=binance", nil)
	positionsReq.SetBasicAuth("admin", "Admin123")
	positionsRR := httptest.NewRecorder()
	srv.ServeHTTP(positionsRR, positionsReq)
	if positionsRR.Code != http.StatusConflict || !bytes.Contains(positionsRR.Body.Bytes(), []byte(`"non_primary_exchange"`)) {
		t.Fatalf("non-primary positions request was not rejected: code=%d body=%s", positionsRR.Code, positionsRR.Body.String())
	}
	ordersReq := httptest.NewRequest(http.MethodGet, "/tvbot/orders?exchange=binance", nil)
	ordersReq.SetBasicAuth("admin", "Admin123")
	ordersRR := httptest.NewRecorder()
	srv.ServeHTTP(ordersRR, ordersReq)
	if ordersRR.Code != http.StatusConflict || !bytes.Contains(ordersRR.Body.Bytes(), []byte(`"non_primary_exchange"`)) {
		t.Fatalf("non-primary orders request was not rejected: code=%d body=%s", ordersRR.Code, ordersRR.Body.String())
	}
	balanceReq := httptest.NewRequest(http.MethodGet, "/tvbot/balances/overview", nil)
	balanceReq.SetBasicAuth("admin", "Admin123")
	balanceRR := httptest.NewRecorder()
	srv.ServeHTTP(balanceRR, balanceReq)
	if balanceRR.Code != http.StatusOK {
		t.Fatalf("balance overview status=%d body=%s", balanceRR.Code, balanceRR.Body.String())
	}
	var overview balanceOverviewResponse
	if err := json.Unmarshal(balanceRR.Body.Bytes(), &overview); err != nil {
		t.Fatal(err)
	}
	for _, row := range overview.Exchanges {
		if row.Exchange == tvbottrading.ExchangeOKX {
			continue
		}
		if row.Status != "non_trading" || row.Configured || row.Balance.TotalEq != "" {
			t.Fatalf("non-primary balance row should be disabled and empty: %#v", row)
		}
	}
}
