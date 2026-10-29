package app

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/config"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/store"
)

func TestMarketMetricBackfillRangesOnlyCoverMissingEdges(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 3, 0, 0, time.UTC)
	target := now.Add(-24 * time.Hour)
	until := now.Truncate(marketMetricInterval)

	if got := marketMetricBackfillRanges(time.Time{}, time.Time{}, false, target, now); len(got) != 1 || !got[0].from.Equal(target.Truncate(marketMetricInterval)) || !got[0].until.Equal(until) {
		t.Fatalf("empty history ranges=%+v", got)
	}
	if got := marketMetricBackfillRanges(target, until.Add(-marketMetricInterval), true, target, now); len(got) != 0 {
		t.Fatalf("complete history ranges=%+v", got)
	}

	first := target.Add(15 * time.Minute)
	last := until.Add(-20 * time.Minute)
	got := marketMetricBackfillRanges(first, last, true, target, now)
	if len(got) != 2 {
		t.Fatalf("both missing edges ranges=%+v", got)
	}
	if !got[0].from.Equal(target.Truncate(marketMetricInterval)) || !got[0].until.Equal(first.Truncate(marketMetricInterval)) {
		t.Fatalf("missing front range=%+v", got[0])
	}
	if !got[1].from.Equal(last.Truncate(marketMetricInterval).Add(marketMetricInterval)) || !got[1].until.Equal(until) {
		t.Fatalf("missing tail range=%+v", got[1])
	}
}

func TestRunMarketTasksLimitsConcurrentSymbolsAndCoversAll(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	svc := New(config.Config{MarketFetchConcurrency: 5, ModelPath: filepath.Join(t.TempDir(), "model.json")}, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	symbols := make([]string, 0, 50)
	for i := 0; i < 50; i++ {
		symbols = append(symbols, fmt.Sprintf("TOKEN%02dUSDT", i))
	}
	var active, maximum atomic.Int32
	results := svc.runMarketTasks(context.Background(), symbols, func(context.Context, string) error {
		current := active.Add(1)
		for {
			previous := maximum.Load()
			if current <= previous || maximum.CompareAndSwap(previous, current) {
				break
			}
		}
		time.Sleep(time.Millisecond)
		active.Add(-1)
		return nil
	})
	if len(results) != len(symbols) || maximum.Load() > 5 {
		t.Fatalf("results=%d symbols=%d maximum concurrency=%d", len(results), len(symbols), maximum.Load())
	}
}
