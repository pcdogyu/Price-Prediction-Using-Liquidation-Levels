package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

// SaveBackgroundSymbolSnapshot stores the last successfully built candidate
// set. It contains only symbols and ranking metadata, never tenant secrets.
func (s *Store) SaveBackgroundSymbolSnapshot(ctx context.Context, snapshot domain.BackgroundSymbolSnapshot) error {
	if s == nil || s.db == nil {
		return errors.New("store is not configured")
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO background_symbol_snapshot(id,payload,updated_ts) VALUES(1,?,?)
		ON CONFLICT(id) DO UPDATE SET payload=excluded.payload,updated_ts=excluded.updated_ts`, payload, snapshot.RefreshedAt.UTC().UnixMilli())
	return err
}

// LoadBackgroundSymbolSnapshot returns the last successful candidate set.
func (s *Store) LoadBackgroundSymbolSnapshot(ctx context.Context) (domain.BackgroundSymbolSnapshot, bool, error) {
	if s == nil || s.db == nil {
		return domain.BackgroundSymbolSnapshot{}, false, errors.New("store is not configured")
	}
	var payload []byte
	err := s.db.QueryRowContext(ctx, `SELECT payload FROM background_symbol_snapshot WHERE id=1`).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.BackgroundSymbolSnapshot{}, false, nil
	}
	if err != nil {
		return domain.BackgroundSymbolSnapshot{}, false, err
	}
	var snapshot domain.BackgroundSymbolSnapshot
	if err := json.Unmarshal(payload, &snapshot); err != nil {
		return domain.BackgroundSymbolSnapshot{}, false, err
	}
	return snapshot, true, nil
}
