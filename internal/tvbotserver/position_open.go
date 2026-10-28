package server

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	tvbotconfig "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotconfig"
	tvbotokx "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotokx"
	tvbotstorage "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotstorage"
	tvbottrading "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

var errPositionOpenAlreadyRunning = errors.New("position open is already running for this position")

type positionOpenRequest struct {
	Exchange string  `json:"exchange"`
	Env      string  `json:"env"`
	APIID    string  `json:"api_id"`
	InstID   string  `json:"inst_id"`
	PosSide  string  `json:"pos_side"`
	Ratio    float64 `json:"ratio"`
}

type positionOpenResponse struct {
	OK              bool    `json:"ok"`
	Status          string  `json:"status"`
	SignalID        string  `json:"signal_id,omitempty"`
	Exchange        string  `json:"exchange"`
	APIID           string  `json:"api_id"`
	InstID          string  `json:"inst_id"`
	PositionSide    string  `json:"position_side"`
	Ratio           float64 `json:"ratio"`
	CurrentNotional string  `json:"current_notional_usdt,omitempty"`
	OpenNotional    string  `json:"open_notional_usdt,omitempty"`
	Message         string  `json:"message,omitempty"`
}

type positionOpenRegistry struct {
	mu     sync.Mutex
	active map[string]struct{}
}

func newPositionOpenRegistry() *positionOpenRegistry {
	return &positionOpenRegistry{active: map[string]struct{}{}}
}

func (r *positionOpenRegistry) start(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.active[key]; exists {
		return false
	}
	r.active[key] = struct{}{}
	return true
}

func (r *positionOpenRegistry) done(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.active, key)
}

var positionOpenJobs = newPositionOpenRegistry()

func positionOpenRatio(raw float64) (float64, error) {
	if math.IsNaN(raw) || math.IsInf(raw, 0) {
		return 0, errors.New("ratio must be one of 0.1, 0.25, 0.5 or 0.75")
	}
	for _, allowed := range []float64{0.1, 0.25, 0.5, 0.75} {
		if math.Abs(raw-allowed) < 1e-9 {
			return allowed, nil
		}
	}
	return 0, errors.New("ratio must be one of 0.1, 0.25, 0.5 or 0.75")
}

func positionOpenKey(tenantID string, req positionOpenRequest, apiID string, side tvbottrading.Side) string {
	return strings.Join([]string{
		strings.TrimSpace(tenantID),
		tvbottrading.NormalizeExchange(req.Exchange),
		tvbottrading.NormalizeTradeEnv(req.Env),
		strings.TrimSpace(apiID),
		strings.ToUpper(strings.TrimSpace(req.InstID)),
		string(side),
	}, "|")
}

func (s *Server) handlePositionOpen(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "only POST is allowed")
		return
	}
	var req positionOpenRequest
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	req.Exchange = tvbottrading.NormalizeExchange(req.Exchange)
	var err error
	req.Env, err = normalizeRequestedTradeEnv(req.Env)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_position_open", err.Error())
		return
	}
	req.APIID = strings.TrimSpace(req.APIID)
	req.InstID = strings.ToUpper(strings.TrimSpace(req.InstID))
	req.PosSide = normalizePosSide(req.PosSide)
	ratio, err := positionOpenRatio(req.Ratio)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_position_open", err.Error())
		return
	}
	if req.InstID == "" {
		writeError(w, http.StatusBadRequest, "invalid_position_open", "inst_id is required")
		return
	}
	if req.Exchange != tvbottrading.ExchangeOKX && req.Exchange != tvbottrading.ExchangeBinance && req.Exchange != tvbottrading.ExchangeGate {
		writeError(w, http.StatusBadRequest, "invalid_position_open", "unsupported exchange")
		return
	}
	cfg, err := configForRequestedTradeEnv(s.ConfigStore.Get(), req.Env)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_position_open", err.Error())
		return
	}
	req.Env = cfg.Trading.Env
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	position, apiID, err := s.currentPositionForOpen(ctx, cfg, req)
	if err != nil {
		if errors.Is(err, errPositionNotOpen) {
			writeError(w, http.StatusConflict, "position_not_open", err.Error())
			return
		}
		writeError(w, http.StatusBadGateway, "positions_failed", err.Error())
		return
	}
	action, positionSide, err := positionOpenAction(position)
	if err != nil {
		writeError(w, http.StatusConflict, "invalid_position_open", err.Error())
		return
	}
	if req.PosSide != "" && req.PosSide != "net" && req.PosSide != positionSide {
		writeError(w, http.StatusConflict, "position_side_mismatch", "requested position side does not match the live position")
		return
	}
	notional, err := positionOpenNotional(position)
	if err != nil {
		writeError(w, http.StatusConflict, "invalid_position_open", err.Error())
		return
	}
	openNotional := notional * ratio
	markPrice, err := positionOpenPrice(position)
	if err != nil {
		writeError(w, http.StatusConflict, "invalid_position_open", err.Error())
		return
	}
	key := positionOpenKey(s.TenantID, req, apiID, action)
	if !positionOpenJobs.start(key) {
		writeError(w, http.StatusConflict, "position_open_running", errPositionOpenAlreadyRunning.Error())
		return
	}

	now := s.now().UTC()
	signal := tvbottrading.Signal{
		Action:         action,
		TenantID:       s.TenantID,
		APIID:          apiID,
		TargetExchange: req.Exchange,
		TradeEnv:       cfg.Trading.Env,
		Coinpair:       req.InstID,
		Ticker:         req.InstID,
		Price:          tvbottrading.NewFlexibleFloat(markPrice),
		SentAt:         now.Format(time.RFC3339Nano),
		Time:           now.Format(time.RFC3339Nano),
		Condition:      "manual_position_open",
		Text:           "持仓页比例开仓",
		OrderIntent:    "manual_position_open",
		PositionEffect: tvbottrading.PositionEffectOpen,
		PositionSide:   positionSide,
		Leverage:       cfg.Trading.Leverage,
		Amount:         tvbottrading.NewFlexibleFloat(openNotional),
	}
	signal.Normalize()
	dedupeKey := tvbotstorage.DedupeKey(signal)
	record, duplicate, err := s.Orders.RecordAccepted(signal, dedupeKey, now)
	if err != nil {
		positionOpenJobs.done(key)
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if duplicate {
		positionOpenJobs.done(key)
		writeJSON(w, http.StatusAccepted, positionOpenResponse{
			OK: true, Status: "duplicate", SignalID: record.SignalID, Exchange: req.Exchange,
			APIID: apiID, InstID: position.InstID, PositionSide: positionSide, Ratio: ratio,
			CurrentNotional: tvbottrading.NormalizeFloat(notional), OpenNotional: tvbottrading.NormalizeFloat(openNotional),
			Message: "manual position open was already recorded",
		})
		return
	}

	execCfg := cfg
	execCfg.Trading.OrderType = string(tvbottrading.OrderTypeMarket)
	go func() {
		defer positionOpenJobs.done(key)
		s.execute(record.SignalID, signal, execCfg)
	}()
	writeJSON(w, http.StatusAccepted, positionOpenResponse{
		OK: true, Status: "accepted", SignalID: record.SignalID, Exchange: req.Exchange,
		APIID: apiID, InstID: position.InstID, PositionSide: positionSide, Ratio: ratio,
		CurrentNotional: tvbottrading.NormalizeFloat(notional), OpenNotional: tvbottrading.NormalizeFloat(openNotional),
		Message: "manual market position open accepted",
	})
}

func (s *Server) currentPositionForOpen(ctx context.Context, cfg tvbotconfig.Config, req positionOpenRequest) (tvbotokx.Position, string, error) {
	switch req.Exchange {
	case tvbottrading.ExchangeOKX:
		client, apiID, err := s.okxClientForCredentials(cfg, req.APIID)
		if err != nil {
			return tvbotokx.Position{}, "", err
		}
		positions, _, err := client.Positions(ctx, "SWAP")
		if err != nil {
			return tvbotokx.Position{}, "", err
		}
		for _, position := range positions {
			if positionMatchesOpenRequest(position, req) && isOpenPosition(position.Pos) {
				return position, apiID, nil
			}
		}
	case tvbottrading.ExchangeBinance:
		client, apiID, err := s.binanceClientForCredentials(cfg, req.APIID)
		if err != nil {
			return tvbotokx.Position{}, "", err
		}
		positions, err := client.Positions(ctx, "")
		if err != nil {
			return tvbotokx.Position{}, "", err
		}
		for _, raw := range positions {
			position := binancePositionToOKX(raw)
			if positionMatchesOpenRequest(position, req) && isOpenPosition(position.Pos) {
				return position, apiID, nil
			}
		}
	case tvbottrading.ExchangeGate:
		client, apiID, err := s.gateClientForCredentials(cfg, req.APIID)
		if err != nil {
			return tvbotokx.Position{}, "", err
		}
		positions, err := client.Positions(ctx, "")
		if err != nil {
			return tvbotokx.Position{}, "", err
		}
		for _, raw := range positions {
			position := gatePositionToOKX(raw)
			if positionMatchesOpenRequest(position, req) && isOpenPosition(position.Pos) {
				return position, apiID, nil
			}
		}
	}
	return tvbotokx.Position{}, "", errPositionNotOpen
}

func positionMatchesOpenRequest(position tvbotokx.Position, req positionOpenRequest) bool {
	if !strings.EqualFold(strings.TrimSpace(position.InstID), strings.TrimSpace(req.InstID)) {
		return false
	}
	want := normalizePosSide(req.PosSide)
	actual := normalizePosSide(position.PosSide)
	if want == "" {
		return true
	}
	if want == "net" {
		return actual == "" || actual == "net"
	}
	if actual == want {
		return true
	}
	if actual != "net" {
		return false
	}
	value, err := strconv.ParseFloat(strings.TrimSpace(position.Pos), 64)
	return err == nil && (value > 0 && want == "long" || value < 0 && want == "short")
}

func positionOpenAction(position tvbotokx.Position) (tvbottrading.Side, string, error) {
	side := normalizePosSide(position.PosSide)
	if side != "long" && side != "short" {
		value, err := strconv.ParseFloat(strings.TrimSpace(position.Pos), 64)
		if err != nil || value == 0 {
			return "", "", errors.New("live position direction is unavailable")
		}
		if value > 0 {
			side = "long"
		} else {
			side = "short"
		}
	}
	if side == "long" {
		return tvbottrading.ActionLong, side, nil
	}
	return tvbottrading.ActionShort, side, nil
}

func positionOpenNotional(position tvbotokx.Position) (float64, error) {
	if value, err := strconv.ParseFloat(strings.TrimSpace(position.NotionalUsd), 64); err == nil && value != 0 && !math.IsNaN(value) && !math.IsInf(value, 0) {
		return math.Abs(value), nil
	}
	size, sizeErr := strconv.ParseFloat(strings.TrimSpace(position.Pos), 64)
	price, priceErr := positionOpenPrice(position)
	if sizeErr != nil || math.Abs(size) <= 0 || priceErr != nil {
		return 0, errors.New("live position notional is unavailable")
	}
	return math.Abs(size) * price, nil
}

func positionOpenPrice(position tvbotokx.Position) (float64, error) {
	for _, raw := range []string{position.MarkPx, position.AvgPx} {
		value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		if err == nil && value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0) {
			return value, nil
		}
	}
	return 0, errors.New("live position price is unavailable")
}
