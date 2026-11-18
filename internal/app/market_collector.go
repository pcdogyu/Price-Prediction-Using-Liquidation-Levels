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
	volumeInput := s.volumeLiveInput
	marketInput := s.marketLiveInput
	s.volumeMu.Unlock()
	if volumeInput == nil || marketInput == nil {
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
			return s.enqueueAggregateTrade(ctx, marketInput, volumeInput, trade)
		},
		Book: s.recordBook,
	})
}

// enqueueAggregateTrade keeps the collector reader non-blocking. Candle
// persistence is isolated from volume-profile backfills, and only symbols
// with an active profile request enter the profile queue. If that queue is
// temporarily full, the next aggregate-trade ID gap triggers REST repair.
func (s *Service) enqueueAggregateTrade(ctx context.Context, marketInput, volumeInput chan domain.AggregateTrade, trade domain.AggregateTrade) error {
	s.RecordAggregateTrade(trade)
	select {
	case marketInput <- trade:
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if !s.volumeRequestedSymbol(trade.Symbol) {
		return nil
	}
	select {
	case volumeInput <- trade:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
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
