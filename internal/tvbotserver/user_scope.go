package server

import (
	"fmt"
	"strings"
)

func (s *Server) scopedForUser(username string) (*Server, error) {
	var scope UserScope
	var err error
	if resolver, ok := s.UserCredentialResolver.(UserScopeResolver); ok {
		scope, err = resolver.ForUserScope(username)
	} else {
		var credentials UserCredentials
		credentials, err = s.UserCredentialResolver.ForUser(username)
		scope = UserScope{Credentials: credentials}
	}
	if err != nil {
		return nil, fmt.Errorf("resolve authenticated user scope: %w", err)
	}
	return s.withUserScope(scope), nil
}

func (s *Server) withUserScope(scope UserScope) *Server {
	credentials := scope.Credentials
	scoped := *s
	if scope.ConfigStore != nil {
		scoped.ConfigStore = scope.ConfigStore
	}
	if scope.Orders != nil {
		scoped.Orders = scope.Orders
	}
	scoped.TenantID = strings.TrimSpace(scope.TenantID)
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
	return &scoped
}

// WithUserScope is used by the bridge to start background monitors against the
// same tenant resources that serve its HTTP requests.
func (s *Server) WithUserScope(scope UserScope) *Server {
	return s.withUserScope(scope)
}
