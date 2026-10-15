package store

import (
	"context"
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
	report := CompactReport{}
	started := time.Now().UTC()
	pageSize, pageCount, freePages, err := s.pageStats(ctx)
	if err != nil {
		return report, err
	}
	report.FreeBytes = freePages * pageSize
	report.BytesBefore = pageCount * pageSize
	if freePages*pageSize >= minimumFreeBytes {
		if _, err = s.db.ExecContext(ctx, `VACUUM`); err != nil {
			return report, err
		}
		report.Compacted = true
		if _, pageCount, _, err = s.pageStats(ctx); err != nil {
			return report, err
		}
		report.BytesAfter = pageCount * pageSize
	}
	if _, err = s.db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return report, err
	}
	// PRAGMA optimize lets SQLite refresh the query planner statistics it needs
	// after a day of deletions instead of waiting for the next table scan.
	if _, err = s.db.ExecContext(ctx, `PRAGMA optimize`); err != nil {
		return report, err
	}
	report.Elapsed = time.Since(started)
	return report, nil
}

func (s *Store) pageStats(ctx context.Context) (pageSize, pageCount, freePages int64, err error) {
	if err = s.db.QueryRowContext(ctx, `PRAGMA page_size`).Scan(&pageSize); err != nil {
		return 0, 0, 0, err
	}
	if err = s.db.QueryRowContext(ctx, `PRAGMA page_count`).Scan(&pageCount); err != nil {
		return 0, 0, 0, err
	}
	if err = s.db.QueryRowContext(ctx, `PRAGMA freelist_count`).Scan(&freePages); err != nil {
		return 0, 0, 0, err
	}
	return pageSize, pageCount, freePages, nil
}
