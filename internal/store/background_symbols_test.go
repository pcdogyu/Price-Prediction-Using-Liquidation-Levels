package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

func TestBackgroundSymbolSnapshotRoundTrip(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	want := domain.BackgroundSymbolSnapshot{
		Symbols:          []domain.WatchSymbol{{Symbol: "BTCUSDT", Rank: 1, QuoteVolumeUSD: 123}},
		TenantCount:      2,
		BaseCount:        1,
		ExceptionSymbols: []string{"ETHUSDT"},
		RefreshedAt:      time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC),
	}
	if err := st.SaveBackgroundSymbolSnapshot(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	got, found, err := st.LoadBackgroundSymbolSnapshot(context.Background())
	if err != nil || !found {
		t.Fatalf("snapshot=%+v found=%v err=%v", got, found, err)
	}
	if got.TenantCount != want.TenantCount || got.BaseCount != want.BaseCount || len(got.Symbols) != 1 || got.Symbols[0].Symbol != "BTCUSDT" || got.ExceptionSymbols[0] != "ETHUSDT" || !got.RefreshedAt.Equal(want.RefreshedAt) {
		t.Fatalf("snapshot=%+v, want %+v", got, want)
	}
}
