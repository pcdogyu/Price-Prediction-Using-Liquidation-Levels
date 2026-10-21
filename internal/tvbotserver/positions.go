package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotbinance"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotconfig"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotgate"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotokx"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotstorage"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

var (
	errPositionNotOpen                = errors.New("position is not open")
	errPendingOrderNoRemaining        = errors.New("pending order has no remaining size")
	positionClosePollInterval         = 5 * time.Second
	positionCloseLimitTimeout         = 60 * time.Second
	marketClosePendingPollInterval    = 250 * time.Millisecond
	marketClosePendingTimeout         = 3 * time.Second
	binanceUnknownOrderLookupAttempts = 6
	binanceUnknownOrderLookupDelay    = 500 * time.Millisecond
	binanceReduceOnlyRetryAttempts    = 3
	binanceReduceOnlyRetryDelay       = 250 * time.Millisecond
	lowMarginPositionCheckInterval    = time.Minute
	lowMarginPositionThresholdUSDT    = 10.0
	autoProfitPositionCheckInterval   = 5 * time.Minute
	autoProfitPositionReturnThreshold = 0.05
	autoProfitClosePollInterval       = 10 * time.Second
	autoProfitCloseLimitTimeout       = 5 * time.Minute
	positionMonitorDefaultInterval    = 300 * time.Second
	positionMonitorScanTimeout        = 20 * time.Second
	stalePendingOrderCancelInterval   = 5 * time.Minute
	stalePendingOrderCancelAfter      = 2 * time.Hour
	positionCloseJobs                 = newPositionCloseRegistry()
	pendingOrderChaseInterval         = 5 * time.Second
	pendingOrderChaseTimeout          = 60 * time.Second
	pendingOrderChaseJobs             = newPendingOrderChaseRegistry()
	positionCloseSeq                  uint64
	positionProtectionSeq             uint64
	pendingOrderMarketSeq             uint64
)

const (
	positionEntryLookback       = 90 * 24 * time.Hour
	positionEntryCacheTTL       = 15 * time.Second
	positionEntryHistoryLimit   = 100
	positionEntryMaxOKXPages    = 90
	binanceUserTradesWindow     = 7 * 24 * time.Hour
	binanceUserTradesLimit      = 1000
	positionEntrySizeEpsilon    = 1e-9
	entryTimeSourceOKXFills     = "okx_fills_history"
	entryTimeSourceBinanceTrade = "binance_user_trades"
	entryTimeSourceGateTrades   = "gate_my_trades"
	entryTimeSourcePositionTime = "exchange_position_time"
	positionProtectionTP        = "tp"
	positionProtectionSL        = "sl"
	positionProtectionTrailing  = "trailing"
)

type positionsResponse struct {
	OK          bool           `json:"ok"`
	Exchange    string         `json:"exchange"`
	APIID       string         `json:"api_id"`
	InstType    string         `json:"inst_type"`
	Count       int            `json:"count"`
	RefreshedAt time.Time      `json:"refreshed_at"`
	Positions   []positionView `json:"positions"`
}

type positionView struct {
	okx.Position
	PricePrecision    *int     `json:"price_precision,omitempty"`
	QuantityPrecision *int     `json:"quantity_precision,omitempty"`
	MarketStrategy    string   `json:"market_strategy,omitempty"`
	EntryADX          *float64 `json:"entry_adx,omitempty"`
	EntryFillTime     string   `json:"entry_fill_time"`
	HoldingSeconds    int64    `json:"holding_seconds"`
	EntryTimeSource   string   `json:"entry_time_source"`
	EntryTimeError    string   `json:"entry_time_error"`
	entryOrdID        string
	entryClOrdID      string
}

type positionEntryFill struct {
	InstID   string
	OrdID    string
	ClOrdID  string
	PosSide  string
	Side     string
	Size     float64
	FillTime time.Time
}

type positionEntryFillCache struct {
	mu    sync.Mutex
	items map[string]positionEntryFillCacheItem
}

type positionEntryFillCacheItem struct {
	fetchedAt time.Time
	fills     []positionEntryFill
}

type pendingOrdersResponse struct {
	OK          bool               `json:"ok"`
	Exchange    string             `json:"exchange"`
	APIID       string             `json:"api_id"`
	InstType    string             `json:"inst_type"`
	Count       int                `json:"count"`
	NormalCount int                `json:"normal_count"`
	AlgoCount   int                `json:"algo_count"`
	TotalCount  int                `json:"total_count"`
	RefreshedAt time.Time          `json:"refreshed_at"`
	Orders      []pendingOrderView `json:"orders"`
	AlgoOrders  []pendingOrderView `json:"algo_orders,omitempty"`
}

type pendingOrderView struct {
	okx.PendingOrder
	OrderGroup             string `json:"order_group,omitempty"`
	AlgoID                 string `json:"algo_id,omitempty"`
	AlgoClOrdID            string `json:"algo_cl_ord_id,omitempty"`
	TriggerPx              string `json:"trigger_px,omitempty"`
	ActivationPx           string `json:"activation_px,omitempty"`
	CallbackRatio          string `json:"callback_ratio,omitempty"`
	PricePrecision         *int   `json:"price_precision,omitempty"`
	QuantityPrecision      *int   `json:"quantity_precision,omitempty"`
	MidPx                  string `json:"mid_px,omitempty"`
	ChasePx                string `json:"chase_px,omitempty"`
	Margin                 string `json:"margin,omitempty"`
	PriceError             string `json:"price_error,omitempty"`
	Chasing                bool   `json:"chasing"`
	Chaseable              bool   `json:"chaseable"`
	ChaseUnavailableReason string `json:"chase_unavailable_reason,omitempty"`
}

type symbolDisplayPrecision struct {
	PricePrecision    *int
	QuantityPrecision *int
}

type positionCloseRequest struct {
	Exchange string  `json:"exchange"`
	Env      string  `json:"env"`
	APIID    string  `json:"api_id"`
	InstID   string  `json:"inst_id"`
	PosSide  string  `json:"pos_side"`
	Mode     string  `json:"mode"`
	Ratio    float64 `json:"ratio,omitempty"`
}

type positionProtectionRequest struct {
	Exchange string `json:"exchange"`
	Env      string `json:"env"`
	APIID    string `json:"api_id"`
	InstID   string `json:"inst_id"`
	PosSide  string `json:"pos_side"`
	Kind     string `json:"kind"`
}

type pendingOrderChaseRequest struct {
	Exchange    string `json:"exchange"`
	Env         string `json:"env"`
	APIID       string `json:"api_id"`
	OrderGroup  string `json:"order_group"`
	InstID      string `json:"inst_id"`
	OrdID       string `json:"ord_id"`
	ClOrdID     string `json:"cl_ord_id"`
	AlgoID      string `json:"algo_id"`
	AlgoClOrdID string `json:"algo_cl_ord_id"`
}

type pendingOrderChaseResponse struct {
	OK          bool   `json:"ok"`
	Status      string `json:"status"`
	APIID       string `json:"api_id"`
	OrderGroup  string `json:"order_group,omitempty"`
	InstID      string `json:"inst_id"`
	OrdID       string `json:"ord_id,omitempty"`
	ClOrdID     string `json:"cl_ord_id,omitempty"`
	AlgoID      string `json:"algo_id,omitempty"`
	AlgoClOrdID string `json:"algo_cl_ord_id,omitempty"`
	MidPx       string `json:"mid_px,omitempty"`
	Px          string `json:"px,omitempty"`
	Message     string `json:"message,omitempty"`
}

type positionCloseResponse struct {
	OK      bool   `json:"ok"`
	Status  string `json:"status"`
	Mode    string `json:"mode"`
	APIID   string `json:"api_id"`
	InstID  string `json:"inst_id"`
	PosSide string `json:"pos_side,omitempty"`
	Sz      string `json:"sz,omitempty"`
	OrdID   string `json:"ord_id,omitempty"`
	ClOrdID string `json:"cl_ord_id,omitempty"`
	Px      string `json:"px,omitempty"`
	Message string `json:"message,omitempty"`
}

type positionProtectionResponse struct {
	OK            bool   `json:"ok"`
	Status        string `json:"status"`
	Exchange      string `json:"exchange"`
	APIID         string `json:"api_id"`
	InstID        string `json:"inst_id"`
	PosSide       string `json:"pos_side,omitempty"`
	Kind          string `json:"kind"`
	Sz            string `json:"sz,omitempty"`
	AlgoID        string `json:"algo_id,omitempty"`
	AlgoClOrdID   string `json:"algo_cl_ord_id,omitempty"`
	TriggerPx     string `json:"trigger_px,omitempty"`
	CallbackRatio string `json:"callback_ratio,omitempty"`
	Message       string `json:"message,omitempty"`
}

type positionCloseOrder struct {
	Position              okx.Position
	Ack                   okx.OrderAck
	Acks                  []okx.OrderAck
	Px                    string
	CloseSz               string
	Partial               bool
	Unknown               bool
	MarketFallback        bool
	NonReduceOnlyFallback bool
}

type gatePositionCloseOrder struct {
	Position       okx.Position
	Order          gate.Order
	Px             string
	CloseSz        string
	TargetSz       string
	InitialPosSize string
	Partial        bool
}

type positionCloseRegistry struct {
	mu     sync.Mutex
	active map[string]struct{}
}

type pendingOrderChaseRegistry struct {
	mu     sync.Mutex
	active map[string]context.CancelFunc
}

func newPositionCloseRegistry() *positionCloseRegistry {
	return &positionCloseRegistry{active: map[string]struct{}{}}
}

func newPendingOrderChaseRegistry() *pendingOrderChaseRegistry {
	return &pendingOrderChaseRegistry{active: map[string]context.CancelFunc{}}
}

func (r *positionCloseRegistry) start(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.active[key]; ok {
		return false
	}
	r.active[key] = struct{}{}
	return true
}

func (r *positionCloseRegistry) done(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.active, key)
}

func (r *pendingOrderChaseRegistry) start(key string, cancel context.CancelFunc) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.active[key]; ok {
		return false
	}
	r.active[key] = cancel
	return true
}

func (r *pendingOrderChaseRegistry) stop(key string) bool {
	r.mu.Lock()
	cancel, ok := r.active[key]
	if ok {
		delete(r.active, key)
	}
	r.mu.Unlock()
	if ok {
		cancel()
	}
	return ok
}

func (r *pendingOrderChaseRegistry) done(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.active, key)
}

func (r *pendingOrderChaseRegistry) move(oldKey, newKey string) {
	if oldKey == newKey {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cancel, ok := r.active[oldKey]
	if !ok {
		return
	}
	delete(r.active, oldKey)
	r.active[newKey] = cancel
}

func (r *pendingOrderChaseRegistry) activeKey(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.active[key]
	return ok
}

func (s *Server) handlePositions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "only GET is allowed")
		return
	}
	exchange := trading.NormalizeExchange(r.URL.Query().Get("exchange"))
	instType := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("inst_type")))
	if instType == "" {
		instType = "SWAP"
	}
	apiID := strings.TrimSpace(r.URL.Query().Get("api_id"))
	refresh := strings.EqualFold(r.URL.Query().Get("refresh"), "true")
	cfg, err := configForRequestedTradeEnv(s.ConfigStore.Get(), r.URL.Query().Get("env"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_environment", err.Error())
		return
	}
	okxStore := s.okxCredentialStore(cfg.Trading.Env)
	binanceStore := s.binanceCredentialStore(cfg.Trading.Env)
	gateStore := s.gateCredentialStore(cfg.Trading.Env)
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	var (
		resp positionsResponse
	)
	if exchange == trading.ExchangeBinance {
		if binanceStore == nil {
			writeError(w, http.StatusServiceUnavailable, "not_configured", "Binance credential store is not configured")
			return
		}
		resp, err = s.fetchBinancePositions(ctx, cfg, apiID, refresh)
	} else if exchange == trading.ExchangeGate {
		if gateStore == nil {
			writeError(w, http.StatusServiceUnavailable, "not_configured", "Gate credential store is not configured")
			return
		}
		resp, err = s.fetchGatePositions(ctx, cfg, apiID)
	} else {
		if okxStore == nil {
			writeError(w, http.StatusServiceUnavailable, "not_configured", "OKX credential store is not configured")
			return
		}
		resp, err = s.fetchPositions(ctx, cfg, apiID, instType, refresh)
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, "positions_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handlePendingOrders(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "only GET is allowed")
		return
	}
	exchange := trading.NormalizeExchange(r.URL.Query().Get("exchange"))
	instType := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("inst_type")))
	if instType == "" {
		instType = "SWAP"
	}
	apiID := strings.TrimSpace(r.URL.Query().Get("api_id"))
	cfg, err := configForRequestedTradeEnv(s.ConfigStore.Get(), r.URL.Query().Get("env"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_environment", err.Error())
		return
	}
	okxStore := s.okxCredentialStore(cfg.Trading.Env)
	binanceStore := s.binanceCredentialStore(cfg.Trading.Env)
	gateStore := s.gateCredentialStore(cfg.Trading.Env)
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	var (
		resp pendingOrdersResponse
	)
	if exchange == trading.ExchangeBinance {
		if binanceStore == nil {
			writeError(w, http.StatusServiceUnavailable, "not_configured", "Binance credential store is not configured")
			return
		}
		resp, err = s.fetchBinancePendingOrders(ctx, cfg, apiID)
	} else if exchange == trading.ExchangeGate {
		if gateStore == nil {
			writeError(w, http.StatusServiceUnavailable, "not_configured", "Gate credential store is not configured")
			return
		}
		resp, err = s.fetchGatePendingOrders(ctx, cfg, apiID)
	} else {
		if okxStore == nil {
			writeError(w, http.StatusServiceUnavailable, "not_configured", "OKX credential store is not configured")
			return
		}
		resp, err = s.fetchPendingOrders(ctx, cfg, apiID, instType)
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, "pending_orders_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handlePendingOrderChase(w http.ResponseWriter, r *http.Request) {
	s.handlePendingOrderChaseAction(w, r, true)
}

func (s *Server) handlePendingOrderChaseStop(w http.ResponseWriter, r *http.Request) {
	s.handlePendingOrderChaseAction(w, r, false)
}

func (s *Server) handlePendingOrderRisk(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "only POST is allowed")
		return
	}
	var req pendingOrderChaseRequest
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	req.normalize()
	if err := req.validate(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_pending_order_risk", err.Error())
		return
	}
	if req.OrderGroup == "algo" {
		writeError(w, http.StatusBadRequest, "invalid_pending_order_risk", "only normal pending orders can be rebuilt with attached risk controls")
		return
	}
	if req.Exchange == trading.ExchangeGate {
		writeError(w, http.StatusBadRequest, "unsupported_exchange_order", "Gate pending order risk rebuild is not supported")
		return
	}
	if req.Exchange == trading.ExchangeBinance {
		writeError(w, http.StatusBadRequest, "invalid_pending_order_risk", "Binance pending order risk rebuild is not supported")
		return
	}
	cfg, err := configForRequestedTradeEnv(s.ConfigStore.Get(), req.Env)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_pending_order_risk", err.Error())
		return
	}
	if s.okxCredentialStore(cfg.Trading.Env) == nil {
		writeError(w, http.StatusServiceUnavailable, "not_configured", "OKX credential store is not configured")
		return
	}
	client, apiID, err := s.okxClientForCredentials(cfg, req.APIID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "credentials_failed", err.Error())
		return
	}
	req.APIID = apiID
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	resp, err := rebuildPendingOrderRiskAtCurrentPrice(ctx, cfg, client, req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "pending_order_risk_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleGatePendingOrderCancel(w http.ResponseWriter, r *http.Request, cfg config.Config, req pendingOrderChaseRequest) {
	if req.OrderGroup == "algo" {
		writeError(w, http.StatusBadRequest, "unsupported_exchange_order", "Gate algorithm order cancellation is not supported")
		return
	}
	client, apiID, err := s.gateClientForCredentials(cfg, req.APIID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "credentials_failed", err.Error())
		return
	}
	if strings.TrimSpace(req.OrdID) == "" {
		writeError(w, http.StatusBadRequest, "invalid_pending_order_cancel", "Gate order id is required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	_, err = client.CancelOrder(ctx, req.InstID, req.OrdID)
	if err != nil {
		writeError(w, http.StatusBadGateway, "pending_order_cancel_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, pendingOrderChaseResponse{OK: true, Status: "canceled", APIID: apiID, OrderGroup: "normal", InstID: req.InstID, OrdID: req.OrdID, ClOrdID: req.ClOrdID})
}

func (s *Server) handlePendingOrderCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "only POST is allowed")
		return
	}
	var req pendingOrderChaseRequest
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	req.normalize()
	if err := req.validate(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_pending_order_cancel", err.Error())
		return
	}
	cfg, err := configForRequestedTradeEnv(s.ConfigStore.Get(), req.Env)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_pending_order_cancel", err.Error())
		return
	}
	if req.Exchange == trading.ExchangeGate {
		s.handleGatePendingOrderCancel(w, r, cfg, req)
		return
	}
	if req.OrderGroup == "algo" {
		if req.Exchange == trading.ExchangeBinance {
			s.handleBinancePendingAlgoOrderCancel(w, r, cfg, req)
			return
		}
		s.handleOKXPendingAlgoOrderCancel(w, r, cfg, req)
		return
	}
	if req.Exchange == trading.ExchangeBinance {
		s.handleBinancePendingOrderCancel(w, r, cfg, req)
		return
	}
	s.handleOKXPendingOrderCancel(w, r, cfg, req)
}

func (s *Server) handleOKXPendingOrderCancel(w http.ResponseWriter, r *http.Request, cfg config.Config, req pendingOrderChaseRequest) {
	if s.okxCredentialStore(cfg.Trading.Env) == nil {
		writeError(w, http.StatusServiceUnavailable, "not_configured", "OKX credential store is not configured")
		return
	}
	client, apiID, err := s.okxClientForCredentials(cfg, req.APIID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "credentials_failed", err.Error())
		return
	}
	req.APIID = apiID
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	order, found, err := currentPendingOrder(ctx, client, req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "pending_order_cancel_failed", err.Error())
		return
	}
	resp := pendingOrderChaseResponse{
		OK:         true,
		APIID:      apiID,
		OrderGroup: req.OrderGroup,
		InstID:     req.InstID,
		OrdID:      req.OrdID,
		ClOrdID:    req.ClOrdID,
	}
	if !found {
		pendingOrderChaseJobs.stop(pendingOrderChaseKey(req))
		resp.Status = "finished"
		resp.Message = "pending order is no longer open"
		writeJSON(w, http.StatusOK, resp)
		return
	}
	resp.OrdID = order.OrdID
	resp.ClOrdID = order.ClOrdID
	if err := cancelPendingOrder(ctx, client, order); err != nil {
		if _, stillOpen, checkErr := currentPendingOrder(ctx, client, req); checkErr == nil && !stillOpen {
			pendingOrderChaseJobs.stop(pendingOrderChaseKey(req))
			resp.Status = "finished"
			resp.Message = "pending order is no longer open"
			writeJSON(w, http.StatusOK, resp)
			return
		}
		writeError(w, http.StatusBadGateway, "pending_order_cancel_failed", err.Error())
		return
	}
	pendingOrderChaseJobs.stop(pendingOrderChaseKey(req))
	resp.Status = "canceled"
	resp.Message = "pending order canceled"
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleBinancePendingOrderCancel(w http.ResponseWriter, r *http.Request, cfg config.Config, req pendingOrderChaseRequest) {
	if s.binanceCredentialStore(cfg.Trading.Env) == nil {
		writeError(w, http.StatusServiceUnavailable, "not_configured", "Binance credential store is not configured")
		return
	}
	if !cfg.BinanceLiveTradingAllowedByEnvironment() {
		writeError(w, http.StatusForbidden, "live_trading_disabled", "Binance live trading is not allowed by environment")
		return
	}
	client, apiID, err := s.binanceClientForCredentials(cfg, req.APIID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "credentials_failed", err.Error())
		return
	}
	req.APIID = apiID
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	order, found, err := currentBinancePendingOrder(ctx, client, req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "pending_order_cancel_failed", err.Error())
		return
	}
	resp := pendingOrderChaseResponse{
		OK:         true,
		APIID:      apiID,
		OrderGroup: req.OrderGroup,
		InstID:     req.InstID,
		OrdID:      req.OrdID,
		ClOrdID:    req.ClOrdID,
	}
	if !found {
		pendingOrderChaseJobs.stop(pendingOrderChaseKey(req))
		resp.Status = "finished"
		resp.Message = "pending order is no longer open"
		writeJSON(w, http.StatusOK, resp)
		return
	}
	resp.OrdID = order.OrdID
	resp.ClOrdID = order.ClOrdID
	if err := cancelBinancePendingOrder(ctx, client, order); err != nil {
		if binancePendingOrderNoLongerOpen(err) {
			pendingOrderChaseJobs.stop(pendingOrderChaseKey(req))
			resp.Status = "finished"
			resp.Message = "pending order is no longer open"
			writeJSON(w, http.StatusOK, resp)
			return
		}
		writeError(w, http.StatusBadGateway, "pending_order_cancel_failed", err.Error())
		return
	}
	pendingOrderChaseJobs.stop(pendingOrderChaseKey(req))
	resp.Status = "canceled"
	resp.Message = "pending order canceled"
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleOKXPendingAlgoOrderCancel(w http.ResponseWriter, r *http.Request, cfg config.Config, req pendingOrderChaseRequest) {
	if s.okxCredentialStore(cfg.Trading.Env) == nil {
		writeError(w, http.StatusServiceUnavailable, "not_configured", "OKX credential store is not configured")
		return
	}
	client, apiID, err := s.okxClientForCredentials(cfg, req.APIID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "credentials_failed", err.Error())
		return
	}
	req.APIID = apiID
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	order, found, err := currentOKXPendingAlgoOrder(ctx, client, req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "pending_order_cancel_failed", err.Error())
		return
	}
	resp := pendingAlgoOrderResponse(req, apiID)
	if !found {
		pendingOrderChaseJobs.stop(pendingOrderChaseKey(req))
		resp.Status = "finished"
		resp.Message = "pending algo order is no longer open"
		writeJSON(w, http.StatusOK, resp)
		return
	}
	resp.AlgoID = strings.TrimSpace(order.AlgoID)
	resp.AlgoClOrdID = strings.TrimSpace(order.AlgoClOrdID)
	_, _, err = client.CancelAlgoOrders(ctx, []okx.CancelAlgoOrderRequest{{
		InstID:      strings.ToUpper(strings.TrimSpace(order.InstID)),
		AlgoID:      strings.TrimSpace(order.AlgoID),
		AlgoClOrdID: strings.TrimSpace(order.AlgoClOrdID),
	}})
	if err != nil {
		writeError(w, http.StatusBadGateway, "pending_order_cancel_failed", err.Error())
		return
	}
	pendingOrderChaseJobs.stop(pendingOrderChaseKey(req))
	resp.Status = "canceled"
	resp.Message = "pending algo order canceled"
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleBinancePendingAlgoOrderCancel(w http.ResponseWriter, r *http.Request, cfg config.Config, req pendingOrderChaseRequest) {
	if s.binanceCredentialStore(cfg.Trading.Env) == nil {
		writeError(w, http.StatusServiceUnavailable, "not_configured", "Binance credential store is not configured")
		return
	}
	if !cfg.BinanceLiveTradingAllowedByEnvironment() {
		writeError(w, http.StatusForbidden, "live_trading_disabled", "Binance live trading is not allowed by environment")
		return
	}
	client, apiID, err := s.binanceClientForCredentials(cfg, req.APIID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "credentials_failed", err.Error())
		return
	}
	req.APIID = apiID
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	order, found, err := currentBinancePendingAlgoOrder(ctx, client, req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "pending_order_cancel_failed", err.Error())
		return
	}
	resp := pendingAlgoOrderResponse(req, apiID)
	if !found {
		pendingOrderChaseJobs.stop(pendingOrderChaseKey(req))
		resp.Status = "finished"
		resp.Message = "pending algo order is no longer open"
		writeJSON(w, http.StatusOK, resp)
		return
	}
	resp.AlgoID = strconv.FormatInt(order.AlgoID, 10)
	resp.AlgoClOrdID = strings.TrimSpace(order.ClientAlgoID)
	if _, err := client.CancelAlgoOrder(ctx, order.AlgoID, strings.TrimSpace(order.ClientAlgoID)); err != nil {
		writeError(w, http.StatusBadGateway, "pending_order_cancel_failed", err.Error())
		return
	}
	pendingOrderChaseJobs.stop(pendingOrderChaseKey(req))
	resp.Status = "canceled"
	resp.Message = "pending algo order canceled"
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handlePendingOrderChaseAction(w http.ResponseWriter, r *http.Request, start bool) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "only POST is allowed")
		return
	}
	var req pendingOrderChaseRequest
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	req.normalize()
	if err := req.validate(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_pending_order_chase", err.Error())
		return
	}
	cfg, err := configForRequestedTradeEnv(s.ConfigStore.Get(), req.Env)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_pending_order_chase", err.Error())
		return
	}
	if req.Exchange == trading.ExchangeGate {
		writeError(w, http.StatusBadRequest, "unsupported_exchange_order", "Gate pending order chase is not supported")
		return
	}
	if req.OrderGroup == "algo" {
		if req.Exchange == trading.ExchangeBinance {
			s.handleBinancePendingAlgoOrderChaseAction(w, r, start, cfg, req)
			return
		}
		s.handleOKXPendingAlgoOrderChaseAction(w, r, start, cfg, req)
		return
	}
	if req.Exchange == trading.ExchangeBinance {
		s.handleBinancePendingOrderChaseAction(w, r, start, cfg, req)
		return
	}
	if s.okxCredentialStore(cfg.Trading.Env) == nil {
		writeError(w, http.StatusServiceUnavailable, "not_configured", "OKX credential store is not configured")
		return
	}
	client, apiID, err := s.okxClientForCredentials(cfg, req.APIID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "credentials_failed", err.Error())
		return
	}
	req.APIID = apiID
	key := pendingOrderChaseKey(req)
	if !start {
		stopped := pendingOrderChaseJobs.stop(key)
		status := "not_running"
		message := "pending order chase was not running"
		if stopped {
			status = "stopped"
			message = "pending order chase stopped"
		}
		writeJSON(w, http.StatusOK, pendingOrderChaseResponse{
			OK:      true,
			Status:  status,
			APIID:   apiID,
			InstID:  req.InstID,
			OrdID:   req.OrdID,
			ClOrdID: req.ClOrdID,
			Message: message,
		})
		return
	}
	if pendingOrderChaseJobs.activeKey(key) {
		writeJSON(w, http.StatusOK, pendingOrderChaseResponse{
			OK:      true,
			Status:  "running",
			APIID:   apiID,
			InstID:  req.InstID,
			OrdID:   req.OrdID,
			ClOrdID: req.ClOrdID,
			Message: "pending order chase is already running",
		})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	req, result, closed, err := preparePendingOrderChase(ctx, cfg, client, req)
	cancel()
	if err != nil {
		writeError(w, http.StatusBadGateway, "pending_order_chase_failed", err.Error())
		return
	}
	if closed {
		writeJSON(w, http.StatusConflict, pendingOrderChaseResponse{
			OK:      false,
			Status:  "finished",
			APIID:   apiID,
			InstID:  req.InstID,
			OrdID:   req.OrdID,
			ClOrdID: req.ClOrdID,
			Message: "pending order is no longer open",
		})
		return
	}
	key = pendingOrderChaseKey(req)
	chaseCtx, chaseCancel := context.WithCancel(context.Background())
	if !pendingOrderChaseJobs.start(key, chaseCancel) {
		chaseCancel()
		result.Status = "running"
		result.Message = "pending order chase is already running"
		writeJSON(w, http.StatusOK, result)
		return
	}
	go s.watchPendingOrderChase(chaseCtx, cfg, client, req)
	result.Status = "running"
	result.Message = "pending order chase started; market fallback after timeout"
	writeJSON(w, http.StatusAccepted, result)
}

func (s *Server) handleBinancePendingOrderChaseAction(w http.ResponseWriter, r *http.Request, start bool, cfg config.Config, req pendingOrderChaseRequest) {
	if s.binanceCredentialStore(cfg.Trading.Env) == nil {
		writeError(w, http.StatusServiceUnavailable, "not_configured", "Binance credential store is not configured")
		return
	}
	if !cfg.BinanceLiveTradingAllowedByEnvironment() {
		writeError(w, http.StatusForbidden, "live_trading_disabled", "Binance live trading is not allowed by environment")
		return
	}
	client, apiID, err := s.binanceClientForCredentials(cfg, req.APIID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "credentials_failed", err.Error())
		return
	}
	req.APIID = apiID
	key := pendingOrderChaseKey(req)
	if !start {
		stopped := pendingOrderChaseJobs.stop(key)
		status := "not_running"
		message := "pending order chase was not running"
		if stopped {
			status = "stopped"
			message = "pending order chase stopped"
		}
		writeJSON(w, http.StatusOK, pendingOrderChaseResponse{
			OK:      true,
			Status:  status,
			APIID:   apiID,
			InstID:  req.InstID,
			OrdID:   req.OrdID,
			ClOrdID: req.ClOrdID,
			Message: message,
		})
		return
	}
	if pendingOrderChaseJobs.activeKey(key) {
		writeJSON(w, http.StatusOK, pendingOrderChaseResponse{
			OK:      true,
			Status:  "running",
			APIID:   apiID,
			InstID:  req.InstID,
			OrdID:   req.OrdID,
			ClOrdID: req.ClOrdID,
			Message: "pending order chase is already running",
		})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	result, closed, err := prepareBinancePendingOrderChase(ctx, client, req)
	cancel()
	if err != nil {
		writeError(w, http.StatusBadGateway, "pending_order_chase_failed", err.Error())
		return
	}
	if closed {
		writeJSON(w, http.StatusConflict, pendingOrderChaseResponse{
			OK:      false,
			Status:  "finished",
			APIID:   apiID,
			InstID:  req.InstID,
			OrdID:   req.OrdID,
			ClOrdID: req.ClOrdID,
			Message: "pending order is no longer open",
		})
		return
	}
	chaseCtx, chaseCancel := context.WithCancel(context.Background())
	if !pendingOrderChaseJobs.start(key, chaseCancel) {
		chaseCancel()
		result.Status = "running"
		result.Message = "pending order chase is already running"
		writeJSON(w, http.StatusOK, result)
		return
	}
	go s.watchBinancePendingOrderChase(chaseCtx, client, req)
	result.Status = "running"
	result.Message = "pending order chase started; market fallback after timeout"
	writeJSON(w, http.StatusAccepted, result)
}

func (s *Server) handleOKXPendingAlgoOrderChaseAction(w http.ResponseWriter, r *http.Request, start bool, cfg config.Config, req pendingOrderChaseRequest) {
	if s.okxCredentialStore(cfg.Trading.Env) == nil {
		writeError(w, http.StatusServiceUnavailable, "not_configured", "OKX credential store is not configured")
		return
	}
	client, apiID, err := s.okxClientForCredentials(cfg, req.APIID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "credentials_failed", err.Error())
		return
	}
	req.APIID = apiID
	key := pendingOrderChaseKey(req)
	if !start {
		writeJSON(w, http.StatusOK, pendingOrderStopResponse(req, apiID, pendingOrderChaseJobs.stop(key)))
		return
	}
	if pendingOrderChaseJobs.activeKey(key) {
		resp := pendingAlgoOrderResponse(req, apiID)
		resp.Status = "running"
		resp.Message = "pending algo order chase is already running"
		writeJSON(w, http.StatusOK, resp)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	result, closed, err := chaseOKXPendingAlgoOrderOnce(ctx, cfg, client, req)
	cancel()
	if err != nil {
		writeError(w, http.StatusBadGateway, "pending_order_chase_failed", err.Error())
		return
	}
	if closed {
		writeJSON(w, http.StatusConflict, result)
		return
	}
	chaseCtx, chaseCancel := context.WithCancel(context.Background())
	if !pendingOrderChaseJobs.start(key, chaseCancel) {
		chaseCancel()
		result.Status = "running"
		result.Message = "pending algo order chase is already running"
		writeJSON(w, http.StatusOK, result)
		return
	}
	go s.watchOKXPendingAlgoOrderChase(chaseCtx, cfg, client, req)
	result.Status = "running"
	result.Message = "pending algo order chase started"
	writeJSON(w, http.StatusAccepted, result)
}

func (s *Server) handleBinancePendingAlgoOrderChaseAction(w http.ResponseWriter, r *http.Request, start bool, cfg config.Config, req pendingOrderChaseRequest) {
	if s.binanceCredentialStore(cfg.Trading.Env) == nil {
		writeError(w, http.StatusServiceUnavailable, "not_configured", "Binance credential store is not configured")
		return
	}
	if !cfg.BinanceLiveTradingAllowedByEnvironment() {
		writeError(w, http.StatusForbidden, "live_trading_disabled", "Binance live trading is not allowed by environment")
		return
	}
	client, apiID, err := s.binanceClientForCredentials(cfg, req.APIID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "credentials_failed", err.Error())
		return
	}
	req.APIID = apiID
	key := pendingOrderChaseKey(req)
	if !start {
		writeJSON(w, http.StatusOK, pendingOrderStopResponse(req, apiID, pendingOrderChaseJobs.stop(key)))
		return
	}
	if pendingOrderChaseJobs.activeKey(key) {
		resp := pendingAlgoOrderResponse(req, apiID)
		resp.Status = "running"
		resp.Message = "pending algo order chase is already running"
		writeJSON(w, http.StatusOK, resp)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	result, closed, nextReq, err := chaseBinancePendingAlgoOrderOnce(ctx, client, req)
	cancel()
	if err != nil {
		writeError(w, http.StatusBadGateway, "pending_order_chase_failed", err.Error())
		return
	}
	if closed {
		writeJSON(w, http.StatusConflict, result)
		return
	}
	chaseCtx, chaseCancel := context.WithCancel(context.Background())
	if !pendingOrderChaseJobs.start(pendingOrderChaseKey(nextReq), chaseCancel) {
		chaseCancel()
		result.Status = "running"
		result.Message = "pending algo order chase is already running"
		writeJSON(w, http.StatusOK, result)
		return
	}
	go s.watchBinancePendingAlgoOrderChase(chaseCtx, client, nextReq)
	result.Status = "running"
	result.Message = "pending algo order chase started"
	writeJSON(w, http.StatusAccepted, result)
}

func (s *Server) StartLowMarginPositionMonitor(ctx context.Context) {
	if s.ConfigStore == nil || (s.OKXCredentials == nil && s.OKXLiveCredentials == nil) {
		return
	}
	go s.runLowMarginPositionMonitor(ctx, lowMarginPositionCheckInterval)
}

func (s *Server) StartPositionMonitor(ctx context.Context) {
	if s.ConfigStore == nil {
		return
	}
	go s.runPositionMonitor(ctx)
}

func (s *Server) runPositionMonitor(ctx context.Context) {
	s.scanPositionMonitor(ctx)
	ticker := time.NewTicker(positionMonitorInterval(s.ConfigStore.Get()))
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.scanPositionMonitor(ctx)
			ticker.Reset(positionMonitorInterval(s.ConfigStore.Get()))
		}
	}
}

func positionMonitorInterval(cfg config.Config) time.Duration {
	interval := time.Duration(cfg.Trading.PositionMonitor.PollIntervalSeconds) * time.Second
	if interval <= 0 {
		return positionMonitorDefaultInterval
	}
	return interval
}

func (s *Server) scanPositionMonitor(ctx context.Context) {
	cfg := s.ConfigStore.Get()
	monitor := cfg.Trading.PositionMonitor
	if !monitor.OKXEnabled && !monitor.BinanceEnabled && !monitor.GateEnabled {
		return
	}
	if monitor.OKXEnabled {
		s.scanOKXPositionMonitor(ctx, cfg, monitor)
	}
	if monitor.BinanceEnabled {
		s.scanBinancePositionMonitor(ctx, cfg, monitor)
	}
	if monitor.GateEnabled {
		s.scanGatePositionMonitor(ctx, cfg, monitor)
	}
}

func (s *Server) scanOKXPositionMonitor(ctx context.Context, cfg config.Config, monitor config.PositionMonitorConfig) {
	if s.okxCredentialStore(cfg.Trading.Env) == nil {
		if s.Logger != nil {
			s.Logger.Warn("OKX position monitor skipped: credential store is not configured")
		}
		return
	}
	store := s.okxCredentialStore(cfg.Trading.Env)
	for _, requestedAPIID := range configuredAPIIDs(store.Status()) {
		scanCtx, cancel := context.WithTimeout(ctx, positionMonitorScanTimeout)
		err := s.scanOKXPositionMonitorForAPI(scanCtx, cfg, monitor, requestedAPIID)
		cancel()
		if err != nil && s.Logger != nil {
			s.Logger.Warn("failed to scan OKX positions for auto close", "api_id", requestedAPIID, "error", err)
		}
	}
}

func (s *Server) scanOKXPositionMonitorForAPI(ctx context.Context, cfg config.Config, monitor config.PositionMonitorConfig, requestedAPIID string) error {
	client, apiID, err := s.okxClientForCredentials(cfg, requestedAPIID)
	if err != nil {
		return err
	}
	positions, _, err := client.Positions(ctx, "SWAP")
	if err != nil {
		return err
	}
	for _, position := range openPositions(positions) {
		ratio, ok := positionMonitorUPLRatio(position)
		if !ok {
			continue
		}
		trigger, hit := positionMonitorTrigger(ratio, monitor)
		if !hit {
			continue
		}
		order, started, err := s.startLimitPositionClose(ctx, apiID, cfg, client, position, "")
		if err != nil {
			if s.Logger != nil {
				s.Logger.Warn("failed to start OKX auto position close", "api_id", apiID, "inst_id", position.InstID, "pos_side", position.PosSide, "trigger", trigger, "upl_ratio", ratio, "error", err)
			}
			continue
		}
		if started && trigger == "stop_loss" {
			detectedAt := s.now()
			eventOrderID := firstNonEmpty(order.Ack.OrdID, order.Ack.ClOrdID)
			if eventOrderID == "" {
				eventOrderID = detectedAt.Format(time.RFC3339Nano)
			}
			_, _, blockErr := s.recordLossCoinpairCooldown(
				cfg,
				strings.Join([]string{"position_monitor", trading.ExchangeOKX, apiID, position.InstID, position.PosSide, eventOrderID}, ":"),
				"position_monitor",
				trading.ExchangeOKX,
				apiID,
				firstNonEmpty(position.MarkPx, order.Px),
				detectedAt,
				position.InstID,
			)
			if blockErr != nil && s.Logger != nil {
				s.Logger.Error("failed to record OKX stop-loss coinpair cooldown", "api_id", apiID, "inst_id", position.InstID, "error", blockErr)
			}
		}
		if started && s.Logger != nil {
			s.Logger.Info("OKX auto position close started", "api_id", apiID, "inst_id", position.InstID, "pos_side", position.PosSide, "trigger", trigger, "upl_ratio", ratio, "px", order.Px, "ord_id", order.Ack.OrdID)
		}
	}
	return nil
}

func (s *Server) scanBinancePositionMonitor(ctx context.Context, cfg config.Config, monitor config.PositionMonitorConfig) {
	if s.binanceCredentialStore(cfg.Trading.Env) == nil {
		if s.Logger != nil {
			s.Logger.Warn("Binance position monitor skipped: credential store is not configured")
		}
		return
	}
	if !cfg.BinanceLiveTradingAllowedByEnvironment() {
		if s.Logger != nil {
			s.Logger.Warn("Binance position monitor skipped: live trading is not allowed by environment")
		}
		return
	}
	store := s.binanceCredentialStore(cfg.Trading.Env)
	for _, requestedAPIID := range configuredBinanceAPIIDs(store.Status()) {
		scanCtx, cancel := context.WithTimeout(ctx, positionMonitorScanTimeout)
		err := s.scanBinancePositionMonitorForAPI(scanCtx, cfg, monitor, requestedAPIID)
		cancel()
		if err != nil && s.Logger != nil {
			s.Logger.Warn("failed to scan Binance positions for auto close", "api_id", requestedAPIID, "error", err)
		}
	}
}

func (s *Server) scanBinancePositionMonitorForAPI(ctx context.Context, cfg config.Config, monitor config.PositionMonitorConfig, requestedAPIID string) error {
	client, apiID, err := s.binanceClientForCredentials(cfg, requestedAPIID)
	if err != nil {
		return err
	}
	positions, err := client.Positions(ctx, "")
	if err != nil {
		return err
	}
	for _, raw := range positions {
		position := binancePositionToOKX(raw)
		if !isOpenPosition(position.Pos) {
			continue
		}
		ratio, ok := positionMonitorUPLRatio(position)
		if !ok {
			continue
		}
		trigger, hit := positionMonitorTrigger(ratio, monitor)
		if !hit {
			continue
		}
		order, started, err := s.startBinanceLimitPositionClose(ctx, apiID, client, position, "")
		if err != nil {
			if s.Logger != nil {
				s.Logger.Warn("failed to start Binance auto position close", "api_id", apiID, "inst_id", position.InstID, "pos_side", position.PosSide, "trigger", trigger, "upl_ratio", ratio, "error", err)
			}
			continue
		}
		if started && trigger == "stop_loss" {
			detectedAt := s.now()
			eventOrderID := firstNonEmpty(order.Ack.OrdID, order.Ack.ClOrdID)
			if eventOrderID == "" {
				eventOrderID = detectedAt.Format(time.RFC3339Nano)
			}
			_, _, blockErr := s.recordLossCoinpairCooldown(
				cfg,
				strings.Join([]string{"position_monitor", trading.ExchangeBinance, apiID, position.InstID, position.PosSide, eventOrderID}, ":"),
				"position_monitor",
				trading.ExchangeBinance,
				apiID,
				firstNonEmpty(position.MarkPx, order.Px),
				detectedAt,
				position.InstID,
			)
			if blockErr != nil && s.Logger != nil {
				s.Logger.Error("failed to record Binance stop-loss coinpair cooldown", "api_id", apiID, "inst_id", position.InstID, "error", blockErr)
			}
		}
		if started && s.Logger != nil {
			s.Logger.Info("Binance auto position close started", "api_id", apiID, "inst_id", position.InstID, "pos_side", position.PosSide, "trigger", trigger, "upl_ratio", ratio, "px", order.Px, "ord_id", order.Ack.OrdID)
		}
	}
	return nil
}

func (s *Server) scanGatePositionMonitor(ctx context.Context, cfg config.Config, monitor config.PositionMonitorConfig) {
	if s.gateCredentialStore(cfg.Trading.Env) == nil {
		if s.Logger != nil {
			s.Logger.Warn("Gate position monitor skipped: credential store is not configured")
		}
		return
	}
	if !cfg.GateLiveTradingAllowedByEnvironment() {
		if s.Logger != nil {
			s.Logger.Warn("Gate position monitor skipped: live trading is not allowed by environment")
		}
		return
	}
	store := s.gateCredentialStore(cfg.Trading.Env)
	for _, requestedAPIID := range configuredGateAPIIDs(store.Status()) {
		scanCtx, cancel := context.WithTimeout(ctx, positionMonitorScanTimeout)
		err := s.scanGatePositionMonitorForAPI(scanCtx, cfg, monitor, requestedAPIID)
		cancel()
		if err != nil && s.Logger != nil {
			s.Logger.Warn("failed to scan Gate positions for auto close", "api_id", requestedAPIID, "error", err)
		}
	}
}

func (s *Server) scanGatePositionMonitorForAPI(ctx context.Context, cfg config.Config, monitor config.PositionMonitorConfig, requestedAPIID string) error {
	client, apiID, err := s.gateClientForCredentials(cfg, requestedAPIID)
	if err != nil {
		return err
	}
	positions, err := client.Positions(ctx, "")
	if err != nil {
		return err
	}
	for _, raw := range positions {
		position := gatePositionToOKX(raw)
		if !isOpenPosition(position.Pos) {
			continue
		}
		ratio, ok := positionMonitorUPLRatio(position)
		if !ok {
			continue
		}
		trigger, hit := positionMonitorTrigger(ratio, monitor)
		if !hit {
			continue
		}
		contract := firstNonEmpty(strings.TrimSpace(raw.Contract), position.InstID)
		if contract == "" {
			continue
		}
		ack, err := placeGateMarketPositionClose(ctx, cfg, client, contract)
		if err != nil {
			if s.Logger != nil {
				s.Logger.Warn("failed to submit Gate auto position close", "api_id", apiID, "inst_id", position.InstID, "pos_side", position.PosSide, "trigger", trigger, "upl_ratio", ratio, "error", err)
			}
			continue
		}
		if trigger == "stop_loss" {
			detectedAt := s.now()
			eventOrderID := firstNonEmpty(ack.IDString(), ack.Text)
			if eventOrderID == "" {
				eventOrderID = detectedAt.Format(time.RFC3339Nano)
			}
			_, _, blockErr := s.recordLossCoinpairCooldown(
				cfg,
				strings.Join([]string{"position_monitor", trading.ExchangeGate, apiID, position.InstID, position.PosSide, eventOrderID}, ":"),
				"position_monitor",
				trading.ExchangeGate,
				apiID,
				firstNonEmpty(position.MarkPx, raw.MarkPrice),
				detectedAt,
				position.InstID,
			)
			if blockErr != nil && s.Logger != nil {
				s.Logger.Error("failed to record Gate stop-loss coinpair cooldown", "api_id", apiID, "inst_id", position.InstID, "error", blockErr)
			}
		}
		if s.Logger != nil {
			s.Logger.Info("Gate auto position close submitted", "api_id", apiID, "inst_id", position.InstID, "pos_side", position.PosSide, "trigger", trigger, "upl_ratio", ratio, "ord_id", ack.IDString())
		}
	}
	return nil
}

func (s *Server) StartStalePendingOrderCancelMonitor(ctx context.Context) {
	if s.ConfigStore == nil {
		return
	}
	go s.runStalePendingOrderCancelMonitor(ctx, stalePendingOrderCancelInterval)
}

func (s *Server) runStalePendingOrderCancelMonitor(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = stalePendingOrderCancelInterval
	}
	s.cancelStalePendingOrders(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.cancelStalePendingOrders(ctx)
		}
	}
}

func (s *Server) cancelStalePendingOrders(ctx context.Context) {
	cfg := s.ConfigStore.Get()
	now := s.now()
	s.scanStaleOKXPendingOrders(ctx, cfg, now)
	s.scanStaleBinancePendingOrders(ctx, cfg, now)
}

func (s *Server) scanStaleOKXPendingOrders(ctx context.Context, cfg config.Config, now time.Time) {
	if s.okxCredentialStore(cfg.Trading.Env) == nil {
		return
	}
	store := s.okxCredentialStore(cfg.Trading.Env)
	for _, requestedAPIID := range configuredAPIIDs(store.Status()) {
		scanCtx, cancel := context.WithTimeout(ctx, positionMonitorScanTimeout)
		err := s.cancelStaleOKXPendingOrdersForAPI(scanCtx, cfg, requestedAPIID, now)
		cancel()
		if err != nil && s.Logger != nil {
			s.Logger.Warn("failed to scan OKX stale pending orders", "api_id", requestedAPIID, "error", err)
		}
	}
}

func (s *Server) cancelStaleOKXPendingOrdersForAPI(ctx context.Context, cfg config.Config, requestedAPIID string, now time.Time) error {
	client, apiID, err := s.okxClientForCredentials(cfg, requestedAPIID)
	if err != nil {
		return err
	}
	orders, _, err := client.PendingOrders(ctx, "SWAP")
	if err != nil {
		return err
	}
	for _, order := range orders {
		age, stale := staleUnfilledPendingOrderAge(order, now, stalePendingOrderCancelAfter)
		if !stale {
			continue
		}
		req := pendingOrderChaseRequest{
			Exchange:   trading.ExchangeOKX,
			APIID:      apiID,
			OrderGroup: "normal",
			InstID:     order.InstID,
			OrdID:      order.OrdID,
			ClOrdID:    order.ClOrdID,
		}
		if err := cancelPendingOrder(ctx, client, order); err != nil {
			if _, stillOpen, checkErr := currentPendingOrder(ctx, client, req); checkErr == nil && !stillOpen {
				pendingOrderChaseJobs.stop(pendingOrderChaseKey(req))
				if s.Logger != nil {
					s.Logger.Info("OKX stale pending order already closed", "api_id", apiID, "inst_id", order.InstID, "ord_id", order.OrdID, "cl_ord_id", order.ClOrdID, "age", age.String())
				}
				continue
			}
			if s.Logger != nil {
				s.Logger.Warn("failed to cancel OKX stale pending order", "api_id", apiID, "inst_id", order.InstID, "ord_id", order.OrdID, "cl_ord_id", order.ClOrdID, "age", age.String(), "error", err)
			}
			continue
		}
		pendingOrderChaseJobs.stop(pendingOrderChaseKey(req))
		if s.Logger != nil {
			s.Logger.Info("OKX stale pending order canceled", "api_id", apiID, "inst_id", order.InstID, "ord_id", order.OrdID, "cl_ord_id", order.ClOrdID, "age", age.String())
		}
	}
	return nil
}

func (s *Server) scanStaleBinancePendingOrders(ctx context.Context, cfg config.Config, now time.Time) {
	if s.binanceCredentialStore(cfg.Trading.Env) == nil {
		return
	}
	store := s.binanceCredentialStore(cfg.Trading.Env)
	ids := configuredBinanceAPIIDs(store.Status())
	if len(ids) == 0 {
		return
	}
	if !cfg.BinanceLiveTradingAllowedByEnvironment() {
		if s.Logger != nil {
			s.Logger.Warn("Binance stale pending order cancel skipped: live trading is not allowed by environment")
		}
		return
	}
	for _, requestedAPIID := range ids {
		scanCtx, cancel := context.WithTimeout(ctx, positionMonitorScanTimeout)
		err := s.cancelStaleBinancePendingOrdersForAPI(scanCtx, cfg, requestedAPIID, now)
		cancel()
		if err != nil && s.Logger != nil {
			s.Logger.Warn("failed to scan Binance stale pending orders", "api_id", requestedAPIID, "error", err)
		}
	}
}

func (s *Server) cancelStaleBinancePendingOrdersForAPI(ctx context.Context, cfg config.Config, requestedAPIID string, now time.Time) error {
	client, apiID, err := s.binanceClientForCredentials(cfg, requestedAPIID)
	if err != nil {
		return err
	}
	orders, err := client.OpenOrders(ctx, "")
	if err != nil {
		return err
	}
	for _, rawOrder := range orders {
		order := binanceOpenOrderToOKX(rawOrder)
		age, stale := staleUnfilledPendingOrderAge(order, now, stalePendingOrderCancelAfter)
		if !stale {
			continue
		}
		req := pendingOrderChaseRequest{
			Exchange:   trading.ExchangeBinance,
			APIID:      apiID,
			OrderGroup: "normal",
			InstID:     order.InstID,
			OrdID:      order.OrdID,
			ClOrdID:    order.ClOrdID,
		}
		if err := cancelBinancePendingOrder(ctx, client, order); err != nil {
			if binancePendingOrderNoLongerOpen(err) {
				pendingOrderChaseJobs.stop(pendingOrderChaseKey(req))
				if s.Logger != nil {
					s.Logger.Info("Binance stale pending order already closed", "api_id", apiID, "symbol", order.InstID, "ord_id", order.OrdID, "cl_ord_id", order.ClOrdID, "age", age.String())
				}
				continue
			}
			if s.Logger != nil {
				s.Logger.Warn("failed to cancel Binance stale pending order", "api_id", apiID, "symbol", order.InstID, "ord_id", order.OrdID, "cl_ord_id", order.ClOrdID, "age", age.String(), "error", err)
			}
			continue
		}
		pendingOrderChaseJobs.stop(pendingOrderChaseKey(req))
		if s.Logger != nil {
			s.Logger.Info("Binance stale pending order canceled", "api_id", apiID, "symbol", order.InstID, "ord_id", order.OrdID, "cl_ord_id", order.ClOrdID, "age", age.String())
		}
	}
	return nil
}

func staleUnfilledPendingOrderAge(order okx.PendingOrder, now time.Time, maxAge time.Duration) (time.Duration, bool) {
	if maxAge <= 0 || !pendingOrderUnfilled(order) {
		return 0, false
	}
	createdAt, ok := pendingOrderCreatedAt(order)
	if !ok {
		return 0, false
	}
	age := now.UTC().Sub(createdAt)
	if age < maxAge || age < 0 {
		return age, false
	}
	return age, true
}

func pendingOrderCreatedAt(order okx.PendingOrder) (time.Time, bool) {
	if createdAt, ok := exchangeMillisTime(strings.TrimSpace(order.CTime)); ok {
		return createdAt, true
	}
	return exchangeMillisTime(strings.TrimSpace(order.UTime))
}

func exchangeMillisTime(raw string) (time.Time, bool) {
	if raw == "" {
		return time.Time{}, false
	}
	if ms, err := strconv.ParseInt(raw, 10, 64); err == nil {
		if ms <= 0 {
			return time.Time{}, false
		}
		return time.UnixMilli(ms).UTC(), true
	}
	if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return parsed.UTC(), true
	}
	return time.Time{}, false
}

func pendingOrderUnfilled(order okx.PendingOrder) bool {
	raw := strings.TrimSpace(order.AccFillSz)
	if raw == "" {
		return false
	}
	filled, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(filled) || math.IsInf(filled, 0) {
		return false
	}
	return math.Abs(filled) <= 1e-12
}

func positionMonitorUPLRatio(position okx.Position) (float64, bool) {
	return positionReturnRatio(position)
}

func positionMonitorTrigger(ratio float64, monitor config.PositionMonitorConfig) (string, bool) {
	if monitor.TakeProfitPct > 0 && ratio >= monitor.TakeProfitPct/100 {
		return "take_profit", true
	}
	if monitor.StopLossPct > 0 && ratio <= -monitor.StopLossPct/100 {
		return "stop_loss", true
	}
	return "", false
}

func (s *Server) runLowMarginPositionMonitor(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = lowMarginPositionCheckInterval
	}
	s.closeLowMarginPositions(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.closeLowMarginPositions(ctx)
		}
	}
}

func (s *Server) closeLowMarginPositions(ctx context.Context) {
	cfg := s.ConfigStore.Get()
	store := s.okxCredentialStore(cfg.Trading.Env)
	if store == nil {
		return
	}
	ids := configuredAPIIDs(store.Status())
	if len(ids) == 0 {
		return
	}
	for _, requestedAPIID := range ids {
		scanCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		err := s.closeLowMarginPositionsForAPI(scanCtx, cfg, requestedAPIID)
		cancel()
		if err != nil && s.Logger != nil {
			s.Logger.Warn("failed to scan low margin positions", "api_id", requestedAPIID, "error", err)
		}
	}
}

func (s *Server) closeLowMarginPositionsForAPI(ctx context.Context, cfg config.Config, requestedAPIID string) error {
	client, apiID, err := s.okxClientForCredentials(cfg, requestedAPIID)
	if err != nil {
		return err
	}
	positions, _, err := client.Positions(ctx, "SWAP")
	if err != nil {
		return err
	}
	for _, position := range openPositions(positions) {
		margin, low := lowMarginPosition(position, lowMarginPositionThresholdUSDT)
		if !low {
			continue
		}
		orderCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		order, started, err := s.startLimitPositionClose(orderCtx, apiID, cfg, client, position, "")
		cancel()
		if err != nil {
			if s.Logger != nil {
				s.Logger.Warn("failed to start low margin position close", "api_id", apiID, "inst_id", position.InstID, "pos_side", position.PosSide, "margin", margin, "error", err)
			}
			continue
		}
		if started && s.Logger != nil {
			s.Logger.Info("low margin position close started", "api_id", apiID, "inst_id", position.InstID, "pos_side", position.PosSide, "margin", margin, "px", order.Px, "ord_id", order.Ack.OrdID)
		}
	}
	return nil
}

// StartAutoProfitPositionCloseMonitor closes profitable OKX positions with a
// repriced limit order before falling back to a market order.
func (s *Server) StartAutoProfitPositionCloseMonitor(ctx context.Context) {
	if s.ConfigStore == nil || (s.OKXCredentials == nil && s.OKXLiveCredentials == nil) {
		return
	}
	go s.runAutoProfitPositionCloseMonitor(ctx, autoProfitPositionCheckInterval)
}

func (s *Server) runAutoProfitPositionCloseMonitor(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = autoProfitPositionCheckInterval
	}
	s.closeProfitablePositions(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.closeProfitablePositions(ctx)
		}
	}
}

func (s *Server) closeProfitablePositions(ctx context.Context) {
	cfg := s.ConfigStore.Get()
	store := s.okxCredentialStore(cfg.Trading.Env)
	if store == nil {
		return
	}
	ids := configuredAPIIDs(store.Status())
	if len(ids) == 0 {
		return
	}
	for _, requestedAPIID := range ids {
		scanCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		err := s.closeProfitablePositionsForAPI(scanCtx, cfg, requestedAPIID)
		cancel()
		if err != nil && s.Logger != nil {
			s.Logger.Warn("failed to scan profitable positions", "api_id", requestedAPIID, "error", err)
		}
	}
}

func (s *Server) closeProfitablePositionsForAPI(ctx context.Context, cfg config.Config, requestedAPIID string) error {
	client, apiID, err := s.okxClientForCredentials(cfg, requestedAPIID)
	if err != nil {
		return err
	}
	positions, _, err := client.Positions(ctx, "SWAP")
	if err != nil {
		return err
	}
	for _, position := range openPositions(positions) {
		returnRatio, ok := positionReturnRatio(position)
		if !ok || returnRatio <= autoProfitPositionReturnThreshold {
			continue
		}
		orderCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		order, started, err := s.startAutoProfitLimitPositionClose(orderCtx, apiID, cfg, client, position)
		cancel()
		if err != nil {
			if s.Logger != nil {
				s.Logger.Warn("failed to start profitable position close", "api_id", apiID, "inst_id", position.InstID, "pos_side", position.PosSide, "return_ratio", returnRatio, "error", err)
			}
			continue
		}
		if started && s.Logger != nil {
			s.Logger.Info("profitable position limit close started", "api_id", apiID, "inst_id", position.InstID, "pos_side", position.PosSide, "return_ratio", returnRatio, "px", order.Px, "ord_id", order.Ack.OrdID)
		}
	}
	return nil
}

func positionReturnRatio(position okx.Position) (float64, bool) {
	if ratio, err := strconv.ParseFloat(strings.TrimSpace(position.UplRatio), 64); err == nil && !math.IsNaN(ratio) && !math.IsInf(ratio, 0) {
		return ratio, true
	}
	upl, uplErr := strconv.ParseFloat(strings.TrimSpace(position.Upl), 64)
	margin, marginErr := strconv.ParseFloat(strings.TrimSpace(position.Margin), 64)
	if uplErr != nil || marginErr != nil || margin == 0 || math.IsNaN(upl) || math.IsNaN(margin) || math.IsInf(upl, 0) || math.IsInf(margin, 0) {
		return 0, false
	}
	return upl / math.Abs(margin), true
}

func (s *Server) handlePositionClose(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "only POST is allowed")
		return
	}
	var req positionCloseRequest
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	req.Exchange = trading.NormalizeExchange(req.Exchange)
	var err error
	req.Env, err = normalizeRequestedTradeEnv(req.Env)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_position_close", err.Error())
		return
	}
	req.APIID = strings.TrimSpace(req.APIID)
	req.InstID = strings.ToUpper(strings.TrimSpace(req.InstID))
	req.PosSide = normalizePosSide(req.PosSide)
	req.Mode = strings.ToLower(strings.TrimSpace(req.Mode))
	if req.InstID == "" {
		writeError(w, http.StatusBadRequest, "invalid_position_close", "inst_id is required")
		return
	}
	if req.Mode != "market" && req.Mode != "limit" {
		writeError(w, http.StatusBadRequest, "invalid_position_close", "mode must be market or limit")
		return
	}
	ratio, err := positionCloseRatio(req.Ratio)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_position_close", err.Error())
		return
	}
	cfg, err := configForRequestedTradeEnv(s.ConfigStore.Get(), req.Env)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_position_close", err.Error())
		return
	}
	if req.Exchange == trading.ExchangeGate {
		s.handleGatePositionClose(w, r, cfg, req, ratio)
		return
	}
	if req.Exchange == trading.ExchangeBinance {
		s.handleBinancePositionClose(w, r, cfg, req, ratio)
		return
	}
	if s.okxCredentialStore(cfg.Trading.Env) == nil {
		writeError(w, http.StatusServiceUnavailable, "not_configured", "OKX credential store is not configured")
		return
	}
	client, apiID, err := s.okxClientForCredentials(cfg, req.APIID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "credentials_failed", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	position, err := currentOpenPosition(ctx, client, req.InstID, req.PosSide)
	if err != nil {
		if errors.Is(err, errPositionNotOpen) {
			writeError(w, http.StatusConflict, "position_not_open", err.Error())
			return
		}
		writeError(w, http.StatusBadGateway, "positions_failed", err.Error())
		return
	}
	closeSz := ""
	if ratio < 1 {
		closeSz, err = okxPositionCloseSize(ctx, client, position, ratio)
		if err != nil {
			writeError(w, http.StatusBadGateway, "position_close_failed", err.Error())
			return
		}
	}
	switch req.Mode {
	case "market":
		order, err := placeMarketPositionClose(ctx, cfg, client, position, closeSz)
		if err != nil {
			writeError(w, http.StatusBadGateway, "position_close_failed", err.Error())
			return
		}
		message := "market close order submitted"
		if order.Px != "" {
			message = fmt.Sprintf("market close converted to a protected limit close order at %s", order.Px)
		}
		writeJSON(w, http.StatusOK, positionCloseResponse{
			OK:      true,
			Status:  "submitted",
			Mode:    req.Mode,
			APIID:   apiID,
			InstID:  order.Position.InstID,
			PosSide: normalizePosSide(order.Position.PosSide),
			Sz:      order.CloseSz,
			OrdID:   order.Ack.OrdID,
			ClOrdID: order.Ack.ClOrdID,
			Px:      order.Px,
			Message: message,
		})
	case "limit":
		order, started, err := s.startLimitPositionClose(ctx, apiID, cfg, client, position, closeSz)
		if err != nil {
			writeError(w, http.StatusBadGateway, "position_close_failed", err.Error())
			return
		}
		if !started {
			writeError(w, http.StatusConflict, "position_close_running", "limit close is already running for this position")
			return
		}
		writeJSON(w, http.StatusAccepted, positionCloseResponse{
			OK:      true,
			Status:  "running",
			Mode:    req.Mode,
			APIID:   apiID,
			InstID:  order.Position.InstID,
			PosSide: normalizePosSide(order.Position.PosSide),
			Sz:      order.CloseSz,
			OrdID:   order.Ack.OrdID,
			ClOrdID: order.Ack.ClOrdID,
			Px:      order.Px,
			Message: "limit close order started",
		})
	}
}

func (s *Server) handleGatePositionClose(w http.ResponseWriter, r *http.Request, cfg config.Config, req positionCloseRequest, ratio float64) {
	if req.Mode == "market" && ratio < 1 {
		writeError(w, http.StatusBadRequest, "unsupported_gate_position_close", "Gate currently supports full market close only")
		return
	}
	if s.gateCredentialStore(cfg.Trading.Env) == nil {
		writeError(w, http.StatusServiceUnavailable, "not_configured", "Gate credential store is not configured")
		return
	}
	client, apiID, err := s.gateClientForCredentials(cfg, req.APIID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "credentials_failed", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	positions, err := client.Positions(ctx, req.InstID)
	if err != nil {
		writeError(w, http.StatusBadGateway, "positions_failed", err.Error())
		return
	}
	var found gate.Position
	for _, candidate := range positions {
		converted := gatePositionToOKX(candidate)
		if strings.EqualFold(converted.InstID, req.InstID) && isOpenPosition(converted.Pos) && (req.PosSide == "" || normalizePosSide(converted.PosSide) == req.PosSide) {
			found = candidate
			break
		}
	}
	if strings.TrimSpace(found.Contract) == "" {
		writeError(w, http.StatusConflict, "position_not_open", "Gate position is not open")
		return
	}
	converted := gatePositionToOKX(found)
	if req.Mode == "limit" {
		closeSz := ""
		var closeErr error
		if ratio < 1 {
			closeSz, closeErr = gatePositionCloseSize(ctx, client, converted, ratio)
			if closeErr != nil {
				writeError(w, http.StatusBadGateway, "position_close_failed", closeErr.Error())
				return
			}
		}
		order, started, startErr := s.startGateLimitPositionClose(ctx, apiID, cfg, client, converted, closeSz)
		if startErr != nil {
			writeError(w, http.StatusBadGateway, "position_close_failed", startErr.Error())
			return
		}
		if !started {
			writeError(w, http.StatusConflict, "position_close_running", "limit close is already running for this position")
			return
		}
		writeJSON(w, http.StatusAccepted, positionCloseResponse{
			OK: true, Status: "running", Mode: req.Mode, APIID: apiID,
			InstID: order.Position.InstID, PosSide: normalizePosSide(order.Position.PosSide),
			Sz: order.CloseSz, OrdID: order.Order.IDString(), ClOrdID: order.Order.Text,
			Px: order.Px, Message: "Gate limit close order started",
		})
		return
	}
	ack, err := placeGateMarketPositionClose(ctx, cfg, client, found.Contract)
	if err != nil {
		writeError(w, http.StatusBadGateway, "position_close_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, positionCloseResponse{
		OK: true, Status: "submitted", Mode: "market", APIID: apiID, InstID: converted.InstID,
		PosSide: normalizePosSide(converted.PosSide), Sz: absolutePositionSize(converted.Pos), OrdID: ack.IDString(),
		ClOrdID: ack.Text, Message: "Gate market close order submitted",
	})
}

func placeGateMarketPositionClose(ctx context.Context, cfg config.Config, client gate.Client, contract string) (gate.Order, error) {
	return client.PlaceOrder(ctx, gate.PlaceOrderRequest{
		Contract: contract, Size: "0", Price: "0", TIF: "ioc", Close: true, ReduceOnly: true,
		PosMarginMode: cfg.MarginMode(), Text: nextGatePositionCloseText(),
	})
}

func (s *Server) startGateLimitPositionClose(ctx context.Context, apiID string, cfg config.Config, client gate.Client, position okx.Position, closeSz string) (gatePositionCloseOrder, bool, error) {
	key := positionCloseKey(trading.ExchangeGate, apiID, position.InstID, position.PosSide)
	if !positionCloseJobs.start(key) {
		return gatePositionCloseOrder{}, false, nil
	}
	order, err := placeGateLimitPositionClose(ctx, cfg, client, position, closeSz)
	if err != nil {
		positionCloseJobs.done(key)
		return gatePositionCloseOrder{}, false, err
	}
	go s.watchGateLimitPositionClose(apiID, cfg, client, order)
	return order, true, nil
}

func gatePositionCloseSize(ctx context.Context, client gate.Client, position okx.Position, ratio float64) (string, error) {
	contract, err := client.Contract(ctx, position.InstID)
	if err != nil {
		return "", err
	}
	step := ""
	if !contract.EnableDecimal {
		step = "1"
	}
	size, err := positionCloseSize(position.Pos, ratio, step, contract.OrderSizeMin)
	if err != nil {
		return "", err
	}
	return gateSignedCloseSize(position.Pos, size), nil
}

func gateSignedCloseSize(positionRaw, absoluteSize string) string {
	size := absolutePositionSize(absoluteSize)
	if size == "" || size == "0" {
		return ""
	}
	value, err := strconv.ParseFloat(strings.TrimSpace(positionRaw), 64)
	if err == nil && value > 0 {
		return "-" + size
	}
	return size
}

func gateLimitClosePrice(ctx context.Context, client gate.Client, position okx.Position) (string, error) {
	contract, err := client.Contract(ctx, position.InstID)
	if err != nil {
		return "", err
	}
	ticker, err := client.Ticker(ctx, position.InstID)
	if err != nil {
		return "", err
	}
	bid, bidErr := strconv.ParseFloat(strings.TrimSpace(ticker.HighestBid), 64)
	ask, askErr := strconv.ParseFloat(strings.TrimSpace(ticker.LowestAsk), 64)
	if bidErr != nil || askErr != nil || bid <= 0 || ask <= 0 {
		last, lastErr := strconv.ParseFloat(strings.TrimSpace(ticker.Last), 64)
		if lastErr != nil || last <= 0 {
			last, lastErr = strconv.ParseFloat(strings.TrimSpace(contract.MarkPrice), 64)
			if lastErr != nil || last <= 0 {
				return "", fmt.Errorf("invalid Gate ticker bid/ask for %s", position.InstID)
			}
		}
		bid, ask = last, last
	}
	side, err := closeOrderSide(position)
	if err != nil {
		return "", err
	}
	return priceOneTickFromMidValue((bid+ask)/2, contract.OrderPriceRound, side)
}

func placeGateLimitPositionClose(ctx context.Context, cfg config.Config, client gate.Client, position okx.Position, closeSz string) (gatePositionCloseOrder, error) {
	px, err := gateLimitClosePrice(ctx, client, position)
	if err != nil {
		return gatePositionCloseOrder{}, err
	}
	marginMode := strings.ToLower(strings.TrimSpace(position.MgnMode))
	if marginMode != "cross" && marginMode != "isolated" {
		marginMode = cfg.MarginMode()
	}
	req := gate.PlaceOrderRequest{
		Contract:      strings.ToUpper(strings.TrimSpace(position.InstID)),
		Size:          "0",
		Price:         px,
		TIF:           "gtc",
		Close:         true,
		ReduceOnly:    true,
		PosMarginMode: marginMode,
		Text:          nextGatePositionCloseText(),
	}
	if strings.TrimSpace(closeSz) != "" {
		req.Size = strings.TrimSpace(closeSz)
		req.Close = false
	}
	ack, err := client.PlaceOrder(ctx, req)
	if err != nil {
		return gatePositionCloseOrder{}, err
	}
	closeSize := absolutePositionSize(req.Size)
	if closeSize == "" || closeSize == "0" {
		closeSize = absolutePositionSize(position.Pos)
	}
	return gatePositionCloseOrder{
		Position:       position,
		Order:          ack,
		Px:             px,
		CloseSz:        closeSize,
		TargetSz:       closeSize,
		InitialPosSize: absolutePositionSize(position.Pos),
		Partial:        strings.TrimSpace(closeSz) != "",
	}, nil
}

func currentGateOpenPosition(ctx context.Context, client gate.Client, instID, posSide string) (gate.Position, okx.Position, error) {
	positions, err := client.Positions(ctx, instID)
	if err != nil {
		return gate.Position{}, okx.Position{}, err
	}
	wantedSide := normalizePosSide(posSide)
	for _, candidate := range positions {
		converted := gatePositionToOKX(candidate)
		if !strings.EqualFold(converted.InstID, strings.TrimSpace(instID)) || !isOpenPosition(converted.Pos) {
			continue
		}
		if wantedSide != "" && normalizePosSide(converted.PosSide) != wantedSide {
			continue
		}
		return candidate, converted, nil
	}
	return gate.Position{}, okx.Position{}, fmt.Errorf("%w: %s %s", errPositionNotOpen, instID, posSide)
}

func currentGatePositionCloseOrder(ctx context.Context, client gate.Client, active gatePositionCloseOrder) (gate.Order, bool, error) {
	orders, err := client.OpenOrders(ctx, active.Position.InstID)
	if err != nil {
		return gate.Order{}, false, err
	}
	wantedID := active.Order.IDString()
	wantedText := strings.TrimSpace(active.Order.Text)
	for _, order := range orders {
		if !strings.EqualFold(strings.TrimSpace(order.Contract), strings.TrimSpace(active.Position.InstID)) {
			continue
		}
		if (wantedID != "" && order.IDString() == wantedID) || (wantedText != "" && strings.TrimSpace(order.Text) == wantedText) {
			return order, true, nil
		}
	}
	return gate.Order{}, false, nil
}

func cancelGatePositionCloseOrder(ctx context.Context, client gate.Client, order gate.Order) error {
	orderID := order.IDString()
	if orderID == "" {
		return errors.New("Gate close order is missing an order id")
	}
	_, err := client.CancelOrder(ctx, order.Contract, orderID)
	return err
}

func gateOrderRemainingSize(order gate.Order) (string, error) {
	raw := strings.TrimSpace(order.Left)
	if raw == "" {
		raw = strings.TrimSpace(order.Size)
	}
	remaining := absolutePositionSize(raw)
	if remaining == "" || remaining == "0" {
		return "", errPendingOrderNoRemaining
	}
	return remaining, nil
}

func gatePartialCloseRemaining(active gatePositionCloseOrder, position okx.Position) string {
	target, targetErr := positiveRatFromDecimal(active.TargetSz)
	initial, initialErr := positiveRatFromDecimal(active.InitialPosSize)
	current, currentErr := positiveRatFromDecimal(absolutePositionSize(position.Pos))
	if targetErr != nil || initialErr != nil || currentErr != nil || target.Sign() <= 0 || current.Sign() <= 0 {
		return ""
	}
	filled := new(big.Rat).Sub(initial, current)
	if filled.Sign() < 0 {
		filled.SetInt64(0)
	}
	remaining := new(big.Rat).Sub(target, filled)
	if remaining.Sign() <= 0 {
		return ""
	}
	if remaining.Cmp(current) > 0 {
		remaining.Set(current)
	}
	decimals := maxInt(decimalPlacesForDisplay(active.TargetSz), decimalPlacesForDisplay(position.Pos))
	return trimDecimalZeros(remaining.FloatString(decimals))
}

func (s *Server) watchGateLimitPositionClose(apiID string, cfg config.Config, client gate.Client, active gatePositionCloseOrder) {
	key := positionCloseKey(trading.ExchangeGate, apiID, active.Position.InstID, active.Position.PosSide)
	defer positionCloseJobs.done(key)

	pollInterval := positionClosePollInterval
	if pollInterval <= 0 {
		pollInterval = 250 * time.Millisecond
	}
	timeoutDuration := positionCloseLimitTimeout
	if timeoutDuration <= 0 {
		timeoutDuration = 60 * time.Second
	}
	poll := time.NewTicker(pollInterval)
	defer poll.Stop()
	timeout := time.NewTimer(timeoutDuration)
	defer timeout.Stop()
	for {
		select {
		case <-poll.C:
			next, closed, err := refreshGateLimitPositionClose(cfg, client, active)
			if err != nil {
				s.logPositionCloseError("Gate limit position close refresh failed", err, active.Position)
				continue
			}
			if closed {
				return
			}
			active = next
		case <-timeout.C:
			if err := fallbackGateMarketPositionClose(cfg, client, active); err != nil {
				s.logPositionCloseError("Gate limit position close fallback failed", err, active.Position)
			}
			return
		}
	}
}

func refreshGateLimitPositionClose(cfg config.Config, client gate.Client, active gatePositionCloseOrder) (gatePositionCloseOrder, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	_, position, err := currentGateOpenPosition(ctx, client, active.Position.InstID, active.Position.PosSide)
	if err != nil {
		if errors.Is(err, errPositionNotOpen) {
			return active, true, nil
		}
		return active, false, err
	}
	remaining := active.CloseSz
	if active.Partial {
		remaining = gatePartialCloseRemaining(active, position)
		if remaining == "" || remaining == "0" {
			return active, true, nil
		}
	}
	openOrder, found, err := currentGatePositionCloseOrder(ctx, client, active)
	if err != nil {
		return active, false, err
	}
	if found && active.Partial {
		remaining, err = gateOrderRemainingSize(openOrder)
		if err != nil {
			if errors.Is(err, errPendingOrderNoRemaining) {
				return active, true, nil
			}
			return active, false, err
		}
		remaining = capCloseSizeToPosition(position.Pos, remaining)
		if remaining == "" || remaining == "0" {
			return active, true, nil
		}
	}
	nextPx, err := gateLimitClosePrice(ctx, client, position)
	if err != nil {
		return active, false, err
	}
	if found && nextPx == active.Px {
		active.Position = position
		active.CloseSz = remaining
		return active, false, nil
	}
	if found {
		if err := cancelGatePositionCloseOrder(ctx, client, openOrder); err != nil {
			if _, _, checkErr := currentGateOpenPosition(ctx, client, active.Position.InstID, active.Position.PosSide); errors.Is(checkErr, errPositionNotOpen) {
				return active, true, nil
			}
			return active, false, err
		}
	}
	closeSz := ""
	if active.Partial {
		closeSz = gateSignedCloseSize(position.Pos, remaining)
	}
	next, err := placeGateLimitPositionClose(ctx, cfg, client, position, closeSz)
	if err != nil {
		return active, false, err
	}
	next.TargetSz = active.TargetSz
	next.InitialPosSize = active.InitialPosSize
	return next, false, nil
}

func fallbackGateMarketPositionClose(cfg config.Config, client gate.Client, active gatePositionCloseOrder) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	openOrder, found, err := currentGatePositionCloseOrder(ctx, client, active)
	if err != nil {
		return err
	}
	remaining := ""
	if found && active.Partial {
		remaining, err = gateOrderRemainingSize(openOrder)
		if err != nil && !errors.Is(err, errPendingOrderNoRemaining) {
			return err
		}
	}
	if found {
		if err := cancelGatePositionCloseOrder(ctx, client, openOrder); err != nil {
			if _, _, checkErr := currentGateOpenPosition(ctx, client, active.Position.InstID, active.Position.PosSide); errors.Is(checkErr, errPositionNotOpen) {
				return nil
			}
			return err
		}
	}
	_, position, err := currentGateOpenPosition(ctx, client, active.Position.InstID, active.Position.PosSide)
	if err != nil {
		if errors.Is(err, errPositionNotOpen) {
			return nil
		}
		return err
	}
	if active.Partial {
		if remaining == "" || remaining == "0" {
			remaining = gatePartialCloseRemaining(active, position)
		}
		remaining = capCloseSizeToPosition(position.Pos, remaining)
		if remaining == "" || remaining == "0" {
			return nil
		}
		_, err = placeGatePositionClose(ctx, cfg, client, position, gateSignedCloseSize(position.Pos, remaining))
		return err
	}
	_, err = placeGatePositionClose(ctx, cfg, client, position, "")
	return err
}

func placeGatePositionClose(ctx context.Context, cfg config.Config, client gate.Client, position okx.Position, closeSz string) (gate.Order, error) {
	marginMode := strings.ToLower(strings.TrimSpace(position.MgnMode))
	if marginMode != "cross" && marginMode != "isolated" {
		marginMode = cfg.MarginMode()
	}
	req := gate.PlaceOrderRequest{
		Contract:      strings.ToUpper(strings.TrimSpace(position.InstID)),
		Size:          "0",
		Price:         "0",
		TIF:           "ioc",
		Close:         true,
		ReduceOnly:    true,
		PosMarginMode: marginMode,
		Text:          nextGatePositionCloseText(),
	}
	if strings.TrimSpace(closeSz) != "" {
		req.Size = strings.TrimSpace(closeSz)
		req.Close = false
	}
	return client.PlaceOrder(ctx, req)
}

func (s *Server) handlePositionProtection(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "only POST is allowed")
		return
	}
	var req positionProtectionRequest
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	req.Exchange = trading.NormalizeExchange(req.Exchange)
	var err error
	req.Env, err = normalizeRequestedTradeEnv(req.Env)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_position_protection", err.Error())
		return
	}
	req.APIID = strings.TrimSpace(req.APIID)
	req.InstID = strings.ToUpper(strings.TrimSpace(req.InstID))
	req.PosSide = normalizePosSide(req.PosSide)
	req.Kind = strings.ToLower(strings.TrimSpace(req.Kind))
	if req.InstID == "" {
		writeError(w, http.StatusBadRequest, "invalid_position_protection", "inst_id is required")
		return
	}
	if !validPositionProtectionKind(req.Kind) {
		writeError(w, http.StatusBadRequest, "invalid_position_protection", "kind must be tp, sl, or trailing")
		return
	}
	cfg, err := configForRequestedTradeEnv(s.ConfigStore.Get(), req.Env)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_position_protection", err.Error())
		return
	}
	if req.Exchange == trading.ExchangeGate {
		writeError(w, http.StatusBadRequest, "unsupported_exchange_position_protection", "Gate position protection is not supported")
		return
	}
	if req.Exchange == trading.ExchangeBinance {
		s.handleBinancePositionProtection(w, r, cfg, req)
		return
	}
	if s.okxCredentialStore(cfg.Trading.Env) == nil {
		writeError(w, http.StatusServiceUnavailable, "not_configured", "OKX credential store is not configured")
		return
	}
	client, apiID, err := s.okxClientForCredentials(cfg, req.APIID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "credentials_failed", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	position, err := currentOpenPosition(ctx, client, req.InstID, req.PosSide)
	if err != nil {
		if errors.Is(err, errPositionNotOpen) {
			writeError(w, http.StatusConflict, "position_not_open", err.Error())
			return
		}
		writeError(w, http.StatusBadGateway, "positions_failed", err.Error())
		return
	}
	resp, err := placeOKXPositionProtection(ctx, cfg, client, apiID, position, req.Kind)
	if err != nil {
		writeError(w, http.StatusBadGateway, "position_protection_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleBinancePositionProtection(w http.ResponseWriter, r *http.Request, cfg config.Config, req positionProtectionRequest) {
	if s.binanceCredentialStore(cfg.Trading.Env) == nil {
		writeError(w, http.StatusServiceUnavailable, "not_configured", "Binance credential store is not configured")
		return
	}
	if !cfg.BinanceLiveTradingAllowedByEnvironment() {
		writeError(w, http.StatusForbidden, "live_trading_disabled", "Binance live trading is not allowed by environment")
		return
	}
	client, apiID, err := s.binanceClientForCredentials(cfg, req.APIID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "credentials_failed", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	position, err := currentBinanceOpenPosition(ctx, client, req.InstID, req.PosSide)
	if err != nil {
		if errors.Is(err, errPositionNotOpen) {
			writeError(w, http.StatusConflict, "position_not_open", err.Error())
			return
		}
		writeError(w, http.StatusBadGateway, "positions_failed", err.Error())
		return
	}
	resp, err := placeBinancePositionProtection(ctx, cfg, client, apiID, position, req.Kind)
	if err != nil {
		writeError(w, http.StatusBadGateway, "position_protection_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleBinancePositionClose(w http.ResponseWriter, r *http.Request, cfg config.Config, req positionCloseRequest, ratio float64) {
	if s.binanceCredentialStore(cfg.Trading.Env) == nil {
		writeError(w, http.StatusServiceUnavailable, "not_configured", "Binance credential store is not configured")
		return
	}
	if !cfg.BinanceLiveTradingAllowedByEnvironment() {
		writeError(w, http.StatusForbidden, "live_trading_disabled", "Binance live trading is not allowed by environment")
		return
	}
	client, apiID, err := s.binanceClientForCredentials(cfg, req.APIID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "credentials_failed", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	position, err := currentBinanceOpenPosition(ctx, client, req.InstID, req.PosSide)
	if err != nil {
		if errors.Is(err, errPositionNotOpen) {
			writeError(w, http.StatusConflict, "position_not_open", err.Error())
			return
		}
		writeError(w, http.StatusBadGateway, "positions_failed", err.Error())
		return
	}
	if err := cancelAllBinanceSymbolOrders(ctx, client, position.InstID); err != nil {
		writeError(w, http.StatusBadGateway, "position_close_cancel_orders_failed", err.Error())
		return
	}
	position, err = currentBinanceOpenPosition(ctx, client, position.InstID, position.PosSide)
	if err != nil {
		if errors.Is(err, errPositionNotOpen) {
			writeError(w, http.StatusConflict, "position_not_open", err.Error())
			return
		}
		writeError(w, http.StatusBadGateway, "positions_failed", err.Error())
		return
	}
	closeSz := ""
	if ratio < 1 {
		closeSz, err = binancePositionCloseSize(ctx, client, position, ratio)
		if err != nil {
			writeError(w, http.StatusBadGateway, "position_close_failed", err.Error())
			return
		}
	}
	if req.Mode == "limit" {
		order, started, err := s.startBinanceLimitPositionClose(ctx, apiID, client, position, closeSz)
		if err != nil {
			writeError(w, http.StatusBadGateway, "position_close_failed", err.Error())
			return
		}
		if !started {
			writeError(w, http.StatusConflict, "position_close_running", "limit close is already running for this position")
			return
		}
		status := "running"
		message := "limit close order started"
		if order.MarketFallback {
			status = "market_fallback"
			message = "Binance limit close was rejected by reduce-only conflict; market reduce-only fallback submitted"
		}
		if order.NonReduceOnlyFallback {
			status = "market_fallback"
			message = "Binance reduce-only close was rejected; market close without reduceOnly submitted after canceling symbol orders"
		}
		if order.Unknown {
			status = "unknown"
			message = "Binance close order status is unknown; refresh positions before retrying"
		}
		writeJSON(w, http.StatusAccepted, positionCloseResponse{
			OK:      true,
			Status:  status,
			Mode:    req.Mode,
			APIID:   apiID,
			InstID:  order.Position.InstID,
			PosSide: normalizePosSide(order.Position.PosSide),
			Sz:      order.CloseSz,
			OrdID:   order.Ack.OrdID,
			ClOrdID: order.Ack.ClOrdID,
			Px:      order.Px,
			Message: message,
		})
		return
	}
	order, err := placeBinancePositionClose(ctx, client, position, req.Mode, closeSz)
	if err != nil {
		writeError(w, http.StatusBadGateway, "position_close_failed", err.Error())
		return
	}
	status := "submitted"
	message := "market close order submitted"
	code := http.StatusOK
	if order.Unknown {
		status = "unknown"
		message = "Binance close order status is unknown; refresh positions before retrying"
		code = http.StatusAccepted
	}
	writeJSON(w, code, positionCloseResponse{
		OK:      true,
		Status:  status,
		Mode:    req.Mode,
		APIID:   apiID,
		InstID:  order.Position.InstID,
		PosSide: normalizePosSide(order.Position.PosSide),
		Sz:      order.CloseSz,
		OrdID:   order.Ack.OrdID,
		ClOrdID: order.Ack.ClOrdID,
		Px:      order.Px,
		Message: message,
	})
}

// cancelAllBinanceSymbolOrders clears both regular and conditional orders for
// the closing symbol before sizing and submitting a position close. It verifies
// both order groups are empty so a partial cancellation never proceeds to a
// close that could be rejected or unexpectedly overlap another order.
func cancelAllBinanceSymbolOrders(ctx context.Context, client binance.Client, symbol string) error {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	if symbol == "" {
		return errors.New("Binance close symbol is required to cancel open orders")
	}
	regular, err := client.OpenOrders(ctx, symbol)
	if err != nil {
		return fmt.Errorf("inspect Binance regular orders for %s failed: %w", symbol, err)
	}
	for _, order := range regular {
		cancelReq := binance.CancelOrderRequest{Symbol: order.Symbol}
		if order.OrderID != 0 {
			cancelReq.OrderID = strconv.FormatInt(order.OrderID, 10)
		} else {
			cancelReq.OrigClientOrderID = strings.TrimSpace(order.ClientOrderID)
		}
		if cancelReq.OrderID == "" && cancelReq.OrigClientOrderID == "" {
			return fmt.Errorf("cannot safely cancel Binance regular order for %s: missing order identifiers", symbol)
		}
		if _, err := client.CancelOrder(ctx, cancelReq); err != nil && !binanceOrderCancellationAlreadyDone(err) {
			return fmt.Errorf("cancel Binance regular order for %s failed: %w", symbol, err)
		}
	}

	algos, err := client.OpenAlgoOrders(ctx, symbol)
	if err != nil {
		return fmt.Errorf("inspect Binance algorithm orders for %s failed: %w", symbol, err)
	}
	for _, order := range algos {
		if _, err := client.CancelAlgoOrder(ctx, order.AlgoID, strings.TrimSpace(order.ClientAlgoID)); err != nil && !binanceOrderCancellationAlreadyDone(err) {
			return fmt.Errorf("cancel Binance algorithm order for %s failed: %w", symbol, err)
		}
	}

	regular, err = client.OpenOrders(ctx, symbol)
	if err != nil {
		return fmt.Errorf("verify Binance regular orders for %s failed: %w", symbol, err)
	}
	algos, err = client.OpenAlgoOrders(ctx, symbol)
	if err != nil {
		return fmt.Errorf("verify Binance algorithm orders for %s failed: %w", symbol, err)
	}
	if len(regular) != 0 || len(algos) != 0 {
		return fmt.Errorf("Binance orders remain for %s after cancellation (regular=%d, algorithm=%d); close was not submitted", symbol, len(regular), len(algos))
	}
	return nil
}

func (s *Server) startLimitPositionClose(ctx context.Context, apiID string, cfg config.Config, client okx.Client, position okx.Position, closeSz string) (positionCloseOrder, bool, error) {
	key := positionCloseKey(trading.ExchangeOKX, apiID, position.InstID, position.PosSide)
	if !positionCloseJobs.start(key) {
		return positionCloseOrder{}, false, nil
	}
	order, err := placeLimitPositionClose(ctx, cfg, client, position, closeSz)
	if err != nil {
		positionCloseJobs.done(key)
		return positionCloseOrder{}, false, err
	}
	go s.watchLimitPositionClose(apiID, cfg, client, order)
	return order, true, nil
}

func (s *Server) startAutoProfitLimitPositionClose(ctx context.Context, apiID string, cfg config.Config, client okx.Client, position okx.Position) (positionCloseOrder, bool, error) {
	key := positionCloseKey(trading.ExchangeOKX, apiID, position.InstID, position.PosSide)
	if !positionCloseJobs.start(key) {
		return positionCloseOrder{}, false, nil
	}
	order, err := placeLimitPositionClose(ctx, cfg, client, position, "")
	if err != nil {
		positionCloseJobs.done(key)
		return positionCloseOrder{}, false, err
	}
	go s.watchAutoProfitLimitPositionClose(apiID, cfg, client, order)
	return order, true, nil
}

func (s *Server) startBinanceLimitPositionClose(ctx context.Context, apiID string, client binance.Client, position okx.Position, closeSz string) (positionCloseOrder, bool, error) {
	key := positionCloseKey(trading.ExchangeBinance, apiID, position.InstID, position.PosSide)
	if !positionCloseJobs.start(key) {
		return positionCloseOrder{}, false, nil
	}
	order, err := placeBinancePositionClose(ctx, client, position, "limit", closeSz)
	if err != nil {
		positionCloseJobs.done(key)
		return positionCloseOrder{}, false, err
	}
	go s.watchBinanceLimitPositionClose(apiID, client, order)
	return order, true, nil
}

func (s *Server) fetchPositions(ctx context.Context, cfg config.Config, requestedAPIID, instType string, refresh bool) (positionsResponse, error) {
	store := s.okxCredentialStore(cfg.Trading.Env)
	creds, apiID, err := store.OKXCredentials(requestedAPIID)
	if err != nil {
		return positionsResponse{}, err
	}
	client := okx.Client{
		BaseURL:     cfg.OKXBaseURL(),
		Credentials: creds,
		Demo:        cfg.DemoTradingHeaderEnabled(),
		HTTPClient:  s.okxHTTPClient(),
	}
	positions, _, err := client.Positions(ctx, instType)
	if err != nil {
		return positionsResponse{}, err
	}
	positions = openPositions(positions)
	precisions := s.okxDisplayPrecisions(ctx, client)
	sort.Slice(positions, func(i, j int) bool {
		if positions[i].InstID == positions[j].InstID {
			return positions[i].PosSide < positions[j].PosSide
		}
		return positions[i].InstID < positions[j].InstID
	})
	now := s.now()
	fills, fillErr := s.cachedPositionEntryFills(
		trading.ExchangeOKX+"|"+apiID+"|"+strings.TrimRight(client.BaseURL, "/")+"|"+strings.ToUpper(strings.TrimSpace(instType)),
		now,
		refresh,
		func() ([]positionEntryFill, error) {
			return fetchOKXPositionEntryFills(ctx, client, instType, positions, now)
		},
	)
	views := positionViewsWithEntryTimes(positions, fills, now, entryTimeSourceOKXFills, fillErr)
	s.enrichPositionViewsWithOrderMetadata(views, trading.ExchangeOKX, apiID)
	applyPositionViewPrecisions(views, precisions)
	return positionsResponse{
		OK:          true,
		Exchange:    trading.ExchangeOKX,
		APIID:       apiID,
		InstType:    instType,
		Count:       len(positions),
		RefreshedAt: now,
		Positions:   views,
	}, nil
}

func (s *Server) fetchPendingOrders(ctx context.Context, cfg config.Config, requestedAPIID, instType string) (pendingOrdersResponse, error) {
	client, apiID, err := s.okxClientForCredentials(cfg, requestedAPIID)
	if err != nil {
		return pendingOrdersResponse{}, err
	}
	orders, _, err := client.PendingOrders(ctx, instType)
	if err != nil {
		return pendingOrdersResponse{}, err
	}
	algoOrders, _, err := client.PendingAlgoOrders(ctx, instType, "")
	if err != nil {
		return pendingOrdersResponse{}, err
	}
	sort.Slice(orders, func(i, j int) bool {
		if orders[i].InstID == orders[j].InstID {
			if orders[i].CTime == orders[j].CTime {
				return orders[i].OrdID < orders[j].OrdID
			}
			return orders[i].CTime > orders[j].CTime
		}
		return orders[i].InstID < orders[j].InstID
	})
	views := s.pendingOrderViews(ctx, cfg, client, apiID, orders)
	algoViews := s.pendingAlgoOrderViews(ctx, cfg, client, apiID, algoOrders)
	normalCount := len(orders)
	algoCount := len(algoViews)
	return pendingOrdersResponse{
		OK:          true,
		Exchange:    trading.ExchangeOKX,
		APIID:       apiID,
		InstType:    instType,
		Count:       normalCount,
		NormalCount: normalCount,
		AlgoCount:   algoCount,
		TotalCount:  normalCount + algoCount,
		RefreshedAt: s.now(),
		Orders:      views,
		AlgoOrders:  algoViews,
	}, nil
}

func (s *Server) fetchBinancePositions(ctx context.Context, cfg config.Config, requestedAPIID string, refresh bool) (positionsResponse, error) {
	client, apiID, err := s.binanceClientForCredentials(cfg, requestedAPIID)
	if err != nil {
		return positionsResponse{}, err
	}
	positions, err := client.Positions(ctx, "")
	if err != nil {
		return positionsResponse{}, err
	}
	precisions := s.binanceDisplayPrecisions(ctx, client)
	out := make([]okx.Position, 0, len(positions))
	for _, position := range positions {
		converted := binancePositionToOKX(position)
		if isOpenPosition(converted.Pos) {
			out = append(out, converted)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].InstID == out[j].InstID {
			return out[i].PosSide < out[j].PosSide
		}
		return out[i].InstID < out[j].InstID
	})
	now := s.now()
	fillsBySymbol := make(map[string][]positionEntryFill)
	errorsBySymbol := make(map[string]error)
	positionsBySymbol := positionsByInstID(out)
	for symbol, symbolPositions := range positionsBySymbol {
		symbol := symbol
		symbolPositions := symbolPositions
		fills, fillErr := s.cachedPositionEntryFills(
			trading.ExchangeBinance+"|"+apiID+"|"+strings.TrimRight(client.BaseURL, "/")+"|"+symbol,
			now,
			refresh,
			func() ([]positionEntryFill, error) {
				return fetchBinancePositionEntryFills(ctx, client, symbol, symbolPositions, now)
			},
		)
		fillsBySymbol[symbol] = fills
		if fillErr != nil {
			errorsBySymbol[symbol] = fillErr
		}
	}
	views := binancePositionViewsWithEntryTimes(out, fillsBySymbol, errorsBySymbol, now)
	s.enrichPositionViewsWithOrderMetadata(views, trading.ExchangeBinance, apiID)
	applyPositionViewPrecisions(views, precisions)
	return positionsResponse{
		OK:          true,
		Exchange:    trading.ExchangeBinance,
		APIID:       apiID,
		InstType:    "USDT-M",
		Count:       len(out),
		RefreshedAt: now,
		Positions:   views,
	}, nil
}

func (s *Server) fetchBinancePendingOrders(ctx context.Context, cfg config.Config, requestedAPIID string) (pendingOrdersResponse, error) {
	client, apiID, err := s.binanceClientForCredentials(cfg, requestedAPIID)
	if err != nil {
		return pendingOrdersResponse{}, err
	}
	orders, err := client.OpenOrders(ctx, "")
	if err != nil {
		return pendingOrdersResponse{}, err
	}
	algoOrders, err := client.OpenAlgoOrders(ctx, "")
	if err != nil {
		return pendingOrdersResponse{}, err
	}
	sort.Slice(orders, func(i, j int) bool {
		if orders[i].Symbol == orders[j].Symbol {
			return orders[i].Time > orders[j].Time
		}
		return orders[i].Symbol < orders[j].Symbol
	})
	views := s.binancePendingOrderViews(ctx, cfg, client, apiID, orders)
	algoViews := s.binancePendingAlgoOrderViews(ctx, cfg, client, apiID, algoOrders)
	normalCount := len(views)
	algoCount := len(algoViews)
	return pendingOrdersResponse{
		OK:          true,
		Exchange:    trading.ExchangeBinance,
		APIID:       apiID,
		InstType:    "USDT-M",
		Count:       normalCount,
		NormalCount: normalCount,
		AlgoCount:   algoCount,
		TotalCount:  normalCount + algoCount,
		RefreshedAt: s.now(),
		Orders:      views,
		AlgoOrders:  algoViews,
	}, nil
}

func (s *Server) fetchGatePositions(ctx context.Context, cfg config.Config, requestedAPIID string) (positionsResponse, error) {
	client, apiID, err := s.gateClientForCredentials(cfg, requestedAPIID)
	if err != nil {
		return positionsResponse{}, err
	}
	rawPositions, err := client.Positions(ctx, "")
	if err != nil {
		return positionsResponse{}, err
	}
	positions := make([]okx.Position, 0, len(rawPositions))
	for _, raw := range rawPositions {
		position := gatePositionToOKX(raw)
		if isOpenPosition(position.Pos) {
			positions = append(positions, position)
		}
	}
	sort.Slice(positions, func(i, j int) bool {
		if positions[i].InstID == positions[j].InstID {
			return positions[i].PosSide < positions[j].PosSide
		}
		return positions[i].InstID < positions[j].InstID
	})
	now := s.now()
	fillsByContract := make(map[string][]positionEntryFill)
	errorsByContract := make(map[string]error)
	loadedContracts := make(map[string]struct{})
	for _, position := range positions {
		contract := strings.ToUpper(strings.TrimSpace(position.InstID))
		if contract == "" {
			continue
		}
		if _, loaded := loadedContracts[contract]; loaded {
			continue
		}
		loadedContracts[contract] = struct{}{}
		trades, tradeErr := client.MyTrades(ctx, contract)
		if tradeErr != nil {
			errorsByContract[contract] = tradeErr
			continue
		}
		fillsByContract[contract] = gatePositionEntryFills(trades)
	}
	views := make([]positionView, 0, len(positions))
	for _, position := range positions {
		contract := strings.ToUpper(strings.TrimSpace(position.InstID))
		views = append(views, positionViewWithEntryTime(
			position,
			fillsByContract[contract],
			now,
			entryTimeSourceGateTrades,
			errorsByContract[contract],
		))
	}
	s.enrichPositionViewsWithOrderMetadata(views, trading.ExchangeGate, apiID)
	return positionsResponse{
		OK: true, Exchange: trading.ExchangeGate, APIID: apiID, InstType: "USDT-M",
		Count: len(positions), RefreshedAt: now, Positions: views,
	}, nil
}

func (s *Server) fetchGatePendingOrders(ctx context.Context, cfg config.Config, requestedAPIID string) (pendingOrdersResponse, error) {
	client, apiID, err := s.gateClientForCredentials(cfg, requestedAPIID)
	if err != nil {
		return pendingOrdersResponse{}, err
	}
	orders, err := client.OpenOrders(ctx, "")
	if err != nil {
		return pendingOrdersResponse{}, err
	}
	sort.Slice(orders, func(i, j int) bool {
		if orders[i].Contract == orders[j].Contract {
			return orders[i].UpdateTime > orders[j].UpdateTime
		}
		return orders[i].Contract < orders[j].Contract
	})
	views := make([]pendingOrderView, 0, len(orders))
	for _, raw := range orders {
		views = append(views, pendingOrderView{
			PendingOrder:           gateOrderToOKX(raw),
			OrderGroup:             "normal",
			Chaseable:              false,
			ChaseUnavailableReason: "Gate 暂不支持自动追价",
		})
	}
	normalCount := len(views)
	return pendingOrdersResponse{
		OK: true, Exchange: trading.ExchangeGate, APIID: apiID, InstType: "USDT-M",
		Count: normalCount, NormalCount: normalCount, TotalCount: normalCount,
		RefreshedAt: s.now(), Orders: views, AlgoOrders: []pendingOrderView{},
	}, nil
}

func (s *Server) cachedPositionEntryFills(key string, now time.Time, refresh bool, fetch func() ([]positionEntryFill, error)) ([]positionEntryFill, error) {
	cache := s.positionEntryCache
	if cache == nil {
		cache = &positionEntryFillCache{}
		s.positionEntryCache = cache
	}
	if !refresh {
		if fills, ok := cache.get(key, now); ok {
			return fills, nil
		}
	}
	fills, err := fetch()
	if err != nil {
		return nil, err
	}
	cache.set(key, now, fills)
	return fills, nil
}

func (c *positionEntryFillCache) get(key string, now time.Time) ([]positionEntryFill, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.items == nil {
		return nil, false
	}
	item, ok := c.items[key]
	if !ok || now.Sub(item.fetchedAt) > positionEntryCacheTTL {
		return nil, false
	}
	return clonePositionEntryFills(item.fills), true
}

func (c *positionEntryFillCache) set(key string, now time.Time, fills []positionEntryFill) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.items == nil {
		c.items = map[string]positionEntryFillCacheItem{}
	}
	c.items[key] = positionEntryFillCacheItem{
		fetchedAt: now,
		fills:     clonePositionEntryFills(fills),
	}
}

func clonePositionEntryFills(fills []positionEntryFill) []positionEntryFill {
	if len(fills) == 0 {
		return nil
	}
	out := make([]positionEntryFill, len(fills))
	copy(out, fills)
	return out
}

func fetchOKXPositionEntryFills(ctx context.Context, client okx.Client, instType string, positions []okx.Position, now time.Time) ([]positionEntryFill, error) {
	if len(positions) == 0 {
		return nil, nil
	}
	since := now.Add(-positionEntryLookback)
	after := ""
	out := []positionEntryFill{}
	for page := 0; page < positionEntryMaxOKXPages; page++ {
		fills, _, err := client.FillsHistory(ctx, instType, after, positionEntryHistoryLimit)
		if err != nil {
			return nil, err
		}
		if len(fills) == 0 {
			return out, nil
		}
		var oldest time.Time
		oldAfter := after
		pageAfter := ""
		for i, fill := range fills {
			if i == len(fills)-1 {
				pageAfter = strings.TrimSpace(fill.TradeID)
				if pageAfter == "" {
					pageAfter = strings.TrimSpace(fill.OrdID)
				}
			}
			fillTimeMS, err := strconv.ParseInt(strings.TrimSpace(fill.FillTime), 10, 64)
			if err != nil {
				continue
			}
			fillTime := time.UnixMilli(fillTimeMS).UTC()
			if oldest.IsZero() || fillTime.Before(oldest) {
				oldest = fillTime
			}
			if fillTime.Before(since) {
				continue
			}
			size, err := strconv.ParseFloat(strings.TrimSpace(fill.FillSz), 64)
			if err != nil || size <= 0 {
				continue
			}
			out = append(out, positionEntryFill{
				InstID:   strings.ToUpper(strings.TrimSpace(fill.InstID)),
				OrdID:    strings.TrimSpace(fill.OrdID),
				ClOrdID:  strings.TrimSpace(fill.ClOrdID),
				PosSide:  fill.PosSide,
				Side:     fill.Side,
				Size:     size,
				FillTime: fillTime,
			})
		}
		after = pageAfter
		if allPositionEntryTimesFound(positions, out) {
			return out, nil
		}
		if oldest.IsZero() || oldest.Before(since) || len(fills) < positionEntryHistoryLimit || after == "" || after == oldAfter {
			return out, nil
		}
	}
	return out, nil
}

func fetchBinancePositionEntryFills(ctx context.Context, client binance.Client, symbol string, positions []okx.Position, now time.Time) ([]positionEntryFill, error) {
	if len(positions) == 0 {
		return nil, nil
	}
	since := now.Add(-positionEntryLookback)
	windowEnd := now.UTC()
	out := []positionEntryFill{}
	for windowEnd.After(since) {
		windowStart := windowEnd.Add(-binanceUserTradesWindow)
		if windowStart.Before(since) {
			windowStart = since
		}
		trades, err := client.UserTrades(ctx, symbol, windowStart, windowEnd, binanceUserTradesLimit)
		if err != nil {
			return nil, err
		}
		for _, trade := range trades {
			fillTime := time.UnixMilli(trade.Time).UTC()
			if fillTime.Before(since) {
				continue
			}
			size, err := strconv.ParseFloat(strings.TrimSpace(trade.Qty), 64)
			if err != nil || size <= 0 {
				continue
			}
			out = append(out, positionEntryFill{
				InstID:   strings.ToUpper(strings.TrimSpace(trade.Symbol)),
				OrdID:    strconv.FormatInt(trade.OrderID, 10),
				PosSide:  trade.PositionSide,
				Side:     trade.Side,
				Size:     size,
				FillTime: fillTime,
			})
		}
		if allPositionEntryTimesFound(positions, out) {
			return out, nil
		}
		if len(trades) >= binanceUserTradesLimit {
			return out, fmt.Errorf("Binance %s 7天成交超过 %d 条，成交历史可能不足", symbol, binanceUserTradesLimit)
		}
		windowEnd = windowStart.Add(-time.Millisecond)
	}
	return out, nil
}

func positionViewsWithEntryTimes(positions []okx.Position, fills []positionEntryFill, now time.Time, source string, fillErr error) []positionView {
	views := make([]positionView, 0, len(positions))
	for _, position := range positions {
		views = append(views, positionViewWithEntryTime(position, fills, now, source, fillErr))
	}
	return views
}

func binancePositionViewsWithEntryTimes(positions []okx.Position, fillsBySymbol map[string][]positionEntryFill, errorsBySymbol map[string]error, now time.Time) []positionView {
	views := make([]positionView, 0, len(positions))
	for _, position := range positions {
		symbol := strings.ToUpper(strings.TrimSpace(position.InstID))
		views = append(views, positionViewWithEntryTime(position, fillsBySymbol[symbol], now, entryTimeSourceBinanceTrade, errorsBySymbol[symbol]))
	}
	return views
}

func positionViewWithEntryTime(position okx.Position, fills []positionEntryFill, now time.Time, source string, fillErr error) positionView {
	view := positionView{Position: position}
	if fillErr != nil {
		view.EntryTimeError = "成交历史读取失败: " + fillErr.Error()
		applyPositionTimeFallback(&view, now)
		return view
	}
	entryFill, ok, message := positionEntryFillForPosition(position, fills)
	if !ok {
		view.EntryTimeError = message
		applyPositionTimeFallback(&view, now)
		return view
	}
	entryTime := entryFill.FillTime
	view.EntryFillTime = entryTime.UTC().Format(time.RFC3339Nano)
	view.EntryTimeSource = source
	view.entryOrdID = entryFill.OrdID
	view.entryClOrdID = entryFill.ClOrdID
	if seconds := int64(now.UTC().Sub(entryTime.UTC()).Seconds()); seconds > 0 {
		view.HoldingSeconds = seconds
	}
	return view
}

func applyPositionTimeFallback(view *positionView, now time.Time) {
	if view == nil || strings.TrimSpace(view.EntryFillTime) != "" {
		return
	}
	entryTime, ok := exchangePositionTime(view.Position)
	if !ok {
		return
	}
	view.EntryFillTime = entryTime.UTC().Format(time.RFC3339Nano)
	view.EntryTimeSource = entryTimeSourcePositionTime
	if seconds := int64(now.UTC().Sub(entryTime.UTC()).Seconds()); seconds > 0 {
		view.HoldingSeconds = seconds
	}
}

func exchangePositionTime(position okx.Position) (time.Time, bool) {
	for _, raw := range []string{position.CTime, position.UTime} {
		ts, ok := parseExchangeMillisTime(raw)
		if ok {
			return ts, true
		}
	}
	return time.Time{}, false
}

func parseExchangeMillisTime(raw string) (time.Time, bool) {
	text := strings.TrimSpace(raw)
	if text == "" || text == "0" || text == "-" {
		return time.Time{}, false
	}
	ms, err := strconv.ParseInt(text, 10, 64)
	if err != nil || ms <= 0 {
		return time.Time{}, false
	}
	return time.UnixMilli(ms).UTC(), true
}

func allPositionEntryTimesFound(positions []okx.Position, fills []positionEntryFill) bool {
	for _, position := range positions {
		if _, ok, _ := positionEntryFillTime(position, fills); !ok {
			return false
		}
	}
	return true
}

func positionEntryFillTime(position okx.Position, fills []positionEntryFill) (time.Time, bool, string) {
	fill, ok, message := positionEntryFillForPosition(position, fills)
	return fill.FillTime, ok, message
}

func positionEntryFillForPosition(position okx.Position, fills []positionEntryFill) (positionEntryFill, bool, string) {
	current, ok := signedPositionSize(position)
	if !ok || nearlyZero(current) {
		return positionEntryFill{}, false, "当前持仓数量无效，无法计算成交起点"
	}
	currentSign := signOf(current)
	relevant := make([]positionEntryFill, 0, len(fills))
	for _, fill := range fills {
		if !positionEntryFillMatches(position, fill) {
			continue
		}
		if _, ok := signedFillSize(fill); !ok {
			continue
		}
		relevant = append(relevant, fill)
	}
	sort.Slice(relevant, func(i, j int) bool {
		if relevant[i].FillTime.Equal(relevant[j].FillTime) {
			return relevant[i].Side < relevant[j].Side
		}
		return relevant[i].FillTime.After(relevant[j].FillTime)
	})
	after := current
	for _, fill := range relevant {
		delta, ok := signedFillSize(fill)
		if !ok {
			continue
		}
		before := after - delta
		if signOf(after) == currentSign && (nearlyZero(before) || signOf(before) != currentSign) {
			return fill, true, ""
		}
		after = before
	}
	return positionEntryFill{}, false, "90天内成交不足，无法重建当前持仓起点"
}

func (s *Server) enrichPositionViewsWithOrderMetadata(views []positionView, exchange, apiID string) {
	if s.Orders == nil || len(views) == 0 {
		return
	}
	records := s.Orders.ListByTargetExchange(exchange, 5000)
	for i := range views {
		if views[i].entryOrdID == "" && views[i].entryClOrdID == "" {
			continue
		}
		for _, rec := range records {
			if rec.Status != storage.StatusSubmitted || rec.MarketStrategy == "" || rec.ADX == nil {
				continue
			}
			if rec.PositionEffect != "" && rec.PositionEffect != trading.PositionEffectOpen {
				continue
			}
			resolvedAPIID := firstNonEmptyString(rec.Result.APIID, rec.APIID)
			if !strings.EqualFold(strings.TrimSpace(resolvedAPIID), strings.TrimSpace(apiID)) ||
				!strings.EqualFold(strings.TrimSpace(rec.Result.InstID), strings.TrimSpace(views[i].InstID)) {
				continue
			}
			if rec.PositionSide != "" && rec.PositionSide != positionDirectionKind(views[i].Position) {
				continue
			}
			if !positionEntryOrderMatches(rec.Result, views[i].entryOrdID, views[i].entryClOrdID) {
				continue
			}
			views[i].MarketStrategy = rec.MarketStrategy
			entryADX := *rec.ADX
			views[i].EntryADX = &entryADX
			break
		}
	}
}

func positionEntryOrderMatches(result trading.OrderResult, ordID, clOrdID string) bool {
	ordID = strings.TrimSpace(ordID)
	clOrdID = strings.TrimSpace(clOrdID)
	return orderIDListContains(result.OrdID, ordID) || orderIDListContains(result.ClOrdID, clOrdID)
}

func orderIDListContains(list, wanted string) bool {
	wanted = strings.TrimSpace(wanted)
	if wanted == "" {
		return false
	}
	for _, candidate := range strings.Split(list, " / ") {
		if wanted == strings.TrimSpace(candidate) {
			return true
		}
	}
	return false
}

func positionEntryFillMatches(position okx.Position, fill positionEntryFill) bool {
	if !strings.EqualFold(strings.TrimSpace(position.InstID), strings.TrimSpace(fill.InstID)) {
		return false
	}
	positionKind := positionDirectionKind(position)
	fillPosSide := normalizePosSide(fill.PosSide)
	if fillPosSide == "" || fillPosSide == "net" {
		return true
	}
	return fillPosSide == positionKind
}

func signedPositionSize(position okx.Position) (float64, bool) {
	size, err := strconv.ParseFloat(strings.TrimSpace(position.Pos), 64)
	if err != nil {
		return 0, false
	}
	switch positionDirectionKind(position) {
	case "long":
		return math.Abs(size), true
	case "short":
		return -math.Abs(size), true
	default:
		return size, true
	}
}

func signedFillSize(fill positionEntryFill) (float64, bool) {
	size := math.Abs(fill.Size)
	if size <= 0 {
		return 0, false
	}
	switch strings.ToLower(strings.TrimSpace(fill.Side)) {
	case "buy":
		return size, true
	case "sell":
		return -size, true
	default:
		return 0, false
	}
}

func positionDirectionKind(position okx.Position) string {
	switch normalizePosSide(position.PosSide) {
	case "long":
		return "long"
	case "short":
		return "short"
	}
	size, err := strconv.ParseFloat(strings.TrimSpace(position.Pos), 64)
	if err != nil {
		return ""
	}
	if size > positionEntrySizeEpsilon {
		return "long"
	}
	if size < -positionEntrySizeEpsilon {
		return "short"
	}
	return ""
}

func signOf(v float64) int {
	if v > positionEntrySizeEpsilon {
		return 1
	}
	if v < -positionEntrySizeEpsilon {
		return -1
	}
	return 0
}

func nearlyZero(v float64) bool {
	return math.Abs(v) <= positionEntrySizeEpsilon
}

func positionsByInstID(positions []okx.Position) map[string][]okx.Position {
	out := make(map[string][]okx.Position)
	for _, position := range positions {
		instID := strings.ToUpper(strings.TrimSpace(position.InstID))
		if instID == "" {
			continue
		}
		out[instID] = append(out[instID], position)
	}
	return out
}

func (s *Server) okxDisplayPrecisions(ctx context.Context, client okx.Client) map[string]symbolDisplayPrecision {
	instruments, _, err := client.SwapInstruments(ctx)
	if err != nil {
		if s.Logger != nil {
			s.Logger.Warn("failed to fetch OKX symbol display precision", "error", err)
		}
		return nil
	}
	out := make(map[string]symbolDisplayPrecision, len(instruments))
	for _, inst := range instruments {
		instID := strings.ToUpper(strings.TrimSpace(inst.InstID))
		if instID == "" {
			continue
		}
		out[instID] = precisionFromOKXInstrument(inst)
	}
	return out
}

func (s *Server) binanceDisplayPrecisions(ctx context.Context, client binance.Client) map[string]symbolDisplayPrecision {
	info, err := client.ExchangeInfo(ctx)
	if err != nil {
		if s.Logger != nil {
			s.Logger.Warn("failed to fetch Binance symbol display precision", "error", err)
		}
		return nil
	}
	out := make(map[string]symbolDisplayPrecision, len(info.Symbols))
	for _, symbol := range info.Symbols {
		symbolID := strings.ToUpper(strings.TrimSpace(symbol.Symbol))
		if symbolID == "" {
			continue
		}
		out[symbolID] = precisionFromBinanceSymbol(symbol)
	}
	return out
}

func precisionFromOKXInstrument(inst okx.Instrument) symbolDisplayPrecision {
	return symbolDisplayPrecision{
		PricePrecision:    boundedPrecision(decimalPlacesForDisplay(inst.TickSz)),
		QuantityPrecision: boundedPrecision(decimalPlacesForDisplay(inst.LotSz)),
	}
}

func precisionFromBinanceSymbol(symbol binance.SymbolInfo) symbolDisplayPrecision {
	pricePrecision := symbol.PricePrecision
	quantityPrecision := symbol.QuantityPrecision
	for _, filter := range symbol.Filters {
		switch filter.FilterType {
		case "PRICE_FILTER":
			pricePrecision = maxInt(pricePrecision, decimalPlacesForDisplay(filter.TickSize))
		case "LOT_SIZE":
			quantityPrecision = maxInt(quantityPrecision, decimalPlacesForDisplay(filter.StepSize))
		}
	}
	return symbolDisplayPrecision{
		PricePrecision:    boundedPrecision(pricePrecision),
		QuantityPrecision: boundedPrecision(quantityPrecision),
	}
}

func applyPositionViewPrecisions(views []positionView, precisions map[string]symbolDisplayPrecision) {
	for i := range views {
		precision, ok := precisions[strings.ToUpper(strings.TrimSpace(views[i].InstID))]
		if !ok {
			continue
		}
		views[i].PricePrecision = cloneIntPtr(precision.PricePrecision)
		views[i].QuantityPrecision = cloneIntPtr(precision.QuantityPrecision)
	}
}

func applyPendingOrderViewPrecision(view *pendingOrderView, precision symbolDisplayPrecision) {
	if view == nil {
		return
	}
	view.PricePrecision = cloneIntPtr(precision.PricePrecision)
	view.QuantityPrecision = cloneIntPtr(precision.QuantityPrecision)
}

func boundedPrecision(value int) *int {
	if value < 0 {
		return nil
	}
	if value > 20 {
		value = 20
	}
	return &value
}

func cloneIntPtr(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func decimalPlacesForDisplay(raw string) int {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" {
		return 0
	}
	if i := strings.Index(raw, "e-"); i >= 0 {
		n, err := strconv.Atoi(raw[i+2:])
		if err == nil && n > 0 {
			return n
		}
	}
	if i := strings.Index(raw, "."); i >= 0 {
		return len(raw) - i - 1
	}
	return 0
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func binancePositionToOKX(position binance.Position) okx.Position {
	posSide := strings.ToLower(strings.TrimSpace(position.PositionSide))
	if posSide == "" || posSide == "both" {
		posSide = "net"
	}
	marginMode := strings.ToLower(strings.TrimSpace(position.MarginType))
	if marginMode == "" && strings.TrimSpace(position.IsolatedMargin) != "" {
		marginMode = config.MarginIsolated
	}
	margin := strings.TrimSpace(position.IsolatedMargin)
	if margin == "" || margin == "0" {
		margin = strings.TrimSpace(position.InitialMargin)
	}
	notional := binancePositionNotional(position)
	lever := binancePositionLeverage(position, notional)
	return okx.Position{
		InstType:    "USDT-M",
		InstID:      strings.ToUpper(strings.TrimSpace(position.Symbol)),
		MgnMode:     marginMode,
		PosSide:     posSide,
		Pos:         strings.TrimSpace(position.PositionAmt),
		AvailPos:    strings.TrimSpace(position.PositionAmt),
		AvgPx:       strings.TrimSpace(position.EntryPrice),
		MarkPx:      strings.TrimSpace(position.MarkPrice),
		Upl:         strings.TrimSpace(position.UnRealizedProfit),
		UplRatio:    binanceUPLRatio(position),
		Lever:       lever,
		LiqPx:       strings.TrimSpace(position.LiquidationPrice),
		NotionalUsd: notional,
		Margin:      margin,
		Adl:         strconv.Itoa(position.Adl),
		UTime:       strconv.FormatInt(position.UpdateTime, 10),
		Ccy:         strings.TrimSpace(position.MarginAsset),
	}
}

func gatePositionEntryFills(trades []gate.MyTrade) []positionEntryFill {
	fills := make([]positionEntryFill, 0, len(trades))
	for _, trade := range trades {
		size, err := strconv.ParseFloat(strings.TrimSpace(trade.Size), 64)
		if err != nil || math.Abs(size) <= positionEntrySizeEpsilon {
			continue
		}
		fillTime, ok := gateUnixTime(trade.CreateTime)
		if !ok {
			continue
		}
		side := "buy"
		if size < 0 {
			side = "sell"
		}
		fills = append(fills, positionEntryFill{
			InstID:   strings.ToUpper(strings.TrimSpace(trade.Contract)),
			OrdID:    strings.TrimSpace(trade.OrderID),
			ClOrdID:  strings.TrimSpace(trade.Text),
			Side:     side,
			Size:     math.Abs(size),
			FillTime: fillTime,
		})
	}
	return fills
}

func gateUnixTime(raw string) (time.Time, bool) {
	seconds, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || seconds <= 0 || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		return time.Time{}, false
	}
	whole := math.Floor(seconds)
	nanos := math.Round((seconds - whole) * 1e9)
	if nanos >= 1e9 {
		whole++
		nanos = 0
	}
	return time.Unix(int64(whole), int64(nanos)).UTC(), true
}

func gateTimeMillis(raw string) string {
	value, ok := gateUnixTime(raw)
	if !ok {
		return ""
	}
	return strconv.FormatInt(value.UnixMilli(), 10)
}

func gatePositionToOKX(position gate.Position) okx.Position {
	size := strings.TrimSpace(position.Size)
	posSide := "net"
	if value, err := strconv.ParseFloat(size, 64); err == nil {
		if value > 0 {
			posSide = "long"
		} else if value < 0 {
			posSide = "short"
		}
	}
	notional := strings.TrimSpace(position.Value)
	if notional == "" {
		if sizeValue, sizeErr := strconv.ParseFloat(strings.TrimSpace(size), 64); sizeErr == nil {
			mark, markErr := strconv.ParseFloat(strings.TrimSpace(position.MarkPrice), 64)
			if markErr == nil && mark > 0 {
				// Gate's position endpoint normally supplies value directly. This
				// fallback preserves compatibility with older responses that omitted it.
				notional = trading.NormalizeFloat(math.Abs(sizeValue) * mark)
			}
		}
	}
	margin := firstNonEmpty(strings.TrimSpace(position.Margin), strings.TrimSpace(position.InitialMargin))
	if margin == "" {
		if notionalValue, notionalErr := strconv.ParseFloat(notional, 64); notionalErr == nil && notionalValue > 0 {
			leverage, leverageErr := strconv.ParseFloat(firstNonEmpty(position.Lever, position.Leverage), 64)
			if leverageErr == nil && leverage > 0 {
				margin = trading.NormalizeFloat(notionalValue / leverage)
			}
		}
	}
	upl := strings.TrimSpace(position.UnrealisedPnl)
	uplRatio := ""
	if uplValue, uplErr := strconv.ParseFloat(upl, 64); uplErr == nil {
		if marginValue, marginErr := strconv.ParseFloat(margin, 64); marginErr == nil && marginValue != 0 {
			uplRatio = trading.NormalizeFloat(uplValue / math.Abs(marginValue))
		}
	}
	return okx.Position{
		InstType:    "USDT-M",
		InstID:      strings.ToUpper(strings.TrimSpace(position.Contract)),
		MgnMode:     strings.ToLower(strings.TrimSpace(position.MarginMode)),
		PosSide:     posSide,
		Pos:         size,
		AvailPos:    size,
		AvgPx:       strings.TrimSpace(position.EntryPrice),
		MarkPx:      strings.TrimSpace(position.MarkPrice),
		Upl:         upl,
		UplRatio:    uplRatio,
		Lever:       firstNonEmpty(position.Lever, position.Leverage),
		LiqPx:       strings.TrimSpace(position.LiquidationPrice),
		NotionalUsd: notional,
		Margin:      margin,
		CTime:       gateTimeMillis(position.OpenTime),
		UTime:       gateTimeMillis(position.UpdateTime),
		Ccy:         "USDT",
	}
}

func gateOrderToOKX(order gate.Order) okx.PendingOrder {
	size := strings.TrimSpace(order.Size)
	side := "buy"
	if value, err := strconv.ParseFloat(size, 64); err == nil && value < 0 {
		side = "sell"
	}
	filled := ""
	if total, totalErr := strconv.ParseFloat(strings.TrimSpace(order.Size), 64); totalErr == nil {
		left, leftErr := strconv.ParseFloat(strings.TrimSpace(order.Left), 64)
		if leftErr == nil {
			filled = trading.NormalizeFloat(math.Abs(total) - math.Abs(left))
		}
	}
	ordType := "limit"
	if strings.TrimSpace(order.Price) == "0" || strings.TrimSpace(order.Price) == "" {
		ordType = "market"
	}
	return okx.PendingOrder{
		InstType:      "USDT-M",
		InstID:        strings.ToUpper(strings.TrimSpace(order.Contract)),
		OrdID:         order.IDString(),
		ClOrdID:       strings.TrimSpace(order.Text),
		Side:          side,
		PosSide:       "net",
		OrdType:       ordType,
		Px:            strings.TrimSpace(order.Price),
		Sz:            strings.TrimLeft(size, "-"),
		AccFillSz:     filled,
		AvgPx:         strings.TrimSpace(order.FillPrice),
		State:         strings.ToLower(strings.TrimSpace(order.Status)),
		ReduceOnly:    json.RawMessage(strconv.FormatBool(order.IsReduceOnly)),
		ClosePosition: json.RawMessage(strconv.FormatBool(order.IsClose)),
		CTime:         strings.TrimSpace(order.CreateTime),
		UTime:         strings.TrimSpace(order.UpdateTime),
	}
}

func binancePositionNotional(position binance.Position) string {
	if notional := strings.TrimLeft(strings.TrimSpace(position.Notional), "-"); notional != "" && notional != "0" {
		return notional
	}
	size, sizeErr := strconv.ParseFloat(strings.TrimSpace(position.PositionAmt), 64)
	if sizeErr != nil || size == 0 {
		return ""
	}
	priceRaw := strings.TrimSpace(position.MarkPrice)
	if priceRaw == "" || priceRaw == "0" {
		priceRaw = strings.TrimSpace(position.EntryPrice)
	}
	price, priceErr := strconv.ParseFloat(priceRaw, 64)
	if priceErr != nil || price <= 0 {
		return ""
	}
	return trading.NormalizeFloat(math.Abs(size) * price)
}

func binancePositionLeverage(position binance.Position, notionalRaw string) string {
	if leverage := normalizeBinanceLeverage(position.Leverage); leverage != "" {
		return leverage
	}
	for _, marginRaw := range []string{position.PositionInitialMargin, position.InitialMargin} {
		if leverage := deriveBinanceLeverage(notionalRaw, marginRaw); leverage != "" {
			return leverage
		}
	}
	return ""
}

func normalizeBinanceLeverage(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "0" || raw == "-" {
		return ""
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return raw
	}
	if value <= 0 {
		return ""
	}
	return strconv.FormatInt(int64(math.Round(value)), 10)
}

func deriveBinanceLeverage(notionalRaw, marginRaw string) string {
	notional, notionalErr := strconv.ParseFloat(strings.TrimSpace(notionalRaw), 64)
	margin, marginErr := strconv.ParseFloat(strings.TrimSpace(marginRaw), 64)
	if notionalErr != nil || marginErr != nil || notional <= 0 || margin <= 0 {
		return ""
	}
	leverage := math.Round(notional / margin)
	if leverage <= 0 {
		return ""
	}
	return strconv.FormatInt(int64(leverage), 10)
}

func binanceOpenOrderToOKX(order binance.OpenOrder) okx.PendingOrder {
	reduceOnly := json.RawMessage("false")
	if order.ReduceOnly {
		reduceOnly = json.RawMessage("true")
	}
	closePosition := json.RawMessage("false")
	if order.ClosePosition {
		closePosition = json.RawMessage("true")
	}
	return okx.PendingOrder{
		InstType:      "USDT-M",
		InstID:        strings.ToUpper(strings.TrimSpace(order.Symbol)),
		OrdID:         strconv.FormatInt(order.OrderID, 10),
		ClOrdID:       strings.TrimSpace(order.ClientOrderID),
		Side:          strings.ToLower(strings.TrimSpace(order.Side)),
		PosSide:       strings.ToLower(strings.TrimSpace(order.PositionSide)),
		OrdType:       strings.ToLower(strings.TrimSpace(order.Type)),
		Px:            strings.TrimSpace(order.Price),
		Sz:            strings.TrimSpace(order.OrigQty),
		AccFillSz:     strings.TrimSpace(order.ExecutedQty),
		AvgPx:         strings.TrimSpace(order.AvgPrice),
		State:         strings.ToLower(strings.TrimSpace(order.Status)),
		ReduceOnly:    reduceOnly,
		ClosePosition: closePosition,
		CTime:         strconv.FormatInt(order.Time, 10),
		UTime:         strconv.FormatInt(order.UpdateTime, 10),
	}
}

func okxAlgoOrderToPendingView(order okx.AlgoOrder) pendingOrderView {
	triggerPx := okxAlgoOrderTriggerPx(order)
	activationPx := strings.TrimSpace(order.ActivePx)
	callbackRatio := firstNonEmpty(order.CallbackRatio, order.CallbackSpread)
	reason := okxAlgoOrderChaseUnavailableReason(order)
	reduceOnly := order.ReduceOnly
	if len(reduceOnly) == 0 {
		reduceOnly = json.RawMessage("false")
	}
	return pendingOrderView{
		PendingOrder: okx.PendingOrder{
			InstType:   strings.ToUpper(strings.TrimSpace(order.InstType)),
			InstID:     strings.ToUpper(strings.TrimSpace(order.InstID)),
			TDMode:     strings.ToLower(strings.TrimSpace(order.TDMode)),
			Side:       strings.ToLower(strings.TrimSpace(order.Side)),
			PosSide:    strings.ToLower(strings.TrimSpace(order.PosSide)),
			OrdType:    strings.ToLower(strings.TrimSpace(order.OrdType)),
			Px:         firstNonEmpty(triggerPx, activationPx),
			Sz:         strings.TrimSpace(order.Sz),
			AccFillSz:  strings.TrimSpace(order.ActualSz),
			State:      strings.ToLower(strings.TrimSpace(order.State)),
			ReduceOnly: reduceOnly,
			CTime:      strings.TrimSpace(order.CTime),
			UTime:      strings.TrimSpace(order.UTime),
		},
		OrderGroup:             "algo",
		AlgoID:                 strings.TrimSpace(order.AlgoID),
		AlgoClOrdID:            strings.TrimSpace(order.AlgoClOrdID),
		TriggerPx:              triggerPx,
		ActivationPx:           activationPx,
		CallbackRatio:          callbackRatio,
		Chaseable:              reason == "",
		ChaseUnavailableReason: reason,
	}
}

func binanceAlgoOrderToPendingView(order binance.AlgoOpenOrder) pendingOrderView {
	triggerPx := strings.TrimSpace(order.TriggerPrice)
	activationPx := strings.TrimSpace(order.ActivatePrice)
	callbackRatio := firstNonEmpty(order.CallbackRate, order.PriceRate)
	reason := binanceAlgoOrderChaseUnavailableReason(order)
	reduceOnly := json.RawMessage("false")
	if order.ReduceOnly {
		reduceOnly = json.RawMessage("true")
	}
	closePosition := json.RawMessage("false")
	if order.ClosePosition {
		closePosition = json.RawMessage("true")
	}
	return pendingOrderView{
		PendingOrder: okx.PendingOrder{
			InstType:      "USDT-M",
			InstID:        strings.ToUpper(strings.TrimSpace(order.Symbol)),
			Side:          strings.ToLower(strings.TrimSpace(order.Side)),
			PosSide:       strings.ToLower(strings.TrimSpace(order.PositionSide)),
			OrdType:       strings.ToLower(strings.TrimSpace(order.OrderType)),
			Px:            firstNonEmpty(triggerPx, activationPx),
			Sz:            strings.TrimSpace(order.Quantity),
			State:         strings.ToLower(strings.TrimSpace(order.AlgoStatus)),
			ReduceOnly:    reduceOnly,
			ClosePosition: closePosition,
			CTime:         strconv.FormatInt(order.CreateTime, 10),
			UTime:         strconv.FormatInt(order.UpdateTime, 10),
		},
		OrderGroup:             "algo",
		AlgoID:                 strconv.FormatInt(order.AlgoID, 10),
		AlgoClOrdID:            strings.TrimSpace(order.ClientAlgoID),
		TriggerPx:              triggerPx,
		ActivationPx:           activationPx,
		CallbackRatio:          callbackRatio,
		Chaseable:              reason == "",
		ChaseUnavailableReason: reason,
	}
}

func okxAlgoOrderTriggerPx(order okx.AlgoOrder) string {
	return firstNonEmpty(order.TPTriggerPx, order.SLTriggerPx, order.TriggerPx, order.MoveTriggerPx)
}

func okxAlgoOrderChaseUnavailableReason(order okx.AlgoOrder) string {
	switch strings.ToLower(strings.TrimSpace(order.OrdType)) {
	case "conditional", "trigger", "move_order_stop":
		return ""
	case "iceberg", "twap", "oco":
		return "该算法订单类型只支持取消"
	default:
		return "该算法订单类型不支持追单"
	}
}

func binanceAlgoOrderChaseUnavailableReason(order binance.AlgoOpenOrder) string {
	switch strings.ToUpper(strings.TrimSpace(order.OrderType)) {
	case "STOP_MARKET", "TAKE_PROFIT_MARKET", "STOP", "TAKE_PROFIT", "TRAILING_STOP_MARKET":
		return ""
	default:
		return "该算法订单类型不支持追单"
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func binanceUPLRatio(position binance.Position) string {
	upl, err1 := strconv.ParseFloat(strings.TrimSpace(position.UnRealizedProfit), 64)
	notional, err2 := strconv.ParseFloat(strings.TrimLeft(strings.TrimSpace(position.Notional), "-"), 64)
	if err1 != nil || err2 != nil || notional <= 0 {
		return ""
	}
	return strconv.FormatFloat(upl/notional, 'f', 8, 64)
}

func (s *Server) pendingOrderViews(ctx context.Context, cfg config.Config, client okx.Client, apiID string, orders []okx.PendingOrder) []pendingOrderView {
	views := make([]pendingOrderView, 0, len(orders))
	tickers := map[string]okx.Ticker{}
	instruments := map[string]okx.Instrument{}
	for _, order := range orders {
		unavailableReason := pendingOrderChaseUnavailableReason(order)
		view := pendingOrderView{
			PendingOrder:           order,
			OrderGroup:             "normal",
			Chaseable:              unavailableReason == "",
			ChaseUnavailableReason: unavailableReason,
			Chasing: pendingOrderChaseJobs.activeKey(pendingOrderChaseKey(pendingOrderChaseRequest{
				APIID:   apiID,
				InstID:  order.InstID,
				OrdID:   order.OrdID,
				ClOrdID: order.ClOrdID,
			})),
		}
		if unavailableReason == "" {
			midPx, chasePx, err := pendingOrderMidAndChasePrice(ctx, client, order, tickers, instruments)
			if err != nil {
				view.PriceError = err.Error()
				view.Chaseable = false
			} else {
				view.MidPx = midPx
				view.ChasePx = chasePx
			}
		}
		if inst, ok := instruments[strings.ToUpper(strings.TrimSpace(order.InstID))]; ok {
			applyPendingOrderViewPrecision(&view, precisionFromOKXInstrument(inst))
			view.Margin = pendingOrderMargin(order, view.MidPx, inst.CtVal, cfg.Trading.Leverage)
		}
		views = append(views, view)
	}
	return views
}

func (s *Server) pendingAlgoOrderViews(ctx context.Context, cfg config.Config, client okx.Client, apiID string, orders []okx.AlgoOrder) []pendingOrderView {
	views := make([]pendingOrderView, 0, len(orders))
	tickers := map[string]okx.Ticker{}
	instruments := map[string]okx.Instrument{}
	for _, order := range orders {
		view := okxAlgoOrderToPendingView(order)
		view.Chasing = pendingOrderChaseJobs.activeKey(pendingOrderChaseKey(pendingOrderChaseRequest{
			APIID:       apiID,
			OrderGroup:  "algo",
			InstID:      view.InstID,
			AlgoID:      view.AlgoID,
			AlgoClOrdID: view.AlgoClOrdID,
		}))
		if view.Chaseable {
			midPx, chasePx, err := pendingAlgoOrderMidAndChasePrice(ctx, client, view.PendingOrder, tickers, instruments)
			if err != nil {
				view.PriceError = err.Error()
			} else {
				view.MidPx = midPx
				view.ChasePx = chasePx
			}
		}
		if inst, ok := instruments[strings.ToUpper(strings.TrimSpace(view.InstID))]; ok {
			applyPendingOrderViewPrecision(&view, precisionFromOKXInstrument(inst))
			view.Margin = pendingOrderMargin(view.PendingOrder, firstNonEmpty(view.TriggerPx, view.ActivationPx, view.MidPx), inst.CtVal, cfg.Trading.Leverage)
		}
		views = append(views, view)
	}
	sort.Slice(views, func(i, j int) bool {
		if views[i].InstID == views[j].InstID {
			return views[i].UTime > views[j].UTime
		}
		return views[i].InstID < views[j].InstID
	})
	return views
}

func (s *Server) binancePendingOrderViews(ctx context.Context, cfg config.Config, client binance.Client, apiID string, orders []binance.OpenOrder) []pendingOrderView {
	views := make([]pendingOrderView, 0, len(orders))
	tickers := map[string]binance.BookTicker{}
	instruments := map[string]binance.SymbolInfo{}
	for _, rawOrder := range orders {
		order := binanceOpenOrderToOKX(rawOrder)
		view := pendingOrderView{
			PendingOrder: order,
			OrderGroup:   "normal",
			Chaseable:    true,
			Chasing: pendingOrderChaseJobs.activeKey(pendingOrderChaseKey(pendingOrderChaseRequest{
				Exchange: trading.ExchangeBinance,
				APIID:    apiID,
				InstID:   order.InstID,
				OrdID:    order.OrdID,
				ClOrdID:  order.ClOrdID,
			})),
		}
		midPx, chasePx, err := binancePendingOrderMidAndChasePrice(ctx, client, order, tickers, instruments)
		if err != nil {
			view.PriceError = err.Error()
		} else {
			view.MidPx = midPx
			view.ChasePx = chasePx
		}
		if inst, ok := instruments[strings.ToUpper(strings.TrimSpace(order.InstID))]; ok {
			applyPendingOrderViewPrecision(&view, precisionFromBinanceSymbol(inst))
		}
		view.Margin = pendingOrderMargin(order, view.MidPx, "1", cfg.Trading.Leverage)
		views = append(views, view)
	}
	return views
}

func (s *Server) binancePendingAlgoOrderViews(ctx context.Context, cfg config.Config, client binance.Client, apiID string, orders []binance.AlgoOpenOrder) []pendingOrderView {
	views := make([]pendingOrderView, 0, len(orders))
	tickers := map[string]binance.BookTicker{}
	instruments := map[string]binance.SymbolInfo{}
	for _, order := range orders {
		view := binanceAlgoOrderToPendingView(order)
		view.Chasing = pendingOrderChaseJobs.activeKey(pendingOrderChaseKey(pendingOrderChaseRequest{
			Exchange:    trading.ExchangeBinance,
			APIID:       apiID,
			OrderGroup:  "algo",
			InstID:      view.InstID,
			AlgoID:      view.AlgoID,
			AlgoClOrdID: view.AlgoClOrdID,
		}))
		if view.Chaseable {
			midPx, chasePx, err := binancePendingAlgoOrderMidAndChasePrice(ctx, client, view.PendingOrder, tickers, instruments)
			if err != nil {
				view.PriceError = err.Error()
			} else {
				view.MidPx = midPx
				view.ChasePx = chasePx
			}
		}
		if inst, ok := instruments[strings.ToUpper(strings.TrimSpace(view.InstID))]; ok {
			applyPendingOrderViewPrecision(&view, precisionFromBinanceSymbol(inst))
		}
		view.Margin = pendingOrderMargin(view.PendingOrder, firstNonEmpty(view.TriggerPx, view.ActivationPx, view.MidPx), "1", cfg.Trading.Leverage)
		views = append(views, view)
	}
	sort.Slice(views, func(i, j int) bool {
		if views[i].InstID == views[j].InstID {
			return views[i].UTime > views[j].UTime
		}
		return views[i].InstID < views[j].InstID
	})
	return views
}

func (s *Server) watchPendingOrderChase(ctx context.Context, cfg config.Config, client okx.Client, req pendingOrderChaseRequest) {
	key := pendingOrderChaseKey(req)
	defer pendingOrderChaseJobs.done(key)
	interval := pendingOrderChaseInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	timeout := pendingOrderChaseTimeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	timeoutTimer := time.NewTimer(timeout)
	defer timeoutTimer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			stepCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			_, closed, err := chasePendingOrderOnce(stepCtx, cfg, client, req, false)
			cancel()
			if err != nil {
				if s.Logger != nil {
					s.Logger.Warn("pending order chase failed", "api_id", req.APIID, "inst_id", req.InstID, "ord_id", req.OrdID, "cl_ord_id", req.ClOrdID, "error", err)
				}
				continue
			}
			if closed {
				return
			}
		case <-timeoutTimer.C:
			stepCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			_, _, err := fallbackPendingOrderMarket(stepCtx, cfg, client, req)
			cancel()
			if err != nil && s.Logger != nil {
				s.Logger.Warn("pending order chase market fallback failed", "api_id", req.APIID, "inst_id", req.InstID, "ord_id", req.OrdID, "cl_ord_id", req.ClOrdID, "error", err)
			}
			return
		}
	}
}

func preparePendingOrderChase(ctx context.Context, cfg config.Config, client okx.Client, req pendingOrderChaseRequest) (pendingOrderChaseRequest, pendingOrderChaseResponse, bool, error) {
	order, found, err := currentPendingOrder(ctx, client, req)
	if err != nil {
		return req, pendingOrderChaseResponse{}, false, err
	}
	resp := pendingOrderChaseResponse{
		OK:      true,
		APIID:   req.APIID,
		InstID:  req.InstID,
		OrdID:   req.OrdID,
		ClOrdID: req.ClOrdID,
	}
	if !found {
		resp.Status = "finished"
		resp.Message = "pending order is no longer open"
		return req, resp, true, nil
	}
	resp.OrdID = order.OrdID
	resp.ClOrdID = order.ClOrdID
	if reason := pendingOrderChaseUnavailableReason(order); reason != "" {
		return req, resp, false, errors.New(reason)
	}
	midPx, chasePx, err := pendingOrderMidAndChasePrice(ctx, client, order, nil, nil)
	if err != nil {
		return req, resp, false, err
	}
	if err := validatePendingOrderChasePx(chasePx); err != nil {
		return req, resp, false, err
	}
	resp.MidPx = midPx
	resp.Px = chasePx
	if shouldRebuildPendingOrderWithProtection(cfg, order) {
		remaining, err := pendingOrderRemainingSize(order)
		if err != nil {
			if errors.Is(err, errPendingOrderNoRemaining) {
				resp.Status = "finished"
				resp.Message = "pending order has no remaining size"
				return req, resp, true, nil
			}
			return req, resp, false, err
		}
		if err := cancelPendingOrder(ctx, client, order); err != nil {
			if _, stillOpen, checkErr := currentPendingOrder(ctx, client, req); checkErr == nil && !stillOpen {
				resp.Status = "finished"
				resp.Message = "pending order is no longer open"
				return req, resp, true, nil
			}
			return req, resp, false, err
		}
		limitReq, err := pendingOrderLimitRequest(cfg, order, remaining, chasePx)
		if err != nil {
			return req, resp, false, err
		}
		ack, _, err := client.PlaceOrder(ctx, limitReq)
		if err != nil {
			return req, resp, false, err
		}
		req.OrdID = strings.TrimSpace(ack.OrdID)
		req.ClOrdID = strings.TrimSpace(ack.ClOrdID)
		if req.ClOrdID == "" {
			req.ClOrdID = limitReq.ClOrdID
		}
		resp.Status = "rebuilt"
		resp.Message = "pending order rebuilt with risk controls"
		resp.OrdID = req.OrdID
		resp.ClOrdID = req.ClOrdID
		return req, resp, false, nil
	}
	result, closed, err := chasePendingOrderFromOrder(ctx, cfg, client, req, order, true)
	return req, result, closed, err
}

func rebuildPendingOrderRiskAtCurrentPrice(ctx context.Context, cfg config.Config, client okx.Client, req pendingOrderChaseRequest) (pendingOrderChaseResponse, error) {
	order, found, err := currentPendingOrder(ctx, client, req)
	if err != nil {
		return pendingOrderChaseResponse{}, err
	}
	resp := pendingOrderChaseResponse{
		OK:         true,
		APIID:      req.APIID,
		OrderGroup: req.OrderGroup,
		InstID:     req.InstID,
		OrdID:      req.OrdID,
		ClOrdID:    req.ClOrdID,
	}
	if !found {
		resp.Status = "finished"
		resp.Message = "pending order is no longer open"
		return resp, nil
	}
	resp.OrdID = order.OrdID
	resp.ClOrdID = order.ClOrdID
	if reason := pendingOrderChaseUnavailableReason(order); reason != "" {
		return resp, errors.New(reason)
	}
	attachAlgoOrds := pendingOrderAttachAlgoOrders(cfg, order, "PENDINGRISK")
	if len(attachAlgoOrds) == 0 {
		resp.Status = "unchanged"
		resp.Px = strings.TrimSpace(order.Px)
		resp.Message = "current order settings do not attach risk controls"
		return resp, nil
	}
	remaining, err := pendingOrderRemainingSize(order)
	if err != nil {
		if errors.Is(err, errPendingOrderNoRemaining) {
			resp.Status = "finished"
			resp.Message = "pending order has no remaining size"
			return resp, nil
		}
		return resp, err
	}
	px := strings.TrimSpace(order.Px)
	if err := validatePendingOrderChasePx(px); err != nil {
		return resp, err
	}
	if err := cancelPendingOrder(ctx, client, order); err != nil {
		if _, stillOpen, checkErr := currentPendingOrder(ctx, client, req); checkErr == nil && !stillOpen {
			resp.Status = "finished"
			resp.Message = "pending order is no longer open"
			return resp, nil
		}
		return resp, err
	}
	limitReq, err := pendingOrderLimitRequest(cfg, order, remaining, px)
	if err != nil {
		return resp, err
	}
	ack, _, err := client.PlaceOrder(ctx, limitReq)
	if err != nil {
		return resp, err
	}
	resp.Status = "rebuilt"
	resp.Message = "pending order rebuilt with current risk controls"
	resp.OrdID = strings.TrimSpace(ack.OrdID)
	resp.ClOrdID = strings.TrimSpace(ack.ClOrdID)
	if resp.ClOrdID == "" {
		resp.ClOrdID = limitReq.ClOrdID
	}
	resp.Px = px
	return resp, nil
}

func chasePendingOrderOnce(ctx context.Context, cfg config.Config, client okx.Client, req pendingOrderChaseRequest, forceAttach bool) (pendingOrderChaseResponse, bool, error) {
	order, found, err := currentPendingOrder(ctx, client, req)
	if err != nil {
		return pendingOrderChaseResponse{}, false, err
	}
	resp := pendingOrderChaseResponse{
		OK:      true,
		APIID:   req.APIID,
		InstID:  req.InstID,
		OrdID:   req.OrdID,
		ClOrdID: req.ClOrdID,
	}
	if !found {
		resp.Status = "finished"
		resp.Message = "pending order is no longer open"
		return resp, true, nil
	}
	return chasePendingOrderFromOrder(ctx, cfg, client, req, order, forceAttach)
}

func chasePendingOrderFromOrder(ctx context.Context, cfg config.Config, client okx.Client, req pendingOrderChaseRequest, order okx.PendingOrder, forceAttach bool) (pendingOrderChaseResponse, bool, error) {
	resp := pendingOrderChaseResponse{
		OK:      true,
		APIID:   req.APIID,
		InstID:  req.InstID,
		OrdID:   req.OrdID,
		ClOrdID: req.ClOrdID,
	}
	resp.OrdID = order.OrdID
	resp.ClOrdID = order.ClOrdID
	if reason := pendingOrderChaseUnavailableReason(order); reason != "" {
		return resp, false, errors.New(reason)
	}
	midPx, chasePx, err := pendingOrderMidAndChasePrice(ctx, client, order, nil, nil)
	if err != nil {
		return resp, false, err
	}
	if err := validatePendingOrderChasePx(chasePx); err != nil {
		return resp, false, err
	}
	resp.MidPx = midPx
	resp.Px = chasePx
	if pendingOrderPriceEquivalent(order.Px, chasePx) {
		resp.Status = "unchanged"
		resp.Message = "pending order price is already at chase price"
		return resp, false, nil
	}
	amendReq := okx.AmendOrderRequest{
		InstID: order.InstID,
		NewPx:  chasePx,
	}
	if strings.TrimSpace(order.OrdID) != "" {
		amendReq.OrdID = order.OrdID
	} else {
		amendReq.ClOrdID = order.ClOrdID
	}
	if _, _, err := client.AmendOrder(ctx, amendReq); err != nil {
		return resp, false, err
	}
	resp.Status = "amended"
	resp.Message = "pending order price amended"
	return resp, false, nil
}

func fallbackPendingOrderMarket(ctx context.Context, cfg config.Config, client okx.Client, req pendingOrderChaseRequest) (pendingOrderChaseResponse, bool, error) {
	order, found, err := currentPendingOrder(ctx, client, req)
	if err != nil {
		return pendingOrderChaseResponse{}, false, err
	}
	resp := pendingOrderChaseResponse{
		OK:      true,
		APIID:   req.APIID,
		InstID:  req.InstID,
		OrdID:   req.OrdID,
		ClOrdID: req.ClOrdID,
	}
	if !found {
		resp.Status = "finished"
		resp.Message = "pending order is no longer open"
		return resp, true, nil
	}
	resp.OrdID = order.OrdID
	resp.ClOrdID = order.ClOrdID
	remaining, err := pendingOrderRemainingSize(order)
	if err != nil {
		if errors.Is(err, errPendingOrderNoRemaining) {
			resp.Status = "finished"
			resp.Message = "pending order has no remaining size"
			return resp, true, nil
		}
		return resp, false, err
	}
	if err := cancelPendingOrder(ctx, client, order); err != nil {
		if _, stillOpen, checkErr := currentPendingOrder(ctx, client, req); checkErr == nil && !stillOpen {
			resp.Status = "finished"
			resp.Message = "pending order is no longer open"
			return resp, true, nil
		}
		return resp, false, err
	}
	marketReq, err := pendingOrderMarketRequest(cfg, order, remaining)
	if err != nil {
		return resp, false, err
	}
	ack, _, err := client.PlaceOrder(ctx, marketReq)
	if err != nil {
		return resp, false, err
	}
	resp.Status = "market_submitted"
	resp.Message = "pending order canceled and market order submitted"
	resp.Px = ""
	if strings.TrimSpace(ack.OrdID) != "" {
		resp.OrdID = ack.OrdID
	}
	if strings.TrimSpace(ack.ClOrdID) != "" {
		resp.ClOrdID = ack.ClOrdID
	}
	return resp, false, nil
}

func (s *Server) watchBinancePendingOrderChase(ctx context.Context, client binance.Client, req pendingOrderChaseRequest) {
	key := pendingOrderChaseKey(req)
	defer pendingOrderChaseJobs.done(key)
	interval := pendingOrderChaseInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	timeout := pendingOrderChaseTimeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	timeoutTimer := time.NewTimer(timeout)
	defer timeoutTimer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			stepCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			_, closed, err := chaseBinancePendingOrderOnce(stepCtx, client, req)
			cancel()
			if err != nil {
				if s.Logger != nil {
					s.Logger.Warn("Binance pending order chase failed", "api_id", req.APIID, "symbol", req.InstID, "ord_id", req.OrdID, "cl_ord_id", req.ClOrdID, "error", err)
				}
				continue
			}
			if closed {
				return
			}
		case <-timeoutTimer.C:
			stepCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			_, _, err := fallbackBinancePendingOrderMarket(stepCtx, client, req)
			cancel()
			if err != nil && s.Logger != nil {
				s.Logger.Warn("Binance pending order chase market fallback failed", "api_id", req.APIID, "symbol", req.InstID, "ord_id", req.OrdID, "cl_ord_id", req.ClOrdID, "error", err)
			}
			return
		}
	}
}

func prepareBinancePendingOrderChase(ctx context.Context, client binance.Client, req pendingOrderChaseRequest) (pendingOrderChaseResponse, bool, error) {
	return chaseBinancePendingOrderOnce(ctx, client, req)
}

func chaseBinancePendingOrderOnce(ctx context.Context, client binance.Client, req pendingOrderChaseRequest) (pendingOrderChaseResponse, bool, error) {
	order, found, err := currentBinancePendingOrder(ctx, client, req)
	if err != nil {
		return pendingOrderChaseResponse{}, false, err
	}
	resp := pendingOrderChaseResponse{
		OK:      true,
		APIID:   req.APIID,
		InstID:  req.InstID,
		OrdID:   req.OrdID,
		ClOrdID: req.ClOrdID,
	}
	if !found {
		resp.Status = "finished"
		resp.Message = "pending order is no longer open"
		return resp, true, nil
	}
	return chaseBinancePendingOrderFromOrder(ctx, client, req, order)
}

func chaseBinancePendingOrderFromOrder(ctx context.Context, client binance.Client, req pendingOrderChaseRequest, order okx.PendingOrder) (pendingOrderChaseResponse, bool, error) {
	resp := pendingOrderChaseResponse{
		OK:      true,
		APIID:   req.APIID,
		InstID:  req.InstID,
		OrdID:   order.OrdID,
		ClOrdID: order.ClOrdID,
	}
	midPx, chasePx, err := binancePendingOrderMidAndChasePrice(ctx, client, order, nil, nil)
	if err != nil {
		return resp, false, err
	}
	resp.MidPx = midPx
	resp.Px = chasePx
	if strings.TrimSpace(order.Px) == chasePx {
		resp.Status = "unchanged"
		resp.Message = "pending order price is already at chase price"
		return resp, false, nil
	}
	ack, err := client.ModifyOrder(ctx, binance.ModifyOrderRequest{
		Symbol:            order.InstID,
		Side:              order.Side,
		Quantity:          strings.TrimSpace(order.Sz),
		Price:             chasePx,
		OrderID:           strings.TrimSpace(order.OrdID),
		OrigClientOrderID: strings.TrimSpace(order.ClOrdID),
	})
	if err != nil {
		return resp, false, err
	}
	resp.Status = "amended"
	resp.Message = "pending order price amended"
	if ack.OrderID != 0 {
		resp.OrdID = strconv.FormatInt(ack.OrderID, 10)
	}
	if strings.TrimSpace(ack.ClientOrderID) != "" {
		resp.ClOrdID = strings.TrimSpace(ack.ClientOrderID)
	}
	return resp, false, nil
}

func fallbackBinancePendingOrderMarket(ctx context.Context, client binance.Client, req pendingOrderChaseRequest) (pendingOrderChaseResponse, bool, error) {
	order, found, err := currentBinancePendingOrder(ctx, client, req)
	if err != nil {
		return pendingOrderChaseResponse{}, false, err
	}
	resp := pendingOrderChaseResponse{
		OK:      true,
		APIID:   req.APIID,
		InstID:  req.InstID,
		OrdID:   req.OrdID,
		ClOrdID: req.ClOrdID,
	}
	if !found {
		resp.Status = "finished"
		resp.Message = "pending order is no longer open"
		return resp, true, nil
	}
	resp.OrdID = order.OrdID
	resp.ClOrdID = order.ClOrdID
	remaining, err := pendingOrderRemainingSize(order)
	if err != nil {
		if errors.Is(err, errPendingOrderNoRemaining) {
			resp.Status = "finished"
			resp.Message = "pending order has no remaining size"
			return resp, true, nil
		}
		return resp, false, err
	}
	if err := cancelBinancePendingOrder(ctx, client, order); err != nil {
		if _, stillOpen, checkErr := currentBinancePendingOrder(ctx, client, req); checkErr == nil && !stillOpen {
			resp.Status = "finished"
			resp.Message = "pending order is no longer open"
			return resp, true, nil
		}
		return resp, false, err
	}
	marketReq := binance.PlaceOrderRequest{
		Symbol:           order.InstID,
		Side:             order.Side,
		PositionSide:     binancePendingPositionSide(order.PosSide),
		Type:             "MARKET",
		Quantity:         remaining,
		NewClientOrderID: nextPendingOrderMarketClOrdID(),
	}
	if okxRawBool(order.ReduceOnly) && (marketReq.PositionSide == "" || marketReq.PositionSide == "BOTH") {
		marketReq.ReduceOnly = true
	}
	ack, err := client.PlaceOrder(ctx, marketReq)
	if err != nil {
		return resp, false, err
	}
	resp.Status = "market_submitted"
	resp.Message = "pending order canceled and market order submitted"
	if ack.OrderID != 0 {
		resp.OrdID = strconv.FormatInt(ack.OrderID, 10)
	}
	if strings.TrimSpace(ack.ClientOrderID) != "" {
		resp.ClOrdID = strings.TrimSpace(ack.ClientOrderID)
	}
	return resp, false, nil
}

func (s *Server) watchOKXPendingAlgoOrderChase(ctx context.Context, cfg config.Config, client okx.Client, req pendingOrderChaseRequest) {
	key := pendingOrderChaseKey(req)
	defer pendingOrderChaseJobs.done(key)
	interval := pendingOrderChaseInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	timeout := pendingOrderChaseTimeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	timeoutTimer := time.NewTimer(timeout)
	defer timeoutTimer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			stepCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			_, closed, err := chaseOKXPendingAlgoOrderOnce(stepCtx, cfg, client, req)
			cancel()
			if err != nil {
				if s.Logger != nil {
					s.Logger.Warn("OKX pending algo order chase failed", "api_id", req.APIID, "inst_id", req.InstID, "algo_id", req.AlgoID, "algo_cl_ord_id", req.AlgoClOrdID, "error", err)
				}
				continue
			}
			if closed {
				return
			}
		case <-timeoutTimer.C:
			return
		}
	}
}

func (s *Server) watchBinancePendingAlgoOrderChase(ctx context.Context, client binance.Client, req pendingOrderChaseRequest) {
	activeKey := pendingOrderChaseKey(req)
	defer func() { pendingOrderChaseJobs.done(activeKey) }()
	interval := pendingOrderChaseInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	timeout := pendingOrderChaseTimeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	timeoutTimer := time.NewTimer(timeout)
	defer timeoutTimer.Stop()
	activeReq := req
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			stepCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			_, closed, nextReq, err := chaseBinancePendingAlgoOrderOnce(stepCtx, client, activeReq)
			cancel()
			if err != nil {
				if s.Logger != nil {
					s.Logger.Warn("Binance pending algo order chase failed", "api_id", activeReq.APIID, "symbol", activeReq.InstID, "algo_id", activeReq.AlgoID, "algo_cl_ord_id", activeReq.AlgoClOrdID, "error", err)
				}
				continue
			}
			if closed {
				return
			}
			nextKey := pendingOrderChaseKey(nextReq)
			pendingOrderChaseJobs.move(activeKey, nextKey)
			activeKey = nextKey
			activeReq = nextReq
		case <-timeoutTimer.C:
			return
		}
	}
}

func chaseOKXPendingAlgoOrderOnce(ctx context.Context, cfg config.Config, client okx.Client, req pendingOrderChaseRequest) (pendingOrderChaseResponse, bool, error) {
	order, found, err := currentOKXPendingAlgoOrder(ctx, client, req)
	if err != nil {
		return pendingOrderChaseResponse{}, false, err
	}
	resp := pendingAlgoOrderResponse(req, req.APIID)
	if !found {
		resp.OK = false
		resp.Status = "finished"
		resp.Message = "pending algo order is no longer open"
		return resp, true, nil
	}
	resp.AlgoID = strings.TrimSpace(order.AlgoID)
	resp.AlgoClOrdID = strings.TrimSpace(order.AlgoClOrdID)
	reason := okxAlgoOrderChaseUnavailableReason(order)
	if reason != "" {
		return resp, false, errors.New(reason)
	}
	viewOrder := okxAlgoOrderToPendingView(order).PendingOrder
	midPx, chasePx, err := pendingAlgoOrderMidAndChasePrice(ctx, client, viewOrder, nil, nil)
	if err != nil {
		return resp, false, err
	}
	resp.MidPx = midPx
	resp.Px = chasePx
	currentPx := firstNonEmpty(okxAlgoOrderTriggerPx(order), order.ActivePx)
	if strings.TrimSpace(currentPx) == chasePx {
		resp.Status = "unchanged"
		resp.Message = "pending algo order trigger price is already at chase price"
		return resp, false, nil
	}
	switch strings.ToLower(strings.TrimSpace(order.OrdType)) {
	case "conditional", "trigger":
		amendReq := okxPendingAlgoAmendRequest(order, chasePx)
		if _, _, err := client.AmendAlgoOrder(ctx, amendReq); err != nil {
			return resp, false, err
		}
	case "move_order_stop":
		if err := recreateOKXMoveOrderStopAlgo(ctx, cfg, client, order, chasePx); err != nil {
			return resp, false, err
		}
	default:
		return resp, false, fmt.Errorf("unsupported OKX algo order type %q", order.OrdType)
	}
	resp.Status = "amended"
	resp.Message = "pending algo order trigger price amended"
	return resp, false, nil
}

func chaseBinancePendingAlgoOrderOnce(ctx context.Context, client binance.Client, req pendingOrderChaseRequest) (pendingOrderChaseResponse, bool, pendingOrderChaseRequest, error) {
	order, found, err := currentBinancePendingAlgoOrder(ctx, client, req)
	if err != nil {
		return pendingOrderChaseResponse{}, false, req, err
	}
	resp := pendingAlgoOrderResponse(req, req.APIID)
	if !found {
		resp.OK = false
		resp.Status = "finished"
		resp.Message = "pending algo order is no longer open"
		return resp, true, req, nil
	}
	resp.AlgoID = strconv.FormatInt(order.AlgoID, 10)
	resp.AlgoClOrdID = strings.TrimSpace(order.ClientAlgoID)
	reason := binanceAlgoOrderChaseUnavailableReason(order)
	if reason != "" {
		return resp, false, req, errors.New(reason)
	}
	viewOrder := binanceAlgoOrderToPendingView(order).PendingOrder
	midPx, chasePx, err := binancePendingAlgoOrderMidAndChasePrice(ctx, client, viewOrder, nil, nil)
	if err != nil {
		return resp, false, req, err
	}
	resp.MidPx = midPx
	resp.Px = chasePx
	currentPx := firstNonEmpty(order.TriggerPrice, order.ActivatePrice)
	if strings.TrimSpace(currentPx) == chasePx {
		resp.Status = "unchanged"
		resp.Message = "pending algo order trigger price is already at chase price"
		return resp, false, req, nil
	}
	if _, err := client.CancelAlgoOrder(ctx, order.AlgoID, strings.TrimSpace(order.ClientAlgoID)); err != nil {
		return resp, false, req, err
	}
	newReq := binancePendingAlgoRecreateRequest(order, chasePx)
	ack, err := client.NewAlgoOrder(ctx, newReq)
	if err != nil {
		return resp, false, req, err
	}
	nextReq := req
	if ack.AlgoID != 0 {
		nextReq.AlgoID = strconv.FormatInt(ack.AlgoID, 10)
		resp.AlgoID = nextReq.AlgoID
	}
	if strings.TrimSpace(ack.ClientAlgoID) != "" {
		nextReq.AlgoClOrdID = strings.TrimSpace(ack.ClientAlgoID)
		resp.AlgoClOrdID = nextReq.AlgoClOrdID
	}
	resp.Status = "amended"
	resp.Message = "pending algo order recreated at chase trigger price"
	return resp, false, nextReq, nil
}

func pendingAlgoOrderResponse(req pendingOrderChaseRequest, apiID string) pendingOrderChaseResponse {
	return pendingOrderChaseResponse{
		OK:          true,
		APIID:       apiID,
		OrderGroup:  "algo",
		InstID:      req.InstID,
		AlgoID:      req.AlgoID,
		AlgoClOrdID: req.AlgoClOrdID,
	}
}

func pendingOrderStopResponse(req pendingOrderChaseRequest, apiID string, stopped bool) pendingOrderChaseResponse {
	status := "not_running"
	message := "pending order chase was not running"
	if stopped {
		status = "stopped"
		message = "pending order chase stopped"
	}
	return pendingOrderChaseResponse{
		OK:          true,
		Status:      status,
		APIID:       apiID,
		OrderGroup:  req.OrderGroup,
		InstID:      req.InstID,
		OrdID:       req.OrdID,
		ClOrdID:     req.ClOrdID,
		AlgoID:      req.AlgoID,
		AlgoClOrdID: req.AlgoClOrdID,
		Message:     message,
	}
}

func okxPendingAlgoAmendRequest(order okx.AlgoOrder, chasePx string) okx.AmendAlgoOrderRequest {
	req := okx.AmendAlgoOrderRequest{
		InstID:      strings.ToUpper(strings.TrimSpace(order.InstID)),
		AlgoID:      strings.TrimSpace(order.AlgoID),
		AlgoClOrdID: strings.TrimSpace(order.AlgoClOrdID),
		CxlOnFail:   false,
	}
	if strings.TrimSpace(order.TPTriggerPx) != "" {
		req.NewTPTriggerPx = chasePx
		if strings.TrimSpace(order.TPOrdPx) != "" {
			req.NewTPOrdPx = strings.TrimSpace(order.TPOrdPx)
		}
		if strings.TrimSpace(order.TPTriggerPxType) != "" {
			req.NewTPTriggerPxType = strings.TrimSpace(order.TPTriggerPxType)
		}
	}
	if strings.TrimSpace(order.SLTriggerPx) != "" {
		req.NewSLTriggerPx = chasePx
		if strings.TrimSpace(order.SLOrdPx) != "" {
			req.NewSLOrdPx = strings.TrimSpace(order.SLOrdPx)
		}
		if strings.TrimSpace(order.SLTriggerPxType) != "" {
			req.NewSLTriggerPxType = strings.TrimSpace(order.SLTriggerPxType)
		}
	}
	if strings.TrimSpace(order.TriggerPx) != "" || (req.NewTPTriggerPx == "" && req.NewSLTriggerPx == "") {
		req.NewTriggerPx = chasePx
		if strings.TrimSpace(order.OrderPx) != "" {
			req.NewOrderPx = strings.TrimSpace(order.OrderPx)
		}
		if strings.TrimSpace(order.TriggerPxType) != "" {
			req.NewTriggerPxType = strings.TrimSpace(order.TriggerPxType)
		}
	}
	return req
}

func recreateOKXMoveOrderStopAlgo(ctx context.Context, cfg config.Config, client okx.Client, order okx.AlgoOrder, chasePx string) error {
	_, _, err := client.CancelAlgoOrders(ctx, []okx.CancelAlgoOrderRequest{{
		InstID:      strings.ToUpper(strings.TrimSpace(order.InstID)),
		AlgoID:      strings.TrimSpace(order.AlgoID),
		AlgoClOrdID: strings.TrimSpace(order.AlgoClOrdID),
	}})
	if err != nil {
		return err
	}
	tdMode := strings.ToLower(strings.TrimSpace(order.TDMode))
	if tdMode == "" {
		tdMode = cfg.MarginMode()
	}
	_, _, err = client.PlaceAlgoOrder(ctx, okx.PlaceAlgoOrderRequest{
		InstID:         strings.ToUpper(strings.TrimSpace(order.InstID)),
		TDMode:         tdMode,
		AlgoClOrdID:    nextPendingOrderAlgoClOrdID(),
		Side:           strings.ToLower(strings.TrimSpace(order.Side)),
		PosSide:        normalizePosSide(order.PosSide),
		OrdType:        "move_order_stop",
		Sz:             strings.TrimSpace(order.Sz),
		ReduceOnly:     okxRawBool(order.ReduceOnly),
		CallbackRatio:  strings.TrimSpace(order.CallbackRatio),
		CallbackSpread: strings.TrimSpace(order.CallbackSpread),
		ActivePx:       chasePx,
	})
	return err
}

func binancePendingAlgoRecreateRequest(order binance.AlgoOpenOrder, chasePx string) binance.AlgoOrderRequest {
	req := binance.AlgoOrderRequest{
		Symbol:           strings.ToUpper(strings.TrimSpace(order.Symbol)),
		Side:             strings.ToUpper(strings.TrimSpace(order.Side)),
		PositionSide:     strings.ToUpper(strings.TrimSpace(order.PositionSide)),
		Type:             strings.ToUpper(strings.TrimSpace(order.OrderType)),
		Quantity:         strings.TrimSpace(order.Quantity),
		WorkingType:      strings.ToUpper(strings.TrimSpace(order.WorkingType)),
		NewClientOrderID: nextPendingOrderAlgoClOrdID(),
		ReduceOnly:       order.ReduceOnly,
		ClosePosition:    order.ClosePosition,
	}
	if strings.EqualFold(req.Type, "TRAILING_STOP_MARKET") {
		req.ActivationPrice = chasePx
		req.CallbackRate = firstNonEmpty(order.CallbackRate, order.PriceRate)
	} else {
		req.TriggerPrice = chasePx
	}
	return req
}

func currentPendingOrder(ctx context.Context, client okx.Client, req pendingOrderChaseRequest) (okx.PendingOrder, bool, error) {
	orders, _, err := client.PendingOrders(ctx, "SWAP")
	if err != nil {
		return okx.PendingOrder{}, false, err
	}
	for _, order := range orders {
		if !strings.EqualFold(order.InstID, req.InstID) {
			continue
		}
		if req.OrdID != "" && order.OrdID == req.OrdID {
			return order, true, nil
		}
		if req.OrdID == "" && req.ClOrdID != "" && order.ClOrdID == req.ClOrdID {
			return order, true, nil
		}
	}
	return okx.PendingOrder{}, false, nil
}

func currentBinancePendingOrder(ctx context.Context, client binance.Client, req pendingOrderChaseRequest) (okx.PendingOrder, bool, error) {
	orders, err := client.OpenOrders(ctx, req.InstID)
	if err != nil {
		return okx.PendingOrder{}, false, err
	}
	for _, rawOrder := range orders {
		order := binanceOpenOrderToOKX(rawOrder)
		if !strings.EqualFold(order.InstID, req.InstID) {
			continue
		}
		if req.OrdID != "" && order.OrdID == req.OrdID {
			return order, true, nil
		}
		if req.OrdID == "" && req.ClOrdID != "" && order.ClOrdID == req.ClOrdID {
			return order, true, nil
		}
	}
	return okx.PendingOrder{}, false, nil
}

func currentOKXPendingAlgoOrder(ctx context.Context, client okx.Client, req pendingOrderChaseRequest) (okx.AlgoOrder, bool, error) {
	orders, _, err := client.PendingAlgoOrders(ctx, "SWAP", req.InstID)
	if err != nil {
		return okx.AlgoOrder{}, false, err
	}
	for _, order := range orders {
		if !strings.EqualFold(strings.TrimSpace(order.InstID), strings.TrimSpace(req.InstID)) {
			continue
		}
		if strings.TrimSpace(req.AlgoID) != "" && strings.TrimSpace(order.AlgoID) == strings.TrimSpace(req.AlgoID) {
			return order, true, nil
		}
		if strings.TrimSpace(req.AlgoID) == "" && strings.TrimSpace(req.AlgoClOrdID) != "" && strings.TrimSpace(order.AlgoClOrdID) == strings.TrimSpace(req.AlgoClOrdID) {
			return order, true, nil
		}
	}
	return okx.AlgoOrder{}, false, nil
}

func currentBinancePendingAlgoOrder(ctx context.Context, client binance.Client, req pendingOrderChaseRequest) (binance.AlgoOpenOrder, bool, error) {
	orders, err := client.OpenAlgoOrders(ctx, req.InstID)
	if err != nil {
		return binance.AlgoOpenOrder{}, false, err
	}
	for _, order := range orders {
		if !strings.EqualFold(strings.TrimSpace(order.Symbol), strings.TrimSpace(req.InstID)) {
			continue
		}
		if strings.TrimSpace(req.AlgoID) != "" {
			id, err := strconv.ParseInt(strings.TrimSpace(req.AlgoID), 10, 64)
			if err == nil && order.AlgoID == id {
				return order, true, nil
			}
		}
		if strings.TrimSpace(req.AlgoID) == "" && strings.TrimSpace(req.AlgoClOrdID) != "" && strings.TrimSpace(order.ClientAlgoID) == strings.TrimSpace(req.AlgoClOrdID) {
			return order, true, nil
		}
	}
	return binance.AlgoOpenOrder{}, false, nil
}

func currentOKXPositionCloseOrder(ctx context.Context, client okx.Client, active positionCloseOrder) (okx.PendingOrder, bool, error) {
	return currentPendingOrder(ctx, client, pendingOrderChaseRequest{
		InstID:  active.Position.InstID,
		OrdID:   strings.TrimSpace(active.Ack.OrdID),
		ClOrdID: strings.TrimSpace(active.Ack.ClOrdID),
	})
}

func currentBinancePositionCloseOrder(ctx context.Context, client binance.Client, active positionCloseOrder) (okx.PendingOrder, bool, error) {
	orders, found, err := currentBinancePositionCloseOrders(ctx, client, active)
	if !found || err != nil {
		return okx.PendingOrder{}, found, err
	}
	return orders[0], true, nil
}

func currentBinancePositionCloseOrders(ctx context.Context, client binance.Client, active positionCloseOrder) ([]okx.PendingOrder, bool, error) {
	acks := positionCloseOrderAcks(active)
	if len(acks) == 0 {
		return nil, false, nil
	}
	orders, err := client.OpenOrders(ctx, active.Position.InstID)
	if err != nil {
		return nil, false, err
	}
	ordIDs := map[string]bool{}
	clOrdIDs := map[string]bool{}
	for _, ack := range acks {
		if id := strings.TrimSpace(ack.OrdID); id != "" {
			ordIDs[id] = true
		}
		if id := strings.TrimSpace(ack.ClOrdID); id != "" {
			clOrdIDs[id] = true
		}
	}
	out := make([]okx.PendingOrder, 0, len(acks))
	for _, rawOrder := range orders {
		order := binanceOpenOrderToOKX(rawOrder)
		if !strings.EqualFold(order.InstID, active.Position.InstID) {
			continue
		}
		if ordIDs[strings.TrimSpace(order.OrdID)] || clOrdIDs[strings.TrimSpace(order.ClOrdID)] {
			out = append(out, order)
		}
	}
	return out, len(out) > 0, nil
}

func positionCloseOrderAcks(active positionCloseOrder) []okx.OrderAck {
	if len(active.Acks) > 0 {
		return active.Acks
	}
	if strings.TrimSpace(active.Ack.OrdID) == "" && strings.TrimSpace(active.Ack.ClOrdID) == "" {
		return nil
	}
	return []okx.OrderAck{active.Ack}
}

func cancelPendingOrder(ctx context.Context, client okx.Client, order okx.PendingOrder) error {
	if strings.TrimSpace(order.OrdID) == "" && strings.TrimSpace(order.ClOrdID) == "" {
		return errors.New("pending order has no ord_id or cl_ord_id")
	}
	req := okx.CancelOrderRequest{InstID: strings.ToUpper(strings.TrimSpace(order.InstID))}
	if strings.TrimSpace(order.OrdID) != "" {
		req.OrdID = strings.TrimSpace(order.OrdID)
	} else {
		req.ClOrdID = strings.TrimSpace(order.ClOrdID)
	}
	_, _, err := client.CancelOrder(ctx, req)
	return err
}

func cancelBinancePendingOrder(ctx context.Context, client binance.Client, order okx.PendingOrder) error {
	if strings.TrimSpace(order.OrdID) == "" && strings.TrimSpace(order.ClOrdID) == "" {
		return errors.New("pending order has no ord_id or cl_ord_id")
	}
	_, err := client.CancelOrder(ctx, binance.CancelOrderRequest{
		Symbol:            order.InstID,
		OrderID:           strings.TrimSpace(order.OrdID),
		OrigClientOrderID: strings.TrimSpace(order.ClOrdID),
	})
	return err
}

func binancePendingOrderNoLongerOpen(err error) bool {
	return binance.IsAPIErrorCode(err, -2011, -2013)
}

func binancePendingPositionSide(posSide string) string {
	switch strings.ToLower(strings.TrimSpace(posSide)) {
	case "long":
		return "LONG"
	case "short":
		return "SHORT"
	case "both":
		return "BOTH"
	default:
		return ""
	}
}

func pendingOrderLimitRequest(cfg config.Config, order okx.PendingOrder, remaining, px string) (okx.PlaceOrderRequest, error) {
	req, err := pendingOrderOrderRequest(cfg, order, remaining, "limit", px, nextPendingOrderLimitClOrdID())
	if err != nil {
		return okx.PlaceOrderRequest{}, err
	}
	req.AttachAlgoOrds = pendingOrderAttachAlgoOrders(cfg, order, req.ClOrdID)
	return req, nil
}

func pendingOrderMarketRequest(cfg config.Config, order okx.PendingOrder, remaining string) (okx.PlaceOrderRequest, error) {
	req, err := pendingOrderOrderRequest(cfg, order, remaining, "market", "", nextPendingOrderMarketClOrdID())
	if err != nil {
		return okx.PlaceOrderRequest{}, err
	}
	req.AttachAlgoOrds = pendingOrderAttachAlgoOrders(cfg, order, req.ClOrdID)
	return req, nil
}

func pendingOrderOrderRequest(cfg config.Config, order okx.PendingOrder, remaining, ordType, px, clOrdID string) (okx.PlaceOrderRequest, error) {
	instID := strings.ToUpper(strings.TrimSpace(order.InstID))
	if instID == "" {
		return okx.PlaceOrderRequest{}, errors.New("inst_id is required")
	}
	side := strings.ToLower(strings.TrimSpace(order.Side))
	if side != "buy" && side != "sell" {
		return okx.PlaceOrderRequest{}, fmt.Errorf("unsupported order side %q", order.Side)
	}
	tdMode := strings.ToLower(strings.TrimSpace(order.TDMode))
	if tdMode == "" {
		tdMode = cfg.MarginMode()
	}
	req := okx.PlaceOrderRequest{
		InstID:  instID,
		TDMode:  tdMode,
		ClOrdID: clOrdID,
		Side:    side,
		OrdType: ordType,
		Px:      px,
		Sz:      remaining,
	}
	posSide := normalizePosSide(order.PosSide)
	if posSide != "" && posSide != "net" {
		req.PosSide = posSide
	}
	if okxRawBool(order.ReduceOnly) {
		req.ReduceOnly = true
	}
	return req, nil
}

func shouldRebuildPendingOrderWithProtection(cfg config.Config, order okx.PendingOrder) bool {
	attachAlgoOrds := pendingOrderAttachAlgoOrders(cfg, order, "PENDINGRISK")
	if len(attachAlgoOrds) == 0 {
		return false
	}
	if len(order.AttachAlgoOrds) == 0 {
		return true
	}
	risk := cfg.OrderSettings().Risk
	risk.Normalize()
	switch risk.Type {
	case trading.RiskTrailing:
		return true
	case trading.RiskTPSL:
		return pendingOrderTPSLRiskStale(attachAlgoOrds, order.AttachAlgoOrds)
	default:
		return false
	}
}

func pendingOrderTPSLRiskStale(expected, existing []map[string]string) bool {
	expectedTP, expectedSL := pendingOrderTPSLRatios(expected)
	if expectedTP == "" || expectedSL == "" {
		return false
	}
	for _, attach := range existing {
		tp := normalizePendingOrderRiskRatio(attach["tpTriggerRatio"])
		sl := normalizePendingOrderRiskRatio(attach["slTriggerRatio"])
		if tp == expectedTP && sl == expectedSL {
			return false
		}
	}
	return true
}

func pendingOrderTPSLRatios(attachAlgoOrds []map[string]string) (string, string) {
	for _, attach := range attachAlgoOrds {
		tp := normalizePendingOrderRiskRatio(attach["tpTriggerRatio"])
		sl := normalizePendingOrderRiskRatio(attach["slTriggerRatio"])
		if tp != "" || sl != "" {
			return tp, sl
		}
	}
	return "", ""
}

func normalizePendingOrderRiskRatio(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return raw
	}
	return trading.NormalizeFloat(value)
}

func pendingOrderAttachAlgoOrders(cfg config.Config, order okx.PendingOrder, clOrdID string) []map[string]string {
	if okxRawBool(order.ReduceOnly) {
		return nil
	}
	settings := cfg.OrderSettings()
	risk := settings.Risk
	risk.Normalize()
	if risk.Type == trading.RiskNone {
		return nil
	}
	action, ok := pendingOrderTradingAction(order.Side)
	if !ok {
		return nil
	}
	return okx.AttachAlgoOrders(action, risk, clOrdID)
}

func pendingOrderTradingAction(side string) (trading.Side, bool) {
	switch strings.ToLower(strings.TrimSpace(side)) {
	case "buy":
		return trading.ActionLong, true
	case "sell":
		return trading.ActionShort, true
	default:
		return "", false
	}
}

func pendingOrderRemainingSize(order okx.PendingOrder) (string, error) {
	totalRaw := strings.TrimSpace(order.Sz)
	total, ok := new(big.Rat).SetString(totalRaw)
	if !ok || total.Sign() <= 0 {
		return "", fmt.Errorf("invalid pending order size %q", order.Sz)
	}
	filledRaw := strings.TrimSpace(order.AccFillSz)
	if filledRaw == "" {
		filledRaw = "0"
	}
	filled, ok := new(big.Rat).SetString(filledRaw)
	if !ok || filled.Sign() < 0 {
		return "", fmt.Errorf("invalid pending order filled size %q", order.AccFillSz)
	}
	remaining := new(big.Rat).Sub(total, filled)
	if remaining.Sign() <= 0 {
		return "", errPendingOrderNoRemaining
	}
	decimals := decimalsFromDecimalString(totalRaw)
	if filledDecimals := decimalsFromDecimalString(filledRaw); filledDecimals > decimals {
		decimals = filledDecimals
	}
	out := trimDecimalZeros(remaining.FloatString(decimals))
	if out == "" || out == "0" {
		return "", errPendingOrderNoRemaining
	}
	return out, nil
}

func pendingOrdersRemainingSize(orders []okx.PendingOrder) (string, error) {
	sum := new(big.Rat)
	decimals := 0
	for _, order := range orders {
		remainingRaw, err := pendingOrderRemainingSize(order)
		if err != nil {
			if errors.Is(err, errPendingOrderNoRemaining) {
				continue
			}
			return "", err
		}
		remaining, ok := new(big.Rat).SetString(remainingRaw)
		if !ok || remaining.Sign() <= 0 {
			continue
		}
		sum.Add(sum, remaining)
		if d := decimalPlacesForDisplay(remainingRaw); d > decimals {
			decimals = d
		}
	}
	if sum.Sign() <= 0 {
		return "", errPendingOrderNoRemaining
	}
	out := trimDecimalZeros(sum.FloatString(decimals))
	if out == "" || out == "0" {
		return "", errPendingOrderNoRemaining
	}
	return out, nil
}

func pendingOrderMargin(order okx.PendingOrder, fallbackPx, ctValRaw string, fallbackLeverage int) string {
	remainingRaw, err := pendingOrderRemainingSize(order)
	if err != nil {
		return ""
	}
	priceRaw := strings.TrimSpace(order.Px)
	if priceRaw == "" || priceRaw == "0" {
		priceRaw = strings.TrimSpace(fallbackPx)
	}
	price, priceErr := strconv.ParseFloat(priceRaw, 64)
	remaining, remainingErr := strconv.ParseFloat(remainingRaw, 64)
	ctVal, ctValErr := strconv.ParseFloat(strings.TrimSpace(ctValRaw), 64)
	leverage := pendingOrderLeverage(order, fallbackLeverage)
	if priceErr != nil || remainingErr != nil || ctValErr != nil || price <= 0 || remaining <= 0 || ctVal <= 0 || leverage <= 0 {
		return ""
	}
	margin := price * remaining * ctVal / leverage
	if math.IsNaN(margin) || math.IsInf(margin, 0) || margin <= 0 {
		return ""
	}
	return trimDecimalZeros(strconv.FormatFloat(margin, 'f', 8, 64))
}

func pendingOrderLeverage(order okx.PendingOrder, fallback int) float64 {
	if leverage, err := strconv.ParseFloat(strings.TrimSpace(order.Lever), 64); err == nil && leverage > 0 {
		return leverage
	}
	if fallback > 0 {
		return float64(fallback)
	}
	return 0
}

func okxRawBool(raw []byte) bool {
	value := strings.TrimSpace(string(raw))
	if value == "" || value == "null" {
		return false
	}
	if parsed, err := strconv.ParseBool(value); err == nil {
		return parsed
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		parsed, _ := strconv.ParseBool(strings.TrimSpace(text))
		return parsed
	}
	return false
}

func pendingOrderMidAndChasePrice(ctx context.Context, client okx.Client, order okx.PendingOrder, tickers map[string]okx.Ticker, instruments map[string]okx.Instrument) (string, string, error) {
	instID := strings.ToUpper(strings.TrimSpace(order.InstID))
	if instID == "" {
		return "", "", errors.New("inst_id is required")
	}
	if reason := pendingOrderChaseUnavailableReason(order); reason != "" {
		return "", "", errors.New(reason)
	}
	var inst okx.Instrument
	var ok bool
	if instruments != nil {
		inst, ok = instruments[instID]
	}
	if !ok {
		var err error
		inst, err = client.SwapInstrument(ctx, instID)
		if err != nil {
			return "", "", err
		}
		if instruments != nil {
			instruments[instID] = inst
		}
	}
	var ticker okx.Ticker
	if tickers != nil {
		ticker, ok = tickers[instID]
	} else {
		ok = false
	}
	if !ok {
		var err error
		ticker, _, err = client.MarketTicker(ctx, instID)
		if err != nil {
			return "", "", err
		}
		if tickers != nil {
			tickers[instID] = ticker
		}
	}
	mid, err := tickerMidPrice(ticker)
	if err != nil {
		return "", "", err
	}
	chasePx, err := passivePendingOrderPrice(mid, inst.TickSz, order.Side)
	if err != nil {
		return "", "", err
	}
	return formatMidPrice(mid, inst.TickSz), chasePx, nil
}

func binancePendingOrderMidAndChasePrice(ctx context.Context, client binance.Client, order okx.PendingOrder, tickers map[string]binance.BookTicker, instruments map[string]binance.SymbolInfo) (string, string, error) {
	symbol := strings.ToUpper(strings.TrimSpace(order.InstID))
	if symbol == "" {
		return "", "", errors.New("inst_id is required")
	}
	if strings.ToLower(strings.TrimSpace(order.OrdType)) != "limit" {
		return "", "", fmt.Errorf("Binance only supports chasing limit orders")
	}
	var inst binance.SymbolInfo
	var ok bool
	if instruments != nil {
		inst, ok = instruments[symbol]
	}
	if !ok {
		var err error
		inst, err = client.SymbolInfo(ctx, symbol)
		if err != nil {
			return "", "", err
		}
		if instruments != nil {
			instruments[symbol] = inst
		}
	}
	tickRaw, err := binanceTickSizeRaw(inst)
	if err != nil {
		return "", "", err
	}
	var ticker binance.BookTicker
	if tickers != nil {
		ticker, ok = tickers[symbol]
	} else {
		ok = false
	}
	if !ok {
		ticker, err = client.BookTicker(ctx, symbol)
		if err != nil {
			return "", "", err
		}
		if tickers != nil {
			tickers[symbol] = ticker
		}
	}
	mid, err := binanceBookMidPrice(ticker)
	if err != nil {
		return "", "", err
	}
	chasePx, err := passivePendingOrderPrice(mid, tickRaw, order.Side)
	if err != nil {
		return "", "", err
	}
	return formatMidPrice(mid, tickRaw), chasePx, nil
}

func pendingAlgoOrderMidAndChasePrice(ctx context.Context, client okx.Client, order okx.PendingOrder, tickers map[string]okx.Ticker, instruments map[string]okx.Instrument) (string, string, error) {
	instID := strings.ToUpper(strings.TrimSpace(order.InstID))
	if instID == "" {
		return "", "", errors.New("inst_id is required")
	}
	var inst okx.Instrument
	var ok bool
	if instruments != nil {
		inst, ok = instruments[instID]
	}
	if !ok {
		var err error
		inst, err = client.SwapInstrument(ctx, instID)
		if err != nil {
			return "", "", err
		}
		if instruments != nil {
			instruments[instID] = inst
		}
	}
	var ticker okx.Ticker
	if tickers != nil {
		ticker, ok = tickers[instID]
	} else {
		ok = false
	}
	if !ok {
		var err error
		ticker, _, err = client.MarketTicker(ctx, instID)
		if err != nil {
			return "", "", err
		}
		if tickers != nil {
			tickers[instID] = ticker
		}
	}
	mid, err := tickerMidPrice(ticker)
	if err != nil {
		return "", "", err
	}
	chasePx, err := activePendingAlgoOrderPrice(ticker.BidPx, ticker.AskPx, ticker.Last, inst.TickSz, order.Side)
	if err != nil {
		return "", "", err
	}
	return formatMidPrice(mid, inst.TickSz), chasePx, nil
}

func binancePendingAlgoOrderMidAndChasePrice(ctx context.Context, client binance.Client, order okx.PendingOrder, tickers map[string]binance.BookTicker, instruments map[string]binance.SymbolInfo) (string, string, error) {
	symbol := strings.ToUpper(strings.TrimSpace(order.InstID))
	if symbol == "" {
		return "", "", errors.New("inst_id is required")
	}
	var inst binance.SymbolInfo
	var ok bool
	if instruments != nil {
		inst, ok = instruments[symbol]
	}
	if !ok {
		var err error
		inst, err = client.SymbolInfo(ctx, symbol)
		if err != nil {
			return "", "", err
		}
		if instruments != nil {
			instruments[symbol] = inst
		}
	}
	tickRaw, err := binanceTickSizeRaw(inst)
	if err != nil {
		return "", "", err
	}
	var ticker binance.BookTicker
	if tickers != nil {
		ticker, ok = tickers[symbol]
	} else {
		ok = false
	}
	if !ok {
		ticker, err = client.BookTicker(ctx, symbol)
		if err != nil {
			return "", "", err
		}
		if tickers != nil {
			tickers[symbol] = ticker
		}
	}
	mid, err := binanceBookMidPrice(ticker)
	if err != nil {
		return "", "", err
	}
	chasePx, err := activePendingAlgoOrderPrice(ticker.BidPrice, ticker.AskPrice, "", tickRaw, order.Side)
	if err != nil {
		return "", "", err
	}
	return formatMidPrice(mid, tickRaw), chasePx, nil
}

func tickerMidPrice(ticker okx.Ticker) (float64, error) {
	bid, bidErr := strconv.ParseFloat(strings.TrimSpace(ticker.BidPx), 64)
	ask, askErr := strconv.ParseFloat(strings.TrimSpace(ticker.AskPx), 64)
	if bidErr == nil && askErr == nil && bid > 0 && ask > 0 {
		return (bid + ask) / 2, nil
	}
	last, lastErr := strconv.ParseFloat(strings.TrimSpace(ticker.Last), 64)
	if lastErr != nil || last <= 0 {
		return 0, fmt.Errorf("invalid ticker bid/ask for %s", ticker.InstID)
	}
	return last, nil
}

func binanceBookMidPrice(ticker binance.BookTicker) (float64, error) {
	bid, bidErr := strconv.ParseFloat(strings.TrimSpace(ticker.BidPrice), 64)
	ask, askErr := strconv.ParseFloat(strings.TrimSpace(ticker.AskPrice), 64)
	if bidErr != nil || askErr != nil || bid <= 0 || ask <= 0 {
		return 0, fmt.Errorf("invalid Binance book ticker bid/ask for %s", ticker.Symbol)
	}
	return (bid + ask) / 2, nil
}

func binanceTickSizeRaw(info binance.SymbolInfo) (string, error) {
	for _, filter := range info.Filters {
		if filter.FilterType == "PRICE_FILTER" && strings.TrimSpace(filter.TickSize) != "" {
			return strings.TrimSpace(filter.TickSize), nil
		}
	}
	filters, err := info.TradingFilters()
	if err != nil {
		return "", err
	}
	return trading.NormalizeFloat(filters.TickSize), nil
}

func passivePendingOrderPrice(mid float64, tickRaw, side string) (string, error) {
	tick, err := strconv.ParseFloat(strings.TrimSpace(tickRaw), 64)
	if err != nil || tick <= 0 {
		return "", fmt.Errorf("invalid tick size %q", tickRaw)
	}
	switch strings.ToLower(strings.TrimSpace(side)) {
	case "buy":
		return formatPriceToTick(mid-tick, tick, tickRaw, false)
	case "sell":
		return formatPriceToTick(mid+tick, tick, tickRaw, true)
	default:
		return "", fmt.Errorf("unsupported order side %q", side)
	}
}

func pendingOrderChaseUnavailableReason(order okx.PendingOrder) string {
	if strings.TrimSpace(order.InstID) == "" {
		return "缺少币对，刷新挂单后重试"
	}
	if strings.TrimSpace(order.OrdID) == "" && strings.TrimSpace(order.ClOrdID) == "" {
		return "缺少订单ID，刷新挂单后重试"
	}
	if !strings.EqualFold(strings.TrimSpace(order.OrdType), "limit") {
		return "普通追单只支持限价单"
	}
	switch strings.ToLower(strings.TrimSpace(order.Side)) {
	case "buy", "sell":
		return ""
	default:
		return fmt.Sprintf("不支持的订单方向 %q", order.Side)
	}
}

func validatePendingOrderChasePx(raw string) error {
	px, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || px <= 0 || math.IsNaN(px) || math.IsInf(px, 0) {
		return fmt.Errorf("invalid chase price %q", raw)
	}
	return nil
}

func pendingOrderPriceEquivalent(a, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == "" || b == "" {
		return false
	}
	if trimDecimalZeros(a) == trimDecimalZeros(b) {
		return true
	}
	ar, aOK := decimalRat(a)
	br, bOK := decimalRat(b)
	if aOK && bOK {
		return ar.Cmp(br) == 0
	}
	af, aErr := strconv.ParseFloat(a, 64)
	bf, bErr := strconv.ParseFloat(b, 64)
	if aErr != nil || bErr != nil {
		return false
	}
	tolerance := math.Max(math.Abs(af), math.Abs(bf)) * 1e-12
	if tolerance < 1e-12 {
		tolerance = 1e-12
	}
	return math.Abs(af-bf) <= tolerance
}

func decimalRat(raw string) (*big.Rat, bool) {
	rat := new(big.Rat)
	if _, ok := rat.SetString(strings.TrimSpace(raw)); ok {
		return rat, true
	}
	return nil, false
}

func activePendingAlgoOrderPrice(bidRaw, askRaw, fallbackRaw, tickRaw, side string) (string, error) {
	tick, err := strconv.ParseFloat(strings.TrimSpace(tickRaw), 64)
	if err != nil || tick <= 0 {
		return "", fmt.Errorf("invalid tick size %q", tickRaw)
	}
	bid, bidErr := strconv.ParseFloat(strings.TrimSpace(bidRaw), 64)
	ask, askErr := strconv.ParseFloat(strings.TrimSpace(askRaw), 64)
	fallback, fallbackErr := strconv.ParseFloat(strings.TrimSpace(fallbackRaw), 64)
	switch strings.ToLower(strings.TrimSpace(side)) {
	case "buy":
		if askErr != nil || ask <= 0 {
			if fallbackErr != nil || fallback <= 0 {
				return "", fmt.Errorf("invalid ask price %q", askRaw)
			}
			ask = fallback
		}
		return formatPriceToTick(ask, tick, tickRaw, true)
	case "sell":
		if bidErr != nil || bid <= 0 {
			if fallbackErr != nil || fallback <= 0 {
				return "", fmt.Errorf("invalid bid price %q", bidRaw)
			}
			bid = fallback
		}
		return formatPriceToTick(bid, tick, tickRaw, false)
	default:
		return "", fmt.Errorf("unsupported order side %q", side)
	}
}

func formatMidPrice(mid float64, tickRaw string) string {
	decimals := decimalsFromDecimalString(tickRaw)
	return trimDecimalZeros(strconv.FormatFloat(mid, 'f', decimals, 64))
}

func (s *Server) okxClientForCredentials(cfg config.Config, requestedAPIID string) (okx.Client, string, error) {
	store := s.okxCredentialStore(cfg.Trading.Env)
	if store == nil {
		return okx.Client{}, "", errors.New("OKX credential store is not configured")
	}
	creds, apiID, err := store.OKXCredentials(requestedAPIID)
	if err != nil {
		return okx.Client{}, "", err
	}
	return okx.Client{
		BaseURL:     cfg.OKXBaseURL(),
		Credentials: creds,
		Demo:        cfg.DemoTradingHeaderEnabled(),
		HTTPClient:  s.okxHTTPClient(),
	}, apiID, nil
}

func (s *Server) binanceClientForCredentials(cfg config.Config, requestedAPIID string) (binance.Client, string, error) {
	store := s.binanceCredentialStore(cfg.Trading.Env)
	if store == nil {
		return binance.Client{}, "", errors.New("Binance credential store is not configured")
	}
	creds, apiID, err := store.BinanceCredentials(requestedAPIID)
	if err != nil {
		return binance.Client{}, "", err
	}
	return binance.Client{
		BaseURL:     cfg.BinanceBaseURL(),
		Credentials: creds,
		HTTPClient:  s.binanceHTTPClient(),
	}, apiID, nil
}

func (s *Server) gateClientForCredentials(cfg config.Config, requestedAPIID string) (gate.Client, string, error) {
	store := s.gateCredentialStore(cfg.Trading.Env)
	if store == nil {
		return gate.Client{}, "", errors.New("Gate credential store is not configured")
	}
	creds, apiID, err := store.GateCredentials(requestedAPIID)
	if err != nil {
		return gate.Client{}, "", err
	}
	return gate.Client{
		BaseURL:     cfg.GateBaseURL(),
		Credentials: creds,
		HTTPClient:  s.GateHTTPClient,
	}, apiID, nil
}

func openPositions(positions []okx.Position) []okx.Position {
	out := make([]okx.Position, 0, len(positions))
	for _, position := range positions {
		if isOpenPosition(position.Pos) {
			out = append(out, position)
		}
	}
	return out
}

func lowMarginPosition(position okx.Position, threshold float64) (float64, bool) {
	if threshold <= 0 || !isOpenPosition(position.Pos) {
		return 0, false
	}
	margin, err := strconv.ParseFloat(strings.TrimSpace(position.Margin), 64)
	if err != nil || margin < 0 {
		return 0, false
	}
	return margin, margin < threshold
}

func currentOpenPosition(ctx context.Context, client okx.Client, instID, posSide string) (okx.Position, error) {
	instID = strings.ToUpper(strings.TrimSpace(instID))
	posSide = normalizePosSide(posSide)
	positions, _, err := client.Positions(ctx, "SWAP")
	if err != nil {
		return okx.Position{}, err
	}
	for _, position := range positions {
		if !strings.EqualFold(position.InstID, instID) || !isOpenPosition(position.Pos) {
			continue
		}
		if posSide == "" || normalizePosSide(position.PosSide) == posSide {
			return position, nil
		}
	}
	return okx.Position{}, fmt.Errorf("%w: %s %s", errPositionNotOpen, instID, posSide)
}

func placeMarketPositionClose(ctx context.Context, cfg config.Config, client okx.Client, position okx.Position, closeSz string) (positionCloseOrder, error) {
	if strings.TrimSpace(closeSz) == "" {
		return closeEntireOKXPosition(ctx, cfg, client, position)
	}
	req, partial, err := positionCloseOrderRequest(cfg, position, "market", "", closeSz)
	if err != nil {
		return positionCloseOrder{}, err
	}
	ack, _, err := client.PlaceOrder(ctx, req)
	if err == nil {
		return recoverUnfilledMarketPositionClose(ctx, cfg, client, positionCloseOrder{Position: position, Ack: ack, CloseSz: req.Sz, Partial: partial})
	}
	fallbackReq, ok := protectedPositionCloseRetryRequest(ctx, client, position, req, err)
	if !ok {
		return positionCloseOrder{}, err
	}
	fallbackAck, _, fallbackErr := client.PlaceOrder(ctx, fallbackReq)
	if fallbackErr != nil {
		return positionCloseOrder{}, fmt.Errorf("OKX market close rejected by price protection: %v; protected limit retry at %s failed: %w", err, fallbackReq.Px, fallbackErr)
	}
	return positionCloseOrder{Position: position, Ack: fallbackAck, Px: fallbackReq.Px, CloseSz: fallbackReq.Sz, Partial: partial}, nil
}

// closeEntireOKXPosition uses OKX's dedicated whole-position market-close
// endpoint. It avoids representing a manual full close as a normal market
// order, which OKX may price-band and leave pending; autoCxl removes any
// outstanding closing orders for this position as part of the request.
func closeEntireOKXPosition(ctx context.Context, cfg config.Config, client okx.Client, position okx.Position) (positionCloseOrder, error) {
	mgnMode := strings.ToLower(strings.TrimSpace(position.MgnMode))
	if mgnMode == "" {
		mgnMode = cfg.MarginMode()
	}
	req := okx.ClosePositionRequest{
		InstID:  strings.ToUpper(strings.TrimSpace(position.InstID)),
		MgnMode: mgnMode,
		AutoCxl: true,
		ClOrdID: nextPositionCloseClOrdID(),
	}
	if posSide := normalizePosSide(position.PosSide); posSide != "" {
		req.PosSide = posSide
	}
	ack, _, err := client.ClosePosition(ctx, req)
	if err != nil {
		return positionCloseOrder{}, err
	}
	return positionCloseOrder{
		Position: position,
		Ack:      okx.OrderAck{ClOrdID: firstNonEmpty(ack.ClOrdID, req.ClOrdID)},
		CloseSz:  absolutePositionSize(position.Pos),
	}, nil
}

// recoverUnfilledMarketPositionClose prevents an accepted OKX market close
// from remaining live indefinitely under exchange price protection. If it is
// still live after a short grace period, cancel it and submit a reduce-only
// limit close for the remaining size.
func recoverUnfilledMarketPositionClose(ctx context.Context, cfg config.Config, client okx.Client, active positionCloseOrder) (positionCloseOrder, error) {
	pending, live, err := waitForUnfilledMarketPositionClose(ctx, client, active)
	if err != nil || !live {
		return active, nil
	}
	remaining, err := pendingOrderRemainingSize(pending)
	if err != nil {
		return active, nil
	}
	if err := cancelPendingOrder(ctx, client, pending); err != nil {
		if _, stillOpen, checkErr := currentOKXPositionCloseOrder(ctx, client, active); checkErr == nil && !stillOpen {
			return active, nil
		}
		return active, nil
	}
	position, err := currentOpenPosition(ctx, client, active.Position.InstID, active.Position.PosSide)
	if err != nil {
		if errors.Is(err, errPositionNotOpen) {
			return active, nil
		}
		return positionCloseOrder{}, fmt.Errorf("market close stayed live and was canceled; refresh position before retrying: %w", err)
	}
	remaining = capCloseSizeToPosition(position.Pos, remaining)
	if remaining == "" || remaining == "0" {
		return active, nil
	}
	fallback, err := placeLimitPositionClose(ctx, cfg, client, position, remaining)
	if err != nil {
		return positionCloseOrder{}, fmt.Errorf("market close stayed live and was canceled; protected limit retry failed: %w", err)
	}
	return fallback, nil
}

func waitForUnfilledMarketPositionClose(ctx context.Context, client okx.Client, active positionCloseOrder) (okx.PendingOrder, bool, error) {
	if marketClosePendingTimeout <= 0 || strings.TrimSpace(active.Ack.OrdID) == "" && strings.TrimSpace(active.Ack.ClOrdID) == "" {
		return okx.PendingOrder{}, false, nil
	}
	pollInterval := marketClosePendingPollInterval
	if pollInterval <= 0 {
		pollInterval = 250 * time.Millisecond
	}
	poll := time.NewTicker(pollInterval)
	defer poll.Stop()
	timeout := time.NewTimer(marketClosePendingTimeout)
	defer timeout.Stop()
	for {
		select {
		case <-ctx.Done():
			return okx.PendingOrder{}, false, ctx.Err()
		case <-poll.C:
			if _, found, err := currentOKXPositionCloseOrder(ctx, client, active); err != nil {
				return okx.PendingOrder{}, false, err
			} else if !found {
				return okx.PendingOrder{}, false, nil
			}
		case <-timeout.C:
			return currentOKXPositionCloseOrder(ctx, client, active)
		}
	}
}

func placeLimitPositionClose(ctx context.Context, cfg config.Config, client okx.Client, position okx.Position, closeSz string) (positionCloseOrder, error) {
	px, err := limitClosePrice(ctx, client, position)
	if err != nil {
		return positionCloseOrder{}, err
	}
	req, partial, err := positionCloseOrderRequest(cfg, position, "limit", px, closeSz)
	if err != nil {
		return positionCloseOrder{}, err
	}
	ack, _, err := client.PlaceOrder(ctx, req)
	if err == nil {
		return positionCloseOrder{Position: position, Ack: ack, Px: px, CloseSz: req.Sz, Partial: partial}, nil
	}
	fallbackReq, ok := protectedPositionCloseRetryRequest(ctx, client, position, req, err)
	if !ok {
		return positionCloseOrder{}, err
	}
	fallbackAck, _, fallbackErr := client.PlaceOrder(ctx, fallbackReq)
	if fallbackErr != nil {
		return positionCloseOrder{}, fmt.Errorf("OKX limit close rejected by price protection: %v; protected limit retry at %s failed: %w", err, fallbackReq.Px, fallbackErr)
	}
	return positionCloseOrder{Position: position, Ack: fallbackAck, Px: fallbackReq.Px, CloseSz: fallbackReq.Sz, Partial: partial}, nil
}

func protectedPositionCloseRetryRequest(ctx context.Context, client okx.Client, position okx.Position, req okx.PlaceOrderRequest, orderErr error) (okx.PlaceOrderRequest, bool) {
	inst, instErr := client.SwapInstrument(ctx, position.InstID)
	if instErr != nil {
		return okx.PlaceOrderRequest{}, false
	}
	tickSz, tickErr := strconv.ParseFloat(strings.TrimSpace(inst.TickSz), 64)
	if tickErr != nil || tickSz <= 0 {
		return okx.PlaceOrderRequest{}, false
	}
	return okx.ProtectedPriceLimitRetryRequest(ctx, client, req, tickSz, orderErr)
}

func placeOKXPositionProtection(ctx context.Context, cfg config.Config, client okx.Client, apiID string, position okx.Position, kind string) (positionProtectionResponse, error) {
	req, triggerPx, callbackRatio, err := okxPositionProtectionRequest(ctx, cfg, client, position, kind)
	if err != nil {
		return positionProtectionResponse{}, err
	}
	ack, _, err := client.PlaceAlgoOrder(ctx, req)
	if err != nil {
		return positionProtectionResponse{}, err
	}
	return positionProtectionResponse{
		OK:            true,
		Status:        "submitted",
		Exchange:      trading.ExchangeOKX,
		APIID:         apiID,
		InstID:        req.InstID,
		PosSide:       normalizePosSide(position.PosSide),
		Kind:          kind,
		Sz:            req.Sz,
		AlgoID:        ack.AlgoID,
		AlgoClOrdID:   ack.AlgoClOrdID,
		TriggerPx:     triggerPx,
		CallbackRatio: callbackRatio,
		Message:       positionProtectionMessage(kind),
	}, nil
}

func okxPositionProtectionRequest(ctx context.Context, cfg config.Config, client okx.Client, position okx.Position, kind string) (okx.PlaceAlgoOrderRequest, string, string, error) {
	side, err := closeOrderSide(position)
	if err != nil {
		return okx.PlaceAlgoOrderRequest{}, "", "", err
	}
	size := absolutePositionSize(position.Pos)
	if size == "" || size == "0" {
		return okx.PlaceAlgoOrderRequest{}, "", "", errPositionNotOpen
	}
	tdMode := strings.ToLower(strings.TrimSpace(position.MgnMode))
	if tdMode == "" {
		tdMode = cfg.MarginMode()
	}
	req := okx.PlaceAlgoOrderRequest{
		InstID:      strings.ToUpper(strings.TrimSpace(position.InstID)),
		TDMode:      tdMode,
		AlgoClOrdID: nextPositionProtectionClOrdID(kind),
		Side:        side,
		OrdType:     "conditional",
		Sz:          size,
		ReduceOnly:  true,
	}
	if posSide := normalizePosSide(position.PosSide); posSide != "" {
		req.PosSide = posSide
	}
	switch kind {
	case positionProtectionTP, positionProtectionSL:
		inst, err := client.SwapInstrument(ctx, position.InstID)
		if err != nil {
			return okx.PlaceAlgoOrderRequest{}, "", "", err
		}
		triggerPx, err := positionProtectionTriggerPrice(position, kind, inst.TickSz, cfg.Trading.TakeProfitPct, cfg.Trading.StopLossPct)
		if err != nil {
			return okx.PlaceAlgoOrderRequest{}, "", "", err
		}
		if kind == positionProtectionTP {
			req.TPTriggerPx = triggerPx
			req.TPOrdPx = "-1"
			req.TPTriggerPxType = "mark"
		} else {
			req.SLTriggerPx = triggerPx
			req.SLOrdPx = "-1"
			req.SLTriggerPxType = "mark"
		}
		return req, triggerPx, "", nil
	case positionProtectionTrailing:
		callbackRatio, err := okxPositionProtectionCallbackRatio(cfg.Trading.TrailingPct)
		if err != nil {
			return okx.PlaceAlgoOrderRequest{}, "", "", err
		}
		req.OrdType = "move_order_stop"
		req.CallbackRatio = callbackRatio
		return req, "", callbackRatio, nil
	default:
		return okx.PlaceAlgoOrderRequest{}, "", "", fmt.Errorf("unsupported position protection kind %q", kind)
	}
}

func placeBinancePositionProtection(ctx context.Context, cfg config.Config, client binance.Client, apiID string, position okx.Position, kind string) (positionProtectionResponse, error) {
	req, triggerPx, callbackRate, err := binancePositionProtectionRequest(ctx, cfg, client, position, kind)
	if err != nil {
		return positionProtectionResponse{}, err
	}
	ack, err := client.NewAlgoOrder(ctx, req)
	if err != nil {
		return positionProtectionResponse{}, err
	}
	algoID := ""
	if ack.AlgoID != 0 {
		algoID = strconv.FormatInt(ack.AlgoID, 10)
	}
	return positionProtectionResponse{
		OK:            true,
		Status:        "submitted",
		Exchange:      trading.ExchangeBinance,
		APIID:         apiID,
		InstID:        req.Symbol,
		PosSide:       normalizePosSide(position.PosSide),
		Kind:          kind,
		Sz:            req.Quantity,
		AlgoID:        algoID,
		AlgoClOrdID:   ack.ClientAlgoID,
		TriggerPx:     triggerPx,
		CallbackRatio: callbackRate,
		Message:       positionProtectionMessage(kind),
	}, nil
}

func binancePositionProtectionRequest(ctx context.Context, cfg config.Config, client binance.Client, position okx.Position, kind string) (binance.AlgoOrderRequest, string, string, error) {
	side, err := closeOrderSide(position)
	if err != nil {
		return binance.AlgoOrderRequest{}, "", "", err
	}
	size := absolutePositionSize(position.Pos)
	if size == "" || size == "0" {
		return binance.AlgoOrderRequest{}, "", "", errPositionNotOpen
	}
	req := binance.AlgoOrderRequest{
		Symbol:           strings.ToUpper(strings.TrimSpace(position.InstID)),
		Side:             strings.ToUpper(side),
		PositionSide:     binanceClosePositionSide(position.PosSide),
		Quantity:         size,
		WorkingType:      "MARK_PRICE",
		NewClientOrderID: nextPositionProtectionClOrdID(kind),
	}
	if req.PositionSide == "" || req.PositionSide == "BOTH" {
		req.ReduceOnly = true
	}
	switch kind {
	case positionProtectionTP, positionProtectionSL:
		inst, err := client.SymbolInfo(ctx, position.InstID)
		if err != nil {
			return binance.AlgoOrderRequest{}, "", "", err
		}
		tickRaw, err := binanceTickSizeRaw(inst)
		if err != nil {
			return binance.AlgoOrderRequest{}, "", "", err
		}
		triggerPx, err := positionProtectionTriggerPrice(position, kind, tickRaw, cfg.Trading.TakeProfitPct, cfg.Trading.StopLossPct)
		if err != nil {
			return binance.AlgoOrderRequest{}, "", "", err
		}
		if kind == positionProtectionTP {
			req.Type = "TAKE_PROFIT_MARKET"
		} else {
			req.Type = "STOP_MARKET"
		}
		req.TriggerPrice = triggerPx
		return req, triggerPx, "", nil
	case positionProtectionTrailing:
		callbackRate, err := binancePositionProtectionCallbackRate(cfg.Trading.TrailingPct)
		if err != nil {
			return binance.AlgoOrderRequest{}, "", "", err
		}
		req.Type = "TRAILING_STOP_MARKET"
		req.CallbackRate = callbackRate
		return req, "", callbackRate, nil
	default:
		return binance.AlgoOrderRequest{}, "", "", fmt.Errorf("unsupported position protection kind %q", kind)
	}
}

func currentBinanceOpenPosition(ctx context.Context, client binance.Client, instID, posSide string) (okx.Position, error) {
	instID = strings.ToUpper(strings.TrimSpace(instID))
	posSide = normalizePosSide(posSide)
	positions, err := client.Positions(ctx, instID)
	if err != nil {
		return okx.Position{}, err
	}
	for _, raw := range positions {
		position := binancePositionToOKX(raw)
		if !strings.EqualFold(position.InstID, instID) || !isOpenPosition(position.Pos) {
			continue
		}
		if posSide == "" || normalizePosSide(position.PosSide) == posSide {
			return position, nil
		}
	}
	return okx.Position{}, fmt.Errorf("%w: %s %s", errPositionNotOpen, instID, posSide)
}

func okxPositionCloseSize(ctx context.Context, client okx.Client, position okx.Position, ratio float64) (string, error) {
	inst, err := client.SwapInstrument(ctx, position.InstID)
	if err != nil {
		return "", err
	}
	return positionCloseSize(position.Pos, ratio, inst.LotSz, inst.MinSz)
}

func binancePositionCloseSize(ctx context.Context, client binance.Client, position okx.Position, ratio float64) (string, error) {
	inst, err := client.SymbolInfo(ctx, position.InstID)
	if err != nil {
		return "", err
	}
	filters, err := inst.TradingFilters()
	if err != nil {
		return "", err
	}
	stepRaw := trading.NormalizeFloat(filters.StepSize)
	return positionCloseSize(position.Pos, ratio, stepRaw, filters.MinQty)
}

func positionCloseRatio(raw float64) (float64, error) {
	if raw == 0 {
		return 1, nil
	}
	if math.IsNaN(raw) || math.IsInf(raw, 0) || raw <= 0 || raw > 1 {
		return 0, fmt.Errorf("ratio must be greater than 0 and less than or equal to 1")
	}
	return raw, nil
}

func positionCloseSize(posRaw string, ratio float64, stepRaw, minRaw string) (string, error) {
	ratio, err := positionCloseRatio(ratio)
	if err != nil {
		return "", err
	}
	if ratio >= 1 {
		size := absolutePositionSize(posRaw)
		if size == "" || size == "0" {
			return "", errPositionNotOpen
		}
		return size, nil
	}
	position, err := positiveRatFromDecimal(posRaw)
	if err != nil || position.Sign() <= 0 {
		return "", fmt.Errorf("invalid position size %q", posRaw)
	}
	ratioRaw := trading.NormalizeFloat(ratio)
	ratioRat, ok := new(big.Rat).SetString(ratioRaw)
	if !ok || ratioRat.Sign() <= 0 {
		return "", fmt.Errorf("invalid close ratio %q", ratioRaw)
	}
	target := new(big.Rat).Mul(position, ratioRat)
	decimals := decimalPlacesForDisplay(strings.TrimLeft(strings.TrimSpace(posRaw), "-+")) + decimalPlacesForDisplay(ratioRaw)
	if decimals < 0 {
		decimals = 0
	}
	if strings.TrimSpace(stepRaw) != "" {
		step, err := positiveRatFromDecimal(stepRaw)
		if err != nil || step.Sign() <= 0 {
			return "", fmt.Errorf("invalid close size step %q", stepRaw)
		}
		target = floorRatToStep(target, step)
		decimals = decimalPlacesForDisplay(stepRaw)
	}
	if target.Sign() <= 0 {
		return "", fmt.Errorf("close size is below minimum step")
	}
	if strings.TrimSpace(minRaw) != "" {
		minimum, err := positiveRatFromDecimal(minRaw)
		if err != nil || minimum.Sign() <= 0 {
			return "", fmt.Errorf("invalid close minimum size %q", minRaw)
		}
		if target.Cmp(minimum) < 0 {
			return "", fmt.Errorf("close size %s is below minimum size %s", trimDecimalZeros(target.FloatString(decimals)), strings.TrimSpace(minRaw))
		}
	}
	return trimDecimalZeros(target.FloatString(decimals)), nil
}

func capCloseSizeToPosition(posRaw, sizeRaw string) string {
	position, posErr := positiveRatFromDecimal(posRaw)
	size, sizeErr := positiveRatFromDecimal(sizeRaw)
	if posErr != nil || sizeErr != nil || position.Sign() <= 0 || size.Sign() <= 0 {
		return ""
	}
	if size.Cmp(position) <= 0 {
		return strings.TrimSpace(sizeRaw)
	}
	decimals := maxInt(decimalPlacesForDisplay(strings.TrimSpace(posRaw)), decimalPlacesForDisplay(strings.TrimSpace(sizeRaw)))
	return trimDecimalZeros(position.FloatString(decimals))
}

func positiveRatFromDecimal(raw string) (*big.Rat, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return nil, errors.New("empty decimal")
	}
	value, ok := new(big.Rat).SetString(text)
	if !ok {
		return nil, fmt.Errorf("invalid decimal %q", raw)
	}
	if value.Sign() < 0 {
		value.Abs(value)
	}
	return value, nil
}

func floorRatToStep(value, step *big.Rat) *big.Rat {
	if value == nil || step == nil || value.Sign() <= 0 || step.Sign() <= 0 {
		return new(big.Rat)
	}
	scaled := new(big.Rat).Quo(value, step)
	steps := new(big.Int).Quo(scaled.Num(), scaled.Denom())
	return new(big.Rat).Mul(new(big.Rat).SetInt(steps), step)
}

func placeBinancePositionClose(ctx context.Context, client binance.Client, position okx.Position, mode, closeSz string) (positionCloseOrder, error) {
	px := ""
	if mode == "limit" {
		var err error
		px, err = binanceLimitClosePrice(ctx, client, position)
		if err != nil {
			return positionCloseOrder{}, err
		}
	}
	req, partial, err := binancePositionCloseOrderRequest(position, mode, px, closeSz)
	if err != nil {
		return positionCloseOrder{}, err
	}
	ack, unknown, err := placeBinanceOrderWithUnknownRecovery(ctx, client, req)
	if err != nil {
		if split, splitErr := placeSplitBinancePositionCloseByMaxQty(ctx, client, position, req, px, partial, err); splitErr == nil {
			return split, nil
		}
		ack, unknown, err = retryBinanceReduceOnlyPositionClose(ctx, client, req, err)
		if err != nil && req.Type == "LIMIT" && binance.IsAPIErrorCode(err, -2022) {
			fallback, fallbackErr := placeBinanceReduceOnlyMarketFallback(ctx, client, position, req)
			if fallbackErr == nil {
				return fallback, nil
			}
			if binance.IsAPIErrorCode(fallbackErr, -2022) {
				plainFallback, plainErr := placeBinanceNonReduceOnlyMarketFallback(ctx, client, position, req)
				if plainErr == nil {
					return plainFallback, nil
				}
				fallbackErr = fmt.Errorf("market close without reduceOnly failed: %w", plainErr)
			}
			err = fmt.Errorf("Binance limit close rejected after reduce-only retries; market reduce-only fallback failed: %w", fallbackErr)
		}
		if err != nil {
			return positionCloseOrder{}, err
		}
	}
	return binancePositionCloseOrderFromAcks(position, []binance.OrderAck{ack}, px, req.Quantity, partial, unknown), nil
}

// placeBinanceNonReduceOnlyMarketFallback is the last-resort path for a
// one-way position when Binance persistently rejects reduceOnly with -2022.
// It clears and verifies all symbol orders, refreshes the live position, and
// caps the order to the equivalent fraction of that refreshed position.
func placeBinanceNonReduceOnlyMarketFallback(ctx context.Context, client binance.Client, originalPosition okx.Position, originalReq binance.PlaceOrderRequest) (positionCloseOrder, error) {
	if err := cancelAllBinanceSymbolOrders(ctx, client, originalReq.Symbol); err != nil {
		return positionCloseOrder{}, fmt.Errorf("cancel Binance symbol orders before non-reduce-only fallback failed: %w", err)
	}
	position, err := currentBinanceOpenPosition(ctx, client, originalPosition.InstID, originalPosition.PosSide)
	if err != nil {
		return positionCloseOrder{}, fmt.Errorf("refresh Binance position before non-reduce-only fallback failed: %w", err)
	}
	ratio := 1.0
	originalSize, originalErr := positiveRatFromDecimal(originalPosition.Pos)
	requestedSize, requestedErr := positiveRatFromDecimal(originalReq.Quantity)
	if originalErr != nil || requestedErr != nil || originalSize.Sign() <= 0 || requestedSize.Sign() <= 0 {
		return positionCloseOrder{}, errors.New("invalid original position or close quantity for non-reduce-only fallback")
	}
	if requestedSize.Cmp(originalSize) < 0 {
		ratioRat := new(big.Rat).Quo(requestedSize, originalSize)
		ratioFloat, _ := ratioRat.Float64()
		ratio = ratioFloat
	}
	size, err := binancePositionCloseSize(ctx, client, position, ratio)
	if err != nil {
		return positionCloseOrder{}, fmt.Errorf("size non-reduce-only fallback from refreshed position failed: %w", err)
	}
	size = capCloseSizeToPosition(position.Pos, size)
	if size == "" || size == "0" {
		return positionCloseOrder{}, errPositionNotOpen
	}
	req, partial, err := binancePositionCloseOrderRequest(position, "market", "", size)
	if err != nil {
		return positionCloseOrder{}, err
	}
	// This is deliberately the only path that submits an order without
	// reduceOnly, and is reached only after an explicit -2022 rejection.
	req.ReduceOnly = false
	ack, unknown, err := placeBinanceOrderWithUnknownRecovery(ctx, client, req)
	if err != nil {
		return positionCloseOrder{}, fmt.Errorf("Binance market close without reduceOnly failed: %w", err)
	}
	order := binancePositionCloseOrderFromAcks(position, []binance.OrderAck{ack}, "", req.Quantity, partial, unknown)
	order.MarketFallback = true
	order.NonReduceOnlyFallback = true
	return order, nil
}

func placeBinanceReduceOnlyMarketFallback(ctx context.Context, client binance.Client, originalPosition okx.Position, originalReq binance.PlaceOrderRequest) (positionCloseOrder, error) {
	position, err := currentBinanceOpenPosition(ctx, client, originalPosition.InstID, originalPosition.PosSide)
	if err != nil {
		return positionCloseOrder{}, fmt.Errorf("refresh Binance position before market fallback failed: %w", err)
	}
	size := capCloseSizeToPosition(position.Pos, originalReq.Quantity)
	if size == "" || size == "0" {
		return positionCloseOrder{}, errPositionNotOpen
	}
	req, partial, err := binancePositionCloseOrderRequest(position, "market", "", size)
	if err != nil {
		return positionCloseOrder{}, err
	}
	ack, unknown, err := placeBinanceOrderWithUnknownRecovery(ctx, client, req)
	if err != nil {
		ack, unknown, err = retryBinanceReduceOnlyPositionClose(ctx, client, req, err)
		if err != nil {
			return positionCloseOrder{}, fmt.Errorf("Binance market reduce-only fallback retry failed: %w", err)
		}
	}
	order := binancePositionCloseOrderFromAcks(position, []binance.OrderAck{ack}, "", req.Quantity, partial, unknown)
	order.MarketFallback = true
	return order, nil
}

func placeSplitBinancePositionCloseByMaxQty(ctx context.Context, client binance.Client, position okx.Position, req binance.PlaceOrderRequest, px string, partial bool, originalErr error) (positionCloseOrder, error) {
	if !binance.IsAPIErrorCode(originalErr, -4005) {
		return positionCloseOrder{}, originalErr
	}
	inst, err := client.SymbolInfo(ctx, req.Symbol)
	if err != nil {
		return positionCloseOrder{}, originalErr
	}
	filters, err := inst.TradingFilters()
	if err != nil {
		return positionCloseOrder{}, originalErr
	}
	reqs, err := binance.SplitPlaceOrderRequestByMaxQty(req, filters)
	if err != nil || len(reqs) <= 1 {
		return positionCloseOrder{}, originalErr
	}
	acks := make([]binance.OrderAck, 0, len(reqs))
	unknownAny := false
	for i, part := range reqs {
		ack, unknown, err := placeBinanceOrderWithUnknownRecovery(ctx, client, part)
		if err != nil {
			ack, unknown, err = retryBinanceReduceOnlyPositionClose(ctx, client, part, err)
			if err != nil {
				return positionCloseOrder{}, fmt.Errorf("Binance split close part %d/%d quantity %s: %w", i+1, len(reqs), part.Quantity, err)
			}
		}
		acks = append(acks, ack)
		unknownAny = unknownAny || unknown
	}
	return binancePositionCloseOrderFromAcks(position, acks, px, req.Quantity, partial, unknownAny), nil
}

func binancePositionCloseOrderFromAcks(position okx.Position, acks []binance.OrderAck, px, closeSz string, partial, unknown bool) positionCloseOrder {
	okxAcks := make([]okx.OrderAck, 0, len(acks))
	for _, ack := range acks {
		okxAcks = append(okxAcks, okxAckFromBinanceOrderAck(ack))
	}
	primary := okx.OrderAck{}
	if len(okxAcks) > 0 {
		primary = okxAcks[0]
	}
	return positionCloseOrder{
		Position: position,
		Ack:      primary,
		Acks:     okxAcks,
		Px:       px,
		CloseSz:  closeSz,
		Partial:  partial,
		Unknown:  unknown,
	}
}

func okxAckFromBinanceOrderAck(ack binance.OrderAck) okx.OrderAck {
	ordID := ""
	if ack.OrderID != 0 {
		ordID = strconv.FormatInt(ack.OrderID, 10)
	}
	return okx.OrderAck{
		OrdID:   ordID,
		ClOrdID: ack.ClientOrderID,
	}
}

func placeBinanceOrderWithUnknownRecovery(ctx context.Context, client binance.Client, req binance.PlaceOrderRequest) (binance.OrderAck, bool, error) {
	ack, err := client.PlaceOrder(ctx, req)
	if err == nil {
		return ack, false, nil
	}
	if !binance.IsExecutionStatusUnknown(err) || strings.TrimSpace(req.NewClientOrderID) == "" {
		return binance.OrderAck{}, false, err
	}
	if ack, found := recoverBinanceUnknownPlaceOrder(ctx, client, req); found {
		return ack, false, nil
	}
	return binanceUnknownOrderAck(req), true, nil
}

func retryBinanceReduceOnlyPositionClose(ctx context.Context, client binance.Client, req binance.PlaceOrderRequest, originalErr error) (binance.OrderAck, bool, error) {
	if !binance.IsAPIErrorCode(originalErr, -2022) {
		return binance.OrderAck{}, false, originalErr
	}
	attempts := binanceReduceOnlyRetryAttempts
	if attempts < 1 {
		attempts = 1
	}
	canceledRegularTotal := 0
	canceledAlgoTotal := 0
	lastErr := originalErr
	for attempt := 0; attempt < attempts; attempt++ {
		canceled, canceledAlgos, discoveryErr := cancelBinanceConflictingReduceOnlyOrders(ctx, client, req)
		canceledRegularTotal += canceled
		canceledAlgoTotal += canceledAlgos
		if discoveryErr != nil {
			lastErr = discoveryErr
		}
		if err := waitBinanceReduceOnlyRetry(ctx); err != nil {
			return binance.OrderAck{}, false, fmt.Errorf("Binance reduce-only close retry canceled %d regular and %d algorithm orders: %w", canceledRegularTotal, canceledAlgoTotal, err)
		}
		ack, unknown, err := placeBinanceOrderWithUnknownRecovery(ctx, client, req)
		if err == nil {
			return ack, unknown, nil
		}
		if !binance.IsAPIErrorCode(err, -2022) {
			return binance.OrderAck{}, false, fmt.Errorf("Binance reduce-only close retry failed after canceling %d regular and %d algorithm orders: %w", canceledRegularTotal, canceledAlgoTotal, err)
		}
		lastErr = err
	}
	return binance.OrderAck{}, false, fmt.Errorf("Binance reduce-only close retry failed after canceling %d regular and %d algorithm orders: %w", canceledRegularTotal, canceledAlgoTotal, lastErr)
}

func waitBinanceReduceOnlyRetry(ctx context.Context) error {
	delay := binanceReduceOnlyRetryDelay
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func recoverBinanceUnknownPlaceOrder(ctx context.Context, client binance.Client, req binance.PlaceOrderRequest) (binance.OrderAck, bool) {
	attempts := binanceUnknownOrderLookupAttempts
	if attempts < 1 {
		attempts = 1
	}
	query := binance.QueryOrderRequest{
		Symbol:            req.Symbol,
		OrigClientOrderID: strings.TrimSpace(req.NewClientOrderID),
	}
	for i := 0; i < attempts; i++ {
		order, err := client.QueryOrder(ctx, query)
		if err == nil {
			if binanceQueriedOrderMatchesRequest(order, req) {
				return binanceOrderAckFromOpenOrder(order, req), true
			}
			return binance.OrderAck{}, false
		}
		if !binancePendingOrderNoLongerOpen(err) && !binance.IsExecutionStatusUnknown(err) {
			return binance.OrderAck{}, false
		}
		if i == attempts-1 {
			break
		}
		timer := time.NewTimer(binanceUnknownOrderLookupDelay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return binance.OrderAck{}, false
		case <-timer.C:
		}
	}
	return binance.OrderAck{}, false
}

func binanceQueriedOrderMatchesRequest(order binance.OpenOrder, req binance.PlaceOrderRequest) bool {
	if !strings.EqualFold(strings.TrimSpace(order.Symbol), strings.TrimSpace(req.Symbol)) {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(order.ClientOrderID), strings.TrimSpace(req.NewClientOrderID)) {
		return false
	}
	if strings.TrimSpace(req.Side) != "" && !strings.EqualFold(strings.TrimSpace(order.Side), strings.TrimSpace(req.Side)) {
		return false
	}
	if strings.TrimSpace(req.Type) != "" && !strings.EqualFold(strings.TrimSpace(order.Type), strings.TrimSpace(req.Type)) {
		return false
	}
	return normalizedBinanceClosePositionSide(order.PositionSide) == normalizedBinanceClosePositionSide(req.PositionSide)
}

func binanceOrderAckFromOpenOrder(order binance.OpenOrder, req binance.PlaceOrderRequest) binance.OrderAck {
	ack := binance.OrderAck{
		OrderID:       order.OrderID,
		Symbol:        order.Symbol,
		Status:        order.Status,
		ClientOrderID: order.ClientOrderID,
		Price:         order.Price,
		OrigQty:       order.OrigQty,
		ExecutedQty:   order.ExecutedQty,
		Type:          order.Type,
		Side:          order.Side,
		PositionSide:  order.PositionSide,
	}
	if strings.TrimSpace(ack.Symbol) == "" {
		ack.Symbol = strings.ToUpper(strings.TrimSpace(req.Symbol))
	}
	if strings.TrimSpace(ack.ClientOrderID) == "" {
		ack.ClientOrderID = strings.TrimSpace(req.NewClientOrderID)
	}
	if strings.TrimSpace(ack.OrigQty) == "" {
		ack.OrigQty = strings.TrimSpace(req.Quantity)
	}
	if strings.TrimSpace(ack.Type) == "" {
		ack.Type = strings.ToUpper(strings.TrimSpace(req.Type))
	}
	if strings.TrimSpace(ack.Side) == "" {
		ack.Side = strings.ToUpper(strings.TrimSpace(req.Side))
	}
	if strings.TrimSpace(ack.PositionSide) == "" {
		ack.PositionSide = strings.ToUpper(strings.TrimSpace(req.PositionSide))
	}
	return ack
}

func binanceUnknownOrderAck(req binance.PlaceOrderRequest) binance.OrderAck {
	return binance.OrderAck{
		Symbol:        strings.ToUpper(strings.TrimSpace(req.Symbol)),
		Status:        "UNKNOWN",
		ClientOrderID: strings.TrimSpace(req.NewClientOrderID),
		Price:         strings.TrimSpace(req.Price),
		OrigQty:       strings.TrimSpace(req.Quantity),
		ExecutedQty:   "0",
		Type:          strings.ToUpper(strings.TrimSpace(req.Type)),
		Side:          strings.ToUpper(strings.TrimSpace(req.Side)),
		PositionSide:  strings.ToUpper(strings.TrimSpace(req.PositionSide)),
	}
}

func cancelBinanceConflictingReduceOnlyOpenOrders(ctx context.Context, client binance.Client, req binance.PlaceOrderRequest) (int, error) {
	orders, err := client.OpenOrders(ctx, req.Symbol)
	if err != nil {
		return 0, err
	}
	canceled := 0
	for _, order := range orders {
		if !binanceConflictingReduceOnlyCloseOrder(order, req) {
			continue
		}
		cancelReq := binance.CancelOrderRequest{Symbol: order.Symbol}
		if order.OrderID != 0 {
			cancelReq.OrderID = strconv.FormatInt(order.OrderID, 10)
		} else {
			cancelReq.OrigClientOrderID = strings.TrimSpace(order.ClientOrderID)
		}
		if _, err := client.CancelOrder(ctx, cancelReq); err != nil {
			if binanceOrderCancellationAlreadyDone(err) {
				canceled++
				continue
			}
			return canceled, fmt.Errorf("cancel conflicting Binance reduce-only close order failed: %w", err)
		}
		canceled++
	}
	return canceled, nil
}

func cancelBinanceConflictingReduceOnlyOrders(ctx context.Context, client binance.Client, req binance.PlaceOrderRequest) (int, int, error) {
	regularCanceled, regularErr := cancelBinanceConflictingReduceOnlyOpenOrders(ctx, client, req)
	algoCanceled, algoErr := cancelBinanceConflictingReduceOnlyAlgoOrders(ctx, client, req)
	if regularErr == nil && algoErr == nil {
		return regularCanceled, algoCanceled, nil
	}
	if regularErr != nil && algoErr != nil {
		return regularCanceled, algoCanceled, fmt.Errorf("inspect Binance regular and algorithm orders failed: regular: %v; algorithm: %v", regularErr, algoErr)
	}
	if regularErr != nil {
		return regularCanceled, algoCanceled, fmt.Errorf("inspect Binance regular orders failed: %w", regularErr)
	}
	return regularCanceled, algoCanceled, fmt.Errorf("inspect Binance algorithm orders failed: %w", algoErr)
}

func cancelBinanceConflictingReduceOnlyAlgoOrders(ctx context.Context, client binance.Client, req binance.PlaceOrderRequest) (int, error) {
	orders, err := client.OpenAlgoOrders(ctx, req.Symbol)
	if err != nil {
		return 0, err
	}
	canceled := 0
	for _, order := range orders {
		if !binanceConflictingReduceOnlyCloseAlgoOrder(order, req) {
			continue
		}
		if _, err := client.CancelAlgoOrder(ctx, order.AlgoID, strings.TrimSpace(order.ClientAlgoID)); err != nil {
			if binanceOrderCancellationAlreadyDone(err) {
				canceled++
				continue
			}
			return canceled, fmt.Errorf("cancel conflicting Binance reduce-only algo close order failed: %w", err)
		}
		canceled++
	}
	return canceled, nil
}

func binanceConflictingReduceOnlyCloseOrder(order binance.OpenOrder, req binance.PlaceOrderRequest) bool {
	if (!order.ReduceOnly && !order.ClosePosition && !binanceManagedCloseOrderID(order.ClientOrderID)) || !strings.EqualFold(order.Symbol, req.Symbol) || !strings.EqualFold(order.Side, req.Side) {
		return false
	}
	reqPosSide := normalizedBinanceClosePositionSide(req.PositionSide)
	orderPosSide := normalizedBinanceClosePositionSide(order.PositionSide)
	return reqPosSide == orderPosSide
}

func binanceConflictingReduceOnlyCloseAlgoOrder(order binance.AlgoOpenOrder, req binance.PlaceOrderRequest) bool {
	if (!order.ReduceOnly && !order.ClosePosition && !binanceManagedCloseOrderID(order.ClientAlgoID)) || !strings.EqualFold(order.Symbol, req.Symbol) || !strings.EqualFold(order.Side, req.Side) {
		return false
	}
	reqPosSide := normalizedBinanceClosePositionSide(req.PositionSide)
	orderPosSide := normalizedBinanceClosePositionSide(order.PositionSide)
	return reqPosSide == orderPosSide
}

func binanceManagedCloseOrderID(raw string) bool {
	raw = strings.ToUpper(strings.TrimSpace(raw))
	return strings.HasPrefix(raw, "PC") || strings.HasPrefix(raw, "PP")
}

func binanceOrderCancellationAlreadyDone(err error) bool {
	if err == nil {
		return false
	}
	if binance.IsAPIErrorCode(err, -2011, -2013) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unknown order") || strings.Contains(message, "order does not exist")
}

func normalizedBinanceClosePositionSide(raw string) string {
	raw = strings.ToUpper(strings.TrimSpace(raw))
	if raw == "" || raw == "BOTH" {
		return "BOTH"
	}
	return raw
}

func binancePositionCloseOrderRequest(position okx.Position, mode, px, closeSz string) (binance.PlaceOrderRequest, bool, error) {
	side, err := closeOrderSide(position)
	if err != nil {
		return binance.PlaceOrderRequest{}, false, err
	}
	size := strings.TrimSpace(closeSz)
	partial := size != ""
	if size == "" {
		size = absolutePositionSize(position.Pos)
	}
	if size == "" || size == "0" {
		return binance.PlaceOrderRequest{}, false, errPositionNotOpen
	}
	ordType := strings.ToUpper(strings.TrimSpace(mode))
	if ordType == "" {
		ordType = "MARKET"
	}
	req := binance.PlaceOrderRequest{
		Symbol:           strings.ToUpper(strings.TrimSpace(position.InstID)),
		Side:             strings.ToUpper(side),
		Type:             ordType,
		Quantity:         size,
		Price:            px,
		NewClientOrderID: nextPositionCloseClOrdID(),
		PositionSide:     binanceClosePositionSide(position.PosSide),
	}
	if req.Type == "LIMIT" {
		req.TimeInForce = "GTC"
	}
	if req.PositionSide == "" || req.PositionSide == "BOTH" {
		req.ReduceOnly = true
	}
	return req, partial, nil
}

func binanceClosePositionSide(posSide string) string {
	switch normalizePosSide(posSide) {
	case "long":
		return "LONG"
	case "short":
		return "SHORT"
	default:
		// Binance one-way mode uses BOTH. Include it explicitly so closing
		// orders carry the same position mode as the live position response.
		return "BOTH"
	}
}

func binanceLimitClosePrice(ctx context.Context, client binance.Client, position okx.Position) (string, error) {
	inst, err := client.SymbolInfo(ctx, position.InstID)
	if err != nil {
		return "", err
	}
	tickRaw, err := binanceTickSizeRaw(inst)
	if err != nil {
		return "", err
	}
	ticker, err := client.BookTicker(ctx, position.InstID)
	if err != nil {
		return "", err
	}
	mid, err := binanceBookMidPrice(ticker)
	if err != nil {
		return "", err
	}
	side, err := closeOrderSide(position)
	if err != nil {
		return "", err
	}
	return priceOneTickFromMidValue(mid, tickRaw, side)
}

func (s *Server) watchLimitPositionClose(apiID string, cfg config.Config, client okx.Client, active positionCloseOrder) {
	s.watchLimitPositionCloseWithOptions(apiID, cfg, client, active, positionClosePollInterval, positionCloseLimitTimeout, false)
}

func (s *Server) watchAutoProfitLimitPositionClose(apiID string, cfg config.Config, client okx.Client, active positionCloseOrder) {
	s.watchLimitPositionCloseWithOptions(apiID, cfg, client, active, autoProfitClosePollInterval, autoProfitCloseLimitTimeout, true)
}

func (s *Server) watchLimitPositionCloseWithOptions(apiID string, cfg config.Config, client okx.Client, active positionCloseOrder, pollInterval, timeoutDuration time.Duration, cancelProtectionOrders bool) {
	key := positionCloseKey(trading.ExchangeOKX, apiID, active.Position.InstID, active.Position.PosSide)
	defer positionCloseJobs.done(key)

	if pollInterval <= 0 {
		pollInterval = positionClosePollInterval
	}
	if timeoutDuration <= 0 {
		timeoutDuration = positionCloseLimitTimeout
	}
	poll := time.NewTicker(pollInterval)
	defer poll.Stop()
	timeout := time.NewTimer(timeoutDuration)
	defer timeout.Stop()

	for {
		select {
		case <-poll.C:
			var (
				next   positionCloseOrder
				closed bool
				err    error
			)
			if active.Partial {
				next, closed, err = refreshPartialLimitPositionClose(cfg, client, active)
			} else {
				next, closed, err = refreshLimitPositionClose(cfg, client, active)
			}
			if err != nil {
				s.logPositionCloseError("limit position close refresh failed", err, active.Position)
				continue
			}
			if closed {
				s.cancelClosedOKXPositionOrders(client, active.Position, cancelProtectionOrders && !active.Partial)
				return
			}
			active = next
		case <-timeout.C:
			var err error
			if active.Partial {
				err = fallbackPartialMarketPositionClose(cfg, client, active)
			} else {
				err = fallbackMarketPositionClose(cfg, client, active)
			}
			if err != nil {
				s.logPositionCloseError("limit position close fallback failed", err, active.Position)
			} else {
				s.cancelClosedOKXPositionOrders(client, active.Position, cancelProtectionOrders && !active.Partial)
			}
			return
		}
	}
}

func (s *Server) cancelClosedOKXPositionOrders(client okx.Client, position okx.Position, cancelProtectionOrders bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for {
		closed, err := positionClosed(ctx, client, position)
		if err != nil {
			s.logPositionCloseError("failed to confirm position close before canceling pending orders", err, position)
			return
		}
		if closed {
			break
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			s.logPositionCloseError("position close not confirmed before pending-order cancellation", ctx.Err(), position)
			return
		case <-timer.C:
		}
	}
	if err := cancelOKXPositionPendingOrders(ctx, client, position); err != nil {
		s.logPositionCloseError("failed to cancel closed position pending orders", err, position)
	}
	if cancelProtectionOrders {
		if err := cancelOKXPositionProtectionOrders(ctx, client, position); err != nil {
			s.logPositionCloseError("failed to cancel closed position protection orders", err, position)
		}
	}
}

func cancelOKXPositionPendingOrders(ctx context.Context, client okx.Client, position okx.Position) error {
	orders, _, err := client.PendingOrders(ctx, "SWAP")
	if err != nil {
		return err
	}
	for _, order := range orders {
		if !strings.EqualFold(strings.TrimSpace(order.InstID), strings.TrimSpace(position.InstID)) {
			continue
		}
		if err := cancelPendingOrder(ctx, client, order); err != nil {
			req := pendingOrderChaseRequest{InstID: order.InstID, OrdID: order.OrdID, ClOrdID: order.ClOrdID}
			if _, stillOpen, checkErr := currentPendingOrder(ctx, client, req); checkErr == nil && !stillOpen {
				continue
			}
			return err
		}
	}
	return nil
}

func cancelOKXPositionProtectionOrders(ctx context.Context, client okx.Client, position okx.Position) error {
	orders, _, err := client.PendingAlgoOrders(ctx, "SWAP", position.InstID)
	if err != nil {
		return err
	}
	reqs := make([]okx.CancelAlgoOrderRequest, 0, len(orders))
	for _, order := range orders {
		if !okxPositionProtectionOrderMatches(order, position) {
			continue
		}
		req := okx.CancelAlgoOrderRequest{InstID: strings.ToUpper(strings.TrimSpace(order.InstID))}
		if strings.TrimSpace(order.AlgoID) != "" {
			req.AlgoID = strings.TrimSpace(order.AlgoID)
		} else if strings.TrimSpace(order.AlgoClOrdID) != "" {
			req.AlgoClOrdID = strings.TrimSpace(order.AlgoClOrdID)
		} else {
			continue
		}
		reqs = append(reqs, req)
	}
	for len(reqs) > 0 {
		n := min(len(reqs), 10)
		if _, _, err := client.CancelAlgoOrders(ctx, reqs[:n]); err != nil {
			return err
		}
		reqs = reqs[n:]
	}
	return nil
}

func okxPositionProtectionOrderMatches(order okx.AlgoOrder, position okx.Position) bool {
	if !strings.EqualFold(strings.TrimSpace(order.InstID), strings.TrimSpace(position.InstID)) {
		return false
	}
	if !okxProtectionPriceSet(order.TPTriggerPx) && !okxProtectionPriceSet(order.SLTriggerPx) {
		return false
	}
	posSide := normalizePosSide(position.PosSide)
	if posSide != "" && posSide != "net" {
		return normalizePosSide(order.PosSide) == posSide
	}
	side, err := closeOrderSide(position)
	return err == nil && strings.EqualFold(strings.TrimSpace(order.Side), side)
}

func okxProtectionPriceSet(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && value != "-1"
}

func refreshLimitPositionClose(cfg config.Config, client okx.Client, active positionCloseOrder) (positionCloseOrder, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	position, err := currentOpenPosition(ctx, client, active.Position.InstID, active.Position.PosSide)
	if err != nil {
		if errors.Is(err, errPositionNotOpen) {
			return active, true, nil
		}
		return active, false, err
	}
	nextPx, err := limitClosePrice(ctx, client, position)
	if err != nil {
		return active, false, err
	}
	if nextPx == active.Px {
		active.Position = position
		return active, false, nil
	}
	if err := cancelPositionCloseOrder(ctx, client, active); err != nil {
		if closed, checkErr := positionClosed(ctx, client, active.Position); checkErr == nil && closed {
			return active, true, nil
		}
		return active, false, err
	}
	next, err := placeLimitPositionClose(ctx, cfg, client, position, "")
	if err != nil {
		return active, false, err
	}
	return next, false, nil
}

func refreshPartialLimitPositionClose(cfg config.Config, client okx.Client, active positionCloseOrder) (positionCloseOrder, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	order, found, err := currentOKXPositionCloseOrder(ctx, client, active)
	if err != nil {
		return active, false, err
	}
	if !found {
		return active, true, nil
	}
	remaining, err := pendingOrderRemainingSize(order)
	if err != nil {
		if errors.Is(err, errPendingOrderNoRemaining) {
			return active, true, nil
		}
		return active, false, err
	}
	position, err := currentOpenPosition(ctx, client, active.Position.InstID, active.Position.PosSide)
	if err != nil {
		if errors.Is(err, errPositionNotOpen) {
			return active, true, nil
		}
		return active, false, err
	}
	remaining = capCloseSizeToPosition(position.Pos, remaining)
	if remaining == "" || remaining == "0" {
		return active, true, nil
	}
	nextPx, err := limitClosePrice(ctx, client, position)
	if err != nil {
		return active, false, err
	}
	if nextPx == active.Px {
		active.Position = position
		active.CloseSz = remaining
		return active, false, nil
	}
	if err := cancelPendingOrder(ctx, client, order); err != nil {
		if _, stillOpen, checkErr := currentOKXPositionCloseOrder(ctx, client, active); checkErr == nil && !stillOpen {
			return active, true, nil
		}
		return active, false, err
	}
	next, err := placeLimitPositionClose(ctx, cfg, client, position, remaining)
	if err != nil {
		return active, false, err
	}
	return next, false, nil
}

func fallbackMarketPositionClose(cfg config.Config, client okx.Client, active positionCloseOrder) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := cancelPositionCloseOrder(ctx, client, active); err != nil {
		closed, checkErr := positionClosed(ctx, client, active.Position)
		if checkErr == nil && closed {
			return nil
		}
	}
	position, err := currentOpenPosition(ctx, client, active.Position.InstID, active.Position.PosSide)
	if err != nil {
		if errors.Is(err, errPositionNotOpen) {
			return nil
		}
		return err
	}
	_, err = placeMarketPositionClose(ctx, cfg, client, position, "")
	return err
}

func fallbackPartialMarketPositionClose(cfg config.Config, client okx.Client, active positionCloseOrder) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	order, found, err := currentOKXPositionCloseOrder(ctx, client, active)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	remaining, err := pendingOrderRemainingSize(order)
	if err != nil {
		if errors.Is(err, errPendingOrderNoRemaining) {
			return nil
		}
		return err
	}
	if err := cancelPendingOrder(ctx, client, order); err != nil {
		if _, stillOpen, checkErr := currentOKXPositionCloseOrder(ctx, client, active); checkErr == nil && !stillOpen {
			return nil
		}
		return err
	}
	position, err := currentOpenPosition(ctx, client, active.Position.InstID, active.Position.PosSide)
	if err != nil {
		if errors.Is(err, errPositionNotOpen) {
			return nil
		}
		return err
	}
	remaining = capCloseSizeToPosition(position.Pos, remaining)
	if remaining == "" || remaining == "0" {
		return nil
	}
	_, err = placeMarketPositionClose(ctx, cfg, client, position, remaining)
	return err
}

func (s *Server) watchBinanceLimitPositionClose(apiID string, client binance.Client, active positionCloseOrder) {
	key := positionCloseKey(trading.ExchangeBinance, apiID, active.Position.InstID, active.Position.PosSide)
	defer positionCloseJobs.done(key)

	poll := time.NewTicker(positionClosePollInterval)
	defer poll.Stop()
	timeout := time.NewTimer(positionCloseLimitTimeout)
	defer timeout.Stop()

	for {
		select {
		case <-poll.C:
			next, closed, err := refreshBinanceLimitPositionClose(client, active)
			if err != nil {
				s.logPositionCloseError("Binance limit position close refresh failed", err, active.Position)
				continue
			}
			if closed {
				return
			}
			active = next
		case <-timeout.C:
			if err := fallbackBinanceMarketPositionClose(client, active); err != nil {
				s.logPositionCloseError("Binance limit position close fallback failed", err, active.Position)
			}
			return
		}
	}
}

func refreshBinanceLimitPositionClose(client binance.Client, active positionCloseOrder) (positionCloseOrder, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	orders, found, err := currentBinancePositionCloseOrders(ctx, client, active)
	if err != nil {
		return active, false, err
	}
	if !found {
		if !active.Partial {
			position, err := currentBinanceOpenPosition(ctx, client, active.Position.InstID, active.Position.PosSide)
			if err != nil {
				if errors.Is(err, errPositionNotOpen) {
					return active, true, nil
				}
				return active, false, err
			}
			next, err := placeBinancePositionClose(ctx, client, position, "limit", "")
			if err != nil {
				return active, false, err
			}
			return next, false, nil
		}
		return active, true, nil
	}
	remaining, err := pendingOrdersRemainingSize(orders)
	if err != nil {
		if errors.Is(err, errPendingOrderNoRemaining) {
			return active, true, nil
		}
		return active, false, err
	}
	position, err := currentBinanceOpenPosition(ctx, client, active.Position.InstID, active.Position.PosSide)
	if err != nil {
		if errors.Is(err, errPositionNotOpen) {
			return active, true, nil
		}
		return active, false, err
	}
	remaining = capCloseSizeToPosition(position.Pos, remaining)
	if remaining == "" || remaining == "0" {
		return active, true, nil
	}
	nextPx, err := binanceLimitClosePrice(ctx, client, position)
	if err != nil {
		return active, false, err
	}
	if nextPx == active.Px {
		active.Position = position
		active.CloseSz = remaining
		return active, false, nil
	}
	for _, order := range orders {
		if err := cancelBinancePendingOrder(ctx, client, order); err != nil {
			if _, stillOpen, checkErr := currentBinancePositionCloseOrders(ctx, client, active); checkErr == nil && !stillOpen {
				return active, true, nil
			}
			return active, false, err
		}
	}
	next, err := placeBinancePositionClose(ctx, client, position, "limit", remaining)
	if err != nil {
		return active, false, err
	}
	return next, false, nil
}

func fallbackBinanceMarketPositionClose(client binance.Client, active positionCloseOrder) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	orders, found, err := currentBinancePositionCloseOrders(ctx, client, active)
	if err != nil {
		return err
	}
	remaining := ""
	if found {
		remaining, err = pendingOrdersRemainingSize(orders)
		if err != nil {
			if errors.Is(err, errPendingOrderNoRemaining) {
				return nil
			}
			return err
		}
		for _, order := range orders {
			if err := cancelBinancePendingOrder(ctx, client, order); err != nil {
				if _, stillOpen, checkErr := currentBinancePositionCloseOrders(ctx, client, active); checkErr == nil && !stillOpen {
					return nil
				}
				return err
			}
		}
	} else if active.Partial {
		return nil
	}
	position, err := currentBinanceOpenPosition(ctx, client, active.Position.InstID, active.Position.PosSide)
	if err != nil {
		if errors.Is(err, errPositionNotOpen) {
			return nil
		}
		return err
	}
	if remaining == "" {
		remaining = absolutePositionSize(position.Pos)
	}
	remaining = capCloseSizeToPosition(position.Pos, remaining)
	if remaining == "" || remaining == "0" {
		return nil
	}
	_, err = placeBinancePositionClose(ctx, client, position, "market", remaining)
	return err
}

func positionClosed(ctx context.Context, client okx.Client, position okx.Position) (bool, error) {
	_, err := currentOpenPosition(ctx, client, position.InstID, position.PosSide)
	if err == nil {
		return false, nil
	}
	if errors.Is(err, errPositionNotOpen) {
		return true, nil
	}
	return false, err
}

func cancelPositionCloseOrder(ctx context.Context, client okx.Client, active positionCloseOrder) error {
	if strings.TrimSpace(active.Ack.OrdID) == "" && strings.TrimSpace(active.Ack.ClOrdID) == "" {
		return nil
	}
	req := okx.CancelOrderRequest{InstID: active.Position.InstID}
	if strings.TrimSpace(active.Ack.OrdID) != "" {
		req.OrdID = active.Ack.OrdID
	} else {
		req.ClOrdID = active.Ack.ClOrdID
	}
	_, _, err := client.CancelOrder(ctx, req)
	return err
}

func positionCloseOrderRequest(cfg config.Config, position okx.Position, ordType, px, closeSz string) (okx.PlaceOrderRequest, bool, error) {
	side, err := closeOrderSide(position)
	if err != nil {
		return okx.PlaceOrderRequest{}, false, err
	}
	size := strings.TrimSpace(closeSz)
	partial := size != ""
	if size == "" {
		size = absolutePositionSize(position.Pos)
	}
	if size == "" || size == "0" {
		return okx.PlaceOrderRequest{}, false, errPositionNotOpen
	}
	tdMode := strings.ToLower(strings.TrimSpace(position.MgnMode))
	if tdMode == "" {
		tdMode = cfg.MarginMode()
	}
	posSide := normalizePosSide(position.PosSide)
	req := okx.PlaceOrderRequest{
		InstID:     strings.ToUpper(strings.TrimSpace(position.InstID)),
		TDMode:     tdMode,
		ClOrdID:    nextPositionCloseClOrdID(),
		Side:       side,
		OrdType:    strings.ToLower(strings.TrimSpace(ordType)),
		Px:         px,
		Sz:         size,
		ReduceOnly: posSide == "" || posSide == "net",
	}
	if posSide != "" && posSide != "net" {
		req.PosSide = posSide
	}
	return req, partial, nil
}

func limitClosePrice(ctx context.Context, client okx.Client, position okx.Position) (string, error) {
	inst, err := client.SwapInstrument(ctx, position.InstID)
	if err != nil {
		return "", err
	}
	ticker, _, err := client.MarketTicker(ctx, position.InstID)
	if err != nil {
		return "", err
	}
	side, err := closeOrderSide(position)
	if err != nil {
		return "", err
	}
	return priceOneTickFromMid(ticker, inst.TickSz, side)
}

func priceOneTickFromMid(ticker okx.Ticker, tickRaw, side string) (string, error) {
	bid, bidErr := strconv.ParseFloat(strings.TrimSpace(ticker.BidPx), 64)
	ask, askErr := strconv.ParseFloat(strings.TrimSpace(ticker.AskPx), 64)
	if bidErr != nil || askErr != nil || bid <= 0 || ask <= 0 {
		last, lastErr := strconv.ParseFloat(strings.TrimSpace(ticker.Last), 64)
		if lastErr != nil || last <= 0 {
			return "", fmt.Errorf("invalid ticker bid/ask for %s", ticker.InstID)
		}
		bid, ask = last, last
	}
	mid := (bid + ask) / 2
	return priceOneTickFromMidValue(mid, tickRaw, side)
}

func priceOneTickFromMidValue(mid float64, tickRaw, side string) (string, error) {
	tick, err := strconv.ParseFloat(strings.TrimSpace(tickRaw), 64)
	if err != nil || tick <= 0 {
		return "", fmt.Errorf("invalid tick size %q", tickRaw)
	}
	switch side {
	case "sell":
		return formatPriceToTick(mid-tick, tick, tickRaw, false)
	case "buy":
		return formatPriceToTick(mid+tick, tick, tickRaw, true)
	default:
		return "", fmt.Errorf("unsupported close side %q", side)
	}
}

func formatPriceToTick(value, tick float64, tickRaw string, roundUp bool) (string, error) {
	if value <= 0 || tick <= 0 {
		return "", fmt.Errorf("invalid close price %.12g for tick %.12g", value, tick)
	}
	scaled := value / tick
	if roundUp {
		value = math.Ceil(scaled-1e-9) * tick
	} else {
		value = math.Floor(scaled+1e-9) * tick
	}
	if value <= 0 {
		return "", fmt.Errorf("invalid close price after tick rounding")
	}
	decimals := decimalsFromDecimalString(tickRaw)
	out := strconv.FormatFloat(value, 'f', decimals, 64)
	return trimDecimalZeros(out), nil
}

func closeOrderSide(position okx.Position) (string, error) {
	switch normalizePosSide(position.PosSide) {
	case "long":
		return "sell", nil
	case "short":
		return "buy", nil
	}
	value, err := strconv.ParseFloat(strings.TrimSpace(position.Pos), 64)
	if err != nil {
		if strings.HasPrefix(strings.TrimSpace(position.Pos), "-") {
			return "buy", nil
		}
		return "sell", nil
	}
	if value < 0 {
		return "buy", nil
	}
	if value > 0 {
		return "sell", nil
	}
	return "", errPositionNotOpen
}

func absolutePositionSize(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return strings.TrimPrefix(raw, "-")
	}
	return trimDecimalZeros(strconv.FormatFloat(math.Abs(value), 'f', -1, 64))
}

func normalizePosSide(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "net" || raw == "both" {
		return ""
	}
	return raw
}

func (r *pendingOrderChaseRequest) normalize() {
	r.Exchange = trading.NormalizeExchange(r.Exchange)
	r.Env = strings.ToLower(strings.TrimSpace(r.Env))
	r.APIID = strings.TrimSpace(r.APIID)
	r.OrderGroup = strings.ToLower(strings.TrimSpace(r.OrderGroup))
	if r.OrderGroup == "" {
		r.OrderGroup = "normal"
	}
	r.InstID = strings.ToUpper(strings.TrimSpace(r.InstID))
	r.OrdID = strings.TrimSpace(r.OrdID)
	r.ClOrdID = strings.TrimSpace(r.ClOrdID)
	r.AlgoID = strings.TrimSpace(r.AlgoID)
	r.AlgoClOrdID = strings.TrimSpace(r.AlgoClOrdID)
}

func (r pendingOrderChaseRequest) validate() error {
	if r.Env != "" && r.Env != config.EnvDemo && r.Env != config.EnvLive {
		return errors.New("env must be demo or live")
	}
	if r.InstID == "" {
		return errors.New("inst_id is required")
	}
	switch r.OrderGroup {
	case "normal":
	case "algo":
		if r.AlgoID == "" && r.AlgoClOrdID == "" {
			return errors.New("algo_id or algo_cl_ord_id is required")
		}
		return nil
	default:
		return errors.New("order_group must be normal or algo")
	}
	if r.OrdID == "" && r.ClOrdID == "" {
		return errors.New("ord_id or cl_ord_id is required")
	}
	return nil
}

func pendingOrderChaseKey(req pendingOrderChaseRequest) string {
	group := strings.ToLower(strings.TrimSpace(req.OrderGroup))
	if group == "" {
		group = "normal"
	}
	id := strings.TrimSpace(req.OrdID)
	if group == "algo" {
		id = strings.TrimSpace(req.AlgoID)
		if id == "" {
			id = "acl:" + strings.TrimSpace(req.AlgoClOrdID)
		}
	} else if id == "" {
		id = "cl:" + strings.TrimSpace(req.ClOrdID)
	}
	return trading.NormalizeExchange(req.Exchange) + "|" + strings.TrimSpace(req.APIID) + "|" + group + "|" + strings.ToUpper(strings.TrimSpace(req.InstID)) + "|" + id
}

func positionCloseKey(exchange, apiID, instID, posSide string) string {
	side := normalizePosSide(posSide)
	if side == "" {
		side = "net"
	}
	return trading.NormalizeExchange(exchange) + "|" + strings.TrimSpace(apiID) + "|" + strings.ToUpper(strings.TrimSpace(instID)) + "|" + side
}

func validPositionProtectionKind(kind string) bool {
	switch kind {
	case positionProtectionTP, positionProtectionSL, positionProtectionTrailing:
		return true
	default:
		return false
	}
}

func positionProtectionMessage(kind string) string {
	switch kind {
	case positionProtectionTP:
		return "take profit protection order submitted"
	case positionProtectionSL:
		return "stop loss protection order submitted"
	case positionProtectionTrailing:
		return "trailing stop protection order submitted"
	default:
		return "position protection order submitted"
	}
}

func positionProtectionTriggerPrice(position okx.Position, kind, tickRaw string, takeProfitPct, stopLossPct float64) (string, error) {
	entry, err := strconv.ParseFloat(strings.TrimSpace(position.AvgPx), 64)
	if err != nil || entry <= 0 {
		return "", fmt.Errorf("position avgPx is required for protection orders")
	}
	side, err := closeOrderSide(position)
	if err != nil {
		return "", err
	}
	longPosition := side == "sell"
	var target float64
	var roundUp bool
	switch kind {
	case positionProtectionTP:
		if math.IsNaN(takeProfitPct) || math.IsInf(takeProfitPct, 0) || takeProfitPct <= 0 {
			return "", fmt.Errorf("take_profit_pct must be positive")
		}
		if longPosition {
			target = entry * (1 + takeProfitPct/100)
			roundUp = true
		} else {
			target = entry * (1 - takeProfitPct/100)
			roundUp = false
		}
	case positionProtectionSL:
		if math.IsNaN(stopLossPct) || math.IsInf(stopLossPct, 0) || stopLossPct <= 0 {
			return "", fmt.Errorf("stop_loss_pct must be positive")
		}
		if longPosition {
			target = entry * (1 - stopLossPct/100)
			roundUp = false
		} else {
			target = entry * (1 + stopLossPct/100)
			roundUp = true
		}
	default:
		return "", fmt.Errorf("unsupported position protection kind %q", kind)
	}
	tick, err := strconv.ParseFloat(strings.TrimSpace(tickRaw), 64)
	if err != nil || tick <= 0 {
		return "", fmt.Errorf("invalid tick size %q", tickRaw)
	}
	return formatPriceToTick(target, tick, tickRaw, roundUp)
}

func okxPositionProtectionCallbackRatio(trailingPct float64) (string, error) {
	if math.IsNaN(trailingPct) || math.IsInf(trailingPct, 0) || trailingPct <= 0 {
		return "", fmt.Errorf("trailing_pct must be positive")
	}
	return trading.NormalizeFloat(trailingPct / 100), nil
}

func binancePositionProtectionCallbackRate(trailingPct float64) (string, error) {
	if math.IsNaN(trailingPct) || math.IsInf(trailingPct, 0) || trailingPct <= 0 {
		return "", fmt.Errorf("Binance trailing_pct must be positive")
	}
	if trailingPct < 0.1 || trailingPct > 10 {
		return "", fmt.Errorf("Binance trailing_pct must be between 0.1 and 10, got %s", trading.NormalizeFloat(trailingPct))
	}
	return trading.NormalizeFloat(trailingPct), nil
}

func nextPositionCloseClOrdID() string {
	seq := atomic.AddUint64(&positionCloseSeq, 1) % 1000000
	return fmt.Sprintf("PC%d%06d", time.Now().UTC().UnixMilli(), seq)
}

func nextGatePositionCloseText() string {
	return "t-" + nextPositionCloseClOrdID()
}

func nextPositionProtectionClOrdID(kind string) string {
	suffix := "PR"
	switch kind {
	case positionProtectionTP:
		suffix = "TP"
	case positionProtectionSL:
		suffix = "SL"
	case positionProtectionTrailing:
		suffix = "TS"
	}
	seq := atomic.AddUint64(&positionProtectionSeq, 1) % 1000000
	return fmt.Sprintf("PP%d%06d%s", time.Now().UTC().UnixMilli(), seq, suffix)
}

func nextPendingOrderLimitClOrdID() string {
	seq := atomic.AddUint64(&pendingOrderMarketSeq, 1) % 1000000
	return fmt.Sprintf("PCL%d%06d", time.Now().UTC().UnixMilli(), seq)
}

func nextPendingOrderMarketClOrdID() string {
	seq := atomic.AddUint64(&pendingOrderMarketSeq, 1) % 1000000
	return fmt.Sprintf("PCM%d%06d", time.Now().UTC().UnixMilli(), seq)
}

func nextPendingOrderAlgoClOrdID() string {
	seq := atomic.AddUint64(&pendingOrderMarketSeq, 1) % 1000000
	return fmt.Sprintf("PCA%d%06d", time.Now().UTC().UnixMilli(), seq)
}

func decimalsFromDecimalString(raw string) int {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" {
		return 0
	}
	if i := strings.Index(raw, "e-"); i >= 0 {
		n, err := strconv.Atoi(raw[i+2:])
		if err == nil && n > 0 {
			return n
		}
	}
	if i := strings.Index(raw, "."); i >= 0 {
		return len(strings.TrimRight(raw[i+1:], "0"))
	}
	return 0
}

func trimDecimalZeros(raw string) string {
	raw = strings.TrimSpace(raw)
	if strings.Contains(raw, ".") {
		raw = strings.TrimRight(strings.TrimRight(raw, "0"), ".")
	}
	if raw == "-0" || raw == "" {
		return "0"
	}
	return raw
}

func (s *Server) logPositionCloseError(message string, err error, position okx.Position) {
	if s.Logger == nil {
		return
	}
	s.Logger.Warn(message,
		"error", err,
		"inst_id", position.InstID,
		"pos_side", position.PosSide,
	)
}

func isOpenPosition(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return true
	}
	return math.Abs(value) > 0
}
