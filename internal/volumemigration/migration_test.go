package volumemigration

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestMigrateUnifiedSQLiteCopiesOnlyVolumeTables(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "liquidation.db")
	targetPath := filepath.Join(dir, "volume.db")
	source, err := sql.Open("sqlite", sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = source.Exec(`
CREATE TABLE volume_profile_levels(symbol TEXT NOT NULL,session_ts INTEGER NOT NULL,price_key TEXT NOT NULL,price REAL NOT NULL,volume_usd REAL NOT NULL,trade_count INTEGER NOT NULL,last_trade_ts INTEGER NOT NULL,PRIMARY KEY(symbol,session_ts,price_key));
CREATE TABLE volume_profile_cursors(symbol TEXT NOT NULL,session_ts INTEGER NOT NULL,last_agg_id INTEGER NOT NULL,last_trade_ts INTEGER NOT NULL,complete INTEGER NOT NULL,updated_ts INTEGER NOT NULL,PRIMARY KEY(symbol,session_ts));
CREATE TABLE volume_profile_snapshots(symbol TEXT NOT NULL,ts INTEGER NOT NULL,session_ts INTEGER NOT NULL,val REAL NOT NULL,vah REAL NOT NULL,total_volume_usd REAL NOT NULL,complete INTEGER NOT NULL,PRIMARY KEY(symbol,ts));
CREATE TABLE volume_archive_imports(symbol TEXT NOT NULL,day TEXT NOT NULL,status TEXT NOT NULL,error TEXT NOT NULL,updated_ts INTEGER NOT NULL,PRIMARY KEY(symbol,day));
CREATE TABLE auth_users(username TEXT PRIMARY KEY,password_hash TEXT NOT NULL);
INSERT INTO volume_profile_levels VALUES('BTCUSDT',1,'100',100,200,2,2);
INSERT INTO volume_profile_cursors VALUES('BTCUSDT',1,2,2,1,3);
INSERT INTO volume_profile_snapshots VALUES('BTCUSDT',4,1,99,101,300,1);
INSERT INTO volume_archive_imports VALUES('BTCUSDT','2026-11-11','complete','',5);
INSERT INTO auth_users VALUES('not-copied','fixture');`)
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
	for _, table := range Tables {
		var count int
		if err := target.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("target table %s count=%d", table, count)
		}
	}
	var authTables int
	if err := target.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='auth_users'").Scan(&authTables); err != nil {
		t.Fatal(err)
	}
	if authTables != 0 {
		t.Fatal("authentication tables must remain in the unified database")
	}
}

func TestMigrateUnifiedSQLiteSkipsMissingSource(t *testing.T) {
	migrated, err := MigrateUnifiedSQLite(context.Background(), filepath.Join(t.TempDir(), "volume.db"), filepath.Join(t.TempDir(), "missing.db"))
	if err != nil || migrated {
		t.Fatalf("missing source migrated=%v err=%v", migrated, err)
	}
}
