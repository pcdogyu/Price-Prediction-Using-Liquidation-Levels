package binance

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
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
		return errors.New("Binance API credentials are incomplete")
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
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

func (e APIError) Error() string {
	if e.Code == 0 && e.Msg == "" {
		return "binance api error"
	}
	return fmt.Sprintf("binance code %d: %s", e.Code, e.Msg)
}

func IsAPIErrorCode(err error, codes ...int) bool {
	var apiErr APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	for _, code := range codes {
		if apiErr.Code == code {
			return true
		}
	}
	return false
}

func IsExecutionStatusUnknown(err error) bool {
	var apiErr APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	if apiErr.Code == -1007 {
		return true
	}
	msg := strings.ToLower(strings.TrimSpace(apiErr.Msg))
	return strings.Contains(msg, "execution status unknown") ||
		strings.Contains(msg, "send status unknown") ||
		strings.Contains(msg, "status unknown")
}

type Balance struct {
	AccountAlias       string `json:"accountAlias"`
	Asset              string `json:"asset"`
	Balance            string `json:"balance"`
	CrossWalletBalance string `json:"crossWalletBalance"`
	CrossUnPnl         string `json:"crossUnPnl"`
	AvailableBalance   string `json:"availableBalance"`
	MaxWithdrawAmount  string `json:"maxWithdrawAmount"`
	MarginAvailable    bool   `json:"marginAvailable"`
	UpdateTime         int64  `json:"updateTime"`
}

type Position struct {
	Symbol                 string `json:"symbol"`
	PositionSide           string `json:"positionSide"`
	PositionAmt            string `json:"positionAmt"`
	EntryPrice             string `json:"entryPrice"`
	BreakEvenPrice         string `json:"breakEvenPrice"`
	MarkPrice              string `json:"markPrice"`
	UnRealizedProfit       string `json:"unRealizedProfit"`
	LiquidationPrice       string `json:"liquidationPrice"`
	IsolatedMargin         string `json:"isolatedMargin"`
	Notional               string `json:"notional"`
	MarginAsset            string `json:"marginAsset"`
	IsolatedWallet         string `json:"isolatedWallet"`
	InitialMargin          string `json:"initialMargin"`
	MaintMargin            string `json:"maintMargin"`
	PositionInitialMargin  string `json:"positionInitialMargin"`
	OpenOrderInitialMargin string `json:"openOrderInitialMargin"`
	Adl                    int    `json:"adl"`
	BidNotional            string `json:"bidNotional"`
	AskNotional            string `json:"askNotional"`
	UpdateTime             int64  `json:"updateTime"`
	Leverage               string `json:"leverage,omitempty"`
	MarginType             string `json:"marginType,omitempty"`
}

type OpenOrder struct {
	AvgPrice      string `json:"avgPrice"`
	ClientOrderID string `json:"clientOrderId"`
	CumQuote      string `json:"cumQuote"`
	ExecutedQty   string `json:"executedQty"`
	OrderID       int64  `json:"orderId"`
	OrigQty       string `json:"origQty"`
	OrigType      string `json:"origType"`
	Price         string `json:"price"`
	ReduceOnly    bool   `json:"reduceOnly"`
	Side          string `json:"side"`
	PositionSide  string `json:"positionSide"`
	Status        string `json:"status"`
	StopPrice     string `json:"stopPrice"`
	ClosePosition bool   `json:"closePosition"`
	Symbol        string `json:"symbol"`
	Time          int64  `json:"time"`
	TimeInForce   string `json:"timeInForce"`
	Type          string `json:"type"`
	ActivatePrice string `json:"activatePrice"`
	PriceRate     string `json:"priceRate"`
	UpdateTime    int64  `json:"updateTime"`
	WorkingType   string `json:"workingType"`
	PriceProtect  bool   `json:"priceProtect"`
	PriceMatch    string `json:"priceMatch"`
}

type UserTrade struct {
	Buyer           bool   `json:"buyer"`
	Commission      string `json:"commission"`
	CommissionAsset string `json:"commissionAsset"`
	ID              int64  `json:"id"`
	Maker           bool   `json:"maker"`
	OrderID         int64  `json:"orderId"`
	Price           string `json:"price"`
	Qty             string `json:"qty"`
	QuoteQty        string `json:"quoteQty"`
	RealizedPnl     string `json:"realizedPnl"`
	Side            string `json:"side"`
	PositionSide    string `json:"positionSide"`
	Symbol          string `json:"symbol"`
	Time            int64  `json:"time"`
}

type Income struct {
	Symbol     string `json:"symbol"`
	IncomeType string `json:"incomeType"`
	Income     string `json:"income"`
	Asset      string `json:"asset"`
	Info       string `json:"info"`
	TranID     int64  `json:"tranId"`
	TradeID    string `json:"tradeId"`
	Time       int64  `json:"time"`
}

type BookTicker struct {
	Symbol   string `json:"symbol"`
	BidPrice string `json:"bidPrice"`
	BidQty   string `json:"bidQty"`
	AskPrice string `json:"askPrice"`
	AskQty   string `json:"askQty"`
	Time     int64  `json:"time"`
}

type PremiumIndex struct {
	Symbol          string `json:"symbol"`
	MarkPrice       string `json:"markPrice"`
	IndexPrice      string `json:"indexPrice"`
	LastFundingRate string `json:"lastFundingRate"`
	Time            int64  `json:"time"`
}

type Ticker24hr struct {
	Symbol      string `json:"symbol"`
	LastPrice   string `json:"lastPrice"`
	Volume      string `json:"volume"`
	QuoteVolume string `json:"quoteVolume"`
	CloseTime   int64  `json:"closeTime"`
}

type ExchangeInfo struct {
	Symbols []SymbolInfo `json:"symbols"`
}

type SymbolInfo struct {
	Symbol            string         `json:"symbol"`
	Pair              string         `json:"pair"`
	ContractType      string         `json:"contractType"`
	Status            string         `json:"status"`
	BaseAsset         string         `json:"baseAsset"`
	QuoteAsset        string         `json:"quoteAsset"`
	MarginAsset       string         `json:"marginAsset"`
	PricePrecision    int            `json:"pricePrecision"`
	QuantityPrecision int            `json:"quantityPrecision"`
	Filters           []SymbolFilter `json:"filters"`
}

type SymbolFilter struct {
	FilterType string `json:"filterType"`
	MinPrice   string `json:"minPrice,omitempty"`
	MaxPrice   string `json:"maxPrice,omitempty"`
	TickSize   string `json:"tickSize,omitempty"`
	MinQty     string `json:"minQty,omitempty"`
	MaxQty     string `json:"maxQty,omitempty"`
	StepSize   string `json:"stepSize,omitempty"`
	Notional   string `json:"notional,omitempty"`
}

type PlaceOrderRequest struct {
	Symbol           string
	Side             string
	PositionSide     string
	Type             string
	TimeInForce      string
	Quantity         string
	Price            string
	NewClientOrderID string
	ReduceOnly       bool
}

type ModifyOrderRequest struct {
	Symbol            string
	Side              string
	Quantity          string
	Price             string
	OrderID           string
	OrigClientOrderID string
}

type CancelOrderRequest struct {
	Symbol            string
	OrderID           string
	OrigClientOrderID string
}

type QueryOrderRequest struct {
	Symbol            string
	OrderID           string
	OrigClientOrderID string
}

type OrderAck struct {
	OrderID       int64  `json:"orderId"`
	Symbol        string `json:"symbol"`
	Status        string `json:"status"`
	ClientOrderID string `json:"clientOrderId"`
	Price         string `json:"price"`
	OrigQty       string `json:"origQty"`
	ExecutedQty   string `json:"executedQty"`
	Type          string `json:"type"`
	Side          string `json:"side"`
	PositionSide  string `json:"positionSide"`
}

type AlgoOrderRequest struct {
	Symbol           string
	Side             string
	PositionSide     string
	Type             string
	Quantity         string
	TriggerPrice     string
	ActivationPrice  string
	CallbackRate     string
	WorkingType      string
	NewClientOrderID string
	ReduceOnly       bool
	ClosePosition    bool
}

type AlgoOrderAck struct {
	AlgoID        int64  `json:"algoId"`
	ClientAlgoID  string `json:"clientAlgoId"`
	AlgoType      string `json:"algoType"`
	OrderType     string `json:"orderType"`
	Symbol        string `json:"symbol"`
	Side          string `json:"side"`
	PositionSide  string `json:"positionSide"`
	Quantity      string `json:"quantity"`
	AlgoStatus    string `json:"algoStatus"`
	TriggerPrice  string `json:"triggerPrice"`
	ActivatePrice string `json:"activatePrice"`
	CallbackRate  string `json:"callbackRate"`
}

type AlgoOpenOrder struct {
	AlgoID        int64  `json:"algoId"`
	ClientAlgoID  string `json:"clientAlgoId"`
	AlgoType      string `json:"algoType"`
	OrderType     string `json:"orderType"`
	Symbol        string `json:"symbol"`
	Side          string `json:"side"`
	PositionSide  string `json:"positionSide"`
	Quantity      string `json:"quantity"`
	AlgoStatus    string `json:"algoStatus"`
	ClosePosition bool   `json:"closePosition"`
	ReduceOnly    bool   `json:"reduceOnly"`
	TriggerPrice  string `json:"triggerPrice"`
	ActivatePrice string `json:"activatePrice"`
	CallbackRate  string `json:"callbackRate"`
	PriceRate     string `json:"priceRate"`
	WorkingType   string `json:"workingType"`
	CreateTime    int64  `json:"createTime"`
	UpdateTime    int64  `json:"updateTime"`
}

type CancelAlgoOrderAck struct {
	AlgoID       int64  `json:"algoId"`
	ClientAlgoID string `json:"clientAlgoId"`
	Code         string `json:"code"`
	Msg          string `json:"msg"`
}

func (c Client) Do(ctx context.Context, method, path string, values url.Values, private bool) ([]byte, error) {
	if c.HTTPClient == nil {
		c.HTTPClient = http.DefaultClient
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if values == nil {
		values = url.Values{}
	}
	params := cloneURLValues(values)
	base := strings.TrimRight(c.BaseURL, "/")
	if base == "" {
		base = "https://fapi.binance.com"
	}
	encodedParams := params.Encode()
	if private {
		if err := c.Credentials.Validate(); err != nil {
			return nil, err
		}
		params.Set("timestamp", strconv.FormatInt(c.Now().UTC().UnixMilli(), 10))
		encodedParams = signedParams(params, c.Credentials.SecretKey)
	}
	var body io.Reader
	requestURL := base + path
	if method == http.MethodGet || method == http.MethodDelete {
		if encodedParams != "" {
			requestURL += "?" + encodedParams
		}
	} else {
		body = strings.NewReader(encodedParams)
	}
	req, err := http.NewRequestWithContext(ctx, method, requestURL, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if method != http.MethodGet && method != http.MethodDelete {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if private {
		req.Header.Set("X-MBX-APIKEY", c.Credentials.APIKey)
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if apiErr := decodeAPIError(respBody); apiErr.Msg != "" || apiErr.Code != 0 {
			return nil, apiErr
		}
		return nil, fmt.Errorf("binance http status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	if apiErr := decodeAPIError(respBody); apiErr.Code < 0 {
		return nil, apiErr
	}
	return respBody, nil
}

func decodeAPIError(b []byte) APIError {
	var errResp APIError
	_ = json.Unmarshal(b, &errResp)
	return errResp
}

func sign(payload, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

func signedParams(values url.Values, secret string) string {
	values.Del("signature")
	payload := values.Encode()
	signature := sign(payload, secret)
	if payload == "" {
		return "signature=" + signature
	}
	return payload + "&signature=" + signature
}

func cloneURLValues(values url.Values) url.Values {
	out := make(url.Values, len(values))
	for key, vals := range values {
		out[key] = append([]string(nil), vals...)
	}
	return out
}

func (c Client) AccountBalance(ctx context.Context) ([]Balance, error) {
	b, err := c.Do(ctx, http.MethodGet, "/fapi/v3/balance", nil, true)
	if err != nil {
		return nil, err
	}
	var out []Balance
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c Client) Positions(ctx context.Context, symbol string) ([]Position, error) {
	q := url.Values{}
	if strings.TrimSpace(symbol) != "" {
		q.Set("symbol", strings.ToUpper(strings.TrimSpace(symbol)))
	}
	b, err := c.Do(ctx, http.MethodGet, "/fapi/v3/positionRisk", q, true)
	if err != nil {
		return nil, err
	}
	var out []Position
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c Client) OpenOrders(ctx context.Context, symbol string) ([]OpenOrder, error) {
	q := url.Values{}
	if strings.TrimSpace(symbol) != "" {
		q.Set("symbol", strings.ToUpper(strings.TrimSpace(symbol)))
	}
	b, err := c.Do(ctx, http.MethodGet, "/fapi/v1/openOrders", q, true)
	if err != nil {
		return nil, err
	}
	var out []OpenOrder
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c Client) QueryOrder(ctx context.Context, req QueryOrderRequest) (OpenOrder, error) {
	q := url.Values{}
	symbol := strings.ToUpper(strings.TrimSpace(req.Symbol))
	if symbol == "" {
		return OpenOrder{}, errors.New("symbol is required")
	}
	q.Set("symbol", symbol)
	if strings.TrimSpace(req.OrderID) != "" {
		q.Set("orderId", strings.TrimSpace(req.OrderID))
	} else if strings.TrimSpace(req.OrigClientOrderID) != "" {
		q.Set("origClientOrderId", strings.TrimSpace(req.OrigClientOrderID))
	} else {
		return OpenOrder{}, errors.New("orderId or origClientOrderId is required")
	}
	b, err := c.Do(ctx, http.MethodGet, "/fapi/v1/order", q, true)
	if err != nil {
		return OpenOrder{}, err
	}
	var out OpenOrder
	if err := json.Unmarshal(b, &out); err != nil {
		return OpenOrder{}, err
	}
	return out, nil
}

func (c Client) OpenAlgoOrders(ctx context.Context, symbol string) ([]AlgoOpenOrder, error) {
	q := url.Values{}
	q.Set("algoType", "CONDITIONAL")
	if strings.TrimSpace(symbol) != "" {
		q.Set("symbol", strings.ToUpper(strings.TrimSpace(symbol)))
	}
	b, err := c.Do(ctx, http.MethodGet, "/fapi/v1/openAlgoOrders", q, true)
	if err != nil {
		return nil, err
	}
	var out []AlgoOpenOrder
	if err := json.Unmarshal(b, &out); err == nil {
		return out, nil
	}
	var wrapped struct {
		Orders []AlgoOpenOrder `json:"orders"`
	}
	if err := json.Unmarshal(b, &wrapped); err != nil {
		return nil, err
	}
	return wrapped.Orders, nil
}

func (c Client) UserTrades(ctx context.Context, symbol string, startTime, endTime time.Time, limit int) ([]UserTrade, error) {
	q := url.Values{}
	q.Set("symbol", strings.ToUpper(strings.TrimSpace(symbol)))
	if !startTime.IsZero() {
		q.Set("startTime", strconv.FormatInt(startTime.UTC().UnixMilli(), 10))
	}
	if !endTime.IsZero() {
		q.Set("endTime", strconv.FormatInt(endTime.UTC().UnixMilli(), 10))
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	b, err := c.Do(ctx, http.MethodGet, "/fapi/v1/userTrades", q, true)
	if err != nil {
		return nil, err
	}
	var out []UserTrade
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c Client) IncomeHistory(ctx context.Context, symbol, incomeType string, startTime, endTime time.Time, limit int) ([]Income, error) {
	q := url.Values{}
	if strings.TrimSpace(symbol) != "" {
		q.Set("symbol", strings.ToUpper(strings.TrimSpace(symbol)))
	}
	if strings.TrimSpace(incomeType) != "" {
		q.Set("incomeType", strings.ToUpper(strings.TrimSpace(incomeType)))
	}
	if !startTime.IsZero() {
		q.Set("startTime", strconv.FormatInt(startTime.UTC().UnixMilli(), 10))
	}
	if !endTime.IsZero() {
		q.Set("endTime", strconv.FormatInt(endTime.UTC().UnixMilli(), 10))
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	b, err := c.Do(ctx, http.MethodGet, "/fapi/v1/income", q, true)
	if err != nil {
		return nil, err
	}
	var out []Income
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c Client) BookTicker(ctx context.Context, symbol string) (BookTicker, error) {
	q := url.Values{}
	if strings.TrimSpace(symbol) != "" {
		q.Set("symbol", strings.ToUpper(strings.TrimSpace(symbol)))
	}
	b, err := c.Do(ctx, http.MethodGet, "/fapi/v1/ticker/bookTicker", q, false)
	if err != nil {
		return BookTicker{}, err
	}
	var out BookTicker
	if err := json.Unmarshal(b, &out); err != nil {
		return BookTicker{}, err
	}
	return out, nil
}

func (c Client) PremiumIndex(ctx context.Context, symbol string) (PremiumIndex, error) {
	q := url.Values{}
	if strings.TrimSpace(symbol) != "" {
		q.Set("symbol", strings.ToUpper(strings.TrimSpace(symbol)))
	}
	b, err := c.Do(ctx, http.MethodGet, "/fapi/v1/premiumIndex", q, false)
	if err != nil {
		return PremiumIndex{}, err
	}
	var out PremiumIndex
	if err := json.Unmarshal(b, &out); err != nil {
		return PremiumIndex{}, err
	}
	return out, nil
}

func (c Client) Ticker24hr(ctx context.Context) ([]Ticker24hr, error) {
	b, err := c.Do(ctx, http.MethodGet, "/fapi/v1/ticker/24hr", nil, false)
	if err != nil {
		return nil, err
	}
	var out []Ticker24hr
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c Client) ExchangeInfo(ctx context.Context) (ExchangeInfo, error) {
	b, err := c.Do(ctx, http.MethodGet, "/fapi/v1/exchangeInfo", nil, false)
	if err != nil {
		return ExchangeInfo{}, err
	}
	var out ExchangeInfo
	if err := json.Unmarshal(b, &out); err != nil {
		return ExchangeInfo{}, err
	}
	return out, nil
}

func (c Client) SymbolInfo(ctx context.Context, symbol string) (SymbolInfo, error) {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	info, err := c.ExchangeInfo(ctx)
	if err != nil {
		return SymbolInfo{}, err
	}
	for _, item := range info.Symbols {
		if strings.EqualFold(item.Symbol, symbol) {
			return item, nil
		}
	}
	return SymbolInfo{}, fmt.Errorf("binance symbol %s not found", symbol)
}

func (c Client) SetLeverage(ctx context.Context, symbol string, leverage int) error {
	q := url.Values{}
	q.Set("symbol", strings.ToUpper(strings.TrimSpace(symbol)))
	q.Set("leverage", strconv.Itoa(leverage))
	_, err := c.Do(ctx, http.MethodPost, "/fapi/v1/leverage", q, true)
	return err
}

func (c Client) ChangeMarginType(ctx context.Context, symbol, marginType string) error {
	q := url.Values{}
	q.Set("symbol", strings.ToUpper(strings.TrimSpace(symbol)))
	q.Set("marginType", strings.ToUpper(strings.TrimSpace(marginType)))
	_, err := c.Do(ctx, http.MethodPost, "/fapi/v1/marginType", q, true)
	if err != nil && IsAPIErrorCode(err, -4046) {
		return nil
	}
	return err
}

func (c Client) PlaceOrder(ctx context.Context, req PlaceOrderRequest) (OrderAck, error) {
	q := url.Values{}
	q.Set("symbol", strings.ToUpper(strings.TrimSpace(req.Symbol)))
	q.Set("side", strings.ToUpper(strings.TrimSpace(req.Side)))
	q.Set("type", strings.ToUpper(strings.TrimSpace(req.Type)))
	q.Set("quantity", strings.TrimSpace(req.Quantity))
	if req.PositionSide != "" {
		q.Set("positionSide", strings.ToUpper(strings.TrimSpace(req.PositionSide)))
	}
	if req.TimeInForce != "" {
		q.Set("timeInForce", strings.ToUpper(strings.TrimSpace(req.TimeInForce)))
	}
	if req.Price != "" {
		q.Set("price", strings.TrimSpace(req.Price))
	}
	if req.NewClientOrderID != "" {
		q.Set("newClientOrderId", strings.TrimSpace(req.NewClientOrderID))
	}
	if req.ReduceOnly {
		q.Set("reduceOnly", "true")
	}
	b, err := c.Do(ctx, http.MethodPost, "/fapi/v1/order", q, true)
	if err != nil {
		return OrderAck{}, err
	}
	var ack OrderAck
	if err := json.Unmarshal(b, &ack); err != nil {
		return OrderAck{}, err
	}
	return ack, nil
}

func (c Client) ModifyOrder(ctx context.Context, req ModifyOrderRequest) (OrderAck, error) {
	q := url.Values{}
	q.Set("symbol", strings.ToUpper(strings.TrimSpace(req.Symbol)))
	q.Set("side", strings.ToUpper(strings.TrimSpace(req.Side)))
	q.Set("quantity", strings.TrimSpace(req.Quantity))
	q.Set("price", strings.TrimSpace(req.Price))
	if strings.TrimSpace(req.OrderID) != "" {
		q.Set("orderId", strings.TrimSpace(req.OrderID))
	} else if strings.TrimSpace(req.OrigClientOrderID) != "" {
		q.Set("origClientOrderId", strings.TrimSpace(req.OrigClientOrderID))
	} else {
		return OrderAck{}, errors.New("orderId or origClientOrderId is required")
	}
	b, err := c.Do(ctx, http.MethodPut, "/fapi/v1/order", q, true)
	if err != nil {
		return OrderAck{}, err
	}
	var ack OrderAck
	if err := json.Unmarshal(b, &ack); err != nil {
		return OrderAck{}, err
	}
	return ack, nil
}

func (c Client) CancelOrder(ctx context.Context, req CancelOrderRequest) (OrderAck, error) {
	q := url.Values{}
	q.Set("symbol", strings.ToUpper(strings.TrimSpace(req.Symbol)))
	if strings.TrimSpace(req.OrderID) != "" {
		q.Set("orderId", strings.TrimSpace(req.OrderID))
	} else if strings.TrimSpace(req.OrigClientOrderID) != "" {
		q.Set("origClientOrderId", strings.TrimSpace(req.OrigClientOrderID))
	} else {
		return OrderAck{}, errors.New("orderId or origClientOrderId is required")
	}
	b, err := c.Do(ctx, http.MethodDelete, "/fapi/v1/order", q, true)
	if err != nil {
		return OrderAck{}, err
	}
	var ack OrderAck
	if err := json.Unmarshal(b, &ack); err != nil {
		return OrderAck{}, err
	}
	return ack, nil
}

func (c Client) CancelAllOpenOrders(ctx context.Context, symbol string) error {
	q := url.Values{}
	q.Set("symbol", strings.ToUpper(strings.TrimSpace(symbol)))
	_, err := c.Do(ctx, http.MethodDelete, "/fapi/v1/allOpenOrders", q, true)
	return err
}

func (c Client) CancelAlgoOrder(ctx context.Context, algoID int64, clientAlgoID string) (CancelAlgoOrderAck, error) {
	q := url.Values{}
	if algoID > 0 {
		q.Set("algoId", strconv.FormatInt(algoID, 10))
	} else if strings.TrimSpace(clientAlgoID) != "" {
		q.Set("clientAlgoId", strings.TrimSpace(clientAlgoID))
	} else {
		return CancelAlgoOrderAck{}, errors.New("algoId or clientAlgoId is required")
	}
	b, err := c.Do(ctx, http.MethodDelete, "/fapi/v1/algoOrder", q, true)
	if err != nil {
		return CancelAlgoOrderAck{}, err
	}
	var ack CancelAlgoOrderAck
	if err := json.Unmarshal(b, &ack); err != nil {
		return CancelAlgoOrderAck{}, err
	}
	if ack.Code != "" && ack.Code != "200" {
		return ack, fmt.Errorf("binance cancel algo rejected %s: %s", ack.Code, ack.Msg)
	}
	return ack, nil
}

func (c Client) CancelAllAlgoOpenOrders(ctx context.Context, symbol string) error {
	q := url.Values{}
	q.Set("symbol", strings.ToUpper(strings.TrimSpace(symbol)))
	_, err := c.Do(ctx, http.MethodDelete, "/fapi/v1/algoOpenOrders", q, true)
	return err
}

func (c Client) NewAlgoOrder(ctx context.Context, req AlgoOrderRequest) (AlgoOrderAck, error) {
	q := url.Values{}
	q.Set("algoType", "CONDITIONAL")
	q.Set("symbol", strings.ToUpper(strings.TrimSpace(req.Symbol)))
	q.Set("side", strings.ToUpper(strings.TrimSpace(req.Side)))
	q.Set("type", strings.ToUpper(strings.TrimSpace(req.Type)))
	if strings.TrimSpace(req.Quantity) != "" {
		q.Set("quantity", strings.TrimSpace(req.Quantity))
	}
	if strings.TrimSpace(req.TriggerPrice) != "" {
		q.Set("triggerPrice", strings.TrimSpace(req.TriggerPrice))
	}
	if strings.TrimSpace(req.ActivationPrice) != "" {
		q.Set("activatePrice", strings.TrimSpace(req.ActivationPrice))
	}
	if strings.TrimSpace(req.CallbackRate) != "" {
		q.Set("callbackRate", strings.TrimSpace(req.CallbackRate))
	}
	if req.PositionSide != "" {
		q.Set("positionSide", strings.ToUpper(strings.TrimSpace(req.PositionSide)))
	}
	if req.WorkingType != "" {
		q.Set("workingType", strings.ToUpper(strings.TrimSpace(req.WorkingType)))
	}
	if req.NewClientOrderID != "" {
		q.Set("clientAlgoId", strings.TrimSpace(req.NewClientOrderID))
	}
	if req.ReduceOnly {
		q.Set("reduceOnly", "true")
	}
	if req.ClosePosition {
		q.Set("closePosition", "true")
	}
	b, err := c.Do(ctx, http.MethodPost, "/fapi/v1/algoOrder", q, true)
	if err != nil {
		return AlgoOrderAck{}, err
	}
	var ack AlgoOrderAck
	if err := json.Unmarshal(b, &ack); err != nil {
		return AlgoOrderAck{}, err
	}
	return ack, nil
}

func USDTBalanceFromAccount(balances []Balance) (Balance, bool) {
	for _, balance := range balances {
		if strings.EqualFold(strings.TrimSpace(balance.Asset), "USDT") {
			return balance, true
		}
	}
	return Balance{}, false
}
