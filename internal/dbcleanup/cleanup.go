package dbcleanup

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

// Target describes one already-migrated module database. Ready must verify
// the module's committed migration marker for the same source database.
type Target struct {
	Name   string
	Path   string
	Tables []string
	Ready  func(context.Context, string, string) (bool, error)
}

type TableReport struct {
	Module string
	Table  string
	Rows   int64
}

// Report records the destructive part of a successful cleanup.
type Report struct {
	Tables      []TableReport
	Rows        int64
	BytesBefore int64
	BytesAfter  int64
	Elapsed     time.Duration
}

// CleanupUnifiedSQLite removes only the migrated module rows from sourcePath.
// It keeps migration metadata and every non-module table. Every target is
// verified before the source transaction begins, then the source is vacuumed
// so the old data is actually released from the large database file.
func CleanupUnifiedSQLite(ctx context.Context, sourcePath string, targets []Target) (Report, error) {
	started := time.Now().UTC()
	var report Report
	sourcePath = strings.TrimSpace(sourcePath)
	if sourcePath == "" {
		return report, errors.New("source database path is required")
	}
	sourceAbs, err := filepath.Abs(sourcePath)
	if err != nil {
		return report, err
	}
	if info, err := os.Stat(sourceAbs); err != nil {
		return report, err
	} else {
		report.BytesBefore = info.Size()
	}
	if len(targets) == 0 {
		return report, errors.New("at least one migrated database target is required")
	}
	seenPaths := map[string]struct{}{sourceAbs: {}}
	seenTables := make(map[string]string)
	for _, target := range targets {
		path := strings.TrimSpace(target.Path)
		if path == "" {
			return report, fmt.Errorf("%s database path is required", target.Name)
		}
		pathAbs, err := filepath.Abs(path)
		if err != nil {
			return report, fmt.Errorf("resolve %s database: %w", target.Name, err)
		}
		if _, ok := seenPaths[pathAbs]; ok {
			return report, fmt.Errorf("%s database path duplicates another database", target.Name)
		}
		seenPaths[pathAbs] = struct{}{}
		if target.Ready == nil {
			return report, fmt.Errorf("%s database readiness check is missing", target.Name)
		}
		ready, err := target.Ready(ctx, path, sourcePath)
		if err != nil {
			return report, fmt.Errorf("verify %s database migration: %w", target.Name, err)
		}
		if !ready {
			return report, fmt.Errorf("%s database migration is not complete", target.Name)
		}
		if err := verifyTables(ctx, path, target.Tables); err != nil {
			return report, fmt.Errorf("verify %s database tables: %w", target.Name, err)
		}
		for _, table := range target.Tables {
			if owner, ok := seenTables[table]; ok {
				return report, fmt.Errorf("table %s is listed for both %s and %s", table, owner, target.Name)
			}
			seenTables[table] = target.Name
		}
	}

	db, err := sql.Open("sqlite", sourceAbs+"?_pragma=busy_timeout(60000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return report, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return report, err
	}
	defer tx.Rollback()
	for _, target := range targets {
		for _, table := range target.Tables {
			present, err := tableExists(ctx, tx, table)
			if err != nil {
				return report, err
			}
			if !present {
				continue
			}
			var count int64
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+quoteIdent(table)).Scan(&count); err != nil {
				return report, err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM `+quoteIdent(table)); err != nil {
				return report, fmt.Errorf("clear %s: %w", table, err)
			}
			report.Tables = append(report.Tables, TableReport{Module: target.Name, Table: table, Rows: count})
			report.Rows += count
		}
	}
	if err := tx.Commit(); err != nil {
		return report, err
	}
	if _, err := db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return report, err
	}
	if _, err := db.ExecContext(ctx, `VACUUM`); err != nil {
		return report, err
	}
	if _, err := db.ExecContext(ctx, `PRAGMA optimize`); err != nil {
		return report, err
	}
	if info, err := os.Stat(sourceAbs); err == nil {
		report.BytesAfter = info.Size()
	}
	report.Elapsed = time.Since(started)
	return report, nil
}

func verifyTables(ctx context.Context, path string, tables []string) error {
	db, err := sql.Open("sqlite", filepath.Clean(path)+"?mode=ro")
	if err != nil {
		return err
	}
	defer db.Close()
	for _, table := range tables {
		present, err := tableExists(ctx, db, table)
		if err != nil {
			return err
		}
		if !present {
			return fmt.Errorf("target table %s is missing", table)
		}
	}
	return nil
}

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func tableExists(ctx context.Context, db queryer, table string) (bool, error) {
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count); err != nil {
		return false, err
	}
	return count != 0, nil
}

func quoteIdent(value string) string {
	if strings.TrimSpace(value) == "" || strings.Contains(value, `"`) {
		return `"invalid_table_name"`
	}
	return `"` + value + `"`
}
