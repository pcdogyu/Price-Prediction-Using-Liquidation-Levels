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
