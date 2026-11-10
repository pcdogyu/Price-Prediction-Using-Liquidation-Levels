package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	tvbotbinance "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotbinance"
	tvbotconfig "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotconfig"
	tvbotgate "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotgate"
	tvbotokx "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotokx"
	tvbotstorage "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotstorage"
	tvbottrading "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

const (
	exchangeOrderSyncInterval  = 5 * time.Minute
	exchangeOrderInitialWindow = 30 * 24 * time.Hour
	exchangeOrderOverlap       = 10 * time.Minute
	exchangeOrderBinanceWindow = 7*24*time.Hour - time.Millisecond
	exchangeOrderPageLimit     = 1000
	exchangeOrderMaxPages      = 100
)

type exchangeOrderSyncCoordinator struct {
	mu      sync.Mutex
	started bool
	running bool
	ctx     context.Context
}

var exchangeOrderSyncCoordinators sync.Map // map[*storage.OrderStore]*exchangeOrderSyncCoordinator

type exchangeOrderSyncCursor struct {
	From           time.Time `json:"from"`
	To             time.Time `json:"to"`
	BinanceMaxID   int64     `json:"binance_max_id,omitempty"`
	OKXLastOrdID   string    `json:"okx_last_ord_id,omitempty"`
	GateLastOffset int       `json:"gate_last_offset,omitempty"`
}

type exchangeOrderTarget struct {
	Exchange string
	APIID    string
	TradeEnv string
	Config   tvbotconfig.Config
}

type exchangeOrderSyncResponse struct {
	Started bool                                `json:"started"`
	Status  tvbotstorage.ExchangeOrderSyncState `json:"status"`
}

func (s *Server) exchangeOrderSyncCoordinator() *exchangeOrderSyncCoordinator {
	if s == nil || s.Orders == nil {
		return nil
	}
	value, _ := exchangeOrderSyncCoordinators.LoadOrStore(s.Orders, &exchangeOrderSyncCoordinator{})
	return value.(*exchangeOrderSyncCoordinator)
}

// StartExchangeOrderSyncer starts one scheduler per tenant database. The
// coordinator is keyed by OrderStore so scoped HTTP handlers and background
// monitors share the same in-flight guard without sharing tenant data.
func (s *Server) StartExchangeOrderSyncer(ctx context.Context) {
	coordinator := s.exchangeOrderSyncCoordinator()
	if coordinator == nil {
		return
	}
	coordinator.mu.Lock()
	if coordinator.started {
		coordinator.mu.Unlock()
		return
	}
	coordinator.started = true
	coordinator.ctx = ctx
	coordinator.mu.Unlock()
	go func() {
		coordinator.trigger(s, false)
		ticker := time.NewTicker(exchangeOrderSyncInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				coordinator.trigger(s, false)
			}
		}
	}()
}

func (c *exchangeOrderSyncCoordinator) trigger(s *Server, force bool) bool {
	if c == nil || s == nil {
		return false
	}
	if !force && !s.exchangeOrderSyncDue() {
		return false
	}
	c.mu.Lock()
	if c.running {
		c.mu.Unlock()
		return false
	}
	c.running = true
	ctx := c.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	c.mu.Unlock()
	go func() {
		defer func() {
			c.mu.Lock()
			c.running = false
			c.mu.Unlock()
		}()
		if err := s.syncExchangeOrders(ctx); err != nil && s.Logger != nil {
			s.Logger.Warn("failed to sync exchange order history", "error", err)
		}
	}()
	return true
}

func exchangeOrderSyncStateDue(state tvbotstorage.ExchangeOrderSyncState, ok bool, now time.Time) bool {
	if !ok {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(state.Status), "syncing") {
		// A new process must be able to resume a sync that was interrupted by a
		// restart. The in-memory coordinator still prevents duplicate work while
		// the current process is running it.
		return true
	}
	status := strings.ToLower(strings.TrimSpace(state.Status))
	last := state.LastSuccessAt
	if status == "failed" || last.IsZero() {
		last = state.LastAttemptedAt
	}
	if last.IsZero() {
		return true
	}
	return !now.UTC().Before(last.UTC().Add(exchangeOrderSyncInterval))
}

func (s *Server) exchangeOrderSyncDue() bool {
	if s == nil || s.Orders == nil {
		return false
	}
	target, err := s.exchangeOrderTarget()
	if err != nil {
		return true
	}
	state, ok, err := s.Orders.GetExchangeOrderSyncState(target.Exchange, target.APIID, target.TradeEnv)
	if err != nil {
		return true
	}
	return exchangeOrderSyncStateDue(state, ok, s.now())
}

func (s *Server) handleExchangeOrders(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "only GET is allowed")
		return
	}
	if s.Orders == nil {
		writeError(w, http.StatusServiceUnavailable, "not_configured", "order database is not configured")
		return
	}
	if primary := s.primaryExchangeForConfig(s.ConfigStore.Get()); primary == "" {
		writeError(w, http.StatusConflict, nonPrimaryExchangeCode, "no primary trading exchange is selected")
		return
	}
	target, targetErr := s.exchangeOrderTarget()
	filter := tvbotstorage.ExchangeOrderFilter{
		Exchange: target.Exchange,
		APIID:    target.APIID,
		TradeEnv: target.TradeEnv,
		Status:   strings.TrimSpace(r.URL.Query().Get("status")),
		InstID:   strings.TrimSpace(r.URL.Query().Get("inst_id")),
		Query:    strings.TrimSpace(r.URL.Query().Get("q")),
		Limit:    queryPositiveInt(r, "limit", 50),
		Offset:   queryNonNegativeInt(r, "offset"),
	}
	if targetErr != nil {
		// Never fall back to another target when the current tenant target is
		// unavailable. The endpoint is target-scoped and must not expose a
		// mixed account history just because configuration is incomplete.
		filter.Exchange = "__unconfigured__"
		filter.APIID = ""
		filter.TradeEnv = ""
	}
	page, err := s.Orders.ListExchangeOrders(filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "exchange_orders_failed", err.Error())
		return
	}
	status := tvbotstorage.ExchangeOrderSyncState{Status: "idle"}
	if targetErr == nil {
		if stored, ok, err := s.Orders.GetExchangeOrderSyncState(target.Exchange, target.APIID, target.TradeEnv); err == nil && ok {
			status = stored
		} else if err != nil && s.Logger != nil {
			s.Logger.Warn("failed to read exchange order sync status", "error", err)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"orders":    page.Orders,
		"total":     page.Total,
		"limit":     filter.Limit,
		"offset":    filter.Offset,
		"page":      filter.Offset/filter.Limit + 1,
		"page_size": filter.Limit,
		"total_pages": func() int {
			if page.Total == 0 {
				return 0
			}
			return (page.Total + filter.Limit - 1) / filter.Limit
		}(),
		"target": map[string]string{
			"exchange":  target.Exchange,
			"api_id":    target.APIID,
			"trade_env": target.TradeEnv,
		},
		"sync": status,
		"target_error": func() string {
			if targetErr != nil {
				return targetErr.Error()
			}
			return ""
		}(),
	})
}

func (s *Server) handleExchangeOrderSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "only POST is allowed")
		return
	}
	if s.Orders == nil {
		writeError(w, http.StatusServiceUnavailable, "not_configured", "order database is not configured")
		return
	}
	if primary := s.primaryExchangeForConfig(s.ConfigStore.Get()); primary == "" {
		writeError(w, http.StatusConflict, nonPrimaryExchangeCode, "no primary trading exchange is selected")
		return
	}
	if _, err := s.exchangeOrderTarget(); err != nil {
		writeError(w, http.StatusBadRequest, "exchange_order_target_unavailable", err.Error())
		return
	}
	coordinator := s.exchangeOrderSyncCoordinator()
	started := coordinator != nil && coordinator.trigger(s, true)
	status := tvbotstorage.ExchangeOrderSyncState{Status: "idle"}
	target, _ := s.exchangeOrderTarget()
	if stored, ok, err := s.Orders.GetExchangeOrderSyncState(target.Exchange, target.APIID, target.TradeEnv); err == nil && ok {
		status = stored
	}
	writeJSON(w, http.StatusAccepted, exchangeOrderSyncResponse{Started: started, Status: status})
}

func (s *Server) exchangeOrderTarget() (exchangeOrderTarget, error) {
	if s == nil || s.ConfigStore == nil {
		return exchangeOrderTarget{}, errors.New("TVBot configuration is not available")
	}
	cfg := s.ConfigStore.Get()
	exchange := s.primaryExchangeForConfig(cfg)
	if !tvbottrading.ValidTargetExchange(exchange) {
		return exchangeOrderTarget{}, errors.New("target exchange is not configured")
	}
	tradeEnv := strings.ToLower(strings.TrimSpace(cfg.Trading.TargetTradeEnv))
	if tradeEnv != tvbottrading.TradeEnvDemo && tradeEnv != tvbottrading.TradeEnvLive {
		tradeEnv = strings.ToLower(strings.TrimSpace(cfg.Trading.Env))
	}
	if tradeEnv != tvbottrading.TradeEnvDemo && tradeEnv != tvbottrading.TradeEnvLive {
		return exchangeOrderTarget{}, errors.New("target trade environment is not configured")
	}
	requestedAPIID := strings.TrimSpace(cfg.Trading.TargetAPIID)
	var actualAPIID string
	var err error
	switch exchange {
	case tvbottrading.ExchangeOKX:
		store := s.okxCredentialStore(tradeEnv)
		if store == nil {
			return exchangeOrderTarget{}, errors.New("OKX credential store is not configured")
		}
		_, actualAPIID, err = store.OKXCredentials(requestedAPIID)
		if err != nil {
			return exchangeOrderTarget{}, err
		}
	case tvbottrading.ExchangeBinance:
		store := s.binanceCredentialStore(tradeEnv)
		if store == nil {
			return exchangeOrderTarget{}, errors.New("Binance credential store is not configured")
		}
		_, actualAPIID, err = store.BinanceCredentials(requestedAPIID)
		if err != nil {
			return exchangeOrderTarget{}, err
		}
	case tvbottrading.ExchangeGate:
		store := s.gateCredentialStore(tradeEnv)
		if store == nil {
			return exchangeOrderTarget{}, errors.New("Gate credential store is not configured")
		}
		_, actualAPIID, err = store.GateCredentials(requestedAPIID)
		if err != nil {
			return exchangeOrderTarget{}, err
		}
	}
	if strings.TrimSpace(actualAPIID) == "" {
		actualAPIID = requestedAPIID
	}
	return exchangeOrderTarget{
		Exchange: exchange,
		APIID:    actualAPIID,
		TradeEnv: tradeEnv,
		Config:   configForTradeEnv(cfg, tradeEnv),
	}, nil
}

func (s *Server) syncExchangeOrders(ctx context.Context) error {
	target, err := s.exchangeOrderTarget()
	if err != nil {
		return err
	}
	now := s.now().UTC()
	state, ok, err := s.Orders.GetExchangeOrderSyncState(target.Exchange, target.APIID, target.TradeEnv)
	if err != nil {
		return err
	}
	if !ok {
		state = tvbotstorage.ExchangeOrderSyncState{
			Exchange: target.Exchange,
			APIID:    target.APIID,
			TradeEnv: target.TradeEnv,
		}
	}
	state.Exchange = target.Exchange
	state.APIID = target.APIID
	state.TradeEnv = target.TradeEnv
	state.Status = "syncing"
	state.LastAttemptedAt = now
	state.LastError = ""
	state.UpdatedAt = now
	if err := s.Orders.UpsertExchangeOrderSyncState(state); err != nil {
		return err
	}
	from := now.Add(-exchangeOrderInitialWindow)
	if !state.LastSuccessAt.IsZero() {
		from = state.LastSuccessAt.Add(-exchangeOrderOverlap)
	}
	if from.Before(now.Add(-exchangeOrderInitialWindow)) && state.LastSuccessAt.IsZero() {
		from = now.Add(-exchangeOrderInitialWindow)
	}
	cursor := exchangeOrderSyncCursor{From: from, To: now}
	var orders []tvbotstorage.ExchangeOrder
	switch target.Exchange {
	case tvbottrading.ExchangeOKX:
		orders, cursor, err = s.fetchOKXExchangeOrders(ctx, target, from, now)
	case tvbottrading.ExchangeBinance:
		orders, cursor, err = s.fetchBinanceExchangeOrders(ctx, target, from, now)
	case tvbottrading.ExchangeGate:
		orders, cursor, err = s.fetchGateExchangeOrders(ctx, target, from, now)
	default:
		err = fmt.Errorf("unsupported target exchange %q", target.Exchange)
	}
	if err != nil {
		state.Status = "failed"
		state.LastError = err.Error()
		state.UpdatedAt = s.now().UTC()
		_ = s.Orders.UpsertExchangeOrderSyncState(state)
		return err
	}
	stats, err := s.Orders.UpsertExchangeOrders(orders, now)
	if err != nil {
		state.Status = "failed"
		state.LastError = err.Error()
		state.UpdatedAt = s.now().UTC()
		_ = s.Orders.UpsertExchangeOrderSyncState(state)
		return err
	}
	cursorJSON, _ := json.Marshal(cursor)
	state.Status = "synced"
	state.CursorJSON = string(cursorJSON)
	state.LastSuccessAt = now
	state.LastError = ""
	state.LastReceived = stats.Received
	state.LastUpserted = stats.Upserted
	state.UpdatedAt = s.now().UTC()
	return s.Orders.UpsertExchangeOrderSyncState(state)
}

func (s *Server) fetchBinanceExchangeOrders(ctx context.Context, target exchangeOrderTarget, from, to time.Time) ([]tvbotstorage.ExchangeOrder, exchangeOrderSyncCursor, error) {
	store := s.binanceCredentialStore(target.TradeEnv)
	creds, apiID, err := store.BinanceCredentials(target.APIID)
	if err != nil {
		return nil, exchangeOrderSyncCursor{}, err
	}
	client := s.analysisBinanceClient(target.Config, creds)
	result := []tvbotstorage.ExchangeOrder{}
	cursor := exchangeOrderSyncCursor{From: from, To: to}
	info, err := client.ExchangeInfo(ctx)
	if err != nil {
		return result, cursor, err
	}
	symbols := make([]string, 0, len(info.Symbols))
	for _, symbol := range info.Symbols {
		if strings.EqualFold(strings.TrimSpace(symbol.Status), "trading") && supportedBinanceExchangeOrderQuoteAsset(symbol.QuoteAsset) {
			symbols = append(symbols, symbol.Symbol)
		}
	}
	if len(symbols) == 0 {
		return result, cursor, errors.New("Binance exchangeInfo returned no active USDT/USDC symbols")
	}
	// Incremental syncs are normally bounded to symbols already present in
	// exchange_orders. That optimization can hide a newly traded symbol when
	// its local fill arrived after the exchange-order cursor was last saved.
	// Discover recent local symbols and backfill them from the initial window;
	// this keeps the scan bounded while allowing history to catch up.
	discovered := s.discoverBinanceOrderSymbols(apiID, target, to)
	knownSet := map[string]struct{}{}
	if known, err := s.Orders.ListExchangeOrderInstIDs(target.Exchange, apiID, target.TradeEnv); err == nil && len(known) > 0 {
		for _, symbol := range known {
			knownSet[strings.ToUpper(strings.TrimSpace(symbol))] = struct{}{}
		}
		filtered := symbols[:0]
		for _, symbol := range symbols {
			normalized := strings.ToUpper(strings.TrimSpace(symbol))
			if _, ok := knownSet[normalized]; ok {
				filtered = append(filtered, symbol)
				continue
			}
			if _, ok := discovered[normalized]; ok {
				filtered = append(filtered, symbol)
			}
		}
		if len(filtered) > 0 {
			symbols = filtered
		}
	}
	for _, symbol := range symbols {
		windowStart := from
		if _, known := knownSet[strings.ToUpper(strings.TrimSpace(symbol))]; !known {
			if _, ok := discovered[strings.ToUpper(strings.TrimSpace(symbol))]; ok {
				windowStart = to.Add(-exchangeOrderInitialWindow)
			}
		}
		for {
			windowEnd := windowStart.Add(exchangeOrderBinanceWindow)
			if windowEnd.After(to) {
				windowEnd = to
			}
			var orderID int64
			for page := 0; page < exchangeOrderMaxPages; page++ {
				rows, err := client.AllOrders(ctx, symbol, orderID, windowStart, windowEnd, exchangeOrderPageLimit)
				if err != nil {
					return result, cursor, err
				}
				pageMaxID := orderID
				for _, row := range rows {
					if row.OrderID <= 0 {
						continue
					}
					if row.OrderID > cursor.BinanceMaxID {
						cursor.BinanceMaxID = row.OrderID
					}
					if row.OrderID > pageMaxID {
						pageMaxID = row.OrderID
					}
					result = append(result, exchangeOrderFromBinance(apiID, target.TradeEnv, row))
				}
				if len(rows) < exchangeOrderPageLimit {
					break
				}
				if pageMaxID <= orderID {
					return result, cursor, fmt.Errorf("Binance allOrders pagination did not advance for %s", symbol)
				}
				orderID = pageMaxID + 1
			}
			if !windowEnd.Before(to) {
				break
			}
			windowStart = windowEnd.Add(time.Millisecond)
		}
	}
	return result, cursor, nil
}

func supportedBinanceExchangeOrderQuoteAsset(quoteAsset string) bool {
	switch strings.ToUpper(strings.TrimSpace(quoteAsset)) {
	case "USDT", "USDC":
		return true
	default:
		return false
	}
}

func (s *Server) discoverBinanceOrderSymbols(apiID string, target exchangeOrderTarget, to time.Time) map[string]struct{} {
	discovered := make(map[string]struct{})
	add := func(symbol string) {
		symbol = strings.ToUpper(strings.TrimSpace(symbol))
		if symbol != "" {
			discovered[symbol] = struct{}{}
		}
	}

	if fills, err := s.Orders.ListBinanceFills(apiID, to.Add(-exchangeOrderInitialWindow)); err == nil {
		for _, fill := range fills {
			add(fill.Symbol)
		}
	}
	for _, record := range s.Orders.List(200) {
		if tvbottrading.NormalizeExchange(record.TargetExchange) != target.Exchange {
			continue
		}
		if strings.TrimSpace(record.APIID) != "" && strings.TrimSpace(record.APIID) != apiID {
			continue
		}
		if env := tvbottrading.NormalizeTradeEnv(record.TradeEnv); env != "" && env != target.TradeEnv {
			continue
		}
		add(record.Result.InstID)
		add(record.Coinpair)
		add(record.Ticker)
	}
	return discovered
}

func (s *Server) fetchOKXExchangeOrders(ctx context.Context, target exchangeOrderTarget, from, to time.Time) ([]tvbotstorage.ExchangeOrder, exchangeOrderSyncCursor, error) {
	store := s.okxCredentialStore(target.TradeEnv)
	creds, apiID, err := store.OKXCredentials(target.APIID)
	if err != nil {
		return nil, exchangeOrderSyncCursor{}, err
	}
	client := s.analysisOKXClient(target.Config, creds)
	result := []tvbotstorage.ExchangeOrder{}
	cursor := exchangeOrderSyncCursor{From: from, To: to}
	after := ""
	for page := 0; page < exchangeOrderMaxPages; page++ {
		rows, _, err := client.OrdersHistoryArchive(ctx, "SWAP", after, "", 100)
		if err != nil {
			return result, cursor, err
		}
		if len(rows) == 0 {
			return result, cursor, nil
		}
		oldest := time.Time{}
		for _, row := range rows {
			createdAt := parseUnixMillis(row.CTime)
			updatedAt := parseUnixMillis(row.UTime)
			if createdAt.IsZero() {
				createdAt = updatedAt
			}
			if !createdAt.IsZero() && (createdAt.Before(from) || createdAt.After(to)) {
				if oldest.IsZero() || createdAt.Before(oldest) {
					oldest = createdAt
				}
				continue
			}
			if strings.TrimSpace(row.OrdID) == "" {
				continue
			}
			result = append(result, exchangeOrderFromOKX(apiID, target.TradeEnv, row, createdAt, updatedAt))
			if !createdAt.IsZero() && (oldest.IsZero() || createdAt.Before(oldest)) {
				oldest = createdAt
			}
			cursor.OKXLastOrdID = row.OrdID
		}
		if len(rows) < 100 || (!oldest.IsZero() && oldest.Before(from)) {
			return result, cursor, nil
		}
		next := strings.TrimSpace(rows[len(rows)-1].OrdID)
		if next == "" || next == after {
			return result, cursor, nil
		}
		after = next
	}
	return result, cursor, fmt.Errorf("OKX order history reached %d page limit", exchangeOrderMaxPages)
}

func (s *Server) fetchGateExchangeOrders(ctx context.Context, target exchangeOrderTarget, from, to time.Time) ([]tvbotstorage.ExchangeOrder, exchangeOrderSyncCursor, error) {
	store := s.gateCredentialStore(target.TradeEnv)
	creds, apiID, err := store.GateCredentials(target.APIID)
	if err != nil {
		return nil, exchangeOrderSyncCursor{}, err
	}
	client := s.analysisGateClient(target.Config, creds)
	result := []tvbotstorage.ExchangeOrder{}
	cursor := exchangeOrderSyncCursor{From: from, To: to}
	for page := 0; page < exchangeOrderMaxPages; page++ {
		offset := page * exchangeOrderPageLimit
		rows, err := client.OrdersTimerange(ctx, "", from, to, exchangeOrderPageLimit, offset)
		if err != nil {
			return result, cursor, err
		}
		for _, row := range rows {
			if row.IDString() == "" {
				continue
			}
			result = append(result, exchangeOrderFromGate(apiID, target.TradeEnv, row))
		}
		cursor.GateLastOffset = offset
		if len(rows) < exchangeOrderPageLimit {
			return result, cursor, nil
		}
	}
	return result, cursor, fmt.Errorf("Gate order history reached %d page limit", exchangeOrderMaxPages)
}

func exchangeOrderFromBinance(apiID, tradeEnv string, row tvbotbinance.OpenOrder) tvbotstorage.ExchangeOrder {
	var createdAt, updatedAt time.Time
	if row.Time > 0 {
		createdAt = time.UnixMilli(row.Time).UTC()
	}
	if row.UpdateTime > 0 {
		updatedAt = time.UnixMilli(row.UpdateTime).UTC()
	}
	if createdAt.IsZero() {
		createdAt = updatedAt
	}
	if updatedAt.IsZero() {
		updatedAt = createdAt
	}
	raw, _ := json.Marshal(row)
	remoteID := strconv.FormatInt(row.OrderID, 10)
	return tvbotstorage.ExchangeOrder{
		OrderKey:      tvbotstorage.ExchangeOrderKey(tvbottrading.ExchangeBinance, apiID, tradeEnv, remoteID),
		Exchange:      tvbottrading.ExchangeBinance,
		APIID:         apiID,
		TradeEnv:      tradeEnv,
		RemoteOrderID: remoteID,
		ClientOrderID: row.ClientOrderID,
		InstID:        row.Symbol,
		Status:        row.Status,
		Side:          row.Side,
		PositionSide:  row.PositionSide,
		OrderType:     row.Type,
		TimeInForce:   row.TimeInForce,
		Price:         row.Price,
		AveragePrice:  row.AvgPrice,
		OriginalQty:   row.OrigQty,
		ExecutedQty:   row.ExecutedQty,
		QuoteQty:      row.CumQuote,
		ReduceOnly:    row.ReduceOnly,
		ClosePosition: row.ClosePosition,
		CreatedAt:     createdAt,
		UpdatedAt:     updatedAt,
		FinishedAt:    finishedOrderTime(row.Status, updatedAt),
		RawJSON:       string(raw),
	}
}

func exchangeOrderFromOKX(apiID, tradeEnv string, row tvbotokx.PendingOrder, createdAt, updatedAt time.Time) tvbotstorage.ExchangeOrder {
	if updatedAt.IsZero() {
		updatedAt = createdAt
	}
	raw, _ := json.Marshal(row)
	return tvbotstorage.ExchangeOrder{
		OrderKey:      tvbotstorage.ExchangeOrderKey(tvbottrading.ExchangeOKX, apiID, tradeEnv, row.OrdID),
		Exchange:      tvbottrading.ExchangeOKX,
		APIID:         apiID,
		TradeEnv:      tradeEnv,
		RemoteOrderID: row.OrdID,
		ClientOrderID: row.ClOrdID,
		InstID:        row.InstID,
		Status:        row.State,
		FinishAs:      row.State,
		Side:          row.Side,
		PositionSide:  row.PosSide,
		OrderType:     row.OrdType,
		Price:         row.Px,
		AveragePrice:  row.AvgPx,
		OriginalQty:   row.Sz,
		ExecutedQty:   row.AccFillSz,
		ReduceOnly:    rawBool(row.ReduceOnly),
		ClosePosition: rawBool(row.ClosePosition),
		CreatedAt:     createdAt,
		UpdatedAt:     updatedAt,
		FinishedAt:    finishedOrderTime(row.State, updatedAt),
		RawJSON:       firstNonEmpty(row.RawJSON, string(raw)),
	}
}

func exchangeOrderFromGate(apiID, tradeEnv string, row tvbotgate.Order) tvbotstorage.ExchangeOrder {
	createdAt, _ := gateUnixTime(row.CreateTime)
	updatedAt, _ := gateUnixTime(row.UpdateTime)
	finishedAt, _ := gateUnixTime(row.FinishTime)
	if createdAt.IsZero() {
		createdAt = updatedAt
	}
	if updatedAt.IsZero() {
		updatedAt = createdAt
	}
	side := ""
	if value, err := strconv.ParseFloat(strings.TrimSpace(row.Size), 64); err == nil {
		if value > 0 {
			side = "buy"
		} else if value < 0 {
			side = "sell"
		}
	}
	raw, _ := json.Marshal(row)
	return tvbotstorage.ExchangeOrder{
		OrderKey:      tvbotstorage.ExchangeOrderKey(tvbottrading.ExchangeGate, apiID, tradeEnv, row.IDString()),
		Exchange:      tvbottrading.ExchangeGate,
		APIID:         apiID,
		TradeEnv:      tradeEnv,
		RemoteOrderID: row.IDString(),
		ClientOrderID: row.Text,
		InstID:        row.Contract,
		Status:        row.Status,
		FinishAs:      row.FinishAs,
		Side:          side,
		TimeInForce:   row.TIF,
		Price:         row.Price,
		AveragePrice:  row.FillPrice,
		OriginalQty:   row.Size,
		RemainingQty:  row.Left,
		QuoteQty:      row.TradeValue,
		ReduceOnly:    row.IsReduceOnly,
		ClosePosition: row.IsClose,
		CreatedAt:     createdAt,
		UpdatedAt:     updatedAt,
		FinishedAt:    finishedAt,
		RawJSON:       string(raw),
	}
}

func parseUnixMillis(raw string) time.Time {
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || value <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(value).UTC()
}

func rawBool(raw json.RawMessage) bool {
	value := strings.TrimSpace(string(raw))
	return strings.EqualFold(value, "true") || value == "1" || strings.EqualFold(value, `"true"`)
}

func finishedOrderTime(status string, updatedAt time.Time) time.Time {
	status = strings.ToLower(strings.TrimSpace(status))
	if status == "filled" || status == "canceled" || status == "mmp_canceled" || status == "cancelled" || status == "finished" {
		return updatedAt
	}
	return time.Time{}
}

func queryPositiveInt(r *http.Request, key string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get(key)))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func queryNonNegativeInt(r *http.Request, key string) int {
	value, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get(key)))
	if err != nil || value < 0 {
		return 0
	}
	return value
}
