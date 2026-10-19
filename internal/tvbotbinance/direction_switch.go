package binance

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

const (
	directionSwitchCloseTimeout = 10 * time.Second
	directionSwitchPollInterval = 200 * time.Millisecond
)

var directionSwitchCloseSeq uint64

type directionSwitchState struct {
	positions  []Position
	openOrders []OpenOrder
}

func (t Trader) prepareDirectionSwitch(ctx context.Context, client Client, action trading.Side, symbol string, filters TradingFilters) (directionSwitchState, error) {
	var state directionSwitchState
	opposite, ok := oppositeAction(action)
	if !ok {
		return state, nil
	}
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	positions, err := client.Positions(ctx, symbol)
	if err != nil {
		return state, fmt.Errorf("binance query positions before direction switch: %w", err)
	}
	state.positions = positions
	openOrders, err := client.OpenOrders(ctx, symbol)
	if err != nil {
		return state, fmt.Errorf("binance query open orders before direction switch: %w", err)
	}
	state.openOrders = openOrders
	oppositeOrders := binanceOpenOrdersForDirection(openOrders, symbol, opposite)
	oppositePositions := binancePositionsForDirection(positions, symbol, opposite)
	if len(oppositeOrders) == 0 && len(oppositePositions) == 0 {
		return state, nil
	}
	if err := t.cancelBinanceOpenOrders(ctx, client, oppositeOrders); err != nil {
		return state, err
	}
	if err := t.cancelBinanceAlgoOrdersForDirection(ctx, client, symbol, opposite); err != nil {
		return state, err
	}

	positions, err = client.Positions(ctx, symbol)
	if err != nil {
		return state, fmt.Errorf("binance refresh positions after canceling reverse orders: %w", err)
	}
	state.positions = positions
	for _, position := range binancePositionsForDirection(positions, symbol, opposite) {
		if err := t.closeBinanceReversePosition(ctx, client, position, filters); err != nil {
			return state, err
		}
	}
	positions, err = client.Positions(ctx, symbol)
	if err != nil {
		return state, fmt.Errorf("binance refresh positions after closing reverse positions: %w", err)
	}
	state.positions = positions
	return state, nil
}

func (t Trader) cancelBinanceOpenOrders(ctx context.Context, client Client, orders []OpenOrder) error {
	for _, order := range orders {
		req := CancelOrderRequest{Symbol: strings.ToUpper(strings.TrimSpace(order.Symbol))}
		if order.OrderID > 0 {
			req.OrderID = strconv.FormatInt(order.OrderID, 10)
		} else if strings.TrimSpace(order.ClientOrderID) != "" {
			req.OrigClientOrderID = strings.TrimSpace(order.ClientOrderID)
		} else {
			continue
		}
		if _, err := client.CancelOrder(ctx, req); err != nil {
			return fmt.Errorf("binance cancel reverse open order %s %s: %w", req.Symbol, firstNonEmpty(req.OrderID, req.OrigClientOrderID), err)
		}
		if t.Logger != nil {
			t.Logger.Info("binance reverse open order canceled", "symbol", req.Symbol, "order_id", req.OrderID, "client_order_id", req.OrigClientOrderID)
		}
	}
	return nil
}

func (t Trader) cancelBinanceAlgoOrdersForDirection(ctx context.Context, client Client, symbol string, direction trading.Side) error {
	orders, err := client.OpenAlgoOrders(ctx, symbol)
	if err != nil {
		return fmt.Errorf("binance query open algo orders before direction switch: %w", err)
	}
	for _, order := range orders {
		orderDirection, ok := binanceAlgoOrderDirection(order)
		if !ok || orderDirection != direction || !strings.EqualFold(order.Symbol, symbol) {
			continue
		}
		if _, err := client.CancelAlgoOrder(ctx, order.AlgoID, order.ClientAlgoID); err != nil {
			return fmt.Errorf("binance cancel reverse algo order %s %d %s: %w", order.Symbol, order.AlgoID, order.ClientAlgoID, err)
		}
		if t.Logger != nil {
			t.Logger.Info("binance reverse algo order canceled", "symbol", order.Symbol, "algo_id", order.AlgoID, "client_algo_id", order.ClientAlgoID)
		}
	}
	return nil
}

func (t Trader) closeBinanceReversePosition(ctx context.Context, client Client, position Position, filters TradingFilters) error {
	req, err := binanceMarketCloseRequest(position, filters)
	if err != nil {
		return err
	}
	reqs, err := splitBinancePlaceOrderRequest(req, filters)
	if err != nil {
		return err
	}
	for i, part := range reqs {
		if _, err := client.PlaceOrder(ctx, part); err != nil {
			return fmt.Errorf("binance close reverse position %s %s part %d/%d: %w", position.Symbol, position.PositionSide, i+1, len(reqs), err)
		}
		if t.Logger != nil {
			t.Logger.Info("binance reverse position close submitted", "symbol", position.Symbol, "position_side", position.PositionSide, "quantity", part.Quantity)
		}
	}
	if err := waitBinancePositionClosed(ctx, client, position); err != nil {
		return err
	}
	return nil
}

func waitBinancePositionClosed(ctx context.Context, client Client, position Position) error {
	waitCtx, cancel := context.WithTimeout(ctx, directionSwitchCloseTimeout)
	defer cancel()
	for {
		positions, err := client.Positions(waitCtx, position.Symbol)
		if err != nil {
			return fmt.Errorf("binance poll reverse position close: %w", err)
		}
		if !binanceHasMatchingOpenPosition(positions, position) {
			return nil
		}
		timer := time.NewTimer(directionSwitchPollInterval)
		select {
		case <-waitCtx.Done():
			timer.Stop()
			return fmt.Errorf("binance reverse position close not confirmed for %s %s: %w", position.Symbol, position.PositionSide, waitCtx.Err())
		case <-timer.C:
		}
	}
}

func binanceMarketCloseRequest(position Position, filters TradingFilters) (PlaceOrderRequest, error) {
	side, err := binanceCloseSide(position)
	if err != nil {
		return PlaceOrderRequest{}, err
	}
	size := binanceAbsoluteSize(position.PositionAmt, filters.StepSizeForOrderType("MARKET"))
	if size == "" || size == "0" {
		return PlaceOrderRequest{}, fmt.Errorf("binance position is not open: %s %s", position.Symbol, position.PositionSide)
	}
	req := PlaceOrderRequest{
		Symbol:           strings.ToUpper(strings.TrimSpace(position.Symbol)),
		Side:             side,
		Type:             "MARKET",
		Quantity:         size,
		NewClientOrderID: directionSwitchClOrdID(),
		PositionSide:     binanceClosePositionSide(position.PositionSide),
	}
	if req.PositionSide == "" || req.PositionSide == "BOTH" {
		req.ReduceOnly = true
	}
	return req, nil
}

func binancePositionsForDirection(positions []Position, symbol string, direction trading.Side) []Position {
	out := make([]Position, 0, len(positions))
	for _, position := range positions {
		got, ok := binancePositionDirection(position)
		if ok && got == direction && strings.EqualFold(position.Symbol, symbol) {
			out = append(out, position)
		}
	}
	return out
}

func binanceOpenOrdersForDirection(orders []OpenOrder, symbol string, direction trading.Side) []OpenOrder {
	out := make([]OpenOrder, 0, len(orders))
	for _, order := range orders {
		got, ok := binanceOpenOrderDirection(order)
		if ok && got == direction && strings.EqualFold(order.Symbol, symbol) {
			out = append(out, order)
		}
	}
	return out
}

func binanceHasMatchingOpenPosition(positions []Position, want Position) bool {
	wantSide := normalizeBinancePositionSide(want.PositionSide)
	wantDirection, directionOK := binancePositionDirection(want)
	for _, position := range positions {
		if !strings.EqualFold(position.Symbol, want.Symbol) {
			continue
		}
		if wantSide != "" {
			if normalizeBinancePositionSide(position.PositionSide) == wantSide && binancePositionOpen(position.PositionAmt) {
				return true
			}
			continue
		}
		gotDirection, ok := binancePositionDirection(position)
		if directionOK && ok && gotDirection == wantDirection {
			return true
		}
	}
	return false
}

func binancePositionDirection(position Position) (trading.Side, bool) {
	if !binancePositionOpen(position.PositionAmt) {
		return "", false
	}
	switch normalizeBinancePositionSide(position.PositionSide) {
	case "LONG":
		return trading.ActionLong, true
	case "SHORT":
		return trading.ActionShort, true
	}
	value, err := strconv.ParseFloat(strings.TrimSpace(position.PositionAmt), 64)
	if err == nil {
		if value > 0 {
			return trading.ActionLong, true
		}
		if value < 0 {
			return trading.ActionShort, true
		}
		return "", false
	}
	if strings.HasPrefix(strings.TrimSpace(position.PositionAmt), "-") {
		return trading.ActionShort, true
	}
	return trading.ActionLong, true
}

func binanceOpenOrderDirection(order OpenOrder) (trading.Side, bool) {
	if order.ReduceOnly || order.ClosePosition {
		return binanceReducedDirection(order.Side, order.PositionSide)
	}
	if direction, ok := sideFromBinancePositionSide(order.PositionSide); ok {
		return direction, true
	}
	return binanceSideToOpenDirection(order.Side)
}

func binanceAlgoOrderDirection(order AlgoOpenOrder) (trading.Side, bool) {
	if direction, ok := sideFromBinancePositionSide(order.PositionSide); ok {
		return direction, true
	}
	return binanceReducedDirection(order.Side, order.PositionSide)
}

func binanceCloseSide(position Position) (string, error) {
	switch normalizeBinancePositionSide(position.PositionSide) {
	case "LONG":
		return "SELL", nil
	case "SHORT":
		return "BUY", nil
	}
	value, err := strconv.ParseFloat(strings.TrimSpace(position.PositionAmt), 64)
	if err == nil {
		if value > 0 {
			return "SELL", nil
		}
		if value < 0 {
			return "BUY", nil
		}
	}
	if strings.HasPrefix(strings.TrimSpace(position.PositionAmt), "-") {
		return "BUY", nil
	}
	return "", fmt.Errorf("binance position is not open: %s %s", position.Symbol, position.PositionSide)
}

func binanceClosePositionSide(posSide string) string {
	switch normalizeBinancePositionSide(posSide) {
	case "LONG":
		return "LONG"
	case "SHORT":
		return "SHORT"
	default:
		return ""
	}
}

func binanceReducedDirection(side, posSide string) (trading.Side, bool) {
	if direction, ok := sideFromBinancePositionSide(posSide); ok {
		return direction, true
	}
	switch strings.ToUpper(strings.TrimSpace(side)) {
	case "SELL":
		return trading.ActionLong, true
	case "BUY":
		return trading.ActionShort, true
	default:
		return "", false
	}
}

func sideFromBinancePositionSide(posSide string) (trading.Side, bool) {
	switch normalizeBinancePositionSide(posSide) {
	case "LONG":
		return trading.ActionLong, true
	case "SHORT":
		return trading.ActionShort, true
	default:
		return "", false
	}
}

func binanceSideToOpenDirection(side string) (trading.Side, bool) {
	switch strings.ToUpper(strings.TrimSpace(side)) {
	case "BUY":
		return trading.ActionLong, true
	case "SELL":
		return trading.ActionShort, true
	default:
		return "", false
	}
}

func binancePositionOpen(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return strings.Trim(raw, "-0. ") != ""
	}
	return math.Abs(value) > 1e-12
}

func normalizeBinancePositionSide(raw string) string {
	raw = strings.ToUpper(strings.TrimSpace(raw))
	if raw == "BOTH" {
		return ""
	}
	return raw
}

func binanceAbsoluteSize(raw string, stepSize float64) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return strings.TrimPrefix(raw, "-")
	}
	return formatStep(math.Abs(value), stepSize, false)
}

func oppositeAction(action trading.Side) (trading.Side, bool) {
	switch action {
	case trading.ActionLong:
		return trading.ActionShort, true
	case trading.ActionShort:
		return trading.ActionLong, true
	default:
		return "", false
	}
}

func directionSwitchClOrdID() string {
	seq := atomic.AddUint64(&directionSwitchCloseSeq, 1) % 1000000
	return trimClientID(strings.ToUpper(fmt.Sprintf("DS%d%06d", time.Now().UTC().UnixMilli(), seq)), 32)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
