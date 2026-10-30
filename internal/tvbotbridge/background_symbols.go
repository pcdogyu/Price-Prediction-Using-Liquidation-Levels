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

// RefreshBackgroundCandidates ranks the application-configured Binance pairs
// by public 24h quote volume and returns the base-20 plus protected Binance
// positions/orders discovered from the tenant scopes.
func (b *Bridge) RefreshBackgroundCandidates(ctx context.Context, configured []string) (domain.BackgroundSymbolSnapshot, error) {
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
	rows, rankErr := exchange.BinanceTopConfiguredUSDTPerpetuals(ctx, configured, app.BackgroundCandidateLimitForProvider())
	if rankErr != nil {
		return domain.BackgroundSymbolSnapshot{}, rankErr
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
	appendProtectedCandidates(&snapshot, rows, baseSet, protected, now)
	return snapshot, nil
}

func appendProtectedCandidates(snapshot *domain.BackgroundSymbolSnapshot, rows []exchange.TopSymbol, baseSet, protected map[string]struct{}, at time.Time) {
	if snapshot == nil {
		return
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
		snapshot.Symbols = append(snapshot.Symbols, domain.WatchSymbol{Symbol: symbol, Rank: len(snapshot.Symbols) + 1, QuoteVolumeUSD: row.QuoteVolumeUSD, UpdatedAt: at})
	}
}

func findTopSymbol(rows []exchange.TopSymbol, symbol string) exchange.TopSymbol {
	for _, row := range rows {
		if row.Symbol == symbol {
			return row
		}
	}
	return exchange.TopSymbol{Symbol: symbol}
}

// configuredBinanceSymbols and normalizeBinanceCandidateSymbol remain shared
// by TVBot tenant configuration tests and legacy callers. Candidate ranking no
// longer uses them: the alert base set comes from the application's APP_SYMBOLS.
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
