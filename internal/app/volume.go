package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/exchange"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/volumeprofile"
)

var fetchAggregateTrades = exchange.BinanceAggregateTrades

const volumeProfileBackfillRetryDelay = 30 * time.Second
const volumeBackfillTaskTimeout = 30 * time.Second

var errVolumeBackfillQueueFull = errors.New("volume profile backfill queue is full")

func (s *Service) startVolumeProfiles(ctx context.Context) {
	bootstrapSymbols := s.volumeBootstrapSymbols()
	s.volumeMu.Lock()
	s.volumeContext = ctx
	if s.volumeRequested == nil {
		s.volumeRequested = make(map[string]struct{})
	}
	if s.volumeStreamSymbols == nil {
		s.volumeStreamSymbols = make(map[string]struct{})
	}
	for _, symbol := range bootstrapSymbols {
		s.volumeRequested[symbol] = struct{}{}
		s.volumeStreamSymbols[symbol] = struct{}{}
	}
	s.volumeLiveInput = make(chan domain.AggregateTrade, 100000)
	s.marketLiveInput = make(chan domain.AggregateTrade, 100000)
	s.volumeMu.Unlock()
	go s.volumeProfileLoop(ctx)
	s.startVolumeBackfillWorkers(ctx)
	go s.marketCandleLoop(ctx)
}

func (s *Service) startVolumeBackfillWorkers(ctx context.Context) {
	workers := s.volumeBackfillWorkerCount()
	for i := 0; i < workers; i++ {
		go s.volumeBackfillLoop(ctx)
	}
	if s.log != nil {
		s.log.Info("aggregate trade backfill workers started", "workers", workers)
	}
}

func (s *Service) volumeBackfillWorkerCount() int {
	workers := s.cfg.VolumeBackfillWorkers
	if workers <= 0 {
		workers = 2
	}
	if workers > 4 {
		workers = 4
	}
	return workers
}

func (s *Service) registerVolumeSymbols(symbols []string) {
	s.volumeMu.Lock()
	defer s.volumeMu.Unlock()
	if s.volumeRequested == nil {
		s.volumeRequested = make(map[string]struct{})
	}
	if s.volumeStreamSymbols == nil {
		s.volumeStreamSymbols = make(map[string]struct{})
	}
	for _, raw := range symbols {
		symbol := normalizeVolumeSymbol(raw)
		if symbol == "" {
			continue
		}
		s.volumeRequested[symbol] = struct{}{}
		s.volumeStreamSymbols[symbol] = struct{}{}
	}
}

// registerVolumeStreamSymbols subscribes alert candidates to live aggregate
// trades without scheduling expensive historical profile backfills.
func (s *Service) registerVolumeStreamSymbols(symbols []string) {
	s.volumeMu.Lock()
	defer s.volumeMu.Unlock()
	if s.volumeStreamSymbols == nil {
		s.volumeStreamSymbols = make(map[string]struct{})
	}
	for _, raw := range symbols {
		if symbol := normalizeVolumeSymbol(raw); symbol != "" {
			s.volumeStreamSymbols[symbol] = struct{}{}
		}
	}
}

func (s *Service) volumeProfileLoop(ctx context.Context) {
	// The resident alert watch list receives live aggregate trades, but only the
	// deliberately small bootstrap set is replayed at startup. Replaying exact
	// aggregate trades for all 50 alert candidates creates a large SQLite/API
	// burst and can starve the dashboard. Other candidates remain available for
	// live collection and on-demand backfill.
	bootstrapSymbols := s.volumeBootstrapSymbols()
	s.volumeMu.Lock()
	input := s.volumeLiveInput
	if input == nil {
		input = make(chan domain.AggregateTrade, 100000)
		s.volumeLiveInput = input
	}
	s.volumeMu.Unlock()
	close(s.volumeLiveReady)
	// Bootstrap shares the resumable queue so page requests can run between
	// slices instead of waiting for two entire high-volume sessions.
	for _, symbol := range bootstrapSymbols {
		if err := s.enqueueVolumeProfileBackfill(ctx, symbol, false, false); err != nil && s.log != nil {
			s.log.Warn("aggregate trade bootstrap enqueue failed", "symbol", symbol, "error", err)
		}
	}

	flush := time.NewTicker(500 * time.Millisecond)
	defer flush.Stop()
	snapshot := time.NewTicker(time.Minute)
	defer snapshot.Stop()
	batch := make([]domain.AggregateTrade, 0, 2000)
	for {
		select {
		case <-ctx.Done():
			if len(batch) > 0 {
				_ = s.applyLiveAggregateTrades(context.Background(), batch)
			}
			return
		case <-s.candidateUpdates:
			// Candidate refreshes register and synchronize their symbols in the
			// background-candidate path. The live collector updates its
			// subscription independently, so this loop only needs to continue.
			continue
		case trade := <-input:
			batch = append(batch, trade)
			if len(batch) >= 2000 {
				if err := s.applyLiveAggregateTrades(ctx, batch); err != nil {
					s.log.Warn("aggregate trade batch failed", "error", err)
				}
				batch = batch[:0]
			}
		case <-flush.C:
			if len(batch) > 0 {
				if err := s.applyLiveAggregateTrades(ctx, batch); err != nil {
					s.log.Warn("aggregate trade batch failed", "error", err)
				}
				batch = batch[:0]
			}
		case now := <-snapshot.C:
			if now.UTC().Minute()%5 == 0 {
				s.saveCurrentVolumeSnapshots(ctx, time.Now().UTC())
			}
		}
	}
}

func (s *Service) volumeBackfillLoop(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-s.volumeLiveReady:
	}
	for {
		var symbol string
		select {
		case <-ctx.Done():
			return
		case symbol = <-s.volumePriorityQueue:
		default:
			select {
			case <-ctx.Done():
				return
			case symbol = <-s.volumePriorityQueue:
			case symbol = <-s.volumeBackfillQueue:
			}
		}
		s.volumeMu.Lock()
		_, pending := s.volumePending[symbol]
		_, active := s.volumeActive[symbol]
		delete(s.volumePriorityPending, symbol)
		claimed := pending && !active
		if claimed {
			if s.volumeActive == nil {
				s.volumeActive = make(map[string]struct{})
			}
			s.volumeActive[symbol] = struct{}{}
		}
		s.volumeMu.Unlock()
		if !claimed {
			continue
		}
		if s.log != nil {
			s.log.Info("aggregate trade replay started", "symbol", symbol, "timeout", volumeBackfillTaskTimeout)
		}
		{
			workCtx, cancel := context.WithTimeout(ctx, volumeBackfillTaskTimeout)
			err := s.syncVolumeSessionExclusive(workCtx, symbol, time.Now().UTC())
			timedOut := errors.Is(workCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil
			cancel()
			s.volumeMu.Lock()
			delete(s.volumePending, symbol)
			delete(s.volumeActive, symbol)
			if timedOut {
				delete(s.volumeBackfillRetryAt, symbol)
				s.volumeMu.Unlock()
				if s.log != nil {
					s.log.Warn("aggregate trade replay timed out; resuming from saved cursor", "symbol", symbol, "timeout", volumeBackfillTaskTimeout)
				}
				if err := s.enqueueVolumeProfileBackfill(ctx, symbol, false, true); err != nil && ctx.Err() == nil && s.log != nil {
					s.log.Warn("aggregate trade replay could not be rescheduled", "symbol", symbol, "error", err)
				}
				continue
			}
			if err != nil {
				if s.volumeBackfillRetryAt == nil {
					s.volumeBackfillRetryAt = make(map[string]time.Time)
				}
				s.volumeBackfillError[symbol] = err.Error()
				s.volumeBackfillRetryAt[symbol] = time.Now().Add(volumeProfileBackfillRetryDelay)
				s.volumeMu.Unlock()
				if ctx.Err() == nil && s.log != nil {
					s.log.Warn("on-demand aggregate trade backfill failed", "symbol", symbol, "error", err)
				}
				continue
			}
			delete(s.volumeBackfillError, symbol)
			// A profile can remain stale after a successful sync when the exchange
			// has no newer trades. Cool down repeated session refreshes in that case.
			if s.volumeBackfillRetryAt == nil {
				s.volumeBackfillRetryAt = make(map[string]time.Time)
			}
			s.volumeBackfillRetryAt[symbol] = time.Now().Add(volumeProfileBackfillRetryDelay)
			_, alreadyStreaming := s.volumeStreamSymbols[symbol]
			if !alreadyStreaming {
				s.volumeStreamSymbols[symbol] = struct{}{}
			}
			s.volumeMu.Unlock()
			if !alreadyStreaming {
				s.updateMarketCollectorAggregateSymbols()
			}
			s.requeueIncompleteVolumeProfile(ctx, symbol)
		}
	}
}

func (s *Service) requeueIncompleteVolumeProfile(ctx context.Context, symbol string) {
	// A live trade batch can detect a gap while this symbol is still marked
	// pending. EnsureVolumeProfile then deduplicates the request, but that can
	// leave the cursor incomplete after this worker clears pending. Recheck
	// after clearing it and queue another pass.
	session, err := volumeprofile.SessionAt(time.Now().UTC())
	if err != nil {
		return
	}
	_, _, complete, err := s.store.VolumeProfileCursor(ctx, symbol, session.Start)
	if err != nil {
		if ctx.Err() == nil && s.log != nil {
			s.log.Warn("aggregate trade backfill completion check failed", "symbol", symbol, "error", err)
		}
		return
	}
	if complete {
		return
	}
	if err := s.enqueueVolumeProfileBackfill(ctx, symbol, false, true); err != nil && ctx.Err() == nil && s.log != nil {
		s.log.Warn("incomplete aggregate trade backfill could not be requeued", "symbol", symbol, "error", err)
	}
}

func (s *Service) syncVolumeSymbols(ctx context.Context, symbols []string) {
	s.registerVolumeSymbols(symbols)
	for _, symbol := range symbols {
		if ctx.Err() != nil {
			return
		}
		workCtx, cancel := context.WithTimeout(ctx, volumeBackfillTaskTimeout)
		err := s.syncVolumeSession(workCtx, symbol, time.Now().UTC())
		cancel()
		if err != nil && ctx.Err() == nil {
			s.log.Warn("aggregate trade initial backfill failed", "symbol", symbol, "error", err)
		}
	}
}

func (s *Service) volumeStreamSymbolList() []string {
	s.volumeMu.Lock()
	defer s.volumeMu.Unlock()
	return s.volumeStreamSymbolListLocked()
}

func (s *Service) volumeStreamSymbolListLocked() []string {
	symbols := make([]string, 0, len(s.volumeStreamSymbols))
	for symbol := range s.volumeStreamSymbols {
		symbols = append(symbols, symbol)
	}
	sort.Strings(symbols)
	return symbols
}

func normalizeVolumeSymbol(symbol string) string {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	if symbol == "" || symbol == "USDT" || !strings.HasSuffix(symbol, "USDT") {
		return ""
	}
	for _, r := range symbol {
		if (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return ""
		}
	}
	return symbol
}

// EnsureVolumeProfile registers a valid on-demand symbol and queues the current
// volume session for aggregate-trade synchronization. The queue is idempotent;
// force only retries an incomplete or failed session.
func (s *Service) EnsureVolumeProfile(ctx context.Context, symbol string, force bool) error {
	return s.enqueueVolumeProfileBackfillPriority(ctx, symbol, force, force, true)
}

// enqueueVolumeProfileBackfill requests an asynchronous current-session sync.
// refreshCompleted lets the radar repair stale or unusable profiles even when
// the cursor was previously marked complete. bypassRetry is reserved for an
// explicit force request and the worker's immediate incomplete-session retry.
func (s *Service) enqueueVolumeProfileBackfill(ctx context.Context, symbol string, refreshCompleted, bypassRetry bool) error {
	return s.enqueueVolumeProfileBackfillPriority(ctx, symbol, refreshCompleted, bypassRetry, false)
}

func (s *Service) enqueueVolumeProfileBackfillPriority(ctx context.Context, symbol string, refreshCompleted, bypassRetry, priority bool) error {
	symbol = normalizeVolumeSymbol(symbol)
	if symbol == "" {
		return fmt.Errorf("symbol must be a Binance USDT perpetual")
	}
	if s.store == nil {
		return errors.New("volume profile store unavailable")
	}
	session, err := volumeprofile.SessionAt(time.Now().UTC())
	if err != nil {
		return err
	}
	_, through, complete, err := s.store.VolumeProfileCursor(ctx, symbol, session.Start)
	if err != nil {
		return err
	}
	// A previously complete cursor may have stopped receiving trades while the
	// symbol was not subscribed. A chart request must repair stale data too.
	s.volumeMu.Lock()
	if priority && complete && s.volumeContext != nil && (through.IsZero() || time.Since(through) > valueAreaProfileMaxAge) {
		refreshCompleted = true
	}
	if s.volumeRequested == nil {
		s.volumeRequested = make(map[string]struct{})
	}
	if s.volumePending == nil {
		s.volumePending = make(map[string]struct{})
	}
	if s.volumeBackfillError == nil {
		s.volumeBackfillError = make(map[string]string)
	}
	if s.volumeBackfillRetryAt == nil {
		s.volumeBackfillRetryAt = make(map[string]time.Time)
	}
	s.volumeRequested[symbol] = struct{}{}
	if complete && !refreshCompleted {
		s.volumeMu.Unlock()
		return nil
	}
	if s.volumeContext == nil || s.volumeBackfillQueue == nil {
		s.volumeMu.Unlock()
		return errors.New("volume profile backfill worker unavailable")
	}
	if _, pending := s.volumePending[symbol]; pending {
		_, active := s.volumeActive[symbol]
		if priority && !active && s.volumePriorityQueue != nil {
			if s.volumePriorityPending == nil {
				s.volumePriorityPending = make(map[string]struct{})
			}
			if _, promoted := s.volumePriorityPending[symbol]; !promoted {
				select {
				case s.volumePriorityQueue <- symbol:
					s.volumePriorityPending[symbol] = struct{}{}
				default:
				}
			}
		}
		s.volumeMu.Unlock()
		return nil
	}
	if !bypassRetry && time.Now().Before(s.volumeBackfillRetryAt[symbol]) {
		s.volumeMu.Unlock()
		return nil
	}
	s.volumePending[symbol] = struct{}{}
	delete(s.volumeBackfillError, symbol)
	if s.volumeBackfillRetryAt == nil {
		s.volumeBackfillRetryAt = make(map[string]time.Time)
	}
	s.volumeBackfillRetryAt[symbol] = time.Now().Add(volumeProfileBackfillRetryDelay)
	queue := s.volumeBackfillQueue
	if priority && s.volumePriorityQueue != nil {
		queue = s.volumePriorityQueue
		if s.volumePriorityPending == nil {
			s.volumePriorityPending = make(map[string]struct{})
		}
		s.volumePriorityPending[symbol] = struct{}{}
	}
	s.volumeMu.Unlock()
	select {
	case queue <- symbol:
		return nil
	case <-ctx.Done():
		s.volumeMu.Lock()
		delete(s.volumePending, symbol)
		delete(s.volumePriorityPending, symbol)
		delete(s.volumeBackfillRetryAt, symbol)
		s.volumeMu.Unlock()
		return ctx.Err()
	default:
		s.volumeMu.Lock()
		delete(s.volumePending, symbol)
		delete(s.volumePriorityPending, symbol)
		delete(s.volumeBackfillRetryAt, symbol)
		s.volumeMu.Unlock()
		return errVolumeBackfillQueueFull
	}
}

func (s *Service) syncVolumeSessionExclusive(ctx context.Context, symbol string, target time.Time) error {
	// The session may contain many REST pages. Keep the shared gate only around
	// each page write in syncVolumeSession so a waiting writer can make progress
	// while this symbol waits on Binance.
	return s.syncVolumeSession(ctx, symbol, target)
}

func (s *Service) volumeBootstrapSymbols() []string {
	configured := s.cfg.BootstrapSymbols
	if len(configured) == 0 {
		configured = []string{"BTCUSDT", "ETHUSDT"}
	}
	seen := make(map[string]struct{}, len(configured))
	result := make([]string, 0, len(configured))
	for _, raw := range configured {
		symbol := strings.ToUpper(strings.TrimSpace(raw))
		if symbol == "" || symbol == "USDT" || !strings.HasSuffix(symbol, "USDT") {
			continue
		}
		if _, ok := seen[symbol]; ok {
			continue
		}
		seen[symbol] = struct{}{}
		result = append(result, symbol)
	}
	if len(result) == 0 {
		return []string{"BTCUSDT", "ETHUSDT"}
	}
	return result
}

// volumeProfileSymbols extends historical VAL/VAH snapshots to the live alert
// watch list while retaining the explicitly configured bootstrap symbols.
func (s *Service) volumeProfileSymbols() []string {
	seen := make(map[string]struct{})
	result := make([]string, 0)
	rankedBackfillSymbols := s.backgroundCandidateSnapshot().VolumeBackfillSymbols
	for _, group := range [][]string{s.volumeBootstrapSymbols(), s.backgroundSymbols(), rankedBackfillSymbols} {
		for _, raw := range group {
			symbol := normalizeVolumeSymbol(raw)
			if symbol == "" {
				continue
			}
			if _, ok := seen[symbol]; ok {
				continue
			}
			seen[symbol] = struct{}{}
			result = append(result, symbol)
		}
	}
	sort.Strings(result)
	return result
}

func (s *Service) applyLiveAggregateTrades(ctx context.Context, trades []domain.AggregateTrade) error {
	type groupKey struct {
		symbol string
		start  int64
	}
	groups := make(map[groupKey][]domain.AggregateTrade)
	sessions := make(map[groupKey]volumeprofile.Session)
	for _, trade := range trades {
		if !s.volumeRequestedSymbol(trade.Symbol) {
			continue
		}
		session, err := volumeprofile.SessionAt(trade.Time)
		if err != nil {
			return err
		}
		key := groupKey{trade.Symbol, session.Start.UnixMilli()}
		groups[key] = append(groups[key], trade)
		sessions[key] = session
	}
	keys := make([]groupKey, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].start == keys[j].start {
			return keys[i].symbol < keys[j].symbol
		}
		return keys[i].start < keys[j].start
	})
	for _, key := range keys {
		rows := groups[key]
		sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
		lastID, _, complete, err := s.store.VolumeProfileCursor(ctx, key.symbol, sessions[key].Start)
		if err != nil {
			return err
		}
		if !complete || lastID == 0 || aggregateTradeGap(rows, lastID) {
			if err = s.store.SetVolumeProfileComplete(ctx, key.symbol, sessions[key].Start, false); err != nil {
				return err
			}
			if err = s.enqueueVolumeProfileBackfill(ctx, key.symbol, false, false); err != nil && !errors.Is(err, errVolumeBackfillQueueFull) {
				return err
			}
			// Historical replay runs on the backfill worker. Keeping this live
			// consumer moving lets it continue draining other symbols while the
			// requested session catches up through the exchange REST endpoint.
			continue
		}
		if err = s.store.ApplyAggregateTrades(ctx, key.symbol, sessions[key].Start, rows, true); err != nil {
			return err
		}
	}
	return nil
}

// marketCandleLoop persists live candles independently from the volume-profile
// worker. A long aggregate-trade replay must not stop chart candles or block
// the shared market-collector connection that also carries liquidation events.
func (s *Service) marketCandleLoop(ctx context.Context) {
	s.volumeMu.Lock()
	input := s.marketLiveInput
	if input == nil {
		input = make(chan domain.AggregateTrade, 100000)
		s.marketLiveInput = input
	}
	s.volumeMu.Unlock()

	flush := time.NewTicker(500 * time.Millisecond)
	defer flush.Stop()
	batch := make([]domain.AggregateTrade, 0, 2000)
	flushBatch := func(writeCtx context.Context) {
		if len(batch) == 0 {
			return
		}
		if err := s.persistLiveCandles(writeCtx, batch); err != nil && writeCtx.Err() == nil && s.log != nil {
			s.log.Warn("live market candle batch failed", "error", err)
		}
		batch = batch[:0]
	}
	for {
		select {
		case <-ctx.Done():
			flushBatch(context.Background())
			return
		case trade := <-input:
			batch = append(batch, trade)
			if len(batch) >= 2000 {
				flushBatch(ctx)
			}
		case <-flush.C:
			flushBatch(ctx)
		}
	}
}

func (s *Service) syncVolumeSession(ctx context.Context, symbol string, target time.Time) error {
	session, err := volumeprofile.SessionAt(target)
	if err != nil {
		return err
	}
	if target.After(session.End) {
		target = session.End
	}
	lastID, _, _, err := s.store.VolumeProfileCursor(ctx, symbol, session.Start)
	if err != nil {
		return err
	}
	if err = s.withBackfillWrite(ctx, true, func() error {
		return s.store.SetVolumeProfileComplete(ctx, symbol, session.Start, false)
	}); err != nil {
		return err
	}
	nextID := lastID + 1
	if lastID == 0 {
		first, findErr := s.firstAggregateTrade(ctx, symbol, session.Start, target)
		if findErr != nil {
			return findErr
		}
		if first == 0 {
			return s.withBackfillWrite(ctx, true, func() error {
				return s.store.ApplyAggregateTrades(ctx, symbol, session.Start, nil, true)
			})
		}
		nextID = first
	}
	for ctx.Err() == nil {
		rows, requestErr := s.aggregateTradesRequest(ctx, symbol, nextID, time.Time{}, time.Time{})
		if requestErr != nil {
			return requestErr
		}
		if len(rows) == 0 {
			break
		}
		page := rows[:0]
		reachedTarget := false
		expectedID := nextID
		for _, trade := range rows {
			if trade.ID < expectedID {
				continue
			}
			if trade.ID > expectedID {
				return fmt.Errorf("aggregate trade gap: expected %d got %d", expectedID, trade.ID)
			}
			if trade.Time.Before(session.Start) {
				expectedID++
				continue
			}
			if trade.Time.After(target) {
				reachedTarget = true
				break
			}
			page = append(page, trade)
			expectedID++
		}
		if len(page) > 0 {
			if err = s.withBackfillWrite(ctx, true, func() error {
				return s.store.ApplyAggregateTrades(ctx, symbol, session.Start, page, false)
			}); err != nil {
				return err
			}
			nextID = page[len(page)-1].ID + 1
		}
		if reachedTarget || len(rows) < 1000 {
			break
		}
		if len(page) == 0 {
			return errors.New("aggregate trade backfill made no progress")
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err = s.withBackfillWrite(ctx, true, func() error {
		return s.store.SetVolumeProfileComplete(ctx, symbol, session.Start, true)
	}); err == nil {
		s.log.Info("aggregate trade session synchronized", "symbol", symbol, "session_start", session.Start, "through", target)
	}
	return err
}

func (s *Service) firstAggregateTrade(ctx context.Context, symbol string, start, target time.Time) (int64, error) {
	for cursor := start; cursor.Before(target); cursor = cursor.Add(time.Minute) {
		end := cursor.Add(time.Minute)
		if end.After(target) {
			end = target
		}
		id, found, err := s.firstTradeInWindow(ctx, symbol, cursor, end)
		if err != nil {
			return 0, err
		}
		if found {
			return id, nil
		}
	}
	return 0, nil
}

func (s *Service) firstTradeInWindow(ctx context.Context, symbol string, start, end time.Time) (int64, bool, error) {
	rows, err := s.aggregateTradesRequest(ctx, symbol, 0, start, end)
	if err != nil {
		return 0, false, err
	}
	if len(rows) == 0 {
		return 0, false, nil
	}
	if len(rows) < 1000 || end.Sub(start) <= time.Millisecond {
		return rows[0].ID, true, nil
	}
	middle := start.Add(end.Sub(start) / 2)
	if id, found, err := s.firstTradeInWindow(ctx, symbol, start, middle); err != nil || found {
		return id, found, err
	}
	return s.firstTradeInWindow(ctx, symbol, middle.Add(time.Millisecond), end)
}

func (s *Service) aggregateTradesRequest(ctx context.Context, symbol string, fromID int64, start, end time.Time) ([]domain.AggregateTrade, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		rows, err := fetchAggregateTrades(ctx, symbol, fromID, start, end)
		if err == nil {
			if waitContext(ctx, 650*time.Millisecond) != nil {
				return nil, ctx.Err()
			}
			return rows, nil
		}
		lastErr = err
		if waitContext(ctx, time.Duration(1<<attempt)*time.Second) != nil {
			return nil, ctx.Err()
		}
	}
	return nil, lastErr
}

func aggregateTradeGap(rows []domain.AggregateTrade, lastID int64) bool {
	expected := lastID + 1
	for _, trade := range rows {
		if trade.ID < expected {
			continue
		}
		if trade.ID > expected {
			return true
		}
		expected++
	}
	return false
}

func waitContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (s *Service) VolumeProfile(ctx context.Context, symbol string) (domain.VolumeProfile, error) {
	now := time.Now().UTC()
	if !s.backgroundSymbol(symbol) && !s.volumeRequestedSymbol(symbol) {
		return domain.VolumeProfile{Symbol: symbol, Source: domain.DataSourceBinanceUSDM, State: "unavailable", UpdatedAt: now}, nil
	}
	session, err := volumeprofile.SessionAt(now)
	if err != nil {
		return domain.VolumeProfile{}, err
	}
	levels, _, through, complete, err := s.store.VolumeProfileLevels(ctx, symbol, session.Start)
	if err != nil {
		return domain.VolumeProfile{}, err
	}
	profile := volumeprofile.Build(symbol, session, levels, through, now, complete)
	s.volumeMu.Lock()
	backfillError := s.volumeBackfillError[symbol]
	s.volumeMu.Unlock()
	if backfillError != "" && profile.State != "ok" {
		profile.State = "unavailable"
		profile.BackfillError = backfillError
	}
	return profile, nil
}

const currentVolumeProfileCacheDuration = 2 * time.Second

// currentVolumeProfile shares the current-session profile between the value-area
// strategy and the liquidation radar. A short cache avoids rebuilding hundreds
// of price levels for every liquidation tick while keeping live VAL/VAH fresh.
func (s *Service) currentVolumeProfile(ctx context.Context, symbol string) (domain.VolumeProfile, error) {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	now := time.Now().UTC()
	s.volumeProfileCacheMu.Lock()
	if cached, ok := s.volumeProfileCache[symbol]; ok && now.Before(cached.next) {
		profile := cached.profile
		s.volumeProfileCacheMu.Unlock()
		return profile, nil
	}
	s.volumeProfileCacheMu.Unlock()

	profile, err := s.VolumeProfile(ctx, symbol)
	// Liquidation events can cover the whole exchange. Do not register every
	// such symbol for expensive replay unless it is monitored or requested.
	if err == nil && !s.backgroundSymbol(symbol) && !s.volumeRequestedSymbol(symbol) {
		return profile, nil
	}
	if err == nil && profile.State == "ok" && !volumeProfileFreshAt(profile, now) {
		profile.State = "stale"
	}
	if err == nil && profile.State != "ok" {
		err = s.enqueueVolumeProfileBackfill(ctx, symbol, true, false)
		if err != nil && ctx.Err() == nil && s.log != nil {
			s.log.Warn("value-area refresh request failed", "symbol", symbol, "error", err)
		}
	}
	if err != nil && profile.Symbol == "" {
		profile = domain.VolumeProfile{Symbol: symbol, Source: domain.DataSourceBinanceUSDM, State: "unavailable", UpdatedAt: now}
	}

	s.volumeProfileCacheMu.Lock()
	if s.volumeProfileCache == nil {
		s.volumeProfileCache = make(map[string]valueAreaProfileCache)
	}
	s.volumeProfileCache[symbol] = valueAreaProfileCache{profile: profile, next: now.Add(currentVolumeProfileCacheDuration)}
	s.volumeProfileCacheMu.Unlock()
	return profile, err
}

func (s *Service) volumeRequestedSymbol(symbol string) bool {
	s.volumeMu.Lock()
	defer s.volumeMu.Unlock()
	_, ok := s.volumeRequested[strings.ToUpper(strings.TrimSpace(symbol))]
	return ok
}

func (s *Service) saveCurrentVolumeSnapshots(ctx context.Context, at time.Time) {
	for _, symbol := range s.volumeProfileSymbols() {
		profile, err := s.VolumeProfile(ctx, symbol)
		if err != nil || profile.State != "ok" || !volumeProfileFreshAt(profile, at) || profile.VAL == nil || profile.VAH == nil {
			continue
		}
		// Timestamp the profile at the last source trade it contains. Using the
		// polling time would let later trades leak into historical as-of lookups.
		_ = s.store.SaveVolumeProfileSnapshot(ctx, domain.VolumeProfileSnapshot{Symbol: symbol, Time: profile.DataThrough, SessionStart: profile.SessionStart, VAL: *profile.VAL, VAH: *profile.VAH, TotalVolumeUSD: profile.TotalVolumeUSD, Complete: true})
	}
}

func volumeProfileFreshAt(profile domain.VolumeProfile, at time.Time) bool {
	if profile.DataThrough.IsZero() || at.IsZero() {
		return false
	}
	age := at.UTC().Sub(profile.DataThrough.UTC())
	return age >= -5*time.Second && age <= valueAreaProfileMaxAge
}
