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
	if err := st.ReplaceWatchSymbols(ctx, []domain.WatchSymbol{{Symbol: "BTCUSDT", Rank: 1, QuoteVolumeUSD: 100, UpdatedAt: at}}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveAlertSettings(ctx, domain.NotificationSettings{TelegramToken: "split-token", TelegramChatID: "split-chat", NotifyAlerts: true}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveAlertStrategySettings(ctx, domain.AlertStrategySettings{WindowMinutes: 5, BTCUSD: 300000}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveAlertSettingsForTenant(ctx, "alice", domain.NotificationSettings{TelegramToken: "tenant-token", NotifyAggressive: true}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveAlertStrategySettingsForTenant(ctx, "alice", domain.AlertStrategySettings{WindowMinutes: 10, ETHUSD: 200000}); err != nil {
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
	for _, table := range []string{"liquidation_alerts", "trade_signals", "alert_watch_symbols", "alert_settings", "alert_strategy_settings", "alert_tenant_settings", "alert_tenant_strategy_settings"} {
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
	var authTable int
	if err := mainDB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='auth_users'").Scan(&authTable); err != nil {
		t.Fatal(err)
	}
	if authTable != 1 {
		t.Fatal("authentication schema must remain in the main database")
	}
}
