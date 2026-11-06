package server

import (
	"fmt"
	"strings"

	tvbotconfig "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotconfig"
	tvbottrading "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

const nonPrimaryExchangeCode = "non_primary_exchange"

// primaryExchangeForConfig resolves the tenant-wide trading exchange. An
// explicitly selected target_exchange always wins. For older tenants that
// predate the single-target setting, exactly one configured exchange is a safe
// implicit primary; multiple configured exchanges remain read-only until the
// user explicitly selects one.
func (s *Server) primaryExchangeForConfig(cfg tvbotconfig.Config) string {
	if s == nil || s.ConfigStore == nil {
		return ""
	}
	if rawTarget := strings.TrimSpace(cfg.Trading.TargetExchange); rawTarget != "" {
		if exchange := tvbottrading.NormalizeExchange(rawTarget); tvbottrading.ValidTargetExchange(exchange) {
			return exchange
		}
	}
	configured := make([]string, 0, 3)
	for _, exchange := range []string{tvbottrading.ExchangeOKX, tvbottrading.ExchangeBinance, tvbottrading.ExchangeGate} {
		if s.exchangeConfiguredInAnyEnvironment(exchange) {
			configured = append(configured, exchange)
		}
	}
	if len(configured) == 1 {
		return configured[0]
	}
	return ""
}

func (s *Server) exchangeConfiguredInAnyEnvironment(exchange string) bool {
	return s.exchangeConfiguredInEnvironment(exchange, tvbotconfig.EnvDemo) ||
		s.exchangeConfiguredInEnvironment(exchange, tvbotconfig.EnvLive)
}

func (s *Server) anyExchangeConfigured() bool {
	for _, exchange := range []string{tvbottrading.ExchangeOKX, tvbottrading.ExchangeBinance, tvbottrading.ExchangeGate} {
		if s.exchangeConfiguredInAnyEnvironment(exchange) {
			return true
		}
	}
	return false
}

func (s *Server) exchangeConfiguredInEnvironment(exchange, env string) bool {
	if s == nil {
		return false
	}
	env = strings.ToLower(strings.TrimSpace(env))
	switch tvbottrading.NormalizeExchange(exchange) {
	case tvbottrading.ExchangeBinance:
		store := s.BinanceCredentials
		if env == tvbotconfig.EnvLive {
			store = s.BinanceLiveCredentials
		}
		if store == nil {
			return false
		}
		status := store.Status()
		for _, account := range status.Credentials {
			if account.Configured {
				return true
			}
		}
	case tvbottrading.ExchangeGate:
		store := s.GateCredentials
		if env == tvbotconfig.EnvLive {
			store = s.GateLiveCredentials
		}
		if store == nil {
			return false
		}
		status := store.Status()
		for _, account := range status.Credentials {
			if account.Configured {
				return true
			}
		}
	default:
		store := s.OKXCredentials
		if env == tvbotconfig.EnvLive {
			store = s.OKXLiveCredentials
		}
		if store == nil {
			return false
		}
		status := store.Status()
		for _, account := range status.Credentials {
			if account.Configured {
				return true
			}
		}
	}
	return false
}

func (s *Server) isPrimaryExchange(cfg tvbotconfig.Config, exchange string) bool {
	primary := s.primaryExchangeForConfig(cfg)
	return primary != "" && primary == tvbottrading.NormalizeExchange(exchange)
}

func nonPrimaryExchangeError(primary, exchange string) error {
	if strings.TrimSpace(primary) == "" {
		return fmt.Errorf("no primary trading exchange is selected")
	}
	primary = tvbottrading.NormalizeExchange(primary)
	exchange = tvbottrading.NormalizeExchange(exchange)
	return fmt.Errorf("%s is non-trading; primary exchange is %s", exchange, primary)
}

func (s *Server) requirePrimaryExchange(cfg tvbotconfig.Config, exchange string) error {
	if s.isPrimaryExchange(cfg, exchange) {
		return nil
	}
	// A number of non-trading validation paths are intentionally usable before
	// any exchange credentials exist. The executor or exchange-specific handler
	// still returns its normal not-configured error, while a tenant that has
	// multiple configured exchanges without a selected primary remains blocked.
	if s.primaryExchangeForConfig(cfg) == "" && !s.anyExchangeConfigured() {
		return nil
	}
	return nonPrimaryExchangeError(s.primaryExchangeForConfig(cfg), exchange)
}

func primaryExchangeLabel(exchange string) string {
	switch tvbottrading.NormalizeExchange(exchange) {
	case tvbottrading.ExchangeBinance:
		return "Binance"
	case tvbottrading.ExchangeGate:
		return "Gate.io"
	default:
		return "OKX"
	}
}
