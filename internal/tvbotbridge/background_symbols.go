package tvbotbridge

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/app"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/exchange"
	tvbotconfig "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotconfig"
)

const tenantBackgroundCandidateLimit = 50

// RefreshBackgroundCandidates reads tenant-owned symbol selections without
// creating lazy tenant resources, ranks each tenant's Binance-compatible pairs
// by public 24h quote volume, and returns the merged base-50 plus protected
// Binance positions/orders.
func (b *Bridge) RefreshBackgroundCandidates(ctx context.Context) (domain.BackgroundSymbolSnapshot, error) {
	if b == nil || b.Tenants == nil || b.Handler == nil {
		return domain.BackgroundSymbolSnapshot{}, errors.New("TVBot tenant manager is unavailable")
	}
	tradable, err := exchange.BinanceUSDTPerpetualSymbolSet(ctx)
	if err != nil {
		return domain.BackgroundSymbolSnapshot{}, err
	}
	records, err := b.Tenants.readOnlyTenantConfigs(ctx)
	if err != nil {
		return domain.BackgroundSymbolSnapshot{}, err
	}
	if len(records) == 0 {
		return domain.BackgroundSymbolSnapshot{}, errors.New("no active TVBot tenants found")
	}

	rankedByTenant := make([][]exchange.TopSymbol, 0, len(records))
	for _, record := range records {
		configured := configuredBinanceSymbols(record.Config)
		if len(configured) == 0 {
			continue
		}
		rows, rankErr := exchange.BinanceTopConfiguredUSDTPerpetuals(ctx, configured, tenantBackgroundCandidateLimit)
		if rankErr != nil {
			// A tenant with no rankable configured symbols must not invalidate
			// other tenants' valid candidates. Public API and exchange metadata
			// failures invalidate the refresh so the previous snapshot survives.
			if !exchange.IsNoRankableUSDTPerpetuals(rankErr) {
				return domain.BackgroundSymbolSnapshot{}, rankErr
			}
			continue
		}
		rankedByTenant = append(rankedByTenant, rows)
	}
	rows := mergeConfiguredCandidates(rankedByTenant, app.BackgroundCandidateLimitForProvider())
	if len(rows) == 0 {
		return domain.BackgroundSymbolSnapshot{}, errors.New("no configured Binance USDT symbol has usable volume")
	}
	now := time.Now().UTC()
	snapshot := domain.BackgroundSymbolSnapshot{TenantCount: len(records), BaseCount: len(rows), RefreshedAt: now}
	baseSet := make(map[string]struct{}, len(rows))
	for i, row := range rows {
		baseSet[row.Symbol] = struct{}{}
		snapshot.Symbols = append(snapshot.Symbols, domain.WatchSymbol{Symbol: row.Symbol, Rank: i + 1, QuoteVolumeUSD: row.QuoteVolumeUSD, UpdatedAt: now})
	}

	protected := map[string]struct{}{"BTCUSDT": {}}
	protected["ETHUSDT"] = struct{}{}
	for _, record := range records {
		if !record.Exists && !strings.EqualFold(record.Username, b.Tenants.legacyOwner) {
			continue
		}
		scope, scopeErr := b.Tenants.readOnlyScope(record.Username, record.Config)
		if scopeErr != nil {
			continue
		}
		protectedServer := b.Handler.WithUserScope(scope)
		protectedRows, protectedErr := protectedServer.ProtectedBinanceSymbols(ctx)
		if protectedErr != nil {
			continue
		}
		for _, symbol := range protectedRows {
			if _, ok := tradable[symbol]; ok {
				protected[symbol] = struct{}{}
			}
		}
	}
	protectedRows := make([]string, 0, len(protected))
	for symbol := range protected {
		if _, ok := baseSet[symbol]; ok {
			continue
		}
		protectedRows = append(protectedRows, symbol)
	}
	sort.Strings(protectedRows)
	for _, symbol := range protectedRows {
		snapshot.ExceptionSymbols = append(snapshot.ExceptionSymbols, symbol)
		row := findTopSymbol(rows, symbol)
		snapshot.Symbols = append(snapshot.Symbols, domain.WatchSymbol{Symbol: symbol, Rank: len(snapshot.Symbols) + 1, QuoteVolumeUSD: row.QuoteVolumeUSD, UpdatedAt: now})
	}
	return snapshot, nil
}

func mergeConfiguredCandidates(perTenant [][]exchange.TopSymbol, limit int) []exchange.TopSymbol {
	if limit <= 0 {
		limit = app.BackgroundCandidateLimitForProvider()
	}
	merged := make(map[string]exchange.TopSymbol)
	for _, rows := range perTenant {
		for _, row := range rows {
			previous, ok := merged[row.Symbol]
			if !ok || row.QuoteVolumeUSD > previous.QuoteVolumeUSD {
				merged[row.Symbol] = row
			}
		}
	}
	rows := make([]exchange.TopSymbol, 0, len(merged))
	for _, row := range merged {
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].QuoteVolumeUSD != rows[j].QuoteVolumeUSD {
			return rows[i].QuoteVolumeUSD > rows[j].QuoteVolumeUSD
		}
		return rows[i].Symbol < rows[j].Symbol
	})
	if len(rows) > limit {
		rows = rows[:limit]
	}
	for i := range rows {
		rows[i].Rank = i + 1
	}
	return rows
}

func findTopSymbol(rows []exchange.TopSymbol, symbol string) exchange.TopSymbol {
	for _, row := range rows {
		if row.Symbol == symbol {
			return row
		}
	}
	return exchange.TopSymbol{Symbol: symbol}
}

func configuredBinanceSymbols(cfg tvbotconfig.Config) []string {
	seen := make(map[string]struct{}, len(cfg.Symbols))
	rows := make([]string, 0, len(cfg.Symbols))
	for key, metadata := range cfg.Symbols {
		candidates := []string{metadata.Coinpair, key}
		for _, raw := range candidates {
			symbol := normalizeBinanceCandidateSymbol(raw)
			if symbol == "" {
				continue
			}
			if _, ok := seen[symbol]; ok {
				continue
			}
			seen[symbol] = struct{}{}
			rows = append(rows, symbol)
			break
		}
	}
	sort.Strings(rows)
	return rows
}

func normalizeBinanceCandidateSymbol(raw string) string {
	symbol := strings.ToUpper(strings.TrimSpace(raw))
	if symbol == "" || strings.HasPrefix(symbol, "ANALYSIS:") {
		return ""
	}
	symbol = strings.TrimSuffix(symbol, "-SWAP")
	symbol = strings.ReplaceAll(symbol, "-", "")
	symbol = strings.ReplaceAll(symbol, "_", "")
	symbol = strings.ReplaceAll(symbol, "/", "")
	if symbol == "USDT" || !strings.HasSuffix(symbol, "USDT") || len(symbol) <= len("USDT") {
		return ""
	}
	for _, r := range symbol {
		if !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') {
			return ""
		}
	}
	return symbol
}
