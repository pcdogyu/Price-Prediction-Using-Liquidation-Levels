package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	tvbotconfig "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotconfig"
	tvbotokx "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotokx"
	tvbottrading "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

func TestPositionOpenRatioAndDirection(t *testing.T) {
	for _, want := range []float64{0.1, 0.25, 0.5, 0.75} {
		if got, err := positionOpenRatio(want); err != nil || got != want {
			t.Fatalf("positionOpenRatio(%v)=%v,%v", want, got, err)
		}
	}
	for _, value := range []float64{0, 0.2, 1, -0.1} {
		if _, err := positionOpenRatio(value); err == nil {
			t.Fatalf("positionOpenRatio(%v) should reject unsupported ratio", value)
		}
	}

	if action, side, err := positionOpenAction(tvbotokx.Position{PosSide: "long", Pos: "2"}); err != nil || action != tvbottrading.ActionLong || side != "long" {
		t.Fatalf("long position action=%q side=%q err=%v", action, side, err)
	}
	if action, side, err := positionOpenAction(tvbotokx.Position{PosSide: "net", Pos: "-2"}); err != nil || action != tvbottrading.ActionShort || side != "short" {
		t.Fatalf("net short position action=%q side=%q err=%v", action, side, err)
	}
	if got, err := positionOpenNotional(tvbotokx.Position{Pos: "2", MarkPx: "100"}); err != nil || got != 200 {
		t.Fatalf("fallback notional=%v err=%v", got, err)
	}
}

type positionOpenCapture struct {
	signal tvbottrading.Signal
	cfg    tvbottrading.RuntimeConfig
}

type positionOpenExecutor struct {
	calls chan positionOpenCapture
}

func (e positionOpenExecutor) ExecuteSignal(ctx context.Context, signal tvbottrading.Signal, cfg tvbottrading.RuntimeConfig) (tvbottrading.OrderResult, error) {
	e.calls <- positionOpenCapture{signal: signal, cfg: cfg}
	return tvbottrading.OrderResult{InstID: signal.Coinpair, OrdID: "manual-open-1", ClOrdID: "manual-open-client"}, nil
}

func (e positionOpenExecutor) Check(ctx context.Context, cfg tvbottrading.RuntimeConfig) (map[string]any, error) {
	return map[string]any{"ok": true}, nil
}

func TestPositionOpenEndpointUsesLivePositionRatioAndForcesMarket(t *testing.T) {
	srv := newTestServer(t)
	configureTestOKX(t, srv.OKXCredentials, "manual")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v5/account/positions" {
			t.Fatalf("unexpected OKX path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instId":"BTC-USDT-SWAP","posSide":"long","pos":"10","avgPx":"99","markPx":"100","notionalUsd":"1000","lever":"5"}]}`))
	}))
	defer upstream.Close()
	cfg := srv.ConfigStore.Get()
	cfg.Trading.BaseURL = upstream.URL
	cfg.Trading.OrderType = string(tvbottrading.OrderTypeLimit)
	cfg.Trading.RiskType = string(tvbottrading.RiskTPSL)
	cfg.Trading.TakeProfitPct = 2
	cfg.Trading.StopLossPct = 1
	srv.ConfigStore = tvbotconfig.NewStore("", cfg)
	srv.OKXHTTPClient = upstream.Client()
	capture := make(chan positionOpenCapture, 1)
	srv.Executor = positionOpenExecutor{calls: capture}

	req := httptest.NewRequest(http.MethodPost, "/tvbot/positions/open", bytes.NewReader([]byte(`{"exchange":"okx","api_id":"manual","inst_id":"BTC-USDT-SWAP","pos_side":"long","ratio":0.25}`)))
	req.SetBasicAuth("admin", "Admin123")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("open status=%d body=%s", rr.Code, rr.Body.String())
	}
	var response positionOpenResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.OK || response.Status != "accepted" || response.OpenNotional != "250" || response.PositionSide != "long" {
		t.Fatalf("unexpected open response: %#v", response)
	}

	select {
	case captured := <-capture:
		if captured.signal.Action != tvbottrading.ActionLong || captured.signal.PositionEffect != tvbottrading.PositionEffectOpen || captured.signal.PositionSide != "long" || captured.signal.OrderIntent != "manual_position_open" {
			t.Fatalf("unexpected manual signal: %#v", captured.signal)
		}
		if captured.signal.Amount.Value != 250 || captured.signal.Price.Value != 100 {
			t.Fatalf("manual signal used client or wrong sizing: amount=%v price=%v", captured.signal.Amount.Value, captured.signal.Price.Value)
		}
		if captured.cfg.OrderSettings().OrderType != tvbottrading.OrderTypeMarket {
			t.Fatalf("manual open order type=%q, want market", captured.cfg.OrderSettings().OrderType)
		}
		if captured.cfg.OrderSettings().Risk.Type != tvbottrading.RiskTPSL {
			t.Fatalf("manual open risk=%q, want tp_sl", captured.cfg.OrderSettings().Risk.Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("manual open was not submitted to executor")
	}
}
