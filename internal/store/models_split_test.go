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

func TestOpenWithModelsDatabaseRoutesPredictionAndBacktestWrites(t *testing.T) {
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "liquidation.db")
	modelsPath := filepath.Join(dir, "models.db")
	st, err := OpenWithMarketAndAlertsAndOptionsAndVolumeAndModelsDatabase(mainPath, "", "", "", "", modelsPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	ctx := context.Background()
	when := time.UnixMilli(1_700_000_000_000).UTC()
	if err := st.SavePrediction(ctx, domain.Prediction{Symbol: "BTCUSDT", Time: when, State: "ok", MarkPrice: 101}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveBacktest(ctx, domain.BacktestReport{Symbol: "BTCUSDT", Samples: 10, GeneratedAt: when}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.LatestPrediction(ctx, "BTCUSDT"); err != nil {
		t.Fatalf("latest prediction: %v", err)
	}
	if _, err := st.Backtest(ctx, "BTCUSDT"); err != nil {
		t.Fatalf("backtest: %v", err)
	}

	mainDB, err := sql.Open("sqlite", mainPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer mainDB.Close()
	modelsDB, err := sql.Open("sqlite", modelsPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer modelsDB.Close()
	for _, table := range []string{"predictions", "backtests"} {
		var mainCount, modelsCount int
		if err := mainDB.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&mainCount); err != nil {
			t.Fatal(err)
		}
		if err := modelsDB.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&modelsCount); err != nil {
			t.Fatal(err)
		}
		if mainCount != 0 || modelsCount != 1 {
			t.Fatalf("table %s main=%d models=%d", table, mainCount, modelsCount)
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
