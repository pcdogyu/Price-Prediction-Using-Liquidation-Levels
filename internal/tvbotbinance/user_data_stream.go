package binance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/binancerest"
)

// DoAPIKey performs an unsigned Binance USER_STREAM request. listenKey
// endpoints authenticate with the API key header and intentionally do not use
// the trading secret/signature.
func (c Client) DoAPIKey(ctx context.Context, method, path string, values url.Values) ([]byte, error) {
	if c.HTTPClient == nil {
		c.HTTPClient = http.DefaultClient
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if until, blocked := RateLimitPauseUntil(c.Now()); blocked {
		return nil, rateLimitPauseError(until)
	}
	if err := c.Credentials.Validate(); err != nil {
		return nil, err
	}
	params := cloneURLValues(values)
	base := strings.TrimRight(c.BaseURL, "/")
	if base == "" {
		base = "https://fapi.binance.com"
	}
	scheduledRequest := binancerest.Request{
		Host:     base,
		Path:     path,
		Class:    binancerest.ClassAccount,
		Priority: binancerest.PriorityAccount,
		Weight:   binancerest.WeightForPath(path),
	}
	release, err := binancerest.Default().Acquire(ctx, scheduledRequest)
	if err != nil {
		return nil, err
	}
	defer release()
	encodedParams := params.Encode()
	requestURL := base + path
	var body io.Reader
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
	req.Header.Set("X-MBX-APIKEY", c.Credentials.APIKey)
	if method != http.MethodGet && method != http.MethodDelete {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		redacted := redactTransportError(method, path, err)
		binancerest.Default().Complete(scheduledRequest, 0, 0, redacted)
		return nil, redacted
	}
	defer resp.Body.Close()
	usedWeight, _ := strconv.Atoi(resp.Header.Get("X-MBX-USED-WEIGHT-1M"))
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		binancerest.Default().Complete(scheduledRequest, resp.StatusCode, usedWeight, err)
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if apiErr := decodeAPIError(respBody); apiErr.Msg != "" || apiErr.Code != 0 {
			noteRateLimit(apiErr, c.Now())
			binancerest.Default().Complete(scheduledRequest, resp.StatusCode, usedWeight, apiErr)
			return nil, apiErr
		}
		err := fmt.Errorf("binance http status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
		noteRateLimit(err, c.Now())
		binancerest.Default().Complete(scheduledRequest, resp.StatusCode, usedWeight, err)
		return nil, err
	}
	if apiErr := decodeAPIError(respBody); apiErr.Code < 0 {
		noteRateLimit(apiErr, c.Now())
		binancerest.Default().Complete(scheduledRequest, resp.StatusCode, usedWeight, apiErr)
		return nil, apiErr
	}
	binancerest.Default().Complete(scheduledRequest, resp.StatusCode, usedWeight, nil)
	return respBody, nil
}

func (c Client) StartUserDataStream(ctx context.Context) (string, error) {
	body, err := c.DoAPIKey(ctx, http.MethodPost, "/fapi/v1/listenKey", nil)
	if err != nil {
		return "", err
	}
	var response struct {
		ListenKey string `json:"listenKey"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return "", err
	}
	if strings.TrimSpace(response.ListenKey) == "" {
		return "", errors.New("Binance listenKey response is empty")
	}
	return strings.TrimSpace(response.ListenKey), nil
}

func (c Client) KeepAliveUserDataStream(ctx context.Context, listenKey string) error {
	listenKey = strings.TrimSpace(listenKey)
	if listenKey == "" {
		return errors.New("Binance listenKey is empty")
	}
	_, err := c.DoAPIKey(ctx, http.MethodPut, "/fapi/v1/listenKey", url.Values{"listenKey": []string{listenKey}})
	return err
}

// UserDataStreamURL returns the private futures stream URL. Binance's current
// private stream endpoint supports selecting ORDER_TRADE_UPDATE explicitly;
// the legacy testnet endpoint is kept for demo/testnet-compatible hosts.
func UserDataStreamURL(baseURL, listenKey string) string {
	host := strings.ToLower(baseURL)
	switch {
	case strings.Contains(host, "demo-fapi.binance.com"):
		return "wss://demo-fstream.binance.com/private/ws?listenKey=" + url.QueryEscape(listenKey) + "&events=ORDER_TRADE_UPDATE"
	case strings.Contains(host, "testnet.binancefuture.com"):
		return "wss://stream.binancefuture.com/ws/" + url.PathEscape(listenKey)
	default:
		return "wss://fstream.binance.com/private/ws?listenKey=" + url.QueryEscape(listenKey) + "&events=ORDER_TRADE_UPDATE"
	}
}

type userDataOrderTradeUpdate struct {
	EventType string `json:"e"`
	EventTime int64  `json:"E"`
	Order     struct {
		Symbol        string `json:"s"`
		Side          string `json:"S"`
		PositionSide  string `json:"ps"`
		EventType     string `json:"x"`
		OrderID       int64  `json:"i"`
		LastQty       string `json:"l"`
		LastPrice     string `json:"L"`
		AveragePrice  string `json:"ap"`
		Commission    string `json:"n"`
		CommissionCcy string `json:"N"`
		RealizedPnl   string `json:"rp"`
		TradeID       int64  `json:"t"`
		TransactionAt int64  `json:"T"`
		Maker         bool   `json:"m"`
	} `json:"o"`
}

type userDataAccountUpdate struct {
	EventType string `json:"e"`
	EventTime int64  `json:"E"`
	Account   struct {
		Positions []struct {
			Symbol         string `json:"s"`
			PositionAmt    string `json:"pa"`
			EntryPrice     string `json:"ep"`
			BreakEven      string `json:"bep"`
			UnrealizedPnl  string `json:"up"`
			IsolatedWallet string `json:"iw"`
			PositionSide   string `json:"ps"`
		} `json:"P"`
	} `json:"a"`
}

// ParseUserDataTrade converts an ORDER_TRADE_UPDATE execution into the
// existing UserTrade shape. Non-trade order updates are deliberately ignored.
func ParseUserDataTrade(payload []byte) (UserTrade, bool) {
	var event userDataOrderTradeUpdate
	if json.Unmarshal(payload, &event) != nil {
		return UserTrade{}, false
	}
	if event.EventType == "" {
		var wrapped struct {
			Data json.RawMessage `json:"data"`
		}
		if json.Unmarshal(payload, &wrapped) == nil && len(wrapped.Data) > 0 {
			_ = json.Unmarshal(wrapped.Data, &event)
		}
	}
	if event.EventType != "ORDER_TRADE_UPDATE" || event.Order.EventType != "TRADE" {
		return UserTrade{}, false
	}
	if strings.TrimSpace(event.Order.Symbol) == "" || strings.TrimSpace(event.Order.LastQty) == "" {
		return UserTrade{}, false
	}
	price := strings.TrimSpace(event.Order.LastPrice)
	if price == "" {
		price = strings.TrimSpace(event.Order.AveragePrice)
	}
	fillTime := event.Order.TransactionAt
	if fillTime <= 0 {
		fillTime = event.EventTime
	}
	return UserTrade{
		Buyer:           strings.EqualFold(event.Order.Side, "BUY"),
		Commission:      event.Order.Commission,
		CommissionAsset: event.Order.CommissionCcy,
		ID:              event.Order.TradeID,
		Maker:           event.Order.Maker,
		OrderID:         event.Order.OrderID,
		Price:           price,
		Qty:             event.Order.LastQty,
		RealizedPnl:     event.Order.RealizedPnl,
		Side:            event.Order.Side,
		PositionSide:    event.Order.PositionSide,
		Symbol:          event.Order.Symbol,
		Time:            fillTime,
	}, true
}

// ParseUserDataPositions extracts the position deltas included in an
// ACCOUNT_UPDATE event. Binance sends only changed positions, so callers must
// merge these rows with their existing snapshot.
func ParseUserDataPositions(payload []byte) ([]Position, bool) {
	var event userDataAccountUpdate
	if json.Unmarshal(payload, &event) != nil {
		return nil, false
	}
	if event.EventType == "" {
		var wrapped struct {
			Data json.RawMessage `json:"data"`
		}
		if json.Unmarshal(payload, &wrapped) == nil && len(wrapped.Data) > 0 {
			_ = json.Unmarshal(wrapped.Data, &event)
		}
	}
	if event.EventType != "ACCOUNT_UPDATE" || len(event.Account.Positions) == 0 {
		return nil, false
	}
	out := make([]Position, 0, len(event.Account.Positions))
	for _, position := range event.Account.Positions {
		if strings.TrimSpace(position.Symbol) == "" {
			continue
		}
		out = append(out, Position{
			Symbol:           position.Symbol,
			PositionAmt:      position.PositionAmt,
			EntryPrice:       position.EntryPrice,
			BreakEvenPrice:   position.BreakEven,
			UnRealizedProfit: position.UnrealizedPnl,
			IsolatedWallet:   position.IsolatedWallet,
			PositionSide:     position.PositionSide,
			UpdateTime:       event.EventTime,
		})
	}
	return out, len(out) > 0
}
