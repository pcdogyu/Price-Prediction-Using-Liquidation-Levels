package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
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

// MarketHistory reads the complete locally stored history for one configured
// symbol and interval. The result is shared by all authenticated tenants in
// this process; the browser no longer needs to replay the same cursor pages
// for every newly opened tab.
func (s *Service) MarketHistory(ctx context.Context, symbol, intervalName string) (domain.MarketView, error) {
	if !s.backgroundSymbol(symbol) {
		return domain.MarketView{}, errors.New("shared market history unavailable for on-demand symbol")
	}
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
	raw, err := s.store.CandlesRange(ctx, symbol, first, now.Add(time.Minute))
	if err != nil {
		return domain.MarketView{}, err
	}
	summary, err := s.store.CandlesRange(ctx, symbol, now.Add(-25*time.Hour), now.Add(time.Minute))
	if err != nil {
		return domain.MarketView{}, err
	}
	s.mu.RLock()
	backfillComplete := s.backfillDone
	s.mu.RUnlock()
	view, err := marketview.BuildHistoryView(symbol, raw, summary, nil, intervalName, now, first, backfillComplete)
	if err != nil {
		return domain.MarketView{}, err
	}
	view.Source = "binance_usdm_shared_snapshot"
	view.HasMore = false
	view.NextBefore = nil
	view.Liquidations = []domain.LiquidationEvent{}
	view.LiquidationsTruncated = false
	view.LiquidationMinimumUSD = domain.DefaultLiquidationMinimumUSD
	return view, nil
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
