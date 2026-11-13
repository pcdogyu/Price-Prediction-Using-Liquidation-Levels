package storage

import (
	"path/filepath"
	"testing"
	"time"
)

func TestBinanceIncomeStorageUpsertsAndTracksSyncState(t *testing.T) {
	store, err := NewSQLiteOrderStore(filepath.Join(t.TempDir(), "orders.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	base := time.Date(2026, 11, 13, 3, 0, 0, 0, time.UTC)
	rows := []BinanceIncomeRecord{
		{IncomeKey: "REALIZED_PNL|1", Symbol: "BTCUSDT", IncomeType: "REALIZED_PNL", Income: "2.5", Asset: "USDT", TranID: "1", Time: base.UnixMilli()},
		{IncomeKey: "COMMISSION|2", Symbol: "BTCUSDT", IncomeType: "COMMISSION", Income: "-0.1", Asset: "USDT", TranID: "2", Time: base.Add(time.Second).UnixMilli()},
	}
	state := BinanceIncomeSyncState{CoveredSinceMS: base.Add(-time.Hour).UnixMilli(), LastSyncedMS: base.UnixMilli(), UpdatedAt: base}
	if err := store.SaveBinanceIncomeSync("binance-demo", "demo", rows, state, base); err != nil {
		t.Fatal(err)
	}
	// Replaying an overlapping record updates the same key instead of duplicating it.
	rows[0].Income = "3.0"
	state.LastSyncedMS = base.Add(time.Minute).UnixMilli()
	if err := store.SaveBinanceIncomeSync("binance-demo", "demo", rows[:1], state, base.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, err := store.ListBinanceIncome("binance-demo", "demo", base.Add(-time.Hour), base.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].IncomeKey != "COMMISSION|2" || got[1].Income != "3.0" {
		t.Fatalf("income upsert/order mismatch: %#v", got)
	}
	gotState, found, err := store.LoadBinanceIncomeSyncState("binance-demo", "demo")
	if err != nil || !found || gotState.CoveredSinceMS != state.CoveredSinceMS || gotState.LastSyncedMS != state.LastSyncedMS {
		t.Fatalf("sync state mismatch: %#v found=%v err=%v", gotState, found, err)
	}
}
