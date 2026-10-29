package app

import (
	"context"
	"strings"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/exchange"
)

const symbolCatalogTTL = 5 * time.Minute

var fetchDashboardSymbols = exchange.BinanceUSDTPerpetualSymbols

// DashboardSymbolCatalog is the exchange-wide selector data used by the
// bubble page. It is intentionally separate from Config.Symbols: the latter
// controls continuous collection and model work, while this catalog only
// controls which pairs may be loaded on demand.
type DashboardSymbolCatalog struct {
	Symbols     []exchange.ContractSymbol `json:"symbols"`
	Source      string                    `json:"source"`
	RefreshedAt time.Time                 `json:"refreshed_at"`
}

// DashboardSymbolCatalog returns a cached Binance contract catalog and keeps
// serving the last good result during a temporary exchangeInfo outage.
func (s *Service) DashboardSymbolCatalog(ctx context.Context) (DashboardSymbolCatalog, error) {
	now := time.Now().UTC()
	s.symbolCatalogMu.Lock()
	if len(s.symbolCatalog) > 0 && now.Sub(s.symbolCatalogAt) < symbolCatalogTTL {
		result := DashboardSymbolCatalog{Symbols: cloneContractSymbols(s.symbolCatalog), Source: "binance_usdm_exchange_info", RefreshedAt: s.symbolCatalogAt}
		s.symbolCatalogMu.Unlock()
		return result, nil
	}
	previous := cloneContractSymbols(s.symbolCatalog)
	previousAt := s.symbolCatalogAt
	s.symbolCatalogMu.Unlock()

	fetchCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	rows, err := fetchDashboardSymbols(fetchCtx)
	cancel()
	if err == nil {
		s.symbolCatalogMu.Lock()
		s.symbolCatalog = cloneContractSymbols(rows)
		s.symbolCatalogAt = now
		s.symbolCatalogMu.Unlock()
		return DashboardSymbolCatalog{Symbols: rows, Source: "binance_usdm_exchange_info", RefreshedAt: now}, nil
	}
	if len(previous) > 0 {
		if s.log != nil {
			s.log.Warn("Binance symbol catalog refresh failed; serving stale catalog", "error", err)
		}
		return DashboardSymbolCatalog{Symbols: previous, Source: "binance_usdm_exchange_info_stale", RefreshedAt: previousAt}, nil
	}
	return DashboardSymbolCatalog{}, err
}

// DashboardSymbolSupported validates an on-demand chart selection against the
// cached exchange catalog. Configured symbols remain available when the
// exchange catalog is temporarily unavailable.
func (s *Service) DashboardSymbolSupported(ctx context.Context, symbol string) bool {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	for _, configured := range s.cfg.Symbols {
		if strings.ToUpper(strings.TrimSpace(configured)) == symbol {
			return true
		}
	}
	catalog, err := s.DashboardSymbolCatalog(ctx)
	if err != nil {
		return false
	}
	for _, row := range catalog.Symbols {
		if row.Symbol == symbol {
			return true
		}
	}
	return false
}

func (s *Service) backgroundSymbol(symbol string) bool {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	configured := s.backgroundSymbols()
	if len(configured) == 0 {
		configured = []string{"BTCUSDT", "ETHUSDT"}
	}
	for _, value := range configured {
		if strings.ToUpper(strings.TrimSpace(value)) == symbol {
			return true
		}
	}
	return false
}

func cloneContractSymbols(rows []exchange.ContractSymbol) []exchange.ContractSymbol {
	if len(rows) == 0 {
		return nil
	}
	out := make([]exchange.ContractSymbol, len(rows))
	copy(out, rows)
	return out
}
