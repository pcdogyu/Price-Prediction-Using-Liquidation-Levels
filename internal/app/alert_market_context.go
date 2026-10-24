package app

import (
	"context"
	"strings"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/alert"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/exchange"
)

const (
	// A few missing minutes are normally an internal polling gap. Repair those
	// gaps from Binance, but do not turn a mostly empty local store into a
	// per-event historical downloader.
	alertCandleRepairMinCoverage = 60
	alertCandleRepairCooldown    = 30 * time.Second
	alertCandleRepairTimeout     = 8 * time.Second
	alertCandleRepairInterval    = time.Minute
	alertCandleRepairInitialWait = 5 * time.Second
)

func (s *Service) alertCandleRepairLoop(ctx context.Context) {
	timer := time.NewTimer(alertCandleRepairInitialWait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
	}
	s.repairConfiguredAlertCandles(ctx)
	ticker := time.NewTicker(alertCandleRepairInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.repairConfiguredAlertCandles(ctx)
		}
	}
}

func (s *Service) repairConfiguredAlertCandles(ctx context.Context) {
	if s.alerts == nil {
		return
	}
	cfg := s.alerts.engine.Config()
	now := time.Now().UTC()
	historyMinutes := alertMarketHistoryMinutes(cfg)
	from := now.Add(-time.Duration(historyMinutes) * time.Minute)
	to := now.Add(time.Minute)
	for _, configured := range s.cfg.Symbols {
		symbol := strings.ToUpper(strings.TrimSpace(configured))
		if symbol == "" {
			continue
		}
		rows, err := s.store.CandlesRange(ctx, symbol, from, to)
		_, _, rangeReady := alert.DonchianRange(rows, cfg.VWAPRangeMinutes)
		if err != nil || rangeReady {
			continue
		}
		if !s.alertCandleRepairCandidate(symbol, rows, cfg) || !s.claimAlertCandleRepair(symbol, now) {
			continue
		}
		repaired, ok := s.repairAlertCandleWindow(ctx, symbol, now, cfg)
		if !ok {
			continue
		}
		if err := s.store.UpsertCandles(ctx, repaired); err != nil {
			s.log.Warn("store alert candle repair failed", "symbol", symbol, "error", err)
		}
	}
}

// liquidationMarketContext builds the 1m market window used by the radar. The
// liquidation event price is overlaid on the current minute so a just-arrived
// cascade is evaluated against the newest price even when the minute candle is
// still being written by the regular market poller.
func (s *Service) liquidationMarketContext(ctx context.Context, event domain.LiquidationEvent, cfg alert.Config) alert.MarketContext {
	period := cfg.VWAPPeriodMinutes
	if period <= 0 {
		period = 240
	}
	historyMinutes := alertMarketHistoryMinutes(cfg)
	at := event.EventTime.UTC()
	if at.IsZero() {
		at = time.Now().UTC()
	}
	event.EventTime = at
	from := at.Add(-time.Duration(historyMinutes) * time.Minute)
	to := at.Add(time.Minute)
	symbol := strings.ToUpper(strings.TrimSpace(event.Symbol))
	rows, err := s.store.CandlesRange(ctx, symbol, from, to)
	if err != nil {
		return alert.MarketContext{Price: event.Price}
	}
	market := buildLiquidationMarketContext(event, cfg, rows)
	if market.DonchianReady || !s.alertCandleRepairCandidate(symbol, rows, cfg) || !s.claimAlertCandleRepair(symbol, time.Now().UTC()) {
		return market
	}
	repaired, ok := s.repairAlertCandleWindow(ctx, symbol, event.EventTime, cfg)
	if !ok {
		return market
	}
	if err := s.store.UpsertCandles(ctx, repaired); err != nil {
		s.log.Warn("store alert candle repair failed", "symbol", symbol, "error", err)
	}
	return buildLiquidationMarketContext(event, cfg, repaired)
}

func (s *Service) alertCandleRepairCandidate(symbol string, rows []domain.Candle, cfg alert.Config) bool {
	if len(rows) == 0 || !s.isConfiguredAlertSymbol(symbol) {
		return false
	}
	rangeMinutes := cfg.VWAPRangeMinutes
	if rangeMinutes <= 0 {
		rangeMinutes = 24 * 60
	}
	return len(rows) >= rangeMinutes-alertCandleRepairMinCoverage
}

func (s *Service) isConfiguredAlertSymbol(symbol string) bool {
	for _, configured := range s.cfg.Symbols {
		if strings.EqualFold(strings.TrimSpace(configured), symbol) {
			return true
		}
	}
	return false
}

func (s *Service) claimAlertCandleRepair(symbol string, now time.Time) bool {
	s.alertCandleRepairMu.Lock()
	defer s.alertCandleRepairMu.Unlock()
	if last, ok := s.alertCandleRepairAt[symbol]; ok && now.Sub(last) < alertCandleRepairCooldown {
		return false
	}
	if s.alertCandleRepairAt == nil {
		s.alertCandleRepairAt = make(map[string]time.Time)
	}
	s.alertCandleRepairAt[symbol] = now
	return true
}

func (s *Service) repairAlertCandleWindow(ctx context.Context, symbol string, at time.Time, cfg alert.Config) ([]domain.Candle, bool) {
	historyMinutes := alertMarketHistoryMinutes(cfg)
	if historyMinutes > exchange.KlinePageLimit {
		historyMinutes = exchange.KlinePageLimit
	}
	until := at.UTC().Add(time.Minute)
	if at.IsZero() {
		until = time.Now().UTC().Add(time.Minute)
	}
	fetchCtx, cancel := context.WithTimeout(ctx, alertCandleRepairTimeout)
	defer cancel()
	rows, _, err := fetchAlertKlines(fetchCtx, symbol, "1m", historyMinutes, until)
	if err != nil || len(rows) == 0 {
		if err != nil {
			s.log.Warn("alert candle repair fetch failed", "symbol", symbol, "error", err)
		}
		return nil, false
	}
	return marketCandlesToDomain(symbol, rows), true
}

// buildLiquidationMarketContext is the allocation-local part of the live
// context builder. Historical replay uses it with one candle query per symbol
// instead of issuing a SQLite range query for every liquidation event.
func buildLiquidationMarketContext(event domain.LiquidationEvent, cfg alert.Config, rows []domain.Candle) alert.MarketContext {
	period := cfg.VWAPPeriodMinutes
	if period <= 0 {
		period = 240
	}
	historyMinutes := alertMarketHistoryMinutes(cfg)

	price := event.Price
	at := event.EventTime.UTC()
	if at.IsZero() {
		at = time.Now().UTC()
	}
	bucket := at.Truncate(time.Minute)
	// The replay range can contain candles after the event. Only candles up to
	// the event minute are visible to that event's decision.
	end := len(rows)
	for end > 0 && rows[end-1].Time.After(bucket) {
		end--
	}
	if end == 0 {
		return alert.MarketContext{Price: price}
	}
	rows = append([]domain.Candle(nil), rows[:end]...)
	if price <= 0 {
		price = rows[len(rows)-1].Close
	}
	last := len(rows) - 1
	if rows[last].Time.Equal(bucket) {
		if price > 0 {
			rows[last].Close = price
			if rows[last].High < price {
				rows[last].High = price
			}
			if rows[last].Low <= 0 || rows[last].Low > price {
				rows[last].Low = price
			}
		}
	} else if rows[last].Time.Before(bucket) {
		rows = append(rows, domain.Candle{Exchange: "binance", Symbol: event.Symbol, Time: bucket, Open: price, High: price, Low: price, Close: price})
	}
	if len(rows) > historyMinutes {
		rows = rows[len(rows)-historyMinutes:]
	}
	if len(rows) < period {
		return alert.MarketContext{Price: price, Candles: rows}
	}
	current := rows[len(rows)-1]
	high, low, rangeReady := alert.DonchianRange(rows, cfg.VWAPRangeMinutes)
	return alert.MarketContext{Ready: true, Price: price, Candles: rows, CurrentCandle: current,
		DonchianHigh: high, DonchianLow: low, DonchianReady: rangeReady}
}

func alertMarketHistoryMinutes(cfg alert.Config) int {
	period := cfg.VWAPPeriodMinutes
	if period <= 0 {
		period = 240
	}
	rangeMinutes := cfg.VWAPRangeMinutes
	if rangeMinutes <= 0 {
		rangeMinutes = 24 * 60
	}
	if rangeMinutes < period {
		rangeMinutes = period
	}
	return rangeMinutes + 2
}
