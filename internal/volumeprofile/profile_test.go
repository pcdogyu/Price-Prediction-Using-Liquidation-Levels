package volumeprofile

import (
	"math"
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

func TestSessionBoundariesAndDST(t *testing.T) {
	cases := []struct {
		name, now, start, end, kind string
	}{
		{"summer morning", "2026-07-01T01:00:00Z", "2026-07-01T00:00:00Z", "2026-07-01T13:30:00Z", "shanghai_0800"},
		{"summer open", "2026-07-01T13:30:00Z", "2026-07-01T13:30:00Z", "2026-07-02T00:00:00Z", "new_york_0930"},
		{"winter morning", "2026-01-05T01:00:00Z", "2026-01-05T00:00:00Z", "2026-01-05T14:30:00Z", "shanghai_0800"},
		{"winter open", "2026-01-05T14:30:00Z", "2026-01-05T14:30:00Z", "2026-01-06T00:00:00Z", "new_york_0930"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			now, _ := time.Parse(time.RFC3339, tc.now)
			wantStart, _ := time.Parse(time.RFC3339, tc.start)
			wantEnd, _ := time.Parse(time.RFC3339, tc.end)
			got, err := SessionAt(now)
			if err != nil || !got.Start.Equal(wantStart) || !got.End.Equal(wantEnd) || got.ResetKind != tc.kind {
				t.Fatalf("session=%+v error=%v", got, err)
			}
		})
	}
}

func TestBuildTwentyFourBinsValueAreaAndTopThree(t *testing.T) {
	start := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	session := Session{Start: start, End: start.Add(13*time.Hour + 30*time.Minute), ResetKind: "shanghai_0800"}
	levels := []Level{{Price: 100, VolumeUSD: 10}, {Price: 101, VolumeUSD: 20}, {Price: 102, VolumeUSD: 70}, {Price: 103, VolumeUSD: 20}, {Price: 104, VolumeUSD: 10}}
	p := Build("BTCUSDT", session, levels, start.Add(time.Hour), start.Add(time.Hour), true)
	if p.Rows != 24 || len(p.Bins) != 24 || p.ValueAreaFraction != .69 || p.VAL == nil || p.VAH == nil || *p.VAL > *p.VAH {
		t.Fatalf("profile=%+v", p)
	}
	inside := 0.0
	ranks := map[int]float64{}
	for _, bin := range p.Bins {
		if bin.InValueArea {
			inside += bin.VolumeUSD
		}
		if bin.VolumeRank > 0 {
			ranks[bin.VolumeRank] = bin.VolumeUSD
		}
	}
	if inside+1e-9 < p.TotalVolumeUSD*.69 {
		t.Fatalf("value area volume=%v total=%v", inside, p.TotalVolumeUSD)
	}
	if len(ranks) != 3 || ranks[1] != 70 || ranks[2] != 20 || ranks[3] != 20 {
		t.Fatalf("top ranks=%v", ranks)
	}
}

func TestBuildValueAreaUsesSixtyNinePercentOfTotalVolume(t *testing.T) {
	start := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	session := Session{Start: start, End: start.Add(13*time.Hour + 30*time.Minute), ResetKind: "shanghai_0800"}
	profile := Build("BTCUSDT", session, []Level{
		{Price: 100, VolumeUSD: 15.5},
		{Price: 101, VolumeUSD: 68.5},
		{Price: 102, VolumeUSD: 16},
	}, start.Add(time.Hour), start.Add(time.Hour), true)
	if profile.TotalVolumeUSD != 100 || profile.ValueAreaFraction != .69 {
		t.Fatalf("total volume/fraction = %v/%v, want 100/.69", profile.TotalVolumeUSD, profile.ValueAreaFraction)
	}
	var inAreaVolume float64
	var inAreaBins int
	for _, bin := range profile.Bins {
		if bin.InValueArea {
			inAreaVolume += bin.VolumeUSD
			inAreaBins++
		}
	}
	if inAreaVolume != 84.5 || inAreaBins <= 1 || profile.VAL == nil || profile.VAH == nil || *profile.VAL > 101 || *profile.VAH < 101 {
		t.Fatalf("69%% target should expand beyond the 68.5%% POC: volume=%v bins=%d VAL=%v VAH=%v", inAreaVolume, inAreaBins, profile.VAL, profile.VAH)
	}
}

func TestBuildSinglePriceAndIncomplete(t *testing.T) {
	start := time.Now().UTC().Add(-time.Hour)
	session := Session{Start: start, End: start.Add(2 * time.Hour), ResetKind: "test"}
	complete := Build("ETHUSDT", session, []Level{{Price: 100, VolumeUSD: 25}}, time.Now().UTC(), time.Now().UTC(), true)
	if complete.Rows != 24 || len(complete.Bins) != 1 || complete.Bins[0].VolumeRank != 1 || complete.VAL == nil || complete.VAH == nil || math.Abs(complete.TotalVolumeUSD-25) > 1e-9 {
		t.Fatalf("complete=%+v", complete)
	}
	incomplete := Build("ETHUSDT", session, []Level{{Price: 100, VolumeUSD: 25}}, start.Add(30*time.Minute), time.Now().UTC(), false)
	if incomplete.State != "backfilling" || incomplete.VAL == nil || incomplete.VAH == nil || !incomplete.Bins[0].InValueArea {
		t.Fatalf("incomplete=%+v", incomplete)
	}
}

func TestValueAreaExpandsBothEqualAdjacentRows(t *testing.T) {
	bins := []domain.VolumeProfileBin{{VolumeUSD: 20}, {VolumeUSD: 60}, {VolumeUSD: 20}}
	low, high := valueArea(bins, 69)
	if low != 0 || high != 2 {
		t.Fatalf("equal adjacent rows must expand together, got [%d,%d]", low, high)
	}
}
