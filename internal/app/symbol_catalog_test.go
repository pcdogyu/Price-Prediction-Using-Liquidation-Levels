package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/config"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/exchange"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/store"
)

func TestDashboardSymbolCatalogCachesAndServesStaleRows(t *testing.T) {
	calls := 0
	original := fetchDashboardSymbols
	fetchDashboardSymbols = func(context.Context) ([]exchange.ContractSymbol, error) {
		calls++
		if calls > 1 {
			return nil, errors.New("temporary exchangeInfo outage")
		}
		return []exchange.ContractSymbol{{Symbol: "SOLUSDT", BaseAsset: "SOL"}}, nil
	}
	t.Cleanup(func() { fetchDashboardSymbols = original })

	st, err := store.Open(filepath.Join(t.TempDir(), "symbols.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc := New(config.Config{Symbols: []string{"BTCUSDT"}}, st, slog.New(slog.NewTextHandler(io.Discard, nil)))

	first, err := svc.DashboardSymbolCatalog(context.Background())
	if err != nil || len(first.Symbols) != 1 || first.Symbols[0].Symbol != "SOLUSDT" {
		t.Fatalf("initial catalog=%+v err=%v", first, err)
	}
	second, err := svc.DashboardSymbolCatalog(context.Background())
	if err != nil || second.Source != "binance_usdm_exchange_info" || calls != 1 {
		t.Fatalf("cached catalog=%+v calls=%d err=%v", second, calls, err)
	}
	// Expire the cache without waiting five minutes; the next failed refresh must
	// preserve the last good catalog.
	svc.symbolCatalogMu.Lock()
	svc.symbolCatalogAt = svc.symbolCatalogAt.Add(-symbolCatalogTTL - 1)
	svc.symbolCatalogMu.Unlock()
	stale, err := svc.DashboardSymbolCatalog(context.Background())
	if err != nil || stale.Source != "binance_usdm_exchange_info_stale" || len(stale.Symbols) != 1 || calls != 2 {
		t.Fatalf("stale catalog=%+v calls=%d err=%v", stale, calls, err)
	}
}
