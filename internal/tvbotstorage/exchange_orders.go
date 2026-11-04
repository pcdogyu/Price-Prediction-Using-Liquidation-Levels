package storage

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ExchangeOrder is a read-only order snapshot imported from an exchange. It is
// intentionally separate from OrderRecord: OrderRecord represents a local
// webhook signal and can be retried, while ExchangeOrder represents an order
// that already exists on the remote account.
type ExchangeOrder struct {
	OrderKey      string    `json:"order_key"`
	Exchange      string    `json:"exchange"`
	APIID         string    `json:"api_id"`
	TradeEnv      string    `json:"trade_env"`
	RemoteOrderID string    `json:"remote_order_id"`
	ClientOrderID string    `json:"client_order_id,omitempty"`
	InstID        string    `json:"inst_id,omitempty"`
	Status        string    `json:"status,omitempty"`
	FinishAs      string    `json:"finish_as,omitempty"`
	Side          string    `json:"side,omitempty"`
	PositionSide  string    `json:"position_side,omitempty"`
	OrderType     string    `json:"order_type,omitempty"`
	TimeInForce   string    `json:"time_in_force,omitempty"`
	Price         string    `json:"price,omitempty"`
	AveragePrice  string    `json:"average_price,omitempty"`
	OriginalQty   string    `json:"original_qty,omitempty"`
	ExecutedQty   string    `json:"executed_qty,omitempty"`
	RemainingQty  string    `json:"remaining_qty,omitempty"`
	QuoteQty      string    `json:"quote_qty,omitempty"`
	ReduceOnly    bool      `json:"reduce_only"`
	ClosePosition bool      `json:"close_position"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at,omitempty"`
	FinishedAt    time.Time `json:"finished_at,omitempty"`
	RawJSON       string    `json:"raw_json,omitempty"`
	FetchedAt     time.Time `json:"fetched_at"`
}

type ExchangeOrderFilter struct {
	Exchange string
	APIID    string
	TradeEnv string
	Status   string
	InstID   string
	Query    string
	From     time.Time
	To       time.Time
	Limit    int
	Offset   int
}

type ExchangeOrderPage struct {
	Orders []ExchangeOrder
	Total  int
}

type ExchangeOrderUpsertStats struct {
	Received int `json:"received"`
	Upserted int `json:"upserted"`
}

type ExchangeOrderSyncState struct {
	Exchange        string    `json:"exchange"`
	APIID           string    `json:"api_id"`
	TradeEnv        string    `json:"trade_env"`
	CursorJSON      string    `json:"-"`
	Status          string    `json:"status"`
	LastAttemptedAt time.Time `json:"last_attempted_at,omitempty"`
	LastSuccessAt   time.Time `json:"last_success_at,omitempty"`
	LastError       string    `json:"last_error,omitempty"`
	LastReceived    int       `json:"last_received"`
	LastUpserted    int       `json:"last_upserted"`
	UpdatedAt       time.Time `json:"updated_at,omitempty"`
}

func ExchangeOrderKey(exchange, apiID, tradeEnv, remoteOrderID string) string {
	return strings.Join([]string{
		strings.ToLower(strings.TrimSpace(exchange)),
		strings.TrimSpace(apiID),
		strings.ToLower(strings.TrimSpace(tradeEnv)),
		strings.TrimSpace(remoteOrderID),
	}, "|")
}

func (s *OrderStore) UpsertExchangeOrders(orders []ExchangeOrder, fetchedAt time.Time) (ExchangeOrderUpsertStats, error) {
	if s.db == nil {
		return ExchangeOrderUpsertStats{}, errors.New("sqlite database is not configured")
	}
	if fetchedAt.IsZero() {
		fetchedAt = time.Now().UTC()
	}
	fetchedAt = fetchedAt.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return ExchangeOrderUpsertStats{}, err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT INTO exchange_orders (
		order_key, exchange, api_id, trade_env, remote_order_id, client_order_id, inst_id, status, finish_as,
		side, position_side, order_type, time_in_force, price, average_price, original_qty, executed_qty,
		remaining_qty, quote_qty, reduce_only, close_position, created_at, updated_at, finished_at, raw_json, fetched_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(exchange, api_id, trade_env, remote_order_id) DO UPDATE SET
		order_key=excluded.order_key,
		client_order_id=excluded.client_order_id,
		inst_id=excluded.inst_id,
		status=excluded.status,
		finish_as=excluded.finish_as,
		side=excluded.side,
		position_side=excluded.position_side,
		order_type=excluded.order_type,
		time_in_force=excluded.time_in_force,
		price=excluded.price,
		average_price=excluded.average_price,
		original_qty=excluded.original_qty,
		executed_qty=excluded.executed_qty,
		remaining_qty=excluded.remaining_qty,
		quote_qty=excluded.quote_qty,
		reduce_only=excluded.reduce_only,
		close_position=excluded.close_position,
		created_at=excluded.created_at,
		updated_at=excluded.updated_at,
		finished_at=excluded.finished_at,
		raw_json=excluded.raw_json,
		fetched_at=excluded.fetched_at`)
	if err != nil {
		return ExchangeOrderUpsertStats{}, err
	}
	defer stmt.Close()
	stats := ExchangeOrderUpsertStats{Received: len(orders)}
	for _, order := range orders {
		exchange := strings.ToLower(strings.TrimSpace(order.Exchange))
		apiID := strings.TrimSpace(order.APIID)
		env := strings.ToLower(strings.TrimSpace(order.TradeEnv))
		remoteID := strings.TrimSpace(order.RemoteOrderID)
		if exchange == "" || apiID == "" || env == "" || remoteID == "" {
			continue
		}
		order.Exchange = exchange
		order.APIID = apiID
		order.TradeEnv = env
		order.RemoteOrderID = remoteID
		if order.OrderKey == "" {
			order.OrderKey = ExchangeOrderKey(exchange, apiID, env, remoteID)
		}
		if order.CreatedAt.IsZero() {
			order.CreatedAt = fetchedAt
		}
		order.CreatedAt = order.CreatedAt.UTC()
		order.UpdatedAt = order.UpdatedAt.UTC()
		order.FinishedAt = order.FinishedAt.UTC()
		if _, err := stmt.Exec(
			order.OrderKey, order.Exchange, order.APIID, order.TradeEnv, order.RemoteOrderID, order.ClientOrderID,
			order.InstID, order.Status, order.FinishAs, order.Side, order.PositionSide, order.OrderType,
			order.TimeInForce, order.Price, order.AveragePrice, order.OriginalQty, order.ExecutedQty,
			order.RemainingQty, order.QuoteQty, boolToSQLite(order.ReduceOnly), boolToSQLite(order.ClosePosition),
			formatOptionalStoreTime(order.CreatedAt), formatOptionalStoreTime(order.UpdatedAt), formatOptionalStoreTime(order.FinishedAt),
			order.RawJSON, fetchedAt.Format(time.RFC3339Nano),
		); err != nil {
			return ExchangeOrderUpsertStats{}, err
		}
		stats.Upserted++
	}
	if err := tx.Commit(); err != nil {
		return ExchangeOrderUpsertStats{}, err
	}
	return stats, nil
}

func (s *OrderStore) ListExchangeOrders(filter ExchangeOrderFilter) (ExchangeOrderPage, error) {
	if s.db == nil {
		return ExchangeOrderPage{}, errors.New("sqlite database is not configured")
	}
	filter.Exchange = strings.ToLower(strings.TrimSpace(filter.Exchange))
	filter.APIID = strings.TrimSpace(filter.APIID)
	filter.TradeEnv = strings.ToLower(strings.TrimSpace(filter.TradeEnv))
	filter.Status = strings.ToLower(strings.TrimSpace(filter.Status))
	filter.InstID = strings.TrimSpace(filter.InstID)
	filter.Query = strings.TrimSpace(filter.Query)
	if filter.Limit <= 0 || filter.Limit > 200 {
		filter.Limit = 50
	}
	if filter.Offset < 0 {
		filter.Offset = 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	where, args := exchangeOrderWhere(filter)
	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM exchange_orders WHERE `+where, args...).Scan(&total); err != nil {
		return ExchangeOrderPage{}, err
	}
	rows, err := s.db.Query(`SELECT order_key, exchange, api_id, trade_env, remote_order_id, client_order_id,
		inst_id, status, finish_as, side, position_side, order_type, time_in_force, price, average_price,
		original_qty, executed_qty, remaining_qty, quote_qty, reduce_only, close_position, created_at,
		updated_at, finished_at, raw_json, fetched_at
		FROM exchange_orders WHERE `+where+` ORDER BY created_at DESC, order_key DESC LIMIT ? OFFSET ?`, append(args, filter.Limit, filter.Offset)...)
	if err != nil {
		return ExchangeOrderPage{}, err
	}
	defer rows.Close()
	orders := make([]ExchangeOrder, 0, filter.Limit)
	for rows.Next() {
		order, err := scanExchangeOrder(rows)
		if err != nil {
			return ExchangeOrderPage{}, err
		}
		orders = append(orders, order)
	}
	if err := rows.Err(); err != nil {
		return ExchangeOrderPage{}, err
	}
	return ExchangeOrderPage{Orders: orders, Total: total}, nil
}

// ListExchangeOrderInstIDs returns the distinct exchange instruments already
// present for one account scope. Syncers use this to keep incremental history
// refreshes bounded to symbols that have actually appeared in the account.
func (s *OrderStore) ListExchangeOrderInstIDs(exchange, apiID, tradeEnv string) ([]string, error) {
	if s.db == nil {
		return nil, errors.New("sqlite database is not configured")
	}
	exchange = strings.ToLower(strings.TrimSpace(exchange))
	apiID = strings.TrimSpace(apiID)
	tradeEnv = strings.ToLower(strings.TrimSpace(tradeEnv))
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT DISTINCT inst_id FROM exchange_orders
		WHERE exchange = ? AND api_id = ? AND trade_env = ? AND TRIM(COALESCE(inst_id, '')) <> ''
		ORDER BY LOWER(inst_id)`, exchange, apiID, tradeEnv)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var instID string
		if err := rows.Scan(&instID); err != nil {
			return nil, err
		}
		out = append(out, strings.TrimSpace(instID))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *OrderStore) GetExchangeOrderSyncState(exchange, apiID, tradeEnv string) (ExchangeOrderSyncState, bool, error) {
	if s.db == nil {
		return ExchangeOrderSyncState{}, false, errors.New("sqlite database is not configured")
	}
	exchange = strings.ToLower(strings.TrimSpace(exchange))
	apiID = strings.TrimSpace(apiID)
	tradeEnv = strings.ToLower(strings.TrimSpace(tradeEnv))
	s.mu.Lock()
	defer s.mu.Unlock()
	row := s.db.QueryRow(`SELECT exchange, api_id, trade_env, cursor_json, status, last_attempted_at, last_success_at,
		last_error, last_received, last_upserted, updated_at FROM exchange_order_sync_state
		WHERE exchange = ? AND api_id = ? AND trade_env = ?`, exchange, apiID, tradeEnv)
	state, err := scanExchangeOrderSyncState(row)
	if errors.Is(err, sql.ErrNoRows) {
		return ExchangeOrderSyncState{}, false, nil
	}
	if err != nil {
		return ExchangeOrderSyncState{}, false, err
	}
	return state, true, nil
}

func (s *OrderStore) UpsertExchangeOrderSyncState(state ExchangeOrderSyncState) error {
	if s.db == nil {
		return errors.New("sqlite database is not configured")
	}
	state.Exchange = strings.ToLower(strings.TrimSpace(state.Exchange))
	state.APIID = strings.TrimSpace(state.APIID)
	state.TradeEnv = strings.ToLower(strings.TrimSpace(state.TradeEnv))
	if state.Status == "" {
		state.Status = "idle"
	}
	if state.UpdatedAt.IsZero() {
		state.UpdatedAt = time.Now().UTC()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`INSERT INTO exchange_order_sync_state (
		exchange, api_id, trade_env, cursor_json, status, last_attempted_at, last_success_at, last_error,
		last_received, last_upserted, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(exchange, api_id, trade_env) DO UPDATE SET
		cursor_json=excluded.cursor_json,
		status=excluded.status,
		last_attempted_at=excluded.last_attempted_at,
		last_success_at=excluded.last_success_at,
		last_error=excluded.last_error,
		last_received=excluded.last_received,
		last_upserted=excluded.last_upserted,
		updated_at=excluded.updated_at`,
		state.Exchange, state.APIID, state.TradeEnv, state.CursorJSON, state.Status,
		formatOptionalStoreTime(state.LastAttemptedAt), formatOptionalStoreTime(state.LastSuccessAt), state.LastError,
		state.LastReceived, state.LastUpserted, state.UpdatedAt.UTC().Format(time.RFC3339Nano),
	)
	return err
}

func exchangeOrderWhere(filter ExchangeOrderFilter) (string, []any) {
	where := []string{"1 = 1"}
	args := []any{}
	if filter.Exchange != "" {
		where = append(where, "exchange = ?")
		args = append(args, filter.Exchange)
	}
	if filter.APIID != "" {
		where = append(where, "api_id = ?")
		args = append(args, filter.APIID)
	}
	if filter.TradeEnv != "" {
		where = append(where, "trade_env = ?")
		args = append(args, filter.TradeEnv)
	}
	if filter.Status != "" {
		where = append(where, "LOWER(status) = ?")
		args = append(args, filter.Status)
	}
	if filter.InstID != "" {
		where = append(where, "LOWER(inst_id) = LOWER(?)")
		args = append(args, filter.InstID)
	}
	if !filter.From.IsZero() {
		where = append(where, "created_at >= ?")
		args = append(args, filter.From.UTC().Format(time.RFC3339Nano))
	}
	if !filter.To.IsZero() {
		where = append(where, "created_at <= ?")
		args = append(args, filter.To.UTC().Format(time.RFC3339Nano))
	}
	if filter.Query != "" {
		pattern := "%" + strings.ToLower(filter.Query) + "%"
		where = append(where, `(LOWER(remote_order_id) LIKE ? OR LOWER(client_order_id) LIKE ? OR LOWER(inst_id) LIKE ? OR LOWER(status) LIKE ? OR LOWER(side) LIKE ? OR LOWER(position_side) LIKE ?)`)
		args = append(args, pattern, pattern, pattern, pattern, pattern, pattern)
	}
	return strings.Join(where, " AND "), args
}

func scanExchangeOrder(scanner orderScanner) (ExchangeOrder, error) {
	var order ExchangeOrder
	var createdAt, updatedAt, finishedAt, fetchedAt string
	var reduceOnly, closePosition int
	if err := scanner.Scan(
		&order.OrderKey, &order.Exchange, &order.APIID, &order.TradeEnv, &order.RemoteOrderID, &order.ClientOrderID,
		&order.InstID, &order.Status, &order.FinishAs, &order.Side, &order.PositionSide, &order.OrderType,
		&order.TimeInForce, &order.Price, &order.AveragePrice, &order.OriginalQty, &order.ExecutedQty,
		&order.RemainingQty, &order.QuoteQty, &reduceOnly, &closePosition, &createdAt, &updatedAt,
		&finishedAt, &order.RawJSON, &fetchedAt,
	); err != nil {
		return ExchangeOrder{}, err
	}
	order.ReduceOnly = reduceOnly != 0
	order.ClosePosition = closePosition != 0
	order.CreatedAt = parseStoreTime(createdAt)
	var err error
	if order.UpdatedAt, err = parseOptionalStoreTime(updatedAt); err != nil {
		return ExchangeOrder{}, fmt.Errorf("parse exchange order updated_at: %w", err)
	}
	if order.FinishedAt, err = parseOptionalStoreTime(finishedAt); err != nil {
		return ExchangeOrder{}, fmt.Errorf("parse exchange order finished_at: %w", err)
	}
	order.FetchedAt = parseStoreTime(fetchedAt)
	order.Exchange = strings.ToLower(strings.TrimSpace(order.Exchange))
	order.TradeEnv = strings.ToLower(strings.TrimSpace(order.TradeEnv))
	return order, nil
}

func scanExchangeOrderSyncState(scanner orderScanner) (ExchangeOrderSyncState, error) {
	var state ExchangeOrderSyncState
	var attemptedAt, successAt, updatedAt string
	if err := scanner.Scan(&state.Exchange, &state.APIID, &state.TradeEnv, &state.CursorJSON, &state.Status,
		&attemptedAt, &successAt, &state.LastError, &state.LastReceived, &state.LastUpserted, &updatedAt); err != nil {
		return ExchangeOrderSyncState{}, err
	}
	var err error
	if state.LastAttemptedAt, err = parseOptionalStoreTime(attemptedAt); err != nil {
		return ExchangeOrderSyncState{}, err
	}
	if state.LastSuccessAt, err = parseOptionalStoreTime(successAt); err != nil {
		return ExchangeOrderSyncState{}, err
	}
	state.UpdatedAt = parseStoreTime(updatedAt)
	return state, nil
}

func boolToSQLite(value bool) int {
	if value {
		return 1
	}
	return 0
}

func formatOptionalStoreTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func parseOptionalStoreTime(value string) (time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return time.Time{}, nil
	}
	return parseStoreTime(value), nil
}
