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
)

const valueAreaOrderTimezone = "Asia/Shanghai"

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
	return signal.PositionEffect == tvbottrading.PositionEffectOpen &&
		strings.EqualFold(strings.TrimSpace(signal.Condition), "value_area_signal")
}

func valueAreaOpenTimeRejection(signal tvbottrading.Signal) string {
	sentAt, err := tvbottrading.ParseTradingViewTime(signal.SentAt)
	if err != nil {
		// Validate has already checked sent_at before this guard runs. Keep the
		// fallback closed if a future caller reaches the helper directly.
		return "value_area_time_window"
	}
	location, err := time.LoadLocation(valueAreaOrderTimezone)
	if err != nil {
		return "value_area_time_window"
	}
	local := sentAt.In(location)
	minutes := local.Hour()*60 + local.Minute()
	if minutes < 20*60 || minutes >= 24*60 {
		return "value_area_time_window"
	}
	minute := local.Minute()
	if minute >= 55 || minute <= 5 || minute >= 25 && minute <= 35 {
		return "value_area_minute_block"
	}
	return ""
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
	symbols := valueAreaSymbolVariants(signal.Coinpair, signal.Ticker)
	records, err := s.Orders.ListActiveOpeningOrders(tvbotstorage.ActiveOpeningOrderFilter{
		TargetExchange: signal.TargetExchange,
		TradeEnv:       signal.TradeEnv,
		APIIDs:         []string{signal.APIID},
		Symbols:        symbols,
	})
	if err != nil {
		return false, err
	}
	for _, record := range records {
		if orderRecordSignalKind(record) != "value_area" {
			return true, nil
		}
	}

	var (
		apiID string
		state valueAreaExchangeState
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

	apiIDs := []string{signal.APIID}
	if apiID != "" {
		apiIDs = append(apiIDs, apiID)
	}
	records, err = s.Orders.ListActiveOpeningOrders(tvbotstorage.ActiveOpeningOrderFilter{
		TargetExchange: signal.TargetExchange,
		TradeEnv:       signal.TradeEnv,
		APIIDs:         apiIDs,
		Symbols:        symbols,
	})
	if err != nil {
		return false, err
	}
	for _, record := range records {
		if orderRecordSignalKind(record) != "value_area" {
			return true, nil
		}
	}

	// A live position or exchange-side entry order without a matching local
	// value-area record is intentionally treated as unknown ownership. This is
	// safer than allowing a new order to collide with a manually placed or
	// externally created position.
	if (state.OpenPosition || state.OpenEntryOrder) && len(records) == 0 {
		return true, nil
	}
	return false, nil
}

type valueAreaExchangeState struct {
	OpenPosition   bool
	OpenEntryOrder bool
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
			break
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
			break
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
			break
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
