package authmigration

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestMigrateUnifiedSQLiteCopiesOnlyAuthTables(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "liquidation.db")
	targetPath := filepath.Join(dir, "auth.db")
	source, err := sql.Open("sqlite", sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = source.Exec(`
CREATE TABLE auth_sessions(token_hash TEXT PRIMARY KEY,expires_ts INTEGER NOT NULL,username TEXT NOT NULL DEFAULT '');
CREATE TABLE auth_users(username TEXT PRIMARY KEY,password_hash TEXT NOT NULL,created_ts INTEGER NOT NULL,updated_ts INTEGER NOT NULL,role TEXT NOT NULL DEFAULT 'user',disabled INTEGER NOT NULL DEFAULT 0,last_login_ts INTEGER);
CREATE TABLE auth_audit(id INTEGER PRIMARY KEY AUTOINCREMENT,actor TEXT NOT NULL,action TEXT NOT NULL,target TEXT NOT NULL,created_ts INTEGER NOT NULL);
CREATE TABLE user_theme(username TEXT PRIMARY KEY,theme TEXT NOT NULL,updated_ts INTEGER NOT NULL);
INSERT INTO auth_sessions VALUES('token-hash',4102444800000,'alice');
INSERT INTO auth_users VALUES('alice','hash',1,2,'admin',0,NULL);
INSERT INTO auth_audit(actor,action,target,created_ts) VALUES('system','login','alice',3);
INSERT INTO user_theme VALUES('alice','dark',4);`)
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
	var settingsTables int
	if err := target.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='user_theme'").Scan(&settingsTables); err != nil {
		t.Fatal(err)
	}
	if settingsTables != 0 {
		t.Fatal("settings tables must remain in the unified database")
	}
}

func TestMigrateUnifiedSQLiteSkipsMissingSource(t *testing.T) {
	migrated, err := MigrateUnifiedSQLite(context.Background(), filepath.Join(t.TempDir(), "auth.db"), filepath.Join(t.TempDir(), "missing.db"))
	if err != nil || migrated {
		t.Fatalf("missing source migrated=%v err=%v", migrated, err)
	}
}
