package binance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotconfig"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

const (
	maxBinanceLeverageFallback = 50
	maxBinanceOrderSplits      = 50
)

var binanceUSDMQuoteAssets = []string{"USDT", "USDC"}

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
	if !cfg.BinanceLiveTradingAllowedByEnvironment() {
		return trading.OrderResult{}, fmt.Errorf("live trading requires config env=live, BINANCE_ENV=live and ALLOW_LIVE_TRADING=true")
	}
	orderSettings := cfg.OrderSettings().Normalize()
	orderSettings.ApplyToSignal(&signal)
	if orderSettings.OrderType == trading.OrderTypeLayered && signal.PositionEffect != trading.PositionEffectClose {
		return t.executeLayeredSignal(ctx, signal, cfg, orderSettings)
	}
	if orderSettings.OrderType == trading.OrderTypeLayered {
		orderSettings.OrderType = trading.OrderTypeMarket
	}
	if err := validateBinanceRisk(signal.Risk); err != nil {
		return trading.OrderResult{}, err
	}
	client, apiID, err := t.client(cfg, signal.APIID)
	if err != nil {
		return trading.OrderResult{}, err
	}
	symbol, err := DeriveUSDMSymbol(signal.Coinpair, signal.Ticker)
	if err != nil {
		return trading.OrderResult{}, err
	}
	info, err := client.SymbolInfo(ctx, symbol)
	if err != nil {
		return trading.OrderResult{}, err
	}
	filters, err := info.TradingFilters()
	if err != nil {
		return trading.OrderResult{}, err
	}
	switchState, err := t.prepareDirectionSwitch(ctx, client, signal.Action, symbol, filters)
	if err != nil {
		return trading.OrderResult{}, err
	}
	sizingPx := signal.Price.Value
	orderType := strings.ToUpper(string(orderSettings.OrderType))
	orderPx := ""
	if orderSettings.OrderType == trading.OrderTypeLimit {
		sizingPx = orderSettings.LimitPrice(signal.Action, signal.Price.Value)
		orderPx = formatStep(sizingPx, filters.TickSize, signal.Action == trading.ActionShort)
	}
	orderStep := filters.StepSizeForOrderType(orderType)
	orderMinQty := filters.MinQtyForOrderType(orderType)
	quantity := formatStep(signal.Amount.Value/sizingPx, orderStep, false)
	if compareDecimal(quantity, orderMinQty) < 0 {
		return trading.OrderResult{}, fmt.Errorf("order size %s is below Binance minQty %s", quantity, orderMinQty)
	}
	posSide := binancePositionSide(signal.Action, cfg.PositionMode())
	marginType := binanceMarginType(cfg.MarginMode())
	if err := client.ChangeMarginType(ctx, symbol, marginType); err != nil {
		if shouldContinueAfterBinanceMarginSetupError(err) {
			if t.Logger != nil {
				t.Logger.Warn("binance margin type setup skipped before order", "symbol", symbol, "margin_type", marginType, "error", err)
			}
		} else {
			return trading.OrderResult{}, fmt.Errorf("binance change margin type %s to %s: %w", symbol, marginType, err)
		}
	}
	usedLeverage, err := t.ensureLeverage(ctx, client, symbol, posSide, signal.Action, signal.Leverage, switchState)
	if err != nil {
		return trading.OrderResult{}, fmt.Errorf("binance set leverage %s to %dx: %w", symbol, signal.Leverage, err)
	}
	clOrdID := clientOrderID(signal)
	req := PlaceOrderRequest{
		Symbol:           symbol,
		Side:             binanceSide(signal.Action),
		PositionSide:     posSide,
		Type:             orderType,
		Quantity:         quantity,
		Price:            orderPx,
		NewClientOrderID: clOrdID,
	}
	if orderSettings.OrderType == trading.OrderTypeLimit {
		req.TimeInForce = "GTC"
	}
	orderRequests, err := splitBinancePlaceOrderRequest(req, filters)
	if err != nil {
		return trading.OrderResult{}, err
	}
	if len(orderRequests) > 1 && t.Logger != nil {
		t.Logger.Warn("binance order quantity exceeds maxQty, splitting main order", "symbol", symbol, "quantity", req.Quantity, "parts", len(orderRequests))
	}
	acks, usedLeverage, err := t.placeOrderRequestsWithLeverageFallback(ctx, client, orderRequests, usedLeverage)
	result := trading.OrderResult{
		APIID:          apiID,
		TargetExchange: trading.ExchangeBinance,
		InstID:         symbol,
		ClOrdID:        joinBinancePlaceOrderClientIDs(orderRequests),
		OrdType:        req.Type,
		Px:             req.Price,
		OrdID:          joinBinanceOrderAckIDs(acks),
		Leverage:       usedLeverage,
	}
	if err != nil {
		return result, err
	}
	signal.Leverage = usedLeverage
	for _, orderReq := range orderRequests {
		riskOrders, err := t.placeRiskOrders(ctx, client, signal, orderReq, sizingPx, filters)
		if err != nil {
			return result, err
		}
		result.RiskOrders = append(result.RiskOrders, riskOrders...)
	}
	if t.Logger != nil {
		t.Logger.Info("binance order submitted", "api_id", apiID, "symbol", symbol, "action", signal.Action, "client_order_id", clOrdID)
	}
	return result, nil
}

func (t Trader) executeLayeredSignal(ctx context.Context, signal trading.Signal, cfg trading.RuntimeConfig, settings trading.OrderSettings) (trading.OrderResult, error) {
	client, apiID, err := t.client(cfg, signal.APIID)
	if err != nil {
		return trading.OrderResult{}, err
	}
	symbol, err := DeriveUSDMSymbol(signal.Coinpair, signal.Ticker)
	if err != nil {
		return trading.OrderResult{}, err
	}
	info, err := client.SymbolInfo(ctx, symbol)
	if err != nil {
		return trading.OrderResult{}, err
	}
	filters, err := info.TradingFilters()
	if err != nil {
		return trading.OrderResult{}, err
	}
	switchState, err := t.prepareDirectionSwitch(ctx, client, signal.Action, symbol, filters)
	if err != nil {
		return trading.OrderResult{}, err
	}
	ticker, err := client.BookTicker(ctx, symbol)
	if err != nil {
		return trading.OrderResult{}, fmt.Errorf("refresh Binance layered price for %s: %w", symbol, err)
	}
	marketPrice, err := binanceBookMidPrice(ticker)
	if err != nil {
		return trading.OrderResult{}, err
	}
	legs, err := settings.Layered.BuildPlan(signal.Action, signal.Amount.Value, signal.Price.Value, marketPrice)
	if err != nil {
		return trading.OrderResult{}, err
	}
	posSide := binancePositionSide(signal.Action, cfg.PositionMode())
	marginType := binanceMarginType(cfg.MarginMode())
	if err := client.ChangeMarginType(ctx, symbol, marginType); err != nil && !shouldContinueAfterBinanceMarginSetupError(err) {
		return trading.OrderResult{}, fmt.Errorf("binance change margin type %s to %s: %w", symbol, marginType, err)
	}
	usedLeverage, err := t.ensureLeverage(ctx, client, symbol, posSide, signal.Action, signal.Leverage, switchState)
	if err != nil {
		return trading.OrderResult{}, fmt.Errorf("binance set leverage %s to %dx: %w", symbol, signal.Leverage, err)
	}
	result := trading.OrderResult{
		APIID:          apiID,
		TargetExchange: trading.ExchangeBinance,
		InstID:         symbol,
		OrdType:        string(trading.OrderTypeLayered),
		Px:             trading.NormalizeFloat(marketPrice),
		Leverage:       usedLeverage,
		PositionEffect: signal.PositionEffect,
		PositionSide:   signal.PositionSide,
	}
	signal.Leverage = usedLeverage
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
		orderType := strings.ToUpper(string(leg.OrderType))
		orderPx := ""
		if leg.OrderType == trading.OrderTypeLimit {
			orderPx = formatStep(leg.Price, filters.TickSize, signal.Action == trading.ActionShort)
			layer.Price = orderPx
		}
		quantity := formatStep(leg.AmountUSDT/leg.Price, filters.StepSizeForOrderType(orderType), false)
		if compareDecimal(quantity, filters.MinQtyForOrderType(orderType)) < 0 {
			layer.Status = "failed"
			layer.Error = fmt.Sprintf("order size %s is below Binance minQty %s", quantity, filters.MinQtyForOrderType(orderType))
			if firstErr == nil {
				firstErr = errors.New(layer.Error)
			}
			result.Layers = append(result.Layers, layer)
			continue
		}
		baseID := clientOrderID(signal)
		clientID := layeredClientOrderID(baseID, index)
		req := PlaceOrderRequest{
			Symbol:           symbol,
			Side:             binanceSide(signal.Action),
			PositionSide:     posSide,
			Type:             orderType,
			Quantity:         quantity,
			Price:            orderPx,
			NewClientOrderID: clientID,
		}
		if leg.OrderType == trading.OrderTypeLimit {
			req.TimeInForce = "GTC"
		}
		orderRequests, splitErr := splitBinancePlaceOrderRequest(req, filters)
		if splitErr != nil {
			layer.Status = "failed"
			layer.Error = splitErr.Error()
			if firstErr == nil {
				firstErr = splitErr
			}
			result.Layers = append(result.Layers, layer)
			continue
		}
		acks, nextLeverage, placeErr := t.placeOrderRequestsWithLeverageFallback(ctx, client, orderRequests, usedLeverage)
		usedLeverage = nextLeverage
		result.Leverage = usedLeverage
		layer.ClOrdID = joinBinancePlaceOrderClientIDs(orderRequests[:minInt(len(acks), len(orderRequests))])
		layer.OrdID = joinBinanceOrderAckIDs(acks)
		if len(acks) > 0 {
			layer.Status = "submitted"
			result.ClOrdID = joinNonEmptyOrderIDs(result.ClOrdID, layer.ClOrdID)
			result.OrdID = joinNonEmptyOrderIDs(result.OrdID, layer.OrdID)
			for partIndex, ack := range acks {
				partReq := orderRequests[partIndex]
				riskOrders, riskErr := t.placeRiskOrders(ctx, client, signal, partReq, leg.Price, filters)
				result.RiskOrders = append(result.RiskOrders, riskOrders...)
				layer.RiskOrders = append(layer.RiskOrders, riskOrders...)
				_ = ack
				if riskErr != nil && layer.Error == "" {
					layer.Error = riskErr.Error()
					if firstErr == nil {
						firstErr = riskErr
					}
				}
			}
		}
		if placeErr != nil {
			layer.Error = placeErr.Error()
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
			firstErr = errors.New("all Binance layered order legs failed")
		}
		return result, firstErr
	}
	if firstErr != nil {
		result.Partial = true
		return result, fmt.Errorf("Binance layered order partially submitted: %w", firstErr)
	}
	return result, nil
}

func binanceBookMidPrice(ticker BookTicker) (float64, error) {
	bid, bidErr := strconv.ParseFloat(strings.TrimSpace(ticker.BidPrice), 64)
	ask, askErr := strconv.ParseFloat(strings.TrimSpace(ticker.AskPrice), 64)
	if bidErr != nil || askErr != nil || bid <= 0 || ask <= 0 {
		return 0, fmt.Errorf("invalid Binance book ticker bid=%q ask=%q", ticker.BidPrice, ticker.AskPrice)
	}
	return (bid + ask) / 2, nil
}

func layeredClientOrderID(base string, index int) string {
	suffix := fmt.Sprintf("-L%02d", index+1)
	return trimClientID(base, 32-len(suffix)) + suffix
}

func joinNonEmptyOrderIDs(left, right string) string {
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

func (t Trader) placeOrderRequestsWithLeverageFallback(ctx context.Context, client Client, reqs []PlaceOrderRequest, currentLeverage int) ([]OrderAck, int, error) {
	acks := make([]OrderAck, 0, len(reqs))
	for i, req := range reqs {
		ack, usedLeverage, err := t.placeOrderWithLeverageFallback(ctx, client, req, currentLeverage)
		currentLeverage = usedLeverage
		if err != nil {
			return acks, currentLeverage, fmt.Errorf("binance place order part %d/%d quantity %s: %w", i+1, len(reqs), req.Quantity, err)
		}
		acks = append(acks, ack)
	}
	return acks, currentLeverage, nil
}

func (t Trader) placeOrderWithLeverageFallback(ctx context.Context, client Client, req PlaceOrderRequest, currentLeverage int) (OrderAck, int, error) {
	ack, err := client.PlaceOrder(ctx, req)
	if err == nil || !isBinanceMaxPositionAtLeverageError(err) || currentLeverage <= 1 {
		return ack, currentLeverage, err
	}
	attempted := []int{currentLeverage}
	lastErr := err
	for leverage := currentLeverage - 1; leverage >= 1; leverage-- {
		attempted = append(attempted, leverage)
		if setErr := client.SetLeverage(ctx, req.Symbol, leverage); setErr != nil {
			lastErr = setErr
			if isBinanceLeverageFallbackError(setErr) {
				if t.Logger != nil {
					t.Logger.Warn("binance leverage fallback setup rejected after max-position error", "symbol", req.Symbol, "leverage", leverage, "error", setErr)
				}
				continue
			}
			return OrderAck{}, leverage, setErr
		}
		if t.Logger != nil {
			t.Logger.Warn("binance order exceeded maximum position at leverage, retrying lower leverage", "symbol", req.Symbol, "previous_leverage", leverage+1, "leverage", leverage, "error", err)
		}
		ack, err = client.PlaceOrder(ctx, req)
		if err == nil {
			return ack, leverage, nil
		}
		lastErr = err
		if !isBinanceMaxPositionAtLeverageError(err) {
			return ack, leverage, err
		}
	}
	return OrderAck{}, 1, fmt.Errorf("binance order exceeded maximum allowable position after trying leverage %s: %w", binanceLeverageAttemptsText(attempted), lastErr)
}

func (t Trader) placeSplitAlgoOrder(ctx context.Context, client Client, req AlgoOrderRequest, filters TradingFilters) ([]trading.RiskOrderResult, error) {
	reqs, err := splitBinanceAlgoOrderRequest(req, filters)
	if err != nil {
		return nil, err
	}
	if len(reqs) > 1 && t.Logger != nil {
		t.Logger.Warn("binance algo order quantity exceeds maxQty, splitting algo order", "symbol", req.Symbol, "type", req.Type, "quantity", req.Quantity, "parts", len(reqs))
	}
	out := make([]trading.RiskOrderResult, 0, len(reqs))
	for i, part := range reqs {
		ack, err := client.NewAlgoOrder(ctx, part)
		if err != nil {
			return out, fmt.Errorf("binance place algo order part %d/%d quantity %s: %w", i+1, len(reqs), part.Quantity, err)
		}
		out = append(out, riskOrderResultFromBinanceAlgo(part, ack))
	}
	return out, nil
}

func riskOrderResultFromBinanceAlgo(req AlgoOrderRequest, ack AlgoOrderAck) trading.RiskOrderResult {
	algoID := ""
	if ack.AlgoID > 0 {
		algoID = strconv.FormatInt(ack.AlgoID, 10)
	}
	clientAlgoID := strings.TrimSpace(ack.ClientAlgoID)
	if clientAlgoID == "" {
		clientAlgoID = strings.TrimSpace(req.NewClientOrderID)
	}
	orderType := strings.TrimSpace(ack.OrderType)
	if orderType == "" {
		orderType = strings.ToUpper(strings.TrimSpace(req.Type))
	}
	triggerPrice := strings.TrimSpace(ack.TriggerPrice)
	if triggerPrice == "" {
		triggerPrice = strings.TrimSpace(req.TriggerPrice)
	}
	activatePrice := strings.TrimSpace(ack.ActivatePrice)
	if activatePrice == "" {
		activatePrice = strings.TrimSpace(req.ActivationPrice)
	}
	callbackRate := strings.TrimSpace(ack.CallbackRate)
	if callbackRate == "" {
		callbackRate = strings.TrimSpace(req.CallbackRate)
	}
	quantity := strings.TrimSpace(ack.Quantity)
	if quantity == "" {
		quantity = strings.TrimSpace(req.Quantity)
	}
	side := strings.TrimSpace(ack.Side)
	if side == "" {
		side = strings.ToUpper(strings.TrimSpace(req.Side))
	}
	positionSide := strings.TrimSpace(ack.PositionSide)
	if positionSide == "" {
		positionSide = strings.ToUpper(strings.TrimSpace(req.PositionSide))
	}
	return trading.RiskOrderResult{
		Exchange:      trading.ExchangeBinance,
		AlgoID:        algoID,
		ClientAlgoID:  clientAlgoID,
		OrderType:     orderType,
		Side:          side,
		PositionSide:  positionSide,
		Quantity:      quantity,
		TriggerPrice:  triggerPrice,
		ActivatePrice: activatePrice,
		CallbackRate:  callbackRate,
	}
}

func (t Trader) ensureLeverage(ctx context.Context, client Client, symbol, posSide string, action trading.Side, desired int, state directionSwitchState) (int, error) {
	if desired <= 0 {
		return 0, fmt.Errorf("invalid leverage %d: must be positive", desired)
	}
	if binanceRemoteLeverageMatches(state.positions, symbol, posSide, action, desired) {
		if t.Logger != nil {
			t.Logger.Info("binance leverage already matches configured value", "symbol", symbol, "position_side", posSide, "leverage", desired)
		}
		return desired, nil
	}
	return t.setLeverageWithFallback(ctx, client, symbol, desired)
}

func (t Trader) setLeverageWithFallback(ctx context.Context, client Client, symbol string, desired int) (int, error) {
	attempts := binanceLeverageAttempts(desired)
	var lastErr error
	for idx, leverage := range attempts {
		err := client.SetLeverage(ctx, symbol, leverage)
		if err == nil {
			return leverage, nil
		}
		lastErr = err
		if !isBinanceLeverageFallbackError(err) {
			return 0, err
		}
		if t.Logger != nil && idx+1 < len(attempts) {
			t.Logger.Warn("binance leverage rejected, trying fallback leverage", "symbol", symbol, "leverage", leverage, "next_leverage", attempts[idx+1], "error", err)
		}
	}
	return 0, fmt.Errorf("set leverage failed after trying %s: %w", binanceLeverageAttemptsText(attempts), lastErr)
}

func binanceLeverageAttempts(desired int) []int {
	if desired <= 0 {
		return nil
	}
	attempts := make([]int, 0, maxBinanceLeverageFallback)
	seen := map[int]bool{}
	add := func(leverage int) {
		if leverage <= 0 || seen[leverage] {
			return
		}
		attempts = append(attempts, leverage)
		seen[leverage] = true
	}
	add(desired)
	for leverage := minInt(desired-1, maxBinanceLeverageFallback); leverage >= 1; leverage-- {
		add(leverage)
	}
	for leverage := desired + 1; leverage <= maxBinanceLeverageFallback; leverage++ {
		add(leverage)
	}
	return attempts
}

func binanceLeverageAttemptsText(attempts []int) string {
	parts := make([]string, 0, len(attempts))
	for _, leverage := range attempts {
		parts = append(parts, strconv.Itoa(leverage)+"x")
	}
	return strings.Join(parts, ", ")
}

func isBinanceLeverageFallbackError(err error) bool {
	if err == nil {
		return false
	}
	if IsAPIErrorCode(err, -4028) {
		return true
	}
	text := strings.ToLower(err.Error())
	if !strings.Contains(text, "leverage") {
		return false
	}
	return strings.Contains(text, "not valid") ||
		strings.Contains(text, "invalid") ||
		strings.Contains(text, "maximum") ||
		strings.Contains(text, "exceed") ||
		strings.Contains(text, "less than") ||
		strings.Contains(text, "greater than")
}

func isBinanceMaxPositionAtLeverageError(err error) bool {
	return IsAPIErrorCode(err, -2027)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func binanceRemoteLeverageMatches(positions []Position, symbol, posSide string, action trading.Side, desired int) bool {
	found := false
	mismatch := false
	for _, position := range positions {
		if !strings.EqualFold(position.Symbol, symbol) || !binancePositionMatchesLeverageScope(position, posSide, action) {
			continue
		}
		if strings.TrimSpace(position.Leverage) == "" {
			continue
		}
		found = true
		if !binanceLeverageValueMatches(position.Leverage, desired) {
			mismatch = true
		}
	}
	return found && !mismatch
}

func binancePositionMatchesLeverageScope(position Position, posSide string, action trading.Side) bool {
	wantSide := normalizeBinancePositionSide(posSide)
	if wantSide != "" {
		return normalizeBinancePositionSide(position.PositionSide) == wantSide
	}
	if direction, ok := binancePositionDirection(position); ok {
		return direction == action
	}
	return normalizeBinancePositionSide(position.PositionSide) == ""
}

func binanceLeverageValueMatches(raw string, desired int) bool {
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	return err == nil && math.Abs(value-float64(desired)) < 1e-9
}

func (t Trader) placeRiskOrders(ctx context.Context, client Client, signal trading.Signal, order PlaceOrderRequest, entryPx float64, filters TradingFilters) ([]trading.RiskOrderResult, error) {
	risk := signal.Risk
	risk.Normalize()
	switch risk.Type {
	case trading.RiskNone:
		return nil, nil
	case trading.RiskTrailing:
		return t.placeTrailingStop(ctx, client, signal, order, filters)
	case trading.RiskTPSL:
	default:
		return nil, nil
	}
	if risk.TPPct == nil || risk.SLPct == nil {
		return nil, nil
	}
	closeSide := "SELL"
	if signal.Action == trading.ActionShort {
		closeSide = "BUY"
	}
	tpPx, slPx := riskTriggerPrices(signal.Action, entryPx, risk.TPPct.Value, risk.SLPct.Value, signal.Leverage)
	tpTrigger := formatStep(tpPx, filters.TickSize, signal.Action == trading.ActionLong)
	slTrigger := formatStep(slPx, filters.TickSize, signal.Action == trading.ActionShort)
	if markPx, err := binanceMarkPrice(ctx, client, order.Symbol); err == nil {
		var adjusted bool
		tpTrigger, slTrigger, adjusted, err = safeBinanceTPSLTriggers(signal.Action, tpTrigger, slTrigger, markPx, filters.TickSize)
		if err != nil {
			return nil, err
		}
		if adjusted && t.Logger != nil {
			t.Logger.Warn("binance tp/sl trigger adjusted away from mark price", "symbol", order.Symbol, "mark_price", trading.NormalizeFloat(markPx), "tp_trigger", tpTrigger, "sl_trigger", slTrigger)
		}
	} else if t.Logger != nil {
		t.Logger.Warn("binance mark price unavailable before tp/sl placement", "symbol", order.Symbol, "error", err)
	}
	tpID := binanceClientIDWithSuffix(order.NewClientOrderID, "TP")
	slID := binanceClientIDWithSuffix(order.NewClientOrderID, "SL")
	out := []trading.RiskOrderResult{}
	tpOrders, err := t.placeSplitAlgoOrder(ctx, client, AlgoOrderRequest{
		Symbol:           order.Symbol,
		Side:             closeSide,
		PositionSide:     order.PositionSide,
		Type:             "TAKE_PROFIT_MARKET",
		Quantity:         order.Quantity,
		TriggerPrice:     tpTrigger,
		WorkingType:      "MARK_PRICE",
		NewClientOrderID: tpID,
		ReduceOnly:       order.PositionSide == "",
	}, filters)
	if err != nil {
		return out, err
	}
	out = append(out, tpOrders...)
	slOrders, err := t.placeSplitAlgoOrder(ctx, client, AlgoOrderRequest{
		Symbol:           order.Symbol,
		Side:             closeSide,
		PositionSide:     order.PositionSide,
		Type:             "STOP_MARKET",
		Quantity:         order.Quantity,
		TriggerPrice:     slTrigger,
		WorkingType:      "MARK_PRICE",
		NewClientOrderID: slID,
		ReduceOnly:       order.PositionSide == "",
	}, filters)
	if err != nil {
		return out, err
	}
	out = append(out, slOrders...)
	return out, nil
}

func validateBinanceRisk(risk trading.Risk) error {
	risk.Normalize()
	if risk.Type != trading.RiskTrailing {
		return nil
	}
	if risk.TrailingPct == nil || !risk.TrailingPct.Set || risk.TrailingPct.Value <= 0 {
		return fmt.Errorf("Binance trailing_pct must be positive")
	}
	if risk.TrailingPct.Value < 0.1 || risk.TrailingPct.Value > 10 {
		return fmt.Errorf("Binance trailing_pct must be between 0.1 and 10, got %s", trading.NormalizeFloat(risk.TrailingPct.Value))
	}
	return nil
}

func (t Trader) placeTrailingStop(ctx context.Context, client Client, signal trading.Signal, order PlaceOrderRequest, filters TradingFilters) ([]trading.RiskOrderResult, error) {
	risk := signal.Risk
	risk.Normalize()
	if risk.TrailingPct == nil || !risk.TrailingPct.Set {
		return nil, nil
	}
	callbackRate := risk.TrailingPct.Value
	if callbackRate < 0.1 || callbackRate > 10 {
		return nil, fmt.Errorf("Binance trailing_pct must be between 0.1 and 10, got %s", trading.NormalizeFloat(callbackRate))
	}
	closeSide := "SELL"
	if signal.Action == trading.ActionShort {
		closeSide = "BUY"
	}
	trailingID := binanceClientIDWithSuffix(order.NewClientOrderID, "TS")
	return t.placeSplitAlgoOrder(ctx, client, AlgoOrderRequest{
		Symbol:           order.Symbol,
		Side:             closeSide,
		PositionSide:     order.PositionSide,
		Type:             "TRAILING_STOP_MARKET",
		Quantity:         order.Quantity,
		CallbackRate:     trading.NormalizeFloat(callbackRate),
		WorkingType:      "MARK_PRICE",
		NewClientOrderID: trailingID,
		ReduceOnly:       order.PositionSide == "",
	}, filters)
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
	client := Client{BaseURL: cfg.BinanceBaseURL(), Credentials: creds, HTTPClient: t.HTTPClient}
	return t.checkClient(ctx, cfg, client, resolvedID)
}

func (t Trader) checkClient(ctx context.Context, cfg trading.RuntimeConfig, client Client, apiID string) (map[string]any, error) {
	balances, err := client.AccountBalance(ctx)
	if err != nil {
		return nil, err
	}
	info, err := client.ExchangeInfo(ctx)
	if err != nil {
		return nil, err
	}
	usdtBalance, found := usdtBalanceFromAccount(balances)
	return map[string]any{
		"ok":                 true,
		"exchange":           trading.ExchangeBinance,
		"api_id":             apiID,
		"base_url":           cfg.BinanceBaseURL(),
		"usdt_balance":       usdtBalance,
		"usdt_balance_found": found,
		"instruments_count":  len(info.Symbols),
	}, nil
}

func usdtBalanceFromAccount(balances []Balance) (USDTBalance, bool) {
	balance, ok := USDTBalanceFromAccount(balances)
	if !ok {
		return USDTBalance{}, false
	}
	return USDTBalance{
		Ccy:              "USDT",
		TotalEquity:      balance.Balance,
		Equity:           balance.Balance,
		AvailableEquity:  balance.AvailableBalance,
		AvailableBalance: balance.AvailableBalance,
		CashBalance:      balance.CrossWalletBalance,
		UpdateTime:       strconv.FormatInt(balance.UpdateTime, 10),
	}, true
}

func (t Trader) client(cfg trading.RuntimeConfig, apiID string) (Client, string, error) {
	creds, resolvedID, err := t.credentials(apiID)
	if err != nil {
		return Client{}, resolvedID, err
	}
	return Client{BaseURL: cfg.BinanceBaseURL(), Credentials: creds, HTTPClient: t.HTTPClient}, resolvedID, nil
}

func (t Trader) credentials(apiID string) (Credentials, string, error) {
	if t.CredentialProvider != nil {
		return t.CredentialProvider.BinanceCredentials(apiID)
	}
	creds := trimCredentials(t.Credentials)
	if err := creds.Validate(); err != nil {
		return Credentials{}, strings.TrimSpace(apiID), err
	}
	return creds, strings.TrimSpace(apiID), nil
}

type TradingFilters struct {
	TickSize       float64
	StepSize       float64
	MarketStepSize float64
	MinQty         string
	MarketMinQty   string
	MaxQty         string
	MarketMaxQty   string
}

func (s SymbolInfo) TradingFilters() (TradingFilters, error) {
	var out TradingFilters
	for _, filter := range s.Filters {
		switch filter.FilterType {
		case "PRICE_FILTER":
			tick, err := strconv.ParseFloat(strings.TrimSpace(filter.TickSize), 64)
			if err != nil || tick <= 0 {
				return TradingFilters{}, fmt.Errorf("invalid Binance tickSize %q", filter.TickSize)
			}
			out.TickSize = tick
		case "LOT_SIZE":
			step, err := strconv.ParseFloat(strings.TrimSpace(filter.StepSize), 64)
			if err != nil || step <= 0 {
				return TradingFilters{}, fmt.Errorf("invalid Binance stepSize %q", filter.StepSize)
			}
			out.StepSize = step
			out.MinQty = strings.TrimSpace(filter.MinQty)
			out.MaxQty = strings.TrimSpace(filter.MaxQty)
		case "MARKET_LOT_SIZE":
			step, err := strconv.ParseFloat(strings.TrimSpace(filter.StepSize), 64)
			if err == nil && step > 0 {
				out.MarketStepSize = step
			}
			out.MarketMinQty = strings.TrimSpace(filter.MinQty)
			out.MarketMaxQty = strings.TrimSpace(filter.MaxQty)
		}
	}
	if out.TickSize <= 0 {
		out.TickSize = math.Pow10(-maxInt(s.PricePrecision, 0))
	}
	if out.StepSize <= 0 {
		out.StepSize = math.Pow10(-maxInt(s.QuantityPrecision, 0))
	}
	if out.MinQty == "" {
		out.MinQty = formatStep(out.StepSize, out.StepSize, false)
	}
	return out, nil
}

func (f TradingFilters) StepSizeForOrderType(orderType string) float64 {
	if strings.EqualFold(strings.TrimSpace(orderType), "MARKET") && f.MarketStepSize > 0 {
		return f.MarketStepSize
	}
	return f.StepSize
}

func (f TradingFilters) MinQtyForOrderType(orderType string) string {
	if strings.EqualFold(strings.TrimSpace(orderType), "MARKET") && positiveDecimalString(f.MarketMinQty) {
		return strings.TrimSpace(f.MarketMinQty)
	}
	return strings.TrimSpace(f.MinQty)
}

func (f TradingFilters) MaxQtyForOrderType(orderType string) string {
	if strings.EqualFold(strings.TrimSpace(orderType), "MARKET") && positiveDecimalString(f.MarketMaxQty) {
		return strings.TrimSpace(f.MarketMaxQty)
	}
	if positiveDecimalString(f.MaxQty) {
		return strings.TrimSpace(f.MaxQty)
	}
	return ""
}

func DeriveUSDMSymbol(coinpair, ticker string) (string, error) {
	raw := strings.TrimSpace(coinpair)
	if raw == "" {
		raw = strings.TrimSpace(ticker)
	}
	if i := strings.LastIndex(raw, ":"); i >= 0 {
		raw = raw[i+1:]
	}
	raw = strings.ToUpper(strings.TrimSpace(raw))
	raw = strings.TrimSuffix(raw, ".P")
	raw = strings.TrimSuffix(raw, "PERP")
	raw = strings.TrimSuffix(raw, "SWAP")
	if symbol, ok := deriveDelimitedUSDMSymbol(raw); ok {
		return symbol, nil
	}
	raw = strings.ReplaceAll(raw, "-", "")
	raw = strings.ReplaceAll(raw, "_", "")
	raw = strings.ReplaceAll(raw, "/", "")
	raw = strings.ReplaceAll(raw, " ", "")
	if raw == "" {
		return "", fmt.Errorf("coinpair or ticker is required")
	}
	if isBinanceUSDMSymbol(raw) {
		return raw, nil
	}
	return raw + "USDT", nil
}

func deriveDelimitedUSDMSymbol(raw string) (string, bool) {
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == '-' || r == '_' || r == '/' || r == ' '
	})
	cleaned := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" || part == "SWAP" || part == "PERP" {
			continue
		}
		cleaned = append(cleaned, part)
	}
	if len(cleaned) < 2 {
		return "", false
	}
	base := cleaned[0]
	quote := cleaned[1]
	if base == "" || !isBinanceUSDMQuoteAsset(quote) {
		return "", false
	}
	return base + quote, true
}

func isBinanceUSDMSymbol(symbol string) bool {
	for _, quote := range binanceUSDMQuoteAssets {
		if strings.HasSuffix(symbol, quote) && len(symbol) > len(quote) {
			return true
		}
	}
	return false
}

func isBinanceUSDMQuoteAsset(quote string) bool {
	for _, supported := range binanceUSDMQuoteAssets {
		if quote == supported {
			return true
		}
	}
	return false
}

func binanceSide(action trading.Side) string {
	if action == trading.ActionShort {
		return "SELL"
	}
	return "BUY"
}

func binancePositionSide(action trading.Side, mode string) string {
	if mode != config.PositionLongShort {
		return ""
	}
	if action == trading.ActionShort {
		return "SHORT"
	}
	return "LONG"
}

func binanceMarginType(mode string) string {
	if mode == config.MarginCross {
		return "CROSSED"
	}
	return "ISOLATED"
}

func shouldContinueAfterBinanceMarginSetupError(err error) bool {
	return IsAPIErrorCode(err, -4047, -4048, -4067, -4068)
}

func splitBinancePlaceOrderRequest(req PlaceOrderRequest, filters TradingFilters) ([]PlaceOrderRequest, error) {
	chunks, err := splitBinanceQuantityByMax(req.Quantity, filters.MaxQtyForOrderType(req.Type), filters.StepSizeForOrderType(req.Type), filters.MinQtyForOrderType(req.Type))
	if err != nil {
		return nil, err
	}
	if len(chunks) <= 1 {
		return []PlaceOrderRequest{req}, nil
	}
	out := make([]PlaceOrderRequest, 0, len(chunks))
	for i, qty := range chunks {
		part := req
		part.Quantity = qty
		part.NewClientOrderID = splitBinanceClientOrderID(req.NewClientOrderID, i+1)
		out = append(out, part)
	}
	return out, nil
}

// SplitPlaceOrderRequestByMaxQty splits a Binance place-order request by the symbol maxQty filters.
func SplitPlaceOrderRequestByMaxQty(req PlaceOrderRequest, filters TradingFilters) ([]PlaceOrderRequest, error) {
	return splitBinancePlaceOrderRequest(req, filters)
}

func splitBinanceAlgoOrderRequest(req AlgoOrderRequest, filters TradingFilters) ([]AlgoOrderRequest, error) {
	chunks, err := splitBinanceQuantityByMax(req.Quantity, filters.MaxQtyForOrderType("MARKET"), filters.StepSizeForOrderType("MARKET"), filters.MinQtyForOrderType("MARKET"))
	if err != nil {
		return nil, err
	}
	if len(chunks) <= 1 {
		return []AlgoOrderRequest{req}, nil
	}
	out := make([]AlgoOrderRequest, 0, len(chunks))
	for i, qty := range chunks {
		part := req
		part.Quantity = qty
		part.NewClientOrderID = splitBinanceClientOrderID(req.NewClientOrderID, i+1)
		out = append(out, part)
	}
	return out, nil
}

func splitBinanceQuantityByMax(quantityRaw, maxQtyRaw string, step float64, minQtyRaw string) ([]string, error) {
	quantityRaw = strings.TrimSpace(quantityRaw)
	maxQtyRaw = strings.TrimSpace(maxQtyRaw)
	if quantityRaw == "" {
		return nil, errors.New("Binance order quantity is required")
	}
	if !positiveDecimalString(maxQtyRaw) || compareDecimal(quantityRaw, maxQtyRaw) <= 0 {
		return []string{quantityRaw}, nil
	}
	quantity, err := strconv.ParseFloat(quantityRaw, 64)
	if err != nil || quantity <= 0 {
		return nil, fmt.Errorf("invalid Binance order quantity %q", quantityRaw)
	}
	maxQty, err := strconv.ParseFloat(maxQtyRaw, 64)
	if err != nil || maxQty <= 0 {
		return nil, fmt.Errorf("invalid Binance maxQty %q", maxQtyRaw)
	}
	chunks := []string{}
	remaining := quantity
	for remaining > 0 {
		if len(chunks) >= maxBinanceOrderSplits {
			return nil, fmt.Errorf("Binance order quantity %s exceeds maxQty %s and requires more than %d split orders", quantityRaw, maxQtyRaw, maxBinanceOrderSplits)
		}
		part := math.Min(remaining, maxQty)
		partRaw := formatStep(part, step, false)
		if compareDecimal(partRaw, "0") <= 0 {
			return nil, fmt.Errorf("Binance split order quantity rounded to zero from %s", trading.NormalizeFloat(part))
		}
		if minQtyRaw = strings.TrimSpace(minQtyRaw); minQtyRaw != "" && compareDecimal(partRaw, minQtyRaw) < 0 {
			return nil, fmt.Errorf("Binance split order quantity %s is below minQty %s", partRaw, minQtyRaw)
		}
		chunks = append(chunks, partRaw)
		placed, err := strconv.ParseFloat(partRaw, 64)
		if err != nil || placed <= 0 {
			return nil, fmt.Errorf("invalid Binance split order quantity %q", partRaw)
		}
		remaining -= placed
		if step > 0 && remaining < step/2 {
			break
		}
		if remaining < 1e-12 {
			break
		}
	}
	return chunks, nil
}

func splitBinanceClientOrderID(base string, part int) string {
	base = strings.TrimSpace(base)
	if base == "" {
		return ""
	}
	suffix := fmt.Sprintf("P%02d", part)
	return binanceClientIDWithSuffix(base, suffix)
}

// binanceClientIDWithSuffix keeps the risk-order and split-order suffixes
// distinct even when a layered entry ID is already at Binance's 32-character
// limit. In particular, a layered entry can end in -L01 and its TP/SL order
// can then be split with a P01 suffix; blindly truncating the entry ID would
// otherwise collapse different layers or TP/SL pairs onto the same ID.
func binanceClientIDWithSuffix(base, suffix string) string {
	base = strings.TrimSpace(base)
	suffix = strings.TrimSpace(suffix)
	if base == "" {
		return ""
	}
	const maxLength = 32
	if len(suffix) >= maxLength {
		return suffix[:maxLength]
	}
	maxBaseLength := maxLength - len(suffix)
	if len(base) <= maxBaseLength {
		return base + suffix
	}

	// Preserve the distinguishing tail (for example -L01TP) and add a short
	// digest of the full base so truncation remains deterministic and unique
	// for different layered entries and split parts.
	const tailLength = 8
	const digestLength = 6
	keepTail := minInt(tailLength, maxBaseLength)
	keepDigest := minInt(digestLength, maxBaseLength-keepTail)
	keepPrefix := maxBaseLength - keepTail - keepDigest
	sum := sha256.Sum256([]byte(base))
	digest := strings.ToUpper(hex.EncodeToString(sum[:]))[:keepDigest]
	return base[:keepPrefix] + digest + base[len(base)-keepTail:] + suffix
}

func joinBinancePlaceOrderClientIDs(reqs []PlaceOrderRequest) string {
	ids := make([]string, 0, len(reqs))
	for _, req := range reqs {
		if id := strings.TrimSpace(req.NewClientOrderID); id != "" {
			ids = append(ids, id)
		}
	}
	return strings.Join(ids, " / ")
}

func joinBinanceOrderAckIDs(acks []OrderAck) string {
	ids := make([]string, 0, len(acks))
	for _, ack := range acks {
		if ack.OrderID != 0 {
			ids = append(ids, strconv.FormatInt(ack.OrderID, 10))
		}
	}
	return strings.Join(ids, " / ")
}

func positiveDecimalString(raw string) bool {
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	return err == nil && value > 0
}

func riskTriggerPrices(action trading.Side, entryPx, tpPct, slPct float64, leverage int) (float64, float64) {
	tp, sl, err := trading.FixedTPSLTriggerPrices(action, entryPx, tpPct, slPct, leverage)
	if err != nil {
		return 0, 0
	}
	return tp, sl
}

func binanceMarkPrice(ctx context.Context, client Client, symbol string) (float64, error) {
	premium, err := client.PremiumIndex(ctx, symbol)
	if err != nil {
		return 0, err
	}
	markPx, err := strconv.ParseFloat(strings.TrimSpace(premium.MarkPrice), 64)
	if err != nil || markPx <= 0 {
		if err == nil {
			err = fmt.Errorf("must be positive")
		}
		return 0, fmt.Errorf("invalid Binance mark price %q: %w", premium.MarkPrice, err)
	}
	return markPx, nil
}

func safeBinanceTPSLTriggers(action trading.Side, tpTrigger, slTrigger string, markPx, tickSize float64) (string, string, bool, error) {
	if markPx <= 0 || tickSize <= 0 {
		return tpTrigger, slTrigger, false, nil
	}
	tpPx, err := strconv.ParseFloat(strings.TrimSpace(tpTrigger), 64)
	if err != nil || tpPx <= 0 {
		return "", "", false, fmt.Errorf("invalid Binance TP trigger price %q", tpTrigger)
	}
	slPx, err := strconv.ParseFloat(strings.TrimSpace(slTrigger), 64)
	if err != nil || slPx <= 0 {
		return "", "", false, fmt.Errorf("invalid Binance SL trigger price %q", slTrigger)
	}
	adjusted := false
	switch action {
	case trading.ActionShort:
		if tpPx >= markPx {
			tpPx = markPx - tickSize
			adjusted = true
		}
		if slPx <= markPx {
			slPx = markPx + tickSize
			adjusted = true
		}
	default:
		if tpPx <= markPx {
			tpPx = markPx + tickSize
			adjusted = true
		}
		if slPx >= markPx {
			slPx = markPx - tickSize
			adjusted = true
		}
	}
	if tpPx <= 0 || slPx <= 0 {
		return "", "", false, fmt.Errorf("Binance TP/SL trigger adjustment failed: mark price %s is too close to zero", trading.NormalizeFloat(markPx))
	}
	if !adjusted {
		return tpTrigger, slTrigger, false, nil
	}
	tpTrigger = formatStep(tpPx, tickSize, action == trading.ActionLong)
	slTrigger = formatStep(slPx, tickSize, action == trading.ActionShort)
	if err := validateBinanceTPSLTriggerSide(action, tpTrigger, slTrigger, markPx); err != nil {
		return "", "", false, err
	}
	return tpTrigger, slTrigger, true, nil
}

func validateBinanceTPSLTriggerSide(action trading.Side, tpTrigger, slTrigger string, markPx float64) error {
	tpPx, tpErr := strconv.ParseFloat(strings.TrimSpace(tpTrigger), 64)
	slPx, slErr := strconv.ParseFloat(strings.TrimSpace(slTrigger), 64)
	if tpErr != nil || slErr != nil {
		return fmt.Errorf("invalid Binance TP/SL trigger prices after adjustment: tp=%q sl=%q", tpTrigger, slTrigger)
	}
	if action == trading.ActionShort {
		if tpPx >= markPx || slPx <= markPx {
			return fmt.Errorf("Binance short TP/SL trigger prices would immediately trigger: mark=%s tp=%s sl=%s", trading.NormalizeFloat(markPx), tpTrigger, slTrigger)
		}
		return nil
	}
	if tpPx <= markPx || slPx >= markPx {
		return fmt.Errorf("Binance long TP/SL trigger prices would immediately trigger: mark=%s tp=%s sl=%s", trading.NormalizeFloat(markPx), tpTrigger, slTrigger)
	}
	return nil
}

func formatStep(value, step float64, ceil bool) string {
	if step <= 0 {
		return trading.NormalizeFloat(value)
	}
	scaled := value / step
	if ceil {
		value = math.Ceil(scaled-1e-12) * step
	} else {
		value = math.Floor(scaled+1e-12) * step
	}
	return trading.NormalizeFloat(value)
}

func compareDecimal(a, b string) int {
	af, _ := strconv.ParseFloat(strings.TrimSpace(a), 64)
	bf, _ := strconv.ParseFloat(strings.TrimSpace(b), 64)
	if af < bf {
		return -1
	}
	if af > bf {
		return 1
	}
	return 0
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func clientOrderID(signal trading.Signal) string {
	sum := sha256.Sum256([]byte(signal.CanonicalTokenPayload() + "|" + signal.SentAt + "|" + signal.Ticker + "|binance"))
	short := hex.EncodeToString(sum[:])[:10]
	return trimClientID(strings.ToUpper(fmt.Sprintf("TV%d%s", time.Now().UTC().UnixMilli(), short)), 32)
}

func trimClientID(v string, maxLen int) string {
	v = strings.TrimSpace(v)
	if maxLen <= 0 {
		return ""
	}
	if len(v) > maxLen {
		return v[:maxLen]
	}
	return v
}
