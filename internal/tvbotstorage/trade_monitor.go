package storage

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

const (
	TradeLifecycleEntryPending     = "entry_pending"
	TradeLifecycleOpen             = "open"
	TradeLifecycleExited           = "exited"
	TradeLifecycleSLHit            = "sl_hit"
	TradeLifecycleTPHit            = "tp_hit"
	TradeLifecycleReentrySubmitted = "reentry_submitted"
	TradeLifecycleCooldown         = "cooldown"
	TradeLifecycleBlocked          = "blocked"
)

type BinanceFill struct {
	APIID           string `json:"api_id"`
	Symbol          string `json:"symbol"`
	TradeID         string `json:"trade_id"`
	OrderID         string `json:"order_id,omitempty"`
	Side            string `json:"side,omitempty"`
	PositionSide    string `json:"position_side,omitempty"`
	Price           string `json:"price,omitempty"`
	Qty             string `json:"qty,omitempty"`
	QuoteQty        string `json:"quote_qty,omitempty"`
	RealizedPnl     string `json:"realized_pnl,omitempty"`
	Commission      string `json:"commission,omitempty"`
	CommissionAsset string `json:"commission_asset,omitempty"`
	Buyer           bool   `json:"buyer"`
	Maker           bool   `json:"maker"`
	FillTime        int64  `json:"fill_time"`
	RawJSON         string `json:"raw_json,omitempty"`
}

type TradeLifecycle struct {
	LifecycleID          string    `json:"lifecycle_id"`
	SourceSignalID       string    `json:"source_signal_id"`
	RootSignalID         string    `json:"root_signal_id"`
	ReentryOfLifecycleID string    `json:"reentry_of_lifecycle_id,omitempty"`
	ReentrySignalID      string    `json:"reentry_signal_id,omitempty"`
	Exchange             string    `json:"exchange"`
	APIID                string    `json:"api_id"`
	Symbol               string    `json:"symbol"`
	Action               string    `json:"action"`
	Status               string    `json:"status"`
	EntryOrderIDs        []string  `json:"entry_order_ids,omitempty"`
	EntryClientOrderIDs  []string  `json:"entry_client_order_ids,omitempty"`
	TPAlgoIDs            []string  `json:"tp_algo_ids,omitempty"`
	TPClientAlgoIDs      []string  `json:"tp_client_algo_ids,omitempty"`
	TPTriggerPrices      []string  `json:"tp_trigger_prices,omitempty"`
	SLAlgoIDs            []string  `json:"sl_algo_ids,omitempty"`
	SLClientAlgoIDs      []string  `json:"sl_client_algo_ids,omitempty"`
	SLTriggerPrices      []string  `json:"sl_trigger_prices,omitempty"`
	EntryPrice           string    `json:"entry_price,omitempty"`
	EntryQty             string    `json:"entry_qty,omitempty"`
	ExitPrice            string    `json:"exit_price,omitempty"`
	ExitQty              string    `json:"exit_qty,omitempty"`
	RealizedPnl          string    `json:"realized_pnl,omitempty"`
	ReentryCount         int       `json:"reentry_count"`
	CooldownUntil        string    `json:"cooldown_until,omitempty"`
	LastFillTime         int64     `json:"last_fill_time,omitempty"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}

type TradeLifecycleUpdate struct {
	Status          string
	EntryPrice      string
	EntryQty        string
	ExitPrice       string
	ExitQty         string
	RealizedPnl     string
	ReentrySignalID string
	CooldownUntil   string
	LastFillTime    int64
	UpdatedAt       time.Time
}

type TradeMonitorCheckpoint struct {
	Exchange     string    `json:"exchange"`
	APIID        string    `json:"api_id"`
	Symbol       string    `json:"symbol"`
	LastFillTime int64     `json:"last_fill_time"`
	LastTradeID  string    `json:"last_trade_id,omitempty"`
	LastPolledAt time.Time `json:"last_polled_at"`
	LastError    string    `json:"last_error,omitempty"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type TradeMonitorEvent struct {
	EventID        string    `json:"event_id"`
	EventTime      time.Time `json:"event_time"`
	Exchange       string    `json:"exchange"`
	APIID          string    `json:"api_id"`
	Symbol         string    `json:"symbol,omitempty"`
	LifecycleID    string    `json:"lifecycle_id,omitempty"`
	SourceSignalID string    `json:"source_signal_id,omitempty"`
	EventType      string    `json:"event_type"`
	Status         string    `json:"status,omitempty"`
	Message        string    `json:"message,omitempty"`
	RawJSON        string    `json:"raw_json,omitempty"`
}

type TradeMonitorFilter struct {
	Exchange string
	APIID    string
	Symbol   string
	Status   string
}

type TradeLifecycleAction struct {
	ActionKey   string    `json:"action_key"`
	APIID       string    `json:"api_id"`
	LifecycleID string    `json:"lifecycle_id"`
	ActionType  string    `json:"action_type"`
	Status      string    `json:"status"`
	EventTime   time.Time `json:"event_time"`
	LastError   string    `json:"last_error,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

const (
	BinanceFillReconciliationAmbiguous = "ambiguous"
	BinanceFillReconciliationUnmatched = "unmatched"
)

type BinanceFillReconciliation struct {
	APIID                   string    `json:"api_id"`
	Symbol                  string    `json:"symbol"`
	TradeID                 string    `json:"trade_id"`
	OrderID                 string    `json:"order_id,omitempty"`
	Side                    string    `json:"side,omitempty"`
	PositionSide            string    `json:"position_side,omitempty"`
	FillTime                int64     `json:"fill_time"`
	Status                  string    `json:"status"`
	Reason                  string    `json:"reason"`
	CandidateLifecycleCount int       `json:"candidate_lifecycle_count"`
	AttemptCount            int       `json:"attempt_count"`
	FirstSeenAt             time.Time `json:"first_seen_at"`
	LastAttemptedAt         time.Time `json:"last_attempted_at"`
	UpdatedAt               time.Time `json:"updated_at"`
}

func BinanceLifecycleActionKey(fill BinanceFill, actionType string) string {
	fill.normalize(0)
	return strings.Join([]string{strings.ToLower(strings.TrimSpace(actionType)), fill.APIID, fill.Symbol, fill.TradeID}, ":")
}

func (s *OrderStore) UpsertBinanceFills(fills []BinanceFill, fetchedAt time.Time) ([]BinanceFill, error) {
	if s.db == nil {
		return nil, errors.New("sqlite database is not configured")
	}
	if len(fills) == 0 {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO binance_fills
		(api_id, symbol, trade_id, order_id, side, position_side, price, qty, quote_qty, realized_pnl, commission, commission_asset, buyer, maker, fill_time, raw_json, fetched_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return nil, err
	}
	defer stmt.Close()
	inserted := make([]BinanceFill, 0, len(fills))
	for i, fill := range fills {
		fill.normalize(i)
		res, err := stmt.Exec(
			fill.APIID,
			fill.Symbol,
			fill.TradeID,
			fill.OrderID,
			fill.Side,
			fill.PositionSide,
			fill.Price,
			fill.Qty,
			fill.QuoteQty,
			fill.RealizedPnl,
			fill.Commission,
			fill.CommissionAsset,
			boolInt(fill.Buyer),
			boolInt(fill.Maker),
			fill.FillTime,
			fill.RawJSON,
			fetchedAt.UTC().Format(time.RFC3339Nano),
		)
		if err != nil {
			return inserted, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			inserted = append(inserted, fill)
		}
	}
	return inserted, tx.Commit()
}

// HasBinanceFillLifecycleAssociation reports whether a Binance fill has
// already been applied to a lifecycle. The association key is the same
// account/symbol/trade identity used by binance_fills.
func (s *OrderStore) HasBinanceFillLifecycleAssociation(apiID, symbol, tradeID string) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("sqlite database is not configured")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var found int
	err := s.db.QueryRow(`SELECT 1 FROM trade_lifecycle_fills WHERE api_id=? AND symbol=? AND trade_id=?`,
		strings.TrimSpace(apiID), strings.ToUpper(strings.TrimSpace(symbol)), strings.TrimSpace(tradeID)).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// ApplyBinanceFillToLifecycle atomically links a fill to one lifecycle and
// rebuilds its entry/exit aggregates from the canonical fill table. Replays
// are safe: a fill can be linked only once, and the lifecycle projection is
// committed in the same transaction as that link.
func (s *OrderStore) ApplyBinanceFillToLifecycle(fill BinanceFill, lifecycleID, role string, now time.Time) (bool, TradeLifecycle, error) {
	if s == nil || s.db == nil {
		return false, TradeLifecycle{}, errors.New("sqlite database is not configured")
	}
	fill.normalize(0)
	lifecycleID = strings.TrimSpace(lifecycleID)
	role = strings.ToLower(strings.TrimSpace(role))
	if fill.APIID == "" || fill.Symbol == "" || fill.TradeID == "" || lifecycleID == "" {
		return false, TradeLifecycle{}, errors.New("Binance fill lifecycle association requires account, symbol, trade, and lifecycle IDs")
	}
	if role != "entry" && role != "exit" {
		return false, TradeLifecycle{}, fmt.Errorf("unsupported Binance lifecycle fill role %q", role)
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return false, TradeLifecycle{}, err
	}
	defer tx.Rollback()

	current, err := scanTradeLifecycle(tx.QueryRow(`SELECT lifecycle_id, source_signal_id, root_signal_id, reentry_of_lifecycle_id, reentry_signal_id,
		exchange, api_id, symbol, action, status, entry_order_ids, entry_client_order_ids, tp_algo_ids, tp_client_algo_ids,
		tp_trigger_prices, sl_algo_ids, sl_client_algo_ids, sl_trigger_prices, entry_price, entry_qty, exit_price, exit_qty,
		realized_pnl, reentry_count, cooldown_until, last_fill_time, created_at, updated_at
		FROM trade_lifecycles WHERE lifecycle_id=?`, lifecycleID).Scan)
	if err != nil {
		return false, TradeLifecycle{}, err
	}
	if trading.NormalizeExchange(current.Exchange) != trading.ExchangeBinance || current.APIID != fill.APIID || !strings.EqualFold(current.Symbol, fill.Symbol) {
		return false, TradeLifecycle{}, fmt.Errorf("Binance fill %s/%s/%s does not belong to lifecycle %s", fill.APIID, fill.Symbol, fill.TradeID, lifecycleID)
	}
	res, err := tx.Exec(`INSERT OR IGNORE INTO trade_lifecycle_fills(api_id,symbol,trade_id,lifecycle_id,role,linked_at)
		SELECT ?,?,?,?,?,? WHERE EXISTS (
			SELECT 1 FROM binance_fills WHERE api_id=? AND symbol=? AND trade_id=?
		)`, fill.APIID, fill.Symbol, fill.TradeID, lifecycleID, role, now.UTC().Format(time.RFC3339Nano), fill.APIID, fill.Symbol, fill.TradeID)
	if err != nil {
		return false, TradeLifecycle{}, err
	}
	inserted, err := res.RowsAffected()
	if err != nil {
		return false, TradeLifecycle{}, err
	}
	if inserted == 0 {
		var existingLifecycleID, existingRole string
		lookupErr := tx.QueryRow(`SELECT lifecycle_id,role FROM trade_lifecycle_fills WHERE api_id=? AND symbol=? AND trade_id=?`, fill.APIID, fill.Symbol, fill.TradeID).Scan(&existingLifecycleID, &existingRole)
		if lookupErr != nil {
			if errors.Is(lookupErr, sql.ErrNoRows) {
				return false, TradeLifecycle{}, errors.New("Binance fill must be persisted before lifecycle reconciliation")
			}
			return false, TradeLifecycle{}, lookupErr
		}
		if existingLifecycleID != lifecycleID || existingRole != role {
			return false, TradeLifecycle{}, fmt.Errorf("Binance fill %s/%s/%s is already linked to lifecycle %s as %s", fill.APIID, fill.Symbol, fill.TradeID, existingLifecycleID, existingRole)
		}
		if _, err := tx.Exec(`DELETE FROM binance_fill_reconciliation WHERE api_id=? AND symbol=? AND trade_id=?`, fill.APIID, fill.Symbol, fill.TradeID); err != nil {
			return false, TradeLifecycle{}, err
		}
		if err := tx.Commit(); err != nil {
			return false, TradeLifecycle{}, err
		}
		return false, current, nil
	}

	entry, err := aggregateLifecycleFills(tx, lifecycleID, "entry")
	if err != nil {
		return false, TradeLifecycle{}, err
	}
	exit, err := aggregateLifecycleFills(tx, lifecycleID, "exit")
	if err != nil {
		return false, TradeLifecycle{}, err
	}
	status := TradeLifecycleOpen
	if entry.qty.Sign() > 0 && exit.qty.Cmp(entry.qty) >= 0 {
		if absRat(exit.realizedPnl).Cmp(big.NewRat(1, 1_000_000_000_000)) <= 0 {
			status = TradeLifecycleExited
		} else if exit.realizedPnl.Sign() < 0 {
			status = TradeLifecycleSLHit
		} else {
			status = TradeLifecycleTPHit
		}
	}
	lastFill := entry.lastFillTime
	if exit.lastFillTime > lastFill {
		lastFill = exit.lastFillTime
	}
	_, err = tx.Exec(`UPDATE trade_lifecycles SET status=?,entry_price=?,entry_qty=?,exit_price=?,exit_qty=?,realized_pnl=?,last_fill_time=?,updated_at=? WHERE lifecycle_id=?`,
		status,
		aggregateAverage(entry),
		ratString(entry.qty),
		aggregateAverage(exit),
		ratString(exit.qty),
		ratString(exit.realizedPnl),
		lastFill,
		now.UTC().Format(time.RFC3339Nano),
		lifecycleID,
	)
	if err != nil {
		return false, TradeLifecycle{}, err
	}
	if role == "exit" && status == TradeLifecycleSLHit {
		eventTime := now.UTC()
		if fill.FillTime > 0 {
			eventTime = time.UnixMilli(fill.FillTime).UTC()
		}
		_, err = tx.Exec(`INSERT OR IGNORE INTO trade_lifecycle_actions(action_key,api_id,lifecycle_id,action_type,status,event_time,created_at,updated_at)
			VALUES(?,?,?,'auto_reentry','pending',?,?,?)`, BinanceLifecycleActionKey(fill, "auto_reentry"), fill.APIID, lifecycleID,
			eventTime.Format(time.RFC3339Nano), now.UTC().Format(time.RFC3339Nano), now.UTC().Format(time.RFC3339Nano))
		if err != nil {
			return false, TradeLifecycle{}, err
		}
	}
	if _, err := tx.Exec(`DELETE FROM binance_fill_reconciliation WHERE api_id=? AND symbol=? AND trade_id=?`, fill.APIID, fill.Symbol, fill.TradeID); err != nil {
		return false, TradeLifecycle{}, err
	}
	if err := tx.Commit(); err != nil {
		return false, TradeLifecycle{}, err
	}
	updated, err := s.findTradeLifecycleSQLiteLocked(lifecycleID)
	return true, updated, err
}

// RecordBinanceFillReconciliation persists why a potential exit fill could not
// be assigned safely. Replays refresh its evidence and increment attempts.
func (s *OrderStore) RecordBinanceFillReconciliation(fill BinanceFill, status, reason string, candidateCount int, now time.Time) error {
	if s == nil || s.db == nil {
		return errors.New("sqlite database is not configured")
	}
	fill.normalize(0)
	status = strings.ToLower(strings.TrimSpace(status))
	reason = strings.TrimSpace(reason)
	if fill.APIID == "" || fill.Symbol == "" || fill.TradeID == "" {
		return errors.New("Binance fill reconciliation requires account, symbol, and trade IDs")
	}
	if status != BinanceFillReconciliationAmbiguous && status != BinanceFillReconciliationUnmatched {
		return fmt.Errorf("unsupported Binance fill reconciliation status %q", status)
	}
	if reason == "" {
		return errors.New("Binance fill reconciliation reason is required")
	}
	if candidateCount < 0 {
		candidateCount = 0
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	stamp := now.UTC().Format(time.RFC3339Nano)
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRow(`SELECT 1 FROM binance_fills WHERE api_id=? AND symbol=? AND trade_id=?`, fill.APIID, fill.Symbol, fill.TradeID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("Binance fill must be persisted before reconciliation is recorded")
		}
		return err
	}
	_, err = tx.Exec(`INSERT INTO binance_fill_reconciliation
		(api_id,symbol,trade_id,status,reason,candidate_lifecycle_count,attempt_count,first_seen_at,last_attempted_at,updated_at)
		VALUES(?,?,?,?,?,?,1,?,?,?)
		ON CONFLICT(api_id,symbol,trade_id) DO UPDATE SET
		status=excluded.status,reason=excluded.reason,candidate_lifecycle_count=excluded.candidate_lifecycle_count,
		attempt_count=binance_fill_reconciliation.attempt_count+1,last_attempted_at=excluded.last_attempted_at,updated_at=excluded.updated_at`,
		fill.APIID, fill.Symbol, fill.TradeID, status, reason, candidateCount, stamp, stamp, stamp)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *OrderStore) ListBinanceFillReconciliations(filter TradeMonitorFilter, limit int) ([]BinanceFillReconciliation, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("sqlite database is not configured")
	}
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	filter.normalize()
	where := []string{"1=1"}
	args := []any{}
	if filter.APIID != "" {
		where = append(where, "r.api_id=?")
		args = append(args, filter.APIID)
	}
	if filter.Symbol != "" {
		where = append(where, "r.symbol=?")
		args = append(args, filter.Symbol)
	}
	args = append(args, limit)
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT r.api_id,r.symbol,r.trade_id,COALESCE(f.order_id,''),COALESCE(f.side,''),COALESCE(f.position_side,''),
		COALESCE(f.fill_time,0),r.status,r.reason,r.candidate_lifecycle_count,r.attempt_count,r.first_seen_at,r.last_attempted_at,r.updated_at
		FROM binance_fill_reconciliation r LEFT JOIN binance_fills f
		ON f.api_id=r.api_id AND f.symbol=r.symbol AND f.trade_id=r.trade_id
		WHERE `+strings.Join(where, " AND ")+` ORDER BY r.last_attempted_at DESC,r.symbol,r.trade_id LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]BinanceFillReconciliation, 0)
	for rows.Next() {
		var item BinanceFillReconciliation
		var firstSeenAt, lastAttemptedAt, updatedAt string
		if err := rows.Scan(&item.APIID, &item.Symbol, &item.TradeID, &item.OrderID, &item.Side, &item.PositionSide,
			&item.FillTime, &item.Status, &item.Reason, &item.CandidateLifecycleCount, &item.AttemptCount,
			&firstSeenAt, &lastAttemptedAt, &updatedAt); err != nil {
			return nil, err
		}
		item.FirstSeenAt = parseStoreTime(firstSeenAt)
		item.LastAttemptedAt = parseStoreTime(lastAttemptedAt)
		item.UpdatedAt = parseStoreTime(updatedAt)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *OrderStore) ClaimTradeLifecycleAction(actionKey string, now time.Time) (TradeLifecycleAction, bool, error) {
	if s == nil || s.db == nil {
		return TradeLifecycleAction{}, false, errors.New("sqlite database is not configured")
	}
	actionKey = strings.TrimSpace(actionKey)
	if actionKey == "" {
		return TradeLifecycleAction{}, false, errors.New("action_key is required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Exec(`UPDATE trade_lifecycle_actions SET status='running',updated_at=? WHERE action_key=? AND status='pending'`, now.UTC().Format(time.RFC3339Nano), actionKey)
	if err != nil {
		return TradeLifecycleAction{}, false, err
	}
	claimed, err := res.RowsAffected()
	if err != nil || claimed == 0 {
		return TradeLifecycleAction{}, false, err
	}
	action, err := scanTradeLifecycleAction(s.db.QueryRow(`SELECT action_key,api_id,lifecycle_id,action_type,status,event_time,last_error,created_at,updated_at FROM trade_lifecycle_actions WHERE action_key=?`, actionKey).Scan)
	return action, err == nil, err
}

func (s *OrderStore) ListPendingTradeLifecycleActions(apiID string, limit int) ([]TradeLifecycleAction, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("sqlite database is not configured")
	}
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT action_key,api_id,lifecycle_id,action_type,status,event_time,last_error,created_at,updated_at
		FROM trade_lifecycle_actions WHERE api_id=? AND status='pending' ORDER BY created_at,action_key LIMIT ?`, strings.TrimSpace(apiID), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]TradeLifecycleAction, 0)
	for rows.Next() {
		action, err := scanTradeLifecycleAction(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, action)
	}
	return out, rows.Err()
}

func (s *OrderStore) FinishTradeLifecycleAction(actionKey, status, lastError string, now time.Time) error {
	if s == nil || s.db == nil {
		return errors.New("sqlite database is not configured")
	}
	status = strings.ToLower(strings.TrimSpace(status))
	if status != "done" && status != "failed" && status != "skipped" {
		return fmt.Errorf("unsupported trade lifecycle action status %q", status)
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Exec(`UPDATE trade_lifecycle_actions SET status=?,last_error=?,updated_at=? WHERE action_key=? AND status='running'`,
		status, strings.TrimSpace(lastError), now.UTC().Format(time.RFC3339Nano), strings.TrimSpace(actionKey))
	if err != nil {
		return err
	}
	changed, err := res.RowsAffected()
	if err == nil && changed == 0 {
		return errors.New("trade lifecycle action is not claimed")
	}
	return err
}

func scanTradeLifecycleAction(scan func(dest ...any) error) (TradeLifecycleAction, error) {
	var action TradeLifecycleAction
	var eventTime, createdAt, updatedAt string
	var lastError sql.NullString
	if err := scan(&action.ActionKey, &action.APIID, &action.LifecycleID, &action.ActionType, &action.Status, &eventTime, &lastError, &createdAt, &updatedAt); err != nil {
		return TradeLifecycleAction{}, err
	}
	action.LastError = lastError.String
	action.EventTime = parseStoreTime(eventTime)
	action.CreatedAt = parseStoreTime(createdAt)
	action.UpdatedAt = parseStoreTime(updatedAt)
	return action, nil
}

type binanceLifecycleFillAggregate struct {
	qty          *big.Rat
	notional     *big.Rat
	realizedPnl  *big.Rat
	lastFillTime int64
}

func aggregateLifecycleFills(tx *sql.Tx, lifecycleID, role string) (binanceLifecycleFillAggregate, error) {
	out := binanceLifecycleFillAggregate{qty: new(big.Rat), notional: new(big.Rat), realizedPnl: new(big.Rat)}
	rows, err := tx.Query(`SELECT f.price,f.qty,f.realized_pnl,f.fill_time FROM trade_lifecycle_fills l
		JOIN binance_fills f ON f.api_id=l.api_id AND f.symbol=l.symbol AND f.trade_id=l.trade_id
		WHERE l.lifecycle_id=? AND l.role=? ORDER BY f.fill_time,f.trade_id`, lifecycleID, role)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var rawPrice, rawQty, rawPnL string
		var fillTime int64
		if err := rows.Scan(&rawPrice, &rawQty, &rawPnL, &fillTime); err != nil {
			return out, err
		}
		price, ok := new(big.Rat).SetString(strings.TrimSpace(rawPrice))
		if !ok {
			return out, fmt.Errorf("invalid Binance fill price %q", rawPrice)
		}
		qty, ok := new(big.Rat).SetString(strings.TrimSpace(rawQty))
		if !ok || qty.Sign() <= 0 {
			return out, fmt.Errorf("invalid Binance fill quantity %q", rawQty)
		}
		pnl := new(big.Rat)
		if strings.TrimSpace(rawPnL) != "" {
			if _, ok := pnl.SetString(strings.TrimSpace(rawPnL)); !ok {
				return out, fmt.Errorf("invalid Binance realized PnL %q", rawPnL)
			}
		}
		out.qty.Add(out.qty, qty)
		out.notional.Add(out.notional, new(big.Rat).Mul(price, qty))
		out.realizedPnl.Add(out.realizedPnl, pnl)
		if fillTime > out.lastFillTime {
			out.lastFillTime = fillTime
		}
	}
	return out, rows.Err()
}

func aggregateAverage(aggregate binanceLifecycleFillAggregate) string {
	if aggregate.qty == nil || aggregate.qty.Sign() == 0 {
		return "0"
	}
	return ratString(new(big.Rat).Quo(aggregate.notional, aggregate.qty))
}

func absRat(value *big.Rat) *big.Rat {
	if value == nil {
		return new(big.Rat)
	}
	return new(big.Rat).Abs(value)
}

func ratString(value *big.Rat) string {
	if value == nil || value.Sign() == 0 {
		return "0"
	}
	text := value.FloatString(16)
	text = strings.TrimRight(strings.TrimRight(text, "0"), ".")
	if text == "-0" || text == "" {
		return "0"
	}
	return text
}

// ListBinanceFills returns fills already persisted by the Binance user stream.
// Analysis uses this local source while the stream is active, avoiding a
// repeated UserTrades REST scan for data the process already owns.
func (s *OrderStore) ListBinanceFills(apiID string, since time.Time) ([]BinanceFill, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("sqlite database is not configured")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT api_id,symbol,trade_id,order_id,side,position_side,price,qty,quote_qty,realized_pnl,commission,commission_asset,buyer,maker,fill_time,raw_json FROM binance_fills WHERE api_id=? AND fill_time>=? ORDER BY fill_time,trade_id`, strings.TrimSpace(apiID), since.UTC().UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BinanceFill
	for rows.Next() {
		var fill BinanceFill
		var buyer, maker int
		if err := rows.Scan(&fill.APIID, &fill.Symbol, &fill.TradeID, &fill.OrderID, &fill.Side, &fill.PositionSide, &fill.Price, &fill.Qty, &fill.QuoteQty, &fill.RealizedPnl, &fill.Commission, &fill.CommissionAsset, &buyer, &maker, &fill.FillTime, &fill.RawJSON); err != nil {
			return nil, err
		}
		fill.Buyer = buyer != 0
		fill.Maker = maker != 0
		out = append(out, fill)
	}
	return out, rows.Err()
}

func (f *BinanceFill) normalize(index int) {
	f.APIID = strings.TrimSpace(f.APIID)
	f.Symbol = strings.ToUpper(strings.TrimSpace(f.Symbol))
	f.TradeID = strings.TrimSpace(f.TradeID)
	if f.TradeID == "" {
		f.TradeID = fallbackBinanceTradeID(*f, index)
	}
	f.OrderID = strings.TrimSpace(f.OrderID)
	f.Side = strings.ToUpper(strings.TrimSpace(f.Side))
	f.PositionSide = strings.ToUpper(strings.TrimSpace(f.PositionSide))
	f.Price = strings.TrimSpace(f.Price)
	f.Qty = strings.TrimSpace(f.Qty)
	f.QuoteQty = strings.TrimSpace(f.QuoteQty)
	f.RealizedPnl = strings.TrimSpace(f.RealizedPnl)
	f.Commission = strings.TrimSpace(f.Commission)
	f.CommissionAsset = strings.TrimSpace(f.CommissionAsset)
	f.RawJSON = strings.TrimSpace(f.RawJSON)
}

// Normalize prepares a fill for lookups and lifecycle reconciliation.
func (f *BinanceFill) Normalize() {
	f.normalize(0)
}

func fallbackBinanceTradeID(fill BinanceFill, index int) string {
	payload := strings.Join([]string{
		fill.APIID,
		fill.Symbol,
		fill.OrderID,
		fill.Side,
		fill.Price,
		fill.Qty,
		strconv.FormatInt(fill.FillTime, 10),
		strconv.Itoa(index),
	}, "|")
	sum := sha256.Sum256([]byte(payload))
	return "fallback-" + hex.EncodeToString(sum[:8])
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func (s *OrderStore) UpsertTradeLifecycleFromOrder(rec OrderRecord, reentryOfLifecycleID string, reentryCount int, now time.Time) (TradeLifecycle, error) {
	if s.db == nil {
		return TradeLifecycle{}, errors.New("sqlite database is not configured")
	}
	lifecycle, ok := tradeLifecycleFromOrder(rec, reentryOfLifecycleID, reentryCount, now)
	if !ok {
		return TradeLifecycle{}, errors.New("order record is not a Binance submitted order")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if lifecycle.ReentryOfLifecycleID != "" {
		if parent, err := s.findTradeLifecycleSQLiteLocked(lifecycle.ReentryOfLifecycleID); err == nil && parent.RootSignalID != "" {
			lifecycle.RootSignalID = parent.RootSignalID
		}
	}
	_, err := s.db.Exec(`INSERT INTO trade_lifecycles
		(lifecycle_id, source_signal_id, root_signal_id, reentry_of_lifecycle_id, reentry_signal_id, exchange, api_id, symbol, action, status,
		 entry_order_ids, entry_client_order_ids, tp_algo_ids, tp_client_algo_ids, tp_trigger_prices, sl_algo_ids, sl_client_algo_ids, sl_trigger_prices,
		 entry_price, entry_qty, exit_price, exit_qty, realized_pnl, reentry_count, cooldown_until, last_fill_time, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(lifecycle_id) DO UPDATE SET
			source_signal_id=excluded.source_signal_id,
			root_signal_id=excluded.root_signal_id,
			reentry_of_lifecycle_id=excluded.reentry_of_lifecycle_id,
			exchange=excluded.exchange,
			api_id=excluded.api_id,
			symbol=excluded.symbol,
			action=excluded.action,
			entry_order_ids=excluded.entry_order_ids,
			entry_client_order_ids=excluded.entry_client_order_ids,
			tp_algo_ids=excluded.tp_algo_ids,
			tp_client_algo_ids=excluded.tp_client_algo_ids,
			tp_trigger_prices=excluded.tp_trigger_prices,
			sl_algo_ids=excluded.sl_algo_ids,
			sl_client_algo_ids=excluded.sl_client_algo_ids,
			sl_trigger_prices=excluded.sl_trigger_prices,
			reentry_count=excluded.reentry_count,
			updated_at=excluded.updated_at`,
		lifecycle.LifecycleID,
		lifecycle.SourceSignalID,
		lifecycle.RootSignalID,
		lifecycle.ReentryOfLifecycleID,
		lifecycle.ReentrySignalID,
		lifecycle.Exchange,
		lifecycle.APIID,
		lifecycle.Symbol,
		lifecycle.Action,
		lifecycle.Status,
		joinMonitorList(lifecycle.EntryOrderIDs),
		joinMonitorList(lifecycle.EntryClientOrderIDs),
		joinMonitorList(lifecycle.TPAlgoIDs),
		joinMonitorList(lifecycle.TPClientAlgoIDs),
		joinMonitorList(lifecycle.TPTriggerPrices),
		joinMonitorList(lifecycle.SLAlgoIDs),
		joinMonitorList(lifecycle.SLClientAlgoIDs),
		joinMonitorList(lifecycle.SLTriggerPrices),
		lifecycle.EntryPrice,
		lifecycle.EntryQty,
		lifecycle.ExitPrice,
		lifecycle.ExitQty,
		lifecycle.RealizedPnl,
		lifecycle.ReentryCount,
		lifecycle.CooldownUntil,
		lifecycle.LastFillTime,
		lifecycle.CreatedAt.UTC().Format(time.RFC3339Nano),
		lifecycle.UpdatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return TradeLifecycle{}, err
	}
	return s.findTradeLifecycleSQLiteLocked(lifecycle.LifecycleID)
}

func tradeLifecycleFromOrder(rec OrderRecord, reentryOfLifecycleID string, reentryCount int, now time.Time) (TradeLifecycle, bool) {
	if rec.SignalID == "" || (rec.Status != StatusSubmitted && rec.Status != StatusPartial) || trading.NormalizeExchange(rec.TargetExchange) != trading.ExchangeBinance {
		return TradeLifecycle{}, false
	}
	if strings.EqualFold(strings.TrimSpace(rec.PositionEffect), trading.PositionEffectClose) || strings.EqualFold(strings.TrimSpace(rec.Result.PositionEffect), trading.PositionEffectClose) {
		return TradeLifecycle{}, false
	}
	symbol := strings.ToUpper(strings.TrimSpace(rec.Result.InstID))
	if symbol == "" {
		return TradeLifecycle{}, false
	}
	apiID := strings.TrimSpace(rec.Result.APIID)
	if apiID == "" {
		apiID = strings.TrimSpace(rec.APIID)
	}
	lifecycle := TradeLifecycle{
		LifecycleID:          rec.SignalID,
		SourceSignalID:       rec.SignalID,
		RootSignalID:         rec.SignalID,
		ReentryOfLifecycleID: strings.TrimSpace(reentryOfLifecycleID),
		Exchange:             trading.ExchangeBinance,
		APIID:                apiID,
		Symbol:               symbol,
		Action:               string(rec.Action),
		Status:               TradeLifecycleEntryPending,
		EntryOrderIDs:        splitMonitorList(rec.Result.OrdID),
		EntryClientOrderIDs:  splitMonitorList(rec.Result.ClOrdID),
		ReentryCount:         reentryCount,
		CreatedAt:            now.UTC(),
		UpdatedAt:            now.UTC(),
	}
	for _, order := range rec.Result.RiskOrders {
		orderType := strings.ToUpper(strings.TrimSpace(order.OrderType))
		switch {
		case strings.Contains(orderType, "TAKE_PROFIT"):
			lifecycle.TPAlgoIDs = appendNonEmpty(lifecycle.TPAlgoIDs, order.AlgoID)
			lifecycle.TPClientAlgoIDs = appendNonEmpty(lifecycle.TPClientAlgoIDs, order.ClientAlgoID)
			lifecycle.TPTriggerPrices = appendNonEmpty(lifecycle.TPTriggerPrices, order.TriggerPrice)
		case strings.Contains(orderType, "STOP"):
			lifecycle.SLAlgoIDs = appendNonEmpty(lifecycle.SLAlgoIDs, order.AlgoID)
			lifecycle.SLClientAlgoIDs = appendNonEmpty(lifecycle.SLClientAlgoIDs, order.ClientAlgoID)
			lifecycle.SLTriggerPrices = appendNonEmpty(lifecycle.SLTriggerPrices, order.TriggerPrice)
		}
	}
	return lifecycle, true
}

func appendNonEmpty(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func splitMonitorList(raw string) []string {
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '/' || r == '\n' || r == '\t'
	})
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		out = append(out, part)
	}
	return out
}

func joinMonitorList(values []string) string {
	if len(values) == 0 {
		return ""
	}
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return strings.Join(out, ",")
}

func (s *OrderStore) UpdateTradeLifecycle(lifecycleID string, update TradeLifecycleUpdate) (TradeLifecycle, error) {
	if s.db == nil {
		return TradeLifecycle{}, errors.New("sqlite database is not configured")
	}
	lifecycleID = strings.TrimSpace(lifecycleID)
	if lifecycleID == "" {
		return TradeLifecycle{}, errors.New("lifecycle_id is required")
	}
	if update.UpdatedAt.IsZero() {
		update.UpdatedAt = time.Now().UTC()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.findTradeLifecycleSQLiteLocked(lifecycleID)
	if err != nil {
		return TradeLifecycle{}, err
	}
	if strings.TrimSpace(update.Status) == "" {
		update.Status = current.Status
	}
	entryPrice := firstNonEmpty(update.EntryPrice, current.EntryPrice)
	entryQty := firstNonEmpty(update.EntryQty, current.EntryQty)
	exitPrice := firstNonEmpty(update.ExitPrice, current.ExitPrice)
	exitQty := firstNonEmpty(update.ExitQty, current.ExitQty)
	realizedPnl := firstNonEmpty(update.RealizedPnl, current.RealizedPnl)
	reentrySignalID := firstNonEmpty(update.ReentrySignalID, current.ReentrySignalID)
	cooldownUntil := firstNonEmpty(update.CooldownUntil, current.CooldownUntil)
	lastFillTime := update.LastFillTime
	if lastFillTime <= 0 {
		lastFillTime = current.LastFillTime
	}
	_, err = s.db.Exec(`UPDATE trade_lifecycles SET
		status = ?,
		entry_price = ?,
		entry_qty = ?,
		exit_price = ?,
		exit_qty = ?,
		realized_pnl = ?,
		reentry_signal_id = ?,
		cooldown_until = ?,
		last_fill_time = ?,
		updated_at = ?
		WHERE lifecycle_id = ?`,
		update.Status,
		entryPrice,
		entryQty,
		exitPrice,
		exitQty,
		realizedPnl,
		reentrySignalID,
		cooldownUntil,
		lastFillTime,
		update.UpdatedAt.UTC().Format(time.RFC3339Nano),
		lifecycleID,
	)
	if err != nil {
		return TradeLifecycle{}, err
	}
	return s.findTradeLifecycleSQLiteLocked(lifecycleID)
}

func (s *OrderStore) FindTradeLifecycle(lifecycleID string) (TradeLifecycle, bool, error) {
	if s.db == nil {
		return TradeLifecycle{}, false, errors.New("sqlite database is not configured")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	lifecycle, err := s.findTradeLifecycleSQLiteLocked(lifecycleID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return TradeLifecycle{}, false, nil
		}
		return TradeLifecycle{}, false, err
	}
	return lifecycle, true, nil
}

func (s *OrderStore) findTradeLifecycleSQLiteLocked(lifecycleID string) (TradeLifecycle, error) {
	row := s.db.QueryRow(`SELECT lifecycle_id, source_signal_id, root_signal_id, reentry_of_lifecycle_id, reentry_signal_id,
		exchange, api_id, symbol, action, status, entry_order_ids, entry_client_order_ids, tp_algo_ids, tp_client_algo_ids,
		tp_trigger_prices, sl_algo_ids, sl_client_algo_ids, sl_trigger_prices, entry_price, entry_qty, exit_price, exit_qty,
		realized_pnl, reentry_count, cooldown_until, last_fill_time, created_at, updated_at
		FROM trade_lifecycles WHERE lifecycle_id = ?`, strings.TrimSpace(lifecycleID))
	return scanTradeLifecycle(row.Scan)
}

func scanTradeLifecycle(scan func(dest ...any) error) (TradeLifecycle, error) {
	var l TradeLifecycle
	var entryOrderIDs, entryClientOrderIDs, tpAlgoIDs, tpClientAlgoIDs, tpTriggerPrices string
	var slAlgoIDs, slClientAlgoIDs, slTriggerPrices string
	var createdAt, updatedAt string
	if err := scan(
		&l.LifecycleID,
		&l.SourceSignalID,
		&l.RootSignalID,
		&l.ReentryOfLifecycleID,
		&l.ReentrySignalID,
		&l.Exchange,
		&l.APIID,
		&l.Symbol,
		&l.Action,
		&l.Status,
		&entryOrderIDs,
		&entryClientOrderIDs,
		&tpAlgoIDs,
		&tpClientAlgoIDs,
		&tpTriggerPrices,
		&slAlgoIDs,
		&slClientAlgoIDs,
		&slTriggerPrices,
		&l.EntryPrice,
		&l.EntryQty,
		&l.ExitPrice,
		&l.ExitQty,
		&l.RealizedPnl,
		&l.ReentryCount,
		&l.CooldownUntil,
		&l.LastFillTime,
		&createdAt,
		&updatedAt,
	); err != nil {
		return TradeLifecycle{}, err
	}
	l.EntryOrderIDs = splitMonitorList(entryOrderIDs)
	l.EntryClientOrderIDs = splitMonitorList(entryClientOrderIDs)
	l.TPAlgoIDs = splitMonitorList(tpAlgoIDs)
	l.TPClientAlgoIDs = splitMonitorList(tpClientAlgoIDs)
	l.TPTriggerPrices = splitMonitorList(tpTriggerPrices)
	l.SLAlgoIDs = splitMonitorList(slAlgoIDs)
	l.SLClientAlgoIDs = splitMonitorList(slClientAlgoIDs)
	l.SLTriggerPrices = splitMonitorList(slTriggerPrices)
	l.CreatedAt = parseStoreTime(createdAt)
	l.UpdatedAt = parseStoreTime(updatedAt)
	return l, nil
}

func (s *OrderStore) ListTradeLifecycles(filter TradeMonitorFilter, limit int) ([]TradeLifecycle, error) {
	if s.db == nil {
		return nil, errors.New("sqlite database is not configured")
	}
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	filter.normalize()
	where := []string{"1=1"}
	args := []any{}
	if filter.Exchange != "" {
		where = append(where, "exchange = ?")
		args = append(args, filter.Exchange)
	}
	if filter.APIID != "" {
		where = append(where, "api_id = ?")
		args = append(args, filter.APIID)
	}
	if filter.Symbol != "" {
		where = append(where, "symbol = ?")
		args = append(args, filter.Symbol)
	}
	if filter.Status != "" {
		where = append(where, "status = ?")
		args = append(args, filter.Status)
	}
	args = append(args, limit)
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT lifecycle_id, source_signal_id, root_signal_id, reentry_of_lifecycle_id, reentry_signal_id,
		exchange, api_id, symbol, action, status, entry_order_ids, entry_client_order_ids, tp_algo_ids, tp_client_algo_ids,
		tp_trigger_prices, sl_algo_ids, sl_client_algo_ids, sl_trigger_prices, entry_price, entry_qty, exit_price, exit_qty,
		realized_pnl, reentry_count, cooldown_until, last_fill_time, created_at, updated_at
		FROM trade_lifecycles WHERE `+strings.Join(where, " AND ")+` ORDER BY updated_at DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TradeLifecycle{}
	for rows.Next() {
		lifecycle, err := scanTradeLifecycle(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, lifecycle)
	}
	return out, rows.Err()
}

func (f *TradeMonitorFilter) normalize() {
	if strings.TrimSpace(f.Exchange) != "" {
		f.Exchange = trading.NormalizeExchange(f.Exchange)
	}
	f.APIID = strings.TrimSpace(f.APIID)
	f.Symbol = strings.ToUpper(strings.TrimSpace(f.Symbol))
	f.Status = strings.ToLower(strings.TrimSpace(f.Status))
}

func (s *OrderStore) TradeMonitorCheckpoint(exchange, apiID, symbol string) (TradeMonitorCheckpoint, bool, error) {
	if s.db == nil {
		return TradeMonitorCheckpoint{}, false, errors.New("sqlite database is not configured")
	}
	exchange = trading.NormalizeExchange(exchange)
	apiID = strings.TrimSpace(apiID)
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	s.mu.Lock()
	defer s.mu.Unlock()
	row := s.db.QueryRow(`SELECT exchange, api_id, symbol, last_fill_time, last_trade_id, last_polled_at, last_error, updated_at
		FROM trade_monitor_checkpoints WHERE exchange = ? AND api_id = ? AND symbol = ?`, exchange, apiID, symbol)
	var cp TradeMonitorCheckpoint
	var lastPolledAt, updatedAt string
	if err := row.Scan(&cp.Exchange, &cp.APIID, &cp.Symbol, &cp.LastFillTime, &cp.LastTradeID, &lastPolledAt, &cp.LastError, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return TradeMonitorCheckpoint{}, false, nil
		}
		return TradeMonitorCheckpoint{}, false, err
	}
	cp.LastPolledAt = parseStoreTime(lastPolledAt)
	cp.UpdatedAt = parseStoreTime(updatedAt)
	return cp, true, nil
}

func (s *OrderStore) UpsertTradeMonitorCheckpoint(cp TradeMonitorCheckpoint) error {
	if s.db == nil {
		return errors.New("sqlite database is not configured")
	}
	cp.Exchange = trading.NormalizeExchange(cp.Exchange)
	cp.APIID = strings.TrimSpace(cp.APIID)
	cp.Symbol = strings.ToUpper(strings.TrimSpace(cp.Symbol))
	if cp.LastPolledAt.IsZero() {
		cp.LastPolledAt = time.Now().UTC()
	}
	if cp.UpdatedAt.IsZero() {
		cp.UpdatedAt = cp.LastPolledAt
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`INSERT INTO trade_monitor_checkpoints
		(exchange, api_id, symbol, last_fill_time, last_trade_id, last_polled_at, last_error, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(exchange, api_id, symbol) DO UPDATE SET
			last_fill_time=excluded.last_fill_time,
			last_trade_id=excluded.last_trade_id,
			last_polled_at=excluded.last_polled_at,
			last_error=excluded.last_error,
			updated_at=excluded.updated_at`,
		cp.Exchange,
		cp.APIID,
		cp.Symbol,
		cp.LastFillTime,
		cp.LastTradeID,
		cp.LastPolledAt.UTC().Format(time.RFC3339Nano),
		cp.LastError,
		cp.UpdatedAt.UTC().Format(time.RFC3339Nano),
	)
	return err
}

func (s *OrderStore) ListTradeMonitorCheckpoints(filter TradeMonitorFilter, limit int) ([]TradeMonitorCheckpoint, error) {
	if s.db == nil {
		return nil, errors.New("sqlite database is not configured")
	}
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	filter.normalize()
	where := []string{"1=1"}
	args := []any{}
	if filter.Exchange != "" {
		where = append(where, "exchange = ?")
		args = append(args, filter.Exchange)
	}
	if filter.APIID != "" {
		where = append(where, "api_id = ?")
		args = append(args, filter.APIID)
	}
	if filter.Symbol != "" {
		where = append(where, "symbol = ?")
		args = append(args, filter.Symbol)
	}
	args = append(args, limit)
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT exchange, api_id, symbol, last_fill_time, last_trade_id, last_polled_at, last_error, updated_at
		FROM trade_monitor_checkpoints WHERE `+strings.Join(where, " AND ")+` ORDER BY updated_at DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TradeMonitorCheckpoint{}
	for rows.Next() {
		var cp TradeMonitorCheckpoint
		var lastPolledAt, updatedAt string
		if err := rows.Scan(&cp.Exchange, &cp.APIID, &cp.Symbol, &cp.LastFillTime, &cp.LastTradeID, &lastPolledAt, &cp.LastError, &updatedAt); err != nil {
			return nil, err
		}
		cp.LastPolledAt = parseStoreTime(lastPolledAt)
		cp.UpdatedAt = parseStoreTime(updatedAt)
		out = append(out, cp)
	}
	return out, rows.Err()
}

func (s *OrderStore) InsertTradeMonitorEvent(event TradeMonitorEvent) error {
	if s.db == nil {
		return errors.New("sqlite database is not configured")
	}
	event.normalize()
	s.mu.Lock()
	defer s.mu.Unlock()
	eventTime := event.EventTime.UTC().Format(time.RFC3339Nano)
	_, err := s.db.Exec(`INSERT INTO trade_monitor_events
		(event_id, event_time, exchange, api_id, symbol, lifecycle_id, source_signal_id, event_type, status, message, raw_json)
		SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
		WHERE julianday(?) >= julianday('now', '-30 days')`,
		event.EventID,
		eventTime,
		event.Exchange,
		event.APIID,
		event.Symbol,
		event.LifecycleID,
		event.SourceSignalID,
		event.EventType,
		event.Status,
		event.Message,
		event.RawJSON,
		eventTime,
	)
	return err
}

func (event *TradeMonitorEvent) normalize() {
	event.Exchange = trading.NormalizeExchange(event.Exchange)
	event.APIID = strings.TrimSpace(event.APIID)
	event.Symbol = strings.ToUpper(strings.TrimSpace(event.Symbol))
	event.LifecycleID = strings.TrimSpace(event.LifecycleID)
	event.SourceSignalID = strings.TrimSpace(event.SourceSignalID)
	event.EventType = strings.ToLower(strings.TrimSpace(event.EventType))
	event.Status = strings.ToLower(strings.TrimSpace(event.Status))
	event.Message = strings.TrimSpace(event.Message)
	event.RawJSON = strings.TrimSpace(event.RawJSON)
	if event.EventTime.IsZero() {
		event.EventTime = time.Now().UTC()
	}
	if strings.TrimSpace(event.EventID) == "" {
		payload := strings.Join([]string{
			event.EventTime.UTC().Format(time.RFC3339Nano),
			event.Exchange,
			event.APIID,
			event.Symbol,
			event.LifecycleID,
			event.EventType,
			event.Status,
			event.Message,
		}, "|")
		sum := sha256.Sum256([]byte(payload))
		event.EventID = event.EventTime.UTC().Format("20060102T150405.000") + "-" + hex.EncodeToString(sum[:6])
	}
}

func (s *OrderStore) ListTradeMonitorEvents(filter TradeMonitorFilter, limit int) ([]TradeMonitorEvent, error) {
	if s.db == nil {
		return nil, errors.New("sqlite database is not configured")
	}
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	filter.normalize()
	where := []string{"1=1"}
	args := []any{}
	if filter.Exchange != "" {
		where = append(where, "exchange = ?")
		args = append(args, filter.Exchange)
	}
	if filter.APIID != "" {
		where = append(where, "api_id = ?")
		args = append(args, filter.APIID)
	}
	if filter.Symbol != "" {
		where = append(where, "symbol = ?")
		args = append(args, filter.Symbol)
	}
	if filter.Status != "" {
		where = append(where, "status = ?")
		args = append(args, filter.Status)
	}
	args = append(args, limit)
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT event_id, event_time, exchange, api_id, symbol, lifecycle_id, source_signal_id, event_type, status, message, raw_json
		FROM trade_monitor_events WHERE `+strings.Join(where, " AND ")+` ORDER BY event_time DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TradeMonitorEvent{}
	for rows.Next() {
		var event TradeMonitorEvent
		var eventTime string
		if err := rows.Scan(&event.EventID, &eventTime, &event.Exchange, &event.APIID, &event.Symbol, &event.LifecycleID, &event.SourceSignalID, &event.EventType, &event.Status, &event.Message, &event.RawJSON); err != nil {
			return nil, err
		}
		event.EventTime = parseStoreTime(eventTime)
		out = append(out, event)
	}
	return out, rows.Err()
}

func TradeMonitorEventRawJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

func parseStoreTime(raw string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, strings.TrimSpace(raw))
	return t.UTC()
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func ParseMonitorFloat(raw string) (float64, bool) {
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	return value, err == nil
}

func MonitorListContains(values []string, value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	for _, existing := range values {
		if strings.EqualFold(strings.TrimSpace(existing), value) {
			return true
		}
	}
	return false
}

func MonitorLifecycleSummary(l TradeLifecycle) string {
	return fmt.Sprintf("%s %s %s %s", l.Exchange, l.APIID, l.Symbol, l.Status)
}
