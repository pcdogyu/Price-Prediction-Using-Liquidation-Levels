package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/volumeprofile"
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
CREATE TABLE IF NOT EXISTS predictions(symbol TEXT NOT NULL,ts INTEGER NOT NULL,source TEXT NOT NULL DEFAULT '',payload BLOB NOT NULL,PRIMARY KEY(symbol,ts));
CREATE TABLE IF NOT EXISTS backtests(symbol TEXT PRIMARY KEY,payload BLOB NOT NULL,updated_ts INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS volume_profile_levels(symbol TEXT NOT NULL,session_ts INTEGER NOT NULL,price_key TEXT NOT NULL,price REAL NOT NULL,volume_usd REAL NOT NULL,trade_count INTEGER NOT NULL,last_trade_ts INTEGER NOT NULL,PRIMARY KEY(symbol,session_ts,price_key));
CREATE TABLE IF NOT EXISTS volume_profile_cursors(symbol TEXT NOT NULL,session_ts INTEGER NOT NULL,last_agg_id INTEGER NOT NULL,last_trade_ts INTEGER NOT NULL,complete INTEGER NOT NULL,updated_ts INTEGER NOT NULL,PRIMARY KEY(symbol,session_ts));
CREATE TABLE IF NOT EXISTS volume_profile_snapshots(symbol TEXT NOT NULL,ts INTEGER NOT NULL,session_ts INTEGER NOT NULL,val REAL NOT NULL,vah REAL NOT NULL,total_volume_usd REAL NOT NULL,complete INTEGER NOT NULL,PRIMARY KEY(symbol,ts));
CREATE INDEX IF NOT EXISTS volume_profile_snapshots_symbol_ts ON volume_profile_snapshots(symbol,ts);
CREATE TABLE IF NOT EXISTS volume_archive_imports(symbol TEXT NOT NULL,day TEXT NOT NULL,status TEXT NOT NULL,error TEXT NOT NULL DEFAULT '',updated_ts INTEGER NOT NULL,PRIMARY KEY(symbol,day));`
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return err
	}
	// Databases created before source isolation do not have this column. Keep
	// their rows with an empty source so they remain preserved but are ignored.
	if !s.hasColumn(ctx, "predictions", "source") {
		if _, err := s.db.ExecContext(ctx, `ALTER TABLE predictions ADD COLUMN source TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	_, err := s.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS predictions_source_symbol_ts ON predictions(source,symbol,ts)`)
	return err
}

func (s *Store) hasColumn(ctx context.Context, table, column string) bool {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, kind string
		var notNull, primary int
		var defaultValue any
		if rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primary) == nil && name == column {
			return true
		}
	}
	return false
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
	return s.CandlesByExchange(ctx, "binance", symbol, since)
}

func (s *Store) CandlesByExchange(ctx context.Context, exchange, symbol string, since time.Time) ([]domain.Candle, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT exchange,symbol,ts,open,high,low,close,volume_usd,taker_buy_usd,open_interest_usd,funding_rate,long_short_ratio FROM candles WHERE exchange=? AND symbol=? AND ts>=? ORDER BY ts`, exchange, symbol, since.UTC().UnixMilli())
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

func (s *Store) CandlesRange(ctx context.Context, symbol string, from, before time.Time) ([]domain.Candle, error) {
	return s.CandlesRangeByExchange(ctx, "binance", symbol, from, before)
}

func (s *Store) CandlesRangeByExchange(ctx context.Context, exchange, symbol string, from, before time.Time) ([]domain.Candle, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT exchange,symbol,ts,open,high,low,close,volume_usd,taker_buy_usd,open_interest_usd,funding_rate,long_short_ratio FROM candles WHERE exchange=? AND symbol=? AND ts>=? AND ts<? ORDER BY ts`, exchange, symbol, from.UTC().UnixMilli(), before.UTC().UnixMilli())
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

func (s *Store) CandleBounds(ctx context.Context, exchange, symbol string) (time.Time, time.Time, bool, error) {
	var first, last sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT MIN(ts),MAX(ts) FROM candles WHERE exchange=? AND symbol=?`, exchange, symbol).Scan(&first, &last)
	if err != nil {
		return time.Time{}, time.Time{}, false, err
	}
	if !first.Valid || !last.Valid {
		return time.Time{}, time.Time{}, false, nil
	}
	return time.UnixMilli(first.Int64).UTC(), time.UnixMilli(last.Int64).UTC(), true, nil
}

func (s *Store) SymbolBounds(ctx context.Context, symbol string) (time.Time, time.Time, bool, error) {
	return s.ExchangeSymbolBounds(ctx, "binance", symbol)
}

func (s *Store) ExchangeSymbolBounds(ctx context.Context, exchange, symbol string) (time.Time, time.Time, bool, error) {
	var first, last sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT MIN(ts),MAX(ts) FROM candles WHERE exchange=? AND symbol=?`, exchange, symbol).Scan(&first, &last)
	if err != nil {
		return time.Time{}, time.Time{}, false, err
	}
	if !first.Valid || !last.Valid {
		return time.Time{}, time.Time{}, false, nil
	}
	return time.UnixMilli(first.Int64).UTC(), time.UnixMilli(last.Int64).UTC(), true, nil
}

func (s *Store) InsertLiquidation(ctx context.Context, e domain.LiquidationEvent) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO liquidations(id,exchange,symbol,position_side,event_ts,received_ts,price,quantity,notional_usd,coverage) VALUES(?,?,?,?,?,?,?,?,?,?)`, e.ID, e.Exchange, e.Symbol, e.PositionSide, e.EventTime.UnixMilli(), e.ReceivedAt.UnixMilli(), e.Price, e.Quantity, e.NotionalUSD, e.Coverage)
	return err
}

func (s *Store) LiquidationTotals(ctx context.Context, symbol string, since time.Time) (longUSD, shortUSD float64, err error) {
	rows, e := s.db.QueryContext(ctx, `SELECT position_side,COALESCE(SUM(notional_usd),0) FROM liquidations WHERE exchange='binance' AND symbol=? AND event_ts>=? GROUP BY position_side`, symbol, since.UnixMilli())
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
	if p.DataSource == "" {
		p.DataSource = domain.DataSourceBinanceUSDM
	}
	b, e := json.Marshal(p)
	if e != nil {
		return e
	}
	_, e = s.db.ExecContext(ctx, `INSERT INTO predictions(symbol,ts,source,payload) VALUES(?,?,?,?) ON CONFLICT(symbol,ts) DO UPDATE SET source=excluded.source,payload=excluded.payload`, p.Symbol, p.Time.UnixMilli(), p.DataSource, b)
	return e
}
func (s *Store) LatestPrediction(ctx context.Context, symbol string) (domain.Prediction, error) {
	var b []byte
	e := s.db.QueryRowContext(ctx, `SELECT payload FROM predictions WHERE source=? AND symbol=? ORDER BY ts DESC LIMIT 1`, domain.DataSourceBinanceUSDM, symbol).Scan(&b)
	if e != nil {
		return domain.Prediction{}, e
	}
	var p domain.Prediction
	e = json.Unmarshal(b, &p)
	return p, e
}
func (s *Store) Predictions(ctx context.Context, symbol string, from, before time.Time) ([]domain.Prediction, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT payload FROM predictions WHERE source=? AND symbol=? AND ts>=? AND ts<? ORDER BY ts`, domain.DataSourceBinanceUSDM, symbol, from.UTC().UnixMilli(), before.UTC().UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Prediction
	for rows.Next() {
		var b []byte
		if err = rows.Scan(&b); err != nil {
			return nil, err
		}
		var p domain.Prediction
		if err = json.Unmarshal(b, &p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ApplyAggregateTrades persists exact per-price totals and the aggregate-trade
// cursor in one transaction. Replayed IDs at or below the cursor are ignored.
func (s *Store) ApplyAggregateTrades(ctx context.Context, symbol string, sessionStart time.Time, trades []domain.AggregateTrade, complete bool) error {
	if len(trades) == 0 && !complete {
		return nil
	}
	sort.Slice(trades, func(i, j int) bool { return trades[i].ID < trades[j].ID })
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	key := sessionStart.UTC().UnixMilli()
	var lastID, lastTradeMS int64
	var oldComplete int
	err = tx.QueryRowContext(ctx, `SELECT last_agg_id,last_trade_ts,complete FROM volume_profile_cursors WHERE symbol=? AND session_ts=?`, symbol, key).Scan(&lastID, &lastTradeMS, &oldComplete)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	levelStatement, err := tx.PrepareContext(ctx, `INSERT INTO volume_profile_levels(symbol,session_ts,price_key,price,volume_usd,trade_count,last_trade_ts) VALUES(?,?,?,?,?,?,?) ON CONFLICT(symbol,session_ts,price_key) DO UPDATE SET volume_usd=volume_profile_levels.volume_usd+excluded.volume_usd,trade_count=volume_profile_levels.trade_count+excluded.trade_count,last_trade_ts=MAX(volume_profile_levels.last_trade_ts,excluded.last_trade_ts)`)
	if err != nil {
		return err
	}
	defer levelStatement.Close()
	for _, trade := range trades {
		if trade.ID <= lastID || trade.Price <= 0 || trade.Quantity <= 0 || trade.Time.Before(sessionStart) {
			continue
		}
		priceKey := trade.PriceText
		if priceKey == "" {
			priceKey = strconv.FormatFloat(trade.Price, 'f', -1, 64)
		}
		tradeMS := trade.Time.UTC().UnixMilli()
		if _, err = levelStatement.ExecContext(ctx, symbol, key, priceKey, trade.Price, trade.Price*trade.Quantity, 1, tradeMS); err != nil {
			return err
		}
		lastID = trade.ID
		if tradeMS > lastTradeMS {
			lastTradeMS = tradeMS
		}
	}
	completed := oldComplete != 0 || complete
	_, err = tx.ExecContext(ctx, `INSERT INTO volume_profile_cursors(symbol,session_ts,last_agg_id,last_trade_ts,complete,updated_ts) VALUES(?,?,?,?,?,?) ON CONFLICT(symbol,session_ts) DO UPDATE SET last_agg_id=MAX(volume_profile_cursors.last_agg_id,excluded.last_agg_id),last_trade_ts=MAX(volume_profile_cursors.last_trade_ts,excluded.last_trade_ts),complete=excluded.complete,updated_ts=excluded.updated_ts`, symbol, key, lastID, lastTradeMS, boolInt(completed), time.Now().UTC().UnixMilli())
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) VolumeProfileLevels(ctx context.Context, symbol string, sessionStart time.Time) ([]volumeprofile.Level, int64, time.Time, bool, error) {
	key := sessionStart.UTC().UnixMilli()
	rows, err := s.db.QueryContext(ctx, `SELECT price,volume_usd FROM volume_profile_levels WHERE symbol=? AND session_ts=? ORDER BY price`, symbol, key)
	if err != nil {
		return nil, 0, time.Time{}, false, err
	}
	defer rows.Close()
	var levels []volumeprofile.Level
	for rows.Next() {
		var level volumeprofile.Level
		if err = rows.Scan(&level.Price, &level.VolumeUSD); err != nil {
			return nil, 0, time.Time{}, false, err
		}
		levels = append(levels, level)
	}
	if err = rows.Err(); err != nil {
		return nil, 0, time.Time{}, false, err
	}
	var lastID, lastMS int64
	var complete int
	err = s.db.QueryRowContext(ctx, `SELECT last_agg_id,last_trade_ts,complete FROM volume_profile_cursors WHERE symbol=? AND session_ts=?`, symbol, key).Scan(&lastID, &lastMS, &complete)
	if errors.Is(err, sql.ErrNoRows) {
		return levels, 0, time.Time{}, false, nil
	}
	if err != nil {
		return nil, 0, time.Time{}, false, err
	}
	return levels, lastID, time.UnixMilli(lastMS).UTC(), complete != 0, nil
}

func (s *Store) SetVolumeProfileComplete(ctx context.Context, symbol string, sessionStart time.Time, complete bool) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO volume_profile_cursors(symbol,session_ts,last_agg_id,last_trade_ts,complete,updated_ts) VALUES(?,?,?,?,?,?) ON CONFLICT(symbol,session_ts) DO UPDATE SET complete=excluded.complete,updated_ts=excluded.updated_ts`, symbol, sessionStart.UTC().UnixMilli(), 0, 0, boolInt(complete), time.Now().UTC().UnixMilli())
	return err
}

func (s *Store) SaveVolumeProfileSnapshot(ctx context.Context, snapshot domain.VolumeProfileSnapshot) error {
	return s.SaveVolumeProfileSnapshots(ctx, []domain.VolumeProfileSnapshot{snapshot})
}

func (s *Store) SaveVolumeProfileSnapshots(ctx context.Context, snapshots []domain.VolumeProfileSnapshot) error {
	if len(snapshots) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	statement, err := tx.PrepareContext(ctx, `INSERT INTO volume_profile_snapshots(symbol,ts,session_ts,val,vah,total_volume_usd,complete) VALUES(?,?,?,?,?,?,?) ON CONFLICT(symbol,ts) DO UPDATE SET session_ts=excluded.session_ts,val=excluded.val,vah=excluded.vah,total_volume_usd=excluded.total_volume_usd,complete=excluded.complete`)
	if err != nil {
		return err
	}
	defer statement.Close()
	for _, snapshot := range snapshots {
		if _, err = statement.ExecContext(ctx, snapshot.Symbol, snapshot.Time.UTC().UnixMilli(), snapshot.SessionStart.UTC().UnixMilli(), snapshot.VAL, snapshot.VAH, snapshot.TotalVolumeUSD, boolInt(snapshot.Complete)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) VolumeProfileSnapshots(ctx context.Context, symbol string, from, before time.Time) ([]domain.VolumeProfileSnapshot, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT ts,session_ts,val,vah,total_volume_usd,complete FROM volume_profile_snapshots WHERE symbol=? AND ts>=? AND ts<? ORDER BY ts`, symbol, from.UTC().UnixMilli(), before.UTC().UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.VolumeProfileSnapshot
	for rows.Next() {
		var snapshot domain.VolumeProfileSnapshot
		var timestamp, session int64
		var complete int
		snapshot.Symbol = symbol
		if err = rows.Scan(&timestamp, &session, &snapshot.VAL, &snapshot.VAH, &snapshot.TotalVolumeUSD, &complete); err != nil {
			return nil, err
		}
		snapshot.Time = time.UnixMilli(timestamp).UTC()
		snapshot.SessionStart = time.UnixMilli(session).UTC()
		snapshot.Complete = complete != 0
		out = append(out, snapshot)
	}
	return out, rows.Err()
}

func (s *Store) MarkArchiveImport(ctx context.Context, symbol string, day time.Time, status, message string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO volume_archive_imports(symbol,day,status,error,updated_ts) VALUES(?,?,?,?,?) ON CONFLICT(symbol,day) DO UPDATE SET status=excluded.status,error=excluded.error,updated_ts=excluded.updated_ts`, symbol, day.UTC().Format("2006-01-02"), status, message, time.Now().UTC().UnixMilli())
	return err
}

func (s *Store) ArchiveImported(ctx context.Context, symbol string, day time.Time) (bool, error) {
	var status string
	err := s.db.QueryRowContext(ctx, `SELECT status FROM volume_archive_imports WHERE symbol=? AND day=?`, symbol, day.UTC().Format("2006-01-02")).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return status == "complete", err
}

func (s *Store) CompleteArchiveDays(ctx context.Context, symbol string, from, before time.Time) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM volume_archive_imports WHERE symbol=? AND status='complete' AND day>=? AND day<?`, symbol, from.UTC().Format("2006-01-02"), before.UTC().Format("2006-01-02")).Scan(&count)
	return count, err
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func (s *Store) PruneBefore(ctx context.Context, before time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	cutoff := before.UTC().UnixMilli()
	for _, q := range []string{
		`DELETE FROM candles WHERE ts<?`,
		`DELETE FROM predictions WHERE ts<?`,
		`DELETE FROM liquidations WHERE event_ts<?`,
		`DELETE FROM volume_profile_snapshots WHERE ts<?`,
	} {
		if _, err = tx.ExecContext(ctx, q, cutoff); err != nil {
			return err
		}
	}
	profileCutoff := time.Now().UTC().Add(-72 * time.Hour).UnixMilli()
	if _, err = tx.ExecContext(ctx, `DELETE FROM volume_profile_levels WHERE session_ts<?`, profileCutoff); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM volume_profile_cursors WHERE session_ts<?`, profileCutoff); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) SaveBacktest(ctx context.Context, r domain.BacktestReport) error {
	if r.DataSource == "" {
		r.DataSource = domain.DataSourceBinanceUSDM
	}
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
	if e == nil && r.DataSource != domain.DataSourceBinanceUSDM {
		return domain.BacktestReport{}, errors.New("binance-only backtest unavailable")
	}
	return r, e
}
