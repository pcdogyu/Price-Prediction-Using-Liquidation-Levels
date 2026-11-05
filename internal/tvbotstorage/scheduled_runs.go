package storage

import (
	"context"
	"errors"
	"strings"
	"time"
)

const DailyPositionCloseRunKey = "daily_position_close"

// ClaimScheduledRun atomically claims one scheduled operation for a local
// calendar date. A successful claim is intentionally never retried for the
// same date: the caller records the result, while the next day gets a new key.
func (s *OrderStore) ClaimScheduledRun(ctx context.Context, runKey, runDate string, now time.Time) (bool, error) {
	if s == nil {
		return false, errors.New("order store is nil")
	}
	runKey = strings.TrimSpace(runKey)
	runDate = strings.TrimSpace(runDate)
	if runKey == "" || runDate == "" {
		return false, errors.New("scheduled run key and date are required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	now = now.UTC()

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		result, err := s.db.ExecContext(ctx, `
			INSERT OR IGNORE INTO tvbot_scheduled_runs
				(run_key, run_date, status, started_at)
			VALUES (?, ?, 'running', ?)`, runKey, runDate, now.Format(time.RFC3339Nano))
		if err != nil {
			return false, err
		}
		rows, err := result.RowsAffected()
		return rows == 1, err
	}
	if s.state.ScheduledRuns == nil {
		s.state.ScheduledRuns = map[string]bool{}
	}
	key := runKey + "|" + runDate
	if s.state.ScheduledRuns[key] {
		return false, nil
	}
	s.state.ScheduledRuns[key] = true
	return true, s.saveLocked()
}

// FinishScheduledRun stores the outcome of a claimed scheduled operation for
// diagnostics without changing its once-per-date claim.
func (s *OrderStore) FinishScheduledRun(ctx context.Context, runKey, runDate, status, message string, finishedAt time.Time) error {
	if s == nil {
		return errors.New("order store is nil")
	}
	runKey = strings.TrimSpace(runKey)
	runDate = strings.TrimSpace(runDate)
	status = strings.TrimSpace(status)
	if runKey == "" || runDate == "" || status == "" {
		return errors.New("scheduled run key, date and status are required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if s.db == nil {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE tvbot_scheduled_runs
		SET status = ?, finished_at = ?, message = ?
		WHERE run_key = ? AND run_date = ?`,
		status, finishedAt.UTC().Format(time.RFC3339Nano), strings.TrimSpace(message), runKey, runDate)
	return err
}
