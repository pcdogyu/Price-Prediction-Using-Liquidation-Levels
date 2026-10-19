package okx

import (
	"context"
	"encoding/json"
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
	positions     []Position
	pendingOrders []PendingOrder
}

func (t Trader) prepareDirectionSwitch(ctx context.Context, client Client, cfg trading.RuntimeConfig, action trading.Side, instID string) (directionSwitchState, error) {
	var state directionSwitchState
	opposite, ok := oppositeAction(action)
	if !ok {
		return state, nil
	}
	instID = strings.ToUpper(strings.TrimSpace(instID))
	positions, _, err := client.Positions(ctx, "SWAP")
	if err != nil {
		return state, fmt.Errorf("okx query positions before direction switch: %w", err)
	}
	state.positions = positions
	pendingOrders, _, err := client.PendingOrders(ctx, "SWAP")
	if err != nil {
		return state, fmt.Errorf("okx query pending orders before direction switch: %w", err)
	}
	state.pendingOrders = pendingOrders
	oppositeOrders := okxPendingOrdersForDirection(pendingOrders, instID, opposite)
	oppositePositions := okxPositionsForDirection(positions, instID, opposite)
	if len(oppositeOrders) == 0 && len(oppositePositions) == 0 {
		return state, nil
	}
	if err := t.cancelOKXPendingOrders(ctx, client, oppositeOrders); err != nil {
		return state, err
	}
	if err := t.cancelOKXAlgoOrdersForDirection(ctx, client, instID, opposite); err != nil {
		return state, err
	}

	positions, _, err = client.Positions(ctx, "SWAP")
	if err != nil {
		return state, fmt.Errorf("okx refresh positions after canceling reverse orders: %w", err)
	}
	state.positions = positions
	for _, position := range okxPositionsForDirection(positions, instID, opposite) {
		if err := t.closeOKXReversePosition(ctx, client, cfg, position); err != nil {
			return state, err
		}
	}
	positions, _, err = client.Positions(ctx, "SWAP")
	if err != nil {
		return state, fmt.Errorf("okx refresh positions after closing reverse positions: %w", err)
	}
	state.positions = positions
	return state, nil
}

func (t Trader) cancelOKXPendingOrders(ctx context.Context, client Client, orders []PendingOrder) error {
	for _, order := range orders {
		req := CancelOrderRequest{InstID: strings.ToUpper(strings.TrimSpace(order.InstID))}
		if strings.TrimSpace(order.OrdID) != "" {
			req.OrdID = strings.TrimSpace(order.OrdID)
		} else if strings.TrimSpace(order.ClOrdID) != "" {
			req.ClOrdID = strings.TrimSpace(order.ClOrdID)
		} else {
			continue
		}
		if _, _, err := client.CancelOrder(ctx, req); err != nil {
			return fmt.Errorf("okx cancel reverse pending order %s %s: %w", req.InstID, firstNonEmpty(req.OrdID, req.ClOrdID), err)
		}
		if t.Logger != nil {
			t.Logger.Info("okx reverse pending order canceled", "inst_id", req.InstID, "ord_id", req.OrdID, "cl_ord_id", req.ClOrdID)
		}
	}
	return nil
}

func (t Trader) cancelOKXAlgoOrdersForDirection(ctx context.Context, client Client, instID string, direction trading.Side) error {
	orders, _, err := client.PendingAlgoOrders(ctx, "SWAP", instID)
	if err != nil {
		return fmt.Errorf("okx query pending algo orders before direction switch: %w", err)
	}
	reqs := make([]CancelAlgoOrderRequest, 0, len(orders))
	for _, order := range orders {
		orderDirection, ok := okxAlgoOrderDirection(order)
		if !ok || orderDirection != direction || !strings.EqualFold(order.InstID, instID) {
			continue
		}
		req := CancelAlgoOrderRequest{InstID: strings.ToUpper(strings.TrimSpace(order.InstID))}
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
			return fmt.Errorf("okx cancel reverse algo orders: %w", err)
		}
		if t.Logger != nil {
			t.Logger.Info("okx reverse algo orders canceled", "inst_id", instID, "count", n)
		}
		reqs = reqs[n:]
	}
	return nil
}

func (t Trader) closeOKXReversePosition(ctx context.Context, client Client, cfg trading.RuntimeConfig, position Position) error {
	req, err := okxMarketCloseRequest(cfg, position)
	if err != nil {
		return err
	}
	if _, _, err := client.PlaceOrder(ctx, req); err != nil {
		return fmt.Errorf("okx close reverse position %s %s: %w", position.InstID, position.PosSide, err)
	}
	if t.Logger != nil {
		t.Logger.Info("okx reverse position close submitted", "inst_id", position.InstID, "pos_side", position.PosSide, "size", req.Sz)
	}
	if err := waitOKXPositionClosed(ctx, client, position); err != nil {
		return err
	}
	return nil
}

func waitOKXPositionClosed(ctx context.Context, client Client, position Position) error {
	waitCtx, cancel := context.WithTimeout(ctx, directionSwitchCloseTimeout)
	defer cancel()
	for {
		positions, _, err := client.Positions(waitCtx, "SWAP")
		if err != nil {
			return fmt.Errorf("okx poll reverse position close: %w", err)
		}
		if !okxHasMatchingOpenPosition(positions, position) {
			return nil
		}
		timer := time.NewTimer(directionSwitchPollInterval)
		select {
		case <-waitCtx.Done():
			timer.Stop()
			return fmt.Errorf("okx reverse position close not confirmed for %s %s: %w", position.InstID, position.PosSide, waitCtx.Err())
		case <-timer.C:
		}
	}
}

func okxMarketCloseRequest(cfg trading.RuntimeConfig, position Position) (PlaceOrderRequest, error) {
	side, err := okxCloseSide(position)
	if err != nil {
		return PlaceOrderRequest{}, err
	}
	size := absoluteSize(position.Pos)
	if size == "" || size == "0" {
		return PlaceOrderRequest{}, fmt.Errorf("okx position is not open: %s %s", position.InstID, position.PosSide)
	}
	tdMode := strings.ToLower(strings.TrimSpace(position.MgnMode))
	if tdMode == "" {
		tdMode = cfg.MarginMode()
	}
	posSide := normalizeOKXPosSide(position.PosSide)
	req := PlaceOrderRequest{
		InstID:     strings.ToUpper(strings.TrimSpace(position.InstID)),
		TDMode:     tdMode,
		ClOrdID:    directionSwitchClOrdID(),
		Side:       side,
		OrdType:    "market",
		Sz:         size,
		ReduceOnly: posSide == "",
	}
	if posSide != "" {
		req.PosSide = posSide
	}
	return req, nil
}

func okxPositionsForDirection(positions []Position, instID string, direction trading.Side) []Position {
	out := make([]Position, 0, len(positions))
	for _, position := range positions {
		got, ok := okxPositionDirection(position)
		if ok && got == direction && strings.EqualFold(position.InstID, instID) {
			out = append(out, position)
		}
	}
	return out
}

func okxPendingOrdersForDirection(orders []PendingOrder, instID string, direction trading.Side) []PendingOrder {
	out := make([]PendingOrder, 0, len(orders))
	for _, order := range orders {
		got, ok := okxPendingOrderDirection(order)
		if ok && got == direction && strings.EqualFold(order.InstID, instID) {
			out = append(out, order)
		}
	}
	return out
}

func okxHasMatchingOpenPosition(positions []Position, want Position) bool {
	wantSide := normalizeOKXPosSide(want.PosSide)
	wantDirection, directionOK := okxPositionDirection(want)
	for _, position := range positions {
		if !strings.EqualFold(position.InstID, want.InstID) {
			continue
		}
		if wantSide != "" {
			if normalizeOKXPosSide(position.PosSide) == wantSide && okxPositionOpen(position.Pos) {
				return true
			}
			continue
		}
		gotDirection, ok := okxPositionDirection(position)
		if directionOK && ok && gotDirection == wantDirection {
			return true
		}
	}
	return false
}

func okxPositionDirection(position Position) (trading.Side, bool) {
	if !okxPositionOpen(position.Pos) {
		return "", false
	}
	switch normalizeOKXPosSide(position.PosSide) {
	case "long":
		return trading.ActionLong, true
	case "short":
		return trading.ActionShort, true
	}
	value, err := strconv.ParseFloat(strings.TrimSpace(position.Pos), 64)
	if err == nil {
		if value > 0 {
			return trading.ActionLong, true
		}
		if value < 0 {
			return trading.ActionShort, true
		}
		return "", false
	}
	if strings.HasPrefix(strings.TrimSpace(position.Pos), "-") {
		return trading.ActionShort, true
	}
	return trading.ActionLong, true
}

func okxPendingOrderDirection(order PendingOrder) (trading.Side, bool) {
	if rawJSONBool(order.ReduceOnly) {
		return okxReducedDirection(order.Side, order.PosSide)
	}
	if direction, ok := sideFromPosSide(order.PosSide); ok {
		return direction, true
	}
	return sideToOpenDirection(order.Side)
}

func okxAlgoOrderDirection(order AlgoOrder) (trading.Side, bool) {
	if direction, ok := sideFromPosSide(order.PosSide); ok {
		return direction, true
	}
	return okxReducedDirection(order.Side, order.PosSide)
}

func okxCloseSide(position Position) (string, error) {
	switch normalizeOKXPosSide(position.PosSide) {
	case "long":
		return "sell", nil
	case "short":
		return "buy", nil
	}
	value, err := strconv.ParseFloat(strings.TrimSpace(position.Pos), 64)
	if err == nil {
		if value > 0 {
			return "sell", nil
		}
		if value < 0 {
			return "buy", nil
		}
	}
	if strings.HasPrefix(strings.TrimSpace(position.Pos), "-") {
		return "buy", nil
	}
	return "", fmt.Errorf("okx position is not open: %s %s", position.InstID, position.PosSide)
}

func okxReducedDirection(side, posSide string) (trading.Side, bool) {
	if direction, ok := sideFromPosSide(posSide); ok {
		return direction, true
	}
	switch strings.ToLower(strings.TrimSpace(side)) {
	case "sell":
		return trading.ActionLong, true
	case "buy":
		return trading.ActionShort, true
	default:
		return "", false
	}
}

func sideFromPosSide(posSide string) (trading.Side, bool) {
	switch normalizeOKXPosSide(posSide) {
	case "long":
		return trading.ActionLong, true
	case "short":
		return trading.ActionShort, true
	default:
		return "", false
	}
}

func sideToOpenDirection(side string) (trading.Side, bool) {
	switch strings.ToLower(strings.TrimSpace(side)) {
	case "buy":
		return trading.ActionLong, true
	case "sell":
		return trading.ActionShort, true
	default:
		return "", false
	}
}

func okxPositionOpen(raw string) bool {
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

func normalizeOKXPosSide(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "net" || raw == "both" {
		return ""
	}
	return raw
}

func absoluteSize(raw string) string {
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

func rawJSONBool(raw json.RawMessage) bool {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 {
		return false
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		return b
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strings.EqualFold(strings.TrimSpace(s), "true")
	}
	return strings.EqualFold(strings.TrimSpace(string(raw)), "true")
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
	return fmt.Sprintf("DS%d%06d", time.Now().UTC().UnixMilli(), seq)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func trimDecimalZeros(raw string) string {
	raw = strings.TrimSpace(raw)
	if strings.Contains(raw, ".") {
		raw = strings.TrimRight(strings.TrimRight(raw, "0"), ".")
	}
	if raw == "" || raw == "-0" {
		return "0"
	}
	return raw
}
