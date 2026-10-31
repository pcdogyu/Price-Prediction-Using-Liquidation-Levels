package marketcollector

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/exchange"
)

func TestSubscriptionNormalizesAndDeduplicatesSymbols(t *testing.T) {
	got := (Subscription{AggregateSymbols: []string{" ethusdt ", "BTCUSDT", "ETHUSDT", "USDT", "bad-usdt"}}).normalized()
	want := []string{"BTCUSDT", "ETHUSDT"}
	if len(got.AggregateSymbols) != len(want) {
		t.Fatalf("symbols=%v, want %v", got.AggregateSymbols, want)
	}
	for i := range want {
		if got.AggregateSymbols[i] != want[i] {
			t.Fatalf("symbols=%v, want %v", got.AggregateSymbols, want)
		}
	}
}

func TestClientReceivesEventsFromCollectorSocket(t *testing.T) {
	socket := `C:\mc-test-` + strconv.FormatInt(time.Now().UnixNano(), 10) + `.sock`
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	defer os.Remove(socket)

	accepted := make(chan struct{})
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		decoder := json.NewDecoder(bufio.NewReader(conn))
		var req request
		if decoder.Decode(&req) != nil {
			return
		}
		if req.Type != requestSubscribe || len(req.Subscription.AggregateSymbols) != 1 || req.Subscription.AggregateSymbols[0] != "ETHUSDT" {
			return
		}
		close(accepted)
		event := Event{Type: eventAggregate, Symbol: "ETHUSDT", AggregateTrade: &domain.AggregateTrade{ID: 42, Symbol: "ETHUSDT", Price: 2000, Quantity: 1, Time: time.Now().UTC()}}
		payload, _ := json.Marshal(event)
		_, _ = conn.Write(append(payload, '\n'))
	}()

	trades := make(chan domain.AggregateTrade, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := NewClient(socket, exchange.NewHealthRegistry(), nil)
	go client.Run(ctx, Subscription{AggregateSymbols: []string{"ETHUSDT"}}, Handler{AggregateTrade: func(_ context.Context, trade domain.AggregateTrade) error {
		trades <- trade
		return nil
	}})

	select {
	case <-accepted:
	case <-time.After(2 * time.Second):
		t.Fatal("collector client did not send subscription")
	}
	select {
	case trade := <-trades:
		if trade.ID != 42 || trade.Symbol != "ETHUSDT" {
			t.Fatalf("trade=%+v", trade)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("collector client did not deliver aggregate trade")
	}
}
