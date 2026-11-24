package app

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/config"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/store"
)

func TestMarketInfoQueuesAuthoritativeKlinesForCVD(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "market-info-cvd.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc := New(config.Config{}, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	svc.marketSyncStarted = true

	if _, err := svc.MarketInfo(context.Background(), "ETHUSDT", "4h"); err != nil {
		t.Fatal(err)
	}
	if len(svc.marketSyncQueue) != 1 {
		t.Fatalf("market-info should enqueue one CVD candle refresh, queued=%d", len(svc.marketSyncQueue))
	}
	task := svc.marketSyncQueue[0]
	if task.symbol != "ETHUSDT" || task.preferredInterval != "1m" || time.Since(task.from) < 24*time.Hour || task.until.IsZero() {
		t.Fatalf("unexpected CVD refresh task: %+v", task)
	}
}
