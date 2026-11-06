package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotokx"
)

func TestValidatePositionClosePriceUsesInclusiveFiftyPercentBoundary(t *testing.T) {
	tests := []struct {
		name      string
		entry     string
		close     float64
		wantError bool
	}{
		{name: "just below upward boundary", entry: "100", close: 149.99},
		{name: "exact upward boundary", entry: "100", close: 150, wantError: true},
		{name: "just below downward boundary", entry: "100", close: 50.01},
		{name: "exact downward boundary", entry: "100", close: 50, wantError: true},
		{name: "invalid entry", entry: "", close: 100, wantError: true},
		{name: "invalid close", entry: "100", close: 0, wantError: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validatePositionClosePrice(
				okx.Position{AvgPx: tc.entry, Pos: "1"},
				positionClosePriceQuote{Price: "close", Value: tc.close},
			)
			if (err != nil) != tc.wantError {
				t.Fatalf("validation error=%v, wantError=%v", err, tc.wantError)
			}
		})
	}
}

func TestOKXMarketClosePriceUsesDirectionalBookQuote(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v5/market/ticker" {
			t.Fatalf("unexpected OKX path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instId":"BTC-USDT-SWAP","bidPx":"99.9","askPx":"100.1","last":"100"}]}`))
	}))
	defer server.Close()
	client := okx.Client{
		BaseURL: server.URL,
		Credentials: okx.Credentials{
			APIKey:     "test-key",
			SecretKey:  "test-secret",
			Passphrase: "test-passphrase",
		},
		HTTPClient: server.Client(),
	}

	tests := []struct {
		name     string
		position okx.Position
		want     float64
	}{
		{name: "long closes on bid", position: okx.Position{InstID: "BTC-USDT-SWAP", Pos: "1", PosSide: "long", AvgPx: "100"}, want: 99.9},
		{name: "short closes on ask", position: okx.Position{InstID: "BTC-USDT-SWAP", Pos: "-1", PosSide: "short", AvgPx: "100"}, want: 100.1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			quote, err := okxMarketClosePrice(context.Background(), client, tc.position)
			if err != nil {
				t.Fatal(err)
			}
			if quote.Value != tc.want {
				t.Fatalf("close quote=%v, want %v", quote.Value, tc.want)
			}
		})
	}
}

func TestWaitForPositionClosePriceRefreshesAfterAbnormalQuote(t *testing.T) {
	oldDelay := positionClosePriceRetryDelay
	positionClosePriceRetryDelay = 0
	t.Cleanup(func() { positionClosePriceRetryDelay = oldDelay })

	position := okx.Position{InstID: "BTC-USDT-SWAP", Pos: "1", AvgPx: "100"}
	quotes := []positionClosePriceQuote{
		{Price: "150", Value: 150},
		{Price: "100", Value: 100},
	}
	sampleCalls := 0
	refreshCalls := 0
	gotPosition, gotQuote, err := waitForPositionClosePrice(
		context.Background(),
		position,
		func(context.Context, okx.Position) (positionClosePriceQuote, error) {
			if sampleCalls >= len(quotes) {
				t.Fatalf("unexpected extra quote sample %d", sampleCalls+1)
			}
			quote := quotes[sampleCalls]
			sampleCalls++
			return quote, nil
		},
		func(context.Context, okx.Position) (okx.Position, error) {
			refreshCalls++
			return okx.Position{InstID: "BTC-USDT-SWAP", Pos: "1", AvgPx: "100"}, nil
		},
	)
	if err != nil {
		t.Fatalf("waitForPositionClosePrice returned error: %v", err)
	}
	if sampleCalls != 2 || refreshCalls != 1 {
		t.Fatalf("samples=%d refreshes=%d, want 2 samples and 1 refresh", sampleCalls, refreshCalls)
	}
	if gotPosition.AvgPx != "100" || gotQuote.Value != 100 {
		t.Fatalf("got position=%#v quote=%#v", gotPosition, gotQuote)
	}
}

func TestWaitForPositionClosePriceStopsWhenContextIsCanceled(t *testing.T) {
	oldDelay := positionClosePriceRetryDelay
	positionClosePriceRetryDelay = time.Hour
	t.Cleanup(func() { positionClosePriceRetryDelay = oldDelay })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, _, err := waitForPositionClosePrice(
		ctx,
		okx.Position{InstID: "BTC-USDT-SWAP", Pos: "-1", AvgPx: "100"},
		func(context.Context, okx.Position) (positionClosePriceQuote, error) {
			cancel()
			return positionClosePriceQuote{Price: "150", Value: 150}, nil
		},
		func(context.Context, okx.Position) (okx.Position, error) {
			t.Fatal("position should not refresh after canceled retry")
			return okx.Position{}, nil
		},
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v, want context.Canceled", err)
	}
}
