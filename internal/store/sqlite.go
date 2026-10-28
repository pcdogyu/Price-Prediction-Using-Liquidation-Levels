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
	"strings"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/authn"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/volumeprofile"
	_ "modernc.org/sqlite"
)

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(15000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
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

// SQLDB returns the shared database handle used by the application store.
// Components that persist data in the same SQLite file must reuse this handle
// so database/sql serializes their writes through the same single connection.
func (s *Store) SQLDB() *sql.DB {
	if s == nil {
		return nil
	}
	return s.db
}

func (s *Store) migrate(ctx context.Context) error {
	const schema = `
CREATE TABLE IF NOT EXISTS candles(exchange TEXT NOT NULL,symbol TEXT NOT NULL,ts INTEGER NOT NULL,open REAL,high REAL,low REAL,close REAL,volume_usd REAL,taker_buy_usd REAL,open_interest_usd REAL,funding_rate REAL,long_short_ratio REAL,PRIMARY KEY(exchange,symbol,ts));
CREATE INDEX IF NOT EXISTS candles_symbol_ts ON candles(symbol,ts);
CREATE INDEX IF NOT EXISTS candles_retention_time ON candles(ts);
CREATE TABLE IF NOT EXISTS liquidations(id TEXT PRIMARY KEY,exchange TEXT NOT NULL,symbol TEXT NOT NULL,position_side TEXT NOT NULL,event_ts INTEGER NOT NULL,received_ts INTEGER NOT NULL,price REAL NOT NULL,quantity REAL NOT NULL,notional_usd REAL NOT NULL,coverage TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS liq_symbol_ts ON liquidations(symbol,event_ts);
CREATE INDEX IF NOT EXISTS liquidations_retention_time ON liquidations(event_ts);
CREATE TABLE IF NOT EXISTS predictions(symbol TEXT NOT NULL,ts INTEGER NOT NULL,source TEXT NOT NULL DEFAULT '',payload BLOB NOT NULL,PRIMARY KEY(symbol,ts));
CREATE INDEX IF NOT EXISTS predictions_retention_time ON predictions(ts);
CREATE TABLE IF NOT EXISTS backtests(symbol TEXT PRIMARY KEY,payload BLOB NOT NULL,updated_ts INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS volume_profile_levels(symbol TEXT NOT NULL,session_ts INTEGER NOT NULL,price_key TEXT NOT NULL,price REAL NOT NULL,volume_usd REAL NOT NULL,trade_count INTEGER NOT NULL,last_trade_ts INTEGER NOT NULL,PRIMARY KEY(symbol,session_ts,price_key));
CREATE INDEX IF NOT EXISTS volume_profile_levels_retention_time ON volume_profile_levels(session_ts);
CREATE TABLE IF NOT EXISTS volume_profile_cursors(symbol TEXT NOT NULL,session_ts INTEGER NOT NULL,last_agg_id INTEGER NOT NULL,last_trade_ts INTEGER NOT NULL,complete INTEGER NOT NULL,updated_ts INTEGER NOT NULL,PRIMARY KEY(symbol,session_ts));
CREATE INDEX IF NOT EXISTS volume_profile_cursors_retention_time ON volume_profile_cursors(session_ts);
CREATE TABLE IF NOT EXISTS volume_profile_snapshots(symbol TEXT NOT NULL,ts INTEGER NOT NULL,session_ts INTEGER NOT NULL,val REAL NOT NULL,vah REAL NOT NULL,total_volume_usd REAL NOT NULL,complete INTEGER NOT NULL,PRIMARY KEY(symbol,ts));
CREATE INDEX IF NOT EXISTS volume_profile_snapshots_symbol_ts ON volume_profile_snapshots(symbol,ts);
CREATE INDEX IF NOT EXISTS volume_profile_snapshots_retention_time ON volume_profile_snapshots(ts);
CREATE TABLE IF NOT EXISTS volume_archive_imports(symbol TEXT NOT NULL,day TEXT NOT NULL,status TEXT NOT NULL,error TEXT NOT NULL DEFAULT '',updated_ts INTEGER NOT NULL,PRIMARY KEY(symbol,day));
CREATE INDEX IF NOT EXISTS volume_archive_imports_retention_day ON volume_archive_imports(day);
CREATE TABLE IF NOT EXISTS auth_sessions(token_hash TEXT PRIMARY KEY,expires_ts INTEGER NOT NULL,username TEXT NOT NULL DEFAULT '');
CREATE INDEX IF NOT EXISTS auth_sessions_retention_expiry ON auth_sessions(expires_ts);
CREATE TABLE IF NOT EXISTS auth_users(username TEXT PRIMARY KEY,password_hash TEXT NOT NULL,created_ts INTEGER NOT NULL,updated_ts INTEGER NOT NULL,role TEXT NOT NULL DEFAULT 'user',disabled INTEGER NOT NULL DEFAULT 0,last_login_ts INTEGER);
CREATE TABLE IF NOT EXISTS auth_audit(id INTEGER PRIMARY KEY AUTOINCREMENT,actor TEXT NOT NULL,action TEXT NOT NULL,target TEXT NOT NULL,created_ts INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS auth_audit_created ON auth_audit(created_ts DESC);
CREATE TABLE IF NOT EXISTS admin_menu(id INTEGER PRIMARY KEY CHECK(id=1),payload BLOB NOT NULL,updated_ts INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS user_menu(username TEXT PRIMARY KEY,payload BLOB NOT NULL,updated_ts INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS user_theme(username TEXT PRIMARY KEY,theme TEXT NOT NULL,updated_ts INTEGER NOT NULL);`
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return err
	}
	for column, definition := range map[string]string{
		"role":          "TEXT NOT NULL DEFAULT 'user'",
		"disabled":      "INTEGER NOT NULL DEFAULT 0",
		"last_login_ts": "INTEGER",
	} {
		if !s.hasColumn(ctx, "auth_users", column) {
			if _, err := s.db.ExecContext(ctx, `ALTER TABLE auth_users ADD COLUMN `+column+` `+definition); err != nil {
				return err
			}
		}
	}
	if !s.hasColumn(ctx, "auth_sessions", "username") {
		if _, err := s.db.ExecContext(ctx, `ALTER TABLE auth_sessions ADD COLUMN username TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	// Databases created before source isolation do not have this column. Keep
	// their rows with an empty source so they remain preserved but are ignored.
	if !s.hasColumn(ctx, "predictions", "source") {
		if _, err := s.db.ExecContext(ctx, `ALTER TABLE predictions ADD COLUMN source TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	_, err := s.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS predictions_source_symbol_ts ON predictions(source,symbol,ts)`)
	if err != nil {
		return err
	}
	if err := s.migrateDashboard(ctx); err != nil {
		return err
	}
	return s.migrateAlerts(ctx)
}

func (s *Store) LoadAuthSessions(ctx context.Context, now time.Time) (map[string]time.Time, error) {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM auth_sessions WHERE expires_ts<=?`, now.UTC().UnixMilli()); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT token_hash,expires_ts FROM auth_sessions WHERE expires_ts>?`, now.UTC().UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sessions := make(map[string]time.Time)
	for rows.Next() {
		var key string
		var expires int64
		if err = rows.Scan(&key, &expires); err != nil {
			return nil, err
		}
		sessions[key] = time.UnixMilli(expires).UTC()
	}
	return sessions, rows.Err()
}

func (s *Store) SaveAuthSession(ctx context.Context, tokenHash string, expires time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO auth_sessions(token_hash,expires_ts,username) VALUES(?,?,?) ON CONFLICT(token_hash) DO UPDATE SET expires_ts=excluded.expires_ts`, tokenHash, expires.UTC().UnixMilli(), "")
	return err
}

func (s *Store) LoadAuthSessionRecords(ctx context.Context, now time.Time) (map[string]authn.RichSessionRecord, error) {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM auth_sessions WHERE expires_ts<=?`, now.UTC().UnixMilli()); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT token_hash,expires_ts,username FROM auth_sessions WHERE expires_ts>?`, now.UTC().UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]authn.RichSessionRecord)
	for rows.Next() {
		var record authn.RichSessionRecord
		var expires int64
		if err := rows.Scan(&record.TokenHash, &expires, &record.Username); err != nil {
			return nil, err
		}
		record.Expires = time.UnixMilli(expires).UTC()
		result[record.TokenHash] = record
	}
	return result, rows.Err()
}

func (s *Store) SaveAuthSessionRecord(ctx context.Context, record authn.RichSessionRecord) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO auth_sessions(token_hash,expires_ts,username) VALUES(?,?,?) ON CONFLICT(token_hash) DO UPDATE SET expires_ts=excluded.expires_ts,username=excluded.username`, record.TokenHash, record.Expires.UTC().UnixMilli(), strings.TrimSpace(record.Username))
	return err
}

func (s *Store) DeleteAuthSession(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM auth_sessions WHERE token_hash=?`, tokenHash)
	return err
}

func (s *Store) LoadAuthUsers(ctx context.Context) (map[string]string, error) {
	records, err := s.LoadAuthUserRecords(ctx)
	if err != nil {
		return nil, err
	}
	users := make(map[string]string, len(records))
	for username, record := range records {
		if !record.Disabled {
			users[username] = record.PasswordHash
		}
	}
	return users, nil
}

func (s *Store) LoadAuthUserRecords(ctx context.Context) (map[string]authn.UserRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT username,password_hash,role,disabled,created_ts,updated_ts,last_login_ts FROM auth_users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users := make(map[string]authn.UserRecord)
	for rows.Next() {
		var record authn.UserRecord
		var disabled int
		var created, updated int64
		var lastLogin sql.NullInt64
		if err = rows.Scan(&record.Username, &record.PasswordHash, &record.Role, &disabled, &created, &updated, &lastLogin); err != nil {
			return nil, err
		}
		record.Disabled = disabled != 0
		record.CreatedAt = time.UnixMilli(created).UTC()
		record.UpdatedAt = time.UnixMilli(updated).UTC()
		if lastLogin.Valid {
			value := time.UnixMilli(lastLogin.Int64).UTC()
			record.LastLoginAt = &value
		}
		users[record.Username] = record
	}
	return users, rows.Err()
}

func (s *Store) ListAuthUsers(ctx context.Context) ([]authn.UserRecord, error) {
	records, err := s.LoadAuthUserRecords(ctx)
	if err != nil {
		return nil, err
	}
	users := make([]authn.UserRecord, 0, len(records))
	for _, record := range records {
		users = append(users, record)
	}
	sort.Slice(users, func(i, j int) bool { return users[i].Username < users[j].Username })
	return users, nil
}

func (s *Store) UpsertAuthUser(ctx context.Context, username, passwordHash string) error {
	username = strings.TrimSpace(username)
	passwordHash = strings.TrimSpace(passwordHash)
	if username == "" || len(username) > 128 || passwordHash == "" {
		return errors.New("invalid authentication user")
	}
	now := time.Now().UTC().UnixMilli()
	_, err := s.db.ExecContext(ctx, `INSERT INTO auth_users(username,password_hash,created_ts,updated_ts,role,disabled) VALUES(?,?,?,?,'user',0)
		ON CONFLICT(username) DO UPDATE SET password_hash=excluded.password_hash,updated_ts=excluded.updated_ts`, username, passwordHash, now, now)
	return err
}

func (s *Store) CreateAuthUser(ctx context.Context, username, passwordHash, role string) error {
	username = strings.TrimSpace(username)
	passwordHash = strings.TrimSpace(passwordHash)
	role = strings.TrimSpace(role)
	if username == "" || len(username) > 128 || passwordHash == "" || (role != "admin" && role != "user") {
		return errors.New("invalid authentication user")
	}
	now := time.Now().UTC().UnixMilli()
	_, err := s.db.ExecContext(ctx, `INSERT INTO auth_users(username,password_hash,created_ts,updated_ts,role,disabled) VALUES(?,?,?,?,?,0)`, username, passwordHash, now, now, role)
	return err
}

func (s *Store) SetAuthUserState(ctx context.Context, username, role string, disabled bool) error {
	username = strings.TrimSpace(username)
	role = strings.TrimSpace(role)
	if username == "" || (role != "admin" && role != "user") {
		return errors.New("invalid authentication user state")
	}
	_, err := s.db.ExecContext(ctx, `UPDATE auth_users SET role=?,disabled=?,updated_ts=? WHERE username=?`, role, boolInt(disabled), time.Now().UTC().UnixMilli(), username)
	return err
}

func (s *Store) DeleteAuthUser(ctx context.Context, username string) error {
	username = strings.TrimSpace(username)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `DELETE FROM auth_users WHERE username=?`, username); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM user_theme WHERE username=?`, username); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return nil
}

func (s *Store) RecordAuthAudit(ctx context.Context, actor, action, target string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO auth_audit(actor,action,target,created_ts) VALUES(?,?,?,?)`, strings.TrimSpace(actor), strings.TrimSpace(action), strings.TrimSpace(target), time.Now().UTC().UnixMilli())
	return err
}

func (s *Store) LoadAdminMenu(ctx context.Context) ([]byte, bool, error) {
	var payload []byte
	err := s.db.QueryRowContext(ctx, `SELECT payload FROM admin_menu WHERE id=1`).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return append([]byte(nil), payload...), true, nil
}

func (s *Store) SaveAdminMenu(ctx context.Context, payload []byte) error {
	if len(payload) == 0 {
		return errors.New("menu payload is empty")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO admin_menu(id,payload,updated_ts) VALUES(1,?,?) ON CONFLICT(id) DO UPDATE SET payload=excluded.payload,updated_ts=excluded.updated_ts`, payload, time.Now().UTC().UnixMilli())
	return err
}

func (s *Store) LoadUserMenu(ctx context.Context, username string) ([]byte, bool, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return nil, false, errors.New("menu username is empty")
	}
	var payload []byte
	err := s.db.QueryRowContext(ctx, `SELECT payload FROM user_menu WHERE username=?`, username).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return append([]byte(nil), payload...), true, nil
}

func (s *Store) SaveUserMenu(ctx context.Context, username string, payload []byte) error {
	username = strings.TrimSpace(username)
	if username == "" {
		return errors.New("menu username is empty")
	}
	if len(payload) == 0 {
		return errors.New("menu payload is empty")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO user_menu(username,payload,updated_ts) VALUES(?,?,?) ON CONFLICT(username) DO UPDATE SET payload=excluded.payload,updated_ts=excluded.updated_ts`, username, payload, time.Now().UTC().UnixMilli())
	return err
}

func (s *Store) LoadUserTheme(ctx context.Context, username string) (string, bool, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return "", false, errors.New("theme username is empty")
	}
	var theme string
	err := s.db.QueryRowContext(ctx, `SELECT theme FROM user_theme WHERE username=?`, username).Scan(&theme)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return theme, true, nil
}

func (s *Store) SaveUserTheme(ctx context.Context, username, theme string) error {
	username = strings.TrimSpace(username)
	theme = strings.ToLower(strings.TrimSpace(theme))
	if username == "" {
		return errors.New("theme username is empty")
	}
	if theme != "dark" && theme != "light" {
		return errors.New("theme must be dark or light")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO user_theme(username,theme,updated_ts) VALUES(?,?,?) ON CONFLICT(username) DO UPDATE SET theme=excluded.theme,updated_ts=excluded.updated_ts`, username, theme, time.Now().UTC().UnixMilli())
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

// LiquidationDelta returns a complete, chronologically ordered series of
// fixed-width liquidation buckets. Empty buckets are included so a chart can
// keep a stable time axis even when no liquidation was observed.
func (s *Store) LiquidationDelta(ctx context.Context, symbol string, from, to time.Time, interval time.Duration) ([]domain.LiquidationDeltaPoint, error) {
	if symbol == "" || !from.Before(to) || interval <= 0 {
		return []domain.LiquidationDeltaPoint{}, nil
	}
	intervalMS := interval.Milliseconds()
	if intervalMS <= 0 {
		return []domain.LiquidationDeltaPoint{}, nil
	}
	points := make([]domain.LiquidationDeltaPoint, 0, int(to.Sub(from)/interval))
	for at := from.UTC(); at.Before(to.UTC()); at = at.Add(interval) {
		points = append(points, domain.LiquidationDeltaPoint{Time: at})
	}
	if len(points) == 0 {
		return points, nil
	}
	index := make(map[int64]int, len(points))
	for i, point := range points {
		index[point.Time.UnixMilli()] = i
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT CAST(event_ts / ? AS INTEGER) * ?,
			COALESCE(SUM(CASE WHEN position_side='long' THEN notional_usd ELSE 0 END),0),
			COALESCE(SUM(CASE WHEN position_side='short' THEN notional_usd ELSE 0 END),0),
			COUNT(*)
		FROM liquidations
		WHERE exchange='binance' AND symbol=? AND event_ts>=? AND event_ts<?
		GROUP BY CAST(event_ts / ? AS INTEGER) * ?
		ORDER BY 1`, intervalMS, intervalMS, symbol, from.UTC().UnixMilli(), to.UTC().UnixMilli(), intervalMS, intervalMS)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var bucketMS int64
		var longUSD, shortUSD float64
		var count int
		if err = rows.Scan(&bucketMS, &longUSD, &shortUSD, &count); err != nil {
			return nil, err
		}
		position, ok := index[bucketMS]
		if !ok {
			continue
		}
		points[position].LongUSD = longUSD
		points[position].ShortUSD = shortUSD
		points[position].DeltaUSD = shortUSD - longUSD
		points[position].Count = count
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return points, nil
}

func (s *Store) Liquidations(ctx context.Context, symbol string, from, before time.Time, limit int, minimumUSD float64) ([]domain.LiquidationEvent, bool, error) {
	if limit < 1 || limit > 5000 {
		limit = 5000
	}
	if minimumUSD < 0 {
		minimumUSD = 0
	}
	out := make([]domain.LiquidationEvent, 0)
	if !from.Before(before) {
		return out, false, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,exchange,symbol,position_side,event_ts,received_ts,price,quantity,notional_usd,coverage
		FROM liquidations WHERE exchange='binance' AND symbol=? AND event_ts>=? AND event_ts<? AND notional_usd>=?
		ORDER BY event_ts DESC,id DESC LIMIT ?`, symbol, from.UTC().UnixMilli(), before.UTC().UnixMilli(), minimumUSD, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var event domain.LiquidationEvent
		var eventMS, receivedMS int64
		if err = rows.Scan(&event.ID, &event.Exchange, &event.Symbol, &event.PositionSide, &eventMS, &receivedMS, &event.Price, &event.Quantity, &event.NotionalUSD, &event.Coverage); err != nil {
			return nil, false, err
		}
		event.EventTime = time.UnixMilli(eventMS).UTC()
		event.ReceivedAt = time.UnixMilli(receivedMS).UTC()
		out = append(out, event)
	}
	if err = rows.Err(); err != nil {
		return nil, false, err
	}
	truncated := len(out) > limit
	if truncated {
		out = out[:limit]
	}
	for left, right := 0, len(out)-1; left < right; left, right = left+1, right-1 {
		out[left], out[right] = out[right], out[left]
	}
	return out, truncated, nil
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
	return status == volumeprofile.ArchiveStatus, err
}

func (s *Store) CompleteArchiveDays(ctx context.Context, symbol string, from, before time.Time) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM volume_archive_imports WHERE symbol=? AND status=? AND day>=? AND day<?`, symbol, volumeprofile.ArchiveStatus, from.UTC().Format("2006-01-02"), before.UTC().Format("2006-01-02")).Scan(&count)
	return count, err
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func (s *Store) PruneBefore(ctx context.Context, before time.Time) error {
	cutoff := before.UTC().UnixMilli()
	for _, q := range []struct {
		table, column string
	}{
		{"candles", "ts"},
		{"predictions", "ts"},
		{"liquidations", "event_ts"},
		{"volume_profile_snapshots", "ts"},
	} {
		if _, err := s.pruneBatches(ctx, q.table, q.column, cutoff); err != nil {
			return err
		}
	}
	profileCutoff := time.Now().UTC().Add(-72 * time.Hour).UnixMilli()
	for _, table := range []string{"volume_profile_levels", "volume_profile_cursors"} {
		if _, err := s.pruneBatches(ctx, table, "session_ts", profileCutoff); err != nil {
			return err
		}
	}
	if _, err := s.pruneBatches(ctx, "volume_archive_imports", "day", before.UTC().Format("2006-01-02")); err != nil {
		return err
	}
	_, err := s.pruneBatches(ctx, "auth_sessions", "expires_ts", time.Now().UTC().UnixMilli())
	return err
}

const pruneBatchSize = 2000

// pruneBatches bounds each write transaction so daily retention cannot hold the
// single SQLite writer for the duration of a large historical cleanup. Table and
// column are internal constants supplied by the retention methods, never input.
func (s *Store) pruneBatches(ctx context.Context, table, column string, cutoff any) (int64, error) {
	var total int64
	query := `DELETE FROM ` + table + ` WHERE rowid IN (SELECT rowid FROM ` + table + ` WHERE ` + column + `<? LIMIT ?)`
	for {
		result, err := s.db.ExecContext(ctx, query, cutoff, pruneBatchSize)
		if err != nil {
			return total, err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return total, err
		}
		total += count
		if count < pruneBatchSize {
			return total, nil
		}
	}
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
