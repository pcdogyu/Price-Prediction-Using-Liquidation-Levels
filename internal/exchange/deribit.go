package exchange

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

// One collector owns this client, limiting public requests across both currencies.
type DeribitClient struct {
	HTTP         *http.Client
	BaseURL      string
	WebSocketURL string
	Spacing      time.Duration
	next         time.Time
}

func NewDeribitClient() *DeribitClient {
	return &DeribitClient{HTTP: &http.Client{Timeout: 6 * time.Second}, BaseURL: "https://www.deribit.com/api/v2", WebSocketURL: "wss://www.deribit.com/ws/api/v2", Spacing: 500 * time.Millisecond}
}

type deribitRPC struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (c *DeribitClient) get(ctx context.Context, path string, params url.Values, target any) error {
	if delay := time.Until(c.next); delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	c.next = time.Now().Add(c.Spacing)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.BaseURL, "/")+"/public/"+path+"?"+params.Encode(), nil)
	if err != nil {
		return err
	}
	response, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("Deribit %s HTTP %d", path, response.StatusCode)
	}
	var packet deribitRPC
	if err = json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(&packet); err != nil {
		return err
	}
	if packet.Error != nil {
		return fmt.Errorf("Deribit %s: %d %s", path, packet.Error.Code, packet.Error.Message)
	}
	if len(packet.Result) == 0 || string(packet.Result) == "null" {
		return fmt.Errorf("Deribit %s: missing result", path)
	}
	return json.Unmarshal(packet.Result, target)
}

type deribitTicker struct {
	Name      string `json:"instrument_name"`
	Timestamp int64  `json:"timestamp"`
	Greeks    struct {
		Gamma *float64 `json:"gamma"`
	} `json:"greeks"`
}

// tickerSnapshots subscribes to each selected option once and collects its
// initial full ticker snapshot. This avoids one REST order-book request per
// contract and uses the same data stream for all 80 options in a currency.
func (c *DeribitClient) tickerSnapshots(ctx context.Context, names []string) (map[string]deribitTicker, error) {
	tickers := make(map[string]deribitTicker, len(names))
	if len(names) == 0 {
		return tickers, nil
	}
	if c.WebSocketURL == "" {
		return tickers, fmt.Errorf("Deribit WebSocket URL is empty")
	}
	connection, _, err := websocket.DefaultDialer.DialContext(ctx, c.WebSocketURL, nil)
	if err != nil {
		return tickers, fmt.Errorf("Deribit ticker WebSocket: %w", err)
	}
	defer connection.Close()
	stopClose := context.AfterFunc(ctx, func() { _ = connection.Close() })
	defer stopClose()
	writeDeadline := time.Now().Add(10 * time.Second)
	if deadline, ok := ctx.Deadline(); ok {
		if deadline.Before(writeDeadline) {
			writeDeadline = deadline
		}
		if err = connection.SetReadDeadline(deadline); err != nil {
			return tickers, err
		}
	}
	if err = connection.SetWriteDeadline(writeDeadline); err != nil {
		return tickers, err
	}

	channels := make([]string, 0, len(names))
	expected := make(map[string]struct{}, len(names))
	for _, name := range names {
		expected[name] = struct{}{}
		channels = append(channels, "ticker."+name+".100ms")
	}
	request := struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Method  string `json:"method"`
		Params  struct {
			Channels []string `json:"channels"`
		} `json:"params"`
	}{JSONRPC: "2.0", ID: 1, Method: "public/subscribe"}
	request.Params.Channels = channels
	if err = connection.WriteJSON(request); err != nil {
		return tickers, fmt.Errorf("Deribit ticker subscribe: %w", err)
	}

	received := make(map[string]struct{}, len(names))
	for len(received) < len(expected) {
		_, message, readErr := connection.ReadMessage()
		if readErr != nil {
			return tickers, fmt.Errorf("Deribit ticker subscription ended after %d/%d contracts: %w", len(received), len(expected), readErr)
		}
		var event struct {
			ID     *int            `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err = json.Unmarshal(message, &event); err != nil {
			return tickers, fmt.Errorf("decode Deribit ticker notification: %w", err)
		}
		if event.Error != nil {
			return tickers, fmt.Errorf("Deribit ticker subscribe: %d %s", event.Error.Code, event.Error.Message)
		}
		if event.Method != "subscription" {
			continue
		}
		var notification struct {
			Channel string          `json:"channel"`
			Data    json.RawMessage `json:"data"`
		}
		if err = json.Unmarshal(event.Params, &notification); err != nil {
			return tickers, fmt.Errorf("decode Deribit ticker channel: %w", err)
		}
		name := strings.TrimSuffix(strings.TrimPrefix(notification.Channel, "ticker."), ".100ms")
		if _, ok := expected[name]; !ok || name == notification.Channel {
			continue
		}
		if _, ok := received[name]; !ok {
			received[name] = struct{}{}
		}
		var ticker deribitTicker
		if err = json.Unmarshal(notification.Data, &ticker); err != nil {
			return tickers, fmt.Errorf("decode Deribit ticker %s: %w", name, err)
		}
		tickers[name] = ticker
	}
	return tickers, nil
}

func deribitOption(name, currency string, now time.Time) (string, bool) {
	parts := strings.Split(name, "-")
	if len(parts) != 4 || parts[0] != currency || (parts[3] != "C" && parts[3] != "P") {
		return "", false
	}
	strike, err := strconv.ParseFloat(parts[2], 64)
	if err != nil || strike <= 0 || math.IsInf(strike, 0) || math.IsNaN(strike) {
		return "", false
	}
	expiry, err := time.Parse("2Jan06", parts[1])
	if err != nil || !expiry.Add(8*time.Hour).After(now) {
		return "", false
	}
	return parts[3], true
}

func (c *DeribitClient) Gamma(ctx context.Context, currency string, now time.Time) (domain.OptionGammaPoint, error) {
	p := domain.OptionGammaPoint{Symbol: currency + "USDT", Underlying: currency, Source: "deribit", State: "ok"}
	if currency != "BTC" && currency != "ETH" {
		return p, fmt.Errorf("unsupported Deribit currency")
	}
	var summaries []struct {
		Name string  `json:"instrument_name"`
		OI   float64 `json:"open_interest"`
	}
	if err := c.get(ctx, "get_book_summary_by_currency", url.Values{"currency": {currency}, "kind": {"option"}}, &summaries); err != nil {
		return p, err
	}
	selected := summaries[:0]
	seen := map[string]bool{}
	for _, summary := range summaries {
		if _, valid := deribitOption(summary.Name, currency, now); !valid || seen[summary.Name] || summary.OI <= 0 || math.IsNaN(summary.OI) || math.IsInf(summary.OI, 0) {
			continue
		}
		seen[summary.Name] = true
		selected = append(selected, summary)
	}
	sort.Slice(selected, func(i, j int) bool {
		if selected[i].OI == selected[j].OI {
			return selected[i].Name < selected[j].Name
		}
		return selected[i].OI > selected[j].OI
	})
	p.EligibleContracts = len(selected)
	if len(selected) > 80 {
		selected = selected[:80]
	}
	p.SelectedContracts = len(selected)
	names := make([]string, 0, len(selected))
	for _, summary := range selected {
		names = append(names, summary.Name)
	}
	tickers, lastError := c.tickerSnapshots(ctx, names)
	if err := ctx.Err(); err != nil {
		return p, err
	}
	for _, summary := range selected {
		ticker, ok := tickers[summary.Name]
		if !ok {
			if lastError == nil {
				lastError = fmt.Errorf("missing ticker snapshot: %s", summary.Name)
			}
			continue
		}
		at := time.UnixMilli(ticker.Timestamp).UTC()
		side, valid := deribitOption(ticker.Name, currency, time.Now().UTC())
		if !valid || ticker.Name != summary.Name || ticker.Greeks.Gamma == nil || *ticker.Greeks.Gamma < 0 || math.IsNaN(*ticker.Greeks.Gamma) || math.IsInf(*ticker.Greeks.Gamma, 0) || ticker.Timestamp <= 0 || at.After(time.Now().Add(time.Minute)) || time.Since(at) > 2*time.Minute {
			lastError = fmt.Errorf("invalid or stale Gamma: %s", summary.Name)
			continue
		}
		if side == "C" {
			p.CallGamma += *ticker.Greeks.Gamma
		} else {
			p.PutGamma += *ticker.Greeks.Gamma
		}
		p.Contracts++
		if p.SourceFrom.IsZero() || at.Before(p.SourceFrom) {
			p.SourceFrom = at
		}
		if at.After(p.SourceTo) {
			p.SourceTo = at
		}
	}
	if err := ctx.Err(); err != nil {
		return p, err
	}
	if p.CallGamma+p.PutGamma <= 0 || math.IsInf(p.CallGamma+p.PutGamma, 0) {
		if lastError == nil {
			lastError = fmt.Errorf("no active contracts with positive Gamma")
		}
		return p, fmt.Errorf("no usable Deribit Gamma (%d/%d): %v", p.Contracts, p.SelectedContracts, lastError)
	}
	value := (p.CallGamma - p.PutGamma) / (p.CallGamma + p.PutGamma)
	p.Gamma = &value
	p.Time = time.Now().UTC()
	if p.Contracts != p.SelectedContracts {
		p.State = "partial"
		p.Warning = fmt.Sprintf("合约采集不完整：%d/%d；%v", p.Contracts, p.SelectedContracts, lastError)
	}
	return p, nil
}
