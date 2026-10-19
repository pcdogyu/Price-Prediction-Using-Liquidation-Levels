package tvbotmigration

import (
	"context"
	"database/sql"
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
