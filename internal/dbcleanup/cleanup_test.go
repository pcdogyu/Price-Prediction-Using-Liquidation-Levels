package dbcleanup

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestCleanupUnifiedSQLiteClearsOnlyMigratedRows(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "liquidation.db")
	targetPath := filepath.Join(dir, "module.db")
	source, err := sql.Open("sqlite", sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Exec(`CREATE TABLE orders(id TEXT PRIMARY KEY); INSERT INTO orders VALUES('old'); CREATE TABLE auth_users(username TEXT PRIMARY KEY); INSERT INTO auth_users VALUES('alice');`); err != nil {
		t.Fatal(err)
	}
	_ = source.Close()
	target, err := sql.Open("sqlite", targetPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := target.Exec(`CREATE TABLE orders(id TEXT PRIMARY KEY); INSERT INTO orders VALUES('old');`); err != nil {
		t.Fatal(err)
	}
	_ = target.Close()

	report, err := CleanupUnifiedSQLite(context.Background(), sourcePath, []Target{{
		Name: "tvbot", Path: targetPath, Tables: []string{"orders"},
		Ready: func(context.Context, string, string) (bool, error) { return true, nil },
	}})
	if err != nil {
		t.Fatal(err)
	}
	if report.Rows != 1 || len(report.Tables) != 1 || report.Tables[0].Rows != 1 {
		t.Fatalf("unexpected report: %#v", report)
	}
	check, err := sql.Open("sqlite", sourcePath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	var orders, users int
	if err := check.QueryRow(`SELECT COUNT(*) FROM orders`).Scan(&orders); err != nil {
		t.Fatal(err)
	}
	if err := check.QueryRow(`SELECT COUNT(*) FROM auth_users`).Scan(&users); err != nil {
		t.Fatal(err)
	}
	if orders != 0 || users != 1 {
		t.Fatalf("cleanup changed unexpected tables: orders=%d users=%d", orders, users)
	}
}

func TestCleanupUnifiedSQLiteRequiresReadyTarget(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "liquidation.db")
	db, err := sql.Open("sqlite", sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE orders(id TEXT PRIMARY KEY); INSERT INTO orders VALUES('old')`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	_, err = CleanupUnifiedSQLite(context.Background(), sourcePath, []Target{{
		Name: "tvbot", Path: filepath.Join(dir, "module.db"), Tables: []string{"orders"},
		Ready: func(context.Context, string, string) (bool, error) { return false, nil },
	}})
	if err == nil {
		t.Fatal("expected unready target error")
	}
}
