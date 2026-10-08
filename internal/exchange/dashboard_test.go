package exchange

import (
	"math"
	"testing"
	"time"
)

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
	b := NewLocalBook("ETHUSDT", .01, 100, [][]string{{"100.01", "2"}, {"100.09", "3"}}, [][]string{{"100.11", "4"}})
	if applied, err := b.Update(DepthUpdate{First: 99, Last: 101, Bids: [][]string{{"100.01", "0"}}}); err != nil || !applied {
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
