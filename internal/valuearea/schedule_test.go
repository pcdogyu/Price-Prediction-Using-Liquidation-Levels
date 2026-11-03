package valuearea

import (
	"testing"
	"time"
)

func TestWithinTradingWindowUsesBeijingBoundaries(t *testing.T) {
	beijing := tradingWindowLocation
	cases := []struct {
		name string
		at   time.Time
		want bool
	}{
		{name: "before start", at: time.Date(2026, time.November, 2, 19, 59, 59, 0, beijing), want: false},
		{name: "start", at: time.Date(2026, time.November, 2, 20, 0, 0, 0, beijing), want: true},
		{name: "inside", at: time.Date(2026, time.November, 2, 23, 59, 59, 0, beijing), want: true},
		{name: "midnight", at: time.Date(2026, time.November, 3, 0, 0, 0, 0, beijing), want: false},
		{name: "zero", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := WithinTradingWindow(tc.at); got != tc.want {
				t.Fatalf("WithinTradingWindow(%s) = %v, want %v", tc.at, got, tc.want)
			}
		})
	}
}

func TestWithinTradingWindowConvertsFromUTC(t *testing.T) {
	if !WithinTradingWindow(time.Date(2026, time.November, 2, 12, 0, 0, 0, time.UTC)) {
		t.Fatal("12:00 UTC should be 20:00 Beijing time")
	}
	if WithinTradingWindow(time.Date(2026, time.November, 2, 16, 0, 0, 0, time.UTC)) {
		t.Fatal("16:00 UTC should be the Beijing midnight boundary")
	}
}

func TestTradingWindowSupportsCustomAndCrossMidnightIntervals(t *testing.T) {
	beijing := tradingWindowLocation
	cases := []struct {
		name   string
		window TradingWindow
		at     time.Time
		want   bool
	}{
		{name: "custom start", window: TradingWindow{StartMinute: 18 * 60, EndMinute: 22 * 60}, at: time.Date(2026, time.November, 2, 18, 0, 0, 0, beijing), want: true},
		{name: "custom end", window: TradingWindow{StartMinute: 18 * 60, EndMinute: 22 * 60}, at: time.Date(2026, time.November, 2, 22, 0, 0, 0, beijing), want: false},
		{name: "cross midnight late", window: TradingWindow{StartMinute: 22 * 60, EndMinute: 2 * 60}, at: time.Date(2026, time.November, 2, 23, 0, 0, 0, beijing), want: true},
		{name: "cross midnight early", window: TradingWindow{StartMinute: 22 * 60, EndMinute: 2 * 60}, at: time.Date(2026, time.November, 3, 1, 59, 0, 0, beijing), want: true},
		{name: "cross midnight outside", window: TradingWindow{StartMinute: 22 * 60, EndMinute: 2 * 60}, at: time.Date(2026, time.November, 3, 2, 0, 0, 0, beijing), want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.window.Contains(tc.at); got != tc.want {
				t.Fatalf("TradingWindow.Contains(%s) = %v, want %v", tc.at, got, tc.want)
			}
		})
	}
}
