package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"

	_ "modernc.org/sqlite"
)

func TestUserChartSignalPreferencesDefaultsPatchAndIsolation(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "chart-preferences.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	defaults, err := s.LoadUserChartSignalPreferences(ctx, "alice")
	if err != nil || !defaults.TVBotSignals || !defaults.WarningSignals || !defaults.AggressiveSignals || !defaults.SteadySignals || !defaults.ValueAreaSignals || !defaults.ActualTrades || !defaults.UpCandles || !defaults.DownCandles || !defaults.LongLiquidations || !defaults.ShortLiquidations || !defaults.VolumeProfile || !defaults.TopVolumeBins || !defaults.Leverage10x || !defaults.Leverage25x || !defaults.Leverage50x || !defaults.Leverage100x || !defaults.ValLine || !defaults.VahLine || !defaults.ModelSignals || defaults.UpdatedAt != 0 {
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
	second, err = s.PatchUserChartSignalPreference(ctx, "alice", "leverage_25x", false)
	if err != nil || second.Leverage25x || !second.Leverage10x || second.UpdatedAt <= updated.UpdatedAt {
		t.Fatalf("new preference patch=%+v err=%v", second, err)
	}

	other, err := s.PatchUserChartSignalPreference(ctx, "bob", "actual_trades", false)
	if err != nil || other.ActualTrades || !other.TVBotSignals {
		t.Fatalf("other user preferences=%+v err=%v", other, err)
	}
	loaded, err := s.LoadUserChartSignalPreferences(ctx, "alice")
	if err != nil || loaded.ActualTrades != true || loaded.TVBotSignals || loaded.SteadySignals || loaded.Leverage25x {
		t.Fatalf("alice preferences after bob patch=%+v err=%v", loaded, err)
	}
	if _, err := s.PatchUserChartSignalPreference(ctx, "alice", "unknown", false); err == nil {
		t.Fatal("unknown preference key was accepted")
	}
	if _, err := s.LoadUserChartSignalPreferences(ctx, " "); err == nil {
		t.Fatal("empty username was accepted")
	}
}

func TestLegacyChartPreferenceRowsGainNewDefaultEnabledColumns(t *testing.T) {
	ctx := context.Background()
	for _, separateSettings := range []bool{false, true} {
		name := "unified"
		if separateSettings {
			name = "separate-settings"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			legacyPath := filepath.Join(dir, "legacy.db")
			legacy, err := sql.Open("sqlite", legacyPath)
			if err != nil {
				t.Fatal(err)
			}
			_, err = legacy.Exec(`CREATE TABLE user_chart_signal_preferences(username TEXT PRIMARY KEY,tvbot_signals INTEGER NOT NULL DEFAULT 1,warning_signals INTEGER NOT NULL DEFAULT 1,aggressive_signals INTEGER NOT NULL DEFAULT 1,steady_signals INTEGER NOT NULL DEFAULT 1,value_area_signals INTEGER NOT NULL DEFAULT 1,actual_trades INTEGER NOT NULL DEFAULT 1,updated_ts INTEGER NOT NULL DEFAULT 0);
INSERT INTO user_chart_signal_preferences VALUES('alice',1,0,1,1,1,1,17);`)
			if err != nil {
				_ = legacy.Close()
				t.Fatal(err)
			}
			if err := legacy.Close(); err != nil {
				t.Fatal(err)
			}

			var s *Store
			if separateSettings {
				s, err = OpenWithMarketAndAlertsAndOptionsAndVolumeAndModelsAndSettingsDatabase(filepath.Join(dir, "main.db"), "", "", "", "", "", legacyPath)
			} else {
				s, err = Open(legacyPath)
			}
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			preferences, err := s.LoadUserChartSignalPreferences(ctx, "alice")
			if err != nil || preferences.WarningSignals || preferences.UpdatedAt != 17 || !preferences.UpCandles || !preferences.DownCandles || !preferences.LongLiquidations || !preferences.ShortLiquidations || !preferences.VolumeProfile || !preferences.TopVolumeBins || !preferences.Leverage10x || !preferences.Leverage25x || !preferences.Leverage50x || !preferences.Leverage100x || !preferences.ValLine || !preferences.VahLine || !preferences.ModelSignals {
				t.Fatalf("legacy preferences were not preserved/upgraded: %+v err=%v", preferences, err)
			}
		})
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
