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
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/valuearea"
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

func TestBuildValueAreaWebhookPayloadsCarryEffectAndSource(t *testing.T) {
	open := domain.TradeSignal{Symbol: "BTCUSDT", Kind: domain.SignalValueArea, Side: "long", PositionEffect: "open", Boundary: "val", BoundaryPrice: 100, VAL: 100, VAH: 110, ReferencePrice: 99, CreatedAt: time.Unix(100, 0)}
	raw, err := buildValueAreaWebhookPayload(open, config.Config{}, false)
	if err != nil {
		t.Fatalf("build value-area open payload: %v", err)
	}
	var got map[string]any
	if err = json.Unmarshal(raw, &got); err != nil || got["condition"] != "value_area_signal" || got["source"] != "value_area_signal" || got["position_effect"] != "open" || got["position_side"] != "long" || got["order_intent"] != "entry_long" || got["boundary"] != "val" || got["boundary_price"] != 100.0 || got["val"] != 100.0 || got["vah"] != 110.0 {
		t.Fatalf("unexpected value-area open payload: %s", raw)
	}
	closeSignal := open
	closeSignal.Side, closeSignal.PositionEffect = "short", "close"
	raw, err = buildValueAreaCloseWebhookPayload(closeSignal, config.Config{})
	if err != nil {
		t.Fatalf("build value-area close payload: %v", err)
	}
	if err = json.Unmarshal(raw, &got); err != nil || got["condition"] != "value_area_close" || got["source"] != "value_area_close" || got["position_effect"] != "close" || got["position_side"] != "long" || got["order_intent"] != "close_long" {
		t.Fatalf("unexpected value-area close payload: %s", raw)
	}
}

func TestValueAreaWebhookTaskCarriesConfiguredWindow(t *testing.T) {
	svc, _, _ := alertTestService(t, "value-area-window-webhook.db")
	svc.alerts.setValueAreaWindow(valuearea.TradingWindow{StartMinute: 0, EndMinute: 24 * 60})
	task := svc.alerts.webhookTask(domain.TradeSignal{
		Symbol: "BTCUSDT", Kind: domain.SignalValueArea, Side: "long", PositionEffect: "open",
		CreatedAt: time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC), ReferencePrice: 100,
	}, "value-area-window", notify.PriorityNormal, buildSignalWebhookPayload)
	raw, err := task.Prepare(time.Date(2026, 11, 3, 16, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("prepare value-area webhook: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("payload is not JSON: %v", err)
	}
	if got["value_area_start_minute"] != float64(0) || got["value_area_end_minute"] != float64(24*60) {
		t.Fatalf("configured value-area window missing from webhook: %s", raw)
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

func TestAutomaticOpenBlocksSuppressEntriesButPreserveCloseLegs(t *testing.T) {
	requests := make(chan []byte, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests <- body
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	svc, cfg, _ := alertTestService(t, "automatic-open-blocks.db")
	cfg.SteadyWebhookURL = server.URL
	cfg.SteadyWebhookTokenNonce = "nonce-value"
	cfg.SteadyWebhookToken = "token-value"
	svc.alerts.webhookURL = cfg.SteadyWebhookURL
	svc.alerts.webhookTokenNonce = cfg.SteadyWebhookTokenNonce
	svc.alerts.webhookToken = cfg.SteadyWebhookToken
	svc.alerts.webhookConfig = cfg
	svc.alerts.webhook = notify.NewWebhookDispatcher(notify.NewWebhook(), svc.alerts.webhookSettings, 8, time.Minute, slog.New(slog.NewTextHandler(io.Discard, nil)))
	svc.alerts.blockOpen0800To0900 = true
	svc.alerts.blockOpen1600To1700 = true
	svc.alerts.blockOpen2130To2230 = true
	svc.alerts.setValueAreaWindow(valuearea.TradingWindow{StartMinute: 0, EndMinute: 24 * 60})
	if _, err := svc.SaveAlertOrderSignalSettings(context.Background(), domain.OrderSignalSettings{
		Alerts: true, Aggressive: true, Steady: true, ValueArea: true,
		AlertsClose: true, AggressiveClose: true, SteadyClose: true, ValueAreaClose: true,
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go svc.alerts.webhook.Start(ctx)
	blockedAt := time.Date(2026, 11, 4, 0, 30, 0, 0, time.UTC) // 08:30 Beijing
	svc.alerts.submitSignalWebhook(domain.TradeSignal{Symbol: "BTCUSDT", Kind: domain.SignalAggressive, Side: "long", CreatedAt: blockedAt, ReferencePrice: 100}, true)
	svc.alerts.submitSignalWebhook(domain.TradeSignal{Symbol: "ETHUSDT", Kind: domain.SignalSteady, Side: "short", CreatedAt: blockedAt, ReferencePrice: 100}, true)
	svc.alerts.submitSignalWebhook(domain.TradeSignal{ID: "value-area-blocked", Symbol: "SOLUSDT", Kind: domain.SignalValueArea, Side: "long", PositionEffect: "open", CreatedAt: blockedAt, ReferencePrice: 100}, true)
	svc.alerts.submitAlertWebhook(domain.LiquidationAlert{ID: "alert-blocked", Symbol: "BNBUSDT", TradeSide: "short", TriggeredAt: blockedAt, ReferencePrice: 100}, true)

	for i := 0; i < 4; i++ {
		select {
		case raw := <-requests:
			var payload map[string]any
			if err := json.Unmarshal(raw, &payload); err != nil {
				t.Fatalf("blocked webhook body is not JSON: %v", err)
			}
			if payload["position_effect"] != "close" {
				t.Fatalf("blocked automatic open emitted a non-close webhook: %#v", payload)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("blocked signal %d did not preserve its close leg", i+1)
		}
	}
	select {
	case raw := <-requests:
		t.Fatalf("blocked automatic opens emitted an unexpected extra webhook: %s", raw)
	case <-time.After(500 * time.Millisecond):
	}

	if _, err := svc.SaveAlertOrderSignalSettings(context.Background(), domain.OrderSignalSettings{
		Alerts: true, Aggressive: true, Steady: true, ValueArea: true,
		AlertsClose: false, AggressiveClose: false, SteadyClose: false, ValueAreaClose: false,
	}); err != nil {
		t.Fatal(err)
	}
	svc.alerts.submitSignalWebhook(domain.TradeSignal{Symbol: "BTCUSDT", Kind: domain.SignalAggressive, Side: "long", CreatedAt: blockedAt, ReferencePrice: 100}, true)
	select {
	case raw := <-requests:
		t.Fatalf("close-disabled blocked signal emitted a webhook: %s", raw)
	case <-time.After(500 * time.Millisecond):
	}

	if _, err := svc.SaveAlertOrderSignalSettings(context.Background(), domain.OrderSignalSettings{
		Alerts: true, Aggressive: true, Steady: true, ValueArea: true,
		AlertsClose: true, AggressiveClose: true, SteadyClose: true, ValueAreaClose: true,
	}); err != nil {
		t.Fatal(err)
	}
	closeSignal := domain.TradeSignal{ID: "value-area-explicit-close", Symbol: "SOLUSDT", Kind: domain.SignalValueArea, Side: "short", PositionEffect: "close", CreatedAt: blockedAt, ReferencePrice: 100}
	svc.alerts.submitSignalWebhook(closeSignal, true)
	select {
	case raw := <-requests:
		var payload map[string]any
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Fatalf("explicit close body is not JSON: %v", err)
		}
		if payload["position_effect"] != "close" {
			t.Fatalf("explicit close changed effect in blocked window: %#v", payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("explicit close was blocked by the automatic-open window")
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
	now := time.Date(2026, 11, 4, 2, 0, 0, 0, time.UTC)
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

func TestManualCloseOnlyAlertTriggerClosesOppositePosition(t *testing.T) {
	requests := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests <- body
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	svc, cfg, st := alertTestService(t, "manual-close-only-alert.db")
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
	created := time.Date(2026, 10, 30, 10, 35, 53, 0, time.UTC)
	if _, err := st.SaveLiquidationAlert(context.Background(), domain.LiquidationAlert{
		ID: "manual-close-only-alert", Symbol: "BTCUSDT", Side: "short", TradeSide: "short",
		WindowMinutes: 3, NotionalUSD: 400000, ThresholdUSD: 300000,
		TriggeredAt: created, ReferencePrice: 77585.1, Status: domain.AlertStateAggressive, UpdatedAt: created,
		FilterApplied: true, OpenAllowed: false, CloseAllowed: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.TriggerAlert(context.Background(), "alert", "manual-close-only-alert"); err != nil {
		t.Fatalf("manual close-only trigger: %v", err)
	}
	select {
	case raw := <-requests:
		var got map[string]string
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("webhook body is not JSON: %v", err)
		}
		if got["action"] != "sell" || got["position_effect"] != "close" || got["position_side"] != "long" || got["order_intent"] != "close_long" {
			t.Fatalf("close-only alert must close the opposite position: %s", raw)
		}
		if got["condition"] != "manual_liquidation_trigger" || got["source"] != "liquidation_alert_manual" {
			t.Fatalf("manual close-only payload source changed: %s", raw)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("manual close-only alert trigger was not delivered to webhook")
	}
}

func TestManualValueAreaCloseTriggerUsesManualFallbackCondition(t *testing.T) {
	requests := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests <- body
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	svc, cfg, st := alertTestService(t, "manual-value-area-close.db")
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
	created := time.Date(2026, 11, 9, 13, 3, 9, 0, time.UTC)
	if _, err := st.SaveTradeSignal(context.Background(), domain.TradeSignal{
		ID: "manual-value-area-close", Symbol: "ICNTUSDT", Kind: domain.SignalValueArea,
		Side: "long", PositionEffect: "close", CreatedAt: created, ReferencePrice: 0.3376,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.TriggerAlert(context.Background(), domain.SignalValueArea, "manual-value-area-close"); err != nil {
		t.Fatalf("manual value-area close trigger: %v", err)
	}
	select {
	case raw := <-requests:
		var got map[string]string
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("webhook body is not JSON: %v", err)
		}
		if got["action"] != "buy" || got["position_effect"] != "close" || got["position_side"] != "short" || got["order_intent"] != "close_short" {
			t.Fatalf("manual value-area close must preserve the close direction: %s", raw)
		}
		if got["condition"] != "manual_liquidation_trigger" || got["source"] != "liquidation_alert_manual" {
			t.Fatalf("manual value-area close must use the manual fallback condition: %s", raw)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("manual value-area close trigger was not delivered to webhook")
	}
}

func TestManualCloseOnlySignalTriggerClosesOppositePosition(t *testing.T) {
	signal := domain.TradeSignal{
		Symbol: "BTCUSDT", Kind: domain.SignalAggressive, Side: "short", ReferencePrice: 77585.1,
		FilterApplied: true, OpenAllowed: false, CloseAllowed: true,
	}
	raw, err := buildManualWebhookPayload(signal, config.Config{})
	if err != nil {
		t.Fatalf("build manual close-only payload: %v", err)
	}
	var got map[string]string
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("payload is not JSON: %v", err)
	}
	if got["action"] != "sell" || got["position_effect"] != "close" || got["position_side"] != "long" || got["order_intent"] != "close_long" {
		t.Fatalf("close-only signal must close the opposite position: %s", raw)
	}
}

func TestManualFilteredSignalTriggerAllowsExplicitEntry(t *testing.T) {
	signal := domain.TradeSignal{
		Symbol: "BTCUSDT", Kind: domain.SignalAggressive, Side: "long", ReferencePrice: 77585.1,
		FilterApplied: true, OpenAllowed: false, CloseAllowed: false,
	}
	raw, err := buildManualWebhookPayload(signal, config.Config{})
	if err != nil {
		t.Fatalf("filtered manual entry payload: %v", err)
	}
	var got map[string]string
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("payload is not JSON: %v", err)
	}
	if got["action"] != "buy" || got["position_effect"] != "open" || got["position_side"] != "long" || got["order_intent"] != "entry_long" {
		t.Fatalf("filtered manual signal must allow an explicit entry: %s", raw)
	}
}

func TestManualSignalTriggerAddsCurrentTenantToLegacyHistory(t *testing.T) {
	requests := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests <- body
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	svc, cfg, st := alertTestService(t, "manual-legacy-signal-trigger.db")
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
	if _, err := st.SaveTradeSignal(context.Background(), domain.TradeSignal{
		ID: "legacy-signal", Symbol: "BTCUSDT", Kind: domain.SignalSteady, Side: "long",
		CreatedAt: time.Date(2026, 10, 16, 3, 4, 5, 0, time.UTC), ReferencePrice: 67123.45,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.TriggerAlert(context.Background(), domain.SignalSteady, "legacy-signal"); err != nil {
		t.Fatalf("manual legacy signal trigger: %v", err)
	}
	select {
	case raw := <-requests:
		var got map[string]string
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("webhook body is not JSON: %v", err)
		}
		if got["tenant_id"] != "pcdog" {
			t.Fatalf("manual legacy signal must carry the current tenant: %s", raw)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("manual legacy signal trigger was not delivered to webhook")
	}
}

func TestValueAreaAutomaticNotificationsStayWithinBeijingWindow(t *testing.T) {
	requests := make(chan struct{}, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- struct{}{}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	svc, cfg, _ := alertTestService(t, "value-area-notification-window.db")
	cfg.SteadyWebhookURL = server.URL
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

	outside := domain.TradeSignal{
		ID: "value-area-outside", Symbol: "BTCUSDT", Kind: domain.SignalValueArea, Side: "long",
		PositionEffect: "open", CreatedAt: time.Date(2026, 11, 2, 11, 59, 59, 0, time.UTC),
		ReferencePrice: 100, Boundary: "val", BoundaryPrice: 100, VAL: 100, VAH: 110,
	}
	if status, message := svc.alerts.submitSignal(outside, true); status != "muted" || message != "交易所刷单仅在北京时间 20:00–24:00 自动通知" {
		t.Fatalf("outside-window Telegram decision = (%q, %q)", status, message)
	}
	svc.alerts.submitSignalWebhook(outside, true)
	select {
	case <-requests:
		t.Fatal("outside-window value-area signal sent an automatic webhook")
	case <-time.After(300 * time.Millisecond):
	}

	inside := outside
	inside.ID = "value-area-inside"
	inside.CreatedAt = time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	svc.alerts.submitSignalWebhook(inside, true)
	for i := 0; i < 2; i++ {
		select {
		case <-requests:
		case <-time.After(2 * time.Second):
			t.Fatalf("inside-window value-area webhook %d was not delivered", i+1)
		}
	}
}
