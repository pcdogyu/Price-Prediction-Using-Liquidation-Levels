package alertsmigration

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

var tables = []string{"liquidation_alerts", "trade_signals"}

// MigrateUnifiedSQLite copies alert history and signal history into a
// standalone database. Alert settings and tenant configuration are deliberately
// excluded and remain in the unified application database.
func MigrateUnifiedSQLite(ctx context.Context, targetPath, sourcePath string) (bool, error) {
	if strings.TrimSpace(sourcePath) == "" {
		return false, nil
	}
	sourceAbs, err := filepath.Abs(sourcePath)
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(sourceAbs); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	targetAbs, err := filepath.Abs(strings.TrimSpace(targetPath))
	if err != nil {
		return false, err
	}
	if sourceAbs == targetAbs {
		return false, errors.New("alerts database must differ from APP_DATABASE")
	}
	if err := os.MkdirAll(filepath.Dir(targetAbs), 0o750); err != nil {
		return false, err
	}
	db, err := sql.Open("sqlite", targetAbs+"?_pragma=busy_timeout(60000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return false, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err = db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS alerts_migrations(source_path TEXT PRIMARY KEY,completed_ts INTEGER NOT NULL)`); err != nil {
		return false, err
	}
	var completed int
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM alerts_migrations WHERE source_path=?`, sourceAbs).Scan(&completed); err != nil {
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
	for _, table := range tables {
		var present int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM legacy.sqlite_master WHERE type='table' AND name=?`, table).Scan(&present); err != nil {
			return false, err
		}
		if present == 0 {
			continue
		}
		if err := copyTable(ctx, tx, table); err != nil {
			return false, fmt.Errorf("split alerts table %s: %w", table, err)
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO alerts_migrations(source_path,completed_ts) VALUES(?,?)`, sourceAbs, time.Now().UTC().UnixMilli()); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

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
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='alerts_migrations'`).Scan(&tableCount); err != nil {
		return false, err
	}
	if tableCount == 0 {
		return false, nil
	}
	var migrationCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM alerts_migrations WHERE source_path=?`, sourceAbs).Scan(&migrationCount); err != nil {
		return false, err
	}
	return migrationCount > 0, nil
}

func copyTable(ctx context.Context, tx *sql.Tx, table string) error {
	var schema string
	if err := tx.QueryRowContext(ctx, `SELECT sql FROM legacy.sqlite_master WHERE type='table' AND name=?`, table).Scan(&schema); err != nil {
		return err
	}
	open := strings.Index(schema, "(")
	if open < 0 {
		return errors.New("table schema has no columns")
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS main.`+quoteIdent(table)+" "+schema[open:]); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `PRAGMA legacy.table_info(`+quoteIdent(table)+`)`)
	if err != nil {
		return err
	}
	var columns []string
	for rows.Next() {
		var cid, notNull, primary int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primary); err != nil {
			rows.Close()
			return err
		}
		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if len(columns) == 0 {
		return nil
	}
	sourceRows, err := tx.QueryContext(ctx, `SELECT `+joinQuoted(columns)+` FROM legacy.`+quoteIdent(table))
	if err != nil {
		return err
	}
	defer sourceRows.Close()
	placeholders := strings.TrimRight(strings.Repeat("?,", len(columns)), ",")
	insert, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO main.`+quoteIdent(table)+` (`+joinQuoted(columns)+`) VALUES (`+placeholders+`)`)
	if err != nil {
		return err
	}
	defer insert.Close()
	for sourceRows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(values))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := sourceRows.Scan(pointers...); err != nil {
			return err
		}
		if _, err := insert.ExecContext(ctx, values...); err != nil {
			return err
		}
	}
	return sourceRows.Err()
}

func joinQuoted(values []string) string {
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = quoteIdent(value)
	}
	return strings.Join(quoted, ",")
}

func quoteIdent(value string) string { return `"` + strings.ReplaceAll(value, `"`, `""`) + `"` }
