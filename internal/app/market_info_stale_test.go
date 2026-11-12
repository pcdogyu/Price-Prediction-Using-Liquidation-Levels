package app

import (
	"testing"
	"time"
)

func TestMarketInfoIntervalUsesDefault(t *testing.T) {
	if got := marketInfoInterval(0); got != 5*time.Minute {
		t.Fatalf("default market info interval = %s, want 5m", got)
	}
	if got := marketInfoInterval(-time.Second); got != 5*time.Minute {
		t.Fatalf("negative market info interval = %s, want 5m", got)
	}
}

func TestMarketMetricStaleAfterFollowsCollectionInterval(t *testing.T) {
	tests := []struct {
		name     string
		interval time.Duration
		want     time.Duration
	}{
		{name: "default", interval: 0, want: 7 * time.Minute},
		{name: "configured five minutes", interval: 5 * time.Minute, want: 7 * time.Minute},
		{name: "configured one minute", interval: time.Minute, want: 3 * time.Minute},
		{name: "configured ten minutes", interval: 10 * time.Minute, want: 12 * time.Minute},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := marketMetricStaleAfter(tt.interval); got != tt.want {
				t.Fatalf("stale after = %s, want %s", got, tt.want)
			}
		})
	}
}
