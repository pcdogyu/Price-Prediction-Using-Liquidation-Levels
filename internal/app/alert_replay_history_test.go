package app

import (
	"strings"
	"testing"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

func TestReplayedTimelineUnfilteredExcludesGammaIgnoredRows(t *testing.T) {
	cases := []struct {
		name string
		row  replayedTimelineRow
		want bool
	}{
		{
			name: "normal alert",
			row:  replayedTimelineRow{item: domain.AlertTimelineItem{Alert: &domain.LiquidationAlert{FilterApplied: false}}},
			want: true,
		},
		{
			name: "gamma ignored alert",
			row:  replayedTimelineRow{item: domain.AlertTimelineItem{Alert: &domain.LiquidationAlert{FilterApplied: false, GammaIgnored: true}}},
			want: false,
		},
		{
			name: "normal signal",
			row:  replayedTimelineRow{item: domain.AlertTimelineItem{Signal: &domain.TradeSignal{FilterApplied: false}}},
			want: true,
		},
		{
			name: "gamma ignored signal",
			row:  replayedTimelineRow{item: domain.AlertTimelineItem{Signal: &domain.TradeSignal{FilterApplied: false, GammaIgnored: true}}},
			want: false,
		},
		{
			name: "filtered gamma ignored row remains in filtered view",
			row:  replayedTimelineRow{item: domain.AlertTimelineItem{Signal: &domain.TradeSignal{FilterApplied: true, GammaIgnored: true}}},
			want: true,
		},
		{
			name: "gamma ignored row is classified as filtered",
			row:  replayedTimelineRow{item: domain.AlertTimelineItem{Alert: &domain.LiquidationAlert{GammaIgnored: true}}},
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			filter := "unfiltered"
			if strings.HasPrefix(tc.name, "filtered") || tc.name == "gamma ignored row is classified as filtered" {
				filter = "filtered"
			}
			if got := replayedTimelineFilterMatch(tc.row, filter); got != tc.want {
				t.Fatalf("filter match = %v, want %v", got, tc.want)
			}
		})
	}
}
