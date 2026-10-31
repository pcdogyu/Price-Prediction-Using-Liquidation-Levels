package app

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

const (
	marketSyncFreshFor    = 10 * time.Minute
	marketSyncJobTimeout  = 90 * time.Second
	marketSyncRetryDelay  = 2 * time.Second
	marketSyncJobInterval = time.Second
	marketSyncRetryAfter  = 15 * time.Second
)

type marketSyncTask struct {
	symbol            string
	from              time.Time
	until             time.Time
	preferredInterval string
}

type marketSyncState struct {
	queued      bool
	running     bool
	from        time.Time
	until       time.Time
	lastSuccess time.Time
	lastError   string
	retryAt     time.Time
}

func marketSyncKey(symbol string) string {
	return strings.ToUpper(strings.TrimSpace(symbol))
}

func (s *Service) marketSyncLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.marketSyncWake:
		}
		for {
			task, ok := s.nextMarketSyncTask()
			if !ok {
				break
			}
			err := s.runMarketSyncTask(ctx, task)
			s.finishMarketSyncTask(task, err)
			if ctx.Err() != nil {
				return
			}
			timer := time.NewTimer(marketSyncJobInterval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}
}

func (s *Service) requestMarketSync(symbol string, from, until time.Time, preferredInterval string) {
	if s == nil || s.store == nil {
		return
	}
	symbol = marketSyncKey(symbol)
	from, until = from.UTC(), until.UTC()
	if symbol == "" || !from.Before(until) {
		return
	}
	now := time.Now().UTC()
	s.marketSyncMu.Lock()
	if !s.marketSyncStarted {
		s.marketSyncMu.Unlock()
		return
	}
	state := s.marketSyncStates[symbol]
	if state.running || state.queued {
		if from.Before(state.from) {
			state.from = from
		}
		if until.After(state.until) {
			state.until = until
		}
		s.marketSyncStates[symbol] = state
		s.marketSyncMu.Unlock()
		return
	}
	if !state.lastSuccess.IsZero() && now.Sub(state.lastSuccess) < marketSyncFreshFor && !from.Before(state.from) {
		s.marketSyncMu.Unlock()
		return
	}
	if !state.retryAt.IsZero() && now.Before(state.retryAt) {
		s.marketSyncMu.Unlock()
		return
	}
	state.queued = true
	state.from = from
	state.until = until
	state.lastError = ""
	s.marketSyncStates[symbol] = state
	s.marketSyncQueue = append(s.marketSyncQueue, marketSyncTask{symbol: symbol, from: from, until: until, preferredInterval: preferredInterval})
	sort.SliceStable(s.marketSyncQueue, func(i, j int) bool {
		return marketSyncPriority(s.marketSyncQueue[i]) < marketSyncPriority(s.marketSyncQueue[j])
	})
	s.marketSyncMu.Unlock()
	select {
	case s.marketSyncWake <- struct{}{}:
	default:
	}
}

func marketSyncPriority(task marketSyncTask) int {
	priority := 1000
	if task.symbol == "ETHUSDT" {
		priority -= 500
	}
	for index, interval := range []string{"1m", "2m", "3m", "5m", "15m", "30m", "1h", "4h", "8h", "12h", "24h"} {
		if task.preferredInterval == interval {
			priority += index
			break
		}
	}
	return priority
}

func (s *Service) nextMarketSyncTask() (marketSyncTask, bool) {
	s.marketSyncMu.Lock()
	defer s.marketSyncMu.Unlock()
	if len(s.marketSyncQueue) == 0 {
		return marketSyncTask{}, false
	}
	task := s.marketSyncQueue[0]
	s.marketSyncQueue = s.marketSyncQueue[1:]
	state := s.marketSyncStates[task.symbol]
	task.from = state.from
	task.until = state.until
	state.queued = false
	state.running = true
	s.marketSyncStates[task.symbol] = state
	return task, true
}

func (s *Service) finishMarketSyncTask(task marketSyncTask, err error) {
	s.marketSyncMu.Lock()
	state := s.marketSyncStates[task.symbol]
	expanded := state.from.Before(task.from) || state.until.After(task.until)
	state.running = false
	if err == nil {
		state.lastSuccess = time.Now().UTC()
		state.lastError = ""
		state.retryAt = time.Time{}
	} else {
		state.lastError = err.Error()
		state.retryAt = time.Now().UTC().Add(marketSyncRetryAfter)
	}
	if expanded {
		state.queued = true
		s.marketSyncQueue = append(s.marketSyncQueue, marketSyncTask{symbol: task.symbol, from: state.from, until: state.until, preferredInterval: task.preferredInterval})
		sort.SliceStable(s.marketSyncQueue, func(i, j int) bool {
			return marketSyncPriority(s.marketSyncQueue[i]) < marketSyncPriority(s.marketSyncQueue[j])
		})
	}
	s.marketSyncStates[task.symbol] = state
	s.marketSyncMu.Unlock()
	if expanded {
		select {
		case s.marketSyncWake <- struct{}{}:
		default:
		}
	}
	if err != nil && s.log != nil {
		s.log.Warn("background Binance K-line sync failed", "symbol", task.symbol, "from", task.from, "until", task.until, "error", err)
	}
}

func (s *Service) marketSyncStatus(symbol string) (bool, time.Time, string) {
	s.marketSyncMu.Lock()
	defer s.marketSyncMu.Unlock()
	state := s.marketSyncStates[marketSyncKey(symbol)]
	return state.queued || state.running, state.lastSuccess, state.lastError
}

func (s *Service) runMarketSyncTask(parent context.Context, task marketSyncTask) error {
	return s.withMarketSyncSlot(parent, func() error {
		return s.runMarketSyncTaskExclusive(parent, task)
	})
}

func (s *Service) runMarketSyncTaskExclusive(parent context.Context, task marketSyncTask) error {
	ctx, cancel := context.WithTimeout(parent, marketSyncJobTimeout)
	defer cancel()
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		rows, err := fetchMarketHistoryKlines(ctx, task.symbol, "1m", task.from, task.until)
		if err == nil {
			return s.persistRemoteKlines(ctx, task.symbol, task.from, task.until, rows)
		}
		lastErr = err
		if attempt == 2 || ctx.Err() != nil {
			break
		}
		timer := time.NewTimer(marketSyncRetryDelay * time.Duration(attempt+1))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return lastErr
}

func (s *Service) syncMarketNow(ctx context.Context, symbol string, from, until time.Time) error {
	return s.withMarketSyncSlot(ctx, func() error {
		return s.syncMarketNowExclusive(ctx, symbol, from, until)
	})
}

func (s *Service) syncMarketNowExclusive(ctx context.Context, symbol string, from, until time.Time) error {
	rows, err := fetchMarketHistoryKlines(ctx, marketSyncKey(symbol), "1m", from.UTC(), until.UTC())
	if err != nil {
		return err
	}
	if err := s.persistRemoteKlines(ctx, marketSyncKey(symbol), from, until, rows); err != nil {
		return err
	}
	s.marketSyncMu.Lock()
	state := s.marketSyncStates[marketSyncKey(symbol)]
	state.from = from.UTC()
	state.until = until.UTC()
	state.lastSuccess = time.Now().UTC()
	state.lastError = ""
	state.retryAt = time.Time{}
	s.marketSyncStates[marketSyncKey(symbol)] = state
	s.marketSyncMu.Unlock()
	return nil
}

func (s *Service) withMarketSyncSlot(ctx context.Context, fn func() error) error {
	if s.marketSyncGate == nil {
		return fn()
	}
	select {
	case s.marketSyncGate <- struct{}{}:
		defer func() { <-s.marketSyncGate }()
		return fn()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) persistRemoteKlines(ctx context.Context, symbol string, from, until time.Time, rows []domain.MarketCandle) error {
	closedBefore := time.Now().UTC().Truncate(time.Minute)
	repaired := make([]domain.Candle, 0, len(rows))
	for _, row := range rows {
		at := row.Time.UTC().Truncate(time.Minute)
		if at.Before(from.UTC()) || !at.Before(until.UTC()) || !at.Before(closedBefore) {
			continue
		}
		repaired = append(repaired, domain.Candle{Exchange: "binance", Symbol: symbol, Time: at, Open: row.Open, High: row.High, Low: row.Low, Close: row.Close, VolumeUSD: row.VolumeUSD})
	}
	if len(repaired) == 0 {
		return fmt.Errorf("Binance returned no closed 1m candles for %s", symbol)
	}
	if err := s.store.UpsertCandles(ctx, repaired); err != nil {
		return err
	}
	s.invalidateMarketHistorySnapshots(symbol)
	return nil
}

func (s *Service) decorateMarketView(view *domain.MarketView, symbol string, source string) {
	if view == nil {
		return
	}
	syncing, syncedAt, syncError := s.marketSyncStatus(symbol)
	view.Source = source
	view.RemoteSyncing = syncing
	view.RemoteSyncError = syncError
	if len(view.Candles) > 0 {
		through := view.Candles[len(view.Candles)-1].Time.UTC()
		view.LocalThrough = &through
	}
	if !syncedAt.IsZero() {
		view.RemoteSyncedAt = &syncedAt
	}
}

func (s *Service) decorateKlineView(view *domain.KlineView, symbol string) {
	if view == nil {
		return
	}
	syncing, syncedAt, syncError := s.marketSyncStatus(symbol)
	view.RemoteSyncing = syncing
	view.RemoteSyncError = syncError
	if len(view.Candles) > 0 {
		through := view.Candles[len(view.Candles)-1].Time.UTC()
		view.LocalThrough = &through
	}
	if !syncedAt.IsZero() {
		view.RemoteSyncedAt = &syncedAt
	}
}
