package okx

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	okxRateLimitMaxAttempts    = 4
	okxRateLimitRetryBaseDelay = 500 * time.Millisecond
	okxRateLimitRetryMaxDelay  = 5 * time.Second
	okxPrivateRequestSpacing   = 250 * time.Millisecond
	okxPrivateRequestMu        sync.Mutex
	okxNextPrivateRequestAt    time.Time
)

type Credentials struct {
	APIKey     string
	SecretKey  string
	Passphrase string
}

func (c Credentials) Validate() error {
	if c.APIKey == "" || c.SecretKey == "" || c.Passphrase == "" {
		return errors.New("OKX API credentials are incomplete")
	}
	return nil
}

type Client struct {
	BaseURL     string
	Credentials Credentials
	Demo        bool
	HTTPClient  *http.Client
	Now         func() time.Time
}

type Envelope struct {
	Code    string          `json:"code"`
	Msg     string          `json:"msg"`
	Data    json.RawMessage `json:"data"`
	InTime  string          `json:"inTime,omitempty"`
	OutTime string          `json:"outTime,omitempty"`
}

func (e Envelope) OK() bool {
	return e.Code == "0"
}

type APIError struct {
	Code string
	Msg  string
	Data json.RawMessage
}

func (e APIError) Error() string {
	msg := fmt.Sprintf("okx code %s: %s", e.Code, e.Msg)
	if detail := apiErrorDetail(e.Data); detail != "" {
		msg += ": " + detail
	}
	return msg
}

func (e APIError) HasCode(code string) bool {
	if e.Code == code {
		return true
	}
	var details []struct {
		SCode string `json:"sCode"`
	}
	if len(e.Data) == 0 || json.Unmarshal(e.Data, &details) != nil {
		return false
	}
	for _, detail := range details {
		if detail.SCode == code {
			return true
		}
	}
	return false
}

func apiErrorDetail(data json.RawMessage) string {
	var details []struct {
		SCode string `json:"sCode"`
		SMsg  string `json:"sMsg"`
	}
	if len(data) == 0 || json.Unmarshal(data, &details) != nil {
		return ""
	}
	parts := make([]string, 0, len(details))
	for _, detail := range details {
		if detail.SCode == "" && detail.SMsg == "" {
			continue
		}
		if detail.SCode == "" {
			parts = append(parts, detail.SMsg)
			continue
		}
		if detail.SMsg == "" {
			parts = append(parts, detail.SCode)
			continue
		}
		parts = append(parts, detail.SCode+": "+detail.SMsg)
	}
	return strings.Join(parts, "; ")
}

type AccountBalanceData struct {
	TotalEq string                 `json:"totalEq"`
	AdjEq   string                 `json:"adjEq"`
	AvailEq string                 `json:"availEq"`
	UTime   string                 `json:"uTime"`
	Details []AccountBalanceDetail `json:"details"`
}

type AccountBalanceDetail struct {
	Ccy       string `json:"ccy"`
	Eq        string `json:"eq"`
	EqUsd     string `json:"eqUsd"`
	AvailBal  string `json:"availBal"`
	AvailEq   string `json:"availEq"`
	CashBal   string `json:"cashBal"`
	FrozenBal string `json:"frozenBal"`
	DisEq     string `json:"disEq"`
	UTime     string `json:"uTime"`
}

func (c Client) Do(ctx context.Context, method, path string, query url.Values, body any, private bool) (Envelope, error) {
	if c.HTTPClient == nil {
		c.HTTPClient = http.DefaultClient
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	base := strings.TrimRight(c.BaseURL, "/")
	if base == "" {
		base = "https://www.okx.com"
	}
	requestPath := path
	if len(query) > 0 {
		requestPath += "?" + query.Encode()
	}
	var bodyBytes []byte
	if body != nil {
		var err error
		bodyBytes, err = json.Marshal(body)
		if err != nil {
			return Envelope{}, err
		}
	}

	attempts := okxRequestAttempts()
	var lastErr error
	var lastEnv Envelope
	for attempt := 0; attempt < attempts; attempt++ {
		if private {
			if err := waitOKXPrivateRequestTurn(ctx, base); err != nil {
				return lastEnv, err
			}
		}

		req, err := http.NewRequestWithContext(ctx, method, base+requestPath, bytes.NewReader(bodyBytes))
		if err != nil {
			return Envelope{}, err
		}
		req.Header.Set("Accept", "application/json")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if c.Demo {
			req.Header.Set("x-simulated-trading", "1")
		}
		if private {
			if err := c.Credentials.Validate(); err != nil {
				return Envelope{}, err
			}
			ts := c.Now().UTC().Format("2006-01-02T15:04:05.000Z")
			req.Header.Set("OK-ACCESS-KEY", c.Credentials.APIKey)
			req.Header.Set("OK-ACCESS-PASSPHRASE", c.Credentials.Passphrase)
			req.Header.Set("OK-ACCESS-TIMESTAMP", ts)
			req.Header.Set("OK-ACCESS-SIGN", sign(ts, method, requestPath, string(bodyBytes), c.Credentials.SecretKey))
		}
		resp, err := c.HTTPClient.Do(req)
		if err != nil {
			return lastEnv, err
		}
		respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		closeErr := resp.Body.Close()
		if readErr != nil {
			return lastEnv, readErr
		}
		if closeErr != nil {
			return lastEnv, closeErr
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			lastErr = okxHTTPStatusError(resp.StatusCode, respBody)
			if okxHTTPRateLimited(resp.StatusCode, respBody) && attempt+1 < attempts {
				delay := okxRetryDelay(attempt, resp.Header.Get("Retry-After"))
				if err := sleepContext(ctx, delay); err != nil {
					return lastEnv, err
				}
				continue
			}
			return Envelope{}, lastErr
		}
		var env Envelope
		if err := json.Unmarshal(respBody, &env); err != nil {
			return lastEnv, fmt.Errorf("decode okx response: %w", err)
		}
		lastEnv = env
		if !env.OK() {
			lastErr = APIError{Code: env.Code, Msg: env.Msg, Data: env.Data}
			if okxEnvelopeRateLimited(env) && attempt+1 < attempts {
				delay := okxRetryDelay(attempt, "")
				if err := sleepContext(ctx, delay); err != nil {
					return lastEnv, err
				}
				continue
			}
			return env, lastErr
		}
		return env, nil
	}
	if lastErr != nil {
		return lastEnv, lastErr
	}
	return lastEnv, errors.New("okx request failed without response")
}

func okxRequestAttempts() int {
	if okxRateLimitMaxAttempts < 1 {
		return 1
	}
	return okxRateLimitMaxAttempts
}

func waitOKXPrivateRequestTurn(ctx context.Context, base string) error {
	if okxPrivateRequestSpacing <= 0 || okxLocalBaseURL(base) {
		return nil
	}
	okxPrivateRequestMu.Lock()
	now := time.Now()
	wait := okxNextPrivateRequestAt.Sub(now)
	if wait <= 0 {
		okxNextPrivateRequestAt = now.Add(okxPrivateRequestSpacing)
		okxPrivateRequestMu.Unlock()
		return nil
	}
	okxNextPrivateRequestAt = okxNextPrivateRequestAt.Add(okxPrivateRequestSpacing)
	okxPrivateRequestMu.Unlock()
	return sleepContext(ctx, wait)
}

func okxLocalBaseURL(base string) bool {
	u, err := url.Parse(base)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func okxHTTPStatusError(status int, body []byte) error {
	return fmt.Errorf("okx http status %d: %s", status, strings.TrimSpace(string(body)))
}

func okxHTTPRateLimited(status int, body []byte) bool {
	if status == http.StatusTooManyRequests {
		return true
	}
	var env Envelope
	if len(body) == 0 || json.Unmarshal(body, &env) != nil {
		return false
	}
	return okxEnvelopeRateLimited(env)
}

func okxEnvelopeRateLimited(env Envelope) bool {
	if env.Code == "50011" {
		return true
	}
	return strings.Contains(strings.ToLower(env.Msg), "too many requests")
}

func okxRetryDelay(attempt int, retryAfter string) time.Duration {
	if parsed, ok := parseRetryAfter(retryAfter); ok {
		return parsed
	}
	delay := okxRateLimitRetryBaseDelay
	if delay <= 0 {
		return 0
	}
	for i := 0; i < attempt; i++ {
		delay *= 2
		if okxRateLimitRetryMaxDelay > 0 && delay >= okxRateLimitRetryMaxDelay {
			return okxRateLimitRetryMaxDelay
		}
	}
	if okxRateLimitRetryMaxDelay > 0 && delay > okxRateLimitRetryMaxDelay {
		return okxRateLimitRetryMaxDelay
	}
	return delay
}

func parseRetryAfter(raw string) (time.Duration, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	if seconds, err := strconv.ParseFloat(raw, 64); err == nil {
		if seconds <= 0 {
			return 0, true
		}
		return time.Duration(seconds * float64(time.Second)), true
	}
	if when, err := http.ParseTime(raw); err == nil {
		delay := time.Until(when)
		if delay < 0 {
			delay = 0
		}
		return delay, true
	}
	return 0, false
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func sign(timestamp, method, requestPath, body, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp + strings.ToUpper(method) + requestPath + body))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

type SetLeverageRequest struct {
	InstID  string `json:"instId"`
	Lever   string `json:"lever"`
	MgnMode string `json:"mgnMode"`
	PosSide string `json:"posSide,omitempty"`
}

type PlaceOrderRequest struct {
	InstID         string              `json:"instId"`
	TDMode         string              `json:"tdMode"`
	ClOrdID        string              `json:"clOrdId"`
	Side           string              `json:"side"`
	PosSide        string              `json:"posSide,omitempty"`
	OrdType        string              `json:"ordType"`
	Px             string              `json:"px,omitempty"`
	Sz             string              `json:"sz"`
	ReduceOnly     bool                `json:"reduceOnly,omitempty"`
	AttachAlgoOrds []map[string]string `json:"attachAlgoOrds,omitempty"`
}

// ClosePositionRequest requests that OKX closes an entire futures or perpetual
// position. Unlike a regular market order, this uses OKX's dedicated
// close-position endpoint and can atomically cancel pending closing orders.
type ClosePositionRequest struct {
	InstID  string `json:"instId"`
	MgnMode string `json:"mgnMode"`
	PosSide string `json:"posSide,omitempty"`
	AutoCxl bool   `json:"autoCxl,omitempty"`
	ClOrdID string `json:"clOrdId,omitempty"`
}

type ClosePositionAck struct {
	ClOrdID string `json:"clOrdId"`
	InstID  string `json:"instId"`
	PosSide string `json:"posSide"`
}

type PlaceAlgoOrderRequest struct {
	InstID          string `json:"instId"`
	TDMode          string `json:"tdMode"`
	AlgoClOrdID     string `json:"algoClOrdId,omitempty"`
	Side            string `json:"side"`
	PosSide         string `json:"posSide,omitempty"`
	OrdType         string `json:"ordType"`
	Sz              string `json:"sz"`
	ReduceOnly      bool   `json:"reduceOnly,omitempty"`
	TPTriggerPx     string `json:"tpTriggerPx,omitempty"`
	TPOrdPx         string `json:"tpOrdPx,omitempty"`
	TPTriggerPxType string `json:"tpTriggerPxType,omitempty"`
	SLTriggerPx     string `json:"slTriggerPx,omitempty"`
	SLOrdPx         string `json:"slOrdPx,omitempty"`
	SLTriggerPxType string `json:"slTriggerPxType,omitempty"`
	CallbackRatio   string `json:"callbackRatio,omitempty"`
	CallbackSpread  string `json:"callbackSpread,omitempty"`
	ActivePx        string `json:"activePx,omitempty"`
}

type OrderAck struct {
	ClOrdID string `json:"clOrdId"`
	OrdID   string `json:"ordId"`
	SCode   string `json:"sCode"`
	SMsg    string `json:"sMsg"`
}

type PlaceAlgoOrderAck struct {
	AlgoID      string `json:"algoId"`
	AlgoClOrdID string `json:"algoClOrdId"`
	SCode       string `json:"sCode"`
	SMsg        string `json:"sMsg"`
}

type CancelOrderRequest struct {
	InstID  string `json:"instId"`
	OrdID   string `json:"ordId,omitempty"`
	ClOrdID string `json:"clOrdId,omitempty"`
}

type CancelOrderAck struct {
	ClOrdID string `json:"clOrdId"`
	OrdID   string `json:"ordId"`
	SCode   string `json:"sCode"`
	SMsg    string `json:"sMsg"`
}

type AlgoOrder struct {
	InstType          string          `json:"instType"`
	InstID            string          `json:"instId"`
	AlgoID            string          `json:"algoId"`
	AlgoClOrdID       string          `json:"algoClOrdId"`
	TDMode            string          `json:"tdMode"`
	Side              string          `json:"side"`
	PosSide           string          `json:"posSide"`
	OrdType           string          `json:"ordType"`
	Sz                string          `json:"sz"`
	ActualSz          string          `json:"actualSz"`
	State             string          `json:"state"`
	ReduceOnly        json.RawMessage `json:"reduceOnly,omitempty"`
	TriggerPx         string          `json:"triggerPx"`
	TriggerPxType     string          `json:"triggerPxType"`
	OrderPx           string          `json:"orderPx"`
	TPTriggerPx       string          `json:"tpTriggerPx"`
	TPOrdPx           string          `json:"tpOrdPx"`
	TPTriggerPxType   string          `json:"tpTriggerPxType"`
	SLTriggerPx       string          `json:"slTriggerPx"`
	SLOrdPx           string          `json:"slOrdPx"`
	SLTriggerPxType   string          `json:"slTriggerPxType"`
	ActivePx          string          `json:"activePx"`
	CallbackRatio     string          `json:"callbackRatio"`
	CallbackSpread    string          `json:"callbackSpread"`
	MoveTriggerPx     string          `json:"moveTriggerPx"`
	MoveTriggerPxType string          `json:"moveTriggerPxType"`
	CTime             string          `json:"cTime"`
	UTime             string          `json:"uTime"`
	RawJSON           string          `json:"-"`
}

var pendingAlgoOrderTypes = []string{"conditional", "trigger", "move_order_stop", "oco", "iceberg", "twap"}

type CancelAlgoOrderRequest struct {
	InstID      string `json:"instId"`
	AlgoID      string `json:"algoId,omitempty"`
	AlgoClOrdID string `json:"algoClOrdId,omitempty"`
}

type CancelAlgoOrderAck struct {
	AlgoID      string `json:"algoId"`
	AlgoClOrdID string `json:"algoClOrdId"`
	SCode       string `json:"sCode"`
	SMsg        string `json:"sMsg"`
}

type AmendOrderRequest struct {
	InstID         string              `json:"instId"`
	OrdID          string              `json:"ordId,omitempty"`
	ClOrdID        string              `json:"clOrdId,omitempty"`
	NewPx          string              `json:"newPx,omitempty"`
	NewSz          string              `json:"newSz,omitempty"`
	AttachAlgoOrds []map[string]string `json:"attachAlgoOrds,omitempty"`
}

type AmendOrderAck struct {
	ClOrdID string `json:"clOrdId"`
	OrdID   string `json:"ordId"`
	ReqID   string `json:"reqId,omitempty"`
	SCode   string `json:"sCode"`
	SMsg    string `json:"sMsg"`
}

type AmendAlgoOrderRequest struct {
	InstID             string `json:"instId"`
	AlgoID             string `json:"algoId,omitempty"`
	AlgoClOrdID        string `json:"algoClOrdId,omitempty"`
	CxlOnFail          bool   `json:"cxlOnFail,omitempty"`
	ReqID              string `json:"reqId,omitempty"`
	NewSz              string `json:"newSz,omitempty"`
	NewTriggerPx       string `json:"newTriggerPx,omitempty"`
	NewOrderPx         string `json:"newOrderPx,omitempty"`
	NewTriggerPxType   string `json:"newTriggerPxType,omitempty"`
	NewTPTriggerPx     string `json:"newTpTriggerPx,omitempty"`
	NewTPOrdPx         string `json:"newTpOrdPx,omitempty"`
	NewTPTriggerPxType string `json:"newTpTriggerPxType,omitempty"`
	NewSLTriggerPx     string `json:"newSlTriggerPx,omitempty"`
	NewSLOrdPx         string `json:"newSlOrdPx,omitempty"`
	NewSLTriggerPxType string `json:"newSlTriggerPxType,omitempty"`
}

type AmendAlgoOrderAck struct {
	AlgoID      string `json:"algoId"`
	AlgoClOrdID string `json:"algoClOrdId"`
	ReqID       string `json:"reqId,omitempty"`
	SCode       string `json:"sCode"`
	SMsg        string `json:"sMsg"`
}

type Instrument struct {
	InstType   string `json:"instType,omitempty"`
	InstID     string `json:"instId"`
	Uly        string `json:"uly,omitempty"`
	InstFamily string `json:"instFamily,omitempty"`
	Category   string `json:"category,omitempty"`
	BaseCcy    string `json:"baseCcy,omitempty"`
	QuoteCcy   string `json:"quoteCcy,omitempty"`
	SettleCcy  string `json:"settleCcy,omitempty"`
	CtVal      string `json:"ctVal"`
	CtMult     string `json:"ctMult,omitempty"`
	CtValCcy   string `json:"ctValCcy,omitempty"`
	CtType     string `json:"ctType,omitempty"`
	ListTime   string `json:"listTime,omitempty"`
	ExpTime    string `json:"expTime,omitempty"`
	Lever      string `json:"lever,omitempty"`
	TickSz     string `json:"tickSz,omitempty"`
	LotSz      string `json:"lotSz"`
	MinSz      string `json:"minSz"`
	Alias      string `json:"alias,omitempty"`
	State      string `json:"state,omitempty"`
}

func (i Instrument) UnderlyingMatchesFamily() bool {
	underlying := strings.ToUpper(strings.TrimSpace(i.Uly))
	family := strings.ToUpper(strings.TrimSpace(i.InstFamily))
	return underlying == "" || family == "" || underlying == family
}

func (i Instrument) ValidateUnderlyingFamily() error {
	if i.UnderlyingMatchesFamily() {
		return nil
	}
	return fmt.Errorf(
		"unsafe OKX instrument mapping: %s has underlying %s but instrument family %s",
		strings.TrimSpace(i.InstID),
		strings.TrimSpace(i.Uly),
		strings.TrimSpace(i.InstFamily),
	)
}

type MarketCandle struct {
	TS      string
	Open    string
	High    string
	Low     string
	Close   string
	Volume  string
	Confirm string
}

type Ticker struct {
	InstType  string `json:"instType"`
	InstID    string `json:"instId"`
	Last      string `json:"last"`
	LastSz    string `json:"lastSz"`
	AskPx     string `json:"askPx"`
	AskSz     string `json:"askSz"`
	BidPx     string `json:"bidPx"`
	BidSz     string `json:"bidSz"`
	Open24h   string `json:"open24h"`
	High24h   string `json:"high24h"`
	Low24h    string `json:"low24h"`
	VolCcy24h string `json:"volCcy24h"`
	Vol24h    string `json:"vol24h"`
	TS        string `json:"ts"`
}

type Fill struct {
	InstType string `json:"instType"`
	InstID   string `json:"instId"`
	TradeID  string `json:"tradeId"`
	OrdID    string `json:"ordId"`
	ClOrdID  string `json:"clOrdId"`
	Side     string `json:"side"`
	PosSide  string `json:"posSide"`
	FillPx   string `json:"fillPx"`
	FillSz   string `json:"fillSz"`
	FillPnl  string `json:"fillPnl"`
	Fee      string `json:"fee"`
	FeeCcy   string `json:"feeCcy"`
	FillTime string `json:"fillTime"`
	RawJSON  string `json:"-"`
}

type AccountBill struct {
	BillID   string `json:"billId"`
	InstType string `json:"instType"`
	InstID   string `json:"instId"`
	Ccy      string `json:"ccy"`
	Type     string `json:"type"`
	SubType  string `json:"subType"`
	BalChg   string `json:"balChg"`
	Pnl      string `json:"pnl"`
	Fee      string `json:"fee"`
	OrdID    string `json:"ordId"`
	TradeID  string `json:"tradeId"`
	TS       string `json:"ts"`
	RawJSON  string `json:"-"`
}

type PendingOrder struct {
	InstType       string              `json:"instType"`
	InstID         string              `json:"instId"`
	OrdID          string              `json:"ordId"`
	ClOrdID        string              `json:"clOrdId"`
	TDMode         string              `json:"tdMode"`
	Side           string              `json:"side"`
	PosSide        string              `json:"posSide"`
	OrdType        string              `json:"ordType"`
	Px             string              `json:"px"`
	Sz             string              `json:"sz"`
	AccFillSz      string              `json:"accFillSz"`
	AvgPx          string              `json:"avgPx"`
	State          string              `json:"state"`
	Lever          string              `json:"lever"`
	ReduceOnly     json.RawMessage     `json:"reduceOnly,omitempty"`
	ClosePosition  json.RawMessage     `json:"closePosition,omitempty"`
	AttachAlgoOrds []map[string]string `json:"attachAlgoOrds,omitempty"`
	CTime          string              `json:"cTime"`
	UTime          string              `json:"uTime"`
	RawJSON        string              `json:"-"`
}

type Position struct {
	InstType    string `json:"instType"`
	InstID      string `json:"instId"`
	MgnMode     string `json:"mgnMode"`
	PosID       string `json:"posId"`
	PosSide     string `json:"posSide"`
	Pos         string `json:"pos"`
	AvailPos    string `json:"availPos"`
	AvgPx       string `json:"avgPx"`
	MarkPx      string `json:"markPx"`
	Upl         string `json:"upl"`
	UplRatio    string `json:"uplRatio"`
	Lever       string `json:"lever"`
	LiqPx       string `json:"liqPx"`
	NotionalUsd string `json:"notionalUsd"`
	Margin      string `json:"margin"`
	MgnRatio    string `json:"mgnRatio"`
	Adl         string `json:"adl"`
	CTime       string `json:"cTime"`
	UTime       string `json:"uTime"`
	Ccy         string `json:"ccy"`
}

func (c Client) SetLeverage(ctx context.Context, req SetLeverageRequest) error {
	_, err := c.Do(ctx, http.MethodPost, "/api/v5/account/set-leverage", nil, req, true)
	return err
}

func (c Client) PlaceOrder(ctx context.Context, req PlaceOrderRequest) (OrderAck, Envelope, error) {
	env, err := c.Do(ctx, http.MethodPost, "/api/v5/trade/order", nil, req, true)
	if err != nil {
		return OrderAck{}, env, err
	}
	var data []OrderAck
	if err := json.Unmarshal(env.Data, &data); err != nil {
		return OrderAck{}, env, err
	}
	if len(data) == 0 {
		return OrderAck{}, env, errors.New("okx order response data is empty")
	}
	if data[0].SCode != "" && data[0].SCode != "0" {
		return data[0], env, fmt.Errorf("okx order rejected %s: %s", data[0].SCode, data[0].SMsg)
	}
	return data[0], env, nil
}

func (c Client) ClosePosition(ctx context.Context, req ClosePositionRequest) (ClosePositionAck, Envelope, error) {
	env, err := c.Do(ctx, http.MethodPost, "/api/v5/trade/close-position", nil, req, true)
	if err != nil {
		return ClosePositionAck{}, env, err
	}
	var data []ClosePositionAck
	if err := json.Unmarshal(env.Data, &data); err != nil {
		return ClosePositionAck{}, env, err
	}
	if len(data) == 0 {
		return ClosePositionAck{}, env, errors.New("okx close-position response data is empty")
	}
	return data[0], env, nil
}

func (c Client) PlaceAlgoOrder(ctx context.Context, req PlaceAlgoOrderRequest) (PlaceAlgoOrderAck, Envelope, error) {
	env, err := c.Do(ctx, http.MethodPost, "/api/v5/trade/order-algo", nil, req, true)
	if err != nil {
		return PlaceAlgoOrderAck{}, env, err
	}
	var data []PlaceAlgoOrderAck
	if err := json.Unmarshal(env.Data, &data); err != nil {
		return PlaceAlgoOrderAck{}, env, err
	}
	if len(data) == 0 {
		return PlaceAlgoOrderAck{}, env, errors.New("okx algo order response data is empty")
	}
	if data[0].SCode != "" && data[0].SCode != "0" {
		return data[0], env, fmt.Errorf("okx algo order rejected %s: %s", data[0].SCode, data[0].SMsg)
	}
	return data[0], env, nil
}

func (c Client) CancelOrder(ctx context.Context, req CancelOrderRequest) (CancelOrderAck, Envelope, error) {
	env, err := c.Do(ctx, http.MethodPost, "/api/v5/trade/cancel-order", nil, req, true)
	if err != nil {
		return CancelOrderAck{}, env, err
	}
	var data []CancelOrderAck
	if err := json.Unmarshal(env.Data, &data); err != nil {
		return CancelOrderAck{}, env, err
	}
	if len(data) == 0 {
		return CancelOrderAck{}, env, errors.New("okx cancel order response data is empty")
	}
	if data[0].SCode != "" && data[0].SCode != "0" {
		return data[0], env, fmt.Errorf("okx cancel order rejected %s: %s", data[0].SCode, data[0].SMsg)
	}
	return data[0], env, nil
}

func (c Client) PendingAlgoOrders(ctx context.Context, instType, instID string) ([]AlgoOrder, Envelope, error) {
	out := []AlgoOrder{}
	seen := map[string]struct{}{}
	var lastEnv Envelope
	for _, ordType := range pendingAlgoOrderTypes {
		orders, env, err := c.pendingAlgoOrdersByType(ctx, instType, instID, ordType)
		if err != nil {
			return nil, env, err
		}
		lastEnv = env
		for _, order := range orders {
			key := pendingAlgoOrderKey(order)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, order)
		}
	}
	if lastEnv.Code == "" {
		lastEnv.Code = "0"
	}
	return out, lastEnv, nil
}

func (c Client) pendingAlgoOrdersByType(ctx context.Context, instType, instID, ordType string) ([]AlgoOrder, Envelope, error) {
	q := url.Values{}
	if strings.TrimSpace(instType) != "" {
		q.Set("instType", strings.ToUpper(strings.TrimSpace(instType)))
	}
	if strings.TrimSpace(instID) != "" {
		q.Set("instId", strings.ToUpper(strings.TrimSpace(instID)))
	}
	if strings.TrimSpace(ordType) != "" {
		q.Set("ordType", strings.ToLower(strings.TrimSpace(ordType)))
	}
	env, err := c.Do(ctx, http.MethodGet, "/api/v5/trade/orders-algo-pending", q, nil, true)
	if err != nil {
		return nil, env, err
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(env.Data, &raw); err != nil {
		return nil, env, err
	}
	out := make([]AlgoOrder, 0, len(raw))
	for _, item := range raw {
		var order AlgoOrder
		if err := json.Unmarshal(item, &order); err != nil {
			return nil, env, err
		}
		order.RawJSON = string(item)
		out = append(out, order)
	}
	return out, env, nil
}

func pendingAlgoOrderKey(order AlgoOrder) string {
	parts := []string{
		strings.ToUpper(strings.TrimSpace(order.InstID)),
		strings.TrimSpace(order.AlgoID),
		strings.TrimSpace(order.AlgoClOrdID),
		strings.ToLower(strings.TrimSpace(order.OrdType)),
	}
	key := strings.Join(parts, "|")
	if strings.Trim(key, "|") != "" {
		return key
	}
	return strings.TrimSpace(order.RawJSON)
}

func (c Client) CancelAlgoOrders(ctx context.Context, reqs []CancelAlgoOrderRequest) ([]CancelAlgoOrderAck, Envelope, error) {
	if len(reqs) == 0 {
		return nil, Envelope{Code: "0"}, nil
	}
	env, err := c.Do(ctx, http.MethodPost, "/api/v5/trade/cancel-algos", nil, reqs, true)
	if err != nil {
		return nil, env, err
	}
	var data []CancelAlgoOrderAck
	if err := json.Unmarshal(env.Data, &data); err != nil {
		return nil, env, err
	}
	if len(data) == 0 {
		return nil, env, errors.New("okx cancel algo response data is empty")
	}
	for _, ack := range data {
		if ack.SCode != "" && ack.SCode != "0" {
			return data, env, fmt.Errorf("okx cancel algo rejected %s: %s", ack.SCode, ack.SMsg)
		}
	}
	return data, env, nil
}

func (c Client) AmendAlgoOrder(ctx context.Context, req AmendAlgoOrderRequest) (AmendAlgoOrderAck, Envelope, error) {
	env, err := c.Do(ctx, http.MethodPost, "/api/v5/trade/amend-algos", nil, req, true)
	if err != nil {
		return AmendAlgoOrderAck{}, env, err
	}
	var data []AmendAlgoOrderAck
	if err := json.Unmarshal(env.Data, &data); err != nil {
		return AmendAlgoOrderAck{}, env, err
	}
	if len(data) == 0 {
		return AmendAlgoOrderAck{}, env, errors.New("okx amend algo response data is empty")
	}
	if data[0].SCode != "" && data[0].SCode != "0" {
		return data[0], env, fmt.Errorf("okx amend algo rejected %s: %s", data[0].SCode, data[0].SMsg)
	}
	return data[0], env, nil
}

func (c Client) AmendOrder(ctx context.Context, req AmendOrderRequest) (AmendOrderAck, Envelope, error) {
	env, err := c.Do(ctx, http.MethodPost, "/api/v5/trade/amend-order", nil, req, true)
	if err != nil {
		return AmendOrderAck{}, env, err
	}
	var data []AmendOrderAck
	if err := json.Unmarshal(env.Data, &data); err != nil {
		return AmendOrderAck{}, env, err
	}
	if len(data) == 0 {
		return AmendOrderAck{}, env, errors.New("okx amend order response data is empty")
	}
	if data[0].SCode != "" && data[0].SCode != "0" {
		return data[0], env, fmt.Errorf("okx amend order rejected %s: %s", data[0].SCode, data[0].SMsg)
	}
	return data[0], env, nil
}

func (c Client) AccountBalance(ctx context.Context) (Envelope, error) {
	return c.Do(ctx, http.MethodGet, "/api/v5/account/balance", nil, nil, true)
}

func (c Client) AccountBalanceSnapshot(ctx context.Context) (AccountBalanceData, Envelope, error) {
	env, err := c.AccountBalance(ctx)
	if err != nil {
		return AccountBalanceData{}, env, err
	}
	var data []AccountBalanceData
	if err := json.Unmarshal(env.Data, &data); err != nil {
		return AccountBalanceData{}, env, err
	}
	if len(data) == 0 {
		return AccountBalanceData{}, env, errors.New("okx account balance data is empty")
	}
	return data[0], env, nil
}

func (c Client) AccountPositions(ctx context.Context, instType string) (Envelope, error) {
	q := url.Values{}
	if strings.TrimSpace(instType) != "" {
		q.Set("instType", strings.ToUpper(strings.TrimSpace(instType)))
	}
	return c.Do(ctx, http.MethodGet, "/api/v5/account/positions", q, nil, true)
}

func (c Client) Positions(ctx context.Context, instType string) ([]Position, Envelope, error) {
	env, err := c.AccountPositions(ctx, instType)
	if err != nil {
		return nil, env, err
	}
	var data []Position
	if err := json.Unmarshal(env.Data, &data); err != nil {
		return nil, env, err
	}
	return data, env, nil
}

func (c Client) Instruments(ctx context.Context) (Envelope, error) {
	q := url.Values{}
	q.Set("instType", "SWAP")
	return c.Do(ctx, http.MethodGet, "/api/v5/public/instruments", q, nil, false)
}

func (c Client) SwapInstruments(ctx context.Context) ([]Instrument, Envelope, error) {
	env, err := c.Instruments(ctx)
	if err != nil {
		return nil, env, err
	}
	var data []Instrument
	if err := json.Unmarshal(env.Data, &data); err != nil {
		return nil, env, err
	}
	return data, env, nil
}

func (c Client) MarketTicker(ctx context.Context, instID string) (Ticker, Envelope, error) {
	q := url.Values{}
	q.Set("instId", strings.ToUpper(strings.TrimSpace(instID)))
	env, err := c.Do(ctx, http.MethodGet, "/api/v5/market/ticker", q, nil, false)
	if err != nil {
		return Ticker{}, env, err
	}
	var data []Ticker
	if err := json.Unmarshal(env.Data, &data); err != nil {
		return Ticker{}, env, err
	}
	if len(data) == 0 {
		return Ticker{}, env, errors.New("okx ticker response data is empty")
	}
	return data[0], env, nil
}

func (c Client) MarketTickers(ctx context.Context, instType string) ([]Ticker, Envelope, error) {
	q := url.Values{}
	q.Set("instType", strings.ToUpper(strings.TrimSpace(instType)))
	env, err := c.Do(ctx, http.MethodGet, "/api/v5/market/tickers", q, nil, false)
	if err != nil {
		return nil, env, err
	}
	var data []Ticker
	if err := json.Unmarshal(env.Data, &data); err != nil {
		return nil, env, err
	}
	return data, env, nil
}

func (c Client) MarketCandles(ctx context.Context, instID, bar string, limit int) ([]MarketCandle, Envelope, error) {
	q := url.Values{}
	q.Set("instId", strings.ToUpper(strings.TrimSpace(instID)))
	if bar != "" {
		q.Set("bar", bar)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	env, err := c.Do(ctx, http.MethodGet, "/api/v5/market/candles", q, nil, false)
	if err != nil {
		return nil, env, err
	}
	var raw [][]string
	if err := json.Unmarshal(env.Data, &raw); err != nil {
		return nil, env, err
	}
	out := make([]MarketCandle, 0, len(raw))
	for _, row := range raw {
		if len(row) < 5 {
			continue
		}
		candle := MarketCandle{
			TS:    row[0],
			Open:  row[1],
			High:  row[2],
			Low:   row[3],
			Close: row[4],
		}
		if len(row) > 5 {
			candle.Volume = row[5]
		}
		if len(row) > 8 {
			candle.Confirm = row[8]
		} else if len(row) > 6 {
			candle.Confirm = row[len(row)-1]
		}
		out = append(out, candle)
	}
	return out, env, nil
}

func (c Client) FillsHistory(ctx context.Context, instType, after string, limit int) ([]Fill, Envelope, error) {
	q := url.Values{}
	q.Set("instType", strings.ToUpper(strings.TrimSpace(instType)))
	if after != "" {
		q.Set("after", after)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	env, err := c.Do(ctx, http.MethodGet, "/api/v5/trade/fills-history", q, nil, true)
	if err != nil {
		return nil, env, err
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(env.Data, &raw); err != nil {
		return nil, env, err
	}
	out := make([]Fill, 0, len(raw))
	for _, item := range raw {
		var fill Fill
		if err := json.Unmarshal(item, &fill); err != nil {
			return nil, env, err
		}
		fill.RawJSON = string(item)
		out = append(out, fill)
	}
	return out, env, nil
}

func (c Client) AccountBillsArchive(ctx context.Context, instType string, begin, end time.Time, after string, limit int) ([]AccountBill, Envelope, error) {
	q := url.Values{}
	if strings.TrimSpace(instType) != "" {
		q.Set("instType", strings.ToUpper(strings.TrimSpace(instType)))
	}
	if !begin.IsZero() {
		q.Set("begin", strconv.FormatInt(begin.UTC().UnixMilli(), 10))
	}
	if !end.IsZero() {
		q.Set("end", strconv.FormatInt(end.UTC().UnixMilli(), 10))
	}
	if strings.TrimSpace(after) != "" {
		q.Set("after", strings.TrimSpace(after))
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	env, err := c.Do(ctx, http.MethodGet, "/api/v5/account/bills-archive", q, nil, true)
	if err != nil {
		return nil, env, err
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(env.Data, &raw); err != nil {
		return nil, env, err
	}
	out := make([]AccountBill, 0, len(raw))
	for _, item := range raw {
		var bill AccountBill
		if err := json.Unmarshal(item, &bill); err != nil {
			return nil, env, err
		}
		bill.RawJSON = string(item)
		out = append(out, bill)
	}
	return out, env, nil
}

func (c Client) PendingOrders(ctx context.Context, instType string) ([]PendingOrder, Envelope, error) {
	q := url.Values{}
	if strings.TrimSpace(instType) != "" {
		q.Set("instType", strings.ToUpper(strings.TrimSpace(instType)))
	}
	env, err := c.Do(ctx, http.MethodGet, "/api/v5/trade/orders-pending", q, nil, true)
	if err != nil {
		return nil, env, err
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(env.Data, &raw); err != nil {
		return nil, env, err
	}
	out := make([]PendingOrder, 0, len(raw))
	for _, item := range raw {
		var order PendingOrder
		if err := json.Unmarshal(item, &order); err != nil {
			return nil, env, err
		}
		order.RawJSON = string(item)
		out = append(out, order)
	}
	return out, env, nil
}

func (c Client) SwapInstrument(ctx context.Context, instID string) (Instrument, error) {
	q := url.Values{}
	q.Set("instType", "SWAP")
	q.Set("instId", strings.ToUpper(strings.TrimSpace(instID)))
	env, err := c.Do(ctx, http.MethodGet, "/api/v5/public/instruments", q, nil, false)
	if err != nil {
		return Instrument{}, err
	}
	var data []Instrument
	if err := json.Unmarshal(env.Data, &data); err != nil {
		return Instrument{}, err
	}
	for _, inst := range data {
		if strings.EqualFold(inst.InstID, instID) {
			return inst, nil
		}
	}
	if len(data) > 0 {
		return data[0], nil
	}
	return Instrument{}, fmt.Errorf("okx instrument %s not found", instID)
}

func (i Instrument) SymbolInfo() (tradingSymbolInfo, error) {
	ctVal, err := strconv.ParseFloat(i.CtVal, 64)
	if err != nil {
		return tradingSymbolInfo{}, fmt.Errorf("invalid ctVal %q: %w", i.CtVal, err)
	}
	var tickSz float64
	if strings.TrimSpace(i.TickSz) != "" {
		tickSz, err = strconv.ParseFloat(i.TickSz, 64)
		if err != nil {
			return tradingSymbolInfo{}, fmt.Errorf("invalid tickSz %q: %w", i.TickSz, err)
		}
	}
	lotSz, err := strconv.ParseFloat(i.LotSz, 64)
	if err != nil {
		return tradingSymbolInfo{}, fmt.Errorf("invalid lotSz %q: %w", i.LotSz, err)
	}
	minSz, err := strconv.ParseFloat(i.MinSz, 64)
	if err != nil {
		return tradingSymbolInfo{}, fmt.Errorf("invalid minSz %q: %w", i.MinSz, err)
	}
	return tradingSymbolInfo{InstID: i.InstID, CtVal: ctVal, TickSz: tickSz, LotSz: lotSz, MinSz: minSz}, nil
}

type tradingSymbolInfo struct {
	InstID string
	CtVal  float64
	TickSz float64
	LotSz  float64
	MinSz  float64
}
