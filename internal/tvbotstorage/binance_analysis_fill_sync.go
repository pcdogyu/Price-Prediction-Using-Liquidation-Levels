package storage

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

type BinanceAnalysisFillSyncState struct {
	APIID          string
	Symbol         string
	CoveredSinceMS int64
	LastSyncedMS   int64
	UpdatedAt      time.Time
}

func (s *OrderStore) LoadBinanceAnalysisFillSyncState(apiID, symbol string) (BinanceAnalysisFillSyncState, bool, error) {
	if s == nil || s.db == nil {
		return BinanceAnalysisFillSyncState{}, false, errors.New("sqlite database is not configured")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var state BinanceAnalysisFillSyncState
	var updatedAt string
	err := s.db.QueryRow(`SELECT api_id,symbol,covered_since_ms,last_synced_ms,updated_at FROM binance_analysis_fill_sync_state WHERE api_id=? AND symbol=?`, strings.TrimSpace(apiID), strings.ToUpper(strings.TrimSpace(symbol))).Scan(&state.APIID, &state.Symbol, &state.CoveredSinceMS, &state.LastSyncedMS, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return BinanceAnalysisFillSyncState{}, false, nil
	}
	if err != nil {
		return BinanceAnalysisFillSyncState{}, false, err
	}
	state.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	return state, err == nil, err
}

func (s *OrderStore) SaveBinanceAnalysisFillSyncState(state BinanceAnalysisFillSyncState) error {
	if s == nil || s.db == nil {
		return errors.New("sqlite database is not configured")
	}
	state.APIID = strings.TrimSpace(state.APIID)
	state.Symbol = strings.ToUpper(strings.TrimSpace(state.Symbol))
	if state.APIID == "" || state.Symbol == "" {
		return errors.New("Binance analysis fill sync state requires account and symbol")
	}
	if state.UpdatedAt.IsZero() {
		state.UpdatedAt = time.Now().UTC()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`INSERT INTO binance_analysis_fill_sync_state(api_id,symbol,covered_since_ms,last_synced_ms,updated_at)
		VALUES(?,?,?,?,?) ON CONFLICT(api_id,symbol) DO UPDATE SET
		covered_since_ms=excluded.covered_since_ms,last_synced_ms=excluded.last_synced_ms,updated_at=excluded.updated_at`,
		state.APIID, state.Symbol, state.CoveredSinceMS, state.LastSyncedMS, state.UpdatedAt.UTC().Format(time.RFC3339Nano))
	return err
}
