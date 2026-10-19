package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotstorage"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

func TestTVBotTemplatesGenerateFreshTokenAndWebhookValidates(t *testing.T) {
	srv := newTestServer(t)
	reqBody := []byte(`{"target_exchange":"binance","api_id":"binance-main","price_source":"close"}`)
	makeReq := func() trading.TemplateResponse {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/tvbot/templates", bytes.NewReader(reqBody))
		req.Header.Set("X-Admin-Token", "admin")
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("template status=%d body=%s", rr.Code, rr.Body.String())
		}
		var resp trading.TemplateResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if resp.Token == "" || resp.TokenNonce == "" || !bytes.Contains([]byte(resp.JSON), []byte(`"token_nonce"`)) {
			t.Fatalf("template should include token and token_nonce: %#v", resp)
		}
		return resp
	}
	first := makeReq()
	second := makeReq()
	if first.Token == second.Token || first.TokenNonce == second.TokenNonce {
		t.Fatalf("repeated template generation should produce different token/nonces: first=%#v second=%#v", first, second)
	}

	signal := validSignal(t, srv)
	signal.TargetExchange = trading.ExchangeBinance
	signal.APIID = "binance-main"
	signal.TokenNonce = first.TokenNonce
	signal.Token = first.Token
	body, err := json.Marshal(signal)
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/tvorder", bytes.NewReader(body)))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("webhook status=%d body=%s", rr.Code, rr.Body.String())
	}
	orderResp := decodeTVOrderSignalResponse(t, rr.Body.Bytes())
	select {
	case got := <-srv.Executor.(fakeExecutor).calls:
		if got.TokenNonce != first.TokenNonce || got.TargetExchange != trading.ExchangeBinance || got.APIID != "binance-main" {
			t.Fatalf("webhook should keep nonce and target routing: %#v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("executor was not called")
	}
	waitOrderStatus(t, srv.Orders, orderResp.SignalID, storage.StatusSubmitted)
}
