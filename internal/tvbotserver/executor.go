package server

import (
	"context"
	"errors"
	"strings"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotbinance"
	tvbotconfig "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotconfig"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotgate"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotokx"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

type ExchangeExecutor struct {
	OKX         trading.Executor
	OKXLive     trading.Executor
	Binance     trading.Executor
	BinanceLive trading.Executor
	Gate        trading.Executor
	GateLive    trading.Executor
}

func (e ExchangeExecutor) WithCredentials(okxCredentials *okx.CredentialStore, binanceCredentials *binance.CredentialStore, gateCredentials *gate.CredentialStore) ExchangeExecutor {
	return ExchangeExecutor{
		OKX:     bindOKXCredentialStore(e.OKX, okxCredentials),
		Binance: bindBinanceCredentialStore(e.Binance, binanceCredentials),
		Gate:    bindGateCredentialStore(e.Gate, gateCredentials),
	}
}

func (e ExchangeExecutor) WithEnvironmentCredentials(okxDemo, okxLive *okx.CredentialStore, binanceDemo, binanceLive *binance.CredentialStore, gateDemo, gateLive *gate.CredentialStore) ExchangeExecutor {
	out := e.WithCredentials(okxDemo, binanceDemo, gateDemo)
	out.OKXLive = bindOKXCredentialStore(e.OKX, okxLive)
	out.BinanceLive = bindBinanceCredentialStore(e.Binance, binanceLive)
	out.GateLive = bindGateCredentialStore(e.Gate, gateLive)
	return out
}

func bindGateCredentialStore(executor trading.Executor, credentials *gate.CredentialStore) trading.Executor {
	switch trader := executor.(type) {
	case gate.Trader:
		trader.CredentialProvider = credentials
		return trader
	case *gate.Trader:
		if trader == nil {
			return executor
		}
		copy := *trader
		copy.CredentialProvider = credentials
		return &copy
	default:
		return executor
	}
}

func bindOKXCredentialStore(executor trading.Executor, credentials *okx.CredentialStore) trading.Executor {
	switch trader := executor.(type) {
	case okx.Trader:
		trader.CredentialProvider = credentials
		return trader
	case *okx.Trader:
		if trader == nil {
			return executor
		}
		copy := *trader
		copy.CredentialProvider = credentials
		return &copy
	default:
		return executor
	}
}

func bindBinanceCredentialStore(executor trading.Executor, credentials *binance.CredentialStore) trading.Executor {
	switch trader := executor.(type) {
	case binance.Trader:
		trader.CredentialProvider = credentials
		return trader
	case *binance.Trader:
		if trader == nil {
			return executor
		}
		copy := *trader
		copy.CredentialProvider = credentials
		return &copy
	default:
		return executor
	}
}

func (e ExchangeExecutor) ExecuteSignal(ctx context.Context, signal trading.Signal, cfg trading.RuntimeConfig) (trading.OrderResult, error) {
	tradeEnv := trading.NormalizeTradeEnv(signal.TradeEnv)
	switch trading.NormalizeExchange(signal.TargetExchange) {
	case trading.ExchangeBinance:
		executor := e.Binance
		if tradeEnv == trading.TradeEnvLive && e.BinanceLive != nil {
			executor = e.BinanceLive
		}
		if executor == nil {
			return trading.OrderResult{}, errors.New("Binance executor is not configured")
		}
		return executor.ExecuteSignal(ctx, signal, cfg)
	case trading.ExchangeOKX:
		executor := e.OKX
		if tradeEnv == trading.TradeEnvLive && e.OKXLive != nil {
			executor = e.OKXLive
		}
		if executor == nil {
			return trading.OrderResult{}, errors.New("OKX executor is not configured")
		}
		return executor.ExecuteSignal(ctx, signal, cfg)
	case trading.ExchangeGate:
		executor := e.Gate
		if tradeEnv == trading.TradeEnvLive && e.GateLive != nil {
			executor = e.GateLive
		}
		if executor == nil {
			return trading.OrderResult{}, errors.New("Gate executor is not configured")
		}
		return executor.ExecuteSignal(ctx, signal, cfg)
	default:
		return trading.OrderResult{}, errors.New("unsupported target_exchange " + strings.TrimSpace(signal.TargetExchange))
	}
}

func (e ExchangeExecutor) Check(ctx context.Context, cfg trading.RuntimeConfig) (map[string]any, error) {
	executor := e.OKX
	if credentialEnvironmentFromRuntimeConfig(cfg) == tvbotconfig.EnvLive && e.OKXLive != nil {
		executor = e.OKXLive
	}
	if executor == nil {
		return nil, errors.New("OKX executor is not configured")
	}
	return executor.Check(ctx, cfg)
}
