package okx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotconfig"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

type Trader struct {
	Credentials        Credentials
	CredentialProvider CredentialProvider
	HTTPClient         *http.Client
	Logger             *slog.Logger
}

type USDTBalance struct {
	Ccy              string `json:"ccy"`
	TotalEquity      string `json:"total_eq,omitempty"`
	Equity           string `json:"eq,omitempty"`
	AvailableEquity  string `json:"avail_eq,omitempty"`
	AvailableBalance string `json:"avail_bal,omitempty"`
	CashBalance      string `json:"cash_bal,omitempty"`
	FrozenBalance    string `json:"frozen_bal,omitempty"`
	UpdateTime       string `json:"u_time,omitempty"`
}

func (t Trader) ExecuteSignal(ctx context.Context, signal trading.Signal, cfg trading.RuntimeConfig) (trading.OrderResult, error) {
	if !cfg.LiveTradingAllowedByEnvironment() {
		return trading.OrderResult{}, fmt.Errorf("live trading requires config env=live, OKX_ENV=live and ALLOW_LIVE_TRADING=true")
	}
	client, apiID, err := t.client(cfg, signal.APIID)
	if err != nil {
		return trading.OrderResult{}, err
	}
	sym, err := t.resolveSymbol(ctx, client, signal, cfg)
	if err != nil {
		return trading.OrderResult{}, err
	}
	switchState, err := t.prepareDirectionSwitch(ctx, client, cfg, signal.Action, sym.InstID)
	if err != nil {
		return trading.OrderResult{}, err
	}
	orderSettings := cfg.OrderSettings().Normalize()
	if signal.Risk.Type == "" {
		orderSettings.ApplyToSignal(&signal)
	}
	if orderSettings.OrderType == trading.OrderTypeLayered && signal.PositionEffect != trading.PositionEffectClose {
		return t.executeLayeredSignal(ctx, signal, cfg, orderSettings)
	}
	if orderSettings.OrderType == trading.OrderTypeLayered {
		orderSettings.OrderType = trading.OrderTypeMarket
	}
	orderType := string(orderSettings.OrderType)
	sizingPx := signal.Price.Value
	orderPx := ""
	if orderSettings.OrderType == trading.OrderTypeLimit {
		sizingPx, orderPx, err = refreshedOKXLimitOrderPrice(ctx, client, signal.Action, orderSettings, sym)
		if err != nil {
			return trading.OrderResult{}, err
		}
	}
	sz, err := trading.SizeFromUSDTNotional(signal.Amount.Value, sizingPx, sym.CtVal, sym.LotSz, sym.MinSz)
	if err != nil {
		return trading.OrderResult{}, err
	}
	posSide := ""
	if cfg.PositionMode() == config.PositionLongShort {
		posSide = string(signal.Action)
	}
	usedLeverage, err := t.ensureLeverage(ctx, client, SetLeverageRequest{
		InstID:  sym.InstID,
		Lever:   strconv.Itoa(signal.Leverage),
		MgnMode: cfg.MarginMode(),
		PosSide: posSide,
	}, switchState, signal.Action)
	if err != nil {
		return trading.OrderResult{}, err
	}
	signal.Leverage = usedLeverage
	clOrdID := clientOrderID(signal)
	req := PlaceOrderRequest{
		InstID:         sym.InstID,
		TDMode:         cfg.MarginMode(),
		ClOrdID:        clOrdID,
		Side:           okxSide(signal.Action),
		PosSide:        posSide,
		OrdType:        orderType,
		Px:             orderPx,
		Sz:             sz,
		AttachAlgoOrds: attachAlgoOrders(signal, clOrdID),
	}
	ack, env, submittedReq, err := t.placeOrderWithFallback(ctx, client, req, signal, clOrdID, sizingPx, sym)
	result := trading.OrderResult{
		SignalID:       "",
		APIID:          apiID,
		TargetExchange: trading.ExchangeOKX,
		InstID:         submittedReq.InstID,
		ClOrdID:        clOrdID,
		OrdType:        submittedReq.OrdType,
		Px:             submittedReq.Px,
		OrdID:          ack.OrdID,
		OKXCode:        env.Code,
		OKXMsg:         env.Msg,
		Leverage:       usedLeverage,
	}
	if err != nil {
		return result, err
	}
	if t.Logger != nil {
		t.Logger.Info("okx order submitted", "api_id", apiID, "inst_id", submittedReq.InstID, "action", signal.Action, "cl_ord_id", clOrdID, "okx_code", env.Code)
	}
	return result, nil
}

func (t Trader) executeLayeredSignal(ctx context.Context, signal trading.Signal, cfg trading.RuntimeConfig, settings trading.OrderSettings) (trading.OrderResult, error) {
	client, apiID, err := t.client(cfg, signal.APIID)
	if err != nil {
		return trading.OrderResult{}, err
	}
	sym, err := t.resolveSymbol(ctx, client, signal, cfg)
	if err != nil {
		return trading.OrderResult{}, err
	}
	switchState, err := t.prepareDirectionSwitch(ctx, client, cfg, signal.Action, sym.InstID)
	if err != nil {
		return trading.OrderResult{}, err
	}
	ticker, _, err := client.MarketTicker(ctx, sym.InstID)
	if err != nil {
		return trading.OrderResult{}, fmt.Errorf("refresh OKX layered price for %s: %w", sym.InstID, err)
	}
	marketPrice, err := okxTickerMidPrice(ticker)
	if err != nil {
		return trading.OrderResult{}, err
	}
	legs, err := settings.Layered.BuildPlan(signal.Action, signal.Amount.Value, signal.Price.Value, marketPrice)
	if err != nil {
		return trading.OrderResult{}, err
	}
	posSide := ""
	if cfg.PositionMode() == config.PositionLongShort {
		posSide = string(signal.Action)
	}
	usedLeverage, err := t.ensureLeverage(ctx, client, SetLeverageRequest{
		InstID: sym.InstID, Lever: strconv.Itoa(signal.Leverage), MgnMode: cfg.MarginMode(), PosSide: posSide,
	}, switchState, signal.Action)
	if err != nil {
		return trading.OrderResult{}, err
	}
	signal.Leverage = usedLeverage
	result := trading.OrderResult{
		APIID:          apiID,
		TargetExchange: trading.ExchangeOKX,
		InstID:         sym.InstID,
		OrdType:        string(trading.OrderTypeLayered),
		Px:             trading.NormalizeFloat(marketPrice),
		Leverage:       usedLeverage,
		PositionEffect: signal.PositionEffect,
		PositionSide:   signal.PositionSide,
	}
	baseID := clientOrderID(signal)
	var firstErr error
	for index, leg := range legs {
		layer := trading.OrderLayerResult{
			Name:       leg.Name,
			AmountPct:  leg.AmountPct,
			AmountUSDT: leg.AmountUSDT,
			OffsetPct:  leg.OffsetPct,
			OrderType:  string(leg.OrderType),
			Price:      trading.NormalizeFloat(leg.Price),
			Status:     "skipped",
		}
		if leg.AmountUSDT <= 0 {
			result.Layers = append(result.Layers, layer)
			continue
		}
		orderPx := ""
		sizingPrice := leg.Price
		if leg.OrderType == trading.OrderTypeLimit {
			orderPx = formatOKXPrice(leg.Price, sym.TickSz, signal.Action == trading.ActionShort)
			if orderPx == "" || orderPx == "0" {
				err := fmt.Errorf("invalid OKX layered limit price for %s", leg.Name)
				layer.Status = "failed"
				layer.Error = err.Error()
				if firstErr == nil {
					firstErr = err
				}
				result.Layers = append(result.Layers, layer)
				continue
			}
			layer.Price = orderPx
			parsed, parseErr := strconv.ParseFloat(orderPx, 64)
			if parseErr != nil || parsed <= 0 {
				err := fmt.Errorf("invalid OKX rounded layered price %q for %s", orderPx, leg.Name)
				layer.Status = "failed"
				layer.Error = err.Error()
				if firstErr == nil {
					firstErr = err
				}
				result.Layers = append(result.Layers, layer)
				continue
			}
			sizingPrice = parsed
		}
		sz, sizeErr := trading.SizeFromUSDTNotional(leg.AmountUSDT, sizingPrice, sym.CtVal, sym.LotSz, sym.MinSz)
		if sizeErr != nil {
			layer.Status = "failed"
			layer.Error = sizeErr.Error()
			if firstErr == nil {
				firstErr = sizeErr
			}
			result.Layers = append(result.Layers, layer)
			continue
		}
		clOrdID := okxLayeredClientOrderID(baseID, index)
		req := PlaceOrderRequest{
			InstID: sym.InstID, TDMode: cfg.MarginMode(), ClOrdID: clOrdID,
			Side: okxSide(signal.Action), PosSide: posSide, OrdType: string(leg.OrderType), Px: orderPx,
			Sz: sz, AttachAlgoOrds: attachAlgoOrders(signal, clOrdID),
		}
		ack, env, submittedReq, placeErr := t.placeOrderWithFallback(ctx, client, req, signal, clOrdID, leg.Price, sym)
		layer.ClOrdID = submittedReq.ClOrdID
		layer.OrdID = ack.OrdID
		layer.RiskOrders = okxRiskOrderResults(submittedReq.AttachAlgoOrds)
		result.RiskOrders = append(result.RiskOrders, layer.RiskOrders...)
		if ack.OrdID != "" || ack.ClOrdID != "" {
			layer.Status = "submitted"
			result.ClOrdID = joinOKXOrderIDs(result.ClOrdID, submittedReq.ClOrdID)
			result.OrdID = joinOKXOrderIDs(result.OrdID, ack.OrdID)
		}
		if placeErr != nil {
			if env.Msg != "" {
				layer.Error = env.Msg
			} else {
				layer.Error = placeErr.Error()
			}
			if firstErr == nil {
				firstErr = placeErr
			}
		}
		if layer.Status == "skipped" {
			layer.Status = "failed"
		}
		result.Layers = append(result.Layers, layer)
	}
	if !result.HasSubmittedOrders() {
		if firstErr == nil {
			firstErr = errors.New("all OKX layered order legs failed")
		}
		return result, firstErr
	}
	if firstErr != nil {
		result.Partial = true
		return result, fmt.Errorf("OKX layered order partially submitted: %w", firstErr)
	}
	return result, nil
}

func okxLayeredClientOrderID(base string, index int) string {
	suffix := fmt.Sprintf("L%02d", index+1)
	return attachAlgoClOrdID(base, suffix)
}

func okxRiskOrderResults(attaches []map[string]string) []trading.RiskOrderResult {
	result := make([]trading.RiskOrderResult, 0, len(attaches))
	for _, attach := range attaches {
		orderType := strings.TrimSpace(attach["ordType"])
		if orderType == "" {
			orderType = "TP_SL"
		}
		result = append(result, trading.RiskOrderResult{
			Exchange:      trading.ExchangeOKX,
			ClientAlgoID:  strings.TrimSpace(attach["attachAlgoClOrdId"]),
			OrderType:     orderType,
			TriggerPrice:  strings.TrimSpace(attach["tpTriggerPx"]),
			ActivatePrice: strings.TrimSpace(attach["slTriggerPx"]),
			CallbackRate:  strings.TrimSpace(attach["callbackRatio"]),
		})
	}
	return result
}

func joinOKXOrderIDs(left, right string) string {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	if left == "" {
		return right
	}
	if right == "" {
		return left
	}
	return left + " / " + right
}

func (t Trader) placeOrderWithFallback(ctx context.Context, client Client, req PlaceOrderRequest, signal trading.Signal, clOrdID string, referencePx float64, sym trading.SymbolInfo) (OrderAck, Envelope, PlaceOrderRequest, error) {
	ack, env, err := t.placeOrderWithAlgoFallback(ctx, client, req, signal, clOrdID, referencePx, sym.TickSz)
	if err == nil {
		return ack, env, req, nil
	}
	fallbackReq, ok := maxPositionFallbackRequest(req, err, sym.LotSz, sym.MinSz)
	if ok {
		if t.Logger != nil {
			t.Logger.Warn("okx order exceeds max position amount, retrying with remaining size", "inst_id", req.InstID, "cl_ord_id", req.ClOrdID, "requested_sz", req.Sz, "retry_sz", fallbackReq.Sz, "error", err)
		}
		fallbackAck, fallbackEnv, fallbackErr := t.placeOrderWithAlgoFallback(ctx, client, fallbackReq, signal, clOrdID, referencePx, sym.TickSz)
		if fallbackErr != nil {
			return fallbackAck, fallbackEnv, fallbackReq, fmt.Errorf("okx max position size retry from %s to %s after rejection: %v; retry failed: %w", req.Sz, fallbackReq.Sz, err, fallbackErr)
		}
		return fallbackAck, fallbackEnv, fallbackReq, nil
	}
	if refreshedReq, refreshedSym, ok := refreshedInstrumentFallbackRequest(ctx, client, req, signal, err); ok {
		if t.Logger != nil {
			t.Logger.Warn("okx instrument rejected, refreshing configured instrument metadata before retry", "inst_id", req.InstID, "retry_inst_id", refreshedReq.InstID, "cl_ord_id", req.ClOrdID, "error", err)
		}
		fallbackAck, fallbackEnv, fallbackErr := t.placeOrderWithAlgoFallback(ctx, client, refreshedReq, signal, clOrdID, referencePx, refreshedSym.TickSz)
		if fallbackErr != nil {
			return fallbackAck, fallbackEnv, refreshedReq, fmt.Errorf("okx instrument %s rejected: %v; refreshed instrument retry as %s failed: %w", req.InstID, err, refreshedReq.InstID, fallbackErr)
		}
		return fallbackAck, fallbackEnv, refreshedReq, nil
	}
	if fallbackReq, fallbackPx, ok := marketPriceProtectionFallbackRequest(ctx, client, req, signal, sym, err); ok {
		if t.Logger != nil {
			t.Logger.Warn("okx market order hit price protection, retrying as a protected limit order", "inst_id", req.InstID, "cl_ord_id", req.ClOrdID, "retry_px", fallbackReq.Px, "retry_sz", fallbackReq.Sz, "error", err)
		}
		fallbackAck, fallbackEnv, fallbackErr := t.placeOrderWithAlgoFallback(ctx, client, fallbackReq, signal, clOrdID, fallbackPx, sym.TickSz)
		if fallbackErr != nil {
			return fallbackAck, fallbackEnv, fallbackReq, fmt.Errorf("okx market order rejected by price protection: %v; protected limit retry at %s failed: %w", err, fallbackReq.Px, fallbackErr)
		}
		return fallbackAck, fallbackEnv, fallbackReq, nil
	}
	return ack, env, req, err
}

func (t Trader) placeOrderWithAlgoFallback(ctx context.Context, client Client, req PlaceOrderRequest, signal trading.Signal, clOrdID string, referencePx, tickSz float64) (OrderAck, Envelope, error) {
	ack, env, err := client.PlaceOrder(ctx, req)
	if err == nil || !isDynamicAlgoUnsupportedError(err) {
		return ack, env, err
	}
	fallbackReq, label, ok := dynamicAlgoFallbackRequest(req, signal, clOrdID, referencePx, tickSz)
	if !ok {
		return ack, env, err
	}
	if t.Logger != nil {
		t.Logger.Warn("okx attached algo rejected, retrying with compatible parameters", "inst_id", req.InstID, "cl_ord_id", req.ClOrdID, "fallback", label, "reference_px", trading.NormalizeFloat(referencePx), "error", err)
	}
	fallbackAck, fallbackEnv, fallbackErr := client.PlaceOrder(ctx, fallbackReq)
	if fallbackErr != nil {
		return fallbackAck, fallbackEnv, fmt.Errorf("okx %s rejected: %v; fallback failed: %w", label, err, fallbackErr)
	}
	return fallbackAck, fallbackEnv, nil
}

func (t Trader) ensureLeverage(ctx context.Context, client Client, req SetLeverageRequest, state directionSwitchState, action trading.Side) (int, error) {
	desired, err := parsePositiveLeverage(req.Lever)
	if err != nil {
		return 0, err
	}
	if okxRemoteLeverageMatches(state, req.InstID, req.PosSide, action, desired) {
		if t.Logger != nil {
			t.Logger.Info("okx leverage already matches configured value", "inst_id", req.InstID, "pos_side", req.PosSide, "leverage", desired)
		}
		return desired, nil
	}
	return t.setLeverageWithFallback(ctx, client, req)
}

func (t Trader) setLeverageWithFallback(ctx context.Context, client Client, req SetLeverageRequest) (int, error) {
	desired, err := parsePositiveLeverage(req.Lever)
	if err != nil {
		return 0, err
	}
	var lastErr error
	for lever := desired; lever >= 1; lever-- {
		req.Lever = strconv.Itoa(lever)
		err := client.SetLeverage(ctx, req)
		if err == nil {
			return lever, nil
		}
		lastErr = err
		if !isMaxLeverageError(err) {
			return 0, err
		}
		if t.Logger != nil && lever > 1 {
			t.Logger.Warn("okx leverage exceeds maximum, trying lower leverage", "inst_id", req.InstID, "leverage", lever, "next_leverage", lever-1)
		}
	}
	return 0, fmt.Errorf("set leverage failed from %dx down to 1x: %w", desired, lastErr)
}

func parsePositiveLeverage(raw string) (int, error) {
	desired, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || desired <= 0 {
		if err == nil {
			err = fmt.Errorf("must be positive")
		}
		return 0, fmt.Errorf("invalid leverage %q: %w", raw, err)
	}
	return desired, nil
}

func okxRemoteLeverageMatches(state directionSwitchState, instID, posSide string, action trading.Side, desired int) bool {
	found := false
	mismatch := false
	for _, position := range state.positions {
		if !strings.EqualFold(position.InstID, instID) || !okxPositionMatchesLeverageScope(position, posSide, action) {
			continue
		}
		if strings.TrimSpace(position.Lever) == "" {
			continue
		}
		found = true
		if !leverageValueMatches(position.Lever, desired) {
			mismatch = true
		}
	}
	for _, order := range state.pendingOrders {
		if !strings.EqualFold(order.InstID, instID) || !okxPendingOrderMatchesLeverageScope(order, posSide, action) {
			continue
		}
		if strings.TrimSpace(order.Lever) == "" {
			continue
		}
		found = true
		if !leverageValueMatches(order.Lever, desired) {
			mismatch = true
		}
	}
	return found && !mismatch
}

func okxPositionMatchesLeverageScope(position Position, posSide string, action trading.Side) bool {
	wantSide := normalizeOKXPosSide(posSide)
	if wantSide != "" {
		return normalizeOKXPosSide(position.PosSide) == wantSide
	}
	if direction, ok := okxPositionDirection(position); ok {
		return direction == action
	}
	return normalizeOKXPosSide(position.PosSide) == ""
}

func okxPendingOrderMatchesLeverageScope(order PendingOrder, posSide string, action trading.Side) bool {
	if rawJSONBool(order.ReduceOnly) {
		return false
	}
	wantSide := normalizeOKXPosSide(posSide)
	if wantSide != "" && normalizeOKXPosSide(order.PosSide) != wantSide {
		return false
	}
	if direction, ok := okxPendingOrderDirection(order); ok {
		return direction == action
	}
	return wantSide == "" || normalizeOKXPosSide(order.PosSide) == wantSide
}

func leverageValueMatches(raw string, desired int) bool {
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	return err == nil && math.Abs(value-float64(desired)) < 1e-9
}

func isMaxLeverageError(err error) bool {
	var apiErr APIError
	if errors.As(err, &apiErr) && apiErr.HasCode("59102") {
		return true
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "59102") ||
		(strings.Contains(text, "leverage") && strings.Contains(text, "maximum"))
}

func (t Trader) resolveSymbol(ctx context.Context, client Client, signal trading.Signal, cfg trading.RuntimeConfig) (trading.SymbolInfo, error) {
	if sym, ok := cfg.SymbolMeta(signal.Coinpair); ok {
		return sym, nil
	}
	instID, coinpair, err := DeriveSwapInstrumentID(signal.Coinpair, signal.Ticker)
	if err != nil {
		return trading.SymbolInfo{}, err
	}
	inst, err := client.SwapInstrument(ctx, instID)
	if err != nil {
		return trading.SymbolInfo{}, err
	}
	if !strings.EqualFold(strings.TrimSpace(signal.PositionEffect), trading.PositionEffectClose) {
		if err := inst.ValidateUnderlyingFamily(); err != nil {
			return trading.SymbolInfo{}, err
		}
	}
	meta, err := inst.SymbolInfo()
	if err != nil {
		return trading.SymbolInfo{}, err
	}
	return trading.SymbolInfo{
		Coinpair: coinpair,
		InstID:   meta.InstID,
		CtVal:    meta.CtVal,
		TickSz:   meta.TickSz,
		LotSz:    meta.LotSz,
		MinSz:    meta.MinSz,
	}, nil
}

func (t Trader) Check(ctx context.Context, cfg trading.RuntimeConfig) (map[string]any, error) {
	return t.CheckAccount(ctx, cfg, "")
}

func (t Trader) CheckAccount(ctx context.Context, cfg trading.RuntimeConfig, apiID string) (map[string]any, error) {
	client, resolvedID, err := t.client(cfg, apiID)
	if err != nil {
		return nil, err
	}
	return t.checkClient(ctx, cfg, client, resolvedID)
}

func (t Trader) CheckCredentials(ctx context.Context, cfg trading.RuntimeConfig, apiID string, creds Credentials) (map[string]any, error) {
	creds = trimCredentials(creds)
	if err := creds.Validate(); err != nil {
		return nil, err
	}
	resolvedID := strings.TrimSpace(apiID)
	if resolvedID == "" {
		resolvedID = "input"
	}
	client := Client{
		BaseURL:     cfg.OKXBaseURL(),
		Credentials: creds,
		Demo:        cfg.DemoTradingHeaderEnabled(),
		HTTPClient:  t.HTTPClient,
	}
	return t.checkClient(ctx, cfg, client, resolvedID)
}

func (t Trader) checkClient(ctx context.Context, cfg trading.RuntimeConfig, client Client, apiID string) (map[string]any, error) {
	balanceData, balance, err := client.AccountBalanceSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	instruments, err := client.Instruments(ctx)
	if err != nil {
		return nil, err
	}
	usdtBalance, usdtBalanceFound := usdtBalanceFromAccount(balanceData)
	return map[string]any{
		"ok":                 true,
		"api_id":             apiID,
		"demo":               cfg.DemoTradingHeaderEnabled(),
		"base_url":           cfg.OKXBaseURL(),
		"balance_code":       balance.Code,
		"balance_msg":        balance.Msg,
		"usdt_balance":       usdtBalance,
		"usdt_balance_found": usdtBalanceFound,
		"instruments":        string(instruments.Data),
	}, nil
}

func usdtBalanceFromAccount(account AccountBalanceData) (USDTBalance, bool) {
	for _, detail := range account.Details {
		if strings.EqualFold(detail.Ccy, "USDT") {
			return USDTBalance{
				Ccy:              "USDT",
				TotalEquity:      account.TotalEq,
				Equity:           detail.Eq,
				AvailableEquity:  detail.AvailEq,
				AvailableBalance: detail.AvailBal,
				CashBalance:      detail.CashBal,
				FrozenBalance:    detail.FrozenBal,
				UpdateTime:       detail.UTime,
			}, true
		}
	}
	return USDTBalance{}, false
}

func (t Trader) client(cfg trading.RuntimeConfig, apiID string) (Client, string, error) {
	creds, resolvedID, err := t.credentials(apiID)
	if err != nil {
		return Client{}, resolvedID, err
	}
	return Client{
		BaseURL:     cfg.OKXBaseURL(),
		Credentials: creds,
		Demo:        cfg.DemoTradingHeaderEnabled(),
		HTTPClient:  t.HTTPClient,
	}, resolvedID, nil
}

func (t Trader) credentials(apiID string) (Credentials, string, error) {
	if t.CredentialProvider != nil {
		return t.CredentialProvider.OKXCredentials(apiID)
	}
	creds := trimCredentials(t.Credentials)
	if err := creds.Validate(); err != nil {
		return Credentials{}, strings.TrimSpace(apiID), err
	}
	return creds, strings.TrimSpace(apiID), nil
}

func okxSide(action trading.Side) string {
	if action == trading.ActionShort {
		return "sell"
	}
	return "buy"
}

func attachAlgoOrders(signal trading.Signal, clOrdID string) []map[string]string {
	return AttachAlgoOrders(signal.Action, signal.Risk, clOrdID, signal.Leverage)
}

func AttachAlgoOrders(action trading.Side, risk trading.Risk, clOrdID string, leverage int) []map[string]string {
	risk.Normalize()
	switch risk.Type {
	case trading.RiskTPSL:
		if risk.TPPct == nil || risk.SLPct == nil || leverage <= 0 {
			return nil
		}
		return []map[string]string{{
			"attachAlgoClOrdId": attachAlgoClOrdID(clOrdID, "A"),
			"tpTriggerRatio":    signedTPRatio(action, risk.TPPct.Value, leverage),
			"tpOrdPx":           "-1",
			"tpTriggerPxType":   "last",
			"slTriggerRatio":    signedSLRatio(action, risk.SLPct.Value, leverage),
			"slOrdPx":           "-1",
			"slTriggerPxType":   "last",
		}}
	case trading.RiskTrailing:
		if risk.TrailingPct == nil {
			return nil
		}
		return []map[string]string{{
			"attachAlgoClOrdId": attachAlgoClOrdID(clOrdID, "T"),
			"ordType":           "move_order_stop",
			"callbackRatio":     pctRatio(risk.TrailingPct.Value),
		}}
	default:
		return nil
	}
}

func dynamicAlgoFallbackRequest(req PlaceOrderRequest, signal trading.Signal, clOrdID string, referencePx, tickSz float64) (PlaceOrderRequest, string, bool) {
	risk := signal.Risk
	risk.Normalize()
	switch risk.Type {
	case trading.RiskTPSL:
		if referencePx <= 0 || !placeOrderRequestUsesTPSLRatio(req) {
			return PlaceOrderRequest{}, "", false
		}
		attach := fixedTPSLAttachAlgoOrders(signal.Action, risk, clOrdID, referencePx, tickSz, signal.Leverage)
		if len(attach) == 0 {
			return PlaceOrderRequest{}, "", false
		}
		fallbackReq := req
		fallbackReq.AttachAlgoOrds = attach
		return fallbackReq, "attached TP/SL ratio", true
	case trading.RiskTrailing:
		if referencePx <= 0 || !placeOrderRequestUsesTrailingCallbackRatio(req) {
			return PlaceOrderRequest{}, "", false
		}
		attach := trailingCallbackSpreadAttachAlgoOrders(risk, clOrdID, referencePx)
		if len(attach) == 0 {
			return PlaceOrderRequest{}, "", false
		}
		fallbackReq := req
		fallbackReq.AttachAlgoOrds = attach
		return fallbackReq, "trailing callback ratio", true
	default:
		return PlaceOrderRequest{}, "", false
	}
}

func placeOrderRequestUsesTrailingCallbackRatio(req PlaceOrderRequest) bool {
	for _, attach := range req.AttachAlgoOrds {
		if strings.EqualFold(strings.TrimSpace(attach["ordType"]), "move_order_stop") &&
			strings.TrimSpace(attach["callbackRatio"]) != "" {
			return true
		}
	}
	return false
}

func placeOrderRequestUsesTPSLRatio(req PlaceOrderRequest) bool {
	for _, attach := range req.AttachAlgoOrds {
		if strings.TrimSpace(attach["tpTriggerRatio"]) != "" || strings.TrimSpace(attach["slTriggerRatio"]) != "" {
			return true
		}
	}
	return false
}

func isDynamicAlgoUnsupportedError(err error) bool {
	if err == nil {
		return false
	}
	var apiErr APIError
	if errors.As(err, &apiErr) && apiErr.HasCode("54079") {
		return true
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "54079") ||
		(strings.Contains(text, "dynamic change") && strings.Contains(text, "futures mode"))
}

func refreshedInstrumentFallbackRequest(ctx context.Context, client Client, req PlaceOrderRequest, signal trading.Signal, orderErr error) (PlaceOrderRequest, trading.SymbolInfo, bool) {
	if !isOKXInstrumentNotFoundError(orderErr) {
		return PlaceOrderRequest{}, trading.SymbolInfo{}, false
	}
	instID, coinpair, err := DeriveSwapInstrumentID(signal.Coinpair, signal.Ticker)
	if err != nil || strings.EqualFold(instID, req.InstID) {
		return PlaceOrderRequest{}, trading.SymbolInfo{}, false
	}
	inst, err := client.SwapInstrument(ctx, instID)
	if err != nil {
		return PlaceOrderRequest{}, trading.SymbolInfo{}, false
	}
	if !strings.EqualFold(strings.TrimSpace(signal.PositionEffect), trading.PositionEffectClose) && inst.ValidateUnderlyingFamily() != nil {
		return PlaceOrderRequest{}, trading.SymbolInfo{}, false
	}
	meta, err := inst.SymbolInfo()
	if err != nil {
		return PlaceOrderRequest{}, trading.SymbolInfo{}, false
	}
	refreshedSym := trading.SymbolInfo{
		Coinpair: coinpair,
		InstID:   meta.InstID,
		CtVal:    meta.CtVal,
		TickSz:   meta.TickSz,
		LotSz:    meta.LotSz,
		MinSz:    meta.MinSz,
	}
	sz, err := trading.SizeFromUSDTNotional(signal.Amount.Value, signal.Price.Value, refreshedSym.CtVal, refreshedSym.LotSz, refreshedSym.MinSz)
	if err != nil {
		return PlaceOrderRequest{}, trading.SymbolInfo{}, false
	}
	refreshedReq := req
	refreshedReq.InstID = refreshedSym.InstID
	refreshedReq.Sz = sz
	return refreshedReq, refreshedSym, true
}

func isOKXInstrumentNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	var apiErr APIError
	if errors.As(err, &apiErr) && apiErr.HasCode("51001") {
		return true
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "51001") && strings.Contains(text, "instrument") && strings.Contains(text, "exist")
}

func marketPriceProtectionFallbackRequest(ctx context.Context, client Client, req PlaceOrderRequest, signal trading.Signal, sym trading.SymbolInfo, orderErr error) (PlaceOrderRequest, float64, bool) {
	fallbackReq, ok := ProtectedMarketLimitFallbackRequest(ctx, client, req, sym.TickSz, orderErr)
	if !ok {
		return PlaceOrderRequest{}, 0, false
	}
	sizingPx, err := strconv.ParseFloat(fallbackReq.Px, 64)
	if err != nil || sizingPx <= 0 {
		return PlaceOrderRequest{}, 0, false
	}
	sz, err := trading.SizeFromUSDTNotional(signal.Amount.Value, sizingPx, sym.CtVal, sym.LotSz, sym.MinSz)
	if err != nil {
		return PlaceOrderRequest{}, 0, false
	}
	fallbackReq.Sz = sz
	return fallbackReq, sizingPx, true
}

// ProtectedMarketLimitFallbackRequest converts an OKX market order rejected by
// price protection into a limit order at a currently permitted price. It keeps
// the request size and reduce-only/position-side semantics unchanged, so it is
// safe for both entry and position-close callers.
func ProtectedMarketLimitFallbackRequest(ctx context.Context, client Client, req PlaceOrderRequest, tickSz float64, orderErr error) (PlaceOrderRequest, bool) {
	if !strings.EqualFold(strings.TrimSpace(req.OrdType), string(trading.OrderTypeMarket)) {
		return PlaceOrderRequest{}, false
	}
	return ProtectedPriceLimitRetryRequest(ctx, client, req, tickSz, orderErr)
}

// ProtectedPriceLimitRetryRequest rebuilds a rejected market or limit order as
// a limit order at a price permitted by the 51006 price-protection response.
// The size, reduce-only flag, and position side are preserved.
func ProtectedPriceLimitRetryRequest(ctx context.Context, client Client, req PlaceOrderRequest, tickSz float64, orderErr error) (PlaceOrderRequest, bool) {
	if !isOKXPriceProtectionError(orderErr) {
		return PlaceOrderRequest{}, false
	}
	limit, ok := okxPriceProtectionLimitFromError(orderErr)
	if !ok {
		return PlaceOrderRequest{}, false
	}
	price, roundUp, ok := protectedLimitPrice(ctx, client, req.Side, req.InstID, limit)
	if !ok {
		return PlaceOrderRequest{}, false
	}
	px := formatOKXPrice(price, tickSz, roundUp)
	if px == "" || px == "0" {
		return PlaceOrderRequest{}, false
	}
	fallbackReq := req
	fallbackReq.OrdType = string(trading.OrderTypeLimit)
	fallbackReq.Px = px
	return fallbackReq, true
}

func protectedLimitPrice(ctx context.Context, client Client, side, instID string, limit okxPriceProtectionLimit) (float64, bool, bool) {
	ticker, _, tickerErr := client.MarketTicker(ctx, instID)
	if strings.EqualFold(side, "buy") {
		if limit.MaxBuy <= 0 {
			return 0, false, false
		}
		if tickerErr == nil {
			if ask, err := strconv.ParseFloat(strings.TrimSpace(ticker.AskPx), 64); err == nil && ask > 0 && ask < limit.MaxBuy {
				return ask, false, true
			}
		}
		return limit.MaxBuy, false, true
	}
	if strings.EqualFold(side, "sell") {
		if limit.MinSell <= 0 {
			return 0, false, false
		}
		if tickerErr == nil {
			if bid, err := strconv.ParseFloat(strings.TrimSpace(ticker.BidPx), 64); err == nil && bid > limit.MinSell {
				return bid, true, true
			}
		}
		return limit.MinSell, true, true
	}
	return 0, false, false
}

func isOKXPriceProtectionError(err error) bool {
	if err == nil {
		return false
	}
	var apiErr APIError
	if errors.As(err, &apiErr) && apiErr.HasCode("51006") {
		return true
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "51006") && strings.Contains(text, "price limit")
}

type okxPriceProtectionLimit struct {
	MaxBuy  float64
	MinSell float64
}

func okxPriceProtectionLimitFromError(err error) (okxPriceProtectionLimit, bool) {
	if err == nil {
		return okxPriceProtectionLimit{}, false
	}
	var apiErr APIError
	_ = errors.As(err, &apiErr)
	for _, text := range okxErrorTexts(err, apiErr) {
		maxBuy, hasMaxBuy := firstRegexFloat(okxMaxBuyPriceRE, text)
		minSell, hasMinSell := firstRegexFloat(okxMinSellPriceRE, text)
		if hasMaxBuy || hasMinSell {
			return okxPriceProtectionLimit{MaxBuy: maxBuy, MinSell: minSell}, true
		}
	}
	return okxPriceProtectionLimit{}, false
}

var (
	okxMaxBuyPriceRE               = regexp.MustCompile(`(?i)max\s+buy\s+price:\s*([0-9]+(?:\.[0-9]+)?)`)
	okxMinSellPriceRE              = regexp.MustCompile(`(?i)min\s+sell\s+price:\s*([0-9]+(?:\.[0-9]+)?)`)
	okxMaxPositionLimitRE          = regexp.MustCompile(`(?i)more than\s+([0-9]+(?:\.[0-9]+)?)(?:\s*\(contracts?\)|\s+contracts?)`)
	okxMaxPositionCurrentOrderRE   = regexp.MustCompile(`(?i)current\s+(?:buy|sell)\s+order\s+size:\s*([0-9]+(?:\.[0-9]+)?)\s*contracts?`)
	okxMaxPositionQuantityRE       = regexp.MustCompile(`(?i)position\s+quantity:\s*([0-9]+(?:\.[0-9]+)?)\s*contracts?`)
	okxMaxPositionPendingOrdersRE  = regexp.MustCompile(`(?i)pending\s+(?:buy|sell)\s+orders:\s*([0-9]+(?:\.[0-9]+)?)\s*contracts?`)
	okxMaxPositionFallbackEpsilon  = 1e-12
	okxMaxPositionFallbackErrorMsg = "maximum position amount"
)

type okxMaxPositionLimit struct {
	Max          float64
	CurrentOrder float64
	Position     float64
	Pending      float64
}

func maxPositionFallbackRequest(req PlaceOrderRequest, err error, lotSz, minSz float64) (PlaceOrderRequest, bool) {
	limit, ok := okxMaxPositionLimitFromError(err)
	if !ok {
		return PlaceOrderRequest{}, false
	}
	currentSize, parseErr := strconv.ParseFloat(strings.TrimSpace(req.Sz), 64)
	if parseErr != nil || currentSize <= 0 {
		return PlaceOrderRequest{}, false
	}
	remaining := limit.Max - limit.Position - limit.Pending
	if remaining <= 0 {
		return PlaceOrderRequest{}, false
	}
	nextSize, sizeErr := trading.SizeFromContracts(remaining, lotSz, minSz)
	if sizeErr != nil {
		return PlaceOrderRequest{}, false
	}
	nextFloat, parseErr := strconv.ParseFloat(nextSize, 64)
	if parseErr != nil || nextFloat <= 0 || nextFloat+okxMaxPositionFallbackEpsilon >= currentSize {
		return PlaceOrderRequest{}, false
	}
	if limit.CurrentOrder > 0 && currentSize+okxMaxPositionFallbackEpsilon < limit.CurrentOrder {
		return PlaceOrderRequest{}, false
	}
	fallbackReq := req
	fallbackReq.Sz = nextSize
	return fallbackReq, true
}

func okxMaxPositionLimitFromError(err error) (okxMaxPositionLimit, bool) {
	if err == nil {
		return okxMaxPositionLimit{}, false
	}
	var apiErr APIError
	hasCode := errors.As(err, &apiErr) && apiErr.HasCode("51004")
	for _, text := range okxErrorTexts(err, apiErr) {
		normalized := strings.ToLower(text)
		if !hasCode && (!strings.Contains(normalized, "51004") || !strings.Contains(normalized, okxMaxPositionFallbackErrorMsg)) {
			continue
		}
		limit, ok := parseOKXMaxPositionLimit(text)
		if ok {
			return limit, true
		}
	}
	return okxMaxPositionLimit{}, false
}

func okxErrorTexts(err error, apiErr APIError) []string {
	texts := []string{}
	if len(apiErr.Data) > 0 {
		var details []struct {
			SMsg string `json:"sMsg"`
		}
		if jsonErr := json.Unmarshal(apiErr.Data, &details); jsonErr == nil {
			for _, detail := range details {
				if text := strings.TrimSpace(detail.SMsg); text != "" {
					texts = append(texts, text)
				}
			}
		}
	}
	if text := strings.TrimSpace(apiErr.Msg); text != "" {
		texts = append(texts, text)
	}
	if err != nil {
		texts = append(texts, err.Error())
	}
	return texts
}

func parseOKXMaxPositionLimit(text string) (okxMaxPositionLimit, bool) {
	max, ok := firstRegexFloat(okxMaxPositionLimitRE, text)
	if !ok || max <= 0 {
		return okxMaxPositionLimit{}, false
	}
	current, _ := firstRegexFloat(okxMaxPositionCurrentOrderRE, text)
	position, _ := firstRegexFloat(okxMaxPositionQuantityRE, text)
	pending, _ := firstRegexFloat(okxMaxPositionPendingOrdersRE, text)
	return okxMaxPositionLimit{
		Max:          max,
		CurrentOrder: current,
		Position:     position,
		Pending:      pending,
	}, true
}

func firstRegexFloat(re *regexp.Regexp, text string) (float64, bool) {
	matches := re.FindStringSubmatch(text)
	if len(matches) < 2 {
		return 0, false
	}
	value, err := strconv.ParseFloat(matches[1], 64)
	if err != nil {
		return 0, false
	}
	return value, true
}

func fixedTPSLAttachAlgoOrders(action trading.Side, risk trading.Risk, clOrdID string, referencePx, tickSz float64, leverage int) []map[string]string {
	risk.Normalize()
	if risk.Type != trading.RiskTPSL || risk.TPPct == nil || risk.SLPct == nil || referencePx <= 0 || leverage <= 0 {
		return nil
	}
	tpPx, slPx, err := trading.FixedTPSLTriggerPrices(action, referencePx, risk.TPPct.Value, risk.SLPct.Value, leverage)
	if err != nil {
		return nil
	}
	tpTrigger := formatOKXTriggerPrice(tpPx, tickSz, action == trading.ActionLong)
	slTrigger := formatOKXTriggerPrice(slPx, tickSz, action == trading.ActionShort)
	if tpTrigger == "" || slTrigger == "" || tpTrigger == "0" || slTrigger == "0" {
		return nil
	}
	return []map[string]string{{
		"attachAlgoClOrdId": attachAlgoClOrdID(clOrdID, "A"),
		"tpTriggerPx":       tpTrigger,
		"tpOrdPx":           "-1",
		"tpTriggerPxType":   "last",
		"slTriggerPx":       slTrigger,
		"slOrdPx":           "-1",
		"slTriggerPxType":   "last",
	}}
}

func fixedTPSLTriggerPrices(action trading.Side, tpPct, slPct, referencePx, tickSz float64, leverage int) (string, string) {
	tpPx, slPx, err := trading.FixedTPSLTriggerPrices(action, referencePx, tpPct, slPct, leverage)
	if err != nil {
		return "", ""
	}
	return formatOKXTriggerPrice(tpPx, tickSz, action == trading.ActionLong), formatOKXTriggerPrice(slPx, tickSz, action == trading.ActionShort)
}

func refreshedOKXLimitOrderPrice(ctx context.Context, client Client, action trading.Side, orderSettings trading.OrderSettings, sym trading.SymbolInfo) (float64, string, error) {
	ticker, _, err := client.MarketTicker(ctx, sym.InstID)
	if err != nil {
		return 0, "", fmt.Errorf("refresh OKX limit price for %s: %w", sym.InstID, err)
	}
	marketPx, err := okxTickerMidPrice(ticker)
	if err != nil {
		return 0, "", fmt.Errorf("refresh OKX limit price for %s: %w", sym.InstID, err)
	}
	limitPx := orderSettings.LimitPrice(action, marketPx)
	roundUp := false
	switch action {
	case trading.ActionLong:
		roundUp = false
	case trading.ActionShort:
		roundUp = true
	default:
		return 0, "", fmt.Errorf("unsupported OKX limit action %q", action)
	}
	orderPx := formatOKXPrice(limitPx, sym.TickSz, roundUp)
	if orderPx == "" || orderPx == "0" {
		return 0, "", fmt.Errorf("invalid OKX limit price for %s", sym.InstID)
	}
	sizingPx, err := strconv.ParseFloat(orderPx, 64)
	if err != nil || sizingPx <= 0 {
		return 0, "", fmt.Errorf("invalid OKX limit price %q for %s", orderPx, sym.InstID)
	}
	return sizingPx, orderPx, nil
}

func okxTickerMidPrice(ticker Ticker) (float64, error) {
	bid, bidErr := strconv.ParseFloat(strings.TrimSpace(ticker.BidPx), 64)
	ask, askErr := strconv.ParseFloat(strings.TrimSpace(ticker.AskPx), 64)
	if bidErr == nil && askErr == nil && bid > 0 && ask > 0 {
		return (bid + ask) / 2, nil
	}
	last, lastErr := strconv.ParseFloat(strings.TrimSpace(ticker.Last), 64)
	if lastErr != nil || last <= 0 {
		return 0, fmt.Errorf("invalid OKX ticker bid/ask for %s", ticker.InstID)
	}
	return last, nil
}

func formatOKXTriggerPrice(target, tickSz float64, roundUp bool) string {
	return formatOKXPrice(target, tickSz, roundUp)
}

func formatOKXPrice(target, tickSz float64, roundUp bool) string {
	if target <= 0 || math.IsNaN(target) || math.IsInf(target, 0) {
		return ""
	}
	if tickSz > 0 && !math.IsNaN(tickSz) && !math.IsInf(tickSz, 0) {
		units := target / tickSz
		if roundUp {
			target = math.Ceil(units-1e-12) * tickSz
		} else {
			target = math.Floor(units+1e-12) * tickSz
		}
	}
	if target <= 0 || math.IsNaN(target) || math.IsInf(target, 0) {
		return ""
	}
	return trading.NormalizeFloat(target)
}

func trailingCallbackSpreadAttachAlgoOrders(risk trading.Risk, clOrdID string, referencePx float64) []map[string]string {
	risk.Normalize()
	if risk.Type != trading.RiskTrailing || risk.TrailingPct == nil || referencePx <= 0 {
		return nil
	}
	spread := referencePx * risk.TrailingPct.Value / 100
	if spread <= 0 {
		return nil
	}
	callbackSpread := trading.NormalizeFloat(spread)
	if callbackSpread == "" || callbackSpread == "0" {
		return nil
	}
	return []map[string]string{{
		"attachAlgoClOrdId": attachAlgoClOrdID(clOrdID, "T"),
		"ordType":           "move_order_stop",
		"callbackSpread":    callbackSpread,
	}}
}

func attachAlgoClOrdID(clOrdID, suffix string) string {
	clOrdID = strings.TrimSpace(clOrdID)
	limit := 32 - len(suffix)
	if limit < 0 {
		limit = 0
	}
	if len(clOrdID) > limit {
		clOrdID = clOrdID[:limit]
	}
	return clOrdID + suffix
}

func signedTPRatio(action trading.Side, pct float64, leverage int) string {
	ratio := pct / float64(leverage) / 100
	if action == trading.ActionShort {
		ratio = -ratio
	}
	return okxRatio(ratio)
}

func signedSLRatio(action trading.Side, pct float64, leverage int) string {
	ratio := pct / float64(leverage) / 100
	if action != trading.ActionShort {
		ratio = -ratio
	}
	return okxRatio(ratio)
}

func pctRatio(pct float64) string {
	return okxRatio(pct / 100)
}

func okxRatio(ratio float64) string {
	if math.IsNaN(ratio) || math.IsInf(ratio, 0) {
		return "0"
	}
	const step = 0.0001
	sign := 1.0
	if ratio < 0 {
		sign = -1
	}
	absRatio := math.Abs(ratio)
	rounded := math.Round(absRatio/step) * step
	if rounded == 0 && absRatio > 0 {
		rounded = step
	}
	return trading.NormalizeFloat(sign * rounded)
}

func clientOrderID(signal trading.Signal) string {
	sum := sha256.Sum256([]byte(signal.CanonicalTokenPayload() + "|" + signal.SentAt + "|" + signal.Ticker))
	short := hex.EncodeToString(sum[:])[:12]
	return strings.ToUpper(fmt.Sprintf("TV%d%s", time.Now().UTC().UnixMilli(), short))
}

func DeriveSwapInstrumentID(coinpair, ticker string) (string, string, error) {
	raw := strings.TrimSpace(coinpair)
	if raw == "" {
		raw = strings.TrimSpace(ticker)
	}
	if raw == "" {
		return "", "", fmt.Errorf("coinpair or ticker is required")
	}
	raw = strings.ToUpper(raw)
	if i := strings.LastIndex(raw, ":"); i >= 0 {
		raw = raw[i+1:]
	}
	raw = strings.TrimSpace(raw)
	raw = strings.TrimSuffix(raw, ".P")
	raw = strings.TrimSuffix(raw, ".PERP")
	raw = strings.TrimSuffix(raw, "PERP")
	raw = strings.ReplaceAll(raw, "_", "-")
	raw = strings.ReplaceAll(raw, "/", "-")
	raw = strings.ReplaceAll(raw, " ", "")
	if strings.HasSuffix(raw, "-SWAP") {
		return raw, baseCoin(raw), nil
	}
	if strings.Contains(raw, "-") {
		parts := strings.Split(raw, "-")
		if len(parts) >= 2 {
			instID := parts[0] + "-" + parts[1] + "-SWAP"
			return instID, parts[0], nil
		}
	}
	if strings.HasSuffix(raw, "USDT") && len(raw) > len("USDT") {
		base := strings.TrimSuffix(raw, "USDT")
		return base + "-USDT-SWAP", base, nil
	}
	return raw + "-USDT-SWAP", raw, nil
}

func baseCoin(instID string) string {
	parts := strings.Split(instID, "-")
	if len(parts) == 0 {
		return instID
	}
	return parts[0]
}
