package alertsmigration

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestMigrateUnifiedSQLiteCopiesOnlyAlertHistory(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "liquidation.db")
	targetPath := filepath.Join(dir, "alerts.db")
	source, err := sql.Open("sqlite", sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = source.Exec(`
CREATE TABLE liquidation_alerts(id TEXT PRIMARY KEY,symbol TEXT NOT NULL,side TEXT NOT NULL,trigger_ts INTEGER NOT NULL,status TEXT NOT NULL,payload BLOB NOT NULL,updated_ts INTEGER NOT NULL);
CREATE TABLE trade_signals(id TEXT PRIMARY KEY,symbol TEXT NOT NULL,kind TEXT NOT NULL,side TEXT NOT NULL,alert_id TEXT NOT NULL,created_ts INTEGER NOT NULL,notified INTEGER NOT NULL,payload BLOB NOT NULL,updated_ts INTEGER NOT NULL);
CREATE TABLE alert_settings(id INTEGER PRIMARY KEY,telegram_token TEXT NOT NULL);
INSERT INTO liquidation_alerts VALUES('alert-1','BTCUSDT','long',100,'watching','{}',101);
INSERT INTO trade_signals VALUES('signal-1','BTCUSDT','aggressive','long','alert-1',102,0,'{}',103);
INSERT INTO alert_settings VALUES(1,'kept-in-main');`)
	if err != nil {
		_ = source.Close()
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	migrated, err := MigrateUnifiedSQLite(ctx, targetPath, sourcePath)
	if err != nil || !migrated {
		t.Fatalf("first migration migrated=%v err=%v", migrated, err)
	}
	completed, err := HasCompletedMigration(ctx, targetPath, sourcePath)
	if err != nil || !completed {
		t.Fatalf("migration marker completed=%v err=%v", completed, err)
	}
	second, err := MigrateUnifiedSQLite(ctx, targetPath, sourcePath)
	if err != nil || second {
		t.Fatalf("second migration migrated=%v err=%v", second, err)
	}

	target, err := sql.Open("sqlite", targetPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	for _, table := range []string{"liquidation_alerts", "trade_signals"} {
		var count int
		if err := target.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("target table %s count=%d", table, count)
		}
	}
	var settings int
	if err := target.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='alert_settings'").Scan(&settings); err != nil {
		t.Fatal(err)
	}
	if settings != 0 {
		t.Fatal("alert settings must remain in the unified database")
	}
}
