package trading

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

type Side string

const (
	ActionLong  Side = "long"
	ActionShort Side = "short"
)

const (
	ExchangeOKX     = "okx"
	ExchangeBinance = "binance"
)

const (
	TradeEnvDemo = "demo"
	TradeEnvLive = "live"
)

type RiskType string

const (
	RiskNone     RiskType = "none"
	RiskTPSL     RiskType = "tp_sl"
	RiskTrailing RiskType = "trailing"
)

type OrderType string

const (
	OrderTypeMarket OrderType = "market"
	OrderTypeLimit  OrderType = "limit"
)

const (
	PositionEffectOpen  = "open"
	PositionEffectClose = "close"
	PositionSideLong    = "long"
	PositionSideShort   = "short"
)

const (
	MarketStrategyTrend      = "trend"
	MarketStrategyScalp      = "scalp"
	MarketStrategyTransition = "transition"
)

type FlexibleFloat struct {
	Value float64
	Set   bool
}

func NewFlexibleFloat(v float64) FlexibleFloat {
	return FlexibleFloat{Value: v, Set: true}
}

func (f *FlexibleFloat) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || bytes.Equal(b, []byte("null")) {
		*f = FlexibleFloat{}
		return nil
	}
	var raw string
	if b[0] == '"' {
		if err := json.Unmarshal(b, &raw); err != nil {
			return err
		}
	} else {
		raw = string(b)
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		*f = FlexibleFloat{}
		return nil
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return fmt.Errorf("invalid number %q", raw)
	}
	*f = FlexibleFloat{Value: v, Set: true}
	return nil
}

func (f FlexibleFloat) MarshalJSON() ([]byte, error) {
	if !f.Set {
		return []byte("null"), nil
	}
	return []byte(strconv.Quote(NormalizeFloat(f.Value))), nil
}

type Risk struct {
	Type        RiskType       `json:"type"`
	TPPct       *FlexibleFloat `json:"tp_pct,omitempty"`
	SLPct       *FlexibleFloat `json:"sl_pct,omitempty"`
	TrailingPct *FlexibleFloat `json:"trailing_pct,omitempty"`
}

type Signal struct {
	Action         Side          `json:"action"`
	SourceAction   Side          `json:"-"`
	APIID          string        `json:"api_id,omitempty"`
	TargetExchange string        `json:"target_exchange,omitempty"`
	TradeEnv       string        `json:"trade_env,omitempty"`
	Coinpair       string        `json:"coinpair"`
	Price          FlexibleFloat `json:"price"`
	SentAt         string        `json:"sent_at"`
	Time           string        `json:"time,omitempty"`
	Ticker         string        `json:"ticker"`
	Exchange       string        `json:"exchange,omitempty"`
	Interval       string        `json:"interval,omitempty"`
	Condition      string        `json:"condition,omitempty"`
	Text           string        `json:"text,omitempty"`
	OrderIntent    string        `json:"order_intent,omitempty"`
	Intent         string        `json:"intent,omitempty"`
	PositionEffect string        `json:"position_effect,omitempty"`
	PositionSide   string        `json:"position_side,omitempty"`
	Leverage       int           `json:"leverage"`
	Amount         FlexibleFloat `json:"amount"`
	Risk           Risk          `json:"risk,omitempty"`
	TokenNonce     string        `json:"token_nonce,omitempty"`
	Token          string        `json:"token"`
	ADX            *float64      `json:"-"`
	MarketStrategy string        `json:"-"`
	RawJSON        string        `json:"-"`
}

type OrderSettings struct {
	Amount                    FlexibleFloat
	Leverage                  int
	OrderType                 OrderType
	Risk                      Risk
	LongLimitPriceMultiplier  float64
	ShortLimitPriceMultiplier float64
}

type TemplateRequest struct {
	PriceSource    string        `json:"price_source"`
	APIID          string        `json:"api_id,omitempty"`
	TargetExchange string        `json:"target_exchange,omitempty"`
	TradeEnv       string        `json:"trade_env,omitempty"`
	Coinpair       string        `json:"coinpair,omitempty"`
	Direction      string        `json:"direction,omitempty"`
	Leverage       int           `json:"leverage"`
	Amount         FlexibleFloat `json:"amount"`
}

type TemplateResponse struct {
	JSON       string `json:"json"`
	Token      string `json:"token"`
	TokenNonce string `json:"token_nonce,omitempty"`
}

type OrderResult struct {
	SignalID       string            `json:"signal_id"`
	APIID          string            `json:"api_id,omitempty"`
	TargetExchange string            `json:"target_exchange,omitempty"`
	InstID         string            `json:"inst_id"`
	ClOrdID        string            `json:"cl_ord_id"`
	OrdType        string            `json:"ord_type,omitempty"`
	Px             string            `json:"px,omitempty"`
	Leverage       int               `json:"leverage,omitempty"`
	OrdID          string            `json:"ord_id,omitempty"`
	OKXCode        string            `json:"okx_code,omitempty"`
	OKXMsg         string            `json:"okx_msg,omitempty"`
	BinanceCode    int               `json:"binance_code,omitempty"`
	BinanceMsg     string            `json:"binance_msg,omitempty"`
	PositionEffect string            `json:"position_effect,omitempty"`
	PositionSide   string            `json:"position_side,omitempty"`
	RiskOrders     []RiskOrderResult `json:"risk_orders,omitempty"`
}

type RiskOrderResult struct {
	Exchange      string `json:"exchange,omitempty"`
	AlgoID        string `json:"algo_id,omitempty"`
	ClientAlgoID  string `json:"client_algo_id,omitempty"`
	OrderType     string `json:"order_type,omitempty"`
	Side          string `json:"side,omitempty"`
	PositionSide  string `json:"position_side,omitempty"`
	Quantity      string `json:"quantity,omitempty"`
	TriggerPrice  string `json:"trigger_price,omitempty"`
	ActivatePrice string `json:"activate_price,omitempty"`
	CallbackRate  string `json:"callback_rate,omitempty"`
}

type Executor interface {
	ExecuteSignal(ctx context.Context, signal Signal, cfg RuntimeConfig) (OrderResult, error)
	Check(ctx context.Context, cfg RuntimeConfig) (map[string]any, error)
}

type RuntimeConfig interface {
	SymbolMeta(coinpair string) (SymbolInfo, bool)
	DemoTradingHeaderEnabled() bool
	LiveTradingAllowedByEnvironment() bool
	BinanceBaseURL() string
	BinanceLiveTradingAllowedByEnvironment() bool
	OKXBaseURL() string
	MarginMode() string
	PositionMode() string
	OrderSettings() OrderSettings
}

type SymbolInfo struct {
	Coinpair string
	InstID   string
	CtVal    float64
	TickSz   float64
	LotSz    float64
	MinSz    float64
}

func (r *Risk) Normalize() {
	r.Type = RiskType(strings.ToLower(strings.TrimSpace(string(r.Type))))
	if r.Type == "" {
		r.Type = RiskNone
	}
}

func (s *Signal) Normalize() {
	switch strings.ToLower(strings.TrimSpace(string(s.Action))) {
	case "buy", "long":
		s.Action = ActionLong
	case "sell", "short":
		s.Action = ActionShort
	default:
		s.Action = Side(strings.ToLower(strings.TrimSpace(string(s.Action))))
	}
	s.SourceAction = normalizeSide(s.SourceAction)
	s.APIID = strings.TrimSpace(s.APIID)
	s.TargetExchange = NormalizeExchange(s.TargetExchange)
	s.TradeEnv = NormalizeTradeEnv(s.TradeEnv)
	s.Coinpair = strings.ToUpper(strings.TrimSpace(s.Coinpair))
	s.Ticker = strings.TrimSpace(s.Ticker)
	s.SentAt = strings.TrimSpace(s.SentAt)
	s.Exchange = strings.TrimSpace(s.Exchange)
	s.Interval = strings.TrimSpace(s.Interval)
	s.Condition = strings.TrimSpace(s.Condition)
	s.Text = strings.TrimSpace(s.Text)
	s.OrderIntent = strings.TrimSpace(s.OrderIntent)
	s.Intent = strings.TrimSpace(s.Intent)
	if s.OrderIntent == "" {
		s.OrderIntent = s.Intent
	}
	s.PositionEffect = normalizePositionEffect(s.PositionEffect)
	s.PositionSide = normalizePositionSide(s.PositionSide)
	s.Time = strings.TrimSpace(s.Time)
	if s.SentAt == "" && s.Time != "" {
		s.SentAt = s.Time
	}
	if s.Coinpair == "" {
		s.Coinpair = strings.ToUpper(s.Ticker)
	}
	s.TokenNonce = strings.TrimSpace(s.TokenNonce)
	s.Token = strings.TrimSpace(s.Token)
	s.MarketStrategy = strings.ToLower(strings.TrimSpace(s.MarketStrategy))
	s.Risk.Normalize()
}

func normalizeSide(side Side) Side {
	switch strings.ToLower(strings.TrimSpace(string(side))) {
	case "buy", "long":
		return ActionLong
	case "sell", "short":
		return ActionShort
	default:
		return Side(strings.ToLower(strings.TrimSpace(string(side))))
	}
}

func normalizePositionEffect(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case PositionEffectOpen, "entry", "enter", "开仓", "開倉":
		return PositionEffectOpen
	case PositionEffectClose, "exit", "reduce", "tp", "sl", "take_profit", "take-profit", "take profit", "stop_loss", "stop-loss", "stop loss", "平仓", "平倉", "止盈", "止损", "止損":
		return PositionEffectClose
	default:
		return strings.ToLower(strings.TrimSpace(raw))
	}
}

func normalizePositionSide(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case PositionSideLong, "buy", "多", "多单":
		return PositionSideLong
	case PositionSideShort, "sell", "空", "空单":
		return PositionSideShort
	default:
		return strings.ToLower(strings.TrimSpace(raw))
	}
}

func NormalizeExchange(exchange string) string {
	switch strings.ToLower(strings.TrimSpace(exchange)) {
	case "", ExchangeOKX:
		return ExchangeOKX
	case ExchangeBinance, "binance_futures", "binance-usdm", "binance_usdm", "binance usdⓈ-m", "binance usdm":
		return ExchangeBinance
	default:
		return strings.ToLower(strings.TrimSpace(exchange))
	}
}

func TargetExchangeFromSignalSource(exchange, ticker string) (string, bool) {
	source := strings.ToLower(strings.TrimSpace(exchange))
	if source == "" {
		source = strings.ToLower(strings.TrimSpace(ticker))
	}
	if i := strings.Index(source, ":"); i >= 0 {
		source = source[:i]
	}
	source = strings.ReplaceAll(source, "_", " ")
	source = strings.ReplaceAll(source, "-", " ")
	switch {
	case strings.Contains(source, "binance"):
		return ExchangeBinance, true
	case strings.Contains(source, "okx"), strings.Contains(source, "okex"):
		return ExchangeOKX, true
	default:
		return "", false
	}
}

func ValidTargetExchange(exchange string) bool {
	switch NormalizeExchange(exchange) {
	case ExchangeOKX, ExchangeBinance:
		return true
	default:
		return false
	}
}

func NormalizeTradeEnv(env string) string {
	switch strings.ToLower(strings.TrimSpace(env)) {
	case "", TradeEnvDemo, "sim", "simulated", "simulation", "paper", "paper_trade", "paper-trade", "test", "testing", "sandbox", "mock", "模拟", "模擬", "测试", "測試":
		if strings.TrimSpace(env) == "" {
			return ""
		}
		return TradeEnvDemo
	case TradeEnvLive, "real", "prod", "production", "mainnet", "实盘", "實盤", "生产", "生產":
		return TradeEnvLive
	default:
		return strings.ToLower(strings.TrimSpace(env))
	}
}

func ValidTradeEnv(env string) bool {
	switch NormalizeTradeEnv(env) {
	case TradeEnvDemo, TradeEnvLive:
		return true
	default:
		return false
	}
}

func (r Risk) Validate() error {
	switch r.Type {
	case RiskNone:
		return nil
	case RiskTPSL:
		if r.TPPct == nil || !r.TPPct.Set || r.TPPct.Value <= 0 {
			return errors.New("risk.tp_pct must be positive for tp_sl")
		}
		if r.SLPct == nil || !r.SLPct.Set || r.SLPct.Value <= 0 {
			return errors.New("risk.sl_pct must be positive for tp_sl")
		}
		return nil
	case RiskTrailing:
		if r.TrailingPct == nil || !r.TrailingPct.Set || r.TrailingPct.Value <= 0 {
			return errors.New("risk.trailing_pct must be positive for trailing")
		}
		return nil
	default:
		return fmt.Errorf("unsupported risk.type %q", r.Type)
	}
}

func (o OrderSettings) Normalize() OrderSettings {
	o.OrderType = OrderType(strings.ToLower(strings.TrimSpace(string(o.OrderType))))
	if o.OrderType == "" {
		o.OrderType = OrderTypeMarket
	}
	if !o.Amount.Set || o.Amount.Value <= 0 {
		o.Amount = NewFlexibleFloat(100)
	}
	if o.Leverage <= 0 {
		o.Leverage = 5
	}
	o.Risk.Normalize()
	if o.Risk.Type == RiskTPSL {
		if o.Risk.TPPct == nil || !o.Risk.TPPct.Set || o.Risk.TPPct.Value <= 0 {
			v := NewFlexibleFloat(2)
			o.Risk.TPPct = &v
		}
		if o.Risk.SLPct == nil || !o.Risk.SLPct.Set || o.Risk.SLPct.Value <= 0 {
			v := NewFlexibleFloat(1)
			o.Risk.SLPct = &v
		}
	}
	if o.Risk.Type == RiskTrailing && (o.Risk.TrailingPct == nil || !o.Risk.TrailingPct.Set || o.Risk.TrailingPct.Value <= 0) {
		v := NewFlexibleFloat(1)
		o.Risk.TrailingPct = &v
	}
	if o.LongLimitPriceMultiplier <= 0 {
		o.LongLimitPriceMultiplier = 0.997
	}
	if o.ShortLimitPriceMultiplier <= 0 {
		o.ShortLimitPriceMultiplier = 1.003
	}
	return o
}

func (o OrderSettings) ApplyToSignal(signal *Signal) {
	o = o.Normalize()
	if signal.Leverage <= 0 {
		signal.Leverage = o.Leverage
	}
	if !signal.Amount.Set || signal.Amount.Value <= 0 {
		signal.Amount = o.Amount
	}
	signal.Risk = o.Risk
	signal.Risk.Normalize()
}

func (o OrderSettings) LimitPrice(action Side, currentPrice float64) float64 {
	o = o.Normalize()
	if action == ActionShort {
		return currentPrice * o.ShortLimitPriceMultiplier
	}
	return currentPrice * o.LongLimitPriceMultiplier
}

func (s Signal) Validate(now time.Time, ttl time.Duration, cfg RuntimeConfig) error {
	s.Normalize()
	switch s.Action {
	case ActionLong, ActionShort:
	default:
		return fmt.Errorf("action must be %q or %q", ActionLong, ActionShort)
	}
	if !ValidTargetExchange(s.TargetExchange) {
		return fmt.Errorf("target_exchange must be %q or %q", ExchangeOKX, ExchangeBinance)
	}
	if !ValidTradeEnv(s.TradeEnv) {
		return fmt.Errorf("trade_env must be %q or %q", TradeEnvDemo, TradeEnvLive)
	}
	if s.Coinpair == "" && strings.TrimSpace(s.Ticker) == "" {
		return errors.New("coinpair or ticker is required")
	}
	if !s.Price.Set || s.Price.Value <= 0 {
		return errors.New("price must be positive")
	}
	if s.Leverage <= 0 {
		return errors.New("leverage must be positive")
	}
	if !s.Amount.Set || s.Amount.Value <= 0 {
		return errors.New("amount must be positive")
	}
	if err := s.Risk.Validate(); err != nil {
		return err
	}
	sentAt, err := ParseTradingViewTime(s.SentAt)
	if err != nil {
		return err
	}
	age := now.UTC().Sub(sentAt)
	if age < -30*time.Second {
		return errors.New("sent_at is too far in the future")
	}
	if ttl > 0 && age > ttl {
		return errors.New("sent_at is expired")
	}
	return nil
}

func ParseTradingViewTime(v string) (time.Time, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Time{}, errors.New("sent_at is required")
	}
	if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
		return t.UTC(), nil
	}
	if ms, err := strconv.ParseInt(v, 10, 64); err == nil {
		if ms > 1_000_000_000_000 {
			return time.UnixMilli(ms).UTC(), nil
		}
		return time.Unix(ms, 0).UTC(), nil
	}
	return time.Time{}, fmt.Errorf("sent_at %q must be RFC3339 or unix timestamp", v)
}

func NormalizeFloat(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "0"
	}
	s := strconv.FormatFloat(v, 'f', 8, 64)
	s = strings.TrimRight(s, "0")
	s = strings.TrimRight(s, ".")
	if s == "" || s == "-0" {
		return "0"
	}
	return s
}

func CanonicalRisk(r Risk) string {
	r.Normalize()
	switch r.Type {
	case RiskTPSL:
		return strings.Join([]string{
			string(r.Type),
			NormalizeOptionalFloat(r.TPPct),
			NormalizeOptionalFloat(r.SLPct),
		}, "|")
	case RiskTrailing:
		return strings.Join([]string{
			string(r.Type),
			NormalizeOptionalFloat(r.TrailingPct),
		}, "|")
	default:
		return string(RiskNone)
	}
}

func NormalizeOptionalFloat(v *FlexibleFloat) string {
	if v == nil || !v.Set {
		return ""
	}
	return NormalizeFloat(v.Value)
}

func CanonicalTokenPayload(leverage int, amount FlexibleFloat, apiID string) string {
	apiID = strings.TrimSpace(apiID)
	if apiID != "" {
		return strings.Join([]string{
			"v3",
			apiID,
			strconv.Itoa(leverage),
			NormalizeFloat(amount.Value),
		}, "\n")
	}
	return strings.Join([]string{
		"v2",
		strconv.Itoa(leverage),
		NormalizeFloat(amount.Value),
	}, "\n")
}

func CanonicalWebhookTokenPayload(apiID string) string {
	apiID = strings.TrimSpace(apiID)
	if apiID == "" {
		return "v4"
	}
	return strings.Join([]string{"v4", apiID}, "\n")
}

func (s Signal) CanonicalTokenPayload() string {
	return CanonicalTokenPayload(s.Leverage, s.Amount, s.APIID)
}

func (s Signal) CanonicalWebhookTokenPayload() string {
	return CanonicalTargetTradeEnvWebhookTokenPayload(s.TargetExchange, s.APIID, s.TradeEnv)
}

func (s Signal) CanonicalNonceWebhookTokenPayload() string {
	return CanonicalTargetTradeEnvWebhookTokenPayloadWithNonce(s.TargetExchange, s.APIID, s.TradeEnv, s.TokenNonce)
}

func (t TemplateRequest) CanonicalTokenPayload() string {
	tradeEnv := NormalizeTradeEnv(t.TradeEnv)
	if tradeEnv == "" {
		tradeEnv = TradeEnvDemo
	}
	return CanonicalTargetTradeEnvWebhookTokenPayload(t.TargetExchange, t.APIID, tradeEnv)
}

func CanonicalTargetWebhookTokenPayloadWithNonce(targetExchange, apiID, tokenNonce string) string {
	tokenNonce = strings.TrimSpace(tokenNonce)
	if tokenNonce == "" {
		return CanonicalTargetWebhookTokenPayload(targetExchange, apiID)
	}
	return strings.Join([]string{
		"v6",
		NormalizeExchange(targetExchange),
		strings.TrimSpace(apiID),
		tokenNonce,
	}, "\n")
}

func CanonicalTargetTradeEnvWebhookTokenPayloadWithNonce(targetExchange, apiID, tradeEnv, tokenNonce string) string {
	tradeEnv = NormalizeTradeEnv(tradeEnv)
	tokenNonce = strings.TrimSpace(tokenNonce)
	if tradeEnv == "" {
		return CanonicalTargetWebhookTokenPayloadWithNonce(targetExchange, apiID, tokenNonce)
	}
	if tokenNonce == "" {
		return CanonicalTargetTradeEnvWebhookTokenPayload(targetExchange, apiID, tradeEnv)
	}
	return strings.Join([]string{
		"v7",
		NormalizeExchange(targetExchange),
		strings.TrimSpace(apiID),
		tradeEnv,
		tokenNonce,
	}, "\n")
}

func CanonicalTargetWebhookTokenPayload(targetExchange, apiID string) string {
	targetExchange = NormalizeExchange(targetExchange)
	apiID = strings.TrimSpace(apiID)
	if targetExchange == ExchangeOKX {
		return CanonicalWebhookTokenPayload(apiID)
	}
	if apiID == "" {
		return strings.Join([]string{"v5", targetExchange}, "\n")
	}
	return strings.Join([]string{"v5", targetExchange, apiID}, "\n")
}

func CanonicalTargetTradeEnvWebhookTokenPayload(targetExchange, apiID, tradeEnv string) string {
	tradeEnv = NormalizeTradeEnv(tradeEnv)
	if tradeEnv == "" {
		return CanonicalTargetWebhookTokenPayload(targetExchange, apiID)
	}
	return strings.Join([]string{
		"v7",
		NormalizeExchange(targetExchange),
		strings.TrimSpace(apiID),
		tradeEnv,
	}, "\n")
}
