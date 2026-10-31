package app

import (
	"context"
	"sort"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

// persistLiveCandles converts the local collector's aggregate trades into the
// one-minute candles used by every chart. The main service remains the only
// SQLite writer; the collector only owns the exchange WebSocket connection.
func (s *Service) persistLiveCandles(ctx context.Context, trades []domain.AggregateTrade) error {
	if len(trades) == 0 {
		return nil
	}
	ordered := append([]domain.AggregateTrade(nil), trades...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Time.Equal(ordered[j].Time) {
			return ordered[i].ID < ordered[j].ID
		}
		return ordered[i].Time.Before(ordered[j].Time)
	})
	rowsByKey := make(map[string]domain.Candle)
	for _, trade := range ordered {
		if trade.ID <= 0 || trade.Symbol == "" || trade.Price <= 0 || trade.Quantity <= 0 || trade.Time.IsZero() {
			continue
		}
		symbol := marketSyncKey(trade.Symbol)
		bucket := trade.Time.UTC().Truncate(time.Minute)
		key := symbol + "|" + bucket.Format(time.RFC3339)

		s.localCandleMu.Lock()
		current, ok := s.localCandles[symbol]
		s.localCandleMu.Unlock()
		if !ok {
			existing, err := s.store.CandlesRange(ctx, symbol, bucket, bucket.Add(time.Minute))
			if err != nil {
				return err
			}
			if len(existing) > 0 {
				current = existing[len(existing)-1]
				ok = true
			}
		}
		if ok && current.Time.After(bucket) {
			continue
		}
		if !ok || !current.Time.Equal(bucket) {
			current = domain.Candle{Exchange: "binance", Symbol: symbol, Time: bucket, Open: trade.Price, High: trade.Price, Low: trade.Price, Close: trade.Price}
		}
		if current.Open <= 0 {
			current.Open = trade.Price
		}
		if trade.Price > current.High {
			current.High = trade.Price
		}
		if current.Low <= 0 || trade.Price < current.Low {
			current.Low = trade.Price
		}
		current.Close = trade.Price
		current.VolumeUSD += trade.Price * trade.Quantity
		current.Exchange = "binance"
		current.Symbol = symbol
		s.localCandleMu.Lock()
		s.localCandles[symbol] = current
		s.localCandleMu.Unlock()
		rowsByKey[key] = current
	}
	if len(rowsByKey) == 0 {
		return nil
	}
	rows := make([]domain.Candle, 0, len(rowsByKey))
	for _, row := range rowsByKey {
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Symbol == rows[j].Symbol {
			return rows[i].Time.Before(rows[j].Time)
		}
		return rows[i].Symbol < rows[j].Symbol
	})
	return s.store.UpsertCandles(ctx, rows)
}
