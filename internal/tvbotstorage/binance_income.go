package storage

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type BinanceIncomeRecord struct {
	IncomeKey  string `json:"income_key"`
	Symbol     string `json:"symbol,omitempty"`
	IncomeType string `json:"incomeType"`
	Income     string `json:"income"`
	Asset      string `json:"asset,omitempty"`
	Info       string `json:"info,omitempty"`
	TranID     string `json:"tranId,omitempty"`
	TradeID    string `json:"tradeId,omitempty"`
	Time       int64  `json:"time"`
}

type BinanceIncomeSyncState struct {
	CoveredSinceMS int64
	LastSyncedMS   int64
	UpdatedAt      time.Time
}

func (s *OrderStore) LoadBinanceIncomeSyncState(apiID, env string) (BinanceIncomeSyncState, bool, error) {
	if s == nil || s.db == nil {
		return BinanceIncomeSyncState{}, false, errors.New("sqlite database is not configured")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var state BinanceIncomeSyncState
	var updatedAt string
	err := s.db.QueryRow(`SELECT covered_since_ms,last_synced_ms,updated_at FROM binance_income_sync_state WHERE api_id=? AND env=?`, strings.TrimSpace(apiID), strings.TrimSpace(env)).Scan(&state.CoveredSinceMS, &state.LastSyncedMS, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return BinanceIncomeSyncState{}, false, nil
	}
	if err != nil {
		return BinanceIncomeSyncState{}, false, err
	}
	state.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	return state, err == nil, err
}

func (s *OrderStore) SaveBinanceIncomeSync(apiID, env string, rows []BinanceIncomeRecord, state BinanceIncomeSyncState, fetchedAt time.Time) error {
	if s == nil || s.db == nil {
		return errors.New("sqlite database is not configured")
	}
	apiID, env = strings.TrimSpace(apiID), strings.TrimSpace(env)
	fetched := fetchedAt.UTC().Format(time.RFC3339Nano)
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT INTO binance_income
		(api_id,env,income_key,symbol,income_type,income,asset,info,tran_id,trade_id,time_ms,fetched_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(api_id,env,income_key) DO UPDATE SET
			symbol=excluded.symbol,income_type=excluded.income_type,income=excluded.income,
			asset=excluded.asset,info=excluded.info,tran_id=excluded.tran_id,
			trade_id=excluded.trade_id,time_ms=excluded.time_ms,fetched_at=excluded.fetched_at`)
	if err != nil {
		return err
	}
	for _, row := range rows {
		key := strings.TrimSpace(row.IncomeKey)
		if key == "" {
			return errors.New("Binance income record has no deduplication key")
		}
		if _, err := stmt.Exec(apiID, env, key, strings.TrimSpace(row.Symbol), strings.ToUpper(strings.TrimSpace(row.IncomeType)), strings.TrimSpace(row.Income), strings.TrimSpace(row.Asset), strings.TrimSpace(row.Info), strings.TrimSpace(row.TranID), strings.TrimSpace(row.TradeID), row.Time, fetched); err != nil {
			_ = stmt.Close()
			return err
		}
	}
	if err := stmt.Close(); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO binance_income_sync_state(api_id,env,covered_since_ms,last_synced_ms,updated_at)
		VALUES(?,?,?,?,?) ON CONFLICT(api_id,env) DO UPDATE SET
		covered_since_ms=excluded.covered_since_ms,last_synced_ms=excluded.last_synced_ms,updated_at=excluded.updated_at`, apiID, env, state.CoveredSinceMS, state.LastSyncedMS, fetched); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *OrderStore) ListBinanceIncome(apiID, env string, since, until time.Time) ([]BinanceIncomeRecord, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("sqlite database is not configured")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT income_key,symbol,income_type,income,asset,info,tran_id,trade_id,time_ms FROM binance_income
		WHERE api_id=? AND env=? AND time_ms>=? AND time_ms<=? ORDER BY time_ms DESC,income_type,tran_id DESC`, strings.TrimSpace(apiID), strings.TrimSpace(env), since.UTC().UnixMilli(), until.UTC().UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]BinanceIncomeRecord, 0)
	for rows.Next() {
		var row BinanceIncomeRecord
		if err := rows.Scan(&row.IncomeKey, &row.Symbol, &row.IncomeType, &row.Income, &row.Asset, &row.Info, &row.TranID, &row.TradeID, &row.Time); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read Binance income rows: %w", err)
	}
	return out, nil
}
