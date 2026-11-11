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

func TestOpenWithAlertsDatabaseRoutesAlertHistoryWrites(t *testing.T) {
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "liquidation.db")
	alertsPath := filepath.Join(dir, "alerts.db")
	st, err := OpenWithMarketAndAlertsDatabase(mainPath, "", alertsPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	ctx := context.Background()
	at := time.UnixMilli(1_700_000_000_000).UTC()
	if _, err := st.SaveLiquidationAlert(ctx, alertRow("split-alert", "BTCUSDT", "long", at, domain.AlertStateWatching)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveTradeSignal(ctx, signalRow("split-signal", "BTCUSDT", domain.SignalAggressive, "long", at)); err != nil {
		t.Fatal(err)
	}

	mainDB, err := sql.Open("sqlite", mainPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer mainDB.Close()
	alertsDB, err := sql.Open("sqlite", alertsPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer alertsDB.Close()
	for _, table := range []string{"liquidation_alerts", "trade_signals"} {
		var mainCount, alertsCount int
		if err := mainDB.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&mainCount); err != nil {
			t.Fatal(err)
		}
		if err := alertsDB.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&alertsCount); err != nil {
			t.Fatal(err)
		}
		if mainCount != 0 || alertsCount != 1 {
			t.Fatalf("table %s main=%d alerts=%d", table, mainCount, alertsCount)
		}
	}
	var configTable int
	if err := mainDB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='alert_settings'").Scan(&configTable); err != nil {
		t.Fatal(err)
	}
	if configTable != 1 {
		t.Fatal("alert settings table must remain in the main database")
	}
}
