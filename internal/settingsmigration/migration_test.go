package settingsmigration

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestMigrateUnifiedSQLiteCopiesOnlySettingsTables(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "liquidation.db")
	targetPath := filepath.Join(dir, "settings.db")
	source, err := sql.Open("sqlite", sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = source.Exec(`
CREATE TABLE admin_menu(id INTEGER PRIMARY KEY,payload BLOB NOT NULL,updated_ts INTEGER NOT NULL);
CREATE TABLE user_menu(username TEXT PRIMARY KEY,payload BLOB NOT NULL,updated_ts INTEGER NOT NULL);
CREATE TABLE user_theme(username TEXT PRIMARY KEY,theme TEXT NOT NULL,updated_ts INTEGER NOT NULL);
CREATE TABLE background_symbol_snapshot(id INTEGER PRIMARY KEY,payload BLOB NOT NULL,updated_ts INTEGER NOT NULL);
CREATE TABLE auth_users(username TEXT PRIMARY KEY,password_hash TEXT NOT NULL);
INSERT INTO admin_menu VALUES(1,'admin',1);
INSERT INTO user_menu VALUES('alice','menu',2);
INSERT INTO user_theme VALUES('alice','dark',3);
INSERT INTO background_symbol_snapshot VALUES(1,'snapshot',4);
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
	migrated, err := MigrateUnifiedSQLite(context.Background(), filepath.Join(t.TempDir(), "settings.db"), filepath.Join(t.TempDir(), "missing.db"))
	if err != nil || migrated {
		t.Fatalf("missing source migrated=%v err=%v", migrated, err)
	}
}
