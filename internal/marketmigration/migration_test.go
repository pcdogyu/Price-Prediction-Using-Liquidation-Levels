package marketmigration

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestMigrateUnifiedSQLiteCopiesOnlyMarketTablesAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "liquidation.db")
	target := filepath.Join(dir, "market.db")
	sourceDB, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	_, err = sourceDB.Exec(`
CREATE TABLE candles(exchange TEXT NOT NULL,symbol TEXT NOT NULL,ts INTEGER NOT NULL,open REAL,high REAL,low REAL,close REAL,volume_usd REAL,taker_buy_usd REAL,open_interest_usd REAL,funding_rate REAL,long_short_ratio REAL,PRIMARY KEY(exchange,symbol,ts));
INSERT INTO candles(exchange,symbol,ts,close) VALUES('binance','BTCUSDT',1,100);
CREATE TABLE liquidations(id TEXT PRIMARY KEY,exchange TEXT NOT NULL,symbol TEXT NOT NULL,position_side TEXT NOT NULL,event_ts INTEGER NOT NULL,received_ts INTEGER NOT NULL,price REAL NOT NULL,quantity REAL NOT NULL,notional_usd REAL NOT NULL,coverage TEXT NOT NULL);
INSERT INTO liquidations VALUES('liq-1','binance','BTCUSDT','long',1,1,100,1,100,'test');
CREATE TABLE auth_users(username TEXT PRIMARY KEY,password_hash TEXT NOT NULL);
INSERT INTO auth_users VALUES('not-copied','fixture');`)
	if err != nil {
		_ = sourceDB.Close()
		t.Fatal(err)
	}
	if err := sourceDB.Close(); err != nil {
		t.Fatal(err)
	}

	migrated, err := MigrateUnifiedSQLite(context.Background(), target, source)
	if err != nil || !migrated {
		t.Fatalf("migration migrated=%v err=%v", migrated, err)
	}
	completed, err := HasCompletedMigration(context.Background(), target, source)
	if err != nil || !completed {
		t.Fatalf("completed=%v err=%v", completed, err)
	}

	check, err := sql.Open("sqlite", target+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	var count int
	if err := check.QueryRow(`SELECT COUNT(*) FROM candles`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("candles=%d err=%v", count, err)
	}
	if err := check.QueryRow(`SELECT COUNT(*) FROM liquidations`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("liquidations=%d err=%v", count, err)
	}
	var authTables int
	if err := check.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='auth_users'`).Scan(&authTables); err != nil || authTables != 0 {
		t.Fatalf("auth_users tables=%d err=%v", authTables, err)
	}
	if migrated, err := MigrateUnifiedSQLite(context.Background(), target, source); err != nil || migrated {
		t.Fatalf("second migration must be a no-op migrated=%v err=%v", migrated, err)
	}
}

func TestMigrateUnifiedSQLiteSkipsMissingSource(t *testing.T) {
	migrated, err := MigrateUnifiedSQLite(context.Background(), filepath.Join(t.TempDir(), "market.db"), filepath.Join(t.TempDir(), "missing.db"))
	if err != nil || migrated {
		t.Fatalf("missing source migrated=%v err=%v", migrated, err)
	}
}
