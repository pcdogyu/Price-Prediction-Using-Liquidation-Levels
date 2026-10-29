package exchange

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

type metricHistoryTransport func(*http.Request) (*http.Response, error)

func (f metricHistoryTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestMetricHistoryStartWithinBinanceRetention(t *testing.T) {
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	calls := 0
	http.DefaultTransport = metricHistoryTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		start, err := strconv.ParseInt(r.URL.Query().Get("startTime"), 10, 64)
		if err != nil || time.Since(time.UnixMilli(start)) >= 30*24*time.Hour {
			t.Fatalf("historical start falls outside Binance retention: %s", r.URL.Query().Get("startTime"))
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("[]")), Header: http.Header{}}, nil
	})
	now := time.Now().UTC().Truncate(5 * time.Minute)
	rows, err := BinanceMetricHistory(context.Background(), "BTCUSDT", now.Add(-24*time.Hour), now, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("expected no rows from empty responses, got %d", rows)
	}
	if calls != 3 {
		t.Fatalf("expected OI and both ratio endpoints, got %d", calls)
	}
}

func TestAllMarketLiquidationsAndExecutedQuantity(t *testing.T) {
	payload := []byte(`{"stream":"!forceOrder@arr","data":{"e":"forceOrder","E":1700000000000,"st":1,"o":{"s":"SOLUSDC","S":"BUY","q":"20","z":"2","ap":"100","T":1700000000000}}}`)
	e, ok := parseBinanceLiquidation(payload, nil)
	if !ok || e.Symbol != "SOLUSDC" || e.PositionSide != "short" || e.Quantity != 2 || e.NotionalUSD != 200 {
		t.Fatalf("event=%+v ok=%v", e, ok)
	}
	for _, p := range []string{`{"st":2,"E":1700000000000,"o":{"s":"BTCUSD_PERP","S":"BUY","q":"10","ap":"100"}}`, `{"st":1,"E":1700000000000,"o":{"s":"SOLUSDT","S":"SELL","q":"-1","ap":"100"}}`} {
		if _, ok := parseBinanceLiquidation([]byte(p), nil); ok {
			t.Fatal("invalid liquidation accepted")
		}
	}
}
func TestDepthSynchronizationAndBuckets(t *testing.T) {
	// Binance sends both e (event type) and E (event timestamp). Without an
	// explicit lowercase e field, encoding/json matches it to E and fails.
	var wire DepthUpdate
	if err := json.Unmarshal([]byte(`{"e":"depthUpdate","E":1700000000000,"s":"ETHUSDT","U":99,"u":101,"pu":98,"b":[["100.01","0"]],"a":[]}`), &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Event != "depthUpdate" || wire.EventTime != 1700000000000 || wire.First != 99 || wire.Last != 101 {
		t.Fatalf("invalid wire update: %+v", wire)
	}
	b := NewLocalBook("ETHUSDT", .01, 100, [][]string{{"100.01", "2"}, {"100.09", "3"}}, [][]string{{"100.11", "4"}})
	if applied, err := b.Update(wire); err != nil || !applied {
		t.Fatal(applied, err)
	}
	s := b.Snapshot(time.Now())
	if len(s.Bids) != 1 || math.Abs(s.Bids[0].Price-100) > 1e-8 || s.Bids[0].Quantity != 3 || math.Abs(s.Asks[0].Price-100.2) > 1e-8 {
		t.Fatalf("snapshot=%+v", s)
	}
	if _, err := b.Update(DepthUpdate{First: 102, Last: 103, Previous: 100}); err == nil {
		t.Fatal("sequence gap accepted")
	}
	if applied, err := b.Update(DepthUpdate{First: 102, Last: 103, Previous: 101, Asks: [][]string{{"100.11", "0"}}}); err != nil || !applied {
		t.Fatal(applied, err)
	}
	if len(b.Snapshot(time.Now()).Asks) != 0 {
		t.Fatal("deleted ask remains")
	}
	if _, err := NewLocalBook("BTCUSDT", .1, 100, nil, nil).Update(DepthUpdate{First: 102, Last: 103}); err == nil {
		t.Fatal("missing initial overlap accepted")
	}
}

func TestDepthSnapshotRetainsAtMost250LevelsPerSide(t *testing.T) {
	b := NewLocalBook("BTCUSDT", .01, 100, nil, nil)
	for i := 0; i < 300; i++ {
		b.bids[int64(10000-i*10)] = 1
		b.asks[int64(10000+i*10)] = 1
	}

	snapshot := b.Snapshot(time.Now())
	if len(snapshot.Bids) != 250 || len(snapshot.Asks) != 250 {
		t.Fatalf("snapshot levels=%d/%d, want 250/250", len(snapshot.Bids), len(snapshot.Asks))
	}
	for i := 1; i < len(snapshot.Bids); i++ {
		if snapshot.Bids[i-1].Price <= snapshot.Bids[i].Price {
			t.Fatalf("bids are not sorted descending at %d: %v then %v", i, snapshot.Bids[i-1].Price, snapshot.Bids[i].Price)
		}
	}
	for i := 1; i < len(snapshot.Asks); i++ {
		if snapshot.Asks[i-1].Price >= snapshot.Asks[i].Price {
			t.Fatalf("asks are not sorted ascending at %d: %v then %v", i, snapshot.Asks[i-1].Price, snapshot.Asks[i].Price)
		}
	}
	if snapshot.BestBid != 100 || snapshot.BestAsk != 100 {
		t.Fatalf("best prices=%v/%v, want 100/100", snapshot.BestBid, snapshot.BestAsk)
	}
}

func TestDepthSnapshotDropsLevelsBeyond2500TicksPerSide(t *testing.T) {
	b := NewLocalBook("ETHUSDT", .01, 100, nil, nil)
	b.bids[232000] = 1
	b.bids[229510] = 1
	b.bids[6900] = 1
	b.asks[232001] = 1
	b.asks[234491] = 1
	b.asks[360510] = 1

	snapshot := b.Snapshot(time.Now())
	if len(snapshot.Bids) != 2 || len(snapshot.Asks) != 2 {
		t.Fatalf("snapshot levels=%d/%d, want 2/2", len(snapshot.Bids), len(snapshot.Asks))
	}
	for _, level := range append(snapshot.Bids, snapshot.Asks...) {
		if level.Price == 69 || level.Price == 3605.1 {
			t.Fatalf("far level was retained: %+v", level)
		}
	}
	if snapshot.Bids[1].Price != 2295.1 || snapshot.Asks[1].Price != 2345 {
		t.Fatalf("unexpected edge levels: bids=%+v asks=%+v", snapshot.Bids, snapshot.Asks)
	}
}

func TestGammaUnitsSignsAndMissingNetPosition(t *testing.T) {
	rows := []OptionExposure{{Contract: OptionContract{Side: "CALL", Strike: "100", Unit: 2, Expiry: time.Now().Add(time.Hour).UnixMilli()}, Gamma: .01, OI: 3}, {Contract: OptionContract{Side: "PUT", Strike: "100", Unit: 1, Expiry: time.Now().Add(time.Hour).UnixMilli()}, Gamma: .01, OI: 1}}
	g := BuildGamma("ETHUSDT", 100, rows, time.Now())
	if math.Abs(g.NetGEXUSD-5) > 1e-8 || math.Abs(g.AbsoluteGEXUSD-7) > 1e-8 || g.GammaWall == nil || *g.GammaWall != 100 || g.Contracts != 2 {
		t.Fatalf("gamma=%+v", g)
	}
	if EstimateNetPosition(nil, number("2")) != nil {
		t.Fatal("missing OI became zero")
	}
	v := EstimateNetPosition(number("300"), number("2"))
	if v == nil || *v != 100 {
		t.Fatal(v)
	}
}
