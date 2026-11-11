package optionsmigration

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestMigrateUnifiedSQLiteCopiesOnlyOptionGammaTables(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "liquidation.db")
	targetPath := filepath.Join(dir, "options.db")
	source, err := sql.Open("sqlite", sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = source.Exec(`
CREATE TABLE gamma_latest(symbol TEXT PRIMARY KEY,payload BLOB NOT NULL);
CREATE TABLE deribit_gamma_history(symbol TEXT NOT NULL,ts INTEGER NOT NULL,payload BLOB NOT NULL,PRIMARY KEY(symbol,ts));
CREATE TABLE auth_users(username TEXT PRIMARY KEY,password_hash TEXT NOT NULL);
INSERT INTO gamma_latest VALUES('BTCUSDT','{}');
INSERT INTO deribit_gamma_history VALUES('BTCUSDT',100,'{}');
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
	for _, table := range []string{"gamma_latest", "deribit_gamma_history"} {
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
	migrated, err := MigrateUnifiedSQLite(context.Background(), filepath.Join(t.TempDir(), "options.db"), filepath.Join(t.TempDir(), "missing.db"))
	if err != nil || migrated {
		t.Fatalf("missing source migrated=%v err=%v", migrated, err)
	}
}
