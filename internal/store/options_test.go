package store

import (
	"context"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

func TestOptionGammaPersistenceBoundsZeroAndRetention(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "options.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	cutoff := at.Add(-time.Duration(domain.OptionGammaMaxHours) * time.Hour)
	if start, err := s.OptionGammaAvailableFrom(ctx, cutoff); err != nil || start != nil {
		t.Fatal(start, err)
	}
	zero := 0.
	p := domain.OptionGammaPoint{Symbol: "BTCUSDT", Underlying: "BTC", Source: "deribit", Time: at, Gamma: &zero, Contracts: 80, SelectedContracts: 80, EligibleContracts: 100, State: "ok"}
	for _, age := range []time.Duration{0, 10 * time.Minute, 24 * time.Hour, 25 * time.Hour, 25*time.Hour + 4*time.Minute, 25*time.Hour + 10*time.Minute} {
		p.Time = at.Add(-age)
		if err = s.SaveOptionGamma(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	p.Time = cutoff.Add(-time.Millisecond)
	if err = s.SaveOptionGamma(ctx, p); err != nil {
		t.Fatal(err)
	}
	p.Time = at
	p.State = "partial"
	p.Contracts = 79
	if err = s.SaveOptionGamma(ctx, p); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []*float64{nil, func() *float64 { v := math.NaN(); return &v }(), func() *float64 { v := 1.1; return &v }()} {
		invalid := p
		invalid.Gamma = bad
		if err = s.SaveOptionGamma(ctx, invalid); err == nil {
			t.Fatal("invalid sample accepted")
		}
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	latest, err := s.LatestOptionGamma(ctx, "BTCUSDT")
	if err != nil || latest.Gamma == nil || *latest.Gamma != 0 || latest.Contracts != 79 || latest.State != "partial" {
		t.Fatal(latest, err)
	}
	points, err := s.OptionGammaHistory(ctx, "BTCUSDT", at.Add(-26*time.Hour), at.Add(time.Millisecond))
	if err != nil || len(points) != 4 || !points[0].Time.Equal(at.Add(-25*time.Hour)) {
		t.Fatal(points, err)
	}
	// A single archive slot can span several delete batches.
	for i := 1; i <= 2001; i++ {
		p.Time = at.Add(-48*time.Hour + time.Duration(i)*time.Millisecond)
		if err = s.SaveOptionGamma(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	report, err := s.CleanupOptionGamma(ctx, at)
	if err != nil {
		t.Fatal(err)
	}
	if report.Expired != 1 || report.Downsampled != 2002 {
		t.Fatalf("cleanup report=%+v", report)
	}
	start, err := s.OptionGammaAvailableFrom(ctx, cutoff)
	if err != nil || start == nil || !start.Equal(at.Add(-48*time.Hour+time.Millisecond)) {
		t.Fatal(start, err)
	}
	points, err = s.OptionGammaHistory(ctx, "BTCUSDT", at.AddDate(0, 0, -200), at.Add(time.Millisecond))
	if err != nil || len(points) != 5 {
		t.Fatal(points, err)
	}
}
