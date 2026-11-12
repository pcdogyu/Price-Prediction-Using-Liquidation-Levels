package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/authn"
	_ "modernc.org/sqlite"
)

func TestOpenWithAuthDatabaseRoutesAuthState(t *testing.T) {
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "liquidation.db")
	settingsPath := filepath.Join(dir, "settings.db")
	authPath := filepath.Join(dir, "auth.db")
	st, err := OpenWithMarketAndAlertsAndOptionsAndVolumeAndModelsAndSettingsAndAuthDatabase(mainPath, "", "", "", "", "", settingsPath, authPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	ctx := context.Background()
	if err := st.CreateAuthUser(ctx, "alice", "hash", "admin"); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveAuthSessionRecord(ctx, authn.RichSessionRecord{TokenHash: "token-hash", Username: "alice", Expires: time.Now().Add(time.Hour).UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordAuthAudit(ctx, "alice", "login", "alice"); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveUserTheme(ctx, "alice", "dark"); err != nil {
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
	authDB, err := sql.Open("sqlite", authPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer authDB.Close()
	settingsDB, err := sql.Open("sqlite", settingsPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer settingsDB.Close()
	wantAuthCounts := map[string]int{"auth_sessions": 1, "auth_users": 0, "auth_audit": 1}
	for _, table := range []string{"auth_sessions", "auth_users", "auth_audit"} {
		var mainCount, authCount int
		if err := mainDB.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&mainCount); err != nil {
			t.Fatal(err)
		}
		if err := authDB.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&authCount); err != nil {
			t.Fatal(err)
		}
		if mainCount != 0 || authCount != wantAuthCounts[table] {
			t.Fatalf("table %s main=%d auth=%d want=%d", table, mainCount, authCount, wantAuthCounts[table])
		}
	}
	var themeCount int
	if err := settingsDB.QueryRow("SELECT COUNT(*) FROM user_theme").Scan(&themeCount); err != nil {
		t.Fatal(err)
	}
	if themeCount != 0 {
		t.Fatalf("deleted user theme count=%d", themeCount)
	}
	var settingsUsers int
	if err := authDB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='user_theme'").Scan(&settingsUsers); err != nil {
		t.Fatal(err)
	}
	if settingsUsers != 0 {
		t.Fatal("settings tables must remain outside the auth database")
	}
}
