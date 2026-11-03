package valuearea

import "time"

const (
	// TradingWindowTimezone is the timezone used by the value-area strategy's
	// daily trading window.
	TradingWindowTimezone = "Asia/Shanghai"
	// DefaultTradingWindowStartMinute and DefaultTradingWindowEndMinute keep
	// the original 20:00–24:00 Beijing schedule for existing deployments.
	DefaultTradingWindowStartMinute = 20 * 60
	DefaultTradingWindowEndMinute   = 24 * 60
)

var tradingWindowLocation = time.FixedZone(TradingWindowTimezone, 8*60*60)

// TradingWindow is a daily Beijing-time interval. EndMinute may be 1440 for
// midnight, and an end before the start represents a window crossing midnight.
type TradingWindow struct {
	StartMinute int
	EndMinute   int
}

// DefaultTradingWindow returns the deployment-compatible value-area schedule.
func DefaultTradingWindow() TradingWindow {
	return TradingWindow{StartMinute: DefaultTradingWindowStartMinute, EndMinute: DefaultTradingWindowEndMinute}
}

// Valid reports whether the interval can be evaluated without ambiguity.
func (w TradingWindow) Valid() bool {
	return w.StartMinute >= 0 && w.StartMinute < 24*60 && w.EndMinute > 0 && w.EndMinute <= 24*60 && w.StartMinute != w.EndMinute
}

// Contains reports whether an instant falls inside the interval. The start is
// inclusive and the end is exclusive, using Beijing time regardless of the
// host timezone.
func (w TradingWindow) Contains(at time.Time) bool {
	if at.IsZero() || !w.Valid() {
		return false
	}
	local := at.In(tradingWindowLocation)
	minutes := local.Hour()*60 + local.Minute()
	if w.StartMinute < w.EndMinute {
		return minutes >= w.StartMinute && minutes < w.EndMinute
	}
	return minutes >= w.StartMinute || minutes < w.EndMinute
}

// WithinTradingWindow reports whether an automatic value-area signal may be
// sent at at. The window is inclusive at 20:00 and exclusive at midnight,
// using Beijing time regardless of the host timezone.
func WithinTradingWindow(at time.Time) bool {
	return DefaultTradingWindow().Contains(at)
}
