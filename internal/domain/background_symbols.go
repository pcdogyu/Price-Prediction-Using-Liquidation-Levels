package domain

import "time"

// BackgroundSymbolSnapshot is the atomically applied Binance candidate set
// assembled from the active TVBot tenant configurations. Symbols contains the
// ranked base set followed by protected exceptions such as open positions.
// Only non-sensitive ranking metadata is persisted.
type BackgroundSymbolSnapshot struct {
	Symbols          []WatchSymbol `json:"symbols"`
	TenantCount      int           `json:"tenant_count"`
	BaseCount        int           `json:"base_count"`
	ExceptionSymbols []string      `json:"exception_symbols,omitempty"`
	RefreshedAt      time.Time     `json:"refreshed_at"`
}
