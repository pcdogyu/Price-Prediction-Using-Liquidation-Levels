package store

import "context"

// migrateVolumeProfile initializes only the tables served by a standalone
// volume-profile database. The unified database keeps its historical copies
// during the rollback window.
func (s *Store) migrateVolumeProfile(ctx context.Context) error {
	_, err := s.volume().ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS volume_profile_levels(symbol TEXT NOT NULL,session_ts INTEGER NOT NULL,price_key TEXT NOT NULL,price REAL NOT NULL,volume_usd REAL NOT NULL,trade_count INTEGER NOT NULL,last_trade_ts INTEGER NOT NULL,PRIMARY KEY(symbol,session_ts,price_key));
CREATE INDEX IF NOT EXISTS volume_profile_levels_retention_time ON volume_profile_levels(session_ts);
CREATE TABLE IF NOT EXISTS volume_profile_cursors(symbol TEXT NOT NULL,session_ts INTEGER NOT NULL,last_agg_id INTEGER NOT NULL,last_trade_ts INTEGER NOT NULL,complete INTEGER NOT NULL,updated_ts INTEGER NOT NULL,PRIMARY KEY(symbol,session_ts));
CREATE INDEX IF NOT EXISTS volume_profile_cursors_retention_time ON volume_profile_cursors(session_ts);
CREATE TABLE IF NOT EXISTS volume_profile_snapshots(symbol TEXT NOT NULL,ts INTEGER NOT NULL,session_ts INTEGER NOT NULL,val REAL NOT NULL,vah REAL NOT NULL,total_volume_usd REAL NOT NULL,complete INTEGER NOT NULL,PRIMARY KEY(symbol,ts));
CREATE INDEX IF NOT EXISTS volume_profile_snapshots_symbol_ts ON volume_profile_snapshots(symbol,ts);
CREATE INDEX IF NOT EXISTS volume_profile_snapshots_retention_time ON volume_profile_snapshots(ts);
CREATE TABLE IF NOT EXISTS volume_archive_imports(symbol TEXT NOT NULL,day TEXT NOT NULL,status TEXT NOT NULL,error TEXT NOT NULL DEFAULT '',updated_ts INTEGER NOT NULL,PRIMARY KEY(symbol,day));
CREATE INDEX IF NOT EXISTS volume_archive_imports_retention_day ON volume_archive_imports(day);`)
	return err
}
