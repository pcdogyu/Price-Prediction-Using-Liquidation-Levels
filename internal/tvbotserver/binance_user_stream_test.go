package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	binance "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotbinance"
)

func TestConfigureBinanceUserStreamConnectionRepliesToPing(t *testing.T) {
	pongReceived := make(chan string, 1)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		conn.SetPongHandler(func(data string) error {
			select {
			case pongReceived <- data:
			default:
			}
			return nil
		})
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		if err := conn.WriteControl(websocket.PingMessage, []byte("heartbeat"), time.Now().Add(time.Second)); err != nil {
			return
		}
		_, _, _ = conn.ReadMessage()
	}))
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	client, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	configureBinanceUserStreamConnection(client)
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for {
			if _, _, err := client.ReadMessage(); err != nil {
				return
			}
		}
	}()
	select {
	case data := <-pongReceived:
		if data != "heartbeat" {
			t.Fatalf("pong payload = %q, want heartbeat", data)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for PONG")
	}
	_ = client.Close()
	<-readDone
}

func TestBinancePositionMonitorUsesFreshUserStreamSnapshot(t *testing.T) {
	client := binance.Client{BaseURL: "https://example.invalid", Credentials: binance.Credentials{APIKey: "test-key"}}
	apiID := "binance-test"
	streamID := binanceUserStreamID(client, apiID)
	now := time.Now().UTC()
	binanceUserStreamRegistry.Lock()
	binanceUserStreamRegistry.active[streamID] = struct{}{}
	binanceUserStreamRegistry.Unlock()
	updateBinanceUserStreamPositions(streamID, []binance.Position{{Symbol: "BTCUSDT", PositionSide: "BOTH", PositionAmt: "1"}}, now)
	t.Cleanup(func() {
		binanceUserStreamRegistry.Lock()
		delete(binanceUserStreamRegistry.active, streamID)
		delete(binanceUserStreamRegistry.positions, streamID)
		binanceUserStreamRegistry.Unlock()
	})
	positions, err := binancePositionMonitorPositions(t.Context(), client, apiID, now.Add(time.Second))
	if err != nil {
		t.Fatalf("position snapshot lookup failed: %v", err)
	}
	if len(positions) != 1 || positions[0].Symbol != "BTCUSDT" {
		t.Fatalf("positions = %+v, want BTCUSDT snapshot", positions)
	}
}
