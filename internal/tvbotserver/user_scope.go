package server

import "fmt"

func (s *Server) scopedForUser(username string) (*Server, error) {
	credentials, err := s.UserCredentialResolver.ForUser(username)
	if err != nil {
		return nil, fmt.Errorf("resolve credentials for authenticated user: %w", err)
	}
	scoped := *s
	scoped.OKXCredentials = credentials.OKX
	scoped.OKXLiveCredentials = credentials.OKXLive
	scoped.BinanceCredentials = credentials.Binance
	scoped.BinanceLiveCredentials = credentials.BinanceLive
	scoped.GateCredentials = credentials.Gate
	scoped.GateLiveCredentials = credentials.GateLive
	scoped.positionEntryCache = &positionEntryFillCache{}
	if executor, ok := s.Executor.(ExchangeExecutor); ok {
		scoped.Executor = executor.WithEnvironmentCredentials(credentials.OKX, credentials.OKXLive, credentials.Binance, credentials.BinanceLive, credentials.Gate, credentials.GateLive)
	} else if executor, ok := s.Executor.(*ExchangeExecutor); ok && executor != nil {
		bound := executor.WithEnvironmentCredentials(credentials.OKX, credentials.OKXLive, credentials.Binance, credentials.BinanceLive, credentials.Gate, credentials.GateLive)
		scoped.Executor = bound
	}
	return &scoped, nil
}
