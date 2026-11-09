package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

type admissionPosition struct {
	Symbol   string
	Side     tvbottrading.Side
	AvgPrice float64
}

type tradeAccountSnapshot struct {
	APIID          string
	Equity         float64
	EquityErr      error
	Positions      []admissionPosition
	PendingSymbols map[string]struct{}
}

type entryAdmission struct {
	Signal         tvbottrading.Signal
	IgnoredCode    string
	IgnoredMessage string
}

var orderAdmissionLocks = struct {
	sync.Mutex
	items map[string]*sync.Mutex
}{items: map[string]*sync.Mutex{}}

func acquireOrderAdmissionLock(key string) func() {
	orderAdmissionLocks.Lock()
	lock := orderAdmissionLocks.items[key]
	if lock == nil {
		lock = &sync.Mutex{}
		orderAdmissionLocks.items[key] = lock
	}
	orderAdmissionLocks.Unlock()
	lock.Lock()
	return lock.Unlock
}

func (s *Server) prepareEntryAdmission(ctx context.Context, signal tvbottrading.Signal, cfg tvbotconfig.Config) (entryAdmission, error) {
	settings := cfg.OrderSettings().Normalize()
	result := entryAdmission{Signal: signal}
	result.Signal.PyramidEnabled = settings.Pyramid.Enabled
	needsAccount := settings.AmountMode == tvbottrading.OrderAmountModeEquityPct ||
		settings.MaxOpenCoinpairs > 0 || settings.Pyramid.Enabled
	if !needsAccount {
		settings.ApplyToSignal(&result.Signal)
		return result, nil
	}

	needBalance := settings.AmountMode == tvbottrading.OrderAmountModeEquityPct || settings.Pyramid.Enabled
	needPositions := settings.MaxOpenCoinpairs > 0 || settings.Pyramid.Enabled
	snapshot, err := s.loadTradeAccountSnapshot(ctx, cfg, signal, needBalance, needPositions, needPositions)
	if err != nil {
		return entryAdmission{}, err
	}
	if snapshot.APIID != "" {
		result.Signal.APIID = snapshot.APIID
	}
	settings.ApplyToSignal(&result.Signal)

	if settings.AmountMode == tvbottrading.OrderAmountModeEquityPct {
		amount, _ := tvbottrading.ResolveOrderAmount(settings.AmountMode, settings.Amount.Value, settings.AmountPct, snapshot.Equity)
		if amount > 0 {
			result.Signal.Amount = tvbottrading.NewFlexibleFloat(amount)
		}
	}

	acceptedSymbols, err := s.activeAcceptedEntrySymbols(result.Signal, snapshot.APIID)
	if err != nil {
		return entryAdmission{}, err
	}
	for symbol := range acceptedSymbols {
		snapshot.PendingSymbols[symbol] = struct{}{}
	}

	symbol := admissionSignalSymbol(result.Signal)
	if symbol == "" {
		return entryAdmission{}, errors.New("cannot determine normalized trading symbol for entry admission")
	}
	if settings.MaxOpenCoinpairs > 0 {
		occupied := make(map[string]struct{}, len(snapshot.Positions)+len(snapshot.PendingSymbols))
		for _, position := range snapshot.Positions {
			if position.Symbol != "" {
				occupied[position.Symbol] = struct{}{}
			}
		}
		for pending := range snapshot.PendingSymbols {
			occupied[pending] = struct{}{}
		}
		if _, alreadyOccupied := occupied[symbol]; !alreadyOccupied && len(occupied) >= settings.MaxOpenCoinpairs {
			result.IgnoredCode = "max_open_coinpairs"
			result.IgnoredMessage = fmt.Sprintf("maximum open coinpairs reached: %d", settings.MaxOpenCoinpairs)
			return result, nil
		}
	}

	if settings.Pyramid.Enabled {
		positionSide := pyramidPositionSide(result.Signal)
		key := tvbotstorage.PyramidStateKey{
			TargetExchange: result.Signal.TargetExchange,
			TradeEnv:       result.Signal.TradeEnv,
			APIID:          snapshot.APIID,
			Coinpair:       symbol,
			PositionSide:   string(positionSide),
		}
		state, stateFound, stateErr := s.Orders.GetPyramidState(key)
		if stateErr != nil {
			return entryAdmission{}, stateErr
		}
		position, hasPosition := snapshot.findPosition(symbol, positionSide)
		_, hasPending := snapshot.PendingSymbols[symbol]
		if !hasPosition {
			if stateFound && !hasPending {
				if err := s.Orders.DeletePyramidState(key); err != nil {
					return entryAdmission{}, err
				}
				stateFound = false
			}
			if hasPending {
				result.IgnoredCode = "pyramid_waiting_for_position"
				result.IgnoredMessage = "same-symbol opening order is pending; wait for a position before pyramiding"
				return result, nil
			}
			result.Signal.PyramidLevel = 0
		} else {
			if hasPending {
				result.IgnoredCode = "pyramid_order_pending"
				result.IgnoredMessage = "same-symbol opening order is already pending; wait for it to settle before pyramiding"
				return result, nil
			}
			if position.AvgPrice <= 0 {
				return entryAdmission{}, errors.New("pyramid entry requires a valid live position average price")
			}
			if !tvbottrading.FavorablePyramidPrice(result.Signal.Action, result.Signal.Price.Value, position.AvgPrice, settings.Pyramid.MinFavorableMovePct) {
				result.IgnoredCode = "pyramid_price_not_favorable"
				result.IgnoredMessage = fmt.Sprintf("signal price %s does not meet the %s pyramid move threshold from average price %s", tvbottrading.NormalizeFloat(result.Signal.Price.Value), string(positionSide), tvbottrading.NormalizeFloat(position.AvgPrice))
				return result, nil
			}
			level := 1
			if stateFound {
				level = state.Level + 1
			}
			if level > settings.Pyramid.MaxAdditionalEntries {
				result.IgnoredCode = "pyramid_max_entries"
				result.IgnoredMessage = fmt.Sprintf("maximum pyramid additional entries reached: %d", settings.Pyramid.MaxAdditionalEntries)
				return result, nil
			}
			if level <= 0 || level > len(settings.Pyramid.AddAmountPcts) {
				return entryAdmission{}, fmt.Errorf("pyramid amount percentage is missing for level %d", level)
			}
			amountPct := settings.Pyramid.AddAmountPcts[level-1]
			amount, _ := tvbottrading.ResolveOrderAmount(tvbottrading.OrderAmountModeEquityPct, settings.Amount.Value, amountPct, snapshot.Equity)
			if amount <= 0 {
				return entryAdmission{}, errors.New("pyramid entry amount is not positive")
			}
			result.Signal.Amount = tvbottrading.NewFlexibleFloat(amount)
			result.Signal.PyramidLevel = level
		}
	}
	return result, nil
}

func (s *Server) resetPyramidStateAfterClose(ctx context.Context, signal tvbottrading.Signal, result tvbottrading.OrderResult, cfg tvbotconfig.Config) {
	if s.Orders == nil || !cfg.Trading.PyramidOrder.Enabled {
		return
	}
	probe := signal
	if strings.TrimSpace(result.TargetExchange) != "" {
		probe.TargetExchange = result.TargetExchange
	}
	if strings.TrimSpace(result.APIID) != "" {
		probe.APIID = result.APIID
	}
	snapshot, err := s.loadTradeAccountSnapshot(ctx, cfg, probe, false, true, true)
	if err != nil {
		if s.Logger != nil {
			s.Logger.Warn("pyramid state reset check failed", "exchange", probe.TargetExchange, "coinpair", probe.Coinpair, "error", err)
		}
		return
	}
	symbol := admissionSymbolKey(probe.TargetExchange, probe.Coinpair, probe.Ticker)
	if symbol == "" {
		symbol = admissionSymbolKey(probe.TargetExchange, result.InstID, "")
	}
	side := pyramidClosePositionSide(probe)
	if _, stillOpen := snapshot.findPosition(symbol, side); stillOpen {
		return
	}
	if _, hasPending := snapshot.PendingSymbols[symbol]; hasPending {
		return
	}
	if err := s.Orders.DeletePyramidState(tvbotstorage.PyramidStateKey{
		TargetExchange: probe.TargetExchange,
		TradeEnv:       probe.TradeEnv,
		APIID:          snapshot.APIID,
		Coinpair:       symbol,
		PositionSide:   string(side),
	}); err != nil && s.Logger != nil {
		s.Logger.Warn("failed to reset pyramid state after close", "exchange", probe.TargetExchange, "coinpair", symbol, "error", err)
	}
}

func (s *Server) activeAcceptedEntrySymbols(signal tvbottrading.Signal, apiID string) (map[string]struct{}, error) {
	result := make(map[string]struct{})
	if s.Orders == nil {
		return result, nil
	}
	records, err := s.Orders.ListActiveOpeningOrders(tvbotstorage.ActiveOpeningOrderFilter{
		TargetExchange: signal.TargetExchange,
		TradeEnv:       signal.TradeEnv,
		APIID:          apiID,
	})
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		if symbol := admissionSymbolKey(signal.TargetExchange, record.Coinpair, record.Ticker); symbol != "" {
			result[symbol] = struct{}{}
		}
	}
	return result, nil
}

func (s *Server) recordPyramidEntry(signal tvbottrading.Signal, result tvbottrading.OrderResult, nowTime func() time.Time) {
	if !signal.PyramidEnabled || s.Orders == nil || !result.HasSubmittedOrders() {
		return
	}
	key := tvbotstorage.PyramidState{
		TargetExchange: signal.TargetExchange,
		TradeEnv:       signal.TradeEnv,
		APIID:          signal.APIID,
		Coinpair:       admissionSignalSymbol(signal),
		PositionSide:   string(pyramidPositionSide(signal)),
		Level:          signal.PyramidLevel,
		LastEntryPrice: signal.Price.Value,
		UpdatedAt:      nowTime().UTC(),
	}
	if key.Coinpair == "" || key.APIID == "" {
		return
	}
	if err := s.Orders.UpsertPyramidState(key); err != nil && s.Logger != nil {
		s.Logger.Warn("failed to persist pyramid state", "exchange", key.TargetExchange, "api_id", key.APIID, "coinpair", key.Coinpair, "error", err)
	}
}

func (s *Server) loadTradeAccountSnapshot(ctx context.Context, cfg tvbotconfig.Config, signal tvbottrading.Signal, needBalance, needPositions, needPending bool) (tradeAccountSnapshot, error) {
	snapshot := tradeAccountSnapshot{PendingSymbols: map[string]struct{}{}}
	exchange := tvbottrading.NormalizeExchange(signal.TargetExchange)
	switch exchange {
	case tvbottrading.ExchangeOKX:
		store := s.okxCredentialStore(cfg.Trading.Env)
		if store == nil {
			return snapshot, errors.New("OKX credential store is not configured")
		}
		creds, apiID, err := store.OKXCredentials(signal.APIID)
		if err != nil {
			return snapshot, err
		}
		snapshot.APIID = apiID
		client := tvbotokx.Client{BaseURL: cfg.OKXBaseURL(), Credentials: creds, Demo: cfg.DemoTradingHeaderEnabled(), HTTPClient: s.okxHTTPClient()}
		if needBalance {
			balance, _, balanceErr := client.AccountBalanceSnapshot(ctx)
			if balanceErr != nil {
				snapshot.EquityErr = balanceErr
			} else {
				snapshot.Equity, snapshot.EquityErr = parseAdmissionPositiveFloat(balance.TotalEq, "OKX total equity")
			}
		}
		if needPositions {
			positions, _, positionErr := client.Positions(ctx, "SWAP")
			if positionErr != nil {
				return snapshot, positionErr
			}
			for _, position := range positions {
				if !isOpenPosition(position.Pos) {
					continue
				}
				symbol := admissionSymbolKey(exchange, position.InstID, "")
				if symbol == "" {
					continue
				}
				avg, _ := parseAdmissionPositiveFloat(position.AvgPx, "OKX average price")
				snapshot.Positions = append(snapshot.Positions, admissionPosition{Symbol: symbol, Side: okxPositionSide(position), AvgPrice: avg})
			}
		}
		if needPending {
			pending, _, pendingErr := client.PendingOrders(ctx, "SWAP")
			if pendingErr != nil {
				return snapshot, pendingErr
			}
			for _, order := range pending {
				if rawJSONBool(order.ReduceOnly) || rawJSONBool(order.ClosePosition) {
					continue
				}
				if symbol := admissionSymbolKey(exchange, order.InstID, ""); symbol != "" {
					snapshot.PendingSymbols[symbol] = struct{}{}
				}
			}
		}
	case tvbottrading.ExchangeBinance:
		store := s.binanceCredentialStore(cfg.Trading.Env)
		if store == nil {
			return snapshot, errors.New("Binance credential store is not configured")
		}
		creds, apiID, err := store.BinanceCredentials(signal.APIID)
		if err != nil {
			return snapshot, err
		}
		snapshot.APIID = apiID
		client := tvbotbinance.Client{BaseURL: cfg.BinanceBaseURL(), Credentials: creds, HTTPClient: s.binanceHTTPClient()}
		var balances []tvbotbinance.Balance
		var positions []tvbotbinance.Position
		if needBalance {
			balances, err = client.AccountBalance(ctx)
			if err != nil {
				snapshot.EquityErr = err
			}
		}
		if needPositions {
			positions, err = client.Positions(ctx, "")
			if err != nil {
				return snapshot, err
			}
		}
		if needBalance && snapshot.EquityErr == nil {
			balance, ok := tvbotbinance.USDTBalanceFromAccount(balances)
			if !ok {
				snapshot.EquityErr = errors.New("Binance USDT balance is unavailable")
			} else {
				equity, equityErr := parseAdmissionPositiveFloat(balance.Balance, "Binance wallet balance")
				if equityErr != nil {
					snapshot.EquityErr = equityErr
				} else {
					if needPositions {
						for _, position := range positions {
							if !strings.EqualFold(strings.TrimSpace(position.MarginAsset), "USDT") {
								continue
							}
							if pnl, pnlErr := strconv.ParseFloat(strings.TrimSpace(position.UnRealizedProfit), 64); pnlErr == nil {
								equity += pnl
							}
						}
					} else if pnl, pnlErr := strconv.ParseFloat(strings.TrimSpace(balance.CrossUnPnl), 64); pnlErr == nil {
						equity += pnl
					}
					snapshot.Equity = equity
					if snapshot.Equity <= 0 {
						snapshot.EquityErr = errors.New("Binance total equity is not positive")
					}
				}
			}
		}
		if needPositions {
			for _, position := range positions {
				qty, qtyErr := strconv.ParseFloat(strings.TrimSpace(position.PositionAmt), 64)
				if qtyErr != nil || qty == 0 {
					continue
				}
				symbol := admissionSymbolKey(exchange, position.Symbol, "")
				avg, _ := parseAdmissionPositiveFloat(position.EntryPrice, "Binance entry price")
				snapshot.Positions = append(snapshot.Positions, admissionPosition{Symbol: symbol, Side: binancePositionSide(position, qty), AvgPrice: avg})
			}
		}
		if needPending {
			orders, orderErr := client.OpenOrders(ctx, "")
			if orderErr != nil {
				return snapshot, orderErr
			}
			for _, order := range orders {
				if order.ReduceOnly || order.ClosePosition {
					continue
				}
				if symbol := admissionSymbolKey(exchange, order.Symbol, ""); symbol != "" {
					snapshot.PendingSymbols[symbol] = struct{}{}
				}
			}
		}
	case tvbottrading.ExchangeGate:
		store := s.gateCredentialStore(cfg.Trading.Env)
		if store == nil {
			return snapshot, errors.New("Gate credential store is not configured")
		}
		creds, apiID, err := store.GateCredentials(signal.APIID)
		if err != nil {
			return snapshot, err
		}
		snapshot.APIID = apiID
		client := tvbotgate.Client{BaseURL: cfg.GateBaseURL(), Credentials: creds, HTTPClient: s.GateHTTPClient}
		var account tvbotgate.Account
		if needBalance {
			account, err = client.Account(ctx)
			if err != nil {
				snapshot.EquityErr = err
			} else {
				snapshot.Equity, snapshot.EquityErr = parseAdmissionPositiveFloat(gateUSDTEq(account), "Gate total equity")
			}
		}
		var positions []tvbotgate.Position
		if needPositions {
			positions, err = client.Positions(ctx, "")
			if err != nil {
				return snapshot, err
			}
			for _, position := range positions {
				size, sizeErr := strconv.ParseFloat(strings.TrimSpace(position.Size), 64)
				if sizeErr != nil || size == 0 {
					continue
				}
				symbol := admissionSymbolKey(exchange, position.Contract, "")
				avg, _ := parseAdmissionPositiveFloat(position.EntryPrice, "Gate entry price")
				snapshot.Positions = append(snapshot.Positions, admissionPosition{Symbol: symbol, Side: signedPositionSide(size), AvgPrice: avg})
			}
		}
		if needPending {
			orders, orderErr := client.OpenOrders(ctx, "")
			if orderErr != nil {
				return snapshot, orderErr
			}
			for _, order := range orders {
				if order.IsReduceOnly || order.IsClose {
					continue
				}
				if symbol := admissionSymbolKey(exchange, order.Contract, ""); symbol != "" {
					snapshot.PendingSymbols[symbol] = struct{}{}
				}
			}
		}
	default:
		return snapshot, fmt.Errorf("unsupported target exchange %q", signal.TargetExchange)
	}
	return snapshot, nil
}

func (s tradeAccountSnapshot) findPosition(symbol string, side tvbottrading.Side) (admissionPosition, bool) {
	for _, position := range s.Positions {
		if position.Symbol == symbol && position.Side == side {
			return position, true
		}
	}
	return admissionPosition{}, false
}

func parseAdmissionPositiveFloat(raw, label string) (float64, error) {
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || value <= 0 {
		if err == nil {
			err = errors.New("value is not positive")
		}
		return 0, fmt.Errorf("%s: %w", label, err)
	}
	return value, nil
}

func rawJSONBool(raw json.RawMessage) bool {
	var value bool
	if json.Unmarshal(raw, &value) == nil {
		return value
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		parsed, err := strconv.ParseBool(strings.TrimSpace(text))
		return err == nil && parsed
	}
	return false
}

func admissionSignalSymbol(signal tvbottrading.Signal) string {
	return admissionSymbolKey(signal.TargetExchange, signal.Coinpair, signal.Ticker)
}

func admissionSymbolKey(exchange, primary, fallback string) string {
	value := strings.TrimSpace(primary)
	if value == "" {
		value = strings.TrimSpace(fallback)
	}
	if colon := strings.LastIndex(value, ":"); colon >= 0 {
		value = value[colon+1:]
	}
	value = strings.ToUpper(strings.TrimSpace(value))
	value = strings.TrimSuffix(value, ".P")
	value = strings.TrimSuffix(value, "-PERP")
	value = strings.TrimSuffix(value, "_PERP")
	value = strings.ReplaceAll(value, "-", "")
	value = strings.ReplaceAll(value, "_", "")
	value = strings.TrimSuffix(value, "SWAP")
	if exchange == tvbottrading.ExchangeGate {
		value = strings.TrimSuffix(value, "USDT") + "USDT"
	}
	return value
}

func pyramidPositionSide(signal tvbottrading.Signal) tvbottrading.Side {
	if strings.EqualFold(strings.TrimSpace(signal.PositionSide), tvbottrading.PositionSideShort) {
		return tvbottrading.ActionShort
	}
	return signal.Action
}

func pyramidClosePositionSide(signal tvbottrading.Signal) tvbottrading.Side {
	switch strings.ToLower(strings.TrimSpace(signal.PositionSide)) {
	case string(tvbottrading.ActionLong):
		return tvbottrading.ActionLong
	case string(tvbottrading.ActionShort):
		return tvbottrading.ActionShort
	}
	if signal.Action == tvbottrading.ActionShort {
		return tvbottrading.ActionLong
	}
	return tvbottrading.ActionShort
}

func signedPositionSide(size float64) tvbottrading.Side {
	if size < 0 {
		return tvbottrading.ActionShort
	}
	return tvbottrading.ActionLong
}

func okxPositionSide(position tvbotokx.Position) tvbottrading.Side {
	switch strings.ToLower(strings.TrimSpace(position.PosSide)) {
	case string(tvbottrading.ActionShort):
		return tvbottrading.ActionShort
	case string(tvbottrading.ActionLong):
		return tvbottrading.ActionLong
	default:
		value, _ := strconv.ParseFloat(strings.TrimSpace(position.Pos), 64)
		return signedPositionSide(value)
	}
}

func binancePositionSide(position tvbotbinance.Position, quantity float64) tvbottrading.Side {
	switch strings.ToUpper(strings.TrimSpace(position.PositionSide)) {
	case "SHORT":
		return tvbottrading.ActionShort
	case "LONG":
		return tvbottrading.ActionLong
	default:
		return signedPositionSide(quantity)
	}
}
