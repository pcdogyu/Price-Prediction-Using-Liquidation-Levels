package store

import (
	"context"
	"database/sql"
)

// migrateMarket creates only the tables served by the market database. The
// unified database keeps its historical copies during the rollback window;
// runtime market methods select this handle only after a committed split.
func (s *Store) migrateMarket(ctx context.Context) error {
	_, err := s.market().ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS candles(exchange TEXT NOT NULL,symbol TEXT NOT NULL,ts INTEGER NOT NULL,open REAL,high REAL,low REAL,close REAL,volume_usd REAL,taker_buy_usd REAL,open_interest_usd REAL,funding_rate REAL,long_short_ratio REAL,PRIMARY KEY(exchange,symbol,ts));
CREATE INDEX IF NOT EXISTS candles_retention_time ON candles(ts);
CREATE TABLE IF NOT EXISTS liquidations(id TEXT PRIMARY KEY,exchange TEXT NOT NULL,symbol TEXT NOT NULL,position_side TEXT NOT NULL,event_ts INTEGER NOT NULL,received_ts INTEGER NOT NULL,price REAL NOT NULL,quantity REAL NOT NULL,notional_usd REAL NOT NULL,coverage TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS liquidations_retention_time ON liquidations(event_ts);
CREATE INDEX IF NOT EXISTS liquidation_time_page ON liquidations(exchange,event_ts DESC,id DESC);
CREATE INDEX IF NOT EXISTS liquidation_symbol_page ON liquidations(exchange,symbol,event_ts DESC,id DESC);
CREATE TABLE IF NOT EXISTS wall_events(id TEXT PRIMARY KEY,symbol TEXT NOT NULL,ts INTEGER NOT NULL,ended_ts INTEGER,payload BLOB NOT NULL);
CREATE INDEX IF NOT EXISTS wall_event_time ON wall_events(symbol,ts DESC,id DESC);
CREATE INDEX IF NOT EXISTS wall_events_retention_time ON wall_events(ts);
CREATE TABLE IF NOT EXISTS book_snapshots(symbol TEXT NOT NULL,ts INTEGER NOT NULL,payload BLOB NOT NULL,PRIMARY KEY(symbol,ts));
CREATE INDEX IF NOT EXISTS book_snapshots_retention_time ON book_snapshots(ts);
CREATE TABLE IF NOT EXISTS market_metrics(symbol TEXT NOT NULL,ts INTEGER NOT NULL,payload BLOB NOT NULL,PRIMARY KEY(symbol,ts));
CREATE INDEX IF NOT EXISTS market_metrics_retention_time ON market_metrics(ts);`)
	return err
}

func (s *Store) databaseForTable(table string) *sql.DB {
	switch table {
	case "candles", "liquidations", "wall_events", "book_snapshots", "market_metrics":
		return s.market()
	case "liquidation_alerts", "trade_signals", "alert_watch_symbols", "alert_settings", "alert_strategy_settings", "alert_tenant_settings", "alert_tenant_strategy_settings":
		return s.alerts()
	case "gamma_latest", "deribit_gamma_history":
		return s.options()
	case "volume_profile_levels", "volume_profile_cursors", "volume_profile_snapshots", "volume_archive_imports":
		return s.volume()
	case "predictions", "backtests":
		return s.models()
	case "admin_menu", "user_menu", "user_theme", "background_symbol_snapshot":
		return s.settings()
	default:
		return s.db
	}
}
