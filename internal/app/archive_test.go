package app

import (
	"context"
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

func TestArchiveSnapshotsDoNotUseFutureTrades(t *testing.T) {
	day := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	var snapshots []domain.VolumeProfileSnapshot
	accumulator := newArchiveAccumulator("BTCUSDT", day, func(_ context.Context, snapshot domain.VolumeProfileSnapshot) error {
		snapshots = append(snapshots, snapshot)
		return nil
	})
	ctx := context.Background()
	if err := accumulator.Add(ctx, domain.AggregateTrade{ID: 1, Symbol: "BTCUSDT", Time: day.Add(time.Minute), Price: 100, Quantity: 1}); err != nil {
		t.Fatal(err)
	}
	if err := accumulator.Add(ctx, domain.AggregateTrade{ID: 2, Symbol: "BTCUSDT", Time: day.Add(5 * time.Minute), Price: 200, Quantity: 100}); err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 1 || !snapshots[0].Time.Equal(day.Add(5*time.Minute)) || snapshots[0].VAH >= 150 {
		t.Fatalf("first snapshot contains future trade: %#v", snapshots)
	}
}

func TestNextVolumeArchiveNight(t *testing.T) {
	china := time.FixedZone("Beijing", 8*60*60)
	for _, tc := range []struct{ now, want time.Time }{
		{time.Date(2026, 11, 28, 21, 59, 0, 0, china), time.Date(2026, 11, 28, 22, 0, 0, 0, china)},
		{time.Date(2026, 11, 28, 22, 0, 0, 0, china), time.Date(2026, 11, 29, 22, 0, 0, 0, china)},
		{time.Date(2026, 11, 28, 23, 0, 0, 0, china), time.Date(2026, 11, 29, 22, 0, 0, 0, china)},
	} {
		if got := nextVolumeArchiveNight(tc.now.UTC()); !got.Equal(tc.want) {
			t.Fatalf("now=%s got=%s want=%s", tc.now, got, tc.want)
		}
	}
}
