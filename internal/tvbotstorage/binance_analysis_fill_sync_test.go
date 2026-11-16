package storage

import (
	"path/filepath"
	"testing"
	"time"
)

func TestBinanceAnalysisFillSyncStatePersistsAndNormalizesKey(t *testing.T) {
	store, err := NewSQLiteOrderStore(filepath.Join(t.TempDir(), "tvbot.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if _, found, err := store.LoadBinanceAnalysisFillSyncState("main", "ethusdt"); err != nil || found {
		t.Fatalf("new sync state should not exist, found=%v err=%v", found, err)
	}
	updatedAt := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	want := BinanceAnalysisFillSyncState{
		APIID: " main ", Symbol: "ethusdt", CoveredSinceMS: 100, LastSyncedMS: 200, UpdatedAt: updatedAt,
	}
	if err := store.SaveBinanceAnalysisFillSyncState(want); err != nil {
		t.Fatal(err)
	}
	got, found, err := store.LoadBinanceAnalysisFillSyncState("main", "ETHUSDT")
	if err != nil {
		t.Fatal(err)
	}
	if !found || got.APIID != "main" || got.Symbol != "ETHUSDT" || got.CoveredSinceMS != 100 || got.LastSyncedMS != 200 || !got.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("unexpected persisted sync state: %#v found=%v", got, found)
	}
}
