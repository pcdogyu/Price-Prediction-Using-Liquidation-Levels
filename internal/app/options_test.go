package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/config"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/store"
)

func TestOptionsFailurePreservesHistoryAndRestart(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "options.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := New(config.Config{}, st, logger)
	view, err := s.Options(ctx, 12)
	if err != nil || len(view.Series) != 2 || view.Series[0].State != "unavailable" || view.Series[0].Latest != nil {
		t.Fatal(view, err)
	}
	value := 0.
	p := domain.OptionGammaPoint{Symbol: "BTCUSDT", Underlying: "BTC", Source: "deribit", Time: time.Now().UTC().Add(-time.Second), Gamma: &value, Contracts: 80, SelectedContracts: 80, EligibleContracts: 100, State: "ok"}
	s.recordOptionGamma(ctx, p.Symbol, p, nil)
	s.recordOptionGamma(ctx, p.Symbol, domain.OptionGammaPoint{}, errors.New("Deribit unavailable"))
	view, err = s.Options(ctx, 12)
	if err != nil || len(view.Series[0].Points) != 1 || view.Series[0].Latest.Gamma == nil || *view.Series[0].Latest.Gamma != 0 || view.Series[0].State != "partial" || view.Series[0].LastError == "" || view.Series[1].Latest != nil {
		t.Fatal(view, err)
	}
	// A fresh service reads persisted history without requiring an exchange call.
	s = New(config.Config{}, st, logger)
	view, err = s.Options(ctx, 1)
	if err != nil || view.Series[0].State != "ok" || !view.Series[0].Latest.Time.Equal(p.Time) {
		t.Fatal(view, err)
	}
	p.Time = time.Now().UTC().Add(-4 * time.Minute)
	p.Symbol = "ETHUSDT"
	p.Underlying = "ETH"
	p.State = "partial"
	p.Contracts = 40
	p.Warning = "partial fixture"
	s.recordOptionGamma(ctx, p.Symbol, p, nil)
	view, err = s.Options(ctx, 1)
	if err != nil || view.Series[1].State != "stale" || s.Health()["deribit_options_ETHUSDT"].Coverage != .5 {
		t.Fatal(view, err)
	}
	if _, err = s.Options(ctx, 169); err == nil {
		t.Fatal("invalid window accepted")
	}
}
