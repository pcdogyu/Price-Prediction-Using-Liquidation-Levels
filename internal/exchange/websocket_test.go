package exchange

import (
	"strings"
	"testing"
)

func TestLiquidationSideSemantics(t *testing.T) {
	tests := []struct{ name, got, want string }{
		{"binance sell closes long", binancePositionSide("SELL"), "long"},
		{"binance buy closes short", binancePositionSide("BUY"), "short"},
		{"bybit Buy means long liquidation", bybitPositionSide("Buy"), "long"},
		{"bybit Sell means short liquidation", bybitPositionSide("Sell"), "short"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Fatalf("got %s want %s", tt.got, tt.want)
			}
		})
	}
}

func TestOfficialMessageShapes(t *testing.T) {
	binance := []byte(`{"e":"forceOrder","E":1568014460893,"o":{"s":"BTCUSDT","S":"SELL","q":"0.014","p":"9910","ap":"9910","T":1568014460893}}`)
	b, ok := parseBinanceLiquidation(binance, []string{"BTCUSDT"})
	if !ok || b.PositionSide != "long" || b.NotionalUSD != 138.74 || b.Coverage != "sampled" {
		t.Fatalf("bad Binance event: %#v ok=%v", b, ok)
	}

	bybit := []byte(`{"topic":"allLiquidation.BTCUSDT","data":[{"T":1739502303205,"s":"BTCUSDT","S":"Buy","v":"0.001","p":"95307.70"}]}`)
	by := parseBybitLiquidations(bybit, []string{"BTCUSDT"})
	if len(by) != 1 || by[0].PositionSide != "long" || by[0].NotionalUSD != 95.3077 {
		t.Fatalf("bad Bybit event: %#v", by)
	}

	okx := []byte(`{"data":[{"instId":"BTC-USDT-SWAP","details":[{"posSide":"short","bkPx":"100000","sz":"2","ts":"1739502303205"}]}]}`)
	o := parseOKXLiquidations(okx, []string{"BTCUSDT"}, map[string]float64{"BTCUSDT": .01})
	if len(o) != 1 || o[0].PositionSide != "short" || o[0].Quantity != .02 || o[0].NotionalUSD != 2000 {
		t.Fatalf("bad OKX event: %#v", o)
	}
}

func TestParseBinanceAggregateTradeAndUMFilter(t *testing.T) {
	payload := []byte(`{"stream":"btcusdt@aggTrade","data":{"e":"aggTrade","E":1700000000100,"s":"BTCUSDT","a":123,"p":"100.50","q":"2.25","T":1700000000000,"st":1}}`)
	trade, ok := ParseBinanceAggregateTrade(payload, []string{"BTCUSDT"})
	if !ok || trade.ID != 123 || trade.Price != 100.5 || trade.Quantity != 2.25 || trade.PriceText != "100.50" {
		t.Fatalf("trade=%+v ok=%v", trade, ok)
	}
	coin := []byte(`{"e":"aggTrade","s":"BTCUSDT","a":124,"p":"100","q":"1","T":1700000000001,"st":2}`)
	if _, ok = ParseBinanceAggregateTrade(coin, []string{"BTCUSDT"}); ok {
		t.Fatal("COIN-M aggregate trade must be rejected")
	}
	if !strings.HasSuffix(binanceMarketStreamBase, "/market") {
		t.Fatalf("Binance regular market streams require the routed /market endpoint: %s", binanceMarketStreamBase)
	}
}
