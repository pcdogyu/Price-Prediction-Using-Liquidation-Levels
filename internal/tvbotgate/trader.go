package gate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

type Trader struct {
	Credentials        Credentials
	CredentialProvider CredentialProvider
	HTTPClient         *http.Client
	Logger             *slog.Logger
}

func (t Trader) ExecuteSignal(ctx context.Context, signal trading.Signal, cfg trading.RuntimeConfig) (trading.OrderResult, error) {
	if !cfg.GateLiveTradingAllowedByEnvironment() {
		return trading.OrderResult{}, errors.New("live trading requires config env=live, GATE_ENV=live and ALLOW_LIVE_TRADING=true")
	}
	settings := cfg.OrderSettings().Normalize()
	settings.ApplyToSignal(&signal)
	if err := signal.Risk.Validate(); err != nil {
		return trading.OrderResult{}, err
	}
	if signal.Risk.Type == trading.RiskTrailing {
		return trading.OrderResult{}, errors.New("Gate futures does not support trailing risk orders in the main order path")
	}
	if settings.OrderType == trading.OrderTypeLayered && signal.PositionEffect != trading.PositionEffectClose {
		return t.executeLayeredSignal(ctx, signal, cfg, settings)
	}
	if settings.OrderType == trading.OrderTypeLayered {
		settings.OrderType = trading.OrderTypeMarket
	}
	client, apiID, err := t.client(cfg, signal.APIID)
	if err != nil {
		return trading.OrderResult{}, err
	}
	contractName, err := deriveContract(signal)
	if err != nil {
		return trading.OrderResult{}, err
	}
	contract, err := client.Contract(ctx, contractName)
	if err != nil {
		return trading.OrderResult{}, err
	}
	if strings.ToLower(strings.TrimSpace(contract.Status)) != "" && strings.ToLower(strings.TrimSpace(contract.Status)) != "trading" {
		return trading.OrderResult{}, fmt.Errorf("Gate contract %s is not trading", contract.Name)
	}
	if signal.Price.Value <= 0 {
		return trading.OrderResult{}, errors.New("Gate order requires a positive signal price")
	}
	sizingPx := signal.Price.Value
	price := "0"
	tif := "ioc"
	if settings.OrderType == trading.OrderTypeLimit {
		sizingPx = settings.LimitPrice(signal.Action, signal.Price.Value)
		price = formatStep(sizingPx, contract.OrderPriceRound, false)
		tif = "gtc"
	}
	qty, err := contractsForNotional(signal.Amount.Value, sizingPx, contract)
	if err != nil {
		return trading.OrderResult{}, err
	}
	if signal.Leverage <= 0 {
		return trading.OrderResult{}, errors.New("leverage must be positive")
	}
	marginMode := strings.ToLower(strings.TrimSpace(cfg.MarginMode()))
	if marginMode != "cross" {
		marginMode = "isolated"
	}
	if cfg.PositionMode() == "long_short" {
		return trading.OrderResult{}, errors.New("Gate futures currently requires net position mode; set position_mode=net")
	}
	leveragePosition, err := client.SetLeverage(ctx, contract.Name, signal.Leverage, marginMode, "")
	if err != nil {
		return trading.OrderResult{}, fmt.Errorf("Gate set leverage %s to %dx: %w", contract.Name, signal.Leverage, err)
	}
	signal.Leverage = gateEffectiveLeverage(leveragePosition, signal.Leverage)
	textID := gateClientOrderID(signal)
	req := PlaceOrderRequest{Contract: contract.Name, Size: qty, Price: price, TIF: tif, Text: textID, PosMarginMode: marginMode}
	if signal.Action == trading.ActionShort {
		req.Size = "-" + qty
	}
	if signal.Risk.Type == trading.RiskTPSL {
		tp, sl := riskTriggerPrices(signal.Action, sizingPx, signal.Risk.TPPct.Value, signal.Risk.SLPct.Value, signal.Leverage)
		req.TPTriggerPrice = formatStep(tp, contract.OrderPriceRound, false)
		req.SLTriggerPrice = formatStep(sl, contract.OrderPriceRound, false)
	}
	ack, err := client.PlaceOrder(ctx, req)
	result := trading.OrderResult{APIID: apiID, TargetExchange: trading.ExchangeGate, InstID: contract.Name, ClOrdID: textID, OrdType: string(settings.OrderType), Px: price, Leverage: signal.Leverage, OrdID: ack.IDString(), PositionEffect: signal.PositionEffect, PositionSide: signal.PositionSide}
	if signal.Risk.Type == trading.RiskTPSL {
		result.RiskOrders = []trading.RiskOrderResult{{Exchange: trading.ExchangeGate, ClientAlgoID: textID + "TP", OrderType: "TAKE_PROFIT", Side: gateCloseSide(signal.Action), Quantity: qty, TriggerPrice: req.TPTriggerPrice}, {Exchange: trading.ExchangeGate, ClientAlgoID: textID + "SL", OrderType: "STOP_LOSS", Side: gateCloseSide(signal.Action), Quantity: qty, TriggerPrice: req.SLTriggerPrice}}
	}
	if err != nil {
		if apiErr := errorAsAPI(err); apiErr != nil {
			result.GateLabel = apiErr.Label
			result.GateMsg = apiErr.Message
		}
		return result, err
	}
	if t.Logger != nil {
		t.Logger.Info("Gate futures order submitted", "api_id", apiID, "contract", contract.Name, "action", signal.Action, "client_order_id", textID)
	}
	return result, nil
}

func (t Trader) executeLayeredSignal(ctx context.Context, signal trading.Signal, cfg trading.RuntimeConfig, settings trading.OrderSettings) (trading.OrderResult, error) {
	client, apiID, err := t.client(cfg, signal.APIID)
	if err != nil {
		return trading.OrderResult{}, err
	}
	contractName, err := deriveContract(signal)
	if err != nil {
		return trading.OrderResult{}, err
	}
	contract, err := client.Contract(ctx, contractName)
	if err != nil {
		return trading.OrderResult{}, err
	}
	if strings.ToLower(strings.TrimSpace(contract.Status)) != "" && strings.ToLower(strings.TrimSpace(contract.Status)) != "trading" {
		return trading.OrderResult{}, fmt.Errorf("Gate contract %s is not trading", contract.Name)
	}
	marketPrice, err := gateRealtimePrice(ctx, client, contract.Name)
	if err != nil {
		return trading.OrderResult{}, err
	}
	legs, err := settings.Layered.BuildPlan(signal.Action, signal.Amount.Value, signal.Price.Value, marketPrice)
	if err != nil {
		return trading.OrderResult{}, err
	}
	if signal.Leverage <= 0 {
		return trading.OrderResult{}, errors.New("leverage must be positive")
	}
	marginMode := strings.ToLower(strings.TrimSpace(cfg.MarginMode()))
	if marginMode != "cross" {
		marginMode = "isolated"
	}
	if cfg.PositionMode() == "long_short" {
		return trading.OrderResult{}, errors.New("Gate futures currently requires net position mode; set position_mode=net")
	}
	leveragePosition, err := client.SetLeverage(ctx, contract.Name, signal.Leverage, marginMode, "")
	if err != nil {
		return trading.OrderResult{}, fmt.Errorf("Gate set leverage %s to %dx: %w", contract.Name, signal.Leverage, err)
	}
	signal.Leverage = gateEffectiveLeverage(leveragePosition, signal.Leverage)
	result := trading.OrderResult{
		APIID:          apiID,
		TargetExchange: trading.ExchangeGate,
		InstID:         contract.Name,
		OrdType:        string(trading.OrderTypeLayered),
		Px:             trading.NormalizeFloat(marketPrice),
		Leverage:       signal.Leverage,
		PositionEffect: signal.PositionEffect,
		PositionSide:   signal.PositionSide,
	}
	baseID := gateClientOrderID(signal)
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
		qty, qtyErr := contractsForNotional(leg.AmountUSDT, leg.Price, contract)
		if qtyErr != nil {
			layer.Status = "failed"
			layer.Error = qtyErr.Error()
			if firstErr == nil {
				firstErr = qtyErr
			}
			result.Layers = append(result.Layers, layer)
			continue
		}
		price := "0"
		tif := "ioc"
		if leg.OrderType == trading.OrderTypeLimit {
			price = formatGateLayerPrice(leg.Price, contract.OrderPriceRound, signal.Action)
			tif = "gtc"
			layer.Price = price
		}
		textID := gateLayeredClientOrderID(baseID, index)
		req := PlaceOrderRequest{Contract: contract.Name, Size: qty, Price: price, TIF: tif, Text: textID, PosMarginMode: marginMode}
		if signal.Action == trading.ActionShort {
			req.Size = "-" + qty
		}
		if signal.Risk.Type == trading.RiskTPSL {
			tp, sl := riskTriggerPrices(signal.Action, leg.Price, signal.Risk.TPPct.Value, signal.Risk.SLPct.Value, signal.Leverage)
			req.TPTriggerPrice = formatStep(tp, contract.OrderPriceRound, false)
			req.SLTriggerPrice = formatStep(sl, contract.OrderPriceRound, false)
			layer.RiskOrders = []trading.RiskOrderResult{
				{Exchange: trading.ExchangeGate, ClientAlgoID: textID + "TP", OrderType: "TAKE_PROFIT", Side: gateCloseSide(signal.Action), Quantity: qty, TriggerPrice: req.TPTriggerPrice},
				{Exchange: trading.ExchangeGate, ClientAlgoID: textID + "SL", OrderType: "STOP_LOSS", Side: gateCloseSide(signal.Action), Quantity: qty, TriggerPrice: req.SLTriggerPrice},
			}
		}
		ack, placeErr := client.PlaceOrder(ctx, req)
		layer.ClOrdID = textID
		layer.OrdID = ack.IDString()
		if layer.OrdID != "" {
			layer.Status = "submitted"
			result.ClOrdID = joinGateOrderIDs(result.ClOrdID, textID)
			result.OrdID = joinGateOrderIDs(result.OrdID, layer.OrdID)
			result.RiskOrders = append(result.RiskOrders, layer.RiskOrders...)
		}
		if placeErr != nil {
			layer.Error = placeErr.Error()
			if apiErr := errorAsAPI(placeErr); apiErr != nil {
				result.GateLabel = apiErr.Label
				result.GateMsg = apiErr.Message
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
			firstErr = errors.New("all Gate layered order legs failed")
		}
		return result, firstErr
	}
	if firstErr != nil {
		result.Partial = true
		return result, fmt.Errorf("Gate layered order partially submitted: %w", firstErr)
	}
	return result, nil
}

func gateRealtimePrice(ctx context.Context, client Client, contract string) (float64, error) {
	tickers, err := client.Tickers(ctx)
	if err != nil {
		return 0, fmt.Errorf("refresh Gate layered price for %s: %w", contract, err)
	}
	for _, ticker := range tickers {
		if !strings.EqualFold(strings.TrimSpace(ticker.Contract), strings.TrimSpace(contract)) {
			continue
		}
		price, parseErr := strconv.ParseFloat(strings.TrimSpace(ticker.Last), 64)
		if parseErr != nil || price <= 0 {
			return 0, fmt.Errorf("invalid Gate ticker price %q for %s", ticker.Last, contract)
		}
		return price, nil
	}
	return 0, fmt.Errorf("Gate ticker for %s not found", contract)
}

func formatGateLayerPrice(value float64, step string, action trading.Side) string {
	n := parsePositive(step, 0)
	if n <= 0 {
		return formatGatePrice(value, step)
	}
	units := value / n
	if action == trading.ActionShort {
		value = math.Ceil(units-1e-10) * n
	} else {
		value = math.Floor(units+1e-10) * n
	}
	return formatGatePrice(value, step)
}

func gateLayeredClientOrderID(base string, index int) string {
	suffix := fmt.Sprintf("-L%02d", index+1)
	base = strings.TrimSpace(base)
	limit := maxGateClientIDLength - len(suffix)
	if len(base) > limit {
		base = base[:limit]
	}
	return base + suffix
}

const maxGateClientIDLength = 28

func joinGateOrderIDs(left, right string) string {
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

func (t Trader) Check(ctx context.Context, cfg trading.RuntimeConfig) (map[string]any, error) {
	return t.CheckAccount(ctx, cfg, "")
}
func (t Trader) CheckAccount(ctx context.Context, cfg trading.RuntimeConfig, apiID string) (map[string]any, error) {
	client, id, err := t.client(cfg, apiID)
	if err != nil {
		return nil, err
	}
	return t.checkClient(ctx, cfg, client, id)
}
func (t Trader) CheckCredentials(ctx context.Context, cfg trading.RuntimeConfig, apiID string, creds Credentials) (map[string]any, error) {
	if err := creds.Validate(); err != nil {
		return nil, err
	}
	id := strings.TrimSpace(apiID)
	if id == "" {
		id = "input"
	}
	return t.checkClient(ctx, cfg, Client{BaseURL: cfg.GateBaseURL(), Credentials: creds, HTTPClient: t.HTTPClient}, id)
}
func (t Trader) checkClient(ctx context.Context, cfg trading.RuntimeConfig, client Client, apiID string) (map[string]any, error) {
	account, err := client.Account(ctx)
	if err != nil {
		return nil, err
	}
	contracts, err := client.Contracts(ctx)
	if err != nil {
		return nil, err
	}
	balance := account.Total
	if strings.TrimSpace(account.Available) != "" {
		balance = account.Available
	}
	return map[string]any{"ok": true, "exchange": trading.ExchangeGate, "api_id": apiID, "base_url": cfg.GateBaseURL(), "usdt_balance": map[string]any{"ccy": "USDT", "eq": account.Total, "avail_eq": account.Available}, "usdt_balance_found": strings.TrimSpace(balance) != "", "instruments_count": len(contracts)}, nil
}
func (t Trader) client(cfg trading.RuntimeConfig, apiID string) (Client, string, error) {
	creds := t.Credentials
	id := strings.TrimSpace(apiID)
	if t.CredentialProvider != nil {
		var err error
		creds, id, err = t.CredentialProvider.GateCredentials(apiID)
		if err != nil {
			return Client{}, id, err
		}
	}
	if err := creds.Validate(); err != nil {
		return Client{}, id, err
	}
	return Client{BaseURL: cfg.GateBaseURL(), Credentials: creds, HTTPClient: t.HTTPClient}, id, nil
}

func DeriveContract(signal trading.Signal) (string, error) { return deriveContract(signal) }
func deriveContract(signal trading.Signal) (string, error) {
	raw := strings.ToUpper(strings.TrimSpace(signal.Coinpair))
	if raw == "" {
		raw = strings.ToUpper(strings.TrimSpace(signal.Ticker))
	}
	if i := strings.LastIndex(raw, ":"); i >= 0 {
		raw = raw[i+1:]
	}
	raw = strings.NewReplacer(".P", "", "PERP", "", "SWAP", "", "/", "_", "-", "_", " ", "").Replace(raw)
	if strings.HasSuffix(raw, "_USDT") {
		return raw, nil
	}
	if strings.HasSuffix(raw, "USDT") {
		return strings.TrimSuffix(raw, "USDT") + "_USDT", nil
	}
	if raw != "" {
		return raw + "_USDT", nil
	}
	return "", errors.New("cannot derive Gate futures contract from coinpair or ticker")
}
func contractsForNotional(amount, price float64, c Contract) (string, error) {
	mult, err := strconv.ParseFloat(strings.TrimSpace(c.QuantoMultiplier), 64)
	if err != nil || mult <= 0 {
		return "", fmt.Errorf("Gate contract %s has invalid quanto_multiplier %q", c.Name, c.QuantoMultiplier)
	}
	if amount <= 0 || price <= 0 {
		return "", errors.New("Gate amount and price must be positive")
	}
	raw := amount / (price * mult)
	min := parsePositive(c.OrderSizeMin, 0)
	if raw < min {
		return "", fmt.Errorf("order size %.8f is below Gate min contract size %s", raw, c.OrderSizeMin)
	}
	if !c.EnableDecimal {
		raw = math.Floor(raw)
	}
	if raw < min {
		return "", fmt.Errorf("order size after Gate contract rounding is below minimum %s", c.OrderSizeMin)
	}
	max := parsePositive(c.OrderSizeMax, 0)
	if max > 0 && raw > max {
		return "", fmt.Errorf("order size %.8f exceeds Gate max contract size %s", raw, c.OrderSizeMax)
	}
	return formatStep(raw, c.OrderSizeMin, true), nil
}
func parsePositive(v string, fallback float64) float64 {
	n, _ := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if n > 0 {
		return n
	}
	return fallback
}
func formatStep(v float64, step string, floor bool) string {
	n := parsePositive(step, 0)
	if n > 0 {
		if floor {
			v = math.Floor(v/n+1e-10) * n
		} else {
			v = math.Round(v/n) * n
		}
	}
	return formatGatePrice(v, step)
}

func formatGatePrice(value float64, step string) string {
	precision := gateStepDecimalPlaces(step)
	if precision < 0 {
		return strconv.FormatFloat(value, 'f', -1, 64)
	}
	raw := strconv.FormatFloat(value, 'f', precision, 64)
	return strings.TrimRight(strings.TrimRight(raw, "0"), ".")
}

func gateStepDecimalPlaces(step string) int {
	n := parsePositive(step, 0)
	if n <= 0 {
		return -1
	}
	raw := strconv.FormatFloat(n, 'f', -1, 64)
	dot := strings.IndexByte(raw, '.')
	if dot < 0 {
		return 0
	}
	return len(raw) - dot - 1
}
func riskTriggerPrices(action trading.Side, entry, tpPct, slPct float64, leverage int) (float64, float64) {
	tp, sl, err := trading.FixedTPSLTriggerPrices(action, entry, tpPct, slPct, leverage)
	if err != nil {
		return 0, 0
	}
	return tp, sl
}

func gateEffectiveLeverage(position Position, fallback int) int {
	for _, raw := range []string{position.Lever, position.Leverage} {
		value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		if err == nil && value > 0 && math.Trunc(value) == value {
			return int(value)
		}
	}
	return fallback
}

func gateCloseSide(action trading.Side) string {
	if action == trading.ActionShort {
		return "BUY"
	}
	return "SELL"
}
func gateClientOrderID(signal trading.Signal) string {
	raw := strings.TrimSpace(signal.TokenNonce)
	if raw == "" {
		raw = strings.TrimSpace(signal.SentAt)
	}
	raw = strings.NewReplacer(":", "", "-", "", "+", "", ".", "", "/", "", " ", "").Replace(raw)
	if raw == "" {
		raw = "order"
	}
	if len(raw) > 24 {
		raw = raw[len(raw)-24:]
	}
	return "t-" + raw
}
func errorAsAPI(err error) *APIError {
	var target APIError
	if errors.As(err, &target) {
		return &target
	}
	var pointer *APIError
	if errors.As(err, &pointer) {
		return pointer
	}
	return nil
}
