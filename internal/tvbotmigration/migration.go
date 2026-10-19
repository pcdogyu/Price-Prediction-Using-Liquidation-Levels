package tvbotmigration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// MigrateSQLite copies the legacy tvbot tables into the unified database in a
// single transaction. It deliberately skips auth tables because those are
// migrated by the unified Argon2 account loader. A completed source path is
// recorded, so restart/retry is idempotent.
func MigrateSQLite(ctx context.Context, targetPath, legacyPath string) (bool, error) {
	legacyPath = strings.TrimSpace(legacyPath)
	if legacyPath == "" {
		return false, nil
	}
	legacyAbs, err := filepath.Abs(legacyPath)
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(legacyAbs); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	targetAbs, err := filepath.Abs(targetPath)
	if err != nil {
		return false, err
	}
	if legacyAbs == targetAbs {
		return false, nil
	}
	db, err := sql.Open("sqlite", targetAbs+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return false, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err = db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS tvbot_migrations(source_path TEXT PRIMARY KEY,completed_ts INTEGER NOT NULL)`); err != nil {
		return false, err
	}
	var completed int
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tvbot_migrations WHERE source_path=?`, legacyAbs).Scan(&completed); err != nil {
		return false, err
	}
	if completed != 0 {
		return false, nil
	}
	if _, err = db.ExecContext(ctx, `ATTACH DATABASE ? AS legacy`, legacyAbs); err != nil {
		return false, err
	}
	defer db.ExecContext(context.Background(), `DETACH DATABASE legacy`)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS tvbot_legacy_id_map(source_path TEXT NOT NULL,table_name TEXT NOT NULL,source_key TEXT NOT NULL,target_key TEXT NOT NULL,created_ts INTEGER NOT NULL,PRIMARY KEY(source_path,table_name,source_key))`); err != nil {
		return false, err
	}
	tables, err := legacyTables(ctx, tx)
	if err != nil {
		return false, err
	}
	for _, table := range tables {
		if table == "auth_users" || table == "auth_sessions" || table == "tvbot_migrations" || table == "tvbot_legacy_id_map" {
			continue
		}
		if err := copyTable(ctx, tx, legacyAbs, table); err != nil {
			return false, fmt.Errorf("migrate tvbot table %s: %w", table, err)
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO tvbot_migrations(source_path,completed_ts) VALUES(?,?)`, legacyAbs, time.Now().UTC().UnixMilli()); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func legacyTables(ctx context.Context, tx *sql.Tx) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT name FROM legacy.sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

func copyTable(ctx context.Context, tx *sql.Tx, sourcePath, table string) error {
	var schema string
	if err := tx.QueryRowContext(ctx, `SELECT sql FROM legacy.sqlite_master WHERE type='table' AND name=?`, table).Scan(&schema); err != nil {
		return err
	}
	open := strings.Index(schema, "(")
	if open < 0 {
		return fmt.Errorf("table schema has no columns")
	}
	create := `CREATE TABLE IF NOT EXISTS main.` + quoteIdent(table) + " " + schema[open:]
	if _, err := tx.ExecContext(ctx, create); err != nil {
		return err
	}
	columns, keys, err := tableColumns(ctx, tx, table)
	if err != nil {
		return err
	}
	if len(columns) == 0 {
		return nil
	}
	selectSQL := `SELECT ` + joinQuoted(columns) + ` FROM legacy.` + quoteIdent(table)
	rows, err := tx.QueryContext(ctx, selectSQL)
	if err != nil {
		return err
	}
	defer rows.Close()
	placeholders := strings.TrimRight(strings.Repeat("?,", len(columns)), ",")
	insertSQL := `INSERT OR IGNORE INTO main.` + quoteIdent(table) + ` (` + joinQuoted(columns) + `) VALUES (` + placeholders + `)`
	insert, err := tx.PrepareContext(ctx, insertSQL)
	if err != nil {
		return err
	}
	defer insert.Close()
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(values))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			return err
		}
		if len(keys) == 1 && isTextKey(values, columns, keys[0]) {
			if err := copyKeyedRow(ctx, tx, insert, sourcePath, table, columns, keys[0], values); err != nil {
				return err
			}
		} else if _, err := insert.ExecContext(ctx, values...); err != nil {
			return err
		}
	}
	return rows.Err()
}

func copyKeyedRow(ctx context.Context, tx *sql.Tx, insert *sql.Stmt, sourcePath, table string, columns []string, key string, values []any) error {
	keyIndex := -1
	for i, column := range columns {
		if column == key {
			keyIndex = i
			break
		}
	}
	if keyIndex < 0 {
		_, err := insert.ExecContext(ctx, values...)
		return err
	}
	original, _ := values[keyIndex].(string)
	var mapped string
	_ = tx.QueryRowContext(ctx, `SELECT target_key FROM tvbot_legacy_id_map WHERE source_path=? AND table_name=? AND source_key=?`, sourcePath, table, original).Scan(&mapped)
	if mapped != "" {
		values[keyIndex] = mapped
		_, err := insert.ExecContext(ctx, values...)
		return err
	}
	result, err := insert.ExecContext(ctx, values...)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected > 0 {
		return nil
	}
	candidate := "legacy:" + original
	for i := 1; ; i++ {
		values[keyIndex] = candidate
		result, err = insert.ExecContext(ctx, values...)
		if err != nil {
			return err
		}
		if affected, _ := result.RowsAffected(); affected > 0 {
			_, err = tx.ExecContext(ctx, `INSERT INTO tvbot_legacy_id_map(source_path,table_name,source_key,target_key,created_ts) VALUES(?,?,?,?,?)`, sourcePath, table, original, candidate, time.Now().UTC().UnixMilli())
			return err
		}
		candidate = fmt.Sprintf("legacy:%s:%d", original, i)
	}
}

func tableColumns(ctx context.Context, tx *sql.Tx, table string) ([]string, []string, error) {
	rows, err := tx.QueryContext(ctx, `PRAGMA legacy.table_info(`+quoteIdent(table)+`)`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var columns, keys []string
	for rows.Next() {
		var cid, notNull, pk int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &pk); err != nil {
			return nil, nil, err
		}
		columns = append(columns, name)
		if pk > 0 {
			keys = append(keys, name)
		}
	}
	return columns, keys, rows.Err()
}

func isTextKey(values []any, columns []string, key string) bool {
	for i, column := range columns {
		if column == key {
			_, ok := values[i].(string)
			return ok
		}
	}
	return false
}

func joinQuoted(values []string) string {
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = quoteIdent(value)
	}
	return strings.Join(out, ",")
}

func quoteIdent(value string) string { return `"` + strings.ReplaceAll(value, `"`, `""`) + `"` }
