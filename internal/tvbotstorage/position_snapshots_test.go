package storage

import (
	"path/filepath"
	"testing"
	"time"
)

func TestUSDTPositionSnapshotIsolationAndUpsert(t *testing.T) {
	store, err := NewSQLiteOrderStore(filepath.Join(t.TempDir(), "orders.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	at := time.Date(2026, 11, 18, 4, 0, 0, 0, time.UTC)
	base := USDTPositionSnapshot{
		Exchange: "Binance", APIID: "main", Env: "demo", ObservedAt: at,
		Status: "ok", PayloadJSON: `[{"symbol":"BTCUSDT"}]`,
	}
	if err := store.UpsertUSDTPositionSnapshot(base); err != nil {
		t.Fatal(err)
	}
	base.Status = "unavailable"
	base.PayloadJSON = "[]"
	if err := store.UpsertUSDTPositionSnapshot(base); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetUSDTPositionSnapshot("binance", "main", "demo", at.Truncate(time.Minute).UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "unavailable" || got.PayloadJSON != "[]" {
		t.Fatalf("same bucket should be replaced, got %+v", got)
	}
	if _, err := store.GetUSDTPositionSnapshot("binance", "main", "live", got.BucketTS); !IsMissingUSDTPositionSnapshot(err) {
		t.Fatalf("environment must be isolated, got err=%v", err)
	}
}

func TestFindExchangeUSDTBalancePeakWindowAndLifetime(t *testing.T) {
	store, err := NewSQLiteOrderStore(filepath.Join(t.TempDir(), "orders.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	base := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	for i, equity := range []string{"100", "200", "200", "150"} {
		at := base.Add(time.Duration(i) * time.Minute)
		if err := store.UpsertUSDTBalanceSnapshot(USDTBalanceSnapshot{
			Exchange: "binance", APIID: "main", Env: "demo", ObservedAt: at, Eq: equity,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.UpsertUSDTBalanceSnapshot(USDTBalanceSnapshot{
		Exchange: "binance", APIID: "main", Env: "live", ObservedAt: base.Add(time.Minute), Eq: "900",
	}); err != nil {
		t.Fatal(err)
	}
	peak, err := store.FindExchangeUSDTBalancePeak("binance", "main", "demo", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if peak.Eq != "200" || peak.BucketTS != base.Add(time.Minute).UnixMilli() {
		t.Fatalf("lifetime peak should choose first equal maximum, got %+v", peak)
	}
	windowPeak, err := store.FindExchangeUSDTBalancePeak("binance", "main", "demo", base.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if windowPeak.Eq != "200" || windowPeak.BucketTS != base.Add(2*time.Minute).UnixMilli() {
		t.Fatalf("window peak should honor cutoff, got %+v", windowPeak)
	}
}
