package app

import (
	"fmt"
	"math"
	"strings"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

const (
	marketPriceFlatThreshold = 0.0005
	marketMetricFlatRatio    = 0.005
	marketFlowFlatRatio      = 0.05
	marketGammaFlipNearRatio = 0.01

	marketBiasBullish      = "bullish"
	marketBiasBearish      = "bearish"
	marketBiasNeutral      = "neutral"
	marketBiasDivergent    = "divergent"
	marketBiasInsufficient = "insufficient"
)

func classifyMarketChange(value *float64, threshold float64) (int, bool) {
	if value == nil {
		return 0, false
	}
	if math.Abs(*value) < threshold {
		return 0, true
	}
	if *value > 0 {
		return 1, true
	}
	return -1, true
}

func classifyMarketRelativeChange(value, base *float64) (int, bool) {
	if value == nil || base == nil || *base <= 0 {
		return 0, false
	}
	return classifyMarketChange(value, *base*marketMetricFlatRatio)
}

func classifyMarketFlow(value *float64, total float64) (int, bool) {
	if value == nil {
		return 0, false
	}
	threshold := total * marketFlowFlatRatio
	if threshold <= 0 {
		threshold = 1
	}
	return classifyMarketChange(value, threshold)
}

func marketDirectionText(value int, positive, negative, flat string) string {
	switch value {
	case 1:
		return positive
	case -1:
		return negative
	default:
		return flat
	}
}

func marketBiasLabel(bias string) string {
	switch bias {
	case marketBiasBullish:
		return "偏多"
	case marketBiasBearish:
		return "偏空"
	case marketBiasDivergent:
		return "周期分歧"
	case marketBiasInsufficient:
		return "数据不足"
	default:
		return "中性"
	}
}

func marketWindowMissing(window domain.MarketWindow) []string {
	missing := []string{}
	if window.OIDeltaUSD == nil {
		missing = append(missing, "OI历史数据")
	}
	if window.NetPositionDeltaUSD == nil {
		missing = append(missing, "净仓估算历史数据")
	}
	if window.CVDDeltaUSD == nil {
		missing = append(missing, "主动买卖历史数据")
	}
	return missing
}

func marketWindowAnalysis(window domain.MarketWindow) string {
	missing := marketWindowMissing(window)
	if len(missing) > 0 {
		return "数据不足：" + strings.Join(missing, "、")
	}
	if window.Structure != "" && window.BiasLabel != "" {
		return window.Structure + " · " + window.BiasLabel
	}
	flow := "主动买入占优"
	if *window.CVDDeltaUSD < 0 {
		flow = "主动卖出占优"
	}
	change := "持仓稳定"
	if *window.OIDeltaUSD > 0 {
		change = "增仓"
	} else if *window.OIDeltaUSD < 0 {
		change = "减仓"
	}
	return change + " · " + flow
}

func marketWindowFlowSummary(series []domain.MarketPoint) (float64, bool) {
	total := 0.
	for _, point := range series {
		if point.BuyUSD == nil || point.SellUSD == nil {
			continue
		}
		total += *point.BuyUSD + *point.SellUSD
	}
	return total, total > 0
}

func enrichMarketWindow(window domain.MarketWindow, currentOI *float64, flowTotal float64) domain.MarketWindow {
	window.Risks = []string{}
	priceState, priceOK := classifyMarketChange(window.PriceChangePct, marketPriceFlatThreshold)
	oiState, oiOK := classifyMarketRelativeChange(window.OIDeltaUSD, currentOI)
	netState, netOK := classifyMarketRelativeChange(window.NetPositionDeltaUSD, currentOI)
	flowState, flowOK := classifyMarketFlow(window.CVDDeltaUSD, flowTotal)
	window.Bias = marketBiasInsufficient
	window.BiasLabel = marketBiasLabel(window.Bias)
	window.Structure = "数据不足"
	window.Evidence = "数据不足"

	missing := marketWindowMissing(window)
	if !priceOK {
		missing = appendUnique(missing, "价格历史数据")
	}
	if !oiOK {
		missing = appendUnique(missing, "OI历史数据")
	}
	if !netOK {
		missing = appendUnique(missing, "净仓估算历史数据")
	}
	if !flowOK {
		missing = appendUnique(missing, "主动买卖历史数据")
	}
	if len(missing) > 0 {
		window.Evidence = "数据不足：" + strings.Join(missing, "、")
		window.Analysis = window.Evidence
		return window
	}

	sameBullish := priceState > 0 && netState > 0 && flowState > 0
	sameBearish := priceState < 0 && netState < 0 && flowState < 0
	switch {
	case sameBullish:
		window.Bias = marketBiasBullish
	case sameBearish:
		window.Bias = marketBiasBearish
	case priceState == 0 && netState == 0 && flowState == 0:
		window.Bias = marketBiasNeutral
	default:
		window.Bias = marketBiasDivergent
	}
	window.BiasLabel = marketBiasLabel(window.Bias)
	window.Structure = marketWindowStructure(window.Bias, oiState)
	window.Evidence = fmt.Sprintf("价格%s · OI%s · 净仓%s · 主动流%s",
		marketDirectionText(priceState, "上涨", "下跌", "横盘"),
		marketDirectionText(oiState, "增仓", "减仓", "稳定"),
		marketDirectionText(netState, "增加", "减少", "稳定"),
		marketDirectionText(flowState, "主动买入", "主动卖出", "均衡"))
	if priceState != 0 && flowState != 0 && priceState != flowState {
		window.Risks = append(window.Risks, "价格与主动流背离")
	}
	if window.Bias == marketBiasDivergent {
		window.Risks = append(window.Risks, "价格、主动流与净仓方向分歧")
	}
	window.Analysis = marketWindowAnalysis(window)
	return window
}

func marketWindowStructure(bias string, oiState int) string {
	switch bias {
	case marketBiasBullish:
		if oiState > 0 {
			return "多头增仓"
		}
		if oiState < 0 {
			return "空头回补"
		}
		return "偏多结构"
	case marketBiasBearish:
		if oiState > 0 {
			return "空头增仓"
		}
		if oiState < 0 {
			return "多头减仓"
		}
		return "偏空结构"
	case marketBiasDivergent:
		return "信号分歧"
	default:
		return "方向中性"
	}
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func assessmentSegment(key, label string, windows []domain.MarketWindow) domain.MarketAssessmentSegment {
	segment := domain.MarketAssessmentSegment{Key: key, Label: label, Bias: marketBiasInsufficient}
	available := 0
	bullish, bearish, neutral, divergent := 0, 0, 0, 0
	for _, window := range windows {
		switch window.Bias {
		case marketBiasBullish:
			bullish++
			available++
		case marketBiasBearish:
			bearish++
			available++
		case marketBiasNeutral:
			neutral++
			available++
		case marketBiasDivergent:
			divergent++
			available++
		}
	}
	if available == 0 {
		segment.Summary = label + "：数据不足"
		return segment
	}
	switch {
	case bullish > 0 && bearish > 0:
		segment.Bias = marketBiasDivergent
	case bullish > bearish && bullish > neutral && bullish >= divergent:
		segment.Bias = marketBiasBullish
	case bearish > bullish && bearish > neutral && bearish >= divergent:
		segment.Bias = marketBiasBearish
	case divergent > 0 && divergent >= bullish && divergent >= bearish:
		segment.Bias = marketBiasDivergent
	default:
		segment.Bias = marketBiasNeutral
	}
	parts := []string{}
	for _, window := range windows {
		if window.Analysis != "" {
			parts = append(parts, window.Label+" "+window.Analysis)
		}
	}
	segment.Summary = label + "：" + marketBiasLabel(segment.Bias)
	if len(parts) > 0 {
		segment.Summary += "；" + strings.Join(parts, "；")
	}
	return segment
}

func overallAssessment(short, medium, long domain.MarketAssessmentSegment) domain.MarketAssessmentSegment {
	overall := domain.MarketAssessmentSegment{Key: "overall", Label: "总体倾向", Bias: marketBiasInsufficient}
	if short.Bias == marketBiasInsufficient && medium.Bias == marketBiasInsufficient {
		overall.Bias = long.Bias
		overall.Summary = "总体倾向：" + marketBiasLabel(overall.Bias)
		return overall
	}
	if (short.Bias == marketBiasBullish || short.Bias == marketBiasBearish) && short.Bias == medium.Bias {
		overall.Bias = short.Bias
		overall.Summary = "总体倾向：" + marketBiasLabel(overall.Bias)
		if long.Bias != marketBiasInsufficient && long.Bias != overall.Bias {
			overall.Summary += "；长线为" + marketBiasLabel(long.Bias)
		}
		return overall
	}
	if (short.Bias == marketBiasBullish || short.Bias == marketBiasBearish) && medium.Bias == marketBiasNeutral {
		overall.Bias = short.Bias
	} else if (medium.Bias == marketBiasBullish || medium.Bias == marketBiasBearish) && short.Bias == marketBiasNeutral {
		overall.Bias = medium.Bias
	} else if short.Bias == marketBiasDivergent || medium.Bias == marketBiasDivergent || short.Bias != medium.Bias {
		overall.Bias = marketBiasDivergent
	} else {
		overall.Bias = marketBiasNeutral
	}
	overall.Summary = "总体倾向：" + marketBiasLabel(overall.Bias)
	return overall
}

func buildMarketAssessment(state string, windows []domain.MarketWindow, current *domain.MarketMetric, gamma domain.GammaView) domain.MarketAssessment {
	byLabel := map[string]domain.MarketWindow{}
	for _, window := range windows {
		byLabel[window.Label] = window
	}
	short := assessmentSegment("short", "短线（5M+1H）", []domain.MarketWindow{byLabel["5m"], byLabel["1h"]})
	medium := assessmentSegment("medium", "中线（4H+12H）", []domain.MarketWindow{byLabel["4h"], byLabel["12h"]})
	long := assessmentSegment("long", "长线（24H）", []domain.MarketWindow{byLabel["24h"]})
	assessment := domain.MarketAssessment{Segments: []domain.MarketAssessmentSegment{short, medium, long}, Overall: overallAssessment(short, medium, long), RiskLevel: "low", RiskLabel: "低风险关注", Risks: []string{}}
	if state == "partial" {
		assessment.Risks = append(assessment.Risks, "永续指标部分缺失")
	}
	if state == "stale" {
		assessment.Risks = append(assessment.Risks, "永续指标已过期")
	}
	for _, window := range windows {
		for _, risk := range window.Risks {
			assessment.Risks = appendUnique(assessment.Risks, risk)
		}
	}
	if current != nil && current.FundingRate != nil && current.AccountRatio != nil && current.TopPositionRatio != nil {
		funding, account, top := *current.FundingRate, *current.AccountRatio, *current.TopPositionRatio
		if funding > 0 && account > 1 && top > 1 {
			assessment.Risks = appendUnique(assessment.Risks, "资金费率与多空比同向，存在多头拥挤风险")
		} else if funding < 0 && account < 1 && top < 1 {
			assessment.Risks = appendUnique(assessment.Risks, "资金费率与多空比同向，存在空头拥挤风险")
		}
	}
	if gamma.State == "stale" {
		assessment.Risks = appendUnique(assessment.Risks, "期权 Gamma 数据已过期")
	} else if gamma.State == "partial" {
		assessment.Risks = appendUnique(assessment.Risks, "期权 Gamma 数据部分覆盖")
	}
	if gamma.State == "ok" || gamma.State == "partial" {
		if gamma.NetGEXUSD < 0 {
			assessment.Risks = appendUnique(assessment.Risks, "负 Gamma，价格波动放大风险")
		}
		if current != nil && current.MarkPrice != nil && gamma.GammaFlip != nil && *current.MarkPrice != 0 && math.Abs(*current.MarkPrice-*gamma.GammaFlip)/math.Abs(*current.MarkPrice) <= marketGammaFlipNearRatio {
			assessment.Risks = appendUnique(assessment.Risks, "价格接近 Gamma Flip，方向切换风险")
		}
	}
	if state == "stale" || state == "unavailable" || len(assessment.Risks) >= 2 {
		assessment.RiskLevel = "high"
		assessment.RiskLabel = "高风险关注"
	} else if len(assessment.Risks) == 1 {
		assessment.RiskLevel = "medium"
		assessment.RiskLabel = "中风险关注"
	}
	assessment.Summary = assessment.Overall.Summary + "；" + assessment.RiskLabel
	if len(assessment.Risks) > 0 {
		assessment.Summary += "：" + strings.Join(assessment.Risks, "、")
	}
	return assessment
}
