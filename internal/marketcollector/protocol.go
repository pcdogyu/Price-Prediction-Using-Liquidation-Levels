package marketcollector

import (
	"sort"
	"strings"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

const (
	requestSubscribe = "subscribe"
	eventLiquidation = "liquidation"
	eventAggregate   = "aggregate_trade"
	eventBook        = "book"
)

// Subscription describes the live feeds a consumer needs. The collector owns
// exchange connections; consumers only change this small local subscription.
type Subscription struct {
	Liquidations     bool     `json:"liquidations"`
	AggregateSymbols []string `json:"aggregate_symbols"`
	DepthSymbols     []string `json:"depth_symbols"`
}

type request struct {
	Type         string       `json:"type"`
	Subscription Subscription `json:"subscription"`
}

type Event struct {
	Type           string                   `json:"type"`
	Symbol         string                   `json:"symbol,omitempty"`
	Liquidation    *domain.LiquidationEvent `json:"liquidation,omitempty"`
	AggregateTrade *domain.AggregateTrade   `json:"aggregate_trade,omitempty"`
	Book           *domain.BookSnapshot     `json:"book,omitempty"`
}

func (s Subscription) normalized() Subscription {
	return Subscription{
		Liquidations:     s.Liquidations,
		AggregateSymbols: normalizeSymbols(s.AggregateSymbols),
		DepthSymbols:     normalizeSymbols(s.DepthSymbols),
	}
}

func normalizeSymbols(symbols []string) []string {
	seen := make(map[string]struct{}, len(symbols))
	out := make([]string, 0, len(symbols))
	for _, raw := range symbols {
		symbol := strings.ToUpper(strings.TrimSpace(raw))
		if symbol == "" || symbol == "USDT" || !strings.HasSuffix(symbol, "USDT") {
			continue
		}
		valid := true
		for _, r := range symbol {
			if (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
				valid = false
				break
			}
		}
		if valid {
			if _, ok := seen[symbol]; !ok {
				seen[symbol] = struct{}{}
				out = append(out, symbol)
			}
		}
	}
	sort.Strings(out)
	return out
}

func containsSymbol(symbol string, symbols []string) bool {
	for _, candidate := range symbols {
		if candidate == symbol {
			return true
		}
	}
	return false
}
