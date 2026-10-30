package server

import (
	"testing"

	tvbotgate "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotgate"
	tvbotstorage "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotstorage"
	tvbottrading "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

func TestGatePositionRiskPlanUsesActualShortPositionAverage(t *testing.T) {
	record := tvbotstorage.OrderRecord{
		Status:         tvbotstorage.StatusSubmitted,
		TargetExchange: tvbottrading.ExchangeGate,
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
			RiskReference:  tvbottrading.RiskReferencePositionAverage,
		},
	}
	position := tvbotgate.Position{
		Contract:   "ETH_USDT",
		Size:       "-52",
		EntryPrice: "2248.85",
		Leverage:   "20",
	}

	plans, err := gatePositionRiskPlan(record, position, "0.01", "default", "isolated")
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 2 {
		t.Fatalf("plans=%#v", plans)
	}
	if plans[0].triggerPx != "2226.36" || plans[0].rule != 2 || plans[0].orderType != "close-short-order" {
		t.Fatalf("unexpected TP plan=%#v", plans[0])
	}
	if plans[1].triggerPx != "2272.46" || plans[1].rule != 1 || plans[1].orderType != "close-short-order" {
		t.Fatalf("unexpected SL plan=%#v", plans[1])
	}
	if plans[0].triggerPx == "2210.67" || plans[1].triggerPx == "2256.45" {
		t.Fatalf("plan still uses the signal-limit price: %#v", plans)
	}
}

func TestGateRiskClientTextIsStableAndDistinct(t *testing.T) {
	tp := gateRiskClientText("default", "ETH_USDT", "tp")
	sl := gateRiskClientText("default", "ETH_USDT", "sl")
	if tp == sl || tp == gateRiskClientText("other", "ETH_USDT", "tp") {
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
