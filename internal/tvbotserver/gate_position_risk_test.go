package server

import (
	"testing"

	tvbotstorage "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotstorage"
	tvbottrading "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

func TestGateLayerRiskPlansUseEachLayerFillPrice(t *testing.T) {
	record := tvbotstorage.OrderRecord{
		Status:         tvbotstorage.StatusSubmitted,
		TargetExchange: tvbottrading.ExchangeGate,
		Action:         tvbottrading.ActionShort,
		PositionEffect: tvbottrading.PositionEffectOpen,
		Leverage:       20,
		Risk: tvbottrading.Risk{
			Type:  tvbottrading.RiskTPSL,
			TPPct: flexibleFloatPointer(20),
			SLPct: flexibleFloatPointer(21),
		},
		Result: tvbottrading.OrderResult{
			TargetExchange: tvbottrading.ExchangeGate,
			InstID:         "ETH_USDT",
			Leverage:       20,
			RiskReference:  tvbottrading.RiskReferenceLayerAverage,
		},
	}
	layer := tvbottrading.OrderLayerResult{
		Name:  "signal_limit",
		OrdID: "11259000794943386",
	}
	plans, err := gateLayerRiskPlans(record, layer, 1, gateLayerFill{quantity: 8, price: 2248.85, orderSize: -8}, "0.01", false, "default", "isolated")
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 2 {
		t.Fatalf("plans=%#v", plans)
	}
	if plans[0].triggerPx != "2226.36" || plans[0].rule != 2 || plans[0].orderType != "plan-close-short-position" || plans[0].size != "8" {
		t.Fatalf("unexpected TP plan=%#v", plans[0])
	}
	if plans[1].triggerPx != "2272.46" || plans[1].rule != 1 || plans[1].orderType != "plan-close-short-position" || plans[1].size != "8" {
		t.Fatalf("unexpected SL plan=%#v", plans[1])
	}
	otherLayer := layer
	otherLayer.OrdID = "11259000794943385"
	other, err := gateLayerRiskPlans(record, otherLayer, 2, gateLayerFill{quantity: 44, price: 2233, orderSize: -44}, "0.01", false, "default", "isolated")
	if err != nil || other[0].triggerPx != "2210.67" || other[1].triggerPx != "2256.45" || other[0].size != "44" || other[0].clientText == plans[0].clientText {
		t.Fatalf("each layer must own a distinct quantity and risk id: plans=%#v other=%#v err=%v", plans, other, err)
	}
}

func TestGateRiskClientTextIsStableAndDistinct(t *testing.T) {
	tp := gateRiskClientText("default", "ETH_USDT", "signal", "layer-1", "tp")
	sl := gateRiskClientText("default", "ETH_USDT", "signal", "layer-1", "sl")
	if tp == sl || tp == gateRiskClientText("other", "ETH_USDT", "signal", "layer-1", "tp") || tp == gateRiskClientText("default", "ETH_USDT", "signal", "layer-2", "tp") {
		t.Fatalf("risk client texts must be distinct: tp=%q sl=%q", tp, sl)
	}
	if len(tp) > 28 || len(sl) > 28 || tp[:len(gatePositionRiskTextPrefix)] != gatePositionRiskTextPrefix {
		t.Fatalf("risk client text does not satisfy Gate limits: tp=%q sl=%q", tp, sl)
	}
}

func flexibleFloatPointer(value float64) *tvbottrading.FlexibleFloat {
	f := tvbottrading.NewFlexibleFloat(value)
	return &f
}
