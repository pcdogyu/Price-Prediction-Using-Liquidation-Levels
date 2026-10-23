package app

import (
	"context"
	"fmt"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

const (
	AlertLiquidationDeltaInterval = 10 * time.Minute
	AlertLiquidationDeltaHours    = 48
)

// AlertLiquidationDelta builds the default 48-hour liquidation imbalance used
// by the alert chart. The bucket end is rounded forward so the currently
// forming 10-minute bucket is visible as well.
func (s *Service) AlertLiquidationDelta(ctx context.Context, symbol string) (domain.LiquidationDeltaView, error) {
	to := time.Now().UTC().Truncate(AlertLiquidationDeltaInterval).Add(AlertLiquidationDeltaInterval)
	from := to.Add(-AlertLiquidationDeltaHours * time.Hour)
	return s.AlertLiquidationDeltaWindow(ctx, symbol, from, to)
}

// AlertLiquidationDeltaWindow builds a bounded, zero-filled liquidation
// imbalance for the requested time window. Callers use this for the K-line
// viewport so the lower chart can share its time axis without downloading
// unrelated history.
func (s *Service) AlertLiquidationDeltaWindow(ctx context.Context, symbol string, from, to time.Time) (domain.LiquidationDeltaView, error) {
	if s == nil || s.store == nil {
		return domain.LiquidationDeltaView{}, fmt.Errorf("liquidation store unavailable")
	}
	if symbol == "" {
		return domain.LiquidationDeltaView{}, fmt.Errorf("symbol is required")
	}
	interval := AlertLiquidationDeltaInterval
	from = from.UTC().Truncate(interval)
	to = to.UTC().Truncate(interval)
	if !from.Before(to) {
		return domain.LiquidationDeltaView{}, fmt.Errorf("liquidation window must have a positive range")
	}
	if to.Sub(from) > AlertLiquidationDeltaHours*time.Hour {
		return domain.LiquidationDeltaView{}, fmt.Errorf("liquidation window must not exceed %d hours", AlertLiquidationDeltaHours)
	}
	points, err := s.store.LiquidationDelta(ctx, symbol, from, to, interval)
	if err != nil {
		return domain.LiquidationDeltaView{}, err
	}
	return domain.LiquidationDeltaView{
		Symbol:      symbol,
		Interval:    "10m",
		IntervalMS:  int64(interval / time.Millisecond),
		Hours:       AlertLiquidationDeltaHours,
		From:        from,
		To:          to,
		Points:      points,
		Source:      "binance_liquidations",
		GeneratedAt: time.Now().UTC(),
	}, nil
}
