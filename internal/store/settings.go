package store

import "context"

// migrateSettingsState initializes only UI and background-state tables in a
// standalone settings database. The unified database keeps rollback copies.
func (s *Store) migrateSettingsState(ctx context.Context) error {
	_, err := s.settings().ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS admin_menu(id INTEGER PRIMARY KEY CHECK(id=1),payload BLOB NOT NULL,updated_ts INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS user_menu(username TEXT PRIMARY KEY,payload BLOB NOT NULL,updated_ts INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS user_theme(username TEXT PRIMARY KEY,theme TEXT NOT NULL,updated_ts INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS background_symbol_snapshot(id INTEGER PRIMARY KEY CHECK(id=1),payload BLOB NOT NULL,updated_ts INTEGER NOT NULL);`)
	return err
}
