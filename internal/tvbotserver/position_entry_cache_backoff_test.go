package server

import (
	"context"
	"errors"
	"testing"
	"time"

	binance "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotbinance"
)

func TestPositionEntryFillCacheBacksOffAfterErrors(t *testing.T) {
	cache := &positionEntryFillCache{}
	started := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	wantErr := errors.New("history unavailable")
	cache.set("FILUSDT", started, nil, wantErr)
	if _, err, ok := cache.get("FILUSDT", started.Add(59*time.Second)); !ok || !errors.Is(err, wantErr) {
		t.Fatalf("first retry window = (ok %v, err %v), want cached error", ok, err)
	}
	if _, _, ok := cache.get("FILUSDT", started.Add(time.Minute)); ok {
		t.Fatal("cache should allow a retry at the first backoff boundary")
	}
	cache.set("FILUSDT", started.Add(time.Minute), nil, wantErr)
	if _, err, ok := cache.get("FILUSDT", started.Add(3*time.Minute-1)); !ok || !errors.Is(err, wantErr) {
		t.Fatalf("second retry window = (ok %v, err %v), want cached error", ok, err)
	}
	if _, _, ok := cache.get("FILUSDT", started.Add(3*time.Minute)); ok {
		t.Fatal("cache should allow the second retry after the doubled backoff")
	}
	cache.set("FILUSDT", started.Add(3*time.Minute), []positionEntryFill{{}}, nil)
	if _, err, ok := cache.get("FILUSDT", started.Add(3*time.Minute+time.Second)); !ok || err != nil {
		t.Fatalf("successful result was not cached: (ok %v, err %v)", ok, err)
	}
	item := cache.items["FILUSDT"]
	if item.failures != 0 || !item.retryAt.IsZero() {
		t.Fatalf("success did not reset failure state: %+v", item)
	}
}

func TestCancelBinancePositionProtectionOrdersDoesNoReadsWithoutSnapshotMatches(t *testing.T) {
	client := binance.Client{}
	err := cancelBinancePositionProtectionOrders(
		context.Background(), client, map[string]struct{}{"BTCUSDT": {}}, nil, nil,
	)
	if err != nil {
		t.Fatalf("empty order snapshots should not trigger reads: %v", err)
	}
}
