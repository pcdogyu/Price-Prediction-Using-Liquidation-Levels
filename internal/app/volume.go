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
	s.volumeMu.Unlock()
	go s.volumeProfileLoop(ctx)
	go s.volumeBackfillLoop(ctx)
}

func (s *Service) volumeProfileLoop(ctx context.Context) {
	// Volume profiles use the deliberately small bootstrap set. The alert and
	// candle candidate set may contain up to 50 symbols, but replaying exact
	// aggregate trades for that set at startup creates a large SQLite/API burst.
	// Other candidates remain available to alerts and on-demand market views.
	bootstrapSymbols := s.volumeBootstrapSymbols()
	s.syncVolumeSymbols(ctx, bootstrapSymbols)
	input := make(chan domain.AggregateTrade, 100000)
	streamCtx, cancelStream := context.WithCancel(ctx)
	s.startAggregateTradeStream(streamCtx, s.volumeStreamSymbolList(), input)
	close(s.volumeLiveReady)

	flush := time.NewTicker(500 * time.Millisecond)
	defer flush.Stop()
	snapshot := time.NewTicker(time.Minute)
	defer snapshot.Stop()
	batch := make([]domain.AggregateTrade, 0, 2000)
	for {
		select {
		case <-ctx.Done():
			cancelStream()
			if len(batch) > 0 {
				_ = s.applyLiveAggregateTrades(context.Background(), batch)
			}
			return
		case symbols := <-s.volumeStreamUpdates:
			cancelStream()
			streamCtx, cancelStream = context.WithCancel(ctx)
			s.startAggregateTradeStream(streamCtx, symbols, input)
		case <-s.candidateUpdates:
			// Candidate refreshes control alert/candle coverage, not the bounded
			// volume-profile feed. Do not restart the stream and replay a newly
			// expanded 50-symbol set into SQLite.
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
				s.saveCurrentVolumeSnapshots(ctx, now.UTC().Truncate(5*time.Minute))
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
		select {
		case <-ctx.Done():
			return
		case symbol := <-s.volumeBackfillQueue:
			err := s.syncVolumeSessionExclusive(ctx, symbol, time.Now().UTC())
			s.volumeMu.Lock()
			delete(s.volumePending, symbol)
			if err != nil {
				s.volumeBackfillError[symbol] = err.Error()
				s.volumeMu.Unlock()
				if ctx.Err() == nil && s.log != nil {
					s.log.Warn("on-demand aggregate trade backfill failed", "symbol", symbol, "error", err)
				}
				continue
			}
			delete(s.volumeBackfillError, symbol)
			_, alreadyStreaming := s.volumeStreamSymbols[symbol]
			if !alreadyStreaming {
				s.volumeStreamSymbols[symbol] = struct{}{}
			}
			streamSymbols := s.volumeStreamSymbolListLocked()
			s.volumeMu.Unlock()
			if !alreadyStreaming {
				select {
				case s.volumeStreamUpdates <- streamSymbols:
				case <-ctx.Done():
					return
				}
			}
		}
	}
}

func (s *Service) syncVolumeSymbols(ctx context.Context, symbols []string) {
	release, ok := s.beginBackfill(ctx)
	if !ok {
		return
	}
	defer release()
	for _, symbol := range symbols {
		if err := s.syncVolumeSession(ctx, symbol, time.Now().UTC()); err != nil && ctx.Err() == nil {
			s.log.Warn("aggregate trade initial backfill failed", "symbol", symbol, "error", err)
		}
	}
}

func (s *Service) startAggregateTradeStream(ctx context.Context, symbols []string, input chan<- domain.AggregateTrade) {
	exchange.StartAggregateTradeStreams(ctx, symbols, func(ctx context.Context, trade domain.AggregateTrade) error {
		s.RecordAggregateTrade(trade)
		select {
		case input <- trade:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}, s.health, s.log)
}

func (s *Service) startCandidateAggregateStream(ctx context.Context, symbols []string, input chan<- domain.AggregateTrade) context.CancelFunc {
	streamCtx, cancel := context.WithCancel(ctx)
	s.startAggregateTradeStream(streamCtx, symbols, input)
	return cancel
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
	_, _, complete, err := s.store.VolumeProfileCursor(ctx, symbol, session.Start)
	if err != nil {
		return err
	}
	s.volumeMu.Lock()
	if s.volumeRequested == nil {
		s.volumeRequested = make(map[string]struct{})
	}
	if s.volumePending == nil {
		s.volumePending = make(map[string]struct{})
	}
	if s.volumeBackfillError == nil {
		s.volumeBackfillError = make(map[string]string)
	}
	s.volumeRequested[symbol] = struct{}{}
	if complete || s.volumeContext == nil || s.volumeBackfillQueue == nil {
		s.volumeMu.Unlock()
		return nil
	}
	if _, pending := s.volumePending[symbol]; pending {
		s.volumeMu.Unlock()
		return nil
	}
	if !force && s.volumeBackfillError[symbol] != "" {
		s.volumeMu.Unlock()
		return nil
	}
	if !force {
		if _, streaming := s.volumeStreamSymbols[symbol]; streaming {
			s.volumeMu.Unlock()
			return nil
		}
	}
	s.volumePending[symbol] = struct{}{}
	delete(s.volumeBackfillError, symbol)
	queue := s.volumeBackfillQueue
	s.volumeMu.Unlock()
	select {
	case queue <- symbol:
		return nil
	case <-ctx.Done():
		s.volumeMu.Lock()
		delete(s.volumePending, symbol)
		s.volumeMu.Unlock()
		return ctx.Err()
	default:
		s.volumeMu.Lock()
		delete(s.volumePending, symbol)
		s.volumeMu.Unlock()
		return errors.New("volume profile backfill queue is full")
	}
}

func (s *Service) syncVolumeSessionExclusive(ctx context.Context, symbol string, target time.Time) error {
	release, ok := s.beginBackfill(ctx)
	if !ok {
		return ctx.Err()
	}
	defer release()
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

func (s *Service) applyLiveAggregateTrades(ctx context.Context, trades []domain.AggregateTrade) error {
	type groupKey struct {
		symbol string
		start  int64
	}
	groups := make(map[groupKey][]domain.AggregateTrade)
	sessions := make(map[groupKey]volumeprofile.Session)
	for _, trade := range trades {
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
			_ = s.store.SetVolumeProfileComplete(ctx, key.symbol, sessions[key].Start, false)
			if err = s.syncVolumeSessionExclusive(ctx, key.symbol, rows[len(rows)-1].Time); err != nil {
				return err
			}
		}
		if err = s.store.ApplyAggregateTrades(ctx, key.symbol, sessions[key].Start, rows, true); err != nil {
			return err
		}
	}
	return nil
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
	_ = s.store.SetVolumeProfileComplete(ctx, symbol, session.Start, false)
	nextID := lastID + 1
	if lastID == 0 {
		first, findErr := s.firstAggregateTrade(ctx, symbol, session.Start, target)
		if findErr != nil {
			return findErr
		}
		if first == 0 {
			return s.store.ApplyAggregateTrades(ctx, symbol, session.Start, nil, true)
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
			if err = s.store.ApplyAggregateTrades(ctx, symbol, session.Start, page, false); err != nil {
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
	if err = s.store.SetVolumeProfileComplete(ctx, symbol, session.Start, true); err == nil {
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

func (s *Service) volumeRequestedSymbol(symbol string) bool {
	s.volumeMu.Lock()
	defer s.volumeMu.Unlock()
	_, ok := s.volumeRequested[strings.ToUpper(strings.TrimSpace(symbol))]
	return ok
}

func (s *Service) saveCurrentVolumeSnapshots(ctx context.Context, at time.Time) {
	for _, symbol := range s.volumeBootstrapSymbols() {
		profile, err := s.VolumeProfile(ctx, symbol)
		if err != nil || profile.State != "ok" || profile.VAL == nil || profile.VAH == nil {
			continue
		}
		_ = s.store.SaveVolumeProfileSnapshot(ctx, domain.VolumeProfileSnapshot{Symbol: symbol, Time: at, SessionStart: profile.SessionStart, VAL: *profile.VAL, VAH: *profile.VAH, TotalVolumeUSD: profile.TotalVolumeUSD, Complete: true})
	}
}
