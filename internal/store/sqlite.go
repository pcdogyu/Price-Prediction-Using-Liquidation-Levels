package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
	_ "modernc.org/sqlite"
)

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err = s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error                   { return s.db.Close() }
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

func (s *Store) migrate(ctx context.Context) error {
	const schema = `
CREATE TABLE IF NOT EXISTS candles(exchange TEXT NOT NULL,symbol TEXT NOT NULL,ts INTEGER NOT NULL,open REAL,high REAL,low REAL,close REAL,volume_usd REAL,taker_buy_usd REAL,open_interest_usd REAL,funding_rate REAL,long_short_ratio REAL,PRIMARY KEY(exchange,symbol,ts));
CREATE INDEX IF NOT EXISTS candles_symbol_ts ON candles(symbol,ts);
CREATE TABLE IF NOT EXISTS liquidations(id TEXT PRIMARY KEY,exchange TEXT NOT NULL,symbol TEXT NOT NULL,position_side TEXT NOT NULL,event_ts INTEGER NOT NULL,received_ts INTEGER NOT NULL,price REAL NOT NULL,quantity REAL NOT NULL,notional_usd REAL NOT NULL,coverage TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS liq_symbol_ts ON liquidations(symbol,event_ts);
CREATE TABLE IF NOT EXISTS predictions(symbol TEXT NOT NULL,ts INTEGER NOT NULL,payload BLOB NOT NULL,PRIMARY KEY(symbol,ts));
CREATE TABLE IF NOT EXISTS backtests(symbol TEXT PRIMARY KEY,payload BLOB NOT NULL,updated_ts INTEGER NOT NULL);`
	_, err := s.db.ExecContext(ctx, schema)
	return err
}

func (s *Store) UpsertCandles(ctx context.Context, cs []domain.Candle) error {
	if len(cs) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := `INSERT INTO candles(exchange,symbol,ts,open,high,low,close,volume_usd,taker_buy_usd,open_interest_usd,funding_rate,long_short_ratio) VALUES(?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(exchange,symbol,ts) DO UPDATE SET open=excluded.open,high=excluded.high,low=excluded.low,close=excluded.close,volume_usd=excluded.volume_usd,taker_buy_usd=excluded.taker_buy_usd,open_interest_usd=CASE WHEN excluded.open_interest_usd>0 THEN excluded.open_interest_usd ELSE candles.open_interest_usd END,funding_rate=CASE WHEN excluded.funding_rate!=0 THEN excluded.funding_rate ELSE candles.funding_rate END,long_short_ratio=CASE WHEN excluded.long_short_ratio>0 THEN excluded.long_short_ratio ELSE candles.long_short_ratio END`
	st, err := tx.PrepareContext(ctx, q)
	if err != nil {
		return err
	}
	defer st.Close()
	for _, c := range cs {
		if _, err = st.ExecContext(ctx, c.Exchange, c.Symbol, c.Time.UTC().UnixMilli(), c.Open, c.High, c.Low, c.Close, c.VolumeUSD, c.TakerBuyUSD, c.OpenInterestUSD, c.FundingRate, c.LongShortRatio); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) Candles(ctx context.Context, symbol string, since time.Time) ([]domain.Candle, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT exchange,symbol,ts,open,high,low,close,volume_usd,taker_buy_usd,open_interest_usd,funding_rate,long_short_ratio FROM candles WHERE symbol=? AND ts>=? ORDER BY ts,exchange`, symbol, since.UTC().UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Candle
	for rows.Next() {
		var c domain.Candle
		var ms int64
		if err = rows.Scan(&c.Exchange, &c.Symbol, &ms, &c.Open, &c.High, &c.Low, &c.Close, &c.VolumeUSD, &c.TakerBuyUSD, &c.OpenInterestUSD, &c.FundingRate, &c.LongShortRatio); err != nil {
			return nil, err
		}
		c.Time = time.UnixMilli(ms).UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) InsertLiquidation(ctx context.Context, e domain.LiquidationEvent) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO liquidations(id,exchange,symbol,position_side,event_ts,received_ts,price,quantity,notional_usd,coverage) VALUES(?,?,?,?,?,?,?,?,?,?)`, e.ID, e.Exchange, e.Symbol, e.PositionSide, e.EventTime.UnixMilli(), e.ReceivedAt.UnixMilli(), e.Price, e.Quantity, e.NotionalUSD, e.Coverage)
	return err
}

func (s *Store) LiquidationTotals(ctx context.Context, symbol string, since time.Time) (longUSD, shortUSD float64, err error) {
	rows, e := s.db.QueryContext(ctx, `SELECT position_side,COALESCE(SUM(notional_usd),0) FROM liquidations WHERE symbol=? AND event_ts>=? GROUP BY position_side`, symbol, since.UnixMilli())
	if e != nil {
		return 0, 0, e
	}
	defer rows.Close()
	for rows.Next() {
		var side string
		var n float64
		if e = rows.Scan(&side, &n); e != nil {
			return 0, 0, e
		}
		if side == "long" {
			longUSD = n
		} else if side == "short" {
			shortUSD = n
		}
	}
	return longUSD, shortUSD, rows.Err()
}

func (s *Store) SavePrediction(ctx context.Context, p domain.Prediction) error {
	b, e := json.Marshal(p)
	if e != nil {
		return e
	}
	_, e = s.db.ExecContext(ctx, `INSERT INTO predictions(symbol,ts,payload) VALUES(?,?,?) ON CONFLICT(symbol,ts) DO UPDATE SET payload=excluded.payload`, p.Symbol, p.Time.UnixMilli(), b)
	return e
}
func (s *Store) LatestPrediction(ctx context.Context, symbol string) (domain.Prediction, error) {
	var b []byte
	e := s.db.QueryRowContext(ctx, `SELECT payload FROM predictions WHERE symbol=? ORDER BY ts DESC LIMIT 1`, symbol).Scan(&b)
	if e != nil {
		return domain.Prediction{}, e
	}
	var p domain.Prediction
	e = json.Unmarshal(b, &p)
	return p, e
}
func (s *Store) SaveBacktest(ctx context.Context, r domain.BacktestReport) error {
	b, e := json.Marshal(r)
	if e != nil {
		return e
	}
	_, e = s.db.ExecContext(ctx, `INSERT INTO backtests(symbol,payload,updated_ts)VALUES(?,?,?) ON CONFLICT(symbol)DO UPDATE SET payload=excluded.payload,updated_ts=excluded.updated_ts`, r.Symbol, b, time.Now().UTC().UnixMilli())
	return e
}
func (s *Store) Backtest(ctx context.Context, symbol string) (domain.BacktestReport, error) {
	var b []byte
	e := s.db.QueryRowContext(ctx, `SELECT payload FROM backtests WHERE symbol=?`, symbol).Scan(&b)
	if e != nil {
		return domain.BacktestReport{}, fmt.Errorf("backtest unavailable: %w", e)
	}
	var r domain.BacktestReport
	e = json.Unmarshal(b, &r)
	return r, e
}
