package alertsmigration

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestMigrateUnifiedSQLiteCopiesCompleteAlertModule(t *testing.T) {
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
CREATE TABLE alert_watch_symbols(symbol TEXT PRIMARY KEY,rank INTEGER NOT NULL,quote_volume_usd REAL NOT NULL,updated_ts INTEGER NOT NULL);
CREATE TABLE alert_settings(id INTEGER PRIMARY KEY,telegram_token TEXT NOT NULL);
CREATE TABLE alert_strategy_settings(id INTEGER PRIMARY KEY,window_minutes INTEGER NOT NULL);
CREATE TABLE alert_tenant_settings(tenant_id TEXT PRIMARY KEY,telegram_token TEXT NOT NULL);
CREATE TABLE alert_tenant_strategy_settings(tenant_id TEXT PRIMARY KEY,window_minutes INTEGER NOT NULL);
INSERT INTO liquidation_alerts VALUES('alert-1','BTCUSDT','long',100,'watching','{}',101);
INSERT INTO trade_signals VALUES('signal-1','BTCUSDT','aggressive','long','alert-1',102,0,'{}',103);
INSERT INTO alert_watch_symbols VALUES('BTCUSDT',1,100,104);
INSERT INTO alert_settings VALUES(1,'copied');
INSERT INTO alert_strategy_settings VALUES(1,5);
INSERT INTO alert_tenant_settings VALUES('alice','tenant-copied');
INSERT INTO alert_tenant_strategy_settings VALUES('alice',10);`)
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
	for _, table := range append(historyTables, settingsTables...) {
		var count int
		if err := target.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("target table %s count=%d", table, count)
		}
	}
}

func TestMigrateUnifiedSQLiteCompletesSettingsForExistingHistoryDatabase(t *testing.T) {
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
INSERT INTO alert_settings VALUES(1,'copied');`)
	if err != nil {
		_ = source.Close()
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}

	target, err := sql.Open("sqlite", targetPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = target.Exec(`
CREATE TABLE alerts_migrations(source_path TEXT PRIMARY KEY,completed_ts INTEGER NOT NULL);
CREATE TABLE liquidation_alerts(id TEXT PRIMARY KEY,symbol TEXT NOT NULL,side TEXT NOT NULL,trigger_ts INTEGER NOT NULL,status TEXT NOT NULL,payload BLOB NOT NULL,updated_ts INTEGER NOT NULL);
CREATE TABLE trade_signals(id TEXT PRIMARY KEY,symbol TEXT NOT NULL,kind TEXT NOT NULL,side TEXT NOT NULL,alert_id TEXT NOT NULL,created_ts INTEGER NOT NULL,notified INTEGER NOT NULL,payload BLOB NOT NULL,updated_ts INTEGER NOT NULL);
INSERT INTO alerts_migrations VALUES(?,1);
INSERT INTO liquidation_alerts VALUES('alert-1','BTCUSDT','long',100,'watching','{}',101);
INSERT INTO trade_signals VALUES('signal-1','BTCUSDT','aggressive','long','alert-1',102,0,'{}',103);`, mustAbs(sourcePath))
	if err != nil {
		_ = target.Close()
		t.Fatal(err)
	}
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}

	migrated, err := MigrateUnifiedSQLite(context.Background(), targetPath, sourcePath)
	if err != nil || !migrated {
		t.Fatalf("settings completion migrated=%v err=%v", migrated, err)
	}
	completed, err := HasCompletedMigration(context.Background(), targetPath, sourcePath)
	if err != nil || !completed {
		t.Fatalf("migration marker completed=%v err=%v", completed, err)
	}
	check, err := sql.Open("sqlite", targetPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	var count int
	if err := check.QueryRow("SELECT COUNT(*) FROM alert_settings").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("alert settings count=%d", count)
	}
}

func mustAbs(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		panic(err)
	}
	return abs
}
