package store

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

func (s *Store) migrateDashboard(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
CREATE INDEX IF NOT EXISTS liquidation_time_page ON liquidations(exchange,event_ts DESC,id DESC);
CREATE INDEX IF NOT EXISTS liquidation_symbol_page ON liquidations(exchange,symbol,event_ts DESC,id DESC);
CREATE TABLE IF NOT EXISTS wall_events(id TEXT PRIMARY KEY,symbol TEXT NOT NULL,ts INTEGER NOT NULL,ended_ts INTEGER,payload BLOB NOT NULL);
CREATE INDEX IF NOT EXISTS wall_event_time ON wall_events(symbol,ts DESC,id DESC);
CREATE TABLE IF NOT EXISTS book_snapshots(symbol TEXT NOT NULL,ts INTEGER NOT NULL,payload BLOB NOT NULL,PRIMARY KEY(symbol,ts));
CREATE TABLE IF NOT EXISTS market_metrics(symbol TEXT NOT NULL,ts INTEGER NOT NULL,payload BLOB NOT NULL,PRIMARY KEY(symbol,ts));
CREATE TABLE IF NOT EXISTS gamma_latest(symbol TEXT PRIMARY KEY,payload BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS deribit_gamma_history(symbol TEXT NOT NULL,ts INTEGER NOT NULL,payload BLOB NOT NULL,PRIMARY KEY(symbol,ts));`)
	return err
}

type pageCursor struct {
	Time int64  `json:"t"`
	ID   string `json:"i"`
}

func EncodeCursor(t time.Time, id string) string {
	b, _ := json.Marshal(pageCursor{t.UnixMilli(), id})
	return base64.RawURLEncoding.EncodeToString(b)
}
func DecodeCursor(value string) (time.Time, string, error) {
	if value == "" {
		return time.Time{}, "", nil
	}
	b, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(b) > 256 {
		return time.Time{}, "", errors.New("invalid cursor")
	}
	var c pageCursor
	if json.Unmarshal(b, &c) != nil || c.Time <= 0 {
		return time.Time{}, "", errors.New("invalid cursor")
	}
	return time.UnixMilli(c.Time).UTC(), c.ID, nil
}

func liquidationWhere(f domain.LiquidationFilter) (string, []any) {
	w := "exchange='binance' AND event_ts>=? AND event_ts<?"
	a := []any{f.From.UnixMilli(), f.To.UnixMilli()}
	if f.Symbol != "" && f.Symbol != "ALL" {
		w += " AND symbol=?"
		a = append(a, f.Symbol)
	}
	if f.Side != "" && f.Side != "all" {
		w += " AND position_side=?"
		a = append(a, f.Side)
	}
	field := "notional_usd"
	if f.Field == "quantity" {
		field = "quantity"
	}
	w += " AND " + field + ">=?"
	a = append(a, f.Minimum)
	return w, a
}
func (s *Store) LiquidationHistory(ctx context.Context, f domain.LiquidationFilter) (domain.LiquidationPage, error) {
	out := domain.LiquidationPage{Rows: []domain.LiquidationEvent{}, Symbols: []string{}, Periods: []domain.LiquidationPeriod{}, Coverage: "sampled: 每个币对每 1000ms 仅推送最近一笔；历史自本服务采集起累计"}
	if f.Limit < 1 || f.Limit > 500 {
		return out, errors.New("limit must be between 1 and 500")
	}
	where, args := liquidationWhere(f)
	ct, ci, err := DecodeCursor(f.Cursor)
	if err != nil {
		return out, err
	}
	if !ct.IsZero() {
		where += " AND (event_ts<? OR (event_ts=? AND id<?))"
		args = append(args, ct.UnixMilli(), ct.UnixMilli(), ci)
	}
	args = append(args, f.Limit+1)
	rows, err := s.db.QueryContext(ctx, `SELECT id,exchange,symbol,position_side,event_ts,received_ts,price,quantity,notional_usd,coverage FROM liquidations WHERE `+where+` ORDER BY event_ts DESC,id DESC LIMIT ?`, args...)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var e domain.LiquidationEvent
		var ts, rx int64
		if err = rows.Scan(&e.ID, &e.Exchange, &e.Symbol, &e.PositionSide, &ts, &rx, &e.Price, &e.Quantity, &e.NotionalUSD, &e.Coverage); err != nil {
			rows.Close()
			return out, err
		}
		e.EventTime = time.UnixMilli(ts).UTC()
		e.ReceivedAt = time.UnixMilli(rx).UTC()
		out.Rows = append(out.Rows, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if len(out.Rows) > f.Limit {
		out.Rows = out.Rows[:f.Limit]
		last := out.Rows[len(out.Rows)-1]
		out.NextCursor = EncodeCursor(last.EventTime, last.ID)
	}
	var oldest sql.NullInt64
	if err = s.db.QueryRowContext(ctx, `SELECT MIN(event_ts) FROM liquidations WHERE exchange='binance'`).Scan(&oldest); err != nil {
		return out, err
	}
	if oldest.Valid {
		t := time.UnixMilli(oldest.Int64).UTC()
		out.AvailableFrom = &t
	}
	rows, err = s.db.QueryContext(ctx, `SELECT DISTINCT symbol FROM liquidations WHERE exchange='binance' ORDER BY symbol`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var sym string
		if err = rows.Scan(&sym); err != nil {
			rows.Close()
			return out, err
		}
		out.Symbols = append(out.Symbols, sym)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	for _, h := range []int{1, 4, 12, 24} {
		sf := f
		sf.Cursor = ""
		cut := f.To.Add(-time.Duration(h) * time.Hour)
		if sf.From.Before(cut) {
			sf.From = cut
		}
		w, a := liquidationWhere(sf)
		p := domain.LiquidationPeriod{Label: fmt.Sprintf("%dH", h)}
		err = s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE WHEN position_side='long' THEN notional_usd ELSE 0 END),0),COALESCE(SUM(CASE WHEN position_side='short' THEN notional_usd ELSE 0 END),0),COUNT(*) FROM liquidations WHERE `+w, a...).Scan(&p.LongUSD, &p.ShortUSD, &p.Count)
		if err != nil {
			return out, err
		}
		out.Periods = append(out.Periods, p)
	}
	return out, nil
}

func (s *Store) SaveWallEvent(ctx context.Context, e domain.WallEvent) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	var ended any
	if e.EndedAt != nil {
		ended = e.EndedAt.UnixMilli()
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO wall_events(id,symbol,ts,ended_ts,payload) VALUES(?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET ended_ts=excluded.ended_ts,payload=excluded.payload`, e.ID, e.Symbol, e.QualifiedAt.UnixMilli(), ended, b)
	return err
}
func (s *Store) InterruptWallEvents(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT payload FROM wall_events WHERE ended_ts IS NULL`)
	if err != nil {
		return err
	}
	var events []domain.WallEvent
	for rows.Next() {
		var b []byte
		var e domain.WallEvent
		if err = rows.Scan(&b); err != nil {
			rows.Close()
			return err
		}
		if err = json.Unmarshal(b, &e); err != nil {
			rows.Close()
			return err
		}
		events = append(events, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, e := range events {
		t := e.LastSeen
		e.EndedAt = &t
		e.EndReason = "restart"
		if err = s.SaveWallEvent(ctx, e); err != nil {
			return err
		}
	}
	return nil
}
func (s *Store) SaveBookSnapshot(ctx context.Context, b domain.BookSnapshot) error {
	raw, err := json.Marshal(b)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	z := gzip.NewWriter(&buf)
	if _, err = z.Write(raw); err != nil {
		return err
	}
	if err = z.Close(); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT OR IGNORE INTO book_snapshots(symbol,ts,payload) VALUES(?,?,?)`, b.Symbol, b.Time.UnixMilli(), buf.Bytes())
	return err
}
func decodeBook(b []byte) (domain.BookSnapshot, error) {
	var out domain.BookSnapshot
	z, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return out, err
	}
	defer z.Close()
	err = json.NewDecoder(io.LimitReader(z, 1<<20)).Decode(&out)
	return out, err
}
func (s *Store) WallHistory(ctx context.Context, symbol, kind string, from, to time.Time, limit int, cursor string) (domain.WallHistory, error) {
	out := domain.WallHistory{Events: []domain.WallEvent{}, Snapshots: []domain.BookSnapshot{}}
	if limit < 1 || limit > 500 {
		return out, errors.New("invalid history limit")
	}
	t, id, err := DecodeCursor(cursor)
	if err != nil {
		return out, err
	}
	table := "wall_events"
	if kind == "snapshots" {
		table = "book_snapshots"
	}
	w := "symbol=? AND ts>=? AND ts<?"
	a := []any{symbol, from.UnixMilli(), to.UnixMilli()}
	if !t.IsZero() {
		if kind == "snapshots" {
			w += " AND ts<?"
			a = append(a, t.UnixMilli())
		} else {
			w += " AND (ts<? OR (ts=? AND id<?))"
			a = append(a, t.UnixMilli(), t.UnixMilli(), id)
		}
	}
	order := "ts DESC"
	if kind != "snapshots" {
		order += ",id DESC"
	}
	a = append(a, limit+1)
	rows, err := s.db.QueryContext(ctx, `SELECT payload FROM `+table+` WHERE `+w+` ORDER BY `+order+` LIMIT ?`, a...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var b []byte
		if err = rows.Scan(&b); err != nil {
			return out, err
		}
		if kind == "snapshots" {
			book, e := decodeBook(b)
			if e != nil {
				return out, e
			}
			out.Snapshots = append(out.Snapshots, book)
		} else {
			var e domain.WallEvent
			if err = json.Unmarshal(b, &e); err != nil {
				return out, err
			}
			out.Events = append(out.Events, e)
		}
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	if len(out.Events) > limit {
		out.Events = out.Events[:limit]
		last := out.Events[limit-1]
		out.NextCursor = EncodeCursor(last.QualifiedAt, last.ID)
	}
	if len(out.Snapshots) > limit {
		out.Snapshots = out.Snapshots[:limit]
		out.NextCursor = EncodeCursor(out.Snapshots[limit-1].Time, "")
	}
	return out, nil
}

func (s *Store) SaveMarketMetric(ctx context.Context, m domain.MarketMetric) error {
	// Historical endpoints populate different fields at the same timestamp.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var old []byte
	err = tx.QueryRowContext(ctx, `SELECT payload FROM market_metrics WHERE symbol=? AND ts=?`, m.Symbol, m.Time.UnixMilli()).Scan(&old)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	fields := map[string]json.RawMessage{}
	if len(old) > 0 {
		if err = json.Unmarshal(old, &fields); err != nil {
			return err
		}
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	var incoming map[string]json.RawMessage
	if err = json.Unmarshal(b, &incoming); err != nil {
		return err
	}
	for k, v := range incoming {
		if string(v) != "null" {
			fields[k] = v
		}
	}
	b, err = json.Marshal(fields)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO market_metrics(symbol,ts,payload) VALUES(?,?,?) ON CONFLICT(symbol,ts) DO UPDATE SET payload=excluded.payload`, m.Symbol, m.Time.UnixMilli(), b)
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) MarketMetrics(ctx context.Context, symbol string, from, to time.Time) ([]domain.MarketMetric, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT payload FROM market_metrics WHERE symbol=? AND ts>=? AND ts<? ORDER BY ts`, symbol, from.UnixMilli(), to.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.MarketMetric{}
	for rows.Next() {
		var b []byte
		var m domain.MarketMetric
		if err = rows.Scan(&b); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(b, &m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
func (s *Store) SaveGamma(ctx context.Context, g domain.GammaView) error {
	b, err := json.Marshal(g)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO gamma_latest(symbol,payload) VALUES(?,?) ON CONFLICT(symbol) DO UPDATE SET payload=excluded.payload`, g.Symbol, b)
	return err
}
func (s *Store) LatestGamma(ctx context.Context, symbol string) (domain.GammaView, error) {
	var g domain.GammaView
	var b []byte
	err := s.db.QueryRowContext(ctx, `SELECT payload FROM gamma_latest WHERE symbol=?`, symbol).Scan(&b)
	if err == nil {
		err = json.Unmarshal(b, &g)
	}
	return g, err
}
func (s *Store) PruneDashboard(ctx context.Context, now time.Time) error {
	for _, q := range []struct {
		table string
		days  int
	}{{"book_snapshots", 30}, {"wall_events", 180}, {"market_metrics", 180}, {"deribit_gamma_history", 180}} {
		// Small batches avoid holding the single SQLite writer during collection.
		for {
			r, e := s.db.ExecContext(ctx, `DELETE FROM `+q.table+` WHERE rowid IN (SELECT rowid FROM `+q.table+` WHERE ts<? LIMIT 2000)`, now.AddDate(0, 0, -q.days).UnixMilli())
			if e != nil {
				return e
			}
			n, e := r.RowsAffected()
			if e != nil {
				return e
			}
			if n < 2000 {
				break
			}
		}
	}
	return nil
}

func ValidateHistoryKind(kind string) bool {
	return strings.EqualFold(kind, "events") || strings.EqualFold(kind, "snapshots")
}
