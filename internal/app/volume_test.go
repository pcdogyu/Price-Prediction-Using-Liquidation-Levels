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

func TestVolumeBootstrapSymbolsUsesBoundedConfiguredSet(t *testing.T) {
	svc := &Service{cfg: config.Config{BootstrapSymbols: []string{" btcusdt ", "ETHUSDT", "BTCUSDT", "SOLUSDT", ""}}}
	got := svc.volumeBootstrapSymbols()
	want := []string{"BTCUSDT", "ETHUSDT", "SOLUSDT"}
	if len(got) != len(want) {
		t.Fatalf("volume bootstrap symbols=%v, want=%v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("volume bootstrap symbols=%v, want=%v", got, want)
		}
	}
}

func TestVolumeBootstrapSymbolsFallsBackToBTCAndETH(t *testing.T) {
	svc := &Service{}
	got := svc.volumeBootstrapSymbols()
	want := []string{"BTCUSDT", "ETHUSDT"}
	if len(got) != len(want) {
		t.Fatalf("volume bootstrap fallback=%v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("volume bootstrap fallback=%v, want=%v", got, want)
		}
	}
}

func TestAggregateTradeGap(t *testing.T) {
	tests := []struct {
		name   string
		lastID int64
		ids    []int64
		want   bool
	}{
		{name: "contiguous", lastID: 9, ids: []int64{10, 11, 12}},
		{name: "duplicates", lastID: 10, ids: []int64{10, 11, 11, 12}},
		{name: "first gap", lastID: 9, ids: []int64{11, 12}, want: true},
		{name: "internal gap", lastID: 9, ids: []int64{10, 12}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows := make([]domain.AggregateTrade, len(tt.ids))
			for i, id := range tt.ids {
				rows[i].ID = id
			}
			if got := aggregateTradeGap(rows, tt.lastID); got != tt.want {
				t.Fatalf("aggregateTradeGap() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEnsureVolumeProfileDeduplicatesPendingRequest(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "volume-ensure.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	svc := &Service{
		store:               st,
		volumeContext:       context.Background(),
		volumeRequested:     make(map[string]struct{}),
		volumeStreamSymbols: make(map[string]struct{}),
		volumePending:       make(map[string]struct{}),
		volumeBackfillError: make(map[string]string),
		volumeBackfillQueue: make(chan string, 4),
	}
	if err := svc.EnsureVolumeProfile(context.Background(), "SOLUSDT", false); err != nil {
		t.Fatal(err)
	}
	if err := svc.EnsureVolumeProfile(context.Background(), "SOLUSDT", false); err != nil {
		t.Fatal(err)
	}
	if got := len(svc.volumeBackfillQueue); got != 1 {
		t.Fatalf("queued backfills=%d, want one", got)
	}
	if got := <-svc.volumeBackfillQueue; got != "SOLUSDT" {
		t.Fatalf("queued symbol=%s, want SOLUSDT", got)
	}
}

func TestOnDemandVolumeBackfillGeneratesProfile(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "volume-on-demand.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	originalFetch := fetchAggregateTrades
	t.Cleanup(func() { fetchAggregateTrades = originalFetch })
	tradeTime := time.Now().UTC().Add(-2 * time.Second)
	fetchAggregateTrades = func(_ context.Context, symbol string, fromID int64, _, _ time.Time) ([]domain.AggregateTrade, error) {
		if fromID == 0 {
			return []domain.AggregateTrade{{ID: 100, Symbol: symbol, Time: tradeTime, Price: 100, Quantity: 2, PriceText: "100"}}, nil
		}
		return []domain.AggregateTrade{
			{ID: 100, Symbol: symbol, Time: tradeTime, Price: 100, Quantity: 2, PriceText: "100"},
			{ID: 101, Symbol: symbol, Time: tradeTime.Add(time.Second), Price: 101, Quantity: 3, PriceText: "101"},
		}, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc := &Service{
		store:               st,
		log:                 slog.New(slog.NewTextHandler(io.Discard, nil)),
		backfillGate:        make(chan struct{}, 1),
		volumeContext:       ctx,
		volumeRequested:     make(map[string]struct{}),
		volumeStreamSymbols: make(map[string]struct{}),
		volumePending:       make(map[string]struct{}),
		volumeBackfillError: make(map[string]string),
		volumeBackfillQueue: make(chan string, 4),
		volumeLiveReady:     make(chan struct{}),
	}
	close(svc.volumeLiveReady)
	go svc.volumeBackfillLoop(ctx)

	if err := svc.EnsureVolumeProfile(ctx, "SOLUSDT", false); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		profile, profileErr := svc.VolumeProfile(ctx, "SOLUSDT")
		if profileErr != nil {
			t.Fatal(profileErr)
		}
		if profile.State == "ok" {
			if len(profile.Bins) != 24 || profile.VAL == nil || profile.VAH == nil || profile.TotalVolumeUSD <= 0 {
				t.Fatalf("profile=%+v", profile)
			}
			svc.volumeMu.Lock()
			_, found := svc.volumeStreamSymbols["SOLUSDT"]
			svc.volumeMu.Unlock()
			if !found {
				t.Fatal("on-demand symbol was not added to the volume stream")
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("on-demand volume profile did not complete")
}
