package store

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
)

func TestUserChartSignalPreferencesDefaultsPatchAndIsolation(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "chart-preferences.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	defaults, err := s.LoadUserChartSignalPreferences(ctx, "alice")
	if err != nil || !defaults.TVBotSignals || !defaults.WarningSignals || !defaults.AggressiveSignals || !defaults.SteadySignals || !defaults.ValueAreaSignals || !defaults.ActualTrades || defaults.UpdatedAt != 0 {
		t.Fatalf("default preferences=%+v err=%v", defaults, err)
	}

	updated, err := s.PatchUserChartSignalPreference(ctx, " alice ", "tvbot_signals", false)
	if err != nil || updated.TVBotSignals || !updated.WarningSignals || updated.UpdatedAt == 0 {
		t.Fatalf("first patch preferences=%+v err=%v", updated, err)
	}
	second, err := s.PatchUserChartSignalPreference(ctx, "alice", "steady_signals", false)
	if err != nil || second.TVBotSignals || second.SteadySignals || !second.AggressiveSignals || second.UpdatedAt <= updated.UpdatedAt {
		t.Fatalf("second patch preferences=%+v err=%v", second, err)
	}

	other, err := s.PatchUserChartSignalPreference(ctx, "bob", "actual_trades", false)
	if err != nil || other.ActualTrades || !other.TVBotSignals {
		t.Fatalf("other user preferences=%+v err=%v", other, err)
	}
	loaded, err := s.LoadUserChartSignalPreferences(ctx, "alice")
	if err != nil || loaded.ActualTrades != true || loaded.TVBotSignals || loaded.SteadySignals {
		t.Fatalf("alice preferences after bob patch=%+v err=%v", loaded, err)
	}
	if _, err := s.PatchUserChartSignalPreference(ctx, "alice", "unknown", false); err == nil {
		t.Fatal("unknown preference key was accepted")
	}
	if _, err := s.LoadUserChartSignalPreferences(ctx, " "); err == nil {
		t.Fatal("empty username was accepted")
	}
}

func TestConcurrentUserChartSignalPreferencePatchesKeepOtherKeys(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "concurrent-chart-preferences.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	var wg sync.WaitGroup
	errCh := make(chan error, 2)
	for _, key := range []string{"warning_signals", "value_area_signals"} {
		key := key
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, patchErr := s.PatchUserChartSignalPreference(ctx, "alice", key, false)
			errCh <- patchErr
		}()
	}
	wg.Wait()
	close(errCh)
	for patchErr := range errCh {
		if patchErr != nil {
			t.Fatal(patchErr)
		}
	}
	preferences, err := s.LoadUserChartSignalPreferences(ctx, "alice")
	if err != nil || preferences.WarningSignals || preferences.ValueAreaSignals || !preferences.AggressiveSignals || !preferences.SteadySignals {
		t.Fatalf("concurrent patches lost state: %+v err=%v", preferences, err)
	}
}
