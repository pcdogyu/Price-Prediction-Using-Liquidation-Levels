package app

import (
	"context"
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

func TestLiquidationMarketContextRepairsSmallDonchianGap(t *testing.T) {
	svc, _, st := alertTestService(t, "alert-candle-repair.db")
	defer st.Close()
	cfg := svc.alerts.engine.Config()
	eventTime := time.Now().UTC().Truncate(time.Minute)
	// Leave a small internal gap: enough history exists to qualify for repair,
	// but the strict Donchian count is still below the configured 24-hour window.
	rows := make([]domain.Candle, 0, cfg.VWAPRangeMinutes-1)
	for index := 0; index < cfg.VWAPRangeMinutes-1; index++ {
		at := eventTime.Add(-time.Duration(cfg.VWAPRangeMinutes-index) * time.Minute)
		if index == 100 {
			continue
		}
		rows = append(rows, domain.Candle{Exchange: "binance", Symbol: "BTCUSDT", Time: at,
			Open: 100, High: 102, Low: 98, Close: 100, VolumeUSD: 1000})
	}
	if err := st.UpsertCandles(context.Background(), rows); err != nil {
		t.Fatal(err)
	}

	originalFetch := fetchAlertKlines
	calls := 0
	fetchAlertKlines = func(_ context.Context, symbol, interval string, limit int, until time.Time) ([]domain.MarketCandle, bool, error) {
		calls++
		if symbol != "BTCUSDT" || interval != "1m" || limit != cfg.VWAPRangeMinutes+2 {
			t.Fatalf("unexpected repair request: %s %s %d", symbol, interval, limit)
		}
		end := until.UTC().Truncate(time.Minute)
		out := make([]domain.MarketCandle, 0, limit)
		for index := limit; index > 0; index-- {
			at := end.Add(-time.Duration(index) * time.Minute)
			out = append(out, domain.MarketCandle{Time: at, Open: 100, High: 102, Low: 98, Close: 100, VolumeUSD: 1000})
		}
		return out, false, nil
	}
	t.Cleanup(func() { fetchAlertKlines = originalFetch })

	market := svc.liquidationMarketContext(context.Background(), domain.LiquidationEvent{Symbol: "BTCUSDT", EventTime: eventTime, Price: 100}, cfg)
	if calls != 1 || !market.DonchianReady || len(market.Candles) < cfg.VWAPRangeMinutes {
		t.Fatalf("small Donchian gap was not repaired: calls=%d ready=%v candles=%d", calls, market.DonchianReady, len(market.Candles))
	}
}

func TestLiquidationMarketContextDoesNotRepairMostlyEmptyHistory(t *testing.T) {
	// Keep this guard explicit: the repair is for small polling gaps, not a way
	// to make every unconfigured alert symbol issue a historical Binance request.
	svc, _, st := alertTestService(t, "alert-candle-repair-guard.db")
	defer st.Close()
	cfg := svc.alerts.engine.Config()
	if svc.alertCandleRepairCandidate("BTCUSDT", make([]domain.Candle, 300), cfg) {
		t.Fatal("mostly empty history must not trigger the small-gap repair")
	}
	if svc.alertCandleRepairCandidate("SOLUSDT", make([]domain.Candle, cfg.VWAPRangeMinutes), cfg) {
		t.Fatal("unconfigured symbols must not trigger the local candle repair")
	}
}
