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
		"position_effect": "open", "position_side": "long",
		"ticker": "BTCUSDT", "coinpair": "BTCUSDT", "price": "67123.45",
		"exchange": "binance", "interval": "3m", "condition": "steady_liquidation_confirmation",
		"text": "entry_long", "order_intent": "entry_long", "source": "liquidation_alert",
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
	if err := json.Unmarshal(raw, &got); err != nil || got["action"] != "sell" || got["order_intent"] != "entry_short" || got["position_effect"] != "open" || got["position_side"] != "short" {
		t.Fatalf("unexpected short payload: %s", raw)
	}
	if _, err := buildSteadyWebhookPayload(domain.TradeSignal{Side: "sideways"}, config.Config{}); err == nil {
		t.Fatal("unknown signal side must be rejected")
	}
}

func TestBuildPrecloseWebhookPayloadClosesOppositePosition(t *testing.T) {
	for _, test := range []struct {
		name, side, action, positionSide, intent string
	}{
		{name: "long entry closes short", side: "long", action: "buy", positionSide: "short", intent: "close_short"},
		{name: "short entry closes long", side: "short", action: "sell", positionSide: "long", intent: "close_long"},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, err := buildPrecloseWebhookPayload(domain.TradeSignal{Kind: domain.SignalSteady, Symbol: "ETHUSDT", Side: test.side}, config.Config{})
			if err != nil {
				t.Fatalf("build preclose payload: %v", err)
			}
			var got map[string]string
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("payload is not JSON: %v", err)
			}
			if got["action"] != test.action || got["position_effect"] != "close" || got["position_side"] != test.positionSide || got["order_intent"] != test.intent || got["text"] != test.intent {
				t.Fatalf("unexpected preclose payload: %s", raw)
			}
			if got["condition"] != "steady_liquidation_preclose" || got["source"] != "liquidation_alert_preclose" {
				t.Fatalf("preclose payload must identify its source: %s", raw)
			}
		})
	}
	if _, err := buildPrecloseWebhookPayload(domain.TradeSignal{Side: "sideways"}, config.Config{}); err == nil {
		t.Fatal("unknown signal side must be rejected for preclose payloads")
	}
}

func TestSubmitSteadyWebhookDeliversThroughApplicationQueue(t *testing.T) {
	requests := make(chan []byte, 2)
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
	svc.alerts.submitSignalWebhook(domain.TradeSignal{
		Symbol: "BTCUSDT", Kind: domain.SignalSteady, Side: "long",
		CreatedAt: time.Date(2026, 10, 16, 3, 4, 5, 0, time.UTC), ReferencePrice: 67123.45, WindowMinutes: 3,
	}, true)

	var payloads [2]map[string]string
	for i := range payloads {
		select {
		case raw := <-requests:
			if err := json.Unmarshal(raw, &payloads[i]); err != nil {
				t.Fatalf("webhook body is not JSON: %v", err)
			}
		case <-time.After(4 * time.Second):
			t.Fatal("steady signal webhook was not delivered to webhook")
		}
	}
	if payloads[0]["action"] != "buy" || payloads[0]["position_effect"] != "close" || payloads[0]["position_side"] != "short" || payloads[0]["order_intent"] != "close_short" {
		t.Fatalf("the preclose payload must be delivered first: %#v", payloads[0])
	}
	if payloads[1]["action"] != "buy" || payloads[1]["position_effect"] != "open" || payloads[1]["position_side"] != "long" || payloads[1]["order_intent"] != "entry_long" || payloads[1]["token_nonce"] != "nonce-value" {
		t.Fatalf("unexpected delivered opening payload: %#v", payloads[1])
	}
}

func TestDisabledOpeningSignalStillSendsPreclose(t *testing.T) {
	requests := make(chan []byte, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests <- body
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	svc, cfg, _ := alertTestService(t, "disabled-opening.db")
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
	if _, err := svc.SaveAlertOrderSignalSettings(context.Background(), domain.OrderSignalSettings{Alerts: false, Aggressive: false, Steady: false, AlertsClose: true, AggressiveClose: true, SteadyClose: true}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go svc.alerts.webhook.Start(ctx)
	svc.alerts.submitSignalWebhook(domain.TradeSignal{Symbol: "BTCUSDT", Kind: domain.SignalAggressive, Side: "short", CreatedAt: time.Now().UTC(), ReferencePrice: 67123.45}, true)

	select {
	case raw := <-requests:
		var got map[string]string
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("webhook body is not JSON: %v", err)
		}
		if got["position_effect"] != "close" || got["position_side"] != "long" || got["order_intent"] != "close_long" {
			t.Fatalf("disabled opening signal must still preclose: %s", raw)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("preclose webhook was not delivered")
	}
	select {
	case raw := <-requests:
		t.Fatalf("disabled opening signal unexpectedly opened a position: %s", raw)
	case <-time.After(500 * time.Millisecond):
	}
}

func TestCloseOnlyRecordsSuppressAutomaticWebhooks(t *testing.T) {
	requests := make(chan []byte, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests <- body
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	svc, cfg, _ := alertTestService(t, "close-only-suppressed.db")
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
	if _, err := svc.SaveAlertOrderSignalSettings(context.Background(), domain.OrderSignalSettings{Alerts: true, Aggressive: true, Steady: true, AlertsClose: true, AggressiveClose: true, SteadyClose: true}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go svc.alerts.webhook.Start(ctx)
	svc.alerts.submitSignalWebhook(domain.TradeSignal{
		Symbol: "BTCUSDT", Kind: domain.SignalSteady, Side: "long", CreatedAt: time.Now().UTC(), ReferencePrice: 67123.45,
		FilterApplied: true, OpenAllowed: false, CloseAllowed: true,
	}, true)
	svc.alerts.submitAlertWebhook(domain.LiquidationAlert{
		ID: "close-only-alert", Symbol: "BTCUSDT", TradeSide: "long", Side: "short", TriggeredAt: time.Now().UTC(), ReferencePrice: 67123.45,
		FilterApplied: true, OpenAllowed: false, CloseAllowed: true,
	}, true)

	select {
	case raw := <-requests:
		t.Fatalf("close-only records unexpectedly sent an automatic webhook: %s", raw)
	case <-time.After(700 * time.Millisecond):
	}
}

func TestNegativeETHGammaLeavesOpeningWebhookForUnifiedTVOrderPolicy(t *testing.T) {
	requests := make(chan []byte, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests <- body
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	svc, cfg, _ := alertTestService(t, "negative-eth-gamma.db")
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
	now := time.Now().UTC()
	svc.dashboard.Lock()
	negative := -0.123456
	svc.dashboard.deribit["ETHUSDT"] = domain.OptionGammaPoint{Symbol: "ETHUSDT", Underlying: "ETH", Source: "deribit", State: "ok", Time: now, Gamma: &negative, Contracts: 80}
	svc.dashboard.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go svc.alerts.webhook.Start(ctx)
	svc.alerts.submitSignalWebhook(domain.TradeSignal{Symbol: "ETHUSDT", Kind: domain.SignalSteady, Side: "long", CreatedAt: now, ReferencePrice: 3210.45}, true)

	var payloads [2]map[string]string
	for i := range payloads {
		select {
		case raw := <-requests:
			if err := json.Unmarshal(raw, &payloads[i]); err != nil {
				t.Fatalf("webhook body is not JSON: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("webhook %d was not delivered", i+1)
		}
	}
	if payloads[0]["position_effect"] != "close" || payloads[0]["position_side"] != "short" || payloads[0]["order_intent"] != "close_short" {
		t.Fatalf("negative ETH gamma must preserve the preclose: %#v", payloads[0])
	}
	if payloads[1]["position_effect"] != "open" || payloads[1]["position_side"] != "long" || payloads[1]["order_intent"] != "entry_long" {
		t.Fatalf("opening signal should reach unified TVBot policy: %#v", payloads[1])
	}
}

func TestAlertWebhookRespectsCloseAndOpenSwitches(t *testing.T) {
	requests := make(chan []byte, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests <- body
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	svc, cfg, _ := alertTestService(t, "alert-preclose.db")
	cfg.SteadyWebhookURL = server.URL
	cfg.SteadyWebhookTokenNonce = "nonce-value"
	cfg.SteadyWebhookToken = "token-value"
	svc.alerts.webhookURL = cfg.SteadyWebhookURL
	svc.alerts.webhookTokenNonce = cfg.SteadyWebhookTokenNonce
	svc.alerts.webhookToken = cfg.SteadyWebhookToken
	svc.alerts.webhookConfig = cfg
	svc.alerts.webhook = notify.NewWebhookDispatcher(notify.NewWebhook(), svc.alerts.webhookSettings, 4, time.Minute, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, err := svc.SaveAlertOrderSignalSettings(context.Background(), domain.OrderSignalSettings{Alerts: true, Aggressive: false, Steady: false, AlertsClose: true, AggressiveClose: true, SteadyClose: true}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go svc.alerts.webhook.Start(ctx)
	svc.alerts.submitAlertWebhook(domain.LiquidationAlert{ID: "alert-1", Symbol: "BTCUSDT", TradeSide: "long", TriggeredAt: time.Now().UTC(), ReferencePrice: 67123.45}, true)

	var payloads [2]map[string]string
	for i := range payloads {
		select {
		case raw := <-requests:
			if err := json.Unmarshal(raw, &payloads[i]); err != nil {
				t.Fatalf("webhook body is not JSON: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("alert webhook %d was not delivered", i+1)
		}
	}
	if payloads[0]["position_effect"] != "close" || payloads[0]["position_side"] != "short" || payloads[0]["order_intent"] != "close_short" {
		t.Fatalf("alert must preclose first when close is enabled: %#v", payloads[0])
	}
	if payloads[1]["position_effect"] != "open" || payloads[1]["position_side"] != "long" || payloads[1]["order_intent"] != "entry_long" {
		t.Fatalf("alert must open after preclose when open is enabled: %#v", payloads[1])
	}
}

func TestBuildSignalWebhookPayloadMapsAggressiveAndAlertKinds(t *testing.T) {
	for _, test := range []struct {
		name      string
		build     func() ([]byte, error)
		condition string
		source    string
	}{
		{
			name: "aggressive", condition: "aggressive_liquidation_signal", source: "liquidation_alert",
			build: func() ([]byte, error) {
				return buildSignalWebhookPayload(domain.TradeSignal{Kind: domain.SignalAggressive, Symbol: "ETHUSDT", Side: "short", ReferencePrice: 3200}, config.Config{})
			},
		},
		{
			name: "alert", condition: "liquidation_alert", source: "liquidation_alert",
			build: func() ([]byte, error) {
				return buildAlertWebhookPayload(domain.TradeSignal{Kind: "alert", Symbol: "ETHUSDT", Side: "long", ReferencePrice: 3200}, config.Config{})
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, err := test.build()
			if err != nil {
				t.Fatalf("build payload: %v", err)
			}
			var got map[string]string
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("payload is not JSON: %v", err)
			}
			if got["condition"] != test.condition || got["source"] != test.source {
				t.Fatalf("unexpected payload: %s", raw)
			}
		})
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
	svc.alerts.tenantID = "pcdog"
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
		if got["action"] != "buy" || got["price"] != "67123.45" || got["condition"] != "manual_liquidation_trigger" || got["source"] != "liquidation_alert_manual" || got["tenant_id"] != "pcdog" {
			t.Fatalf("unexpected manual payload: %s", raw)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("manual alert trigger was not delivered to webhook")
	}
}
