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

func TestOpenWithSettingsDatabaseRoutesUIAndBackgroundState(t *testing.T) {
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "liquidation.db")
	settingsPath := filepath.Join(dir, "settings.db")
	st, err := OpenWithMarketAndAlertsAndOptionsAndVolumeAndModelsAndSettingsDatabase(mainPath, "", "", "", "", "", settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	ctx := context.Background()
	if err := st.SaveAdminMenu(ctx, []byte(`[{"key":"dashboard"}]`)); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveUserMenu(ctx, "alice", []byte(`[{"key":"alerts"}]`)); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveUserTheme(ctx, "alice", "dark"); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveBackgroundSymbolSnapshot(ctx, domain.BackgroundSymbolSnapshot{Symbols: []domain.WatchSymbol{{Symbol: "BTCUSDT", Rank: 1}}, TenantCount: 1, BaseCount: 1, RefreshedAt: time.UnixMilli(1_700_000_000_000).UTC()}); err != nil {
		t.Fatal(err)
	}
	if _, found, err := st.LoadAdminMenu(ctx); err != nil || !found {
		t.Fatalf("admin menu found=%v err=%v", found, err)
	}
	if _, found, err := st.LoadUserMenu(ctx, "alice"); err != nil || !found {
		t.Fatalf("user menu found=%v err=%v", found, err)
	}
	if theme, found, err := st.LoadUserTheme(ctx, "alice"); err != nil || !found || theme != "dark" {
		t.Fatalf("theme=%q found=%v err=%v", theme, found, err)
	}
	if _, found, err := st.LoadBackgroundSymbolSnapshot(ctx); err != nil || !found {
		t.Fatalf("background snapshot found=%v err=%v", found, err)
	}
	if err := st.CreateAuthUser(ctx, "alice", "hash", "user"); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteAuthUser(ctx, "alice"); err != nil {
		t.Fatal(err)
	}

	mainDB, err := sql.Open("sqlite", mainPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer mainDB.Close()
	settingsDB, err := sql.Open("sqlite", settingsPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer settingsDB.Close()
	for _, table := range []string{"admin_menu", "user_menu", "background_symbol_snapshot"} {
		var mainCount, settingsCount int
		if err := mainDB.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&mainCount); err != nil {
			t.Fatal(err)
		}
		if err := settingsDB.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&settingsCount); err != nil {
			t.Fatal(err)
		}
		if mainCount != 0 || settingsCount != 1 {
			t.Fatalf("table %s main=%d settings=%d", table, mainCount, settingsCount)
		}
	}
	var themeCount int
	if err := settingsDB.QueryRow("SELECT COUNT(*) FROM user_theme").Scan(&themeCount); err != nil {
		t.Fatal(err)
	}
	if themeCount != 0 {
		t.Fatalf("deleted user theme count=%d", themeCount)
	}
	var authTables int
	if err := mainDB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='auth_users'").Scan(&authTables); err != nil {
		t.Fatal(err)
	}
	if authTables != 1 {
		t.Fatal("authentication schema must remain in the main database")
	}
}
