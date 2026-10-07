package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/exchange"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/volumeprofile"
)

func (s *Service) startVolumeProfiles(ctx context.Context) {
	go s.volumeProfileLoop(ctx)
}

func (s *Service) volumeProfileLoop(ctx context.Context) {
	for _, symbol := range s.cfg.Symbols {
		if err := s.syncVolumeSession(ctx, symbol, time.Now().UTC()); err != nil && ctx.Err() == nil {
			s.log.Warn("aggregate trade initial backfill failed", "symbol", symbol, "error", err)
		}
	}
	input := make(chan domain.AggregateTrade, 100000)
	exchange.StartAggregateTradeStreams(ctx, s.cfg.Symbols, func(ctx context.Context, trade domain.AggregateTrade) error {
		s.RecordAggregateTrade(trade)
		select {
		case input <- trade:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}, s.health, s.log)
	close(s.volumeLiveReady)

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
		_, lastID, _, complete, err := s.store.VolumeProfileLevels(ctx, key.symbol, sessions[key].Start)
		if err != nil {
			return err
		}
		if !complete || lastID == 0 || aggregateTradeGap(rows, lastID) {
			_ = s.store.SetVolumeProfileComplete(ctx, key.symbol, sessions[key].Start, false)
			if err = s.syncVolumeSession(ctx, key.symbol, rows[len(rows)-1].Time); err != nil {
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
	_, lastID, _, _, err := s.store.VolumeProfileLevels(ctx, symbol, session.Start)
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
		rows, err := exchange.BinanceAggregateTrades(ctx, symbol, fromID, start, end)
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
	session, err := volumeprofile.SessionAt(now)
	if err != nil {
		return domain.VolumeProfile{}, err
	}
	levels, _, through, complete, err := s.store.VolumeProfileLevels(ctx, symbol, session.Start)
	if err != nil {
		return domain.VolumeProfile{}, err
	}
	return volumeprofile.Build(symbol, session, levels, through, now, complete), nil
}

func (s *Service) saveCurrentVolumeSnapshots(ctx context.Context, at time.Time) {
	for _, symbol := range s.cfg.Symbols {
		profile, err := s.VolumeProfile(ctx, symbol)
		if err != nil || profile.State != "ok" || profile.VAL == nil || profile.VAH == nil {
			continue
		}
		_ = s.store.SaveVolumeProfileSnapshot(ctx, domain.VolumeProfileSnapshot{Symbol: symbol, Time: at, SessionStart: profile.SessionStart, VAL: *profile.VAL, VAH: *profile.VAH, TotalVolumeUSD: profile.TotalVolumeUSD, Complete: true})
	}
}
