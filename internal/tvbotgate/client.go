package gate

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Credentials struct {
	APIKey    string
	SecretKey string
}

func (c Credentials) Validate() error {
	if strings.TrimSpace(c.APIKey) == "" || strings.TrimSpace(c.SecretKey) == "" {
		return errors.New("Gate API credentials are incomplete")
	}
	return nil
}

type Client struct {
	BaseURL     string
	Credentials Credentials
	HTTPClient  *http.Client
	Now         func() time.Time
}
type APIError struct {
	Label   string `json:"label"`
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"`
	Status  int    `json:"-"`
}

func (e APIError) Error() string {
	parts := []string{}
	if e.Label != "" {
		parts = append(parts, e.Label)
	}
	if e.Message != "" {
		parts = append(parts, e.Message)
	}
	if e.Detail != "" && e.Detail != e.Message {
		parts = append(parts, e.Detail)
	}
	if len(parts) == 0 {
		return fmt.Sprintf("Gate API error (HTTP %d)", e.Status)
	}
	return "Gate API error: " + strings.Join(parts, ": ")
}

type Account struct {
	User          int64  `json:"user"`
	Currency      string `json:"currency"`
	Total         string `json:"total"`
	UnrealisedPnl string `json:"unrealised_pnl"`
	Available     string `json:"available"`
	PositionMode  string `json:"position_mode"`
	InDualMode    bool   `json:"in_dual_mode"`
}
type Contract struct {
	Name             string `json:"name"`
	Type             string `json:"type"`
	QuantoMultiplier string `json:"quanto_multiplier"`
	OrderSizeMin     string `json:"order_size_min"`
	OrderSizeMax     string `json:"order_size_max"`
	OrderPriceRound  string `json:"order_price_round"`
	MarkPrice        string `json:"mark_price"`
	LastPrice        string `json:"last_price"`
	Status           string `json:"status"`
	EnableDecimal    bool   `json:"enable_decimal"`
}

func (c *Contract) UnmarshalJSON(data []byte) error {
	var raw struct {
		Name             json.RawMessage `json:"name"`
		Type             json.RawMessage `json:"type"`
		QuantoMultiplier json.RawMessage `json:"quanto_multiplier"`
		OrderSizeMin     json.RawMessage `json:"order_size_min"`
		OrderSizeMax     json.RawMessage `json:"order_size_max"`
		OrderPriceRound  json.RawMessage `json:"order_price_round"`
		MarkPrice        json.RawMessage `json:"mark_price"`
		LastPrice        json.RawMessage `json:"last_price"`
		Status           json.RawMessage `json:"status"`
		EnableDecimal    bool            `json:"enable_decimal"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	fields := []struct {
		raw *json.RawMessage
		out *string
	}{
		{&raw.Name, &c.Name}, {&raw.Type, &c.Type}, {&raw.QuantoMultiplier, &c.QuantoMultiplier},
		{&raw.OrderSizeMin, &c.OrderSizeMin}, {&raw.OrderSizeMax, &c.OrderSizeMax},
		{&raw.OrderPriceRound, &c.OrderPriceRound}, {&raw.MarkPrice, &c.MarkPrice},
		{&raw.LastPrice, &c.LastPrice}, {&raw.Status, &c.Status},
	}
	for _, field := range fields {
		value, err := gateJSONText(*field.raw)
		if err != nil {
			return err
		}
		*field.out = value
	}
	c.EnableDecimal = raw.EnableDecimal
	return nil
}

func gateJSONText(raw json.RawMessage) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return "", nil
	}
	var text string
	if trimmed[0] == '"' {
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return "", err
		}
		return text, nil
	}
	var number json.Number
	if err := json.Unmarshal(trimmed, &number); err != nil {
		return "", fmt.Errorf("decode Gate numeric value: %w", err)
	}
	return number.String(), nil
}

type Ticker struct {
	Contract         string `json:"contract"`
	Last             string `json:"last"`
	HighestBid       string `json:"highest_bid"`
	LowestAsk        string `json:"lowest_ask"`
	Volume24hQuote   string `json:"volume_24h_quote"`
	Volume24hBase    string `json:"volume_24h_base"`
	ChangePercentage string `json:"change_percentage"`
}

func (t *Ticker) UnmarshalJSON(data []byte) error {
	var raw struct {
		Contract         json.RawMessage `json:"contract"`
		Last             json.RawMessage `json:"last"`
		HighestBid       json.RawMessage `json:"highest_bid"`
		LowestAsk        json.RawMessage `json:"lowest_ask"`
		Volume24hQuote   json.RawMessage `json:"volume_24h_quote"`
		Volume24hBase    json.RawMessage `json:"volume_24h_base"`
		ChangePercentage json.RawMessage `json:"change_percentage"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	fields := []struct {
		raw *json.RawMessage
		out *string
	}{
		{&raw.Contract, &t.Contract}, {&raw.Last, &t.Last}, {&raw.HighestBid, &t.HighestBid},
		{&raw.LowestAsk, &t.LowestAsk}, {&raw.Volume24hQuote, &t.Volume24hQuote},
		{&raw.Volume24hBase, &t.Volume24hBase}, {&raw.ChangePercentage, &t.ChangePercentage},
	}
	for _, field := range fields {
		value, err := gateJSONText(*field.raw)
		if err != nil {
			return err
		}
		*field.out = value
	}
	return nil
}

type Position struct {
	Contract         string `json:"contract"`
	Size             string `json:"size"`
	Leverage         string `json:"leverage"`
	Value            string `json:"value"`
	Margin           string `json:"margin"`
	InitialMargin    string `json:"initial_margin"`
	EntryPrice       string `json:"entry_price"`
	MarkPrice        string `json:"mark_price"`
	LiquidationPrice string `json:"liq_price"`
	UnrealisedPnl    string `json:"unrealised_pnl"`
	Mode             string `json:"mode"`
	MarginMode       string `json:"pos_margin_mode"`
	Lever            string `json:"lever"`
	UpdateTime       string `json:"update_time"`
	OpenTime         string `json:"open_time"`
}

// UnmarshalJSON accepts both the string and number representations returned by
// different Gate API deployments for decimal position fields. Gate's futures
// position endpoint currently returns size and some related values as JSON
// numbers, while other endpoints and older responses use strings.
func (p *Position) UnmarshalJSON(data []byte) error {
	var raw struct {
		Contract         json.RawMessage `json:"contract"`
		Size             json.RawMessage `json:"size"`
		Leverage         json.RawMessage `json:"leverage"`
		Value            json.RawMessage `json:"value"`
		Margin           json.RawMessage `json:"margin"`
		InitialMargin    json.RawMessage `json:"initial_margin"`
		EntryPrice       json.RawMessage `json:"entry_price"`
		MarkPrice        json.RawMessage `json:"mark_price"`
		LiquidationPrice json.RawMessage `json:"liq_price"`
		UnrealisedPnl    json.RawMessage `json:"unrealised_pnl"`
		Mode             json.RawMessage `json:"mode"`
		MarginMode       json.RawMessage `json:"pos_margin_mode"`
		Lever            json.RawMessage `json:"lever"`
		UpdateTime       json.RawMessage `json:"update_time"`
		OpenTime         json.RawMessage `json:"open_time"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	fields := []struct {
		raw *json.RawMessage
		out *string
	}{
		{&raw.Contract, &p.Contract}, {&raw.Size, &p.Size}, {&raw.Leverage, &p.Leverage},
		{&raw.Value, &p.Value}, {&raw.Margin, &p.Margin}, {&raw.InitialMargin, &p.InitialMargin},
		{&raw.EntryPrice, &p.EntryPrice}, {&raw.MarkPrice, &p.MarkPrice}, {&raw.LiquidationPrice, &p.LiquidationPrice},
		{&raw.UnrealisedPnl, &p.UnrealisedPnl}, {&raw.Mode, &p.Mode}, {&raw.MarginMode, &p.MarginMode},
		{&raw.Lever, &p.Lever}, {&raw.UpdateTime, &p.UpdateTime}, {&raw.OpenTime, &p.OpenTime},
	}
	for _, field := range fields {
		value, err := gateJSONText(*field.raw)
		if err != nil {
			return err
		}
		*field.out = value
	}
	return nil
}

// MyTrade is a private Gate futures fill. Gate returns numeric fields as both
// JSON numbers and strings depending on the endpoint/version, so all fields
// used by the position view are normalized to text here.
type MyTrade struct {
	ID         string `json:"id"`
	Contract   string `json:"contract"`
	OrderID    string `json:"order_id"`
	Size       string `json:"size"`
	Price      string `json:"price"`
	CreateTime string `json:"create_time"`
	Text       string `json:"text"`
	CloseSize  string `json:"close_size"`
	TradeValue string `json:"trade_value"`
	Fee        string `json:"fee"`
	PointFee   string `json:"point_fee"`
	Role       string `json:"role"`
}

func (t *MyTrade) UnmarshalJSON(data []byte) error {
	var raw struct {
		ID         json.RawMessage `json:"id"`
		Contract   json.RawMessage `json:"contract"`
		OrderID    json.RawMessage `json:"order_id"`
		Size       json.RawMessage `json:"size"`
		Price      json.RawMessage `json:"price"`
		CreateTime json.RawMessage `json:"create_time"`
		Text       json.RawMessage `json:"text"`
		CloseSize  json.RawMessage `json:"close_size"`
		TradeValue json.RawMessage `json:"trade_value"`
		Fee        json.RawMessage `json:"fee"`
		PointFee   json.RawMessage `json:"point_fee"`
		Role       json.RawMessage `json:"role"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	fields := []struct {
		raw *json.RawMessage
		out *string
	}{
		{&raw.ID, &t.ID}, {&raw.Contract, &t.Contract}, {&raw.OrderID, &t.OrderID},
		{&raw.Size, &t.Size}, {&raw.Price, &t.Price}, {&raw.CreateTime, &t.CreateTime},
		{&raw.Text, &t.Text}, {&raw.CloseSize, &t.CloseSize}, {&raw.TradeValue, &t.TradeValue},
		{&raw.Fee, &t.Fee}, {&raw.PointFee, &t.PointFee}, {&raw.Role, &t.Role},
	}
	for _, field := range fields {
		value, err := gateJSONText(*field.raw)
		if err != nil {
			return err
		}
		*field.out = value
	}
	return nil
}

// PositionClose is a Gate futures position-close record. Gate returns the
// decimal and timestamp fields as either JSON numbers or strings depending on
// the endpoint/version, so all values used by the analysis layer are kept as
// normalized text.
type PositionClose struct {
	Time          string `json:"time"`
	Contract      string `json:"contract"`
	Side          string `json:"side"`
	Pnl           string `json:"pnl"`
	PnlPnl        string `json:"pnl_pnl"`
	PnlFund       string `json:"pnl_fund"`
	PnlFee        string `json:"pnl_fee"`
	Text          string `json:"text"`
	MaxSize       string `json:"max_size"`
	AccumSize     string `json:"accum_size"`
	FirstOpenTime string `json:"first_open_time"`
	LongPrice     string `json:"long_price"`
	ShortPrice    string `json:"short_price"`
}

func (p *PositionClose) UnmarshalJSON(data []byte) error {
	var raw struct {
		Time          json.RawMessage `json:"time"`
		Contract      json.RawMessage `json:"contract"`
		Side          json.RawMessage `json:"side"`
		Pnl           json.RawMessage `json:"pnl"`
		PnlPnl        json.RawMessage `json:"pnl_pnl"`
		PnlFund       json.RawMessage `json:"pnl_fund"`
		PnlFee        json.RawMessage `json:"pnl_fee"`
		Text          json.RawMessage `json:"text"`
		MaxSize       json.RawMessage `json:"max_size"`
		AccumSize     json.RawMessage `json:"accum_size"`
		FirstOpenTime json.RawMessage `json:"first_open_time"`
		LongPrice     json.RawMessage `json:"long_price"`
		ShortPrice    json.RawMessage `json:"short_price"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	fields := []struct {
		raw *json.RawMessage
		out *string
	}{
		{&raw.Time, &p.Time}, {&raw.Contract, &p.Contract}, {&raw.Side, &p.Side},
		{&raw.Pnl, &p.Pnl}, {&raw.PnlPnl, &p.PnlPnl}, {&raw.PnlFund, &p.PnlFund},
		{&raw.PnlFee, &p.PnlFee}, {&raw.Text, &p.Text}, {&raw.MaxSize, &p.MaxSize},
		{&raw.AccumSize, &p.AccumSize}, {&raw.FirstOpenTime, &p.FirstOpenTime},
		{&raw.LongPrice, &p.LongPrice}, {&raw.ShortPrice, &p.ShortPrice},
	}
	for _, field := range fields {
		value, err := gateJSONText(*field.raw)
		if err != nil {
			return err
		}
		*field.out = value
	}
	return nil
}

type Order struct {
	ID           json.RawMessage `json:"id"`
	Contract     string          `json:"contract"`
	Size         string          `json:"size"`
	Left         string          `json:"left"`
	Price        string          `json:"price"`
	FillPrice    string          `json:"fill_price"`
	Status       string          `json:"status"`
	FinishAs     string          `json:"finish_as"`
	Text         string          `json:"text"`
	IsReduceOnly bool            `json:"is_reduce_only"`
	IsClose      bool            `json:"is_close"`
	CreateTime   string          `json:"create_time"`
	UpdateTime   string          `json:"update_time"`
}

// UnmarshalJSON accepts both the string and number representations returned by
// different Gate API deployments for order fields. In particular, the futures
// open-orders endpoint returns size and left as JSON numbers, while price and
// time fields may be strings or numbers depending on the response.
func (o *Order) UnmarshalJSON(data []byte) error {
	var raw struct {
		ID           json.RawMessage `json:"id"`
		Contract     json.RawMessage `json:"contract"`
		Size         json.RawMessage `json:"size"`
		Left         json.RawMessage `json:"left"`
		Price        json.RawMessage `json:"price"`
		FillPrice    json.RawMessage `json:"fill_price"`
		Status       json.RawMessage `json:"status"`
		FinishAs     json.RawMessage `json:"finish_as"`
		Text         json.RawMessage `json:"text"`
		IsReduceOnly bool            `json:"is_reduce_only"`
		IsClose      bool            `json:"is_close"`
		CreateTime   json.RawMessage `json:"create_time"`
		UpdateTime   json.RawMessage `json:"update_time"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	fields := []struct {
		raw *json.RawMessage
		out *string
	}{
		{&raw.Contract, &o.Contract}, {&raw.Size, &o.Size}, {&raw.Left, &o.Left},
		{&raw.Price, &o.Price}, {&raw.FillPrice, &o.FillPrice}, {&raw.Status, &o.Status},
		{&raw.FinishAs, &o.FinishAs}, {&raw.Text, &o.Text}, {&raw.CreateTime, &o.CreateTime},
		{&raw.UpdateTime, &o.UpdateTime},
	}
	for _, field := range fields {
		value, err := gateJSONText(*field.raw)
		if err != nil {
			return err
		}
		*field.out = value
	}
	o.ID = raw.ID
	o.IsReduceOnly = raw.IsReduceOnly
	o.IsClose = raw.IsClose
	return nil
}

func (o Order) IDString() string { return rawString(o.ID) }
func rawString(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	var s string
	if raw[0] == '"' {
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
	}
	return strings.Trim(string(raw), "\"")
}

// PriceTriggeredInitial is the order that Gate will submit after a price
// trigger fires. Gate returns size as an integer for most contracts and amount
// as a decimal string for decimal contracts, so both are normalized to text.
type PriceTriggeredInitial struct {
	Contract     string `json:"contract"`
	Size         string `json:"size"`
	Amount       string `json:"amount"`
	Price        string `json:"price"`
	TIF          string `json:"tif"`
	Text         string `json:"text"`
	ReduceOnly   bool   `json:"reduce_only"`
	IsReduceOnly bool   `json:"is_reduce_only"`
	IsClose      bool   `json:"is_close"`
}

func (p *PriceTriggeredInitial) UnmarshalJSON(data []byte) error {
	var raw struct {
		Contract     json.RawMessage `json:"contract"`
		Size         json.RawMessage `json:"size"`
		Amount       json.RawMessage `json:"amount"`
		Price        json.RawMessage `json:"price"`
		TIF          json.RawMessage `json:"tif"`
		Text         json.RawMessage `json:"text"`
		ReduceOnly   bool            `json:"reduce_only"`
		IsReduceOnly bool            `json:"is_reduce_only"`
		IsClose      bool            `json:"is_close"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	fields := []struct {
		raw *json.RawMessage
		out *string
	}{
		{&raw.Contract, &p.Contract}, {&raw.Size, &p.Size}, {&raw.Amount, &p.Amount},
		{&raw.Price, &p.Price}, {&raw.TIF, &p.TIF}, {&raw.Text, &p.Text},
	}
	for _, field := range fields {
		value, err := gateJSONText(*field.raw)
		if err != nil {
			return err
		}
		*field.out = value
	}
	p.ReduceOnly = raw.ReduceOnly
	p.IsReduceOnly = raw.IsReduceOnly
	p.IsClose = raw.IsClose
	return nil
}

// PriceTriggeredTrigger contains the Gate trigger condition for an auto
// order. The display layer only needs the normalized trigger price and rule,
// but retaining the other fields keeps the client response useful for future
// trigger-order operations.
type PriceTriggeredTrigger struct {
	StrategyType int64  `json:"strategy_type"`
	PriceType    int64  `json:"price_type"`
	Price        string `json:"price"`
	Rule         int64  `json:"rule"`
	Expiration   int64  `json:"expiration"`
}

func (p *PriceTriggeredTrigger) UnmarshalJSON(data []byte) error {
	var raw struct {
		StrategyType json.RawMessage `json:"strategy_type"`
		PriceType    json.RawMessage `json:"price_type"`
		Price        json.RawMessage `json:"price"`
		Rule         json.RawMessage `json:"rule"`
		Expiration   json.RawMessage `json:"expiration"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	parseInt := func(value json.RawMessage) (int64, error) {
		text, err := gateJSONText(value)
		if err != nil || text == "" {
			return 0, err
		}
		return strconv.ParseInt(text, 10, 64)
	}
	var err error
	if p.StrategyType, err = parseInt(raw.StrategyType); err != nil {
		return err
	}
	if p.PriceType, err = parseInt(raw.PriceType); err != nil {
		return err
	}
	if p.Price, err = gateJSONText(raw.Price); err != nil {
		return err
	}
	if p.Rule, err = parseInt(raw.Rule); err != nil {
		return err
	}
	if p.Expiration, err = parseInt(raw.Expiration); err != nil {
		return err
	}
	return nil
}

// PriceTriggeredOrder is Gate's futures auto/price-triggered order model.
// These are the exchange-side TP/SL records that must be queried separately
// from ordinary futures orders.
type PriceTriggeredOrder struct {
	Initial       PriceTriggeredInitial `json:"initial"`
	Trigger       PriceTriggeredTrigger `json:"trigger"`
	ID            json.RawMessage       `json:"id"`
	IDStringValue json.RawMessage       `json:"id_string"`
	CreateTime    string                `json:"create_time"`
	FinishTime    string                `json:"finish_time"`
	Status        string                `json:"status"`
	FinishAs      string                `json:"finish_as"`
	Reason        string                `json:"reason"`
	OrderType     string                `json:"order_type"`
	PosMarginMode string                `json:"pos_margin_mode"`
}

func (p *PriceTriggeredOrder) UnmarshalJSON(data []byte) error {
	var raw struct {
		Initial       PriceTriggeredInitial `json:"initial"`
		Trigger       PriceTriggeredTrigger `json:"trigger"`
		ID            json.RawMessage       `json:"id"`
		IDString      json.RawMessage       `json:"id_string"`
		CreateTime    json.RawMessage       `json:"create_time"`
		FinishTime    json.RawMessage       `json:"finish_time"`
		Status        json.RawMessage       `json:"status"`
		FinishAs      json.RawMessage       `json:"finish_as"`
		Reason        json.RawMessage       `json:"reason"`
		OrderType     json.RawMessage       `json:"order_type"`
		PosMarginMode json.RawMessage       `json:"pos_margin_mode"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	fields := []struct {
		raw *json.RawMessage
		out *string
	}{
		{&raw.CreateTime, &p.CreateTime}, {&raw.FinishTime, &p.FinishTime},
		{&raw.Status, &p.Status}, {&raw.FinishAs, &p.FinishAs}, {&raw.Reason, &p.Reason},
		{&raw.OrderType, &p.OrderType}, {&raw.PosMarginMode, &p.PosMarginMode},
	}
	for _, field := range fields {
		value, err := gateJSONText(*field.raw)
		if err != nil {
			return err
		}
		*field.out = value
	}
	p.Initial = raw.Initial
	p.Trigger = raw.Trigger
	p.ID = raw.ID
	p.IDStringValue = raw.IDString
	return nil
}

func (p PriceTriggeredOrder) IDString() string {
	if id := rawString(p.IDStringValue); id != "" {
		return id
	}
	return rawString(p.ID)
}

type PlaceOrderRequest struct {
	Contract       string `json:"contract"`
	Size           string `json:"size"`
	Price          string `json:"price"`
	TIF            string `json:"tif,omitempty"`
	Text           string `json:"text,omitempty"`
	Close          bool   `json:"close,omitempty"`
	ReduceOnly     bool   `json:"reduce_only,omitempty"`
	PosMarginMode  string `json:"pos_margin_mode,omitempty"`
	TPTriggerPrice string `json:"tpsl_tp_trigger_price,omitempty"`
	SLTriggerPrice string `json:"tpsl_sl_trigger_price,omitempty"`
}

func (c Client) Account(ctx context.Context) (Account, error) {
	var out Account
	err := c.Do(ctx, http.MethodGet, "/futures/usdt/accounts", nil, nil, true, &out)
	return out, err
}
func (c Client) Contracts(ctx context.Context) ([]Contract, error) {
	var out []Contract
	err := c.Do(ctx, http.MethodGet, "/futures/usdt/contracts", nil, nil, false, &out)
	return out, err
}
func (c Client) Tickers(ctx context.Context) ([]Ticker, error) {
	var out []Ticker
	err := c.Do(ctx, http.MethodGet, "/futures/usdt/tickers", nil, nil, false, &out)
	return out, err
}
func (c Client) Ticker(ctx context.Context, contract string) (Ticker, error) {
	var out []Ticker
	q := url.Values{}
	if strings.TrimSpace(contract) != "" {
		q.Set("contract", contract)
	}
	if err := c.Do(ctx, http.MethodGet, "/futures/usdt/tickers", q, nil, false, &out); err != nil {
		return Ticker{}, err
	}
	if len(out) == 0 {
		return Ticker{}, fmt.Errorf("Gate ticker not found for %s", strings.TrimSpace(contract))
	}
	return out[0], nil
}
func (c Client) Contract(ctx context.Context, name string) (Contract, error) {
	var out Contract
	err := c.Do(ctx, http.MethodGet, "/futures/usdt/contracts/"+url.PathEscape(name), nil, nil, false, &out)
	return out, err
}
func (c Client) Positions(ctx context.Context, contract string) ([]Position, error) {
	q := url.Values{}
	if strings.TrimSpace(contract) != "" {
		q.Set("contract", contract)
	}
	var out []Position
	err := c.Do(ctx, http.MethodGet, "/futures/usdt/positions", q, nil, true, &out)
	return out, err
}
func (c Client) MyTrades(ctx context.Context, contract string) ([]MyTrade, error) {
	q := url.Values{"limit": []string{"1000"}}
	if strings.TrimSpace(contract) != "" {
		q.Set("contract", contract)
	}
	var out []MyTrade
	err := c.Do(ctx, http.MethodGet, "/futures/usdt/my_trades", q, nil, true, &out)
	return out, err
}

func gateTimeQuery(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return strconv.FormatInt(value.UTC().Unix(), 10)
}

func (c Client) MyTradesTimerange(ctx context.Context, contract string, from, to time.Time, limit, offset int) ([]MyTrade, error) {
	if limit <= 0 {
		limit = 1000
	}
	if offset < 0 {
		offset = 0
	}
	q := url.Values{
		"limit":  []string{strconv.Itoa(limit)},
		"offset": []string{strconv.Itoa(offset)},
	}
	if value := gateTimeQuery(from); value != "" {
		q.Set("from", value)
	}
	if value := gateTimeQuery(to); value != "" {
		q.Set("to", value)
	}
	if strings.TrimSpace(contract) != "" {
		q.Set("contract", contract)
	}
	var out []MyTrade
	err := c.Do(ctx, http.MethodGet, "/futures/usdt/my_trades_timerange", q, nil, true, &out)
	return out, err
}

func (c Client) PositionCloses(ctx context.Context, contract string, from, to time.Time, limit, offset int) ([]PositionClose, error) {
	if limit <= 0 {
		limit = 1000
	}
	if offset < 0 {
		offset = 0
	}
	q := url.Values{
		"limit":  []string{strconv.Itoa(limit)},
		"offset": []string{strconv.Itoa(offset)},
	}
	if value := gateTimeQuery(from); value != "" {
		q.Set("from", value)
	}
	if value := gateTimeQuery(to); value != "" {
		q.Set("to", value)
	}
	if strings.TrimSpace(contract) != "" {
		q.Set("contract", contract)
	}
	var out []PositionClose
	err := c.Do(ctx, http.MethodGet, "/futures/usdt/position_close", q, nil, true, &out)
	return out, err
}
func (c Client) OpenOrders(ctx context.Context, contract string) ([]Order, error) {
	q := url.Values{"status": []string{"open"}}
	if strings.TrimSpace(contract) != "" {
		q.Set("contract", contract)
	}
	var out []Order
	err := c.Do(ctx, http.MethodGet, "/futures/usdt/orders", q, nil, true, &out)
	return out, err
}

func (c Client) OpenPriceTriggeredOrders(ctx context.Context, contract string) ([]PriceTriggeredOrder, error) {
	q := url.Values{"status": []string{"open"}}
	if strings.TrimSpace(contract) != "" {
		q.Set("contract", contract)
	}
	var out []PriceTriggeredOrder
	err := c.Do(ctx, http.MethodGet, "/futures/usdt/price_orders", q, nil, true, &out)
	return out, err
}

func (c Client) CancelOrder(ctx context.Context, contract, orderID string) (Order, error) {
	var out Order
	err := c.Do(ctx, http.MethodDelete, "/futures/usdt/orders/"+url.PathEscape(strings.TrimSpace(orderID)), url.Values{"contract": []string{strings.TrimSpace(contract)}}, nil, true, &out)
	return out, err
}
func (c Client) SetLeverage(ctx context.Context, contract string, leverage int, marginMode string, dualSide string) (Position, error) {
	q := url.Values{}
	q.Set("leverage", strconv.Itoa(leverage))
	q.Set("margin_mode", marginMode)
	if dualSide != "" {
		q.Set("dual_side", dualSide)
	}
	var out Position
	err := c.Do(ctx, http.MethodPost, "/futures/usdt/positions/"+url.PathEscape(contract)+"/set_leverage", q, nil, true, &out)
	return out, err
}
func (c Client) PlaceOrder(ctx context.Context, req PlaceOrderRequest) (Order, error) {
	var out Order
	err := c.Do(ctx, http.MethodPost, "/futures/usdt/orders", nil, req, true, &out)
	return out, err
}

func (c Client) Do(ctx context.Context, method, endpoint string, query url.Values, body any, private bool, out any) error {
	base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	base = strings.TrimSuffix(base, "/api/v4")
	endpoint = "/" + strings.TrimLeft(endpoint, "/")
	signaturePath := "/api/v4" + endpoint
	queryString := ""
	if query != nil {
		queryString = query.Encode()
	}
	rawBody := []byte{}
	if body != nil {
		var err error
		rawBody, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	u := base + signaturePath
	if queryString != "" {
		u += "?" + queryString
	}
	req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(rawBody))
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	if private {
		if err := c.Credentials.Validate(); err != nil {
			return err
		}
		now := time.Now()
		if c.Now != nil {
			now = c.Now()
		}
		timestamp := strconv.FormatInt(now.Unix(), 10)
		hash := sha512.Sum512(rawBody)
		bodyHash := hex.EncodeToString(hash[:])
		signing := strings.Join([]string{method, signaturePath, queryString, bodyHash, timestamp}, "\n")
		mac := hmac.New(sha512.New, []byte(c.Credentials.SecretKey))
		_, _ = mac.Write([]byte(signing))
		req.Header.Set("KEY", c.Credentials.APIKey)
		req.Header.Set("SIGN", hex.EncodeToString(mac.Sum(nil)))
		req.Header.Set("Timestamp", timestamp)
	}
	hc := c.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if readErr != nil {
		return readErr
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var apiErr APIError
		_ = json.Unmarshal(data, &apiErr)
		apiErr.Status = resp.StatusCode
		if apiErr.Message == "" {
			apiErr.Message = strings.TrimSpace(string(data))
		}
		return apiErr
	}
	if out == nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decode Gate response: %w", err)
	}
	return nil
}
