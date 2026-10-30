package app

import (
	"context"
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

func TestMissingMarketGapsFindsOnlyInternalIntervalGaps(t *testing.T) {
	base := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	raw := make([]domain.Candle, 0, 75)
	for minute := 0; minute < 90; minute++ {
		if minute >= 30 && minute < 45 {
			continue
		}
		raw = append(raw, domain.Candle{Exchange: "binance", Symbol: "BTCUSDT", Time: base.Add(time.Duration(minute) * time.Minute), Open: 100, High: 101, Low: 99, Close: 100, VolumeUSD: 1})
	}
	gaps := missingMarketGaps(raw, 15*time.Minute, base.Add(2*time.Hour))
	if len(gaps) != 1 {
		t.Fatalf("gaps=%d, want one gap: %+v", len(gaps), gaps)
	}
	if !gaps[0].from.Equal(base.Add(30*time.Minute)) || !gaps[0].until.Equal(base.Add(45*time.Minute)) || gaps[0].minutes != 15 {
		t.Fatalf("gap=%+v", gaps[0])
	}
}

func TestRepairMarketGapsFetchesOnlyMissingMinutes(t *testing.T) {
	svc, _, st := alertTestService(t, "market-gap-repair.db")
	base := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	raw := make([]domain.Candle, 0, 75)
	for minute := 0; minute < 90; minute++ {
		if minute >= 30 && minute < 45 {
			continue
		}
		raw = append(raw, domain.Candle{Exchange: "binance", Symbol: "BTCUSDT", Time: base.Add(time.Duration(minute) * time.Minute), Open: 100, High: 101, Low: 99, Close: 100, VolumeUSD: 1})
	}
	if err := st.UpsertCandles(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	originalFetch := fetchAlertKlines
	t.Cleanup(func() { fetchAlertKlines = originalFetch })
	calls := 0
	fetchAlertKlines = func(_ context.Context, symbol, interval string, limit int, until time.Time) ([]domain.MarketCandle, bool, error) {
		calls++
		if symbol != "BTCUSDT" || interval != "1m" || limit != 15 || !until.Equal(base.Add(45*time.Minute)) {
			t.Fatalf("unexpected repair fetch: symbol=%s interval=%s limit=%d until=%s", symbol, interval, limit, until)
		}
		rows := make([]domain.MarketCandle, 0, limit)
		for minute := 30; minute < 45; minute++ {
			rows = append(rows, domain.MarketCandle{Time: base.Add(time.Duration(minute) * time.Minute), Open: 100, High: 101, Low: 99, Close: 100, VolumeUSD: 1})
		}
		return rows, false, nil
	}
	if err := svc.RepairMarketGaps(context.Background(), "BTCUSDT", "15m", base.Add(90*time.Minute), 6); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("fetch calls=%d, want one targeted request", calls)
	}
	rows, err := st.CandlesRange(context.Background(), "BTCUSDT", base, base.Add(90*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 90 {
		t.Fatalf("stored candles=%d, want repaired 90-minute range", len(rows))
	}
}
