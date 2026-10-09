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

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

// One collector owns this client, limiting public requests across both currencies.
type DeribitClient struct {
	HTTP    *http.Client
	BaseURL string
	Spacing time.Duration
	next    time.Time
}

func NewDeribitClient() *DeribitClient {
	return &DeribitClient{HTTP: &http.Client{Timeout: 6 * time.Second}, BaseURL: "https://www.deribit.com/api/v2", Spacing: 200 * time.Millisecond}
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
	var lastError error
	for _, summary := range selected {
		var book struct {
			Name      string   `json:"instrument_name"`
			Timestamp int64    `json:"timestamp"`
			OI        *float64 `json:"open_interest"`
			Greeks    struct {
				Gamma *float64 `json:"gamma"`
			} `json:"greeks"`
		}
		if err := c.get(ctx, "get_order_book", url.Values{"instrument_name": {summary.Name}, "depth": {"1"}}, &book); err != nil {
			if ctx.Err() != nil {
				return p, ctx.Err()
			}
			lastError = err
			// Do not keep sending requests after a rate-limit response.
			if strings.Contains(err.Error(), "10028") || strings.Contains(err.Error(), "HTTP 429") {
				break
			}
			continue
		}
		at := time.UnixMilli(book.Timestamp).UTC()
		side, valid := deribitOption(book.Name, currency, time.Now().UTC())
		if !valid || book.Name != summary.Name || book.Greeks.Gamma == nil || *book.Greeks.Gamma < 0 || math.IsNaN(*book.Greeks.Gamma) || math.IsInf(*book.Greeks.Gamma, 0) || book.OI == nil || *book.OI <= 0 || book.Timestamp <= 0 || at.After(time.Now().Add(time.Minute)) || time.Since(at) > 2*time.Minute {
			lastError = fmt.Errorf("invalid or stale Gamma: %s", summary.Name)
			continue
		}
		if side == "C" {
			p.CallGamma += *book.Greeks.Gamma
		} else {
			p.PutGamma += *book.Greeks.Gamma
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
