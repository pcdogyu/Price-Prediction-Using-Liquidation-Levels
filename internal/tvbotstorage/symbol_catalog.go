package storage

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

type SymbolCatalogCache struct {
	Exchange    string    `json:"exchange"`
	Env         string    `json:"env"`
	PayloadJSON string    `json:"payload_json"`
	Count       int       `json:"count"`
	SyncedAt    time.Time `json:"synced_at,omitempty"`
	AttemptedAt time.Time `json:"attempted_at"`
	Error       string    `json:"error,omitempty"`
	TickerError string    `json:"ticker_error,omitempty"`
}

func (s *OrderStore) UpsertSymbolCatalogCaches(items []SymbolCatalogCache) error {
	if s.db == nil {
		return errors.New("sqlite database is not configured")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT INTO symbol_catalog_cache
		(exchange, env, payload_json, count, synced_at, attempted_at, error, ticker_error)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(exchange, env) DO UPDATE SET
			payload_json=excluded.payload_json,
			count=excluded.count,
			synced_at=excluded.synced_at,
			attempted_at=excluded.attempted_at,
			error=excluded.error,
			ticker_error=excluded.ticker_error`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, item := range items {
		item = normalizeSymbolCatalogCache(item)
		var syncedAt any
		if !item.SyncedAt.IsZero() {
			syncedAt = item.SyncedAt.UTC().Format(time.RFC3339Nano)
		}
		if _, err := stmt.Exec(
			item.Exchange,
			item.Env,
			item.PayloadJSON,
			item.Count,
			syncedAt,
			item.AttemptedAt.UTC().Format(time.RFC3339Nano),
			item.Error,
			item.TickerError,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *OrderStore) ListSymbolCatalogCaches() ([]SymbolCatalogCache, error) {
	if s.db == nil {
		return nil, errors.New("sqlite database is not configured")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT exchange, env, payload_json, count, synced_at, attempted_at, error, ticker_error
		FROM symbol_catalog_cache ORDER BY exchange ASC, env ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SymbolCatalogCache
	for rows.Next() {
		item, err := scanSymbolCatalogCache(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func normalizeSymbolCatalogCache(item SymbolCatalogCache) SymbolCatalogCache {
	item.Exchange = trading.NormalizeExchange(item.Exchange)
	item.Env = normalizeSymbolCatalogEnv(item.Env)
	item.PayloadJSON = strings.TrimSpace(item.PayloadJSON)
	if item.PayloadJSON == "" {
		item.PayloadJSON = "{}"
	}
	if item.Count < 0 {
		item.Count = 0
	}
	if item.AttemptedAt.IsZero() {
		item.AttemptedAt = time.Now().UTC()
	}
	item.SyncedAt = item.SyncedAt.UTC()
	item.AttemptedAt = item.AttemptedAt.UTC()
	item.Error = strings.TrimSpace(item.Error)
	item.TickerError = strings.TrimSpace(item.TickerError)
	return item
}

func normalizeSymbolCatalogEnv(env string) string {
	switch trading.NormalizeTradeEnv(env) {
	case trading.TradeEnvLive:
		return trading.TradeEnvLive
	default:
		return trading.TradeEnvDemo
	}
}

func scanSymbolCatalogCache(scanner interface{ Scan(dest ...any) error }) (SymbolCatalogCache, error) {
	var item SymbolCatalogCache
	var syncedAt, attemptedAt sql.NullString
	var errorText, tickerError sql.NullString
	if err := scanner.Scan(
		&item.Exchange,
		&item.Env,
		&item.PayloadJSON,
		&item.Count,
		&syncedAt,
		&attemptedAt,
		&errorText,
		&tickerError,
	); err != nil {
		return SymbolCatalogCache{}, err
	}
	item.Exchange = trading.NormalizeExchange(item.Exchange)
	item.Env = normalizeSymbolCatalogEnv(item.Env)
	if errorText.Valid {
		item.Error = errorText.String
	}
	if tickerError.Valid {
		item.TickerError = tickerError.String
	}
	if syncedAt.Valid && strings.TrimSpace(syncedAt.String) != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, syncedAt.String); err == nil {
			item.SyncedAt = parsed.UTC()
		}
	}
	if attemptedAt.Valid && strings.TrimSpace(attemptedAt.String) != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, attemptedAt.String); err == nil {
			item.AttemptedAt = parsed.UTC()
		}
	}
	return item, nil
}
