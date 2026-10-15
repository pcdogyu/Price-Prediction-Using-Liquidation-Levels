package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

// migrateAlerts creates the tables behind the liquidation alert engine. Alerts and
// signals store their JSON payload next to the indexed columns that drive the
// history pages, so a row stays readable even if the struct gains fields later.
func (s *Store) migrateAlerts(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS liquidation_alerts(id TEXT PRIMARY KEY,symbol TEXT NOT NULL,side TEXT NOT NULL,trigger_ts INTEGER NOT NULL,status TEXT NOT NULL,payload BLOB NOT NULL,updated_ts INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS liquidation_alerts_time ON liquidation_alerts(trigger_ts DESC,id DESC);
CREATE INDEX IF NOT EXISTS liquidation_alerts_symbol ON liquidation_alerts(symbol,trigger_ts DESC);
CREATE TABLE IF NOT EXISTS trade_signals(id TEXT PRIMARY KEY,symbol TEXT NOT NULL,kind TEXT NOT NULL,side TEXT NOT NULL,alert_id TEXT NOT NULL DEFAULT '',created_ts INTEGER NOT NULL,notified INTEGER NOT NULL DEFAULT 0,payload BLOB NOT NULL,updated_ts INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS trade_signals_time ON trade_signals(created_ts DESC,id DESC);
CREATE INDEX IF NOT EXISTS trade_signals_kind ON trade_signals(kind,created_ts DESC);
CREATE INDEX IF NOT EXISTS trade_signals_symbol ON trade_signals(symbol,created_ts DESC);
CREATE TABLE IF NOT EXISTS alert_watch_symbols(symbol TEXT PRIMARY KEY,rank INTEGER NOT NULL,quote_volume_usd REAL NOT NULL,updated_ts INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS alert_settings(id INTEGER PRIMARY KEY CHECK(id=1),telegram_enabled INTEGER NOT NULL,telegram_token TEXT NOT NULL,telegram_chat_id TEXT NOT NULL,notify_alerts INTEGER NOT NULL,notify_aggressive INTEGER NOT NULL,notify_steady INTEGER NOT NULL,updated_ts INTEGER NOT NULL);`)
	return err
}

// SaveLiquidationAlert inserts or refreshes an alert row and reports whether the
// row is new, which is what gates the first notification of an alert.
func (s *Store) SaveLiquidationAlert(ctx context.Context, alert domain.LiquidationAlert) (bool, error) {
	if alert.ID == "" || alert.Symbol == "" {
		return false, errors.New("alert id and symbol are required")
	}
	payload, err := json.Marshal(alert)
	if err != nil {
		return false, err
	}
	updated := alert.UpdatedAt
	if updated.IsZero() {
		updated = time.Now().UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var prior int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM liquidation_alerts WHERE id=?`, alert.ID).Scan(&prior); err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO liquidation_alerts(id,symbol,side,trigger_ts,status,payload,updated_ts) VALUES(?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET status=excluded.status,payload=excluded.payload,updated_ts=excluded.updated_ts`,
		alert.ID, alert.Symbol, alert.Side, alert.TriggeredAt.UnixMilli(), alert.Status, payload, updated.UnixMilli()); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return prior == 0, nil
}

// LiquidationAlerts reads the alert history newest first. Side accepts long or
// short, symbol accepts a single symbol or an empty value for all symbols.
func (s *Store) LiquidationAlerts(ctx context.Context, symbol, side string, limit int, cursor string) (domain.AlertPage, error) {
	out := domain.AlertPage{Rows: []domain.LiquidationAlert{}}
	if limit < 1 || limit > 200 {
		return out, errors.New("invalid alert limit")
	}
	at, id, err := DecodeCursor(cursor)
	if err != nil {
		return out, err
	}
	where := "trigger_ts>=0"
	args := []any{}
	if symbol != "" && symbol != "ALL" {
		where += " AND symbol=?"
		args = append(args, symbol)
	}
	if side == "long" || side == "short" {
		where += " AND side=?"
		args = append(args, side)
	}
	if !at.IsZero() {
		where += " AND (trigger_ts<? OR (trigger_ts=? AND id<?))"
		args = append(args, at.UnixMilli(), at.UnixMilli(), id)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT payload FROM liquidation_alerts WHERE `+where+` ORDER BY trigger_ts DESC,id DESC LIMIT ?`, append(args, limit+1)...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var payload []byte
		if err = rows.Scan(&payload); err != nil {
			return out, err
		}
		var alert domain.LiquidationAlert
		if err = json.Unmarshal(payload, &alert); err != nil {
			return out, err
		}
		out.Rows = append(out.Rows, alert)
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	if len(out.Rows) > limit {
		out.Rows = out.Rows[:limit]
		last := out.Rows[len(out.Rows)-1]
		out.NextCursor = EncodeCursor(last.TriggeredAt, last.ID)
	}
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM liquidation_alerts WHERE `+where, args...).Scan(&out.AlertCount)
	return out, err
}

// AlertCount reports how many alerts are stored, used by the summary metrics.
func (s *Store) AlertCount(ctx context.Context) (int64, error) {
	var count int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM liquidation_alerts`).Scan(&count)
	return count, err
}

// SaveTradeSignal inserts a signal once and reports whether it is new. The
// deterministic IDs make a restart replay a no-op, so the caller only notifies for
// a signal that was really created now.
func (s *Store) SaveTradeSignal(ctx context.Context, signal domain.TradeSignal) (bool, error) {
	if signal.ID == "" || signal.Symbol == "" {
		return false, errors.New("signal id and symbol are required")
	}
	payload, err := json.Marshal(signal)
	if err != nil {
		return false, err
	}
	res, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO trade_signals(id,symbol,kind,side,alert_id,created_ts,notified,payload,updated_ts) VALUES(?,?,?,?,?,?,0,?,?)`,
		signal.ID, signal.Symbol, signal.Kind, signal.Side, signal.AlertID, signal.CreatedAt.UnixMilli(), payload, time.Now().UTC().UnixMilli())
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

// MarkTradeSignalNotified records the delivery result so the page can show whether
// a signal really reached Telegram. The payload keeps its own copy because the
// history pages serve it directly.
func (s *Store) MarkTradeSignalNotified(ctx context.Context, id, status, message string) error {
	if id == "" {
		return errors.New("signal id is required")
	}
	var payload []byte
	err := s.db.QueryRowContext(ctx, `SELECT payload FROM trade_signals WHERE id=?`, id).Scan(&payload)
	if err != nil {
		return err
	}
	var signal domain.TradeSignal
	if err = json.Unmarshal(payload, &signal); err != nil {
		return err
	}
	signal.NotifyStatus = status
	signal.NotifyError = message
	signal.NotifiedAt = time.Now().UTC()
	raw, err := json.Marshal(signal)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE trade_signals SET notified=1,payload=?,updated_ts=? WHERE id=?`, raw, time.Now().UTC().UnixMilli(), id)
	return err
}

// TradeSignals reads the signal history newest first. Kind accepts aggressive or
// steady, empty reads both.
func (s *Store) TradeSignals(ctx context.Context, symbol, kind string, limit int, cursor string) (domain.SignalPage, error) {
	out := domain.SignalPage{Rows: []domain.TradeSignal{}}
	if limit < 1 || limit > 200 {
		return out, errors.New("invalid signal limit")
	}
	at, id, err := DecodeCursor(cursor)
	if err != nil {
		return out, err
	}
	where := "created_ts>=0"
	args := []any{}
	if symbol != "" && symbol != "ALL" {
		where += " AND symbol=?"
		args = append(args, symbol)
	}
	if kind == domain.SignalAggressive || kind == domain.SignalSteady {
		where += " AND kind=?"
		args = append(args, kind)
	}
	if !at.IsZero() {
		where += " AND (created_ts<? OR (created_ts=? AND id<?))"
		args = append(args, at.UnixMilli(), at.UnixMilli(), id)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT payload FROM trade_signals WHERE `+where+` ORDER BY created_ts DESC,id DESC LIMIT ?`, append(args, limit+1)...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var payload []byte
		if err = rows.Scan(&payload); err != nil {
			return out, err
		}
		var signal domain.TradeSignal
		if err = json.Unmarshal(payload, &signal); err != nil {
			return out, err
		}
		out.Rows = append(out.Rows, signal)
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	if len(out.Rows) > limit {
		out.Rows = out.Rows[:limit]
		last := out.Rows[len(out.Rows)-1]
		out.NextCursor = EncodeCursor(last.CreatedAt, last.ID)
	}
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(kind='aggressive'),0),COALESCE(SUM(kind='steady'),0) FROM trade_signals WHERE `+where, args...).Scan(&out.SignalCount, &out.AggressiveCount, &out.SteadyCount)
	return out, err
}

// SignalTotals reports the stored signal counts per kind.
func (s *Store) SignalTotals(ctx context.Context) (total, aggressive, steady int64, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(kind='aggressive'),0),COALESCE(SUM(kind='steady'),0) FROM trade_signals`).Scan(&total, &aggressive, &steady)
	return
}

// ReplaceWatchSymbols stores the current top volume watch list as one snapshot.
func (s *Store) ReplaceWatchSymbols(ctx context.Context, rows []domain.WatchSymbol) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `DELETE FROM alert_watch_symbols`); err != nil {
		return err
	}
	for _, row := range rows {
		if row.Symbol == "" {
			continue
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO alert_watch_symbols(symbol,rank,quote_volume_usd,updated_ts) VALUES(?,?,?,?)`, row.Symbol, row.Rank, row.QuoteVolumeUSD, row.UpdatedAt.UnixMilli()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// WatchSymbols reads the last persisted watch list ordered by rank.
func (s *Store) WatchSymbols(ctx context.Context) ([]domain.WatchSymbol, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT symbol,rank,quote_volume_usd,updated_ts FROM alert_watch_symbols ORDER BY rank,symbol`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.WatchSymbol{}
	for rows.Next() {
		var row domain.WatchSymbol
		var updated int64
		if err = rows.Scan(&row.Symbol, &row.Rank, &row.QuoteVolumeUSD, &updated); err != nil {
			return nil, err
		}
		row.UpdatedAt = time.UnixMilli(updated).UTC()
		out = append(out, row)
	}
	return out, rows.Err()
}

// AlertSettings reads the persisted notification settings. The boolean reports
// whether a row exists, so the caller can fall back to environment defaults. The
// bot token never leaves this function unmasked to other layers than the notifier.
func (s *Store) AlertSettings(ctx context.Context) (domain.NotificationSettings, bool, error) {
	var out domain.NotificationSettings
	var enabled, alerts, aggressive, steady int
	var token, chatID string
	var updated int64
	err := s.db.QueryRowContext(ctx, `SELECT telegram_enabled,telegram_token,telegram_chat_id,notify_alerts,notify_aggressive,notify_steady,updated_ts FROM alert_settings WHERE id=1`).
		Scan(&enabled, &token, &chatID, &alerts, &aggressive, &steady, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return out, false, nil
	}
	if err != nil {
		return out, false, err
	}
	out = domain.NotificationSettings{TelegramEnabled: enabled != 0, TelegramToken: token, TelegramChatID: chatID, NotifyAlerts: alerts != 0, NotifyAggressive: aggressive != 0, NotifySteady: steady != 0, UpdatedAt: time.UnixMilli(updated).UTC()}
	return out, true, nil
}

// SaveAlertSettings stores the settings as the single row the alert page manages.
func (s *Store) SaveAlertSettings(ctx context.Context, in domain.NotificationSettings) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO alert_settings(id,telegram_enabled,telegram_token,telegram_chat_id,notify_alerts,notify_aggressive,notify_steady,updated_ts) VALUES(1,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET telegram_enabled=excluded.telegram_enabled,telegram_token=excluded.telegram_token,telegram_chat_id=excluded.telegram_chat_id,notify_alerts=excluded.notify_alerts,notify_aggressive=excluded.notify_aggressive,notify_steady=excluded.notify_steady,updated_ts=excluded.updated_ts`,
		boolInt(in.TelegramEnabled), in.TelegramToken, in.TelegramChatID, boolInt(in.NotifyAlerts), boolInt(in.NotifyAggressive), boolInt(in.NotifySteady), time.Now().UTC().UnixMilli())
	return err
}

// RecentLiquidationEvents loads stored liquidations oldest first. The alert engine
// replays them after a restart so rolling windows and open cycles keep running.
func (s *Store) RecentLiquidationEvents(ctx context.Context, since time.Time, limit int) ([]domain.LiquidationEvent, error) {
	if limit < 1 || limit > 200000 {
		return nil, errors.New("invalid liquidation read limit")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,exchange,symbol,position_side,event_ts,received_ts,price,quantity,notional_usd,coverage FROM liquidations WHERE exchange='binance' AND event_ts>=? ORDER BY event_ts ASC,id ASC LIMIT ?`, since.UnixMilli(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.LiquidationEvent{}
	for rows.Next() {
		var e domain.LiquidationEvent
		var eventTS, receivedTS int64
		if err = rows.Scan(&e.ID, &e.Exchange, &e.Symbol, &e.PositionSide, &eventTS, &receivedTS, &e.Price, &e.Quantity, &e.NotionalUSD, &e.Coverage); err != nil {
			return nil, err
		}
		e.EventTime, e.ReceivedAt = time.UnixMilli(eventTS).UTC(), time.UnixMilli(receivedTS).UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}

// PruneAlerts deletes alert and signal rows older than the retention horizon in
// small batches, mirroring how the liquidation history is pruned.
func (s *Store) PruneAlerts(ctx context.Context, before time.Time) error {
	cutoff := before.UTC().UnixMilli()
	for _, target := range []struct {
		table, column string
	}{
		{"liquidation_alerts", "trigger_ts"},
		{"trade_signals", "created_ts"},
	} {
		if _, err := s.pruneBatches(ctx, target.table, target.column, cutoff); err != nil {
			return err
		}
	}
	return nil
}

// AlertOldestTime reports the oldest stored alert, so the page can show the range
// of data available for backtesting.
func (s *Store) AlertOldestTime(ctx context.Context) (time.Time, error) {
	var oldest sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT MIN(trigger_ts) FROM liquidation_alerts`).Scan(&oldest); err != nil {
		return time.Time{}, err
	}
	if !oldest.Valid {
		return time.Time{}, nil
	}
	return time.UnixMilli(oldest.Int64).UTC(), nil
}
