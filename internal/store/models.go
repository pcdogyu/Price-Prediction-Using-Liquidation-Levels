package store

import "context"

// migrateModelResults initializes only the tables served by a standalone
// model-result database. The unified database keeps its historical copies
// during the rollback window.
func (s *Store) migrateModelResults(ctx context.Context) error {
	_, err := s.models().ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS predictions(symbol TEXT NOT NULL,ts INTEGER NOT NULL,source TEXT NOT NULL DEFAULT '',payload BLOB NOT NULL,PRIMARY KEY(symbol,ts));
CREATE INDEX IF NOT EXISTS predictions_retention_time ON predictions(ts);
CREATE INDEX IF NOT EXISTS predictions_source_symbol_ts ON predictions(source,symbol,ts);
CREATE TABLE IF NOT EXISTS backtests(symbol TEXT PRIMARY KEY,payload BLOB NOT NULL,updated_ts INTEGER NOT NULL);`)
	return err
}
