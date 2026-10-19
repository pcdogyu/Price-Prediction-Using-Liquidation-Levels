package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotbinance"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotconfig"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotokx"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

type tvOrderPositionSemantics struct {
	effect string
	side   string
}

func applyTVOrderPositionSemantics(signal *trading.Signal) error {
	signal.Normalize()
	explicitEffect := signal.PositionEffect
	explicitSide := signal.PositionSide
	orderIntent := strings.TrimSpace(firstNonEmptyString(signal.OrderIntent, signal.Intent))
	primary := tvOrderPositionSemanticsFromText(orderIntent, true)
	fallback := tvOrderPositionSemanticsFromText(strings.Join([]string{signal.Condition, signal.Text}, " "), false)

	effect := explicitEffect
	if effect == "" {
		effect = primary.effect
	}
	if effect == "" {
		effect = fallback.effect
	}
	if effect == "" {
		effect = trading.PositionEffectOpen
	}
	side := explicitSide
	if side == "" {
		side = primary.side
	}
	if side == "" {
		side = fallback.side
	}
	inferredSide, ok := tvOrderPositionSideFromAction(signal.Action, effect)
	if !ok {
		signal.PositionEffect = effect
		signal.PositionSide = side
		signal.OrderIntent = orderIntent
		return fmt.Errorf("unsupported action %q for position effect %q", signal.Action, effect)
	}
	if side == "" {
		side = inferredSide
	} else if side != inferredSide {
		signal.PositionEffect = effect
		signal.PositionSide = side
		signal.OrderIntent = orderIntent
		return fmt.Errorf("position intent conflict: action %q implies %s %s but signal text implies %s %s", signal.Action, effect, inferredSide, effect, side)
	}

	signal.PositionEffect = effect
	signal.PositionSide = side
	signal.OrderIntent = orderIntent
	return nil
}

func tvOrderPositionSideFromAction(action trading.Side, effect string) (string, bool) {
	switch effect {
	case trading.PositionEffectClose:
		switch action {
		case trading.ActionLong:
			return trading.PositionSideShort, true
		case trading.ActionShort:
			return trading.PositionSideLong, true
		}
	default:
		switch action {
		case trading.ActionLong:
			return trading.PositionSideLong, true
		case trading.ActionShort:
			return trading.PositionSideShort, true
		}
	}
	return "", false
}

func tvOrderPositionSemanticsFromText(raw string, structured bool) tvOrderPositionSemantics {
	text := strings.ToLower(strings.TrimSpace(raw))
	if text == "" {
		return tvOrderPositionSemantics{}
	}
	var out tvOrderPositionSemantics
	if hasTVOrderCloseIntent(text, structured) {
		out.effect = trading.PositionEffectClose
	} else if hasTVOrderOpenIntent(text, structured) {
		out.effect = trading.PositionEffectOpen
	}
	out.side = tvOrderPositionSideFromText(text, structured)
	return out
}

func hasTVOrderCloseIntent(text string, structured bool) bool {
	if strings.Contains(text, "止盈") ||
		strings.Contains(text, "止损") ||
		strings.Contains(text, "止損") ||
		strings.Contains(text, "平仓") ||
		strings.Contains(text, "平倉") ||
		strings.Contains(text, "离场") ||
		strings.Contains(text, "離場") ||
		strings.Contains(text, "exit") ||
		strings.Contains(text, "take_profit") ||
		strings.Contains(text, "take-profit") ||
		strings.Contains(text, "take profit") ||
		strings.Contains(text, "stop_loss") ||
		strings.Contains(text, "stop-loss") ||
		strings.Contains(text, "stop loss") ||
		strings.Contains(text, "reduce") {
		return true
	}
	if structured {
		for _, token := range tvOrderIntentTokens(text) {
			switch token {
			case "tp", "sl", "takeprofit", "stoploss":
				return true
			}
		}
	}
	if structured && text == "close" {
		return true
	}
	return strings.Contains(text, "close_long") ||
		strings.Contains(text, "close-short") ||
		strings.Contains(text, "close_short") ||
		strings.Contains(text, "close-long") ||
		strings.Contains(text, "close long") ||
		strings.Contains(text, "close short")
}

func hasTVOrderOpenIntent(text string, structured bool) bool {
	if strings.Contains(text, "开仓") ||
		strings.Contains(text, "開倉") ||
		strings.Contains(text, "入场") ||
		strings.Contains(text, "入場") ||
		strings.Contains(text, "entry") ||
		strings.Contains(text, "enter") ||
		strings.Contains(text, "smc_enter") {
		return true
	}
	if structured && text == "open" {
		return true
	}
	return strings.Contains(text, "open_long") ||
		strings.Contains(text, "open-short") ||
		strings.Contains(text, "open_short") ||
		strings.Contains(text, "open-long") ||
		strings.Contains(text, "open long") ||
		strings.Contains(text, "open short")
}

func tvOrderPositionSideFromText(text string, structured bool) string {
	long := strings.Contains(text, "long") ||
		strings.Contains(text, "多单") ||
		strings.Contains(text, "多單") ||
		strings.Contains(text, "多仓") ||
		strings.Contains(text, "多倉") ||
		strings.Contains(text, "做多")
	short := strings.Contains(text, "short") ||
		strings.Contains(text, "空单") ||
		strings.Contains(text, "空單") ||
		strings.Contains(text, "空仓") ||
		strings.Contains(text, "空倉") ||
		strings.Contains(text, "做空")
	if structured {
		long = long || strings.Contains(text, "多")
		short = short || strings.Contains(text, "空")
	}
	switch {
	case long && !short:
		return trading.PositionSideLong
	case short && !long:
		return trading.PositionSideShort
	default:
		return ""
	}
}

func tvOrderIntentTokens(text string) []string {
	fields := strings.FieldsFunc(text, func(r rune) bool {
		switch r {
		case '_', '-', ' ', '/', ':', ';', ',', '.', '|':
			return true
		default:
			return false
		}
	})
	for i := range fields {
		fields[i] = strings.TrimSpace(fields[i])
	}
	return fields
}

func (s *Server) executePositionCloseSignal(signalID string, signal trading.Signal, cfg config.Config) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	result, err := s.closePositionFromSignal(ctx, signal, cfg)
	result.SignalID = signalID
	result.PositionEffect = trading.PositionEffectClose
	result.PositionSide = signal.PositionSide
	if err != nil {
		code := "position_close_failed"
		if errors.Is(err, errPositionNotOpen) {
			code = "position_not_open"
		}
		_ = s.Orders.MarkFailedCode(signalID, code, err, s.now())
		if s.Logger != nil {
			s.Logger.Error("position close failed", "signal_id", signalID, "action", signal.Action, "position_side", signal.PositionSide, "coinpair", signal.Coinpair, "error", err)
		}
		return
	}
	completedAt := s.now()
	if err := s.Orders.MarkSubmitted(signalID, result, completedAt); err != nil {
		if s.Logger != nil {
			s.Logger.Error("failed to store submitted position close", "signal_id", signalID, "error", err)
		}
		return
	}
	cooldownSource, shouldCreateCooldown := webhookCloseCooldownSource(signal)
	if !shouldCreateCooldown {
		return
	}
	triggerPrice := strings.TrimSpace(result.Px)
	if triggerPrice == "" && signal.Price.Set {
		triggerPrice = trading.NormalizeFloat(signal.Price.Value)
	}
	if _, _, err := s.recordLossCoinpairCooldown(
		cfg,
		cooldownSource+":"+signalID,
		cooldownSource,
		firstNonEmptyString(result.TargetExchange, signal.TargetExchange),
		firstNonEmptyString(result.APIID, signal.APIID),
		triggerPrice,
		completedAt,
		result.InstID,
		signal.Coinpair,
		signal.Ticker,
	); err != nil && s.Logger != nil {
		s.Logger.Error("failed to record TP/SL webhook coinpair cooldown", "signal_id", signalID, "coinpair", signal.Coinpair, "source", cooldownSource, "error", err)
	}
}

func (s *Server) closePositionFromSignal(ctx context.Context, signal trading.Signal, cfg config.Config) (trading.OrderResult, error) {
	switch trading.NormalizeExchange(signal.TargetExchange) {
	case trading.ExchangeBinance:
		return s.closeBinancePositionFromSignal(ctx, signal, cfg)
	case trading.ExchangeOKX:
		return s.closeOKXPositionFromSignal(ctx, signal, cfg)
	default:
		return trading.OrderResult{}, fmt.Errorf("unsupported target_exchange %q", signal.TargetExchange)
	}
}

func (s *Server) closeOKXPositionFromSignal(ctx context.Context, signal trading.Signal, cfg config.Config) (trading.OrderResult, error) {
	if s.OKXCredentials == nil {
		return trading.OrderResult{}, errors.New("OKX credential store is not configured")
	}
	client, apiID, err := s.okxClientForCredentials(cfg, signal.APIID)
	if err != nil {
		return trading.OrderResult{}, err
	}
	instID, err := okxPositionCloseInstrumentID(cfg, signal)
	if err != nil {
		return trading.OrderResult{}, err
	}
	position, err := currentOpenPositionByDirection(ctx, client, instID, signal.PositionSide)
	if err != nil {
		return trading.OrderResult{}, err
	}
	order, started, err := s.startLimitPositionClose(ctx, apiID, cfg, client, position, "")
	if err != nil {
		return trading.OrderResult{}, err
	}
	if !started {
		return trading.OrderResult{}, errors.New("limit close is already running for this position")
	}
	return trading.OrderResult{
		APIID:          apiID,
		TargetExchange: trading.ExchangeOKX,
		InstID:         order.Position.InstID,
		ClOrdID:        order.Ack.ClOrdID,
		OrdType:        "limit",
		Px:             order.Px,
		OrdID:          order.Ack.OrdID,
		OKXCode:        order.Ack.SCode,
		OKXMsg:         order.Ack.SMsg,
		PositionEffect: trading.PositionEffectClose,
		PositionSide:   signal.PositionSide,
	}, nil
}

func okxPositionCloseInstrumentID(cfg config.Config, signal trading.Signal) (string, error) {
	if sym, ok := cfg.SymbolMeta(signal.Coinpair); ok {
		return sym.InstID, nil
	}
	instID, _, err := okx.DeriveSwapInstrumentID(signal.Coinpair, signal.Ticker)
	if err != nil {
		return "", err
	}
	return instID, nil
}

func (s *Server) closeBinancePositionFromSignal(ctx context.Context, signal trading.Signal, cfg config.Config) (trading.OrderResult, error) {
	if s.BinanceCredentials == nil {
		return trading.OrderResult{}, errors.New("Binance credential store is not configured")
	}
	if !cfg.BinanceLiveTradingAllowedByEnvironment() {
		return trading.OrderResult{}, errors.New("Binance live trading is not allowed by environment")
	}
	client, apiID, err := s.binanceClientForCredentials(cfg, signal.APIID)
	if err != nil {
		return trading.OrderResult{}, err
	}
	symbol, err := binance.DeriveUSDMSymbol(signal.Coinpair, signal.Ticker)
	if err != nil {
		return trading.OrderResult{}, err
	}
	position, err := currentBinanceOpenPositionByDirection(ctx, client, symbol, signal.PositionSide)
	if err != nil {
		return trading.OrderResult{}, err
	}
	order, started, err := s.startBinanceLimitPositionClose(ctx, apiID, client, position, "")
	if err != nil {
		return trading.OrderResult{}, err
	}
	if !started {
		return trading.OrderResult{}, errors.New("limit close is already running for this position")
	}
	return trading.OrderResult{
		APIID:          apiID,
		TargetExchange: trading.ExchangeBinance,
		InstID:         order.Position.InstID,
		ClOrdID:        order.Ack.ClOrdID,
		OrdType:        "limit",
		Px:             order.Px,
		OrdID:          order.Ack.OrdID,
		PositionEffect: trading.PositionEffectClose,
		PositionSide:   signal.PositionSide,
	}, nil
}

func currentOpenPositionByDirection(ctx context.Context, client okx.Client, instID, direction string) (okx.Position, error) {
	instID = strings.ToUpper(strings.TrimSpace(instID))
	direction = strings.ToLower(strings.TrimSpace(direction))
	positions, _, err := client.Positions(ctx, "SWAP")
	if err != nil {
		return okx.Position{}, err
	}
	for _, position := range positions {
		if !strings.EqualFold(position.InstID, instID) || !isOpenPosition(position.Pos) {
			continue
		}
		if positionDirectionKind(position) == direction {
			return position, nil
		}
	}
	return okx.Position{}, fmt.Errorf("%w: %s %s", errPositionNotOpen, instID, direction)
}

func currentBinanceOpenPositionByDirection(ctx context.Context, client binance.Client, instID, direction string) (okx.Position, error) {
	instID = strings.ToUpper(strings.TrimSpace(instID))
	direction = strings.ToLower(strings.TrimSpace(direction))
	positions, err := client.Positions(ctx, instID)
	if err != nil {
		return okx.Position{}, err
	}
	for _, raw := range positions {
		position := binancePositionToOKX(raw)
		if !strings.EqualFold(position.InstID, instID) || !isOpenPosition(position.Pos) {
			continue
		}
		if positionDirectionKind(position) == direction {
			return position, nil
		}
	}
	return okx.Position{}, fmt.Errorf("%w: %s %s", errPositionNotOpen, instID, direction)
}
