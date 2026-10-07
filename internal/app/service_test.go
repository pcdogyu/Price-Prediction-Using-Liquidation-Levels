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
