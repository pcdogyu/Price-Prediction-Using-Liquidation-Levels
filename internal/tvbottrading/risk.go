package trading

import (
	"fmt"
	"math"
)

// FixedTPSLTriggerPrices converts the configured margin-return percentages
// into exchange price triggers. A leveraged position earns or loses roughly
// leverage times the underlying price move, so the price distance must be
// divided by the actual leverage used for the order.
func FixedTPSLTriggerPrices(action Side, entryPrice, takeProfitPct, stopLossPct float64, leverage int) (float64, float64, error) {
	if entryPrice <= 0 || math.IsNaN(entryPrice) || math.IsInf(entryPrice, 0) {
		return 0, 0, fmt.Errorf("entry price must be positive")
	}
	if leverage <= 0 {
		return 0, 0, fmt.Errorf("leverage must be positive")
	}
	if takeProfitPct <= 0 || math.IsNaN(takeProfitPct) || math.IsInf(takeProfitPct, 0) {
		return 0, 0, fmt.Errorf("take profit percentage must be positive")
	}
	if stopLossPct <= 0 || math.IsNaN(stopLossPct) || math.IsInf(stopLossPct, 0) {
		return 0, 0, fmt.Errorf("stop loss percentage must be positive")
	}
	takeProfitMove := takeProfitPct / float64(leverage) / 100
	stopLossMove := stopLossPct / float64(leverage) / 100
	switch action {
	case ActionLong:
		return entryPrice * (1 + takeProfitMove), entryPrice * (1 - stopLossMove), nil
	case ActionShort:
		return entryPrice * (1 - takeProfitMove), entryPrice * (1 + stopLossMove), nil
	default:
		return 0, 0, fmt.Errorf("unsupported action %q", action)
	}
}
