package app

import (
	"testing"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/config"
)

func TestAlertRuntimeServiceConfigUsesTenantWebhookTarget(t *testing.T) {
	svc := &Service{}
	svc.SetWebhookTargetResolver(func(tenantID string) (WebhookTarget, bool) {
		if tenantID != "wyx" {
			return WebhookTarget{}, false
		}
		return WebhookTarget{Exchange: "gate", APIID: "gate-main", TradeEnv: "demo", Token: "tenant-token"}, true
	})
	rtm := &alertRuntime{service: svc, tenantID: "wyx", webhookConfig: config.Config{
		SteadyWebhookTargetExchange: "binance",
		SteadyWebhookAPIID:          "binance-main",
		SteadyWebhookTradeEnv:       "demo",
	}}
	got := rtm.serviceConfig()
	if got.SteadyWebhookTargetExchange != "gate" || got.SteadyWebhookAPIID != "gate-main" || got.SteadyWebhookTradeEnv != "demo" {
		t.Fatalf("tenant target did not override process default: %#v", got)
	}
	if got.SteadyWebhookToken != "tenant-token" {
		t.Fatalf("tenant webhook token did not override process default: %#v", got)
	}
}
