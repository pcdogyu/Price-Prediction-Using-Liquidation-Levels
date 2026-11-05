package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestScheduledRunClaimIsPersistentAndIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scheduled-runs.db")
	now := time.Date(2026, time.November, 5, 6, 0, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60))
	store, err := NewSQLiteOrderStore(path, "")
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimScheduledRun(context.Background(), DailyPositionCloseRunKey, "2026-11-05", now)
	if err != nil || !claimed {
		t.Fatalf("first scheduled run claim = %v, %v", claimed, err)
	}
	claimed, err = store.ClaimScheduledRun(context.Background(), DailyPositionCloseRunKey, "2026-11-05", now.Add(time.Minute))
	if err != nil || claimed {
		t.Fatalf("duplicate scheduled run claim = %v, %v", claimed, err)
	}
	if err := store.FinishScheduledRun(context.Background(), DailyPositionCloseRunKey, "2026-11-05", "completed", "ok", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewSQLiteOrderStore(path, "")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	claimed, err = reopened.ClaimScheduledRun(context.Background(), DailyPositionCloseRunKey, "2026-11-05", now.Add(time.Hour))
	if err != nil || claimed {
		t.Fatalf("reopened duplicate scheduled run claim = %v, %v", claimed, err)
	}
	claimed, err = reopened.ClaimScheduledRun(context.Background(), DailyPositionCloseRunKey, "2026-11-06", now.Add(24*time.Hour))
	if err != nil || !claimed {
		t.Fatalf("next-day scheduled run claim = %v, %v", claimed, err)
	}
}
