package coinglass

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

func TestCaptureStoresMapResponses(t *testing.T) {
	upgrader := websocket.Upgrader{}
	var server *httptest.Server
	handler := http.NewServeMux()
	handler.HandleFunc("/json/list", func(w http.ResponseWriter, _ *http.Request) {
		wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/devtools/page/map"
		_ = json.NewEncoder(w).Encode([]target{{Type: "page", URL: mapURL, WebSocketDebuggerURL: wsURL}})
	})
	handler.HandleFunc("/devtools/page/map", func(w http.ResponseWriter, r *http.Request) {
		connection, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer connection.Close()
		var decodeCommand struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		if err = connection.ReadJSON(&decodeCommand); err != nil {
			t.Error(err)
			return
		}
		decoded, _ := json.Marshal([]map[string]any{
			{"symbol": "BTC", "scope": "binance", "code": "0", "success": true, "data": map[string]any{"lastPrice": 100, "liqMapV2": map[string]any{
				"90":  [][]any{{90, 1000, 10, "h1"}, {90, 500, 25, "h2"}},
				"110": [][]any{{110, 2000, 50, "h2"}, {110, 3000, 100, "h3"}},
			}}},
			{"symbol": "BTC", "scope": "aggregate", "code": "0", "success": true, "data": map[string]any{"lastPrice": 100}},
			{"symbol": "ETH", "scope": "binance", "code": "0", "success": true, "data": map[string]any{"lastPrice": 10}},
			{"symbol": "ETH", "scope": "aggregate", "code": "0", "success": true, "data": map[string]any{"lastPrice": 10}},
		})
		_ = connection.WriteJSON(map[string]any{"id": decodeCommand.ID, "result": map[string]any{"result": map[string]any{"type": "string", "value": string(decoded)}}})
	})
	server = httptest.NewServer(handler)
	defer server.Close()

	dir := t.TempDir()
	result, err := New(server.URL, dir).Capture(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Responses) != 0 || len(result.Parsed) != 4 || result.File == "" {
		t.Fatalf("result=%+v", result)
	}
	data, err := os.ReadFile(filepath.Join(dir, "latest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved Result
	if err = json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"lastPrice": 100`) || !strings.Contains(string(data), `"lastPrice": 10`) || saved.File != result.File {
		t.Fatalf("saved capture=%s", data)
	}
	latest, err := New(server.URL, dir).Latest()
	if err != nil || len(latest.Parsed) != 4 || latest.File != result.File {
		t.Fatalf("latest=%+v error=%v", latest, err)
	}
	liquidationMap, err := New(server.URL, dir).LiquidationMap("BTCUSDT", time.Hour)
	if err != nil {
		t.Fatalf("liquidation map error=%v", err)
	}
	if liquidationMap.DataSource != "coinglass_binance_liqmap" || liquidationMap.MarkPrice != 100 || liquidationMap.ATR != 0 || liquidationMap.CapturedAt == nil || len(liquidationMap.Bins) != 2 || liquidationMap.LiquidationAboveUSD != 5000 || liquidationMap.LiquidationBelowUSD != 1500 || liquidationMap.LiquidationDirection != "down" {
		t.Fatalf("liquidation map=%+v", liquidationMap)
	}
	if liquidationMap.Bins[0].Price != 90 || liquidationMap.Bins[0].LongUSD != 1500 || liquidationMap.Bins[0].ShortUSD != 0 || liquidationMap.Bins[0].LeverageUSD["10"] != 1000 || liquidationMap.Bins[0].LeverageUSD["25"] != 500 {
		t.Fatalf("lower bin=%+v", liquidationMap.Bins[0])
	}
	if liquidationMap.Bins[1].Price != 110 || liquidationMap.Bins[1].LongUSD != 0 || liquidationMap.Bins[1].ShortUSD != 5000 || liquidationMap.Bins[1].LeverageUSD["50"] != 2000 || liquidationMap.Bins[1].LeverageUSD["100"] != 3000 {
		t.Fatalf("upper bin=%+v", liquidationMap.Bins[1])
	}
	if len(liquidationMap.TopLong) != 1 || liquidationMap.TopLong[0].Price != 90 || liquidationMap.TopLong[0].AmountUSD != 1500 || len(liquidationMap.TopShort) != 1 || liquidationMap.TopShort[0].Price != 110 || liquidationMap.TopShort[0].AmountUSD != 5000 {
		t.Fatalf("top liquidations long=%+v short=%+v", liquidationMap.TopLong, liquidationMap.TopShort)
	}
}

func TestLiquidationDirectionComparesSnapshotSides(t *testing.T) {
	tests := []struct {
		name         string
		bins         []domain.MapBin
		above, below float64
		direction    string
	}{
		{name: "up when lower side is larger", bins: []domain.MapBin{{Price: 90, TotalUSD: 700}, {Price: 110, TotalUSD: 300}}, above: 300, below: 700, direction: "up"},
		{name: "down when upper side is larger", bins: []domain.MapBin{{Price: 90, TotalUSD: 200}, {Price: 110, TotalUSD: 800}}, above: 800, below: 200, direction: "down"},
		{name: "balanced when both sides match", bins: []domain.MapBin{{Price: 90, TotalUSD: 500}, {Price: 100, TotalUSD: 999}, {Price: 110, TotalUSD: 500}}, above: 500, below: 500, direction: "balanced"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			above, below, direction := liquidationDirection(test.bins, 100)
			if above != test.above || below != test.below || direction != test.direction {
				t.Fatalf("above=%v below=%v direction=%q", above, below, direction)
			}
		})
	}
}

func TestStrongestLiquidationsReturnsTopThreeByAmount(t *testing.T) {
	bins := []domain.MapBin{
		{Price: 90, LongUSD: 100},
		{Price: 91, LongUSD: 400},
		{Price: 92, LongUSD: 200},
		{Price: 93, LongUSD: 300},
		{Price: 110, ShortUSD: 900},
		{Price: 111, ShortUSD: 600},
		{Price: 112, ShortUSD: 800},
		{Price: 113, ShortUSD: 700},
	}
	long := strongestLiquidations(bins, true, 3)
	short := strongestLiquidations(bins, false, 3)
	if len(long) != 3 || long[0].Price != 91 || long[1].Price != 93 || long[2].Price != 92 {
		t.Fatalf("long top three=%+v", long)
	}
	if len(short) != 3 || short[0].Price != 110 || short[1].Price != 112 || short[2].Price != 113 {
		t.Fatalf("short top three=%+v", short)
	}
}
