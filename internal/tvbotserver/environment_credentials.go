package server

import (
	"fmt"
	"strings"

	tvbotbinance "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotbinance"
	tvbotconfig "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotconfig"
	tvbotgate "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotgate"
	tvbotokx "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotokx"
	tvbottrading "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

func normalizeRequestedTradeEnv(raw string) (string, error) {
	env := strings.ToLower(strings.TrimSpace(raw))
	if env == "" {
		return "", nil
	}
	switch env {
	case tvbotconfig.EnvDemo, tvbotconfig.EnvLive:
		return env, nil
	default:
		return "", fmt.Errorf("env must be %q or %q", tvbotconfig.EnvDemo, tvbotconfig.EnvLive)
	}
}

// configForRequestedTradeEnv keeps legacy requests compatible: an omitted env
// uses the current trading configuration, while an explicit env selects the
// matching credential store for this request only.
func configForRequestedTradeEnv(cfg tvbotconfig.Config, raw string) (tvbotconfig.Config, error) {
	env, err := normalizeRequestedTradeEnv(raw)
	if err != nil {
		return cfg, err
	}
	if env == "" {
		return cfg, nil
	}
	return configForTradeEnv(cfg, env), nil
}

func normalizeCredentialEnvironment(raw, fallback string) string {
	if strings.TrimSpace(raw) == "" {
		raw = fallback
	}
	if tvbottrading.NormalizeTradeEnv(raw) == tvbottrading.TradeEnvLive {
		return tvbotconfig.EnvLive
	}
	return tvbotconfig.EnvDemo
}

func (s *Server) okxCredentialStore(env string) *tvbotokx.CredentialStore {
	if normalizeCredentialEnvironment(env, tvbotconfig.EnvDemo) == tvbotconfig.EnvLive && s.OKXLiveCredentials != nil {
		return s.OKXLiveCredentials
	}
	return s.OKXCredentials
}

func (s *Server) binanceCredentialStore(env string) *tvbotbinance.CredentialStore {
	if normalizeCredentialEnvironment(env, tvbotconfig.EnvDemo) == tvbotconfig.EnvLive && s.BinanceLiveCredentials != nil {
		return s.BinanceLiveCredentials
	}
	return s.BinanceCredentials
}

func (s *Server) gateCredentialStore(env string) *tvbotgate.CredentialStore {
	if normalizeCredentialEnvironment(env, tvbotconfig.EnvDemo) == tvbotconfig.EnvLive && s.GateLiveCredentials != nil {
		return s.GateLiveCredentials
	}
	return s.GateCredentials
}

func credentialEnvironmentFromConfig(cfg tvbotconfig.Config) string {
	return normalizeCredentialEnvironment(cfg.Trading.Env, tvbotconfig.EnvDemo)
}

func credentialEnvironmentFromRuntimeConfig(cfg interface{ DemoTradingHeaderEnabled() bool }) string {
	if cfg.DemoTradingHeaderEnabled() {
		return tvbotconfig.EnvDemo
	}
	return tvbotconfig.EnvLive
}
