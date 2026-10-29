package server

import (
	"context"
	"errors"
	"math"
	"sort"
	"strconv"
	"strings"
)

// ProtectedBinanceSymbols returns Binance USDT-M symbols with a live position
// or normal/algo pending order for this tenant. It is intentionally a single
// daily snapshot operation; normal position and order monitors remain the
// source of live protection behavior.
func (s *Server) ProtectedBinanceSymbols(ctx context.Context) ([]string, error) {
	if s == nil || s.ConfigStore == nil {
		return nil, errors.New("Binance tenant configuration is unavailable")
	}
	cfg := s.ConfigStore.Get()
	credentials := s.binanceCredentialStore(cfg.Trading.Env)
	if credentials == nil {
		return nil, nil
	}
	ids := configuredBinanceAPIIDs(credentials.Status())
	seen := make(map[string]struct{})
	var firstErr error
	for _, requestedAPIID := range ids {
		client, _, err := s.binanceClientForCredentials(cfg, requestedAPIID)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		positions, err := client.Positions(ctx, "")
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
		} else {
			for _, position := range positions {
				amount, parseErr := strconv.ParseFloat(strings.TrimSpace(position.PositionAmt), 64)
				if parseErr == nil && math.Abs(amount) > 0 {
					addBinanceProtectedSymbol(seen, position.Symbol)
				}
			}
		}
		orders, err := client.OpenOrders(ctx, "")
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
		} else {
			for _, order := range orders {
				addBinanceProtectedSymbol(seen, order.Symbol)
			}
		}
		algoOrders, err := client.OpenAlgoOrders(ctx, "")
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
		} else {
			for _, order := range algoOrders {
				addBinanceProtectedSymbol(seen, order.Symbol)
			}
		}
	}
	rows := make([]string, 0, len(seen))
	for symbol := range seen {
		rows = append(rows, symbol)
	}
	sort.Strings(rows)
	return rows, firstErr
}

func addBinanceProtectedSymbol(seen map[string]struct{}, raw string) {
	symbol := strings.ToUpper(strings.TrimSpace(raw))
	if strings.HasSuffix(symbol, "USDT") && symbol != "USDT" {
		seen[symbol] = struct{}{}
	}
}
