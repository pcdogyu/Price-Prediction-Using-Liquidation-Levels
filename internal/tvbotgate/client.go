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
	Volume24hQuote   string `json:"volume_24h_quote"`
	Volume24hBase    string `json:"volume_24h_base"`
	ChangePercentage string `json:"change_percentage"`
}
type Position struct {
	Contract      string `json:"contract"`
	Size          string `json:"size"`
	Leverage      string `json:"leverage"`
	EntryPrice    string `json:"entry_price"`
	MarkPrice     string `json:"mark_price"`
	UnrealisedPnl string `json:"unrealised_pnl"`
	Mode          string `json:"mode"`
	MarginMode    string `json:"pos_margin_mode"`
	Lever         string `json:"lever"`
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
func (c Client) OpenOrders(ctx context.Context, contract string) ([]Order, error) {
	q := url.Values{"status": []string{"open"}}
	if strings.TrimSpace(contract) != "" {
		q.Set("contract", contract)
	}
	var out []Order
	err := c.Do(ctx, http.MethodGet, "/futures/usdt/orders", q, nil, true, &out)
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
