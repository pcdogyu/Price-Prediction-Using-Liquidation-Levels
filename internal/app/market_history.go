package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/exchange"
	marketview "github.com/pcdogyu/price-prediction-liquidation-levels/internal/market"
)

const (
	marketHistorySnapshotTTL        = 10 * time.Minute
	marketHistorySnapshotMaxEntries = 12
)

type marketHistorySnapshotEntry struct {
	view      domain.MarketView
	expiresAt time.Time
	lastUsed  time.Time
}

type marketHistorySnapshotBuild struct {
	done chan struct{}
	view domain.MarketView
	err  error
}

// fetchMarketHistoryKlines is replaceable in app tests. Production uses the
// Binance native interval endpoint for the complete shared snapshot.
var fetchMarketHistoryKlines = exchange.BinanceKlineHistory

// MarketHistory reads the complete chart history within the configured
// backfill window for one configured symbol and interval. The result is shared
// by all authenticated tenants in this process; the browser no longer needs to
// replay the same cursor pages for every newly opened tab.
func (s *Service) MarketHistory(ctx context.Context, symbol, intervalName string) (domain.MarketView, error) {
	if _, ok := marketview.ParseInterval(intervalName); !ok {
		return domain.MarketView{}, fmt.Errorf("unsupported market interval %q", intervalName)
	}
	key := symbol + "|" + intervalName
	now := time.Now().UTC()

	s.marketHistoryMu.Lock()
	if entry, ok := s.marketHistorySnapshots[key]; ok && now.Before(entry.expiresAt) {
		entry.lastUsed = now
		s.marketHistorySnapshots[key] = entry
		view := entry.view
		s.marketHistoryMu.Unlock()
		s.decorateMarketView(&view, symbol, view.Source)
		return view, nil
	}
	if build, ok := s.marketHistoryBuilds[key]; ok {
		s.marketHistoryMu.Unlock()
		select {
		case <-build.done:
			return build.view, build.err
		case <-ctx.Done():
			return domain.MarketView{}, ctx.Err()
		}
	}
	build := &marketHistorySnapshotBuild{done: make(chan struct{})}
	s.marketHistoryBuilds[key] = build
	s.marketHistoryMu.Unlock()

	view, err := s.buildMarketHistory(ctx, symbol, intervalName)
	if err != nil {
		// A completely empty local database is the only case where the request
		// waits for Binance. Once one local candle exists, all future requests
		// return it immediately and use the background sync queue instead.
		from, until := s.marketHistorySyncWindow(ctx, symbol, intervalName, now)
		if syncErr := s.syncMarketNow(ctx, symbol, from, until); syncErr == nil {
			view, err = s.buildMarketHistory(ctx, symbol, intervalName)
			if err == nil {
				s.decorateMarketView(&view, symbol, "binance_usdm_remote_fallback")
			}
		} else {
			err = fmt.Errorf("local market history unavailable: %w (Binance fallback: %v)", err, syncErr)
		}
	}
	s.marketHistoryMu.Lock()
	delete(s.marketHistoryBuilds, key)
	build.view, build.err = view, err
	if err == nil {
		s.evictMarketHistorySnapshots(now)
		s.marketHistorySnapshots[key] = marketHistorySnapshotEntry{view: view, expiresAt: now.Add(marketHistorySnapshotTTL), lastUsed: now}
	}
	close(build.done)
	s.marketHistoryMu.Unlock()
	return view, err
}

func (s *Service) buildMarketHistory(ctx context.Context, symbol, intervalName string) (domain.MarketView, error) {
	now := time.Now().UTC()
	first, _, exists, err := s.store.SymbolBounds(ctx, symbol)
	if err != nil {
		return domain.MarketView{}, err
	}
	if !exists {
		return domain.MarketView{}, errors.New("market history unavailable")
	}
	historyStart, historyEnd := s.marketHistorySyncWindowFromFirst(first, intervalName, now)
	localRaw, err := s.store.CandlesRange(ctx, symbol, historyStart, historyEnd)
	if err != nil {
		return domain.MarketView{}, err
	}
	summaryEnd := now.UTC().Truncate(time.Hour).Add(time.Hour)
	localSummary, err := s.store.CandlesRange(ctx, symbol, now.Add(-25*time.Hour), summaryEnd)
	if err != nil {
		return domain.MarketView{}, err
	}

	if len(localRaw) == 0 {
		return domain.MarketView{}, errors.New("market history unavailable locally")
	}
	s.mu.RLock()
	backfillComplete := s.backfillDone
	s.mu.RUnlock()
	view, err := marketview.BuildHistoryView(symbol, localRaw, localSummary, nil, intervalName, now, first, backfillComplete)
	if err != nil {
		return domain.MarketView{}, err
	}
	s.requestMarketSync(symbol, historyStart, historyEnd, intervalName)
	s.decorateMarketView(&view, symbol, "binance_usdm_local")
	view.HasMore = false
	view.NextBefore = nil
	view.Liquidations = []domain.LiquidationEvent{}
	view.LiquidationsTruncated = false
	view.LiquidationMinimumUSD = domain.DefaultLiquidationMinimumUSD
	return view, nil
}

func (s *Service) marketHistorySyncWindow(ctx context.Context, symbol, intervalName string, now time.Time) (time.Time, time.Time) {
	first, _, exists, err := s.store.SymbolBounds(ctx, symbol)
	if err != nil || !exists {
		lookback := 24 * time.Hour
		if s.cfg.BackfillDays > 0 {
			lookback = time.Duration(s.cfg.BackfillDays) * 24 * time.Hour
		}
		return now.Add(-lookback), marketHistoryEnd(now, intervalName)
	}
	return s.marketHistorySyncWindowFromFirst(first, intervalName, now)
}

func (s *Service) marketHistorySyncWindowFromFirst(first time.Time, intervalName string, now time.Time) (time.Time, time.Time) {
	historyEnd := marketHistoryEnd(now, intervalName)
	historyStart := first
	lookbackDays := s.cfg.BackfillDays
	if lookbackDays <= 0 {
		lookbackDays = 1
	}
	lookback := now.Add(-time.Duration(lookbackDays) * 24 * time.Hour)
	if lookback.After(historyStart) {
		historyStart = lookback
	}
	return historyStart, historyEnd
}

func marketHistoryEnd(now time.Time, intervalName string) time.Time {
	if intervalName == "24h" {
		return now.UTC().Truncate(24 * time.Hour).Add(24 * time.Hour)
	}
	interval, ok := marketview.ParseInterval(intervalName)
	if !ok {
		return now.UTC().Add(time.Minute)
	}
	return now.UTC().Truncate(interval).Add(interval)
}

func (s *Service) evictMarketHistorySnapshots(now time.Time) {
	for key, entry := range s.marketHistorySnapshots {
		if now.After(entry.expiresAt) {
			delete(s.marketHistorySnapshots, key)
		}
	}
	for len(s.marketHistorySnapshots) >= marketHistorySnapshotMaxEntries {
		oldestKey := ""
		var oldest time.Time
		for key, entry := range s.marketHistorySnapshots {
			if oldestKey == "" || entry.lastUsed.Before(oldest) {
				oldestKey, oldest = key, entry.lastUsed
			}
		}
		if oldestKey == "" {
			return
		}
		delete(s.marketHistorySnapshots, oldestKey)
	}
}

func (s *Service) invalidateMarketHistorySnapshots(symbol string) {
	prefix := marketSyncKey(symbol) + "|"
	s.marketHistoryMu.Lock()
	defer s.marketHistoryMu.Unlock()
	for key := range s.marketHistorySnapshots {
		if len(key) >= len(prefix) && key[:len(prefix)] == prefix {
			delete(s.marketHistorySnapshots, key)
		}
	}
}
