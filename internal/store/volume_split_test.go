package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
	_ "modernc.org/sqlite"
)

func TestOpenWithVolumeDatabaseRoutesProfileWrites(t *testing.T) {
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "liquidation.db")
	volumePath := filepath.Join(dir, "volume.db")
	st, err := OpenWithMarketAndAlertsAndOptionsAndVolumeDatabase(mainPath, "", "", "", volumePath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	ctx := context.Background()
	start := time.UnixMilli(1_700_000_000_000).UTC()
	trades := []domain.AggregateTrade{{ID: 1, Symbol: "BTCUSDT", Time: start.Add(time.Minute), Price: 100, PriceText: "100", Quantity: 2}}
	if err := st.ApplyAggregateTrades(ctx, "BTCUSDT", start, trades, true); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveVolumeProfileSnapshot(ctx, domain.VolumeProfileSnapshot{Symbol: "BTCUSDT", Time: start.Add(time.Minute), SessionStart: start, VAL: 99, VAH: 101, TotalVolumeUSD: 200, Complete: true}); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkArchiveImport(ctx, "BTCUSDT", start, "complete", ""); err != nil {
		t.Fatal(err)
	}

	mainDB, err := sql.Open("sqlite", mainPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer mainDB.Close()
	volumeDB, err := sql.Open("sqlite", volumePath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer volumeDB.Close()
	for _, table := range []string{"volume_profile_levels", "volume_profile_cursors", "volume_profile_snapshots", "volume_archive_imports"} {
		var mainCount, volumeCount int
		if err := mainDB.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&mainCount); err != nil {
			t.Fatal(err)
		}
		if err := volumeDB.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&volumeCount); err != nil {
			t.Fatal(err)
		}
		if mainCount != 0 || volumeCount != 1 {
			t.Fatalf("table %s main=%d volume=%d", table, mainCount, volumeCount)
		}
	}
	var authTables int
	if err := mainDB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='auth_users'").Scan(&authTables); err != nil {
		t.Fatal(err)
	}
	if authTables != 1 {
		t.Fatal("unified database schema must remain available")
	}
}
