package app

import (
	"context"
	"sort"
	"strings"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/marketcollector"
)

func (s *Service) startMarketCollector(ctx context.Context) {
	if strings.TrimSpace(s.cfg.MarketCollectorSocket) == "" {
		if s.log != nil {
			s.log.Error("market collector disabled: socket is empty")
		}
		return
	}
	s.volumeMu.Lock()
	input := s.volumeLiveInput
	s.volumeMu.Unlock()
	if input == nil {
		if s.log != nil {
			s.log.Error("market collector disabled: aggregate trade input is not initialized")
		}
		return
	}
	client := marketcollector.NewClient(s.cfg.MarketCollectorSocket, s.health, s.log)
	s.marketCollectorMu.Lock()
	s.marketCollector = client
	s.marketCollectorMu.Unlock()
	go client.Run(ctx, marketcollector.Subscription{
		Liquidations:     true,
		AggregateSymbols: s.collectorAggregateSymbols(),
		DepthSymbols:     append([]string(nil), s.cfg.DepthSymbols...),
	}, marketcollector.Handler{
		Liquidation: s.RecordLiquidation,
		AggregateTrade: func(ctx context.Context, trade domain.AggregateTrade) error {
			s.RecordAggregateTrade(trade)
			select {
			case input <- trade:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
		Book: s.recordBook,
	})
}

func (s *Service) updateMarketCollectorAggregateSymbols() {
	if s == nil {
		return
	}
	s.marketCollectorMu.RLock()
	client := s.marketCollector
	s.marketCollectorMu.RUnlock()
	if client == nil {
		return
	}
	client.SetSubscription(marketcollector.Subscription{
		Liquidations:     true,
		AggregateSymbols: s.collectorAggregateSymbols(),
		DepthSymbols:     append([]string(nil), s.cfg.DepthSymbols...),
	})
}

func (s *Service) collectorAggregateSymbols() []string {
	combined := append(s.backgroundSymbols(), s.volumeStreamSymbolList()...)
	seen := make(map[string]struct{}, len(combined))
	result := make([]string, 0, len(combined))
	for _, raw := range combined {
		symbol := strings.ToUpper(strings.TrimSpace(raw))
		if symbol == "" {
			continue
		}
		if _, ok := seen[symbol]; ok {
			continue
		}
		seen[symbol] = struct{}{}
		result = append(result, symbol)
	}
	sort.Strings(result)
	return result
}
