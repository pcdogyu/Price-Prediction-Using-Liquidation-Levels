package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/config"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/store"
)

// TestDailyMaintenanceBoundsCapturesAndGammaHistory is the disk-hygiene contract:
// the browser captures and the Deribit Gamma history both stay inside their
// rolling window, and the live capture pointer survives the sweep.
func TestDailyMaintenanceBoundsCapturesAndGammaHistory(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "maintenance.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	captures := filepath.Join(dir, "captures")
	if err = os.MkdirAll(captures, 0o750); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	stamp := func(at time.Time) string { return at.Format("20060102T150405.000000000Z") }
	expired := filepath.Join(captures, "coinglass-"+stamp(now.Add(-4*24*time.Hour))+".json")
	current := filepath.Join(captures, "coinglass-"+stamp(now.Add(-time.Minute))+".json")
	latest := filepath.Join(captures, "latest.json")
	for _, path := range []string{expired, current, latest} {
		if err = os.WriteFile(path, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	past := now.Add(-4 * 24 * time.Hour)
	if err = os.Chtimes(expired, past, past); err != nil {
		t.Fatal(err)
	}

	value := 0.4
	expiredPoint := domain.OptionGammaPoint{Symbol: "BTCUSDT", Underlying: "BTC", Source: "deribit", Time: past, Gamma: &value, Contracts: 80, SelectedContracts: 80, EligibleContracts: 100, State: "ok"}
	currentPoint := expiredPoint
	currentPoint.Time = now.Add(-time.Minute)
	for _, point := range []domain.OptionGammaPoint{expiredPoint, currentPoint} {
		if err = st.SaveOptionGamma(ctx, point); err != nil {
			t.Fatal(err)
		}
	}

	s := New(config.Config{CoinGlassCaptureDir: captures, BackfillDays: 180}, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.runDailyMaintenance(ctx)

	if _, statErr := os.Stat(expired); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expired capture survived maintenance: %v", statErr)
	}
	for _, path := range []string{current, latest} {
		if _, statErr := os.Stat(path); statErr != nil {
			t.Fatalf("%s must survive: %v", path, statErr)
		}
	}
	points, err := st.OptionGammaHistory(ctx, "BTCUSDT", now.Add(-time.Duration(domain.OptionGammaMaxHours)*time.Hour), now)
	if err != nil || len(points) != 1 || !points[0].Time.Equal(currentPoint.Time) {
		t.Fatalf("gamma window=%d points=%v err=%v", domain.OptionGammaMaxHours, points, err)
	}
}
