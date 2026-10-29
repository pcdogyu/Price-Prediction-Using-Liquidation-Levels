package storage

import (
	"context"
	"errors"
	"time"
)

const tvbotRetentionBatchSize = 2000

// PruneRetention removes TVBot rows older than their respective cutoffs. Both
// timestamp columns are stored as UTC RFC3339 text, so the cutoffs are
// formatted the same way as all current writers. Deletions are bounded to
// avoid holding SQLite's writer for the entire historical cleanup.
func (s *OrderStore) PruneRetention(ctx context.Context, tradeMonitorBefore, analysisCacheBefore time.Time) (tradeMonitorEvents, analysisCache int64, err error) {
	if s.db == nil {
		return 0, 0, errors.New("sqlite database is not configured")
	}
	tradeMonitorCutoff := tradeMonitorBefore.UTC().Format(time.RFC3339Nano)
	analysisCacheCutoff := analysisCacheBefore.UTC().Format(time.RFC3339Nano)
	s.mu.Lock()
	defer s.mu.Unlock()
	tradeMonitorEvents, err = s.pruneTextRowsLocked(ctx, "trade_monitor_events", "event_time", tradeMonitorCutoff)
	if err != nil {
		return tradeMonitorEvents, 0, err
	}
	analysisCache, err = s.pruneTextRowsLocked(ctx, "analysis_cache", "refreshed_at", analysisCacheCutoff)
	return tradeMonitorEvents, analysisCache, err
}

func (s *OrderStore) pruneTextRowsLocked(ctx context.Context, table, column, cutoff string) (int64, error) {
	var total int64
	query := `DELETE FROM ` + table + ` WHERE rowid IN (SELECT rowid FROM ` + table + ` WHERE ` + column + `<? ORDER BY ` + column + ` LIMIT ?)`
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		result, err := s.db.ExecContext(ctx, query, cutoff, tvbotRetentionBatchSize)
		if err != nil {
			return total, err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return total, err
		}
		total += count
		if count < tvbotRetentionBatchSize {
			return total, nil
		}
	}
}
