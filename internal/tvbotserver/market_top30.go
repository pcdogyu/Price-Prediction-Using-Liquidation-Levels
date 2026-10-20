package server

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotconfig"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotstorage"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

var excludedRankingStablecoinBases = map[string]bool{
	"BUSD": true, "DAI": true, "FDUSD": true, "FRAX": true, "PYUSD": true,
	"RLUSD": true, "TUSD": true, "USD1": true, "USDC": true, "USDD": true,
	"USDE": true, "USDP": true, "USDS": true, "USDT": true,
}

type marketScopeDecision struct {
	Exchange  string
	TradeEnv  string
	Symbol    string
	Scope     string
	Limit     int
	Available bool
	Allowed   bool
}

func applyMarketScopeRankings(resp *symbolsResponse, scope string) {
	if resp == nil {
		return
	}
	limit := config.MarketTurnoverScopeLimit(scope)
	resp.OKX.Live.TopInstruments = topOKXInstruments(resp.OKX.Live.Instruments, limit)
	resp.OKX.Demo.TopInstruments = topOKXInstruments(resp.OKX.Demo.Instruments, limit)
	resp.Binance.Live.TopInstruments = topBinanceInstruments(resp.Binance.Live.Instruments, limit)
	resp.Binance.Demo.TopInstruments = topBinanceInstruments(resp.Binance.Demo.Instruments, limit)
	resp.Gate.Live.TopInstruments = topGateInstruments(resp.Gate.Live.Instruments, limit)
	resp.Gate.Demo.TopInstruments = topGateInstruments(resp.Gate.Demo.Instruments, limit)
}

func markUnavailableMarketScopeRankings(resp *symbolsResponse, scope string) {
	if resp == nil {
		return
	}
	limit := config.MarketTurnoverScopeLimit(scope)
	markOKXRankingUnavailable(&resp.OKX.Live, limit)
	markOKXRankingUnavailable(&resp.OKX.Demo, limit)
	markBinanceRankingUnavailable(&resp.Binance.Live, limit)
	markBinanceRankingUnavailable(&resp.Binance.Demo, limit)
	markGateRankingUnavailable(&resp.Gate.Live, limit)
	markGateRankingUnavailable(&resp.Gate.Demo, limit)
}

func markOKXRankingUnavailable(set *okxInstrumentSet, limit int) {
	if set == nil || set.Error != "" || set.TickerError != "" {
		return
	}
	candidateCount, coverageComplete := okxRankingCoverage(set.Instruments)
	if coverageComplete && rankingSizeComplete(candidateCount, len(set.TopInstruments), limit) {
		return
	}
	set.TickerError = fmt.Sprintf("%s ranking unavailable: got %d ranked symbols", marketScopeLabel(limit), len(set.TopInstruments))
}

func markBinanceRankingUnavailable(set *binanceInstrumentSet, limit int) {
	if set == nil || set.Error != "" || set.TickerError != "" {
		return
	}
	candidateCount, coverageComplete := binanceRankingCoverage(set.Instruments)
	if coverageComplete && rankingSizeComplete(candidateCount, len(set.TopInstruments), limit) {
		return
	}
	set.TickerError = fmt.Sprintf("%s ranking unavailable: got %d ranked symbols", marketScopeLabel(limit), len(set.TopInstruments))
}

func markGateRankingUnavailable(set *gateInstrumentSet, limit int) {
	if set == nil || set.Error != "" || set.TickerError != "" {
		return
	}
	candidateCount, coverageComplete := gateRankingCoverage(set.Instruments)
	if coverageComplete && rankingSizeComplete(candidateCount, len(set.TopInstruments), limit) {
		return
	}
	set.TickerError = fmt.Sprintf("%s ranking unavailable: got %d ranked symbols", marketScopeLabel(limit), len(set.TopInstruments))
}

func rankingSizeComplete(catalogCount, rankedCount, limit int) bool {
	required := catalogCount
	if limit > 0 && required > limit {
		required = limit
	}
	return required > 0 && rankedCount >= required
}

func topOKXInstruments(in []symbolInstrument, limit int) []symbolInstrument {
	eligible := make([]symbolInstrument, 0, len(in))
	for _, instrument := range in {
		turnover, ok := positiveTurnover(instrument.TurnoverUSDT24h)
		if !ok || !strings.EqualFold(strings.TrimSpace(instrument.State), "live") || excludedRankingBase(instrument.BaseCcy, instrument.InstID) || !instrument.UnderlyingMatchesFamily() {
			continue
		}
		instrument.TurnoverUSDT24h = trading.NormalizeFloat(turnover)
		eligible = append(eligible, instrument)
	}
	sort.SliceStable(eligible, func(i, j int) bool {
		left, _ := positiveTurnover(eligible[i].TurnoverUSDT24h)
		right, _ := positiveTurnover(eligible[j].TurnoverUSDT24h)
		if left == right {
			return strings.Compare(strings.ToUpper(eligible[i].InstID), strings.ToUpper(eligible[j].InstID)) < 0
		}
		return left > right
	})
	return limitOKXInstruments(eligible, limit)
}

func topBinanceInstruments(in []binanceSymbolInstrument, limit int) []binanceSymbolInstrument {
	eligible := make([]binanceSymbolInstrument, 0, len(in))
	for _, instrument := range in {
		turnover, ok := positiveTurnover(instrument.TurnoverUSDT24h)
		if !ok || !strings.EqualFold(strings.TrimSpace(instrument.Status), "TRADING") || excludedRankingBase(instrument.BaseAsset, instrument.Symbol) {
			continue
		}
		instrument.TurnoverUSDT24h = trading.NormalizeFloat(turnover)
		eligible = append(eligible, instrument)
	}
	sort.SliceStable(eligible, func(i, j int) bool {
		left, _ := positiveTurnover(eligible[i].TurnoverUSDT24h)
		right, _ := positiveTurnover(eligible[j].TurnoverUSDT24h)
		if left == right {
			return strings.Compare(strings.ToUpper(eligible[i].Symbol), strings.ToUpper(eligible[j].Symbol)) < 0
		}
		return left > right
	})
	return limitBinanceInstruments(eligible, limit)
}

func topGateInstruments(in []gateSymbolInstrument, limit int) []gateSymbolInstrument {
	eligible := make([]gateSymbolInstrument, 0, len(in))
	for _, instrument := range in {
		turnover, ok := positiveTurnover(instrument.TurnoverUSDT24h)
		if !ok || !gateInstrumentTrading(instrument) || excludedRankingBase(instrument.BaseAsset, instrument.Symbol) {
			continue
		}
		instrument.TurnoverUSDT24h = trading.NormalizeFloat(turnover)
		eligible = append(eligible, instrument)
	}
	sort.SliceStable(eligible, func(i, j int) bool {
		left, _ := positiveTurnover(eligible[i].TurnoverUSDT24h)
		right, _ := positiveTurnover(eligible[j].TurnoverUSDT24h)
		if left == right {
			return strings.Compare(strings.ToUpper(eligible[i].Symbol), strings.ToUpper(eligible[j].Symbol)) < 0
		}
		return left > right
	})
	if limit > 0 && len(eligible) > limit {
		eligible = eligible[:limit]
	}
	return append([]gateSymbolInstrument(nil), eligible...)
}

func okxRankingCoverage(in []symbolInstrument) (int, bool) {
	count := 0
	for _, instrument := range in {
		if !strings.EqualFold(strings.TrimSpace(instrument.State), "live") || excludedRankingBase(instrument.BaseCcy, instrument.InstID) || !instrument.UnderlyingMatchesFamily() {
			continue
		}
		turnover, ok := parseAnyFloat(instrument.TurnoverUSDT24h)
		if !ok || turnover < 0 {
			return count, false
		}
		if turnover > 0 {
			count++
		}
	}
	return count, true
}

func binanceRankingCoverage(in []binanceSymbolInstrument) (int, bool) {
	count := 0
	for _, instrument := range in {
		if !strings.EqualFold(strings.TrimSpace(instrument.Status), "TRADING") || excludedRankingBase(instrument.BaseAsset, instrument.Symbol) {
			continue
		}
		turnover, ok := parseAnyFloat(instrument.TurnoverUSDT24h)
		if !ok || turnover < 0 {
			return count, false
		}
		if turnover > 0 {
			count++
		}
	}
	return count, true
}

func gateRankingCoverage(in []gateSymbolInstrument) (int, bool) {
	count := 0
	for _, instrument := range in {
		if !gateInstrumentTrading(instrument) || excludedRankingBase(instrument.BaseAsset, instrument.Symbol) {
			continue
		}
		turnover, ok := parseAnyFloat(instrument.TurnoverUSDT24h)
		if !ok || turnover < 0 {
			return count, false
		}
		if turnover > 0 {
			count++
		}
	}
	return count, true
}

func gateInstrumentTrading(instrument gateSymbolInstrument) bool {
	status := strings.ToLower(strings.TrimSpace(instrument.Status))
	if status == "" {
		status = strings.ToLower(strings.TrimSpace(instrument.State))
	}
	return status == "trading" || status == "tradable" || status == "online"
}

func excludedRankingBase(base, instrument string) bool {
	base = strings.ToUpper(strings.TrimSpace(base))
	if base == "" {
		base = marketSymbolBase("", instrument)
	}
	if excludedRankingStablecoinBases[base] {
		return true
	}
	return base == "PAXG" || strings.HasPrefix(base, "XAU") || strings.HasPrefix(base, "XAG")
}

func positiveTurnover(raw string) (float64, bool) {
	value, ok := parseAnyFloat(raw)
	return value, ok && value > 0
}

func limitOKXInstruments(in []symbolInstrument, limit int) []symbolInstrument {
	if len(in) == 0 {
		return []symbolInstrument{}
	}
	if limit > 0 && len(in) > limit {
		in = in[:limit]
	}
	return append([]symbolInstrument(nil), in...)
}

func limitBinanceInstruments(in []binanceSymbolInstrument, limit int) []binanceSymbolInstrument {
	if len(in) == 0 {
		return []binanceSymbolInstrument{}
	}
	if limit > 0 && len(in) > limit {
		in = in[:limit]
	}
	return append([]binanceSymbolInstrument(nil), in...)
}

func (s *Server) marketScopeDecision(signal trading.Signal) (marketScopeDecision, error) {
	exchange := trading.NormalizeExchange(signal.TargetExchange)
	tradeEnv := trading.NormalizeTradeEnv(signal.TradeEnv)
	if tradeEnv == "" {
		tradeEnv = trading.TradeEnvDemo
	}
	decision := marketScopeDecision{Exchange: exchange, TradeEnv: tradeEnv, Scope: config.DefaultMarketTurnoverScope, Limit: config.MarketTurnoverScopeLimit(config.DefaultMarketTurnoverScope)}
	if s.ConfigStore == nil || s.Orders == nil {
		return decision, fmt.Errorf("market turnover scope dependencies are not configured")
	}
	cfg := s.ConfigStore.Get()
	decision.Scope = cfg.Trading.MarketTurnoverScope
	decision.Limit = config.MarketTurnoverScopeLimit(decision.Scope)
	resp, err := s.cachedSymbolsResponse(cfg)
	if err != nil {
		return decision, err
	}
	candidates := marketSymbolCandidates(signal.Coinpair, signal.Ticker)
	switch exchange {
	case trading.ExchangeBinance:
		set := resp.Binance.Demo
		if tradeEnv == trading.TradeEnvLive {
			set = resp.Binance.Live
		}
		candidateCount, coverageComplete := binanceRankingCoverage(set.Instruments)
		decision.Available = set.SyncedAt != "" && coverageComplete && rankingSizeComplete(candidateCount, len(set.TopInstruments), decision.Limit)
		for _, instrument := range set.TopInstruments {
			base := marketSymbolBase(instrument.BaseAsset, instrument.Symbol)
			if candidates[base] {
				decision.Allowed = true
				decision.Symbol = instrument.Symbol
				break
			}
		}
	case trading.ExchangeOKX:
		set := resp.OKX.Demo
		if tradeEnv == trading.TradeEnvLive {
			set = resp.OKX.Live
		}
		candidateCount, coverageComplete := okxRankingCoverage(set.Instruments)
		decision.Available = set.SyncedAt != "" && coverageComplete && rankingSizeComplete(candidateCount, len(set.TopInstruments), decision.Limit)
		for _, instrument := range set.TopInstruments {
			base := marketSymbolBase(instrument.BaseCcy, instrument.InstID)
			if candidates[base] {
				decision.Allowed = true
				decision.Symbol = instrument.InstID
				break
			}
		}
	case trading.ExchangeGate:
		set := resp.Gate.Demo
		if tradeEnv == trading.TradeEnvLive {
			set = resp.Gate.Live
		}
		candidateCount, coverageComplete := gateRankingCoverage(set.Instruments)
		decision.Available = set.SyncedAt != "" && coverageComplete && rankingSizeComplete(candidateCount, len(set.TopInstruments), decision.Limit)
		for _, instrument := range set.TopInstruments {
			base := marketSymbolBase(instrument.BaseAsset, instrument.Symbol)
			if candidates[base] {
				decision.Allowed = true
				decision.Symbol = instrument.Symbol
				break
			}
		}
	default:
		return decision, fmt.Errorf("unsupported target exchange %q", exchange)
	}
	return decision, nil
}

func marketSymbolCandidates(values ...string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, value := range values {
		keyword, _ := coinpairCooldownIdentity(value)
		if keyword != "" {
			out[keyword] = true
		}
	}
	return out
}

func marketSymbolBase(base, instrument string) string {
	keyword, _ := coinpairCooldownIdentity(base, instrument)
	return keyword
}

func (s *Server) recordMarketScopeIgnoredSignal(signal trading.Signal, decision marketScopeDecision, now time.Time) (storage.OrderRecord, error) {
	return s.Orders.RecordIgnoredReason(signal, marketScopeOutsideCode(decision), marketScopeOutsideMessage(decision), now)
}

func marketScopeIgnoredResponse(record storage.OrderRecord, decision marketScopeDecision) map[string]any {
	return map[string]any{
		"status":          "ignored",
		"reason":          marketScopeOutsideCode(decision),
		"signal_id":       record.SignalID,
		"target_exchange": decision.Exchange,
		"trade_env":       decision.TradeEnv,
		"market_scope":    decision.Scope,
	}
}

func marketScopeLabel(limit int) string {
	if limit > 0 {
		return fmt.Sprintf("turnover top %d", limit)
	}
	return "all eligible turnover"
}

func marketScopeOutsideMessage(decision marketScopeDecision) string {
	return fmt.Sprintf("coinpair is outside %s %s %s scope", decision.Exchange, decision.TradeEnv, marketScopeLabel(decision.Limit))
}

func marketScopeUnavailableMessage(decision marketScopeDecision) string {
	return fmt.Sprintf("%s %s %s ranking is unavailable", decision.Exchange, decision.TradeEnv, marketScopeLabel(decision.Limit))
}

func marketScopeOutsideCode(decision marketScopeDecision) string {
	return "outside_market_" + decision.Scope
}

func marketScopeCheckFailedCode(decision marketScopeDecision) string {
	return decision.Scope + "_check_failed"
}

func marketScopeUnavailableCode(decision marketScopeDecision) string {
	return decision.Scope + "_unavailable"
}

func marketScopeAutoReentryEventType(decision marketScopeDecision) string {
	return "auto_reentry_" + decision.Scope + "_blocked"
}
