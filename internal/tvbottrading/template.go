package trading

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

type TokenGenerator interface {
	Generate(payload string) string
}

type alertTemplatePayload struct {
	SentAt         string `json:"sent_at"`
	APIID          string `json:"api_id,omitempty"`
	TargetExchange string `json:"target_exchange,omitempty"`
	TradeEnv       string `json:"trade_env"`
	Action         string `json:"action"`
	Ticker         string `json:"ticker"`
	Coinpair       string `json:"coinpair"`
	Price          string `json:"price"`
	Exchange       string `json:"exchange"`
	Interval       string `json:"interval"`
	Condition      string `json:"condition"`
	Text           string `json:"text"`
	OrderIntent    string `json:"order_intent"`
	Source         string `json:"source"`
	TokenNonce     string `json:"token_nonce"`
	Token          string `json:"token"`
}

const (
	templateDynamicAction = "{{strategy.order.action}}"
	templateDynamicTicker = "{{ticker}}"
)

func BuildTemplate(req TemplateRequest, generator TokenGenerator) (TemplateResponse, error) {
	req.PriceSource = strings.ToLower(strings.TrimSpace(req.PriceSource))
	req.APIID = strings.TrimSpace(req.APIID)
	req.TargetExchange = NormalizeExchange(req.TargetExchange)
	req.TradeEnv = NormalizeTradeEnv(req.TradeEnv)
	req.Coinpair = templateCoinpair(req.Coinpair)
	if req.PriceSource == "" {
		req.PriceSource = "close"
	}
	if req.TradeEnv == "" {
		req.TradeEnv = TradeEnvDemo
	}
	if !ValidTargetExchange(req.TargetExchange) {
		return TemplateResponse{}, fmt.Errorf("target_exchange must be okx or binance")
	}
	if !ValidTradeEnv(req.TradeEnv) {
		return TemplateResponse{}, fmt.Errorf("trade_env must be demo or live")
	}
	if req.PriceSource != "close" && req.PriceSource != "high" && req.PriceSource != "low" {
		return TemplateResponse{}, fmt.Errorf("price_source must be close, high or low")
	}
	action, err := templateAction(req.Direction)
	if err != nil {
		return TemplateResponse{}, err
	}
	signal := Signal{
		Action:         Side(action),
		APIID:          req.APIID,
		TargetExchange: req.TargetExchange,
		TradeEnv:       req.TradeEnv,
		Coinpair:       req.Coinpair,
		Price:          NewFlexibleFloat(0),
		SentAt:         "{{timenow}}",
		Ticker:         req.Coinpair,
		Leverage:       req.Leverage,
		Amount:         req.Amount,
	}
	tokenNonce, err := newTemplateTokenNonce()
	if err != nil {
		return TemplateResponse{}, err
	}
	signal.TokenNonce = tokenNonce
	token := generator.Generate(signal.CanonicalNonceWebhookTokenPayload())
	payload := alertTemplatePayload{
		SentAt:         signal.SentAt,
		APIID:          req.APIID,
		TargetExchange: req.TargetExchange,
		TradeEnv:       req.TradeEnv,
		Action:         action,
		Ticker:         signal.Ticker,
		Coinpair:       signal.Coinpair,
		Price:          "{{" + req.PriceSource + "}}",
		Exchange:       "{{exchange}}",
		Interval:       "{{interval}}",
		Condition:      "{{strategy.order.comment}}",
		Text:           "{{strategy.order.alert_message}}",
		OrderIntent:    "{{strategy.order.alert_message}}",
		Source:         "tradingview",
		TokenNonce:     tokenNonce,
		Token:          token,
	}
	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return TemplateResponse{}, err
	}
	return TemplateResponse{JSON: string(b), Token: token, TokenNonce: tokenNonce}, nil
}

func templateCoinpair(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return templateDynamicTicker
	}
	return strings.ToUpper(raw)
}

func templateAction(rawDirection string) (string, error) {
	direction := strings.ToLower(strings.TrimSpace(rawDirection))
	direction = strings.ReplaceAll(direction, "-", "_")
	switch direction {
	case "", "both", "all", "long_short", "both_sides", "多空都做":
		return templateDynamicAction, nil
	case "long", "long_only", "only_long", "buy", "只做多":
		return "buy", nil
	case "short", "short_only", "only_short", "sell", "只做空":
		return "sell", nil
	default:
		return "", fmt.Errorf("direction must be both, long or short")
	}
}

func newTemplateTokenNonce() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate token nonce: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}
