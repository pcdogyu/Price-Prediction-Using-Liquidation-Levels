package app

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/config"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
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
