package app

import (
	"testing"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

func TestEnrichMarketWindowClassifiesDirectionalStructures(t *testing.T) {
	tests := []struct {
		name      string
		price     float64
		oi        float64
		net       float64
		flow      float64
		wantBias  string
		wantShape string
	}{
		{name: "long build", price: .01, oi: 10, net: 10, flow: 100, wantBias: marketBiasBullish, wantShape: "多头增仓"},
		{name: "short build", price: -.01, oi: 10, net: -10, flow: -100, wantBias: marketBiasBearish, wantShape: "空头增仓"},
		{name: "short cover", price: .01, oi: -10, net: 10, flow: 100, wantBias: marketBiasBullish, wantShape: "空头回补"},
		{name: "long reduction", price: -.01, oi: -10, net: -10, flow: -100, wantBias: marketBiasBearish, wantShape: "多头减仓"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			window := domain.MarketWindow{
				Label:               "5m",
				PriceChangePct:      ptr(tc.price),
				OIDeltaUSD:          ptr(tc.oi),
				NetPositionDeltaUSD: ptr(tc.net),
				CVDDeltaUSD:         ptr(tc.flow),
			}
			got := enrichMarketWindow(window, ptr(1000), 1000)
			if got.Bias != tc.wantBias || got.Structure != tc.wantShape || got.Analysis == "" {
				t.Fatalf("window=%+v", got)
			}
		})
	}
}

func TestEnrichMarketWindowFiltersNoiseAndMarksDivergence(t *testing.T) {
	quiet := enrichMarketWindow(domain.MarketWindow{
		Label:               "5m",
		PriceChangePct:      ptr(.0001),
		OIDeltaUSD:          ptr(1),
		NetPositionDeltaUSD: ptr(1),
		CVDDeltaUSD:         ptr(10),
	}, ptr(1000), 1000)
	if quiet.Bias != marketBiasNeutral || quiet.Structure != "方向中性" {
		t.Fatalf("quiet window=%+v", quiet)
	}

	divergent := enrichMarketWindow(domain.MarketWindow{
		Label:               "1h",
		PriceChangePct:      ptr(.01),
		OIDeltaUSD:          ptr(10),
		NetPositionDeltaUSD: ptr(-10),
		CVDDeltaUSD:         ptr(100),
	}, ptr(1000), 1000)
	if divergent.Bias != marketBiasDivergent || len(divergent.Risks) == 0 {
		t.Fatalf("divergent window=%+v", divergent)
	}
}

func TestMarketAssessmentSplitsHorizonsAndRiskFlags(t *testing.T) {
	windows := []domain.MarketWindow{
		{Label: "5m", Bias: marketBiasBullish, Analysis: "多头增仓 · 偏多"},
		{Label: "1h", Bias: marketBiasBullish, Analysis: "空头回补 · 偏多"},
		{Label: "4h", Bias: marketBiasBearish, Analysis: "空头增仓 · 偏空"},
		{Label: "12h", Bias: marketBiasBullish, Analysis: "多头增仓 · 偏多"},
		{Label: "24h", Bias: marketBiasBearish, Analysis: "多头减仓 · 偏空"},
	}
	current := &domain.MarketMetric{FundingRate: ptr(.001), AccountRatio: ptr(1.2), TopPositionRatio: ptr(1.3), MarkPrice: ptr(100)}
	gamma := domain.GammaView{State: "ok", NetGEXUSD: -100, GammaFlip: ptr(100.5)}
	assessment := buildMarketAssessment("ok", windows, current, gamma)
	if len(assessment.Segments) != 3 || assessment.Segments[0].Bias != marketBiasBullish || assessment.Segments[1].Bias != marketBiasDivergent {
		t.Fatalf("segments=%+v", assessment.Segments)
	}
	if assessment.Overall.Bias != marketBiasDivergent || assessment.RiskLevel != "high" || len(assessment.Risks) < 3 {
		t.Fatalf("assessment=%+v", assessment)
	}
}
