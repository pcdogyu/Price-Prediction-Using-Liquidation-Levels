package app

import (
	"context"
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
	aggregateSymbols := s.volumeStreamSymbolListLocked()
	s.volumeMu.Unlock()
	if input == nil {
		if s.log != nil {
			s.log.Error("market collector disabled: aggregate trade input is not initialized")
		}
		return
	}
	client := marketcollector.NewClient(s.cfg.MarketCollectorSocket, s.health, s.log)
	s.marketCollector = client
	go client.Run(ctx, marketcollector.Subscription{
		Liquidations:     true,
		AggregateSymbols: aggregateSymbols,
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

func (s *Service) updateMarketCollectorAggregateSymbols(symbols []string) {
	if s == nil || s.marketCollector == nil {
		return
	}
	s.marketCollector.SetSubscription(marketcollector.Subscription{
		Liquidations:     true,
		AggregateSymbols: append([]string(nil), symbols...),
		DepthSymbols:     append([]string(nil), s.cfg.DepthSymbols...),
	})
}
