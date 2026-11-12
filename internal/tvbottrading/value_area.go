package trading

import "strings"

// IsValueAreaOpeningSignal reports whether a signal is an opening request from
// the value-area strategy. Both automatic refresh and manual value-area
// openings use the same exchange-side reverse-position behavior.
func IsValueAreaOpeningSignal(signal Signal) bool {
	if signal.PositionEffect != PositionEffectOpen {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(signal.Condition)) {
	case "value_area_signal", "value_area_manual":
		return true
	default:
		return false
	}
}
