package app

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/config"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

type steadyWebhookPayload struct {
	SentAt         string `json:"sent_at"`
	APIID          string `json:"api_id"`
	TargetExchange string `json:"target_exchange"`
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

func buildSteadyWebhookPayload(signal domain.TradeSignal, cfg config.Config) ([]byte, error) {
	action, intent, err := steadyWebhookDirection(signal.Side)
	if err != nil {
		return nil, err
	}
	price := strconv.FormatFloat(signal.ReferencePrice, 'f', -1, 64)
	interval := strings.TrimSpace(cfg.SteadyWebhookInterval)
	if interval == "" {
		interval = fmt.Sprintf("%dm", signal.WindowMinutes)
	}
	payload := steadyWebhookPayload{
		SentAt: signal.CreatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
		APIID:  cfg.SteadyWebhookAPIID, TargetExchange: cfg.SteadyWebhookTargetExchange,
		TradeEnv: cfg.SteadyWebhookTradeEnv, Action: action,
		Ticker: signal.Symbol, Coinpair: signal.Symbol, Price: price,
		Exchange: cfg.SteadyWebhookExchange, Interval: interval,
		Condition: "steady_liquidation_confirmation", Text: intent,
		OrderIntent: intent, Source: "tradingview",
		TokenNonce: cfg.SteadyWebhookTokenNonce, Token: cfg.SteadyWebhookToken,
	}
	return json.Marshal(payload)
}

func steadyWebhookDirection(side string) (action, intent string, err error) {
	switch strings.ToLower(strings.TrimSpace(side)) {
	case "long":
		return "buy", "entry_long", nil
	case "short":
		return "sell", "entry_short", nil
	default:
		return "", "", fmt.Errorf("unsupported steady signal side %q", side)
	}
}
