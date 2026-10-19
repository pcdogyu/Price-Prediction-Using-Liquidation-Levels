package trading

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotsecurity"
)

func TestBuildTemplateProducesValidJSONAndToken(t *testing.T) {
	req := TemplateRequest{
		PriceSource: "close",
		Leverage:    5,
		Amount:      NewFlexibleFloat(100),
	}
	tokenSvc := security.NewTokenService("unit-test-secret")
	resp, err := BuildTemplate(req, tokenSvc)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(resp.JSON), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["price"] != "{{close}}" || payload["sent_at"] != "{{timenow}}" || payload["ticker"] != "{{ticker}}" {
		t.Fatalf("unexpected placeholders: %#v", payload)
	}
	if payload["trade_env"] != TradeEnvDemo {
		t.Fatalf("template should default to demo trade_env: %#v", payload)
	}
	if payload["action"] != "{{strategy.order.action}}" || payload["coinpair"] != "{{ticker}}" {
		t.Fatalf("unexpected dynamic placeholders: %#v", payload)
	}
	if payload["exchange"] != "{{exchange}}" || payload["interval"] != "{{interval}}" {
		t.Fatalf("unexpected TradingView market placeholders: %#v", payload)
	}
	if payload["condition"] != "{{strategy.order.comment}}" || payload["text"] != "{{strategy.order.alert_message}}" || payload["source"] != "tradingview" {
		t.Fatalf("unexpected TradingView order fields: %#v", payload)
	}
	if payload["order_intent"] != "{{strategy.order.alert_message}}" {
		t.Fatalf("order_intent should use strategy alert_message: %#v", payload)
	}
	if _, ok := payload["risk"]; ok {
		t.Fatalf("risk field should not be present: %#v", payload)
	}
	if _, ok := payload["amount"]; ok {
		t.Fatalf("amount field should not be present: %#v", payload)
	}
	if _, ok := payload["leverage"]; ok {
		t.Fatalf("leverage field should not be present: %#v", payload)
	}
	nonce, ok := payload["token_nonce"].(string)
	if !ok || nonce == "" || nonce != resp.TokenNonce {
		t.Fatalf("token_nonce not included: %#v", payload)
	}
	if !tokenSvc.Validate(CanonicalTargetTradeEnvWebhookTokenPayloadWithNonce(req.TargetExchange, req.APIID, TradeEnvDemo, resp.TokenNonce), resp.Token) {
		t.Fatal("generated token did not validate against canonical template payload")
	}
	if strings.LastIndex(resp.JSON, `"token"`) < strings.LastIndex(resp.JSON, `"source"`) {
		t.Fatalf("token should be the final JSON field: %s", resp.JSON)
	}
}

func TestBuildTemplateCanBindSelectedAPI(t *testing.T) {
	req := TemplateRequest{
		PriceSource: "low",
		APIID:       "backup",
		TradeEnv:    TradeEnvLive,
		Leverage:    3,
		Amount:      NewFlexibleFloat(50),
	}
	tokenSvc := security.NewTokenService("unit-test-secret")
	resp, err := BuildTemplate(req, tokenSvc)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(resp.JSON), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["api_id"] != "backup" {
		t.Fatalf("api_id not included: %#v", payload)
	}
	if payload["trade_env"] != TradeEnvLive {
		t.Fatalf("trade_env not included: %#v", payload)
	}
	if !tokenSvc.Validate(CanonicalTargetTradeEnvWebhookTokenPayloadWithNonce(req.TargetExchange, req.APIID, req.TradeEnv, resp.TokenNonce), resp.Token) {
		t.Fatal("generated token did not validate against selected api payload")
	}
	withoutAPI := req
	withoutAPI.APIID = ""
	if tokenSvc.Validate(CanonicalTargetTradeEnvWebhookTokenPayloadWithNonce(withoutAPI.TargetExchange, withoutAPI.APIID, withoutAPI.TradeEnv, resp.TokenNonce), resp.Token) {
		t.Fatal("token should be bound to api_id when api_id is selected")
	}
}

func TestBuildTemplateCanBindTargetExchange(t *testing.T) {
	req := TemplateRequest{
		TargetExchange: ExchangeBinance,
		PriceSource:    "close",
		APIID:          "binance-main",
		TradeEnv:       TradeEnvDemo,
	}
	tokenSvc := security.NewTokenService("unit-test-secret")
	resp, err := BuildTemplate(req, tokenSvc)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(resp.JSON), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["target_exchange"] != ExchangeBinance || payload["api_id"] != "binance-main" {
		t.Fatalf("target exchange not included: %#v", payload)
	}
	if !tokenSvc.Validate(CanonicalTargetTradeEnvWebhookTokenPayloadWithNonce(req.TargetExchange, req.APIID, req.TradeEnv, resp.TokenNonce), resp.Token) {
		t.Fatal("generated token did not validate against Binance target payload")
	}
	okxReq := req
	okxReq.TargetExchange = ExchangeOKX
	if tokenSvc.Validate(CanonicalTargetTradeEnvWebhookTokenPayloadWithNonce(okxReq.TargetExchange, okxReq.APIID, okxReq.TradeEnv, resp.TokenNonce), resp.Token) {
		t.Fatal("Binance target token should not validate as OKX token")
	}
}

func TestBuildTemplateCanBindCoinpairAndDirection(t *testing.T) {
	tests := []struct {
		name       string
		direction  string
		wantAction string
	}{
		{name: "both", direction: "both", wantAction: "{{strategy.order.action}}"},
		{name: "long only", direction: "long", wantAction: "buy"},
		{name: "short only", direction: "short", wantAction: "sell"},
	}
	tokenSvc := security.NewTokenService("unit-test-secret")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := TemplateRequest{
				TargetExchange: ExchangeBinance,
				PriceSource:    "close",
				TradeEnv:       TradeEnvDemo,
				Coinpair:       "syrupusdt.p",
				Direction:      tt.direction,
			}
			resp, err := BuildTemplate(req, tokenSvc)
			if err != nil {
				t.Fatal(err)
			}
			var payload map[string]any
			if err := json.Unmarshal([]byte(resp.JSON), &payload); err != nil {
				t.Fatal(err)
			}
			if payload["action"] != tt.wantAction {
				t.Fatalf("unexpected action: %#v", payload)
			}
			if payload["ticker"] != "SYRUPUSDT.P" || payload["coinpair"] != "SYRUPUSDT.P" {
				t.Fatalf("unexpected fixed coinpair: %#v", payload)
			}
			if !tokenSvc.Validate(CanonicalTargetTradeEnvWebhookTokenPayloadWithNonce(req.TargetExchange, req.APIID, req.TradeEnv, resp.TokenNonce), resp.Token) {
				t.Fatal("generated token did not validate against selected target payload")
			}
		})
	}
}

func TestBuildTemplateRejectsInvalidDirection(t *testing.T) {
	req := TemplateRequest{
		PriceSource: "close",
		Direction:   "sideways",
	}
	tokenSvc := security.NewTokenService("unit-test-secret")
	if _, err := BuildTemplate(req, tokenSvc); err == nil || !strings.Contains(err.Error(), "direction must be") {
		t.Fatalf("expected invalid direction error, got %v", err)
	}
}

func TestBuildTemplateRejectsInvalidTradeEnv(t *testing.T) {
	req := TemplateRequest{
		PriceSource: "close",
		TradeEnv:    "sideways",
	}
	tokenSvc := security.NewTokenService("unit-test-secret")
	if _, err := BuildTemplate(req, tokenSvc); err == nil || !strings.Contains(err.Error(), "trade_env must be") {
		t.Fatalf("expected invalid trade_env error, got %v", err)
	}
}

func TestBuildTemplateGeneratesDifferentTokenEachTime(t *testing.T) {
	req := TemplateRequest{
		TargetExchange: ExchangeBinance,
		PriceSource:    "close",
		APIID:          "binance-main",
	}
	tokenSvc := security.NewTokenService("unit-test-secret")
	first, err := BuildTemplate(req, tokenSvc)
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildTemplate(req, tokenSvc)
	if err != nil {
		t.Fatal(err)
	}
	if first.Token == second.Token {
		t.Fatalf("template tokens should differ for repeated generation: %q", first.Token)
	}
	if first.TokenNonce == "" || second.TokenNonce == "" || first.TokenNonce == second.TokenNonce {
		t.Fatalf("template token nonces should differ: first=%q second=%q", first.TokenNonce, second.TokenNonce)
	}
}

func TestCanonicalTokenPayloadDoesNotBindActionOrCoinpair(t *testing.T) {
	longBTC := Signal{Action: ActionLong, Coinpair: "BTC", Leverage: 5, Amount: NewFlexibleFloat(100)}
	shortETH := Signal{Action: ActionShort, Coinpair: "ETH", Leverage: 5, Amount: NewFlexibleFloat(100)}
	if longBTC.CanonicalTokenPayload() != shortETH.CanonicalTokenPayload() {
		t.Fatalf("token payload should not depend on action or coinpair")
	}
}
