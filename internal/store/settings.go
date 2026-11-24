package store

import "context"

// migrateSettingsState initializes only UI and background-state tables in a
// standalone settings database. The unified database keeps rollback copies.
func (s *Store) migrateSettingsState(ctx context.Context) error {
	_, err := s.settings().ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS admin_menu(id INTEGER PRIMARY KEY CHECK(id=1),payload BLOB NOT NULL,updated_ts INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS user_menu(username TEXT PRIMARY KEY,payload BLOB NOT NULL,updated_ts INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS user_theme(username TEXT PRIMARY KEY,theme TEXT NOT NULL,updated_ts INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS user_chart_signal_preferences(username TEXT PRIMARY KEY,tvbot_signals INTEGER NOT NULL DEFAULT 1,warning_signals INTEGER NOT NULL DEFAULT 1,aggressive_signals INTEGER NOT NULL DEFAULT 1,steady_signals INTEGER NOT NULL DEFAULT 1,value_area_signals INTEGER NOT NULL DEFAULT 1,actual_trades INTEGER NOT NULL DEFAULT 1,up_candles INTEGER NOT NULL DEFAULT 1,down_candles INTEGER NOT NULL DEFAULT 1,long_liquidations INTEGER NOT NULL DEFAULT 1,short_liquidations INTEGER NOT NULL DEFAULT 1,volume_profile INTEGER NOT NULL DEFAULT 1,top_volume_bins INTEGER NOT NULL DEFAULT 1,leverage_10x INTEGER NOT NULL DEFAULT 1,leverage_25x INTEGER NOT NULL DEFAULT 1,leverage_50x INTEGER NOT NULL DEFAULT 1,leverage_100x INTEGER NOT NULL DEFAULT 1,val_line INTEGER NOT NULL DEFAULT 1,vah_line INTEGER NOT NULL DEFAULT 1,model_signals INTEGER NOT NULL DEFAULT 1,updated_ts INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS background_symbol_snapshot(id INTEGER PRIMARY KEY CHECK(id=1),payload BLOB NOT NULL,updated_ts INTEGER NOT NULL);`)
	if err != nil {
		return err
	}
	return s.migrateUserChartSignalPreferenceColumns(ctx)
}

func (s *Store) migrateUserChartSignalPreferenceColumns(ctx context.Context) error {
	for _, column := range []string{
		"up_candles", "down_candles", "long_liquidations", "short_liquidations",
		"volume_profile", "top_volume_bins", "leverage_10x", "leverage_25x",
		"leverage_50x", "leverage_100x", "val_line", "vah_line", "model_signals",
	} {
		if hasColumnOn(ctx, s.settings(), "user_chart_signal_preferences", column) {
			continue
		}
		if _, err := s.settings().ExecContext(ctx, `ALTER TABLE user_chart_signal_preferences ADD COLUMN `+column+` INTEGER NOT NULL DEFAULT 1`); err != nil {
			return err
		}
	}
	return nil
}
