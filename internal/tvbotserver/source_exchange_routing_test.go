package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotstorage"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

func TestTVOrderPreservesExplicitOKXTargetForBinanceSource(t *testing.T) {
	srv := newTestServer(t)
	signal := validSignal(t, srv)
	signal.Exchange = "BINANCE"
	signal.TargetExchange = trading.ExchangeOKX
	signal.APIID = "okx-moni"
	signal.Token = srv.Token.Generate(signal.CanonicalWebhookTokenPayload())
	body, err := json.Marshal(signal)
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/tvorder", bytes.NewReader(body)))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	resp := decodeTVOrderSignalResponse(t, rr.Body.Bytes())
	select {
	case got := <-srv.Executor.(fakeExecutor).calls:
		if got.Exchange != "BINANCE" || got.TargetExchange != trading.ExchangeOKX || got.APIID != "okx-moni" {
			t.Fatalf("explicit OKX target should win over BINANCE source: %#v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("executor was not called")
	}
	waitOrderStatus(t, srv.Orders, resp.SignalID, storage.StatusSubmitted)
	records := srv.Orders.List(10)
	if len(records) != 1 || records[0].SourceExchange != "BINANCE" || records[0].TargetExchange != trading.ExchangeOKX || records[0].APIID != "okx-moni" {
		t.Fatalf("order record should preserve explicit OKX target: %#v", records)
	}
}

func TestTVOrderRoutesMissingTargetExchangeBySource(t *testing.T) {
	srv := newTestServer(t)
	signal := validSignal(t, srv)
	signal.Exchange = "BINANCE"
	signal.TargetExchange = ""
	signal.APIID = "okx-moni"
	signal.Token = srv.Token.Generate(signal.CanonicalTokenPayload())
	body, err := json.Marshal(signal)
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/tvorder", bytes.NewReader(body)))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	resp := decodeTVOrderSignalResponse(t, rr.Body.Bytes())
	select {
	case got := <-srv.Executor.(fakeExecutor).calls:
		if got.Exchange != "BINANCE" || got.TargetExchange != trading.ExchangeBinance || got.APIID != "" {
			t.Fatalf("missing target_exchange should route by BINANCE source: %#v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("executor was not called")
	}
	waitOrderStatus(t, srv.Orders, resp.SignalID, storage.StatusSubmitted)
	records := srv.Orders.List(10)
	if len(records) != 1 || records[0].SourceExchange != "BINANCE" || records[0].TargetExchange != trading.ExchangeBinance || records[0].APIID != "" {
		t.Fatalf("order record should save source-derived Binance target: %#v", records)
	}
}

func TestTVOrderPreservesExplicitBinanceTargetForOKXSource(t *testing.T) {
	srv := newTestServer(t)
	signal := validSignal(t, srv)
	signal.Exchange = "OKX"
	signal.TargetExchange = trading.ExchangeBinance
	signal.APIID = "binance-main"
	signal.Token = srv.Token.Generate(signal.CanonicalWebhookTokenPayload())
	body, err := json.Marshal(signal)
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/tvorder", bytes.NewReader(body)))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	resp := decodeTVOrderSignalResponse(t, rr.Body.Bytes())
	select {
	case got := <-srv.Executor.(fakeExecutor).calls:
		if got.Exchange != "OKX" || got.TargetExchange != trading.ExchangeBinance || got.APIID != "binance-main" {
			t.Fatalf("explicit Binance target should win over OKX source: %#v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("executor was not called")
	}
	waitOrderStatus(t, srv.Orders, resp.SignalID, storage.StatusSubmitted)
}

func TestOrderRetryPreservesStoredTargetExchange(t *testing.T) {
	srv := newTestServer(t)
	installOKXRetryTicker(t, srv, "BTC-USDT-SWAP", "50010", "50030", "50020")
	signal := validSignal(t, srv)
	signal.Exchange = "BINANCE"
	signal.TargetExchange = trading.ExchangeOKX
	signal.APIID = "okx-moni"
	signal.Normalize()
	source, duplicate, err := srv.Orders.RecordAccepted(signal, "source-binance-to-okx", srv.now())
	if err != nil {
		t.Fatal(err)
	}
	if duplicate {
		t.Fatal("seed order should not be duplicate")
	}
	if err := srv.Orders.MarkFailed(source.SignalID, fmt.Errorf("old OKX route failed"), srv.now()); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/tvbot/orders/"+source.SignalID+"/retry", nil)
	req.SetBasicAuth("admin", "Admin123")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("retry status=%d body=%s", rr.Code, rr.Body.String())
	}
	var retryResp struct {
		SignalID string `json:"signal_id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &retryResp); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-srv.Executor.(fakeExecutor).calls:
		if got.Exchange != "BINANCE" || got.TargetExchange != trading.ExchangeOKX || got.APIID != "okx-moni" {
			t.Fatalf("retry should preserve stored target exchange: %#v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("retry order was not executed")
	}
	waitOrderStatus(t, srv.Orders, retryResp.SignalID, storage.StatusSubmitted)
	records := srv.Orders.List(10)
	if len(records) != 2 {
		t.Fatalf("orders len=%d records=%#v", len(records), records)
	}
	var retry storage.OrderRecord
	for _, rec := range records {
		if rec.SignalID != source.SignalID {
			retry = rec
			break
		}
	}
	if retry.TargetExchange != trading.ExchangeOKX || retry.APIID != "okx-moni" || retry.SourceExchange != "BINANCE" {
		t.Fatalf("retry record should preserve stored target: %#v", retry)
	}
}
