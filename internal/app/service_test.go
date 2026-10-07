package app

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/config"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/store"
)

type historyCall struct{ from, until time.Time }
type historyClient struct{ calls []historyCall }

func (*historyClient) Name() string { return "binance" }
func (c *historyClient) History(_ context.Context, symbol string, from, until time.Time) ([]domain.Candle, error) {
	c.calls = append(c.calls, historyCall{from, until})
	return []domain.Candle{{Exchange: c.Name(), Symbol: symbol, Time: from.Truncate(time.Minute), Open: 100, High: 101, Low: 99, Close: 100}}, nil
}
func (*historyClient) Current(context.Context, string) (domain.Candle, error) {
	return domain.Candle{}, nil
}

func TestNewRegistersOnlyBinance(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := New(config.Config{Symbols: []string{"BTCUSDT"}, ModelPath: filepath.Join(t.TempDir(), "model.json")}, st, logger)
	if len(svc.clients) != 1 || svc.clients[0].Name() != "binance" {
		t.Fatalf("registered clients=%v", svc.clients)
	}
}

func TestRecordLiquidationPersistsAndPublishes(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := New(config.Config{Symbols: []string{"BTCUSDT"}, ModelPath: filepath.Join(t.TempDir(), "model.json")}, st, logger)
	updates, cancel := svc.SubscribeLiquidations()
	defer cancel()
	now := time.Now().UTC().Truncate(time.Millisecond)
	event := domain.LiquidationEvent{ID: "live", Exchange: "binance", Symbol: "BTCUSDT", PositionSide: "long", EventTime: now, ReceivedAt: now, Price: 100, Quantity: 2, NotionalUSD: 200, Coverage: "sampled"}
	if err = svc.RecordLiquidation(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-updates:
		if got.ID != event.ID || got.NotionalUSD != event.NotionalUSD {
			t.Fatalf("published=%+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("liquidation event was not published")
	}
	events, truncated, err := st.Liquidations(context.Background(), "BTCUSDT", now.Add(-time.Second), now.Add(time.Second), 10, 0)
	if err != nil || truncated || len(events) != 1 || events[0].ID != event.ID {
		t.Fatalf("stored=%+v truncated=%v error=%v", events, truncated, err)
	}
}

func TestRealtimePriceBroadcastAndMarketOverlay(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := New(config.Config{Symbols: []string{"BTCUSDT"}, ModelPath: filepath.Join(t.TempDir(), "model.json")}, st, logger)
	now := time.Now().UTC().Truncate(time.Minute)
	if err = st.UpsertCandles(context.Background(), []domain.Candle{{Exchange: "binance", Symbol: "BTCUSDT", Time: now, Open: 100, High: 101, Low: 99, Close: 100}}); err != nil {
		t.Fatal(err)
	}
	updates, cancel := svc.SubscribePrices()
	defer cancel()
	svc.RecordAggregateTrade(domain.AggregateTrade{ID: 10, Symbol: "BTCUSDT", Time: now.Add(20 * time.Second), Price: 102, Quantity: 1})
	svc.RecordAggregateTrade(domain.AggregateTrade{ID: 11, Symbol: "BTCUSDT", Time: now.Add(21 * time.Second), Price: 103, Quantity: 1})
	svc.broadcastLatestPrices()
	select {
	case tick := <-updates:
		if tick.TradeID != 11 || tick.Price != 103 {
			t.Fatalf("price tick=%+v", tick)
		}
	case <-time.After(time.Second):
		t.Fatal("realtime price was not published")
	}
	view, err := svc.Market(context.Background(), "BTCUSDT", "1m", time.Time{}, 120)
	if err != nil {
		t.Fatal(err)
	}
	last := view.Candles[len(view.Candles)-1]
	if view.RealtimePrice == nil || view.RealtimePrice.TradeID != 11 || view.Summary.LastPrice != 103 || !view.Summary.UpdatedAt.Equal(now.Add(21*time.Second)) || last.Close != 103 || last.High != 103 || last.Complete {
		t.Fatalf("realtime market view=%+v last=%+v", view, last)
	}
	historical, err := svc.Market(context.Background(), "BTCUSDT", "1m", now.Add(time.Minute), 120)
	if err != nil || historical.RealtimePrice != nil || historical.Summary.LastPrice != 100 {
		t.Fatalf("historical market view=%+v error=%v", historical, err)
	}
}

func TestBootstrapRequestsOnlyMissingOlderRange(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now().UTC().Truncate(time.Minute)
	rows := []domain.Candle{
		{Exchange: "binance", Symbol: "BTCUSDT", Time: now.AddDate(0, 0, -30), Open: 100, High: 101, Low: 99, Close: 100},
		{Exchange: "binance", Symbol: "BTCUSDT", Time: now, Open: 100, High: 101, Low: 99, Close: 100},
	}
	if err = st.UpsertCandles(context.Background(), rows); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := New(config.Config{Symbols: []string{"BTCUSDT"}, BackfillDays: 180, TrainingDays: 30, ModelPath: filepath.Join(t.TempDir(), "model.json")}, st, logger)
	fake := &historyClient{}
	svc.clients = nil
	svc.clients = append(svc.clients, fake)
	svc.bootstrap(context.Background())
	if len(fake.calls) != 1 {
		t.Fatalf("history calls=%#v", fake.calls)
	}
	call := fake.calls[0]
	if call.until.Sub(now.AddDate(0, 0, -30)) > time.Second || call.until.Sub(now.AddDate(0, 0, -30)) < -time.Second {
		t.Fatalf("unexpected older range end=%v", call.until)
	}
	if days := call.until.Sub(call.from).Hours() / 24; days < 149 || days > 151 {
		t.Fatalf("unexpected missing range days=%v", days)
	}
	if svc.backfillDone {
		t.Fatal("partial response must not mark the 180 day backfill complete")
	}
}
