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

func TestPrioritizeBootstrapSymbolsKeepsETHFirst(t *testing.T) {
	got := prioritizeBootstrapSymbols([]string{"BTCUSDT", "SOLUSDT"})
	if len(got) != 3 || got[0] != "ETHUSDT" || got[1] != "BTCUSDT" || got[2] != "SOLUSDT" {
		t.Fatalf("bootstrap symbols=%v", got)
	}
}

func TestPersistLiveCandlesBuildsLocalOneMinuteCandle(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "local-candles.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc := New(config.Config{}, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	base := time.Now().UTC().Truncate(time.Minute).Add(-2 * time.Minute)
	trades := []domain.AggregateTrade{
		{ID: 1, Symbol: "ETHUSDT", Time: base.Add(5 * time.Second), Price: 100, Quantity: 2},
		{ID: 2, Symbol: "ETHUSDT", Time: base.Add(30 * time.Second), Price: 102, Quantity: 1},
		{ID: 3, Symbol: "ETHUSDT", Time: base.Add(50 * time.Second), Price: 99, Quantity: 3},
	}
	if err := svc.persistLiveCandles(context.Background(), trades); err != nil {
		t.Fatal(err)
	}
	rows, err := st.CandlesRange(context.Background(), "ETHUSDT", base, base.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Open != 100 || rows[0].High != 102 || rows[0].Low != 99 || rows[0].Close != 99 || rows[0].VolumeUSD != 599 {
		t.Fatalf("local candle=%+v, want OHLCV 100/102/99/99/599", rows)
	}
}

func TestAlertKlinesUsesLocalCandlesBeforeRemote(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "local-first.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc := New(config.Config{}, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	end := time.Now().UTC().Truncate(time.Minute)
	rows := make([]domain.Candle, 0, 10)
	for index := 0; index < 10; index++ {
		price := 2000 + float64(index)
		rows = append(rows, domain.Candle{Exchange: "binance", Symbol: "ETHUSDT", Time: end.Add(time.Duration(index-10) * time.Minute), Open: price, High: price + 1, Low: price - 1, Close: price, VolumeUSD: 10})
	}
	if err := st.UpsertCandles(context.Background(), rows); err != nil {
		t.Fatal(err)
	}
	original := fetchAlertKlines
	called := false
	fetchAlertKlines = func(context.Context, string, string, int, time.Time) ([]domain.MarketCandle, bool, error) {
		called = true
		return nil, false, context.Canceled
	}
	t.Cleanup(func() { fetchAlertKlines = original })
	view, err := svc.AlertKlines(context.Background(), "ETHUSDT", "5m", 2, end)
	if err != nil {
		t.Fatal(err)
	}
	if called || view.Source != "binance_usdm_local" || len(view.Candles) != 2 {
		t.Fatalf("local-first view=%+v remote_called=%v", view, called)
	}
}

func TestRemoteSyncKeepsCurrentLocalCandle(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "remote-sync.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc := New(config.Config{}, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	now := time.Now().UTC().Truncate(time.Minute)
	if err := st.UpsertCandles(context.Background(), []domain.Candle{{Exchange: "binance", Symbol: "ETHUSDT", Time: now, Open: 105, High: 106, Low: 104, Close: 105, VolumeUSD: 50}}); err != nil {
		t.Fatal(err)
	}
	original := fetchMarketHistoryKlines
	fetchMarketHistoryKlines = func(context.Context, string, string, time.Time, time.Time) ([]domain.MarketCandle, error) {
		return []domain.MarketCandle{
			{Time: now.Add(-time.Minute), Open: 100, High: 103, Low: 99, Close: 102, VolumeUSD: 20},
			{Time: now, Open: 1, High: 2, Low: 1, Close: 1, VolumeUSD: 1},
		}, nil
	}
	t.Cleanup(func() { fetchMarketHistoryKlines = original })
	if err := svc.runMarketSyncTask(context.Background(), marketSyncTask{symbol: "ETHUSDT", from: now.Add(-2 * time.Minute), until: now.Add(time.Minute), preferredInterval: "1m"}); err != nil {
		t.Fatal(err)
	}
	rows, err := st.CandlesRange(context.Background(), "ETHUSDT", now.Add(-time.Minute), now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Close != 102 || rows[1].Close != 105 {
		t.Fatalf("remote sync rows=%+v, want closed remote row and current local row", rows)
	}
}
