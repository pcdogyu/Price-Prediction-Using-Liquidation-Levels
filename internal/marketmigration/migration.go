package marketmigration

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

// Tables is the explicit market-data boundary for the second database split.
// Gamma, predictions, volume profiles, alerts, authentication, and TVBot data
// remain in the unified database until their own migration phases.
var Tables = []string{
	"candles",
	"liquidations",
	"wall_events",
	"book_snapshots",
	"market_metrics",
}

// MigrateUnifiedSQLite copies only market-data tables from sourcePath to
// targetPath. The source is never modified and a committed absolute-path
// marker makes retries idempotent.
func MigrateUnifiedSQLite(ctx context.Context, targetPath, sourcePath string) (bool, error) {
	if strings.TrimSpace(sourcePath) == "" {
		return false, nil
	}
	sourceAbs, err := absoluteExistingPath(sourcePath)
	if err != nil {
		return false, err
	}
	if sourceAbs == "" {
		return false, nil
	}
	targetAbs, err := filepath.Abs(strings.TrimSpace(targetPath))
	if err != nil {
		return false, err
	}
	if sourceAbs == targetAbs {
		return false, errors.New("market database must differ from APP_DATABASE")
	}
	if err := os.MkdirAll(filepath.Dir(targetAbs), 0o750); err != nil {
		return false, err
	}
	db, err := openDatabase(targetAbs)
	if err != nil {
		return false, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err = db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS market_migrations(source_path TEXT PRIMARY KEY,completed_ts INTEGER NOT NULL)`); err != nil {
		return false, err
	}
	var completed int
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM market_migrations WHERE source_path=?`, sourceAbs).Scan(&completed); err != nil {
		return false, err
	}
	if completed != 0 {
		return false, nil
	}
	if _, err = db.ExecContext(ctx, `ATTACH DATABASE ? AS legacy`, sourceAbs); err != nil {
		return false, err
	}
	defer db.ExecContext(context.Background(), `DETACH DATABASE legacy`)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS market_legacy_id_map(source_path TEXT NOT NULL,table_name TEXT NOT NULL,source_key TEXT NOT NULL,target_key TEXT NOT NULL,created_ts INTEGER NOT NULL,PRIMARY KEY(source_path,table_name,source_key))`); err != nil {
		return false, err
	}
	for _, table := range Tables {
		var present int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM legacy.sqlite_master WHERE type='table' AND name=?`, table).Scan(&present); err != nil {
			return false, err
		}
		if present == 0 {
			continue
		}
		if err := copyTable(ctx, tx, sourceAbs, table); err != nil {
			return false, fmt.Errorf("split market table %s: %w", table, err)
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO market_migrations(source_path,completed_ts) VALUES(?,?)`, sourceAbs, time.Now().UTC().UnixMilli()); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// HasCompletedMigration reports whether targetPath contains a committed marker
// for sourcePath. This prevents startup from selecting a partial or empty
// market database.
func HasCompletedMigration(ctx context.Context, targetPath, sourcePath string) (bool, error) {
	targetAbs, err := filepath.Abs(strings.TrimSpace(targetPath))
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(targetAbs); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	sourceAbs, err := filepath.Abs(strings.TrimSpace(sourcePath))
	if err != nil {
		return false, err
	}
	db, err := sql.Open("sqlite", targetAbs+"?mode=ro")
	if err != nil {
		return false, err
	}
	defer db.Close()
	var tableCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='market_migrations'`).Scan(&tableCount); err != nil {
		return false, err
	}
	if tableCount == 0 {
		return false, nil
	}
	var migrationCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM market_migrations WHERE source_path=?`, sourceAbs).Scan(&migrationCount); err != nil {
		return false, err
	}
	return migrationCount > 0, nil
}

func absoluteExistingPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("source database path is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(abs); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	return abs, nil
}

func openDatabase(path string) (*sql.DB, error) {
	return sql.Open("sqlite", path+"?_pragma=busy_timeout(60000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)")
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
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS main.`+quoteIdent(table)+" "+schema[open:]); err != nil {
		return err
	}
	columns, keys, err := tableColumns(ctx, tx, table)
	if err != nil {
		return err
	}
	if len(columns) == 0 {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+joinQuoted(columns)+` FROM legacy.`+quoteIdent(table))
	if err != nil {
		return err
	}
	defer rows.Close()
	placeholders := strings.TrimRight(strings.Repeat("?,", len(columns)), ",")
	insert, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO main.`+quoteIdent(table)+` (`+joinQuoted(columns)+`) VALUES (`+placeholders+`)`)
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
	_ = tx.QueryRowContext(ctx, `SELECT target_key FROM market_legacy_id_map WHERE source_path=? AND table_name=? AND source_key=?`, sourcePath, table, original).Scan(&mapped)
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
	keyExists, err := targetKeyExists(ctx, tx, table, key, values[keyIndex])
	if err != nil {
		return err
	}
	if !keyExists {
		return nil
	}
	for i := 0; i < 10000; i++ {
		candidate := "legacy:" + original
		if i > 0 {
			candidate = fmt.Sprintf("legacy:%s:%d", original, i)
		}
		values[keyIndex] = candidate
		result, err = insert.ExecContext(ctx, values...)
		if err != nil {
			return err
		}
		if affected, _ := result.RowsAffected(); affected > 0 {
			_, err = tx.ExecContext(ctx, `INSERT INTO market_legacy_id_map(source_path,table_name,source_key,target_key,created_ts) VALUES(?,?,?,?,?)`, sourcePath, table, original, candidate, time.Now().UTC().UnixMilli())
			return err
		}
		keyExists, err = targetKeyExists(ctx, tx, table, key, candidate)
		if err != nil {
			return err
		}
		if !keyExists {
			return nil
		}
	}
	return fmt.Errorf("could not map colliding key %q in table %q", original, table)
}

func targetKeyExists(ctx context.Context, tx *sql.Tx, table, key string, value any) (bool, error) {
	var found int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM main.`+quoteIdent(table)+` WHERE `+quoteIdent(key)+`=? LIMIT 1`, value).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
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
