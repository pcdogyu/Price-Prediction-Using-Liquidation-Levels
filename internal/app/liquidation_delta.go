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

// AlertLiquidationDelta builds the fixed 48-hour liquidation imbalance used by
// the alert chart. The bucket end is rounded forward so the currently forming
// 10-minute bucket is visible as well.
func (s *Service) AlertLiquidationDelta(ctx context.Context, symbol string) (domain.LiquidationDeltaView, error) {
	if s == nil || s.store == nil {
		return domain.LiquidationDeltaView{}, fmt.Errorf("liquidation store unavailable")
	}
	if symbol == "" {
		return domain.LiquidationDeltaView{}, fmt.Errorf("symbol is required")
	}
	interval := AlertLiquidationDeltaInterval
	to := time.Now().UTC().Truncate(interval).Add(interval)
	from := to.Add(-AlertLiquidationDeltaHours * time.Hour)
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
