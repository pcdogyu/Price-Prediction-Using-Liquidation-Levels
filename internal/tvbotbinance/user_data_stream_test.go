package binance

import (
	"strings"
	"testing"
)

func TestParseUserDataTrade(t *testing.T) {
	trade, ok := ParseUserDataTrade([]byte(`{
		"e":"ORDER_TRADE_UPDATE","E":1793205213000,
		"o":{"s":"SOLUSDT","S":"SELL","ps":"SHORT","x":"TRADE","i":9001,
		"l":"0.5","L":"182.25","n":"0.01","N":"USDT","rp":"-2.5",
		"t":7001,"T":1793205212999,"m":true}
	}`))
	if !ok {
		t.Fatal("expected trade update to parse")
	}
	if trade.Symbol != "SOLUSDT" || trade.Side != "SELL" || trade.PositionSide != "SHORT" || trade.Qty != "0.5" || trade.Price != "182.25" || trade.ID != 7001 || trade.OrderID != 9001 || trade.Time != 1793205212999 || !trade.Maker {
		t.Fatalf("unexpected trade: %#v", trade)
	}
	if _, ok := ParseUserDataTrade([]byte(`{"e":"ORDER_TRADE_UPDATE","o":{"s":"SOLUSDT","x":"NEW"}}`)); ok {
		t.Fatal("non-trade order update should be ignored")
	}
}

func TestParseUserDataPositions(t *testing.T) {
	positions, ok := ParseUserDataPositions([]byte(`{
		"e":"ACCOUNT_UPDATE","E":1793205213000,
		"a":{"P":[{"s":"SOLUSDT","pa":"-0.5","ep":"182.25","bep":"182.30","up":"-1.2","iw":"50","ps":"SHORT"}]}
	}`))
	if !ok || len(positions) != 1 || positions[0].Symbol != "SOLUSDT" || positions[0].PositionAmt != "-0.5" || positions[0].PositionSide != "SHORT" {
		t.Fatalf("unexpected positions=%#v ok=%v", positions, ok)
	}
	if _, ok := ParseUserDataPositions([]byte(`{"e":"ORDER_TRADE_UPDATE"}`)); ok {
		t.Fatal("non-account update should be ignored")
	}
}

func TestUserDataStreamURLUsesPrivateUSDMSStream(t *testing.T) {
	url := UserDataStreamURL("https://fapi.binance.com", "listen-key")
	if !strings.HasPrefix(url, "wss://fstream.binance.com/private/ws?") || !strings.Contains(url, "events=ORDER_TRADE_UPDATE") {
		t.Fatalf("unexpected production stream URL: %s", url)
	}
	demoURL := UserDataStreamURL("https://demo-fapi.binance.com", "listen-key")
	if !strings.HasPrefix(demoURL, "wss://demo-fstream.binance.com/private/ws?") {
		t.Fatalf("unexpected demo stream URL: %s", demoURL)
	}
}
