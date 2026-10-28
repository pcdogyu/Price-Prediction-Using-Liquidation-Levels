package server

import (
	"fmt"
	"strings"

	tvbotconfig "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotconfig"
	tvbottrading "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

// syncAPIKeyTradeTarget makes the API-key checkbox the single source of truth
// for liquidation alert routing inside a user's tenant. Credentials are not
// deleted when an exchange is deselected; only its active trading marker is
// cleared.
func (s *Server) syncAPIKeyTradeTarget(exchange, env, apiID string, active bool) error {
	exchange = tvbottrading.NormalizeExchange(exchange)
	env = normalizeCredentialEnvironment(env, tvbotconfig.EnvDemo)
	apiID = strings.TrimSpace(apiID)
	if !tvbottrading.ValidTargetExchange(exchange) {
		return fmt.Errorf("unsupported target exchange %q", exchange)
	}
	if active {
		if s.ConfigStore != nil {
			if _, err := s.ConfigStore.UpdateSettings(func(cfg *tvbotconfig.Config) error {
				cfg.Trading.TargetExchange = exchange
				cfg.Trading.TargetAPIID = apiID
				cfg.Trading.TargetTradeEnv = env
				return nil
			}); err != nil {
				return fmt.Errorf("save trading target: %w", err)
			}
		}
		return s.clearOtherCredentialActives(exchange, env)
	}
	if err := s.clearCredentialActive(exchange, env); err != nil {
		return err
	}
	if s.ConfigStore == nil {
		return nil
	}
	current := s.ConfigStore.Get().Trading
	if tvbottrading.NormalizeExchange(current.TargetExchange) != exchange || strings.TrimSpace(current.TargetTradeEnv) != env {
		return nil
	}
	_, err := s.ConfigStore.UpdateSettings(func(cfg *tvbotconfig.Config) error {
		cfg.Trading.TargetExchange = ""
		cfg.Trading.TargetAPIID = ""
		cfg.Trading.TargetTradeEnv = ""
		return nil
	})
	return err
}

func (s *Server) clearOtherCredentialActives(selected, env string) error {
	for _, exchange := range []string{tvbottrading.ExchangeOKX, tvbottrading.ExchangeBinance, tvbottrading.ExchangeGate} {
		if exchange == selected {
			continue
		}
		if err := s.clearCredentialActive(exchange, env); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) clearCredentialActive(exchange, env string) error {
	var err error
	switch tvbottrading.NormalizeExchange(exchange) {
	case tvbottrading.ExchangeOKX:
		if store := s.okxCredentialStore(env); store != nil {
			err = store.ClearActive()
		}
	case tvbottrading.ExchangeBinance:
		if store := s.binanceCredentialStore(env); store != nil {
			err = store.ClearActive()
		}
	case tvbottrading.ExchangeGate:
		if store := s.gateCredentialStore(env); store != nil {
			err = store.ClearActive()
		}
	}
	if err != nil {
		return fmt.Errorf("clear %s active API: %w", exchange, err)
	}
	return nil
}
