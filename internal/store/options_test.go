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
	at := time.Now().UTC().Truncate(time.Millisecond)
	cutoff := at.Add(-72 * time.Hour)
	if start, err := s.OptionGammaAvailableFrom(ctx, cutoff); err != nil || start != nil {
		t.Fatal(start, err)
	}
	zero := 0.
	p := domain.OptionGammaPoint{Symbol: "BTCUSDT", Underlying: "BTC", Source: "deribit", Time: at, Gamma: &zero, Contracts: 80, SelectedContracts: 80, EligibleContracts: 100, State: "ok"}
	for _, age := range []int{0, 1, 3, 4} {
		p.Time = at.AddDate(0, 0, -age)
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
	points, err := s.OptionGammaHistory(ctx, "BTCUSDT", at.Add(-24*time.Hour), at)
	if err != nil || len(points) != 1 || !points[0].Time.Equal(at.Add(-24*time.Hour)) {
		t.Fatal(points, err)
	}
	// Expired histories can span several delete batches when reducing retention.
	for i := 1; i <= 2001; i++ {
		p.Time = at.Add(-4*24*time.Hour - time.Duration(i)*time.Minute)
		if err = s.SaveOptionGamma(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.PruneOptionGamma(ctx, at); err != nil {
		t.Fatal(err)
	}
	if err = s.PruneDashboard(ctx, at); err != nil {
		t.Fatal(err)
	}
	start, err := s.OptionGammaAvailableFrom(ctx, cutoff)
	if err != nil || start == nil || !start.Equal(cutoff) {
		t.Fatal(start, err)
	}
	points, err = s.OptionGammaHistory(ctx, "BTCUSDT", at.AddDate(0, 0, -200), at.Add(time.Millisecond))
	if err != nil || len(points) != 3 {
		t.Fatal(points, err)
	}
}
