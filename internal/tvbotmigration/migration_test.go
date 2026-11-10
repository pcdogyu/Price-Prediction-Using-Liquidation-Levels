package tvbotmigration

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestMigrateSQLiteIsTransactionalIdempotentAndMapsCollisions(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "legacy.db")
	target := filepath.Join(dir, "unified.db")
	legacyDB, err := sql.Open("sqlite", legacy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = legacyDB.Exec(`CREATE TABLE orders(id TEXT PRIMARY KEY,value TEXT); INSERT INTO orders(id,value) VALUES('same','legacy'); CREATE TABLE auth_users(username TEXT PRIMARY KEY,password_hash TEXT); INSERT INTO auth_users(username,password_hash) VALUES('legacy','secret');`); err != nil {
		t.Fatal(err)
	}
	_ = legacyDB.Close()
	targetDB, err := sql.Open("sqlite", target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = targetDB.Exec(`CREATE TABLE orders(id TEXT PRIMARY KEY,value TEXT); INSERT INTO orders(id,value) VALUES('same','current');`); err != nil {
		t.Fatal(err)
	}
	_ = targetDB.Close()
	if migrated, err := MigrateSQLite(context.Background(), target, legacy); err != nil || !migrated {
		t.Fatalf("migration migrated=%v err=%v", migrated, err)
	}
	check, err := sql.Open("sqlite", target)
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	var count int
	if err = check.QueryRow(`SELECT COUNT(*) FROM orders`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("expected collision row to be preserved, count=%d err=%v", count, err)
	}
	var mapped string
	if err = check.QueryRow(`SELECT target_key FROM tvbot_legacy_id_map WHERE table_name='orders' AND source_key='same'`).Scan(&mapped); err != nil || mapped != "legacy:same" {
		t.Fatalf("unexpected collision mapping %q err=%v", mapped, err)
	}
	if err = check.QueryRow(`SELECT COUNT(*) FROM auth_users`).Scan(&count); err != nil || count != 0 {
		if err == nil {
			t.Fatalf("legacy auth table must not be copied, count=%d", count)
		}
	}
	if migrated, err := MigrateSQLite(context.Background(), target, legacy); err != nil || migrated {
		t.Fatalf("second migration must be a no-op migrated=%v err=%v", migrated, err)
	}
}

func TestMigrateUnifiedSQLiteCopiesOnlyTVBotTablesAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "liquidation.db")
	target := filepath.Join(dir, "tvbot.db")
	sourceDB, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = sourceDB.Exec(`
CREATE TABLE orders(signal_id TEXT PRIMARY KEY,dedupe_key TEXT NOT NULL,status TEXT NOT NULL,accepted_at TEXT NOT NULL,updated_at TEXT NOT NULL);
INSERT INTO orders(signal_id,dedupe_key,status,accepted_at,updated_at) VALUES('signal-1','dedupe-1','submitted','2026-11-10T00:00:00Z','2026-11-10T00:01:00Z');
CREATE TABLE candles(exchange TEXT NOT NULL,symbol TEXT NOT NULL,ts INTEGER NOT NULL,PRIMARY KEY(exchange,symbol,ts));
INSERT INTO candles(exchange,symbol,ts) VALUES('binance','BTCUSDT',1);
CREATE TABLE auth_users(username TEXT PRIMARY KEY,password_hash TEXT NOT NULL);
INSERT INTO auth_users(username,password_hash) VALUES('admin','not-copied');`); err != nil {
		_ = sourceDB.Close()
		t.Fatal(err)
	}
	if err := sourceDB.Close(); err != nil {
		t.Fatal(err)
	}

	migrated, err := MigrateUnifiedSQLite(context.Background(), target, source)
	if err != nil || !migrated {
		t.Fatalf("first split migrated=%v err=%v", migrated, err)
	}
	check, err := sql.Open("sqlite", target)
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	var count int
	if err = check.QueryRow(`SELECT COUNT(*) FROM orders WHERE signal_id='signal-1'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("TVBot order was not copied, count=%d err=%v", count, err)
	}
	if err = check.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='candles'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("market table leaked into TVBot database, count=%d err=%v", count, err)
	}
	if err = check.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='auth_users'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("auth table leaked into TVBot database, count=%d err=%v", count, err)
	}
	if err = check.QueryRow(`SELECT COUNT(*) FROM tvbot_migrations WHERE source_path=?`, mustAbs(t, source)).Scan(&count); err != nil || count != 1 {
		t.Fatalf("split marker count=%d err=%v", count, err)
	}

	migrated, err = MigrateUnifiedSQLite(context.Background(), target, source)
	if err != nil || migrated {
		t.Fatalf("second split must be a no-op migrated=%v err=%v", migrated, err)
	}
}

func TestMigrateUnifiedSQLiteSkipsMissingSource(t *testing.T) {
	target := filepath.Join(t.TempDir(), "tvbot.db")
	migrated, err := MigrateUnifiedSQLite(context.Background(), target, filepath.Join(t.TempDir(), "missing.db"))
	if err != nil || migrated {
		t.Fatalf("missing source must be a no-op migrated=%v err=%v", migrated, err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("missing source should not create target, stat err=%v", err)
	}
}

func mustAbs(t *testing.T, path string) string {
	t.Helper()
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}
