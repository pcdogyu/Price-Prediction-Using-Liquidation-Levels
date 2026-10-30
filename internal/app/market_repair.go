package app

import (
	"context"
	"fmt"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/exchange"
	marketview "github.com/pcdogyu/price-prediction-liquidation-levels/internal/market"
)

const marketGapRepairTimeout = 8 * time.Second

type marketGap struct {
	from    time.Time
	until   time.Time
	minutes int
}

// missingMarketGaps only returns gaps between two known aggregate candles. The
// page boundaries are intentionally ignored because the neighboring candle may
// be outside the requested local range.
func missingMarketGaps(raw []domain.Candle, interval time.Duration, now time.Time) []marketGap {
	if interval <= 0 {
		return nil
	}
	candles := marketview.Aggregate(marketview.CompositeMinutes(raw), interval, now)
	if len(candles) < 2 {
		return nil
	}
	gaps := make([]marketGap, 0)
	for index := 1; index < len(candles); index++ {
		previous := candles[index-1].Time.UTC()
		current := candles[index].Time.UTC()
		missing := int(current.Sub(previous)/interval) - 1
		if missing <= 0 {
			continue
		}
		gaps = append(gaps, marketGap{from: previous.Add(interval), until: current, minutes: missing * int(interval/time.Minute)})
	}
	return gaps
}

// RepairMarketGaps repairs only internal gaps in one requested chart page.
// It is called explicitly by the chart's manual backfill button, so normal
// market reads remain read-only and do not add periodic upstream or SQLite I/O.
func (s *Service) RepairMarketGaps(ctx context.Context, symbol, intervalName string, before time.Time, limit int) error {
	if !s.backgroundSymbol(symbol) {
		return nil
	}
	interval, ok := marketview.ParseInterval(intervalName)
	if !ok {
		return fmt.Errorf("unsupported market interval %q", intervalName)
	}
	if limit < 1 {
		limit = 120
	}
	if limit > 500 {
		limit = 500
	}
	if before.IsZero() {
		before = time.Now().UTC().Add(interval)
	}
	before = before.UTC()
	from := before.Add(-time.Duration(limit+3) * interval)
	raw, err := s.store.CandlesRange(ctx, symbol, from, before)
	if err != nil {
		return err
	}
	gaps := missingMarketGaps(raw, interval, time.Now().UTC())
	if len(gaps) == 0 {
		return nil
	}

	repairCtx, cancel := context.WithTimeout(ctx, marketGapRepairTimeout)
	defer cancel()
	release, ok := s.beginBackfill(repairCtx)
	if !ok {
		return repairCtx.Err()
	}
	defer release()

	remainingMinutes := exchange.KlinePageLimit
	for _, gap := range gaps {
		if gap.minutes > remainingMinutes {
			return fmt.Errorf("market gap too large to repair: %s to %s", gap.from.Format(time.RFC3339), gap.until.Format(time.RFC3339))
		}
		fetchCtx, cancelFetch := context.WithTimeout(repairCtx, marketGapRepairTimeout)
		rows, _, fetchErr := fetchAlertKlines(fetchCtx, symbol, "1m", gap.minutes, gap.until)
		cancelFetch()
		if fetchErr != nil {
			return fmt.Errorf("market gap fetch failed: %w", fetchErr)
		}
		repaired := make([]domain.Candle, 0, len(rows))
		for _, row := range rows {
			if row.Time.Before(gap.from) || !row.Time.Before(gap.until) {
				continue
			}
			repaired = append(repaired, domain.Candle{Exchange: "binance", Symbol: symbol, Time: row.Time, Open: row.Open, High: row.High, Low: row.Low, Close: row.Close, VolumeUSD: row.VolumeUSD})
		}
		if len(repaired) == 0 {
			return fmt.Errorf("market gap fetch returned no candles: %s to %s", gap.from.Format(time.RFC3339), gap.until.Format(time.RFC3339))
		}
		if err := s.store.UpsertCandles(repairCtx, repaired); err != nil {
			return fmt.Errorf("market gap store failed: %w", err)
		}
		remainingMinutes -= gap.minutes
		s.log.Info("market gap repaired", "symbol", symbol, "interval", intervalName, "from", gap.from, "until", gap.until, "candles", len(repaired))
	}
	return nil
}
