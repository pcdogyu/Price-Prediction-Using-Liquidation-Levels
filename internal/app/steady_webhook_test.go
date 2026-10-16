package app

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/config"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/notify"
)

func TestBuildSteadyWebhookPayloadMatchesTVOrderContract(t *testing.T) {
	signal := domain.TradeSignal{
		Symbol: "BTCUSDT", Kind: domain.SignalSteady, Side: "long",
		CreatedAt:      time.Date(2026, 10, 16, 3, 4, 5, 123456789, time.FixedZone("CST", 8*60*60)),
		ReferencePrice: 67123.45, WindowMinutes: 3,
	}
	cfg := config.Config{
		SteadyWebhookAPIID: "okx_bot", SteadyWebhookTargetExchange: "okx",
		SteadyWebhookTradeEnv: "demo", SteadyWebhookExchange: "binance",
		SteadyWebhookInterval: "3m", SteadyWebhookTokenNonce: "nonce-value",
		SteadyWebhookToken: "token-value",
	}
	raw, err := buildSteadyWebhookPayload(signal, cfg)
	if err != nil {
		t.Fatalf("build payload: %v", err)
	}
	var got map[string]string
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("payload is not JSON: %v", err)
	}
	want := map[string]string{
		"sent_at": "2026-10-15T19:04:05.123456789Z", "api_id": "okx_bot",
		"target_exchange": "okx", "trade_env": "demo", "action": "buy",
		"ticker": "BTCUSDT", "coinpair": "BTCUSDT", "price": "67123.45",
		"exchange": "binance", "interval": "3m", "condition": "steady_liquidation_confirmation",
		"text": "entry_long", "order_intent": "entry_long", "source": "tradingview",
		"token_nonce": "nonce-value", "token": "token-value",
	}
	for key, wantValue := range want {
		if got[key] != wantValue {
			t.Fatalf("payload[%q] = %q, want %q; payload=%s", key, got[key], wantValue, raw)
		}
	}
}

func TestBuildSteadyWebhookPayloadMapsShortAndRejectsUnknownSide(t *testing.T) {
	raw, err := buildSteadyWebhookPayload(domain.TradeSignal{Symbol: "ETHUSDT", Side: "short", CreatedAt: time.Unix(0, 0)}, config.Config{})
	if err != nil {
		t.Fatalf("build short payload: %v", err)
	}
	var got map[string]string
	if err := json.Unmarshal(raw, &got); err != nil || got["action"] != "sell" || got["order_intent"] != "entry_short" {
		t.Fatalf("unexpected short payload: %s", raw)
	}
	if _, err := buildSteadyWebhookPayload(domain.TradeSignal{Side: "sideways"}, config.Config{}); err == nil {
		t.Fatal("unknown signal side must be rejected")
	}
}

func TestSubmitSteadyWebhookDeliversThroughApplicationQueue(t *testing.T) {
	requests := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests <- body
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	svc, cfg, _ := alertTestService(t, "steady-webhook.db")
	cfg.SteadyWebhookURL = server.URL
	cfg.SteadyWebhookAPIID = "okx_bot"
	cfg.SteadyWebhookTargetExchange = "okx"
	cfg.SteadyWebhookTradeEnv = "demo"
	cfg.SteadyWebhookExchange = "binance"
	cfg.SteadyWebhookInterval = "3m"
	cfg.SteadyWebhookTokenNonce = "nonce-value"
	cfg.SteadyWebhookToken = "token-value"
	svc.alerts.webhookURL = cfg.SteadyWebhookURL
	svc.alerts.webhookTokenNonce = cfg.SteadyWebhookTokenNonce
	svc.alerts.webhookToken = cfg.SteadyWebhookToken
	svc.alerts.webhookConfig = cfg
	svc.alerts.webhook = notify.NewWebhookDispatcher(notify.NewWebhook(), svc.alerts.webhookSettings, 4, time.Minute, slog.New(slog.NewTextHandler(io.Discard, nil)))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go svc.alerts.webhook.Start(ctx)
	svc.alerts.submitSteadyWebhook(domain.TradeSignal{
		Symbol: "BTCUSDT", Kind: domain.SignalSteady, Side: "long",
		CreatedAt: time.Date(2026, 10, 16, 3, 4, 5, 0, time.UTC), ReferencePrice: 67123.45, WindowMinutes: 3,
	}, true)

	select {
	case raw := <-requests:
		var got map[string]string
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("webhook body is not JSON: %v", err)
		}
		if got["action"] != "buy" || got["order_intent"] != "entry_long" || got["token_nonce"] != "nonce-value" {
			t.Fatalf("unexpected delivered payload: %s", raw)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("steady signal was not delivered to webhook")
	}
}

func TestManualAlertTriggerUsesPersistedDirectionAndPrice(t *testing.T) {
	requests := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests <- body
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	svc, cfg, st := alertTestService(t, "manual-trigger.db")
	cfg.SteadyWebhookURL = server.URL
	cfg.SteadyWebhookAPIID = "okx_bot"
	cfg.SteadyWebhookTargetExchange = "okx"
	cfg.SteadyWebhookTradeEnv = "demo"
	cfg.SteadyWebhookExchange = "binance"
	cfg.SteadyWebhookInterval = "3m"
	cfg.SteadyWebhookTokenNonce = "nonce-value"
	cfg.SteadyWebhookToken = "token-value"
	svc.alerts.webhookURL = cfg.SteadyWebhookURL
	svc.alerts.webhookConfig = cfg
	svc.alerts.webhookTokenNonce = cfg.SteadyWebhookTokenNonce
	svc.alerts.webhookToken = cfg.SteadyWebhookToken
	created := time.Date(2026, 10, 16, 3, 4, 5, 0, time.UTC)
	if _, err := st.SaveLiquidationAlert(context.Background(), domain.LiquidationAlert{
		ID: "manual-alert", Symbol: "BTCUSDT", Side: "long", TradeSide: "long",
		WindowMinutes: 3, NotionalUSD: 400000, ThresholdUSD: 300000,
		TriggeredAt: created, ReferencePrice: 67123.45, Status: domain.AlertStateWatching, UpdatedAt: created,
	}); err != nil {
		t.Fatal(err)
	}
	result, err := svc.TriggerAlert(context.Background(), "alert", "manual-alert")
	if err != nil {
		t.Fatalf("manual trigger: %v", err)
	}
	if result == nil {
		t.Fatal("manual trigger result must be returned")
	}
	select {
	case raw := <-requests:
		var got map[string]string
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("webhook body is not JSON: %v", err)
		}
		if got["action"] != "buy" || got["price"] != "67123.45" || got["condition"] != "manual_liquidation_trigger" || got["source"] != "liquidation_alert_manual" {
			t.Fatalf("unexpected manual payload: %s", raw)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("manual alert trigger was not delivered to webhook")
	}
}
