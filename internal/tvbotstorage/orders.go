package storage

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
	_ "modernc.org/sqlite"
)

type OrderStatus string

const (
	StatusAccepted  OrderStatus = "accepted"
	StatusDuplicate OrderStatus = "duplicate"
	StatusSubmitted OrderStatus = "submitted"
	StatusPartial   OrderStatus = "partial"
	StatusFailed    OrderStatus = "failed"
	StatusRejected  OrderStatus = "rejected"
	StatusIgnored   OrderStatus = "ignored"
)

type OrderRecord struct {
	SignalID       string              `json:"signal_id"`
	DedupeKey      string              `json:"dedupe_key"`
	Status         OrderStatus         `json:"status"`
	Action         trading.Side        `json:"action"`
	SourceAction   trading.Side        `json:"source_action,omitempty"`
	APIID          string              `json:"api_id,omitempty"`
	SourceExchange string              `json:"source_exchange,omitempty"`
	TargetExchange string              `json:"target_exchange,omitempty"`
	TradeEnv       string              `json:"trade_env,omitempty"`
	Coinpair       string              `json:"coinpair"`
	Ticker         string              `json:"ticker"`
	Price          string              `json:"price"`
	Leverage       int                 `json:"leverage"`
	Amount         string              `json:"amount"`
	ADX            *float64            `json:"adx,omitempty"`
	MarketStrategy string              `json:"market_strategy,omitempty"`
	Risk           trading.Risk        `json:"risk,omitempty"`
	OrderIntent    string              `json:"order_intent,omitempty"`
	PositionEffect string              `json:"position_effect,omitempty"`
	PositionSide   string              `json:"position_side,omitempty"`
	TokenHash      string              `json:"token_hash"`
	AcceptedAt     time.Time           `json:"accepted_at"`
	UpdatedAt      time.Time           `json:"updated_at"`
	Result         trading.OrderResult `json:"result,omitempty"`
	ErrorCode      string              `json:"error_code,omitempty"`
	Error          string              `json:"error,omitempty"`
	RawJSON        string              `json:"raw_json,omitempty"`
}

type OrderStore struct {
	mu     sync.Mutex
	path   string
	state  orderState
	db     *sql.DB
	ownsDB bool
}

type orderState struct {
	Dedupe              map[string]string `json:"dedupe"`
	Orders              []OrderRecord     `json:"orders"`
	CoinpairBlocks      []CoinpairBlock   `json:"coinpair_blocks,omitempty"`
	CoinpairBlockEvents map[string]bool   `json:"coinpair_block_events,omitempty"`
}

func NewOrderStore(path string) (*OrderStore, error) {
	s := &OrderStore{
		path: path,
		state: orderState{
			Dedupe:              map[string]string{},
			Orders:              []OrderRecord{},
			CoinpairBlocks:      []CoinpairBlock{},
			CoinpairBlockEvents: map[string]bool{},
		},
	}
	if path == "" {
		return s, nil
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func NewSQLiteOrderStore(databasePath, legacyJSONPath string) (*OrderStore, error) {
	if databasePath == "" {
		return nil, errors.New("database path is required")
	}
	if err := os.MkdirAll(filepath.Dir(databasePath), 0o755); err != nil && filepath.Dir(databasePath) != "." {
		return nil, err
	}
	db, err := sql.Open("sqlite", databasePath+"?_pragma=busy_timeout(60000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s, err := newSQLiteOrderStore(db, true, legacyJSONPath)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// NewSQLiteOrderStoreWithDB creates an order store backed by an existing
// database/sql handle. The caller owns the handle and remains responsible for
// closing it. This is required when multiple subsystems share one SQLite file.
func NewSQLiteOrderStoreWithDB(db *sql.DB, legacyJSONPath string) (*OrderStore, error) {
	if db == nil {
		return nil, errors.New("database handle is required")
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	return newSQLiteOrderStore(db, false, legacyJSONPath)
}

func newSQLiteOrderStore(db *sql.DB, ownsDB bool, legacyJSONPath string) (*OrderStore, error) {
	s := &OrderStore{
		path: legacyJSONPath,
		state: orderState{
			Dedupe:              map[string]string{},
			Orders:              []OrderRecord{},
			CoinpairBlocks:      []CoinpairBlock{},
			CoinpairBlockEvents: map[string]bool{},
		},
		db:     db,
		ownsDB: ownsDB,
	}
	if err := s.migrateSQLite(); err != nil {
		return nil, err
	}
	if err := s.importLegacyJSON(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *OrderStore) RecordAccepted(signal trading.Signal, dedupeKey string, now time.Time) (OrderRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		return s.recordAcceptedSQLiteLocked(signal, dedupeKey, now)
	}
	if id, ok := s.state.Dedupe[dedupeKey]; ok {
		if _, found := s.findLocked(id); found {
			rec := newOrderRecord(signal, dedupeKey, StatusDuplicate, now)
			rec.SignalID = s.newSignalIDLocked(now, dedupeKey+"|duplicate")
			rec.ErrorCode = "duplicate"
			rec.Error = "duplicate signal ignored"
			s.state.Orders = append(s.state.Orders, rec)
			if err := s.saveLocked(); err != nil {
				return OrderRecord{}, false, err
			}
			return rec, true, nil
		}
	}
	rec := newOrderRecord(signal, dedupeKey, StatusAccepted, now)
	rec.SignalID = s.newSignalIDLocked(now, dedupeKey)
	s.state.Dedupe[dedupeKey] = rec.SignalID
	s.state.Orders = append(s.state.Orders, rec)
	if err := s.saveLocked(); err != nil {
		return OrderRecord{}, false, err
	}
	return rec, false, nil
}

func (s *OrderStore) RecordRejected(signal trading.Signal, code string, err error, now time.Time) (OrderRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		return s.recordRejectedSQLiteLocked(signal, code, err, now)
	}
	message := ""
	if err != nil {
		message = err.Error()
	}
	dedupeKey := RejectedKey(signal, code, message, now)
	rec := newOrderRecord(signal, dedupeKey, StatusRejected, now)
	rec.SignalID = s.newSignalIDLocked(now, dedupeKey)
	rec.ErrorCode = code
	rec.Error = message
	s.state.Orders = append(s.state.Orders, rec)
	if err := s.saveLocked(); err != nil {
		return OrderRecord{}, err
	}
	return rec, nil
}

func (s *OrderStore) RecordIgnored(signal trading.Signal, filter string, now time.Time) (OrderRecord, error) {
	filter = strings.ToUpper(strings.TrimSpace(filter))
	return s.RecordIgnoredReason(signal, "coinpair_filtered", fmt.Sprintf("coinpair matched ignore filter %q", filter), now)
}

func (s *OrderStore) RecordIgnoredReason(signal trading.Signal, code, message string, now time.Time) (OrderRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	code = strings.ToLower(strings.TrimSpace(code))
	message = strings.TrimSpace(message)
	if code == "" {
		code = "coinpair_filtered"
	}
	if s.db != nil {
		return s.recordIgnoredSQLiteLocked(signal, code, message, now)
	}
	dedupeKey := RejectedKey(signal, code, message, now)
	rec := newOrderRecord(signal, dedupeKey, StatusIgnored, now)
	rec.SignalID = s.newSignalIDLocked(now, dedupeKey)
	rec.ErrorCode = code
	rec.Error = message
	s.state.Orders = append(s.state.Orders, rec)
	if err := s.saveLocked(); err != nil {
		return OrderRecord{}, err
	}
	return rec, nil
}

func (s *OrderStore) MarkSubmitted(signalID string, result trading.OrderResult, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		return s.markSubmittedSQLiteLocked(signalID, result, now)
	}
	for i := range s.state.Orders {
		if s.state.Orders[i].SignalID == signalID {
			s.state.Orders[i].Status = StatusSubmitted
			s.state.Orders[i].UpdatedAt = now.UTC()
			s.state.Orders[i].Result = result
			s.state.Orders[i].Error = ""
			return s.saveLocked()
		}
	}
	return errors.New("signal record not found")
}

func (s *OrderStore) MarkPartiallySubmitted(signalID string, result trading.OrderResult, submitErr error, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		return s.markPartiallySubmittedSQLiteLocked(signalID, result, submitErr, now)
	}
	for i := range s.state.Orders {
		if s.state.Orders[i].SignalID == signalID {
			s.state.Orders[i].Status = StatusPartial
			s.state.Orders[i].UpdatedAt = now.UTC()
			s.state.Orders[i].Result = result
			s.state.Orders[i].ErrorCode = "partial_submission"
			if submitErr != nil {
				s.state.Orders[i].Error = submitErr.Error()
			}
			return s.saveLocked()
		}
	}
	return errors.New("signal record not found")
}

func (s *OrderStore) MarkFailed(signalID string, err error, now time.Time) error {
	return s.MarkFailedCode(signalID, "", err, now)
}

func (s *OrderStore) MarkFailedCode(signalID, code string, err error, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		return s.markFailedSQLiteLocked(signalID, code, err, now)
	}
	for i := range s.state.Orders {
		if s.state.Orders[i].SignalID == signalID {
			s.state.Orders[i].Status = StatusFailed
			s.state.Orders[i].UpdatedAt = now.UTC()
			s.state.Orders[i].ErrorCode = strings.TrimSpace(code)
			if err != nil {
				s.state.Orders[i].Error = err.Error()
			}
			return s.saveLocked()
		}
	}
	return errors.New("signal record not found")
}

func (s *OrderStore) List(limit int) []OrderRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		records, err := s.listSQLiteLocked(limit)
		if err == nil {
			return records
		}
		return nil
	}
	if limit <= 0 || limit > len(s.state.Orders) {
		limit = len(s.state.Orders)
	}
	out := make([]OrderRecord, 0, limit)
	for i := len(s.state.Orders) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, s.state.Orders[i])
	}
	return out
}

func (s *OrderStore) ListPage(limit, offset int) []OrderRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		records, err := s.listSQLitePageLocked(limit, offset)
		if err == nil {
			return records
		}
		return nil
	}
	return s.listPageMemoryLocked(limit, offset, "")
}

func (s *OrderStore) ListSearchPage(query string, limit, offset int) []OrderRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		records, err := s.listSQLiteSearchPageLocked("", query, limit, offset)
		if err == nil {
			return records
		}
		return nil
	}
	return s.listSearchPageMemoryLocked(limit, offset, "", query)
}

func (s *OrderStore) ListByTargetExchange(exchange string, limit int) []OrderRecord {
	exchange = trading.NormalizeExchange(exchange)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		records, err := s.listSQLiteByTargetExchangeLocked(exchange, limit)
		if err == nil {
			return records
		}
		return nil
	}
	if limit <= 0 {
		limit = len(s.state.Orders)
	}
	out := make([]OrderRecord, 0, min(limit, len(s.state.Orders)))
	for i := len(s.state.Orders) - 1; i >= 0 && len(out) < limit; i-- {
		if trading.NormalizeExchange(s.state.Orders[i].TargetExchange) == exchange {
			out = append(out, s.state.Orders[i])
		}
	}
	return out
}

func (s *OrderStore) ListByTargetExchangePage(exchange string, limit, offset int) []OrderRecord {
	exchange = trading.NormalizeExchange(exchange)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		records, err := s.listSQLiteByTargetExchangePageLocked(exchange, limit, offset)
		if err == nil {
			return records
		}
		return nil
	}
	return s.listPageMemoryLocked(limit, offset, exchange)
}

func (s *OrderStore) ListSearchByTargetExchangePage(exchange, query string, limit, offset int) []OrderRecord {
	exchange = trading.NormalizeExchange(exchange)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		records, err := s.listSQLiteSearchPageLocked(exchange, query, limit, offset)
		if err == nil {
			return records
		}
		return nil
	}
	return s.listSearchPageMemoryLocked(limit, offset, exchange, query)
}

func (s *OrderStore) ListFilteredPage(exchange string, status OrderStatus, query string, limit, offset int) []OrderRecord {
	exchange = strings.TrimSpace(exchange)
	if exchange != "" {
		exchange = trading.NormalizeExchange(exchange)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		records, err := s.listSQLiteFilteredPageLocked(exchange, status, query, limit, offset)
		if err == nil {
			return records
		}
		return nil
	}
	return s.listFilteredPageMemoryLocked(limit, offset, exchange, status, query)
}

func (s *OrderStore) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		n, err := s.countSQLiteLocked("")
		if err == nil {
			return n
		}
		return 0
	}
	return len(s.state.Orders)
}

func (s *OrderStore) CountSearch(query string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		n, err := s.countSQLiteSearchLocked("", query)
		if err == nil {
			return n
		}
		return 0
	}
	return s.countSearchMemoryLocked("", query)
}

func (s *OrderStore) CountByTargetExchange(exchange string) int {
	exchange = trading.NormalizeExchange(exchange)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		n, err := s.countSQLiteLocked(exchange)
		if err == nil {
			return n
		}
		return 0
	}
	n := 0
	for i := range s.state.Orders {
		if trading.NormalizeExchange(s.state.Orders[i].TargetExchange) == exchange {
			n++
		}
	}
	return n
}

func (s *OrderStore) CountSearchByTargetExchange(exchange, query string) int {
	exchange = trading.NormalizeExchange(exchange)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		n, err := s.countSQLiteSearchLocked(exchange, query)
		if err == nil {
			return n
		}
		return 0
	}
	return s.countSearchMemoryLocked(exchange, query)
}

func (s *OrderStore) CountFiltered(exchange string, status OrderStatus, query string) int {
	exchange = strings.TrimSpace(exchange)
	if exchange != "" {
		exchange = trading.NormalizeExchange(exchange)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		n, err := s.countSQLiteFilteredLocked(exchange, status, query)
		if err == nil {
			return n
		}
		return 0
	}
	return s.countFilteredMemoryLocked(exchange, status, query)
}

func ValidOrderStatus(status OrderStatus) bool {
	switch status {
	case StatusAccepted, StatusDuplicate, StatusSubmitted, StatusPartial, StatusFailed, StatusRejected, StatusIgnored:
		return true
	default:
		return false
	}
}

func (s *OrderStore) Get(signalID string) (OrderRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.findLocked(signalID)
}

func (s *OrderStore) findLocked(signalID string) (OrderRecord, bool) {
	if s.db != nil {
		rec, err := s.findSQLiteLocked(signalID)
		return rec, err == nil
	}
	for _, rec := range s.state.Orders {
		if rec.SignalID == signalID {
			return rec, true
		}
	}
	return OrderRecord{}, false
}

func (s *OrderStore) newSignalIDLocked(now time.Time, seed string) string {
	id := NewSignalID(now, seed)
	if !s.hasSignalIDLocked(id) {
		return id
	}
	for i := 1; ; i++ {
		id = NewSignalID(now, collisionSeed(seed, i))
		if !s.hasSignalIDLocked(id) {
			return id
		}
	}
}

func (s *OrderStore) hasSignalIDLocked(signalID string) bool {
	if s.db != nil {
		var exists int
		err := s.db.QueryRow(`SELECT 1 FROM orders WHERE signal_id = ? LIMIT 1`, signalID).Scan(&exists)
		return err == nil
	}
	for _, rec := range s.state.Orders {
		if rec.SignalID == signalID {
			return true
		}
	}
	return false
}

func (s *OrderStore) Close() error {
	if s.db == nil {
		return nil
	}
	if !s.ownsDB {
		return nil
	}
	return s.db.Close()
}

func (s *OrderStore) migrateSQLite() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS orders (
			signal_id TEXT PRIMARY KEY,
			dedupe_key TEXT NOT NULL,
			status TEXT NOT NULL,
			action TEXT,
			source_action TEXT,
			api_id TEXT,
			source_exchange TEXT,
			target_exchange TEXT,
			trade_env TEXT,
			coinpair TEXT,
			ticker TEXT,
			price TEXT,
			leverage INTEGER,
			amount TEXT,
			adx REAL,
			market_strategy TEXT,
			risk_json TEXT,
			order_intent TEXT,
			position_effect TEXT,
			position_side TEXT,
			token_hash TEXT,
			accepted_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			result_json TEXT,
			error_code TEXT,
			error TEXT,
			raw_json TEXT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_orders_dedupe ON orders(dedupe_key)`,
		`CREATE INDEX IF NOT EXISTS idx_orders_accepted_at ON orders(accepted_at)`,
		`CREATE TABLE IF NOT EXISTS okx_fills (
			api_id TEXT NOT NULL,
			inst_type TEXT NOT NULL,
			inst_id TEXT NOT NULL,
			trade_id TEXT NOT NULL,
			ord_id TEXT,
			cl_ord_id TEXT,
			side TEXT,
			fill_px TEXT,
			fill_sz TEXT,
			fill_pnl TEXT,
			fee TEXT,
			fee_ccy TEXT,
			fill_time INTEGER NOT NULL,
			raw_json TEXT,
			fetched_at TEXT NOT NULL,
			PRIMARY KEY(api_id, inst_id, trade_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_okx_fills_time ON okx_fills(api_id, fill_time)`,
		`CREATE TABLE IF NOT EXISTS market_candles (
			inst_id TEXT NOT NULL,
			bar TEXT NOT NULL,
			ts INTEGER NOT NULL,
			open TEXT,
			high TEXT,
			low TEXT,
			close TEXT,
			volume TEXT,
			confirm TEXT,
			fetched_at TEXT NOT NULL,
			PRIMARY KEY(inst_id, bar, ts)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_market_candles_ts ON market_candles(inst_id, bar, ts)`,
		`CREATE TABLE IF NOT EXISTS analysis_cache (
			cache_key TEXT PRIMARY KEY,
			payload_json TEXT NOT NULL,
			refreshed_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_analysis_cache_refreshed_at ON analysis_cache(refreshed_at)`,
		`CREATE TABLE IF NOT EXISTS symbol_catalog_cache (
			exchange TEXT NOT NULL,
			env TEXT NOT NULL,
			payload_json TEXT NOT NULL,
			count INTEGER NOT NULL DEFAULT 0,
			synced_at TEXT,
			attempted_at TEXT NOT NULL,
			error TEXT,
			ticker_error TEXT,
			PRIMARY KEY(exchange, env)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_symbol_catalog_cache_attempted ON symbol_catalog_cache(exchange, env, attempted_at)`,
		`CREATE TABLE IF NOT EXISTS usdt_balance_snapshots (
			exchange TEXT NOT NULL DEFAULT 'okx',
			api_id TEXT NOT NULL,
			env TEXT NOT NULL,
			bucket_ts INTEGER NOT NULL,
			observed_at TEXT NOT NULL,
			total_eq TEXT,
			eq TEXT,
			eq_usd TEXT,
			avail_eq TEXT,
			avail_bal TEXT,
			cash_bal TEXT,
			frozen_bal TEXT,
			dis_eq TEXT,
			balance_updated_at TEXT,
			PRIMARY KEY(exchange, api_id, env, bucket_ts)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_usdt_balance_snapshots_time ON usdt_balance_snapshots(exchange, api_id, env, bucket_ts)`,
		`CREATE TABLE IF NOT EXISTS binance_fills (
			api_id TEXT NOT NULL,
			symbol TEXT NOT NULL,
			trade_id TEXT NOT NULL,
			order_id TEXT,
			side TEXT,
			position_side TEXT,
			price TEXT,
			qty TEXT,
			quote_qty TEXT,
			realized_pnl TEXT,
			commission TEXT,
			commission_asset TEXT,
			buyer INTEGER NOT NULL DEFAULT 0,
			maker INTEGER NOT NULL DEFAULT 0,
			fill_time INTEGER NOT NULL,
			raw_json TEXT,
			fetched_at TEXT NOT NULL,
			PRIMARY KEY(api_id, symbol, trade_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_binance_fills_time ON binance_fills(api_id, symbol, fill_time)`,
		`CREATE TABLE IF NOT EXISTS trade_lifecycles (
			lifecycle_id TEXT PRIMARY KEY,
			source_signal_id TEXT NOT NULL,
			root_signal_id TEXT NOT NULL,
			reentry_of_lifecycle_id TEXT,
			reentry_signal_id TEXT,
			exchange TEXT NOT NULL,
			api_id TEXT NOT NULL,
			symbol TEXT NOT NULL,
			action TEXT NOT NULL,
			status TEXT NOT NULL,
			entry_order_ids TEXT,
			entry_client_order_ids TEXT,
			tp_algo_ids TEXT,
			tp_client_algo_ids TEXT,
			tp_trigger_prices TEXT,
			sl_algo_ids TEXT,
			sl_client_algo_ids TEXT,
			sl_trigger_prices TEXT,
			entry_price TEXT,
			entry_qty TEXT,
			exit_price TEXT,
			exit_qty TEXT,
			realized_pnl TEXT,
			reentry_count INTEGER NOT NULL DEFAULT 0,
			cooldown_until TEXT,
			last_fill_time INTEGER,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_trade_lifecycles_status ON trade_lifecycles(exchange, api_id, symbol, status)`,
		`CREATE TABLE IF NOT EXISTS trade_monitor_checkpoints (
			exchange TEXT NOT NULL,
			api_id TEXT NOT NULL,
			symbol TEXT NOT NULL,
			last_fill_time INTEGER NOT NULL DEFAULT 0,
			last_trade_id TEXT,
			last_polled_at TEXT NOT NULL,
			last_error TEXT,
			updated_at TEXT NOT NULL,
			PRIMARY KEY(exchange, api_id, symbol)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_trade_monitor_checkpoints_time ON trade_monitor_checkpoints(exchange, api_id, updated_at)`,
		`CREATE TABLE IF NOT EXISTS trade_monitor_events (
			event_id TEXT PRIMARY KEY,
			event_time TEXT NOT NULL,
			exchange TEXT NOT NULL,
			api_id TEXT NOT NULL,
			symbol TEXT,
			lifecycle_id TEXT,
			source_signal_id TEXT,
			event_type TEXT NOT NULL,
			status TEXT,
			message TEXT,
			raw_json TEXT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_trade_monitor_events_time ON trade_monitor_events(event_time)`,
		`CREATE TABLE IF NOT EXISTS coinpair_blocks (
			keyword TEXT PRIMARY KEY,
			symbol TEXT NOT NULL,
			trigger_price TEXT,
			source TEXT NOT NULL,
			exchange TEXT,
			api_id TEXT,
			started_at TEXT NOT NULL,
			expires_at TEXT NOT NULL,
			last_event_id TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_coinpair_blocks_expires ON coinpair_blocks(expires_at)`,
		`CREATE TABLE IF NOT EXISTS coinpair_block_events (
			event_id TEXT PRIMARY KEY,
			keyword TEXT NOT NULL,
			occurred_at TEXT NOT NULL,
			expires_at TEXT NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_coinpair_block_events_time ON coinpair_block_events(occurred_at)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	if err := s.ensureOrderExchangeColumns(); err != nil {
		return err
	}
	if err := s.ensureUSDTBalanceExchangeKey(); err != nil {
		return err
	}
	return nil
}

func (s *OrderStore) ensureOrderExchangeColumns() error {
	columns, err := sqliteTableColumns(s.db, "orders")
	if err != nil {
		return err
	}
	if !columns["source_exchange"] {
		if _, err := s.db.Exec(`ALTER TABLE orders ADD COLUMN source_exchange TEXT`); err != nil {
			return err
		}
	}
	if !columns["source_action"] {
		if _, err := s.db.Exec(`ALTER TABLE orders ADD COLUMN source_action TEXT`); err != nil {
			return err
		}
	}
	if !columns["target_exchange"] {
		if _, err := s.db.Exec(`ALTER TABLE orders ADD COLUMN target_exchange TEXT`); err != nil {
			return err
		}
	}
	if !columns["trade_env"] {
		if _, err := s.db.Exec(`ALTER TABLE orders ADD COLUMN trade_env TEXT`); err != nil {
			return err
		}
	}
	if !columns["raw_json"] {
		if _, err := s.db.Exec(`ALTER TABLE orders ADD COLUMN raw_json TEXT`); err != nil {
			return err
		}
	}
	if !columns["risk_json"] {
		if _, err := s.db.Exec(`ALTER TABLE orders ADD COLUMN risk_json TEXT`); err != nil {
			return err
		}
	}
	if !columns["adx"] {
		if _, err := s.db.Exec(`ALTER TABLE orders ADD COLUMN adx REAL`); err != nil {
			return err
		}
	}
	if !columns["market_strategy"] {
		if _, err := s.db.Exec(`ALTER TABLE orders ADD COLUMN market_strategy TEXT`); err != nil {
			return err
		}
	}
	if !columns["order_intent"] {
		if _, err := s.db.Exec(`ALTER TABLE orders ADD COLUMN order_intent TEXT`); err != nil {
			return err
		}
	}
	if !columns["position_effect"] {
		if _, err := s.db.Exec(`ALTER TABLE orders ADD COLUMN position_effect TEXT`); err != nil {
			return err
		}
	}
	if !columns["position_side"] {
		if _, err := s.db.Exec(`ALTER TABLE orders ADD COLUMN position_side TEXT`); err != nil {
			return err
		}
	}
	if _, err := s.db.Exec(`UPDATE orders SET source_exchange = '' WHERE source_exchange IS NULL`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`UPDATE orders SET source_action = '' WHERE source_action IS NULL`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`UPDATE orders SET target_exchange = 'okx' WHERE target_exchange IS NULL OR target_exchange = ''`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`UPDATE orders SET trade_env = 'demo' WHERE trade_env IS NULL OR trade_env = ''`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`UPDATE orders SET raw_json = '' WHERE raw_json IS NULL`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`UPDATE orders SET risk_json = '' WHERE risk_json IS NULL`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`UPDATE orders SET market_strategy = '' WHERE market_strategy IS NULL`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`UPDATE orders SET order_intent = '' WHERE order_intent IS NULL`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`UPDATE orders SET position_effect = '' WHERE position_effect IS NULL`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`UPDATE orders SET position_side = '' WHERE position_side IS NULL`); err != nil {
		return err
	}
	return nil
}

func (s *OrderStore) ensureUSDTBalanceExchangeKey() error {
	columns, err := sqliteTableColumnInfo(s.db, "usdt_balance_snapshots")
	if err != nil {
		return err
	}
	exchangePK := false
	for _, column := range columns {
		if column.Name == "exchange" && column.PK > 0 {
			exchangePK = true
			break
		}
	}
	if exchangePK {
		return nil
	}
	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS usdt_balance_snapshots_new (
			exchange TEXT NOT NULL DEFAULT 'okx',
			api_id TEXT NOT NULL,
			env TEXT NOT NULL,
			bucket_ts INTEGER NOT NULL,
			observed_at TEXT NOT NULL,
			total_eq TEXT,
			eq TEXT,
			eq_usd TEXT,
			avail_eq TEXT,
			avail_bal TEXT,
			cash_bal TEXT,
			frozen_bal TEXT,
			dis_eq TEXT,
			balance_updated_at TEXT,
			PRIMARY KEY(exchange, api_id, env, bucket_ts)
		);
		INSERT OR REPLACE INTO usdt_balance_snapshots_new
			(exchange, api_id, env, bucket_ts, observed_at, total_eq, eq, eq_usd, avail_eq, avail_bal, cash_bal, frozen_bal, dis_eq, balance_updated_at)
		SELECT
			COALESCE(NULLIF(exchange, ''), 'okx'), api_id, env, bucket_ts, observed_at, total_eq, eq, eq_usd, avail_eq, avail_bal, cash_bal, frozen_bal, dis_eq, balance_updated_at
		FROM usdt_balance_snapshots;
		DROP TABLE usdt_balance_snapshots;
		ALTER TABLE usdt_balance_snapshots_new RENAME TO usdt_balance_snapshots;
		CREATE INDEX IF NOT EXISTS idx_usdt_balance_snapshots_time ON usdt_balance_snapshots(exchange, api_id, env, bucket_ts);
	`)
	if err != nil && strings.Contains(err.Error(), "no such column: exchange") {
		_, err = s.db.Exec(`
			DROP TABLE IF EXISTS usdt_balance_snapshots_new;
			CREATE TABLE usdt_balance_snapshots_new (
				exchange TEXT NOT NULL DEFAULT 'okx',
				api_id TEXT NOT NULL,
				env TEXT NOT NULL,
				bucket_ts INTEGER NOT NULL,
				observed_at TEXT NOT NULL,
				total_eq TEXT,
				eq TEXT,
				eq_usd TEXT,
				avail_eq TEXT,
				avail_bal TEXT,
				cash_bal TEXT,
				frozen_bal TEXT,
				dis_eq TEXT,
				balance_updated_at TEXT,
				PRIMARY KEY(exchange, api_id, env, bucket_ts)
			);
			INSERT OR REPLACE INTO usdt_balance_snapshots_new
				(exchange, api_id, env, bucket_ts, observed_at, total_eq, eq, eq_usd, avail_eq, avail_bal, cash_bal, frozen_bal, dis_eq, balance_updated_at)
			SELECT
				'okx', api_id, env, bucket_ts, observed_at, total_eq, eq, eq_usd, avail_eq, avail_bal, cash_bal, frozen_bal, dis_eq, balance_updated_at
			FROM usdt_balance_snapshots;
			DROP TABLE usdt_balance_snapshots;
			ALTER TABLE usdt_balance_snapshots_new RENAME TO usdt_balance_snapshots;
			CREATE INDEX IF NOT EXISTS idx_usdt_balance_snapshots_time ON usdt_balance_snapshots(exchange, api_id, env, bucket_ts);
		`)
	}
	return err
}

type sqliteColumnInfo struct {
	Name string
	PK   int
}

func sqliteTableColumns(db *sql.DB, table string) (map[string]bool, error) {
	infos, err := sqliteTableColumnInfo(db, table)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, info := range infos {
		out[info.Name] = true
	}
	return out, nil
}

func sqliteTableColumnInfo(db *sql.DB, table string) ([]sqliteColumnInfo, error) {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []sqliteColumnInfo
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return nil, err
		}
		out = append(out, sqliteColumnInfo{Name: name, PK: pk})
	}
	return out, rows.Err()
}

func (s *OrderStore) importLegacyJSON() error {
	if s.path == "" {
		return nil
	}
	b, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if len(b) == 0 {
		return nil
	}
	var state orderState
	if err := json.Unmarshal(b, &state); err != nil {
		return err
	}
	for _, rec := range state.Orders {
		if rec.SignalID == "" {
			rec.SignalID = NewSignalID(rec.AcceptedAt, rec.DedupeKey)
		}
		if rec.UpdatedAt.IsZero() {
			rec.UpdatedAt = rec.AcceptedAt
		}
		if err := s.insertOrderSQLiteLocked(rec); err != nil {
			return err
		}
	}
	return nil
}

func (s *OrderStore) recordAcceptedSQLiteLocked(signal trading.Signal, dedupeKey string, now time.Time) (OrderRecord, bool, error) {
	if s.hasPrimaryDedupeSQLiteLocked(dedupeKey) {
		rec := newOrderRecord(signal, dedupeKey, StatusDuplicate, now)
		rec.SignalID = s.newSignalIDLocked(now, dedupeKey+"|duplicate")
		rec.ErrorCode = "duplicate"
		rec.Error = "duplicate signal ignored"
		if err := s.insertOrderSQLiteLocked(rec); err != nil {
			return OrderRecord{}, false, err
		}
		return rec, true, nil
	}
	rec := newOrderRecord(signal, dedupeKey, StatusAccepted, now)
	rec.SignalID = s.newSignalIDLocked(now, dedupeKey)
	if err := s.insertOrderSQLiteLocked(rec); err != nil {
		return OrderRecord{}, false, err
	}
	return rec, false, nil
}

func (s *OrderStore) recordRejectedSQLiteLocked(signal trading.Signal, code string, err error, now time.Time) (OrderRecord, error) {
	message := ""
	if err != nil {
		message = err.Error()
	}
	dedupeKey := RejectedKey(signal, code, message, now)
	rec := newOrderRecord(signal, dedupeKey, StatusRejected, now)
	rec.SignalID = s.newSignalIDLocked(now, dedupeKey)
	rec.ErrorCode = code
	rec.Error = message
	if err := s.insertOrderSQLiteLocked(rec); err != nil {
		return OrderRecord{}, err
	}
	return rec, nil
}

func (s *OrderStore) recordIgnoredSQLiteLocked(signal trading.Signal, code, message string, now time.Time) (OrderRecord, error) {
	dedupeKey := RejectedKey(signal, code, message, now)
	rec := newOrderRecord(signal, dedupeKey, StatusIgnored, now)
	rec.SignalID = s.newSignalIDLocked(now, dedupeKey)
	rec.ErrorCode = code
	rec.Error = message
	if err := s.insertOrderSQLiteLocked(rec); err != nil {
		return OrderRecord{}, err
	}
	return rec, nil
}

func (s *OrderStore) markSubmittedSQLiteLocked(signalID string, result trading.OrderResult, now time.Time) error {
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return err
	}
	res, err := s.db.Exec(`UPDATE orders SET status = ?, updated_at = ?, result_json = ?, error = '' WHERE signal_id = ?`,
		string(StatusSubmitted),
		now.UTC().Format(time.RFC3339Nano),
		string(resultJSON),
		signalID,
	)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return errors.New("signal record not found")
	}
	return nil
}

func (s *OrderStore) markPartiallySubmittedSQLiteLocked(signalID string, result trading.OrderResult, submitErr error, now time.Time) error {
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return err
	}
	message := ""
	if submitErr != nil {
		message = submitErr.Error()
	}
	res, err := s.db.Exec(`UPDATE orders SET status = ?, updated_at = ?, result_json = ?, error_code = ?, error = ? WHERE signal_id = ?`,
		string(StatusPartial), now.UTC().Format(time.RFC3339Nano), string(resultJSON), "partial_submission", message, signalID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return errors.New("signal record not found")
	}
	return nil
}

func (s *OrderStore) markFailedSQLiteLocked(signalID, code string, failErr error, now time.Time) error {
	message := ""
	if failErr != nil {
		message = failErr.Error()
	}
	res, err := s.db.Exec(`UPDATE orders SET status = ?, updated_at = ?, error_code = ?, error = ? WHERE signal_id = ?`,
		string(StatusFailed),
		now.UTC().Format(time.RFC3339Nano),
		strings.TrimSpace(code),
		message,
		signalID,
	)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return errors.New("signal record not found")
	}
	return nil
}

func orderResultPresent(result trading.OrderResult) bool {
	return strings.TrimSpace(result.SignalID) != "" ||
		strings.TrimSpace(result.APIID) != "" ||
		strings.TrimSpace(result.TargetExchange) != "" ||
		strings.TrimSpace(result.InstID) != "" ||
		strings.TrimSpace(result.ClOrdID) != "" ||
		strings.TrimSpace(result.OrdType) != "" ||
		strings.TrimSpace(result.Px) != "" ||
		strings.TrimSpace(result.OrdID) != "" ||
		strings.TrimSpace(result.OKXCode) != "" ||
		strings.TrimSpace(result.OKXMsg) != "" ||
		result.BinanceCode != 0 ||
		strings.TrimSpace(result.BinanceMsg) != "" ||
		strings.TrimSpace(result.PositionEffect) != "" ||
		strings.TrimSpace(result.PositionSide) != "" ||
		result.Leverage != 0 ||
		len(result.RiskOrders) > 0
}

func (s *OrderStore) insertOrderSQLiteLocked(rec OrderRecord) error {
	if rec.AcceptedAt.IsZero() {
		rec.AcceptedAt = time.Now().UTC()
	}
	if rec.UpdatedAt.IsZero() {
		rec.UpdatedAt = rec.AcceptedAt
	}
	resultJSON := ""
	if orderResultPresent(rec.Result) {
		b, err := json.Marshal(rec.Result)
		if err != nil {
			return err
		}
		resultJSON = string(b)
	}
	riskJSON := ""
	if strings.TrimSpace(string(rec.Risk.Type)) != "" {
		rec.Risk.Normalize()
		b, err := json.Marshal(rec.Risk)
		if err != nil {
			return err
		}
		riskJSON = string(b)
	}
	_, err := s.db.Exec(`INSERT OR IGNORE INTO orders (
		signal_id, dedupe_key, status, action, source_action, api_id, source_exchange, target_exchange, trade_env, coinpair, ticker, price,
		leverage, amount, adx, market_strategy, risk_json, order_intent, position_effect, position_side, token_hash, accepted_at, updated_at, result_json, error_code, error, raw_json
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.SignalID,
		rec.DedupeKey,
		string(rec.Status),
		string(rec.Action),
		string(rec.SourceAction),
		rec.APIID,
		rec.SourceExchange,
		rec.TargetExchange,
		rec.TradeEnv,
		rec.Coinpair,
		rec.Ticker,
		rec.Price,
		rec.Leverage,
		rec.Amount,
		rec.ADX,
		rec.MarketStrategy,
		riskJSON,
		rec.OrderIntent,
		rec.PositionEffect,
		rec.PositionSide,
		rec.TokenHash,
		rec.AcceptedAt.UTC().Format(time.RFC3339Nano),
		rec.UpdatedAt.UTC().Format(time.RFC3339Nano),
		resultJSON,
		rec.ErrorCode,
		rec.Error,
		rec.RawJSON,
	)
	return err
}

func (s *OrderStore) listSQLiteLocked(limit int) ([]OrderRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	return s.listSQLitePageLocked(limit, 0)
}

func (s *OrderStore) listSQLitePageLocked(limit, offset int) ([]OrderRecord, error) {
	return s.listSQLiteSearchPageLocked("", "", limit, offset)
}

func (s *OrderStore) listSQLiteByTargetExchangeLocked(exchange string, limit int) ([]OrderRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	return s.listSQLiteByTargetExchangePageLocked(exchange, limit, 0)
}

func (s *OrderStore) listSQLiteByTargetExchangePageLocked(exchange string, limit, offset int) ([]OrderRecord, error) {
	return s.listSQLiteSearchPageLocked(exchange, "", limit, offset)
}

func (s *OrderStore) listSQLiteSearchPageLocked(exchange, query string, limit, offset int) ([]OrderRecord, error) {
	return s.listSQLiteFilteredPageLocked(exchange, "", query, limit, offset)
}

func (s *OrderStore) listSQLiteFilteredPageLocked(exchange string, status OrderStatus, query string, limit, offset int) ([]OrderRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	where, args := orderSQLiteFilterWhere(exchange, status, query)
	sql := `SELECT signal_id, dedupe_key, status, action, source_action, api_id, source_exchange, target_exchange, trade_env, coinpair, ticker, price,
		leverage, amount, adx, market_strategy, risk_json, order_intent, position_effect, position_side, token_hash, accepted_at, updated_at, result_json, error_code, error, raw_json
		FROM orders`
	if where != "" {
		sql += " WHERE " + where
	}
	sql += " ORDER BY accepted_at DESC, signal_id DESC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)
	rows, err := s.db.Query(sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OrderRecord
	for rows.Next() {
		rec, err := scanOrder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

func (s *OrderStore) countSQLiteLocked(exchange string) (int, error) {
	return s.countSQLiteSearchLocked(exchange, "")
}

func (s *OrderStore) countSQLiteSearchLocked(exchange, query string) (int, error) {
	return s.countSQLiteFilteredLocked(exchange, "", query)
}

func (s *OrderStore) countSQLiteFilteredLocked(exchange string, status OrderStatus, query string) (int, error) {
	where, args := orderSQLiteFilterWhere(exchange, status, query)
	var n int
	sql := `SELECT COUNT(*) FROM orders`
	if where != "" {
		sql += " WHERE " + where
	}
	err := s.db.QueryRow(sql, args...).Scan(&n)
	return n, err
}

func (s *OrderStore) listPageMemoryLocked(limit, offset int, exchange string) []OrderRecord {
	return s.listSearchPageMemoryLocked(limit, offset, exchange, "")
}

func (s *OrderStore) listSearchPageMemoryLocked(limit, offset int, exchange, query string) []OrderRecord {
	return s.listFilteredPageMemoryLocked(limit, offset, exchange, "", query)
}

func (s *OrderStore) listFilteredPageMemoryLocked(limit, offset int, exchange string, status OrderStatus, query string) []OrderRecord {
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	out := make([]OrderRecord, 0, min(limit, len(s.state.Orders)))
	terms := orderSearchTerms(query)
	skipped := 0
	for i := len(s.state.Orders) - 1; i >= 0 && len(out) < limit; i-- {
		rec := s.state.Orders[i]
		if exchange != "" && trading.NormalizeExchange(rec.TargetExchange) != exchange {
			continue
		}
		if status != "" && rec.Status != status {
			continue
		}
		if !orderRecordMatchesSearch(rec, terms) {
			continue
		}
		if skipped < offset {
			skipped++
			continue
		}
		out = append(out, rec)
	}
	return out
}

func (s *OrderStore) countSearchMemoryLocked(exchange, query string) int {
	return s.countFilteredMemoryLocked(exchange, "", query)
}

func (s *OrderStore) countFilteredMemoryLocked(exchange string, status OrderStatus, query string) int {
	terms := orderSearchTerms(query)
	n := 0
	for i := range s.state.Orders {
		rec := s.state.Orders[i]
		if exchange != "" && trading.NormalizeExchange(rec.TargetExchange) != exchange {
			continue
		}
		if status != "" && rec.Status != status {
			continue
		}
		if orderRecordMatchesSearch(rec, terms) {
			n++
		}
	}
	return n
}

const orderSQLiteSearchText = `LOWER(COALESCE(coinpair, '') || ' ' || COALESCE(ticker, '') || ' ' || COALESCE(amount, '') || ' ' || COALESCE(price, '') || ' ' || COALESCE(signal_id, '') || ' ' || COALESCE(dedupe_key, '') || ' ' || COALESCE(api_id, '') || ' ' || COALESCE(source_exchange, '') || ' ' || COALESCE(target_exchange, '') || ' ' || COALESCE(status, '') || ' ' || COALESCE(action, '') || ' ' || COALESCE(result_json, '') || ' ' || COALESCE(error_code, '') || ' ' || COALESCE(error, ''))`

func orderSQLiteSearchWhere(exchange, query string) (string, []any) {
	return orderSQLiteFilterWhere(exchange, "", query)
}

func orderSQLiteFilterWhere(exchange string, status OrderStatus, query string) (string, []any) {
	var clauses []string
	var args []any
	if strings.TrimSpace(exchange) != "" {
		clauses = append(clauses, "target_exchange = ?")
		args = append(args, trading.NormalizeExchange(exchange))
	}
	if status != "" {
		clauses = append(clauses, "status = ?")
		args = append(args, status)
	}
	for _, term := range orderSearchTerms(query) {
		clauses = append(clauses, orderSQLiteSearchText+` LIKE ? ESCAPE '\'`)
		args = append(args, sqliteLikeContainsPattern(term))
	}
	return strings.Join(clauses, " AND "), args
}

func sqliteLikeContainsPattern(term string) string {
	var b strings.Builder
	b.WriteByte('%')
	for _, r := range term {
		switch r {
		case '\\', '%', '_':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('%')
	return b.String()
}

func orderSearchTerms(query string) []string {
	return strings.Fields(strings.ToLower(strings.TrimSpace(query)))
}

func orderRecordMatchesSearch(rec OrderRecord, terms []string) bool {
	if len(terms) == 0 {
		return true
	}
	text := orderRecordSearchText(rec)
	for _, term := range terms {
		if !strings.Contains(text, term) {
			return false
		}
	}
	return true
}

func orderRecordSearchText(rec OrderRecord) string {
	parts := []string{
		rec.Coinpair,
		rec.Ticker,
		rec.Amount,
		rec.Price,
		rec.SignalID,
		rec.DedupeKey,
		rec.APIID,
		rec.SourceExchange,
		rec.TargetExchange,
		string(rec.Status),
		string(rec.Action),
		string(rec.SourceAction),
		rec.MarketStrategy,
		rec.ErrorCode,
		rec.Error,
	}
	if orderResultPresent(rec.Result) {
		if b, err := json.Marshal(rec.Result); err == nil {
			parts = append(parts, string(b))
		}
	}
	return strings.ToLower(strings.Join(parts, " "))
}

func (s *OrderStore) findSQLiteLocked(signalID string) (OrderRecord, error) {
	row := s.db.QueryRow(`SELECT signal_id, dedupe_key, status, action, source_action, api_id, source_exchange, target_exchange, trade_env, coinpair, ticker, price,
		leverage, amount, adx, market_strategy, risk_json, order_intent, position_effect, position_side, token_hash, accepted_at, updated_at, result_json, error_code, error, raw_json
		FROM orders WHERE signal_id = ?`, signalID)
	return scanOrder(row)
}

func (s *OrderStore) hasPrimaryDedupeSQLiteLocked(dedupeKey string) bool {
	var exists int
	err := s.db.QueryRow(`SELECT 1 FROM orders WHERE dedupe_key = ? AND status IN (?, ?, ?, ?) LIMIT 1`,
		dedupeKey,
		string(StatusAccepted),
		string(StatusSubmitted),
		string(StatusPartial),
		string(StatusFailed),
	).Scan(&exists)
	return err == nil
}

type orderScanner interface {
	Scan(dest ...any) error
}

func scanOrder(scanner orderScanner) (OrderRecord, error) {
	var rec OrderRecord
	var status, acceptedAt, updatedAt string
	var action, sourceAction sql.NullString
	var adx sql.NullFloat64
	var apiID, sourceExchange, targetExchange, tradeEnv, coinpair, ticker, price, amount, marketStrategy, riskJSON, orderIntent, positionEffect, positionSide, tokenHash, resultJSON, errorCode, errorText, rawJSON sql.NullString
	if err := scanner.Scan(
		&rec.SignalID,
		&rec.DedupeKey,
		&status,
		&action,
		&sourceAction,
		&apiID,
		&sourceExchange,
		&targetExchange,
		&tradeEnv,
		&coinpair,
		&ticker,
		&price,
		&rec.Leverage,
		&amount,
		&adx,
		&marketStrategy,
		&riskJSON,
		&orderIntent,
		&positionEffect,
		&positionSide,
		&tokenHash,
		&acceptedAt,
		&updatedAt,
		&resultJSON,
		&errorCode,
		&errorText,
		&rawJSON,
	); err != nil {
		return OrderRecord{}, err
	}
	rec.Status = OrderStatus(status)
	rec.Action = trading.Side(nullableString(action))
	rec.SourceAction = trading.Side(nullableString(sourceAction))
	rec.APIID = nullableString(apiID)
	rec.SourceExchange = nullableString(sourceExchange)
	rec.TargetExchange = nullableString(targetExchange)
	rec.TradeEnv = trading.NormalizeTradeEnv(nullableString(tradeEnv))
	if rec.TradeEnv == "" {
		rec.TradeEnv = trading.TradeEnvDemo
	}
	rec.Coinpair = nullableString(coinpair)
	rec.Ticker = nullableString(ticker)
	rec.Price = nullableString(price)
	rec.Amount = nullableString(amount)
	if adx.Valid {
		rec.ADX = float64Pointer(adx.Float64)
	}
	rec.MarketStrategy = nullableString(marketStrategy)
	rec.OrderIntent = nullableString(orderIntent)
	rec.PositionEffect = nullableString(positionEffect)
	rec.PositionSide = nullableString(positionSide)
	if nullableString(riskJSON) != "" {
		if err := json.Unmarshal([]byte(nullableString(riskJSON)), &rec.Risk); err != nil {
			return OrderRecord{}, err
		}
		rec.Risk.Normalize()
	}
	rec.TokenHash = nullableString(tokenHash)
	rec.ErrorCode = nullableString(errorCode)
	rec.Error = nullableString(errorText)
	rec.RawJSON = nullableString(rawJSON)
	rec.TargetExchange = trading.NormalizeExchange(rec.TargetExchange)
	if acceptedAt != "" {
		parsed, err := time.Parse(time.RFC3339Nano, acceptedAt)
		if err != nil {
			return OrderRecord{}, fmt.Errorf("parse accepted_at: %w", err)
		}
		rec.AcceptedAt = parsed
	}
	if updatedAt != "" {
		parsed, err := time.Parse(time.RFC3339Nano, updatedAt)
		if err != nil {
			return OrderRecord{}, fmt.Errorf("parse updated_at: %w", err)
		}
		rec.UpdatedAt = parsed
	}
	if nullableString(resultJSON) != "" {
		if err := json.Unmarshal([]byte(nullableString(resultJSON)), &rec.Result); err != nil {
			return OrderRecord{}, err
		}
		if rec.PositionEffect == "" {
			rec.PositionEffect = rec.Result.PositionEffect
		}
		if rec.PositionSide == "" {
			rec.PositionSide = rec.Result.PositionSide
		}
	}
	return rec, nil
}

func nullableString(value sql.NullString) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

func (s *OrderStore) load() error {
	b, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if len(b) == 0 {
		return nil
	}
	if err := json.Unmarshal(b, &s.state); err != nil {
		return err
	}
	if s.state.Dedupe == nil {
		s.state.Dedupe = map[string]string{}
	}
	if s.state.Orders == nil {
		s.state.Orders = []OrderRecord{}
	}
	if s.state.CoinpairBlocks == nil {
		s.state.CoinpairBlocks = []CoinpairBlock{}
	}
	if s.state.CoinpairBlockEvents == nil {
		s.state.CoinpairBlockEvents = map[string]bool{}
	}
	return nil
}

func (s *OrderStore) saveLocked() error {
	if s.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil && filepath.Dir(s.path) != "." {
		return err
	}
	b, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func DedupeKey(signal trading.Signal) string {
	payload := string(signal.Action) + "|" +
		trading.NormalizeExchange(signal.TargetExchange) + "|" +
		signal.APIID + "|" +
		trading.NormalizeTradeEnv(signal.TradeEnv) + "|" +
		signal.Coinpair + "|" +
		trading.NormalizeFloat(signal.Price.Value) + "|" +
		signal.SentAt + "|" +
		signal.Ticker + "|" +
		trading.NormalizeFloat(signal.Amount.Value) + "|" +
		strings.TrimSpace(signal.OrderIntent) + "|" +
		strings.TrimSpace(signal.PositionEffect) + "|" +
		strings.TrimSpace(signal.PositionSide) + "|" +
		signal.CanonicalTokenPayload() + "|" +
		signal.Token
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

func RejectedKey(signal trading.Signal, code, message string, now time.Time) string {
	payload := code + "|" +
		message + "|" +
		string(signal.Action) + "|" +
		trading.NormalizeExchange(signal.TargetExchange) + "|" +
		signal.APIID + "|" +
		trading.NormalizeTradeEnv(signal.TradeEnv) + "|" +
		signal.Coinpair + "|" +
		trading.NormalizeFloat(signal.Price.Value) + "|" +
		signal.SentAt + "|" +
		signal.Ticker + "|" +
		trading.NormalizeFloat(signal.Amount.Value) + "|" +
		strings.TrimSpace(signal.OrderIntent) + "|" +
		strings.TrimSpace(signal.PositionEffect) + "|" +
		strings.TrimSpace(signal.PositionSide) + "|" +
		signal.CanonicalTokenPayload() + "|" +
		signal.Token + "|" +
		now.UTC().Format(time.RFC3339Nano)
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

func RetryKey(sourceSignalID string, signal trading.Signal, now time.Time) string {
	payload := "retry|" +
		sourceSignalID + "|" +
		string(signal.Action) + "|" +
		trading.NormalizeExchange(signal.TargetExchange) + "|" +
		signal.APIID + "|" +
		trading.NormalizeTradeEnv(signal.TradeEnv) + "|" +
		signal.Coinpair + "|" +
		trading.NormalizeFloat(signal.Price.Value) + "|" +
		signal.SentAt + "|" +
		signal.Ticker + "|" +
		strconv.Itoa(signal.Leverage) + "|" +
		trading.NormalizeFloat(signal.Amount.Value) + "|" +
		strings.TrimSpace(signal.OrderIntent) + "|" +
		strings.TrimSpace(signal.PositionEffect) + "|" +
		strings.TrimSpace(signal.PositionSide) + "|" +
		now.UTC().Format(time.RFC3339Nano)
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

func NewSignalID(now time.Time, dedupeKey string) string {
	short := dedupeKey
	if len(short) > 12 {
		short = short[:12]
	}
	return now.UTC().Format("20060102T150405.000") + "-" + short
}

func ShortHash(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:8])
}

func collisionSeed(seed string, index int) string {
	sum := sha256.Sum256([]byte(seed + "|" + strconv.Itoa(index)))
	return hex.EncodeToString(sum[:])
}

func newOrderRecord(signal trading.Signal, dedupeKey string, status OrderStatus, now time.Time) OrderRecord {
	signal.Normalize()
	rec := OrderRecord{
		DedupeKey:      dedupeKey,
		Status:         status,
		Action:         signal.Action,
		SourceAction:   signal.SourceAction,
		APIID:          signal.APIID,
		SourceExchange: strings.TrimSpace(signal.Exchange),
		TargetExchange: trading.NormalizeExchange(signal.TargetExchange),
		TradeEnv:       trading.NormalizeTradeEnv(signal.TradeEnv),
		Coinpair:       signal.Coinpair,
		Ticker:         signal.Ticker,
		Leverage:       signal.Leverage,
		ADX:            cloneFloat64Pointer(signal.ADX),
		MarketStrategy: strings.TrimSpace(signal.MarketStrategy),
		Risk:           signal.Risk,
		OrderIntent:    strings.TrimSpace(signal.OrderIntent),
		PositionEffect: strings.TrimSpace(signal.PositionEffect),
		PositionSide:   strings.TrimSpace(signal.PositionSide),
		RawJSON:        strings.TrimSpace(signal.RawJSON),
		AcceptedAt:     now.UTC(),
		UpdatedAt:      now.UTC(),
	}
	if rec.TradeEnv == "" {
		rec.TradeEnv = trading.TradeEnvDemo
	}
	if signal.Price.Set {
		rec.Price = trading.NormalizeFloat(signal.Price.Value)
	}
	if signal.Amount.Set {
		rec.Amount = trading.NormalizeFloat(signal.Amount.Value)
	}
	if signal.Token != "" {
		rec.TokenHash = ShortHash(signal.Token)
	}
	return rec
}

func cloneFloat64Pointer(value *float64) *float64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func float64Pointer(value float64) *float64 {
	return &value
}
