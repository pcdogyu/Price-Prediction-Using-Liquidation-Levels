package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

func TestGammaFlipHistoryPersistsAndExpires(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "gamma-flip.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	flip := 65000.0
	for _, at := range []time.Time{now.Add(-31 * 24 * time.Hour), now.Add(-5 * time.Minute), now} {
		view := domain.GammaView{Symbol: "BTCUSDT", State: "ok", Time: at, SpotPrice: 64000, NetGEXUSD: 12.5, GammaFlip: &flip, FlipState: "ok", Contracts: 10, ExpectedContracts: 10}
		if err := st.SaveGamma(ctx, view); err != nil {
			t.Fatal(err)
		}
	}
	points, err := st.GammaFlipHistory(ctx, "btcusdt", now.Add(-24*time.Hour), now.Add(time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 2 || points[0].GammaFlip == nil || *points[0].GammaFlip != flip || points[0].GammaValue != 12.5 || points[0].SpotPrice != 64000 {
		t.Fatalf("unexpected Gamma Flip history: %+v", points)
	}
	removed, err := st.PruneGammaFlipHistory(ctx, now)
	if err != nil || removed != 1 {
		t.Fatalf("pruned=%d err=%v, want one expired point", removed, err)
	}
}
