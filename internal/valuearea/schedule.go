package valuearea

import "time"

const (
	// TradingWindowTimezone is the timezone used by the value-area strategy's
	// daily trading window.
	TradingWindowTimezone = "Asia/Shanghai"
	tradingWindowStart    = 20 * 60
	tradingWindowEnd      = 24 * 60
)

var tradingWindowLocation = time.FixedZone(TradingWindowTimezone, 8*60*60)

// WithinTradingWindow reports whether an automatic value-area signal may be
// sent at at. The window is inclusive at 20:00 and exclusive at midnight,
// using Beijing time regardless of the host timezone.
func WithinTradingWindow(at time.Time) bool {
	if at.IsZero() {
		return false
	}
	local := at.In(tradingWindowLocation)
	minutes := local.Hour()*60 + local.Minute()
	return minutes >= tradingWindowStart && minutes < tradingWindowEnd
}
