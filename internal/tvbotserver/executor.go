package server

import (
	"context"
	"errors"
	"strings"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotbinance"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotokx"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

type ExchangeExecutor struct {
	OKX     trading.Executor
	Binance trading.Executor
}

func (e ExchangeExecutor) WithCredentials(okxCredentials *okx.CredentialStore, binanceCredentials *binance.CredentialStore) ExchangeExecutor {
	return ExchangeExecutor{
		OKX:     bindOKXCredentialStore(e.OKX, okxCredentials),
		Binance: bindBinanceCredentialStore(e.Binance, binanceCredentials),
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
	switch trading.NormalizeExchange(signal.TargetExchange) {
	case trading.ExchangeBinance:
		if e.Binance == nil {
			return trading.OrderResult{}, errors.New("Binance executor is not configured")
		}
		return e.Binance.ExecuteSignal(ctx, signal, cfg)
	case trading.ExchangeOKX:
		if e.OKX == nil {
			return trading.OrderResult{}, errors.New("OKX executor is not configured")
		}
		return e.OKX.ExecuteSignal(ctx, signal, cfg)
	default:
		return trading.OrderResult{}, errors.New("unsupported target_exchange " + strings.TrimSpace(signal.TargetExchange))
	}
}

func (e ExchangeExecutor) Check(ctx context.Context, cfg trading.RuntimeConfig) (map[string]any, error) {
	if e.OKX == nil {
		return nil, errors.New("OKX executor is not configured")
	}
	return e.OKX.Check(ctx, cfg)
}
