package httpapi

import (
	"strings"
	"testing"
)

func TestMarketInfoAssessmentUIIsEmbedded(t *testing.T) {
	page := string(pagesHTML)
	for _, marker := range []string{
		`id="market-assessment-heading"`,
		`id="market-assessment-cards"`,
		`id="market-assessment-risks"`,
		`Delta Exposure (DEX)`,
		`id="delta-exposure-method"`,
		`id="gamma-chart-help"`,
		`<th>交易倾向</th>`,
		`<th>依据 / 风险</th>`,
	} {
		if !strings.Contains(page, marker) {
			t.Fatalf("market information page is missing %q", marker)
		}
	}
	assets := string(pagesJS)
	for _, marker := range []string{
		"function renderMarketAssessment(assessment)",
		"marketBiasLabel",
		"delta_exposure",
		"对冲方向",
		"price_change_pct",
		"w.evidence||w.analysis",
		"function gammaChart(g, currentPrice)",
		"gamma-chart-panning",
		"chart.onwheel",
		"当前价格上下20%",
	} {
		if !strings.Contains(assets, marker) {
			t.Fatalf("market information asset is missing %q", marker)
		}
	}
}
