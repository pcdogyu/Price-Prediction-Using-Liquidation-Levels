package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
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
	valuearea "github.com/pcdogyu/price-prediction-liquidation-levels/internal/valuearea"
)

const (
	valueAreaOrderTimezone           = "Asia/Shanghai"
	valueAreaConflictingStrategyCode = "value_area_conflicting_strategy"
)

// A local accepted/submitted record protects the short interval in which an
// exchange order may not be visible yet. Once that interval has elapsed, the
// exchange-side position and open-order state is authoritative; otherwise a
// historical record could permanently prevent a new value-area entry after
// the position was closed or the order was cancelled.
const valueAreaLocalConflictFreshness = 15 * time.Minute

var valueAreaOrderLocks = newKeyedMutexRegistry()

type keyedMutexRegistry struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func newKeyedMutexRegistry() *keyedMutexRegistry {
	return &keyedMutexRegistry{locks: make(map[string]*sync.Mutex)}
}

func (r *keyedMutexRegistry) lock(key string) func() {
	r.mu.Lock()
	mutex := r.locks[key]
	if mutex == nil {
		mutex = &sync.Mutex{}
		r.locks[key] = mutex
	}
	r.mu.Unlock()
	mutex.Lock()
	return mutex.Unlock
}

func isAutomaticValueAreaOpenSignal(signal tvbottrading.Signal) bool {
	return tvbottrading.IsValueAreaOpeningSignal(signal) &&
		strings.EqualFold(strings.TrimSpace(signal.Condition), "value_area_signal")
}

func valueAreaOpenTimeRejection(signal tvbottrading.Signal) string {
	sentAt, err := tvbottrading.ParseTradingViewTime(signal.SentAt)
	if err != nil {
		// Validate has already checked sent_at before this guard runs. Keep the
		// fallback closed if a future caller reaches the helper directly.
		return "value_area_time_window"
	}
	window, ok := valueAreaWindowFromSignal(signal)
	if !ok || !window.Contains(sentAt) {
		return "value_area_time_window"
	}
	return ""
}

func valueAreaWindowFromSignal(signal tvbottrading.Signal) (valuearea.TradingWindow, bool) {
	if signal.ValueAreaStartMinute == nil && signal.ValueAreaEndMinute == nil {
		return valuearea.DefaultTradingWindow(), true
	}
	if signal.ValueAreaStartMinute == nil || signal.ValueAreaEndMinute == nil {
		return valuearea.TradingWindow{}, false
	}
	window := valuearea.TradingWindow{
		StartMinute: *signal.ValueAreaStartMinute,
		EndMinute:   *signal.ValueAreaEndMinute,
	}
	return window, window.Valid()
}

func valueAreaOrderLockKey(signal tvbottrading.Signal) string {
	return strings.Join([]string{
		strings.TrimSpace(signal.TenantID),
		tvbottrading.NormalizeExchange(signal.TargetExchange),
		tvbottrading.NormalizeTradeEnv(signal.TradeEnv),
		strings.TrimSpace(signal.APIID),
		strings.Join(valueAreaSymbolVariants(signal.Coinpair, signal.Ticker), ","),
	}, "|")
}

func (s *Server) valueAreaOpenConflict(ctx context.Context, signal tvbottrading.Signal, cfg tvbotconfig.Config) (bool, error) {
	if s.valueAreaOpenConflictChecker != nil {
		return s.valueAreaOpenConflictChecker(ctx, signal, cfg)
	}
	return s.defaultValueAreaOpenConflict(ctx, signal, cfg)
}

func (s *Server) defaultValueAreaOpenConflict(ctx context.Context, signal tvbottrading.Signal, cfg tvbotconfig.Config) (bool, error) {
	if s.Orders == nil {
		return false, errors.New("order store is not configured")
	}

	var (
		apiID string
		state valueAreaExchangeState
		err   error
	)
	switch tvbottrading.NormalizeExchange(signal.TargetExchange) {
	case tvbottrading.ExchangeOKX:
		client, resolvedAPIID, clientErr := s.okxClientForCredentials(cfg, signal.APIID)
		if clientErr != nil {
			return false, clientErr
		}
		apiID = resolvedAPIID
		state, err = valueAreaOKXExchangeState(ctx, client, signal)
	case tvbottrading.ExchangeBinance:
		client, resolvedAPIID, clientErr := s.binanceClientForCredentials(cfg, signal.APIID)
		if clientErr != nil {
			return false, clientErr
		}
		apiID = resolvedAPIID
		state, err = valueAreaBinanceExchangeState(ctx, client, signal)
	case tvbottrading.ExchangeGate:
		client, resolvedAPIID, clientErr := s.gateClientForCredentials(cfg, signal.APIID)
		if clientErr != nil {
			return false, clientErr
		}
		apiID = resolvedAPIID
		state, err = valueAreaGateExchangeState(ctx, client, signal)
	default:
		return false, fmt.Errorf("unsupported target_exchange %q", signal.TargetExchange)
	}
	if err != nil {
		return false, err
	}

	symbols := valueAreaSymbolVariants(signal.Coinpair, signal.Ticker)
	apiIDs := []string{signal.APIID}
	if apiID != "" {
		apiIDs = append(apiIDs, apiID)
	}
	records, err := s.Orders.ListActiveOpeningOrders(tvbotstorage.ActiveOpeningOrderFilter{
		TargetExchange: signal.TargetExchange,
		TradeEnv:       signal.TradeEnv,
		APIIDs:         apiIDs,
		Symbols:        symbols,
	})
	if err != nil {
		return false, err
	}
	return valueAreaLocalOpeningConflictForSignal(records, state, s.now(), signal), nil
}

func valueAreaLocalOpeningConflict(records []tvbotstorage.OrderRecord, state valueAreaExchangeState, now time.Time) bool {
	return valueAreaLocalOpeningConflictForSignal(records, state, now, tvbottrading.Signal{})
}

func valueAreaLocalOpeningConflictForSignal(records []tvbotstorage.OrderRecord, state valueAreaExchangeState, now time.Time, signal tvbottrading.Signal) bool {
	freshRecord := false
	for _, record := range records {
		if !valueAreaLocalOpeningRecordFresh(record, now) {
			continue
		}
		freshRecord = true
	}

	// A value-area reverse opening is allowed only when the exchange confirms
	// exactly one live position on the opposite side and there are no pending
	// entry orders. The executor will close that position and wait for the
	// exchange to report flat before submitting the new direction.
	if valueAreaImmediateReverse(signal, state) {
		return false
	}

	// Same-direction positions, unknown position direction, and any pending
	// entry order remain conflicts even when a local record is recent. A recent
	// local value-area record only permits another request when the exchange is
	// otherwise flat, preserving the existing short-lived duplicate protection.
	if state.OpenPosition || state.OpenEntryOrder {
		return true
	}
	if freshRecord {
		for _, record := range records {
			if valueAreaLocalOpeningRecordFresh(record, now) && orderRecordSignalKind(record) != "value_area" {
				return true
			}
		}
	}
	return false
}

func valueAreaImmediateReverse(signal tvbottrading.Signal, state valueAreaExchangeState) bool {
	if !tvbottrading.IsValueAreaOpeningSignal(signal) || !state.OpenPosition || state.OpenEntryOrder || len(state.OpenPositionSides) != 1 {
		return false
	}
	target := normalizeValueAreaSide(signal.PositionSide)
	if target == "" {
		return false
	}
	return state.OpenPositionSides[0] == oppositeValueAreaSide(target)
}

func normalizeValueAreaSide(raw string) tvbottrading.Side {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case tvbottrading.PositionSideLong:
		return tvbottrading.ActionLong
	case tvbottrading.PositionSideShort:
		return tvbottrading.ActionShort
	default:
		return ""
	}
}

func oppositeValueAreaSide(side tvbottrading.Side) tvbottrading.Side {
	switch side {
	case tvbottrading.ActionLong:
		return tvbottrading.ActionShort
	case tvbottrading.ActionShort:
		return tvbottrading.ActionLong
	default:
		return ""
	}
}

func valueAreaLocalOpeningRecordFresh(record tvbotstorage.OrderRecord, now time.Time) bool {
	updatedAt := record.UpdatedAt
	if updatedAt.IsZero() || (!record.AcceptedAt.IsZero() && record.AcceptedAt.After(updatedAt)) {
		updatedAt = record.AcceptedAt
	}
	if updatedAt.IsZero() {
		// Legacy records without timestamps are unknown, so keep the guard
		// fail-closed instead of silently allowing a duplicate entry.
		return true
	}
	return !updatedAt.Before(now.Add(-valueAreaLocalConflictFreshness))
}

type valueAreaExchangeState struct {
	OpenPosition      bool
	OpenEntryOrder    bool
	OpenPositionSides []tvbottrading.Side
}

func appendValueAreaPositionSide(sides *[]tvbottrading.Side, side tvbottrading.Side) {
	if side != tvbottrading.ActionLong && side != tvbottrading.ActionShort {
		return
	}
	for _, existing := range *sides {
		if existing == side {
			return
		}
	}
	*sides = append(*sides, side)
}

func valueAreaSymbolVariants(values ...string) []string {
	seen := make(map[string]struct{})
	var out []string
	add := func(value string) {
		value = strings.TrimSpace(value)
		if i := strings.LastIndex(value, ":"); i >= 0 {
			value = value[i+1:]
		}
		value = strings.ToUpper(value)
		if value == "" {
			return
		}
		for _, candidate := range []string{
			value,
			strings.NewReplacer("-", "", "_", "", "/", "", " ", "").Replace(value),
		} {
			if candidate != "" {
				if _, ok := seen[candidate]; !ok {
					seen[candidate] = struct{}{}
					out = append(out, candidate)
				}
			}
		}
	}
	for _, value := range values {
		add(value)
	}
	for _, value := range append([]string(nil), out...) {
		for _, quote := range []string{"USDT", "USDC", "BUSD", "USD"} {
			if strings.HasSuffix(value, quote) && len(value) > len(quote) {
				add(strings.TrimSuffix(value, quote))
			}
		}
		if strings.HasSuffix(value, "SWAP") && len(value) > len("SWAP") {
			add(strings.TrimSuffix(value, "SWAP"))
		}
	}
	return out
}

func valueAreaOKXExchangeState(ctx context.Context, client tvbotokx.Client, signal tvbottrading.Signal) (valueAreaExchangeState, error) {
	instID, _, err := tvbotokx.DeriveSwapInstrumentID(signal.Coinpair, signal.Ticker)
	if err != nil {
		return valueAreaExchangeState{}, err
	}
	positions, _, err := client.Positions(ctx, "SWAP")
	if err != nil {
		return valueAreaExchangeState{}, err
	}
	state := valueAreaExchangeState{}
	for _, position := range positions {
		if strings.EqualFold(strings.TrimSpace(position.InstID), instID) && isOpenPosition(position.Pos) {
			state.OpenPosition = true
			if side, ok := valueAreaOKXPositionSide(position); ok {
				appendValueAreaPositionSide(&state.OpenPositionSides, side)
			}
		}
	}
	orders, _, err := client.PendingOrders(ctx, "SWAP")
	if err != nil {
		return valueAreaExchangeState{}, err
	}
	for _, order := range orders {
		if !strings.EqualFold(strings.TrimSpace(order.InstID), instID) || jsonTruthy(order.ReduceOnly) || jsonTruthy(order.ClosePosition) {
			continue
		}
		state.OpenEntryOrder = true
		break
	}
	return state, nil
}

func valueAreaBinanceExchangeState(ctx context.Context, client tvbotbinance.Client, signal tvbottrading.Signal) (valueAreaExchangeState, error) {
	symbol, err := tvbotbinance.DeriveUSDMSymbol(signal.Coinpair, signal.Ticker)
	if err != nil {
		return valueAreaExchangeState{}, err
	}
	positions, err := client.Positions(ctx, symbol)
	if err != nil {
		return valueAreaExchangeState{}, err
	}
	state := valueAreaExchangeState{}
	for _, position := range positions {
		converted := binancePositionToOKX(position)
		if strings.EqualFold(strings.TrimSpace(position.Symbol), symbol) && isOpenPosition(converted.Pos) {
			state.OpenPosition = true
			if side, ok := valueAreaBinancePositionSide(position); ok {
				appendValueAreaPositionSide(&state.OpenPositionSides, side)
			}
		}
	}
	orders, err := client.OpenOrders(ctx, symbol)
	if err != nil {
		return valueAreaExchangeState{}, err
	}
	for _, order := range orders {
		if !strings.EqualFold(strings.TrimSpace(order.Symbol), symbol) || order.ReduceOnly || order.ClosePosition {
			continue
		}
		state.OpenEntryOrder = true
		break
	}
	return state, nil
}

func valueAreaGateExchangeState(ctx context.Context, client tvbotgate.Client, signal tvbottrading.Signal) (valueAreaExchangeState, error) {
	contract, err := tvbotgate.DeriveContract(signal)
	if err != nil {
		return valueAreaExchangeState{}, err
	}
	rawPositions, err := client.Positions(ctx, contract)
	if err != nil {
		return valueAreaExchangeState{}, err
	}
	state := valueAreaExchangeState{}
	for _, position := range rawPositions {
		size, parseErr := strconv.ParseFloat(strings.TrimSpace(position.Size), 64)
		if strings.EqualFold(strings.TrimSpace(position.Contract), contract) && parseErr == nil && size != 0 && !math.IsNaN(size) && !math.IsInf(size, 0) {
			state.OpenPosition = true
			if size > 0 {
				appendValueAreaPositionSide(&state.OpenPositionSides, tvbottrading.ActionLong)
			} else {
				appendValueAreaPositionSide(&state.OpenPositionSides, tvbottrading.ActionShort)
			}
		}
	}
	orders, err := client.OpenOrders(ctx, contract)
	if err != nil {
		return valueAreaExchangeState{}, err
	}
	for _, order := range orders {
		if !strings.EqualFold(strings.TrimSpace(order.Contract), contract) || order.IsReduceOnly || order.IsClose {
			continue
		}
		state.OpenEntryOrder = true
		break
	}
	return state, nil
}

func valueAreaBinancePositionSide(position tvbotbinance.Position) (tvbottrading.Side, bool) {
	switch strings.ToUpper(strings.TrimSpace(position.PositionSide)) {
	case "LONG":
		return tvbottrading.ActionLong, true
	case "SHORT":
		return tvbottrading.ActionShort, true
	}
	value, err := strconv.ParseFloat(strings.TrimSpace(position.PositionAmt), 64)
	if err == nil {
		switch {
		case value > 0:
			return tvbottrading.ActionLong, true
		case value < 0:
			return tvbottrading.ActionShort, true
		}
	}
	raw := strings.TrimSpace(position.PositionAmt)
	if strings.HasPrefix(raw, "-") {
		return tvbottrading.ActionShort, true
	}
	if raw != "" {
		return tvbottrading.ActionLong, true
	}
	return "", false
}

func valueAreaOKXPositionSide(position tvbotokx.Position) (tvbottrading.Side, bool) {
	switch strings.ToLower(strings.TrimSpace(position.PosSide)) {
	case "long":
		return tvbottrading.ActionLong, true
	case "short":
		return tvbottrading.ActionShort, true
	}
	value, err := strconv.ParseFloat(strings.TrimSpace(position.Pos), 64)
	if err == nil {
		switch {
		case value > 0:
			return tvbottrading.ActionLong, true
		case value < 0:
			return tvbottrading.ActionShort, true
		}
	}
	raw := strings.TrimSpace(position.Pos)
	if strings.HasPrefix(raw, "-") {
		return tvbottrading.ActionShort, true
	}
	if raw != "" {
		return tvbottrading.ActionLong, true
	}
	return "", false
}

func jsonTruthy(raw json.RawMessage) bool {
	var value bool
	if json.Unmarshal(raw, &value) == nil {
		return value
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		switch strings.ToLower(strings.TrimSpace(text)) {
		case "true", "1", "yes":
			return true
		}
	}
	return false
}
