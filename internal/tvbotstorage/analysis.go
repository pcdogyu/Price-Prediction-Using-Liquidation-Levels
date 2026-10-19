package storage

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

type MarketCandle struct {
	InstID  string `json:"inst_id"`
	Bar     string `json:"bar"`
	TS      int64  `json:"ts"`
	Open    string `json:"open"`
	High    string `json:"high"`
	Low     string `json:"low"`
	Close   string `json:"close"`
	Volume  string `json:"volume,omitempty"`
	Confirm string `json:"confirm,omitempty"`
}

type OKXFill struct {
	APIID    string `json:"api_id"`
	InstType string `json:"inst_type"`
	InstID   string `json:"inst_id"`
	TradeID  string `json:"trade_id"`
	OrdID    string `json:"ord_id,omitempty"`
	ClOrdID  string `json:"cl_ord_id,omitempty"`
	Side     string `json:"side,omitempty"`
	FillPx   string `json:"fill_px,omitempty"`
	FillSz   string `json:"fill_sz,omitempty"`
	FillPnl  string `json:"fill_pnl,omitempty"`
	Fee      string `json:"fee,omitempty"`
	FeeCcy   string `json:"fee_ccy,omitempty"`
	FillTime int64  `json:"fill_time"`
	RawJSON  string `json:"raw_json,omitempty"`
}

type USDTBalanceSnapshot struct {
	Exchange         string    `json:"exchange,omitempty"`
	APIID            string    `json:"api_id"`
	Env              string    `json:"env"`
	BucketTS         int64     `json:"bucket_ts"`
	ObservedAt       time.Time `json:"observed_at"`
	TotalEq          string    `json:"total_eq,omitempty"`
	Eq               string    `json:"eq,omitempty"`
	EqUsd            string    `json:"eq_usd,omitempty"`
	AvailEq          string    `json:"avail_eq,omitempty"`
	AvailBal         string    `json:"avail_bal,omitempty"`
	CashBal          string    `json:"cash_bal,omitempty"`
	FrozenBal        string    `json:"frozen_bal,omitempty"`
	DisEq            string    `json:"dis_eq,omitempty"`
	BalanceUpdatedAt string    `json:"balance_updated_at,omitempty"`
}

type CachedPayload struct {
	PayloadJSON string
	RefreshedAt time.Time
}

const usdtBalanceSnapshotBucket = time.Minute

func (s *OrderStore) UpsertMarketCandles(candles []MarketCandle, fetchedAt time.Time) error {
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
	stmt, err := tx.Prepare(`INSERT INTO market_candles
		(inst_id, bar, ts, open, high, low, close, volume, confirm, fetched_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(inst_id, bar, ts) DO UPDATE SET
			open=excluded.open,
			high=excluded.high,
			low=excluded.low,
			close=excluded.close,
			volume=excluded.volume,
			confirm=excluded.confirm,
			fetched_at=excluded.fetched_at`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, candle := range candles {
		if _, err := stmt.Exec(
			candle.InstID,
			candle.Bar,
			candle.TS,
			candle.Open,
			candle.High,
			candle.Low,
			candle.Close,
			candle.Volume,
			candle.Confirm,
			fetchedAt.UTC().Format(time.RFC3339Nano),
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *OrderStore) ListMarketCandles(instID, bar string, since time.Time, limit int) ([]MarketCandle, error) {
	if s.db == nil {
		return nil, errors.New("sqlite database is not configured")
	}
	if limit <= 0 {
		limit = 72
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT inst_id, bar, ts, open, high, low, close, volume, confirm
		FROM market_candles
		WHERE inst_id = ? AND bar = ? AND ts >= ?
		ORDER BY ts ASC LIMIT ?`,
		instID,
		bar,
		since.UTC().UnixMilli(),
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MarketCandle
	for rows.Next() {
		var candle MarketCandle
		if err := rows.Scan(&candle.InstID, &candle.Bar, &candle.TS, &candle.Open, &candle.High, &candle.Low, &candle.Close, &candle.Volume, &candle.Confirm); err != nil {
			return nil, err
		}
		out = append(out, candle)
	}
	return out, rows.Err()
}

func (s *OrderStore) UpsertOKXFills(fills []OKXFill, fetchedAt time.Time) error {
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
	stmt, err := tx.Prepare(`INSERT INTO okx_fills
		(api_id, inst_type, inst_id, trade_id, ord_id, cl_ord_id, side, fill_px, fill_sz, fill_pnl, fee, fee_ccy, fill_time, raw_json, fetched_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(api_id, inst_id, trade_id) DO UPDATE SET
			inst_type=excluded.inst_type,
			ord_id=excluded.ord_id,
			cl_ord_id=excluded.cl_ord_id,
			side=excluded.side,
			fill_px=excluded.fill_px,
			fill_sz=excluded.fill_sz,
			fill_pnl=excluded.fill_pnl,
			fee=excluded.fee,
			fee_ccy=excluded.fee_ccy,
			fill_time=excluded.fill_time,
			raw_json=excluded.raw_json,
			fetched_at=excluded.fetched_at`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for i, fill := range fills {
		if fill.TradeID == "" {
			fill.TradeID = fallbackTradeID(fill, i)
		}
		if _, err := stmt.Exec(
			fill.APIID,
			fill.InstType,
			fill.InstID,
			fill.TradeID,
			fill.OrdID,
			fill.ClOrdID,
			fill.Side,
			fill.FillPx,
			fill.FillSz,
			fill.FillPnl,
			fill.Fee,
			fill.FeeCcy,
			fill.FillTime,
			fill.RawJSON,
			fetchedAt.UTC().Format(time.RFC3339Nano),
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *OrderStore) ListOKXFills(apiID string, since time.Time) ([]OKXFill, error) {
	if s.db == nil {
		return nil, errors.New("sqlite database is not configured")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT api_id, inst_type, inst_id, trade_id, ord_id, cl_ord_id, side, fill_px, fill_sz, fill_pnl, fee, fee_ccy, fill_time, raw_json
		FROM okx_fills
		WHERE api_id = ? AND fill_time >= ? AND inst_type = 'SWAP' AND inst_id LIKE '%-USDT-SWAP'
		ORDER BY fill_time DESC`,
		apiID,
		since.UTC().UnixMilli(),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OKXFill
	for rows.Next() {
		var fill OKXFill
		if err := rows.Scan(&fill.APIID, &fill.InstType, &fill.InstID, &fill.TradeID, &fill.OrdID, &fill.ClOrdID, &fill.Side, &fill.FillPx, &fill.FillSz, &fill.FillPnl, &fill.Fee, &fill.FeeCcy, &fill.FillTime, &fill.RawJSON); err != nil {
			return nil, err
		}
		out = append(out, fill)
	}
	return out, rows.Err()
}

func (s *OrderStore) UpsertUSDTBalanceSnapshot(snapshot USDTBalanceSnapshot) error {
	if s.db == nil {
		return errors.New("sqlite database is not configured")
	}
	snapshot.Exchange = strings.ToLower(strings.TrimSpace(snapshot.Exchange))
	if snapshot.Exchange == "" {
		snapshot.Exchange = "okx"
	}
	snapshot.APIID = strings.TrimSpace(snapshot.APIID)
	snapshot.Env = strings.ToLower(strings.TrimSpace(snapshot.Env))
	if snapshot.APIID == "" {
		snapshot.APIID = "default"
	}
	if snapshot.Env == "" {
		snapshot.Env = "demo"
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
	_, err := s.db.Exec(`INSERT INTO usdt_balance_snapshots
		(exchange, api_id, env, bucket_ts, observed_at, total_eq, eq, eq_usd, avail_eq, avail_bal, cash_bal, frozen_bal, dis_eq, balance_updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(exchange, api_id, env, bucket_ts) DO UPDATE SET
			observed_at=excluded.observed_at,
			total_eq=excluded.total_eq,
			eq=excluded.eq,
			eq_usd=excluded.eq_usd,
			avail_eq=excluded.avail_eq,
			avail_bal=excluded.avail_bal,
			cash_bal=excluded.cash_bal,
			frozen_bal=excluded.frozen_bal,
			dis_eq=excluded.dis_eq,
			balance_updated_at=excluded.balance_updated_at`,
		snapshot.Exchange,
		snapshot.APIID,
		snapshot.Env,
		snapshot.BucketTS,
		snapshot.ObservedAt.Format(time.RFC3339Nano),
		snapshot.TotalEq,
		snapshot.Eq,
		snapshot.EqUsd,
		snapshot.AvailEq,
		snapshot.AvailBal,
		snapshot.CashBal,
		snapshot.FrozenBal,
		snapshot.DisEq,
		snapshot.BalanceUpdatedAt,
	)
	return err
}

func (s *OrderStore) ListUSDTBalanceSnapshots(apiID, env string, since time.Time, limit int) ([]USDTBalanceSnapshot, error) {
	return s.ListExchangeUSDTBalanceSnapshots("okx", apiID, env, since, limit)
}

func (s *OrderStore) ListExchangeUSDTBalanceSnapshots(exchange, apiID, env string, since time.Time, limit int) ([]USDTBalanceSnapshot, error) {
	if s.db == nil {
		return nil, errors.New("sqlite database is not configured")
	}
	exchange = strings.ToLower(strings.TrimSpace(exchange))
	if exchange == "" {
		exchange = "okx"
	}
	apiID = strings.TrimSpace(apiID)
	env = strings.ToLower(strings.TrimSpace(env))
	if apiID == "" {
		apiID = "default"
	}
	if env == "" {
		env = "demo"
	}
	if limit <= 0 {
		limit = 72
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT exchange, api_id, env, bucket_ts, observed_at, total_eq, eq, eq_usd, avail_eq, avail_bal, cash_bal, frozen_bal, dis_eq, balance_updated_at
		FROM usdt_balance_snapshots
		WHERE exchange = ? AND api_id = ? AND env = ? AND bucket_ts >= ?
		ORDER BY bucket_ts ASC LIMIT ?`,
		exchange,
		apiID,
		env,
		since.UTC().UnixMilli(),
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []USDTBalanceSnapshot
	for rows.Next() {
		var snapshot USDTBalanceSnapshot
		var observedAt string
		if err := rows.Scan(
			&snapshot.Exchange,
			&snapshot.APIID,
			&snapshot.Env,
			&snapshot.BucketTS,
			&observedAt,
			&snapshot.TotalEq,
			&snapshot.Eq,
			&snapshot.EqUsd,
			&snapshot.AvailEq,
			&snapshot.AvailBal,
			&snapshot.CashBal,
			&snapshot.FrozenBal,
			&snapshot.DisEq,
			&snapshot.BalanceUpdatedAt,
		); err != nil {
			return nil, err
		}
		if observedAt != "" {
			parsed, err := time.Parse(time.RFC3339Nano, observedAt)
			if err != nil {
				return nil, err
			}
			snapshot.ObservedAt = parsed
		}
		out = append(out, snapshot)
	}
	return out, rows.Err()
}

func (s *OrderStore) CachePayload(key string, payload any, refreshedAt time.Time) error {
	if s.db == nil {
		return errors.New("sqlite database is not configured")
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.db.Exec(`INSERT INTO analysis_cache(cache_key, payload_json, refreshed_at)
		VALUES (?, ?, ?)
		ON CONFLICT(cache_key) DO UPDATE SET payload_json=excluded.payload_json, refreshed_at=excluded.refreshed_at`,
		key,
		string(b),
		refreshedAt.UTC().Format(time.RFC3339Nano),
	)
	return err
}

func (s *OrderStore) CachedPayload(key string) (CachedPayload, bool, error) {
	if s.db == nil {
		return CachedPayload{}, false, errors.New("sqlite database is not configured")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var payload, refreshedAt string
	err := s.db.QueryRow(`SELECT payload_json, refreshed_at FROM analysis_cache WHERE cache_key = ?`, key).Scan(&payload, &refreshedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return CachedPayload{}, false, nil
	}
	if err != nil {
		return CachedPayload{}, false, err
	}
	parsed, err := time.Parse(time.RFC3339Nano, refreshedAt)
	if err != nil {
		return CachedPayload{}, false, err
	}
	return CachedPayload{PayloadJSON: payload, RefreshedAt: parsed}, true, nil
}

func fallbackTradeID(fill OKXFill, index int) string {
	return fill.OrdID + "|" + fill.ClOrdID + "|" + fill.Side + "|" + fill.FillPx + "|" + fill.FillSz + "|" + strconv.FormatInt(fill.FillTime, 10) + "|" + strconv.Itoa(index)
}
