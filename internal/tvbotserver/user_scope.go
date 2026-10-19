package server

import "fmt"

func (s *Server) scopedForUser(username string) (*Server, error) {
	credentials, err := s.UserCredentialResolver.ForUser(username)
	if err != nil {
		return nil, fmt.Errorf("resolve credentials for authenticated user: %w", err)
	}
	scoped := *s
	scoped.OKXCredentials = credentials.OKX
	scoped.BinanceCredentials = credentials.Binance
	scoped.positionEntryCache = &positionEntryFillCache{}
	if executor, ok := s.Executor.(ExchangeExecutor); ok {
		scoped.Executor = executor.WithCredentials(credentials.OKX, credentials.Binance)
	} else if executor, ok := s.Executor.(*ExchangeExecutor); ok && executor != nil {
		bound := executor.WithCredentials(credentials.OKX, credentials.Binance)
		scoped.Executor = bound
	}
	return &scoped, nil
}
