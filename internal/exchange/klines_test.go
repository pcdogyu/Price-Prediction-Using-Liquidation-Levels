package exchange

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeKlines points the proxy at a fixture server and returns the query it saw.
func fakeKlines(t *testing.T, handler http.HandlerFunc) *url.Values {
	t.Helper()
	seen := &url.Values{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/fapi/v1/klines" {
			t.Errorf("unexpected upstream path %s", r.URL.Path)
		}
		*seen = r.URL.Query()
		handler(w, r)
	}))
	previous := binanceFuturesBase
	binanceFuturesBase = server.URL
	t.Cleanup(func() {
		binanceFuturesBase = previous
		server.Close()
	})
	return seen
}

// klineRow renders one Binance kline row. The chart only needs the four prices
// and the quote volume; the other slots stay at their Binance positions.
func klineRow(at time.Time, open, high, low, close, quoteVolume string) string {
	return fmt.Sprintf(`[%d,%q,%q,%q,%q,"1000",%d,%q,"40","5","5",""]`, at.UnixMilli(), open, high, low, close, at.Add(time.Minute).UnixMilli(), quoteVolume)
}

// TestBinanceKlinesQueryAndMapping pins the upstream window and the candle fields.
func TestBinanceKlinesQueryAndMapping(t *testing.T) {
	until := time.Now().UTC().Add(-time.Hour).Truncate(15 * time.Minute)
	start := until.Add(-2 * 15 * time.Minute)
	seen := fakeKlines(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// the first row starts after the requested startTime because Binance floors
		// it to the interval; the last row opens at the exclusive end and is dropped
		fmt.Fprintf(w, "[%s,%s,%s]", klineRow(start.Add(30*time.Second), "1", "3", "0.5", "2", "5000"),
			klineRow(start.Add(30*time.Second).Add(15*time.Minute), "2", "4", "1", "3", "6000"), klineRow(until, "3", "9", "3", "9", "7000"))
	})
	candles, hasMore, err := BinanceKlines(context.Background(), "DOGEUSDT", "15m", 2, until)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"symbol": "DOGEUSDT", "interval": "15m", "limit": "2",
		"startTime": strconv.FormatInt(start.UnixMilli(), 10), "endTime": strconv.FormatInt(until.UnixMilli()-1, 10)} {
		if got := seen.Get(key); got != want {
			t.Fatalf("query %s=%q want %q (full %q)", key, got, want, seen.Encode())
		}
	}
	if len(candles) != 2 {
		t.Fatalf("the row at the exclusive end must be dropped: %+v", candles)
	}
	first := candles[0]
	if !first.Time.Equal(start.Add(30*time.Second)) || first.Open != 1 || first.High != 3 || first.Low != 0.5 || first.Close != 2 || first.VolumeUSD != 5000 || first.ExchangeCount != 1 {
		t.Fatalf("candle fields mapped wrong: %+v", first)
	}
	if !first.Complete || !candles[1].Complete {
		t.Fatalf("candles that already closed must be complete: %+v", candles)
	}
	if !hasMore {
		t.Fatalf("a full page must report older data even when its first row lands mid-window: %+v", candles)
	}
}

// TestBinanceKlinesGuards pins the interval check, the page clamp and the short
// page answer, none of which a browser can bypass through the proxy.
func TestBinanceKlinesGuards(t *testing.T) {
	if _, _, err := BinanceKlines(context.Background(), "BTCUSDT", "1d", 100, time.Time{}); err == nil || !strings.Contains(err.Error(), "unsupported kline interval") {
		t.Fatalf("1d is not one of the offered intervals: %v", err)
	}
	until := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Hour)
	seen := fakeKlines(t, func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "[]") })
	candles, hasMore, err := BinanceKlines(context.Background(), "BTCUSDT", "4h", 5000, until)
	if err != nil {
		t.Fatal(err)
	}
	if len(candles) != 0 || hasMore {
		t.Fatalf("an empty page must not claim older data: %v %v", candles, hasMore)
	}
	if got := seen.Get("limit"); got != "1000" {
		t.Fatalf("the limit must be clamped to the page limit: %s", got)
	}
	start, err := strconv.ParseInt(seen.Get("startTime"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	// The cursor is floored to the interval, so the window is measured from that
	// boundary rather than from the submitted timestamp.
	end := until.Truncate(4 * time.Hour).UnixMilli()
	if span := end - start; span != int64(1000*4*time.Hour/time.Millisecond) {
		t.Fatalf("the window must hold exactly limit buckets, got %d ms", span)
	}
}

// TestBinanceKlinesAlignsTheLiveWindow proves the newest candle stays inside the
// window: an unaligned right edge would drop it and leave the chart one candle
// short, and report that there is no older data.
func TestBinanceKlinesAlignsTheLiveWindow(t *testing.T) {
	const stepMS = int64(15 * time.Minute / time.Millisecond)
	seen := fakeKlines(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "[%s]", klineRow(time.Now().UTC().Truncate(15*time.Minute), "1", "2", "0.5", "1.5", "10"))
	})
	candles, _, err := BinanceKlines(context.Background(), "BTCUSDT", "15m", 4, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	start, err := strconv.ParseInt(seen.Get("startTime"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	if start%stepMS != 0 {
		t.Fatalf("the live window must start on a candle boundary: %d", start)
	}
	end, err := strconv.ParseInt(seen.Get("endTime"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	if (end+1)%stepMS != 0 {
		t.Fatalf("the live window must end on a candle boundary: %d", end)
	}
	if len(candles) != 1 {
		t.Fatalf("the live window must include the forming candle: %+v", candles)
	}
	if candles[0].Time.UnixMilli()%stepMS != 0 {
		t.Fatalf("candle times must land on bucket boundaries: %+v", candles[0])
	}
	if candles[0].Complete {
		t.Fatal("the candle that is still forming must be reported incomplete")
	}
}

// TestBinanceKlinesReportsUpstreamFailure keeps the exchange answer visible, so an
// unknown pair shows up as a chart error instead of an empty chart.
func TestBinanceKlinesReportsUpstreamFailure(t *testing.T) {
	fakeKlines(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"code":-1121,"msg":"Invalid symbol."}`, http.StatusBadRequest)
	})
	if _, _, err := BinanceKlines(context.Background(), "NOSUCHPAIR", "1m", 100, time.Time{}); err == nil ||
		!strings.Contains(err.Error(), "binance klines NOSUCHPAIR 1m") || !strings.Contains(err.Error(), "Invalid symbol") {
		t.Fatalf("the upstream error must reach the caller: %v", err)
	}
}

// TestKlineIntervalCatalog keeps the nine offered periods and their lengths.
func TestKlineIntervalCatalog(t *testing.T) {
	want := map[string]int{"1m": 1, "2m": 2, "5m": 5, "15m": 15, "30m": 30, "1h": 60, "4h": 240, "8h": 480, "12h": 720}
	if len(KlineIntervals) != len(want) {
		t.Fatalf("the chart must offer exactly nine periods: %v", KlineIntervals)
	}
	for _, name := range KlineIntervals {
		minutes, ok := KlineIntervalMinutes[name]
		if !ok || minutes != want[name] {
			t.Fatalf("interval %s maps to %d minutes, want %d", name, minutes, want[name])
		}
		delete(want, name)
	}
	if len(want) != 0 {
		t.Fatalf("intervals listed in the map but not the chart: %v", want)
	}
}
