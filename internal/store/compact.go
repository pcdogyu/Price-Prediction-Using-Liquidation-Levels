package store

import (
	"context"
	"database/sql"
	"time"
)

// CompactReport describes one space-reclamation pass.
type CompactReport struct {
	Compacted   bool
	FreeBytes   int64
	BytesBefore int64
	BytesAfter  int64
	Elapsed     time.Duration
}

// Compact checkpoints the write-ahead log and rewrites the database once daily
// retention has freed at least minimumFreeBytes. VACUUM touches the whole file,
// so it belongs to the maintenance window and never runs for a few free pages.
func (s *Store) Compact(ctx context.Context, minimumFreeBytes int64) (CompactReport, error) {
	report, err := compactDatabase(ctx, s.db, minimumFreeBytes)
	if err != nil {
		return report, err
	}
	if s.marketDB != nil && s.marketDB != s.db {
		marketReport, marketErr := compactDatabase(ctx, s.marketDB, minimumFreeBytes)
		if marketErr != nil {
			return report, marketErr
		}
		report.Compacted = report.Compacted || marketReport.Compacted
		report.FreeBytes += marketReport.FreeBytes
		report.BytesBefore += marketReport.BytesBefore
		report.BytesAfter += marketReport.BytesAfter
		report.Elapsed += marketReport.Elapsed
	}
	if s.alertsDB == nil || s.alertsDB == s.db || s.alertsDB == s.marketDB {
		if s.optionsDB == nil || s.optionsDB == s.db || s.optionsDB == s.marketDB {
			if (s.volumeDB == nil || s.volumeDB == s.db || s.volumeDB == s.marketDB) && (s.modelsDB == nil || s.modelsDB == s.db || s.modelsDB == s.marketDB) {
				return report, nil
			}
		}
	}
	if s.alertsDB != nil && s.alertsDB != s.db && s.alertsDB != s.marketDB {
		alertsReport, alertsErr := compactDatabase(ctx, s.alertsDB, minimumFreeBytes)
		if alertsErr != nil {
			return report, alertsErr
		}
		report.Compacted = report.Compacted || alertsReport.Compacted
		report.FreeBytes += alertsReport.FreeBytes
		report.BytesBefore += alertsReport.BytesBefore
		report.BytesAfter += alertsReport.BytesAfter
		report.Elapsed += alertsReport.Elapsed
	}
	if s.optionsDB == nil || s.optionsDB == s.db || s.optionsDB == s.marketDB || s.optionsDB == s.alertsDB {
		if (s.volumeDB == nil || s.volumeDB == s.db || s.volumeDB == s.marketDB || s.volumeDB == s.alertsDB) && (s.modelsDB == nil || s.modelsDB == s.db || s.modelsDB == s.marketDB || s.modelsDB == s.alertsDB) {
			return report, nil
		}
	} else {
		optionsReport, err := compactDatabase(ctx, s.optionsDB, minimumFreeBytes)
		if err != nil {
			return report, err
		}
		report.Compacted = report.Compacted || optionsReport.Compacted
		report.FreeBytes += optionsReport.FreeBytes
		report.BytesBefore += optionsReport.BytesBefore
		report.BytesAfter += optionsReport.BytesAfter
		report.Elapsed += optionsReport.Elapsed
	}
	if s.volumeDB == nil || s.volumeDB == s.db || s.volumeDB == s.marketDB || s.volumeDB == s.alertsDB || s.volumeDB == s.optionsDB {
		if s.modelsDB == nil || s.modelsDB == s.db || s.modelsDB == s.marketDB || s.modelsDB == s.alertsDB || s.modelsDB == s.optionsDB {
			return report, nil
		}
	} else {
		volumeReport, err := compactDatabase(ctx, s.volumeDB, minimumFreeBytes)
		if err != nil {
			return report, err
		}
		report.Compacted = report.Compacted || volumeReport.Compacted
		report.FreeBytes += volumeReport.FreeBytes
		report.BytesBefore += volumeReport.BytesBefore
		report.BytesAfter += volumeReport.BytesAfter
		report.Elapsed += volumeReport.Elapsed
	}
	if s.modelsDB != nil && s.modelsDB != s.db && s.modelsDB != s.marketDB && s.modelsDB != s.alertsDB && s.modelsDB != s.optionsDB && s.modelsDB != s.volumeDB {
		modelsReport, err := compactDatabase(ctx, s.modelsDB, minimumFreeBytes)
		if err != nil {
			return report, err
		}
		report.Compacted = report.Compacted || modelsReport.Compacted
		report.FreeBytes += modelsReport.FreeBytes
		report.BytesBefore += modelsReport.BytesBefore
		report.BytesAfter += modelsReport.BytesAfter
		report.Elapsed += modelsReport.Elapsed
	}
	return report, nil
}

func compactDatabase(ctx context.Context, db *sql.DB, minimumFreeBytes int64) (CompactReport, error) {
	report := CompactReport{}
	started := time.Now().UTC()
	pageSize, pageCount, freePages, err := pageStats(ctx, db)
	if err != nil {
		return report, err
	}
	report.FreeBytes = freePages * pageSize
	report.BytesBefore = pageCount * pageSize
	if freePages*pageSize >= minimumFreeBytes {
		if _, err = db.ExecContext(ctx, `VACUUM`); err != nil {
			return report, err
		}
		report.Compacted = true
		if _, pageCount, _, err = pageStats(ctx, db); err != nil {
			return report, err
		}
		report.BytesAfter = pageCount * pageSize
	}
	if _, err = db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return report, err
	}
	// PRAGMA optimize lets SQLite refresh the query planner statistics it needs
	// after a day of deletions instead of waiting for the next table scan.
	if _, err = db.ExecContext(ctx, `PRAGMA optimize`); err != nil {
		return report, err
	}
	report.Elapsed = time.Since(started)
	return report, nil
}

func pageStats(ctx context.Context, db *sql.DB) (pageSize, pageCount, freePages int64, err error) {
	if err = db.QueryRowContext(ctx, `PRAGMA page_size`).Scan(&pageSize); err != nil {
		return 0, 0, 0, err
	}
	if err = db.QueryRowContext(ctx, `PRAGMA page_count`).Scan(&pageCount); err != nil {
		return 0, 0, 0, err
	}
	if err = db.QueryRowContext(ctx, `PRAGMA freelist_count`).Scan(&freePages); err != nil {
		return 0, 0, 0, err
	}
	return pageSize, pageCount, freePages, nil
}

func (s *Store) pageStats(ctx context.Context) (pageSize, pageCount, freePages int64, err error) {
	return pageStats(ctx, s.db)
}
