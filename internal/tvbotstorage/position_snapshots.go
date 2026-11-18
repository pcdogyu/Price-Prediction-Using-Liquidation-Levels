package storage

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

// USDTPositionSnapshot stores the position state paired with a balance sample.
// PayloadJSON contains only normalized, non-secret position fields.
type USDTPositionSnapshot struct {
	Exchange    string    `json:"exchange"`
	APIID       string    `json:"api_id"`
	Env         string    `json:"env"`
	BucketTS    int64     `json:"bucket_ts"`
	ObservedAt  time.Time `json:"observed_at"`
	Status      string    `json:"status"`
	PayloadJSON string    `json:"payload_json"`
}

func (s *OrderStore) UpsertUSDTPositionSnapshot(snapshot USDTPositionSnapshot) error {
	if s.db == nil {
		return errors.New("sqlite database is not configured")
	}
	snapshot.Exchange = strings.ToLower(strings.TrimSpace(snapshot.Exchange))
	snapshot.APIID = strings.TrimSpace(snapshot.APIID)
	snapshot.Env = strings.ToLower(strings.TrimSpace(snapshot.Env))
	snapshot.Status = strings.ToLower(strings.TrimSpace(snapshot.Status))
	if snapshot.Exchange == "" || snapshot.APIID == "" || snapshot.Env == "" {
		return errors.New("exchange, api_id and env are required")
	}
	if snapshot.Status == "" {
		snapshot.Status = "unavailable"
	}
	if snapshot.PayloadJSON == "" {
		snapshot.PayloadJSON = "[]"
	}
	if snapshot.ObservedAt.IsZero() {
		snapshot.ObservedAt = time.Now().UTC()
	}
	snapshot.ObservedAt = snapshot.ObservedAt.UTC()
	if snapshot.BucketTS <= 0 {
		snapshot.BucketTS = snapshot.ObservedAt.Truncate(usdtBalanceSnapshotBucket).UnixMilli()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`INSERT INTO usdt_position_snapshots
		(exchange, api_id, env, bucket_ts, observed_at, status, payload_json)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(exchange, api_id, env, bucket_ts) DO UPDATE SET
			observed_at=excluded.observed_at,
			status=excluded.status,
			payload_json=excluded.payload_json`,
		snapshot.Exchange,
		snapshot.APIID,
		snapshot.Env,
		snapshot.BucketTS,
		snapshot.ObservedAt.Format(time.RFC3339Nano),
		snapshot.Status,
		snapshot.PayloadJSON,
	)
	return err
}

func (s *OrderStore) GetUSDTPositionSnapshot(exchange, apiID, env string, bucketTS int64) (USDTPositionSnapshot, error) {
	if s.db == nil {
		return USDTPositionSnapshot{}, errors.New("sqlite database is not configured")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var snapshot USDTPositionSnapshot
	var observedAt string
	err := s.db.QueryRow(`SELECT exchange, api_id, env, bucket_ts, observed_at, status, payload_json
		FROM usdt_position_snapshots
		WHERE exchange = ? AND api_id = ? AND env = ? AND bucket_ts = ?`,
		strings.ToLower(strings.TrimSpace(exchange)),
		strings.TrimSpace(apiID),
		strings.ToLower(strings.TrimSpace(env)),
		bucketTS,
	).Scan(
		&snapshot.Exchange,
		&snapshot.APIID,
		&snapshot.Env,
		&snapshot.BucketTS,
		&observedAt,
		&snapshot.Status,
		&snapshot.PayloadJSON,
	)
	if err != nil {
		return USDTPositionSnapshot{}, err
	}
	if observedAt != "" {
		snapshot.ObservedAt, err = time.Parse(time.RFC3339Nano, observedAt)
		if err != nil {
			return USDTPositionSnapshot{}, err
		}
	}
	return snapshot, nil
}

// FindExchangeUSDTBalancePeak returns the highest stored chart equity point.
// An empty since value means the whole recorded history. Equal values resolve
// to the earliest sample so that the first matching position state is stable.
func (s *OrderStore) FindExchangeUSDTBalancePeak(exchange, apiID, env string, since time.Time) (USDTBalanceSnapshot, error) {
	if s.db == nil {
		return USDTBalanceSnapshot{}, errors.New("sqlite database is not configured")
	}
	query := `SELECT exchange, api_id, env, bucket_ts, observed_at, currency, eq_source, total_eq, eq, eq_usd, avail_eq, avail_bal, cash_bal, frozen_bal, dis_eq, balance_updated_at
		FROM usdt_balance_snapshots
		WHERE exchange = ? AND api_id = ? AND env = ? AND TRIM(COALESCE(eq, '')) <> ''`
	args := []any{
		strings.ToLower(strings.TrimSpace(exchange)),
		strings.TrimSpace(apiID),
		strings.ToLower(strings.TrimSpace(env)),
	}
	if !since.IsZero() {
		query += ` AND bucket_ts >= ?`
		args = append(args, since.UTC().UnixMilli())
	}
	query += ` ORDER BY CAST(eq AS REAL) DESC, bucket_ts ASC LIMIT 1`

	s.mu.Lock()
	defer s.mu.Unlock()
	var snapshot USDTBalanceSnapshot
	var observedAt string
	err := s.db.QueryRow(query, args...).Scan(
		&snapshot.Exchange,
		&snapshot.APIID,
		&snapshot.Env,
		&snapshot.BucketTS,
		&observedAt,
		&snapshot.Currency,
		&snapshot.EqSource,
		&snapshot.TotalEq,
		&snapshot.Eq,
		&snapshot.EqUsd,
		&snapshot.AvailEq,
		&snapshot.AvailBal,
		&snapshot.CashBal,
		&snapshot.FrozenBal,
		&snapshot.DisEq,
		&snapshot.BalanceUpdatedAt,
	)
	if err != nil {
		return USDTBalanceSnapshot{}, err
	}
	if observedAt != "" {
		snapshot.ObservedAt, err = time.Parse(time.RFC3339Nano, observedAt)
		if err != nil {
			return USDTBalanceSnapshot{}, err
		}
	}
	return snapshot, nil
}

func IsMissingUSDTPositionSnapshot(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}
