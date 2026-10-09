package app

import (
	"context"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/exchange"
)

type wallTrack struct {
	Event        domain.WallEvent
	Linked       bool
	Emitted      bool
	LastObserved time.Time
}
type dashboardState struct {
	sync.RWMutex
	books           map[string]domain.BookSnapshot
	samples         map[string][]domain.BookSnapshot
	tracks          map[string]map[string]*wallTrack
	lastSave        map[string]time.Time
	metrics         map[string]domain.MarketMetric
	gamma           map[string]domain.GammaView
	deribit         map[string]domain.OptionGammaPoint
	deribitAttempts map[string]optionGammaAttempt
}

func newDashboardState() *dashboardState {
	return &dashboardState{books: map[string]domain.BookSnapshot{}, samples: map[string][]domain.BookSnapshot{}, tracks: map[string]map[string]*wallTrack{}, lastSave: map[string]time.Time{}, metrics: map[string]domain.MarketMetric{}, gamma: map[string]domain.GammaView{}, deribit: map[string]domain.OptionGammaPoint{}, deribitAttempts: map[string]optionGammaAttempt{}}
}
func (s *Service) startDashboard(ctx context.Context) {
	if err := s.store.InterruptWallEvents(ctx); err != nil {
		s.log.Error("interrupt previous wall events", "error", err)
	}
	for _, symbol := range s.cfg.Symbols {
		h, err := s.store.WallHistory(ctx, symbol, "snapshots", time.Now().Add(-10*time.Minute), time.Now().Add(time.Second), 500, "")
		if err == nil && len(h.Snapshots) > 0 {
			sort.Slice(h.Snapshots, func(i, j int) bool { return h.Snapshots[i].Time.Before(h.Snapshots[j].Time) })
			s.dashboard.samples[symbol] = h.Snapshots
			s.dashboard.books[symbol] = h.Snapshots[len(h.Snapshots)-1]
		}
		if g, err := s.store.LatestGamma(ctx, symbol); err == nil {
			s.dashboard.gamma[symbol] = g
		}
	}
	exchange.StartDepthStreams(ctx, s.cfg.Symbols, s.recordBook, s.health, s.log)
	go s.marketInfoLoop(ctx)
	go s.gammaLoop(ctx)
	for _, symbol := range []string{"BTCUSDT", "ETHUSDT"} {
		if point, err := s.store.LatestOptionGamma(ctx, symbol); err == nil {
			s.dashboard.Lock()
			s.dashboard.deribit[symbol] = point
			s.dashboard.Unlock()
		}
	}
	go s.deribitGammaLoop(ctx)
	go s.backfillMarketMetrics(ctx)
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			if err := s.store.PruneDashboard(ctx, time.Now().UTC()); err != nil && ctx.Err() == nil {
				s.log.Warn("dashboard retention failed", "error", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.interruptStaleWalls(ctx, time.Now().UTC())
			}
		}
	}()
}
func wallThreshold(levels []domain.DepthLevel) float64 {
	values := make([]float64, 0, len(levels))
	for _, l := range levels {
		if l.NotionalUSD > 0 {
			values = append(values, l.NotionalUSD)
		}
	}
	if len(values) == 0 {
		return 250000
	}
	sort.Float64s(values)
	return math.Max(250000, values[int(float64(len(values)-1)*.85)])
}
func updateWalls(tracks map[string]*wallTrack, book domain.BookSnapshot) ([]domain.WallEvent, domain.BookSnapshot) {
	changed := []domain.WallEvent{}
	filtered := book
	filtered.Bids = append([]domain.DepthLevel{}, book.Bids...)
	filtered.Asks = append([]domain.DepthLevel{}, book.Asks...)
	for _, side := range []string{"bid", "ask"} {
		levels := filtered.Bids
		if side == "ask" {
			levels = filtered.Asks
		}
		th := wallThreshold(levels)
		byPrice := map[int64]float64{}
		for _, lv := range levels {
			byPrice[int64(math.Round(lv.Price/book.BucketWidth))] = lv.NotionalUSD
		}
		for i, lv := range levels {
			k := int64(math.Round(lv.Price / book.BucketWidth))
			key := fmt.Sprintf("%s:%d", side, k)
			tr := tracks[key]
			linked := byPrice[k-1] >= th*.6 || byPrice[k+1] >= th*.6
			if lv.NotionalUSD >= th {
				if tr == nil {
					tr = &wallTrack{Event: domain.WallEvent{ID: fmt.Sprintf("%s:%s:%d:%d", book.Symbol, side, k, book.Time.UnixNano()), Symbol: book.Symbol, Side: side, Price: lv.Price, ThresholdUSD: th, PeakUSD: lv.NotionalUSD, StartedAt: book.Time, LastSeen: book.Time}, LastObserved: book.Time}
					tracks[key] = tr
				} else {
					gap := book.Time.Sub(tr.LastObserved)
					if gap <= time.Second && tr.Event.LastSeen.Equal(tr.LastObserved) {
						tr.Event.DurationMS += gap.Milliseconds()
					} else if !tr.Emitted {
						tr.Event.DurationMS = 0
						tr.Event.StartedAt = book.Time
						tr.Linked = false
					}
					tr.Event.LastSeen = book.Time
					tr.Event.PeakUSD = math.Max(tr.Event.PeakUSD, lv.NotionalUSD)
				}
				tr.LastObserved = book.Time
				tr.Linked = tr.Linked || linked
				if !tr.Emitted && tr.Linked && tr.Event.DurationMS >= 3000 {
					tr.Event.QualifiedAt = book.Time
					tr.Emitted = true
					changed = append(changed, tr.Event)
				}
			} else if tr != nil {
				if !tr.Emitted {
					tr.Event.DurationMS = 0
					tr.Linked = false
				}
				tr.LastObserved = book.Time
			}
			weight := .1
			if linked {
				weight = .35
			}
			if tr != nil && tr.Emitted && lv.NotionalUSD >= th {
				weight = 1
			}
			levels[i].NotionalUSD *= weight
			levels[i].Quantity *= weight
		}
	}
	for k, tr := range tracks {
		if book.Time.Sub(tr.Event.LastSeen) > 15*time.Second {
			if tr.Emitted {
				t := tr.Event.LastSeen
				tr.Event.EndedAt = &t
				tr.Event.EndReason = "disappeared"
				changed = append(changed, tr.Event)
			}
			delete(tracks, k)
		}
	}
	return changed, filtered
}
func (s *Service) recordBook(ctx context.Context, book domain.BookSnapshot) error {
	d := s.dashboard
	d.Lock()
	defer d.Unlock()
	if d.tracks[book.Symbol] == nil {
		d.tracks[book.Symbol] = map[string]*wallTrack{}
	}
	changed, filtered := updateWalls(d.tracks[book.Symbol], book)
	for _, e := range changed {
		if err := s.store.SaveWallEvent(ctx, e); err != nil {
			return err
		}
	}
	d.books[book.Symbol] = book
	samples := d.samples[book.Symbol]
	if len(samples) == 0 || book.Time.Sub(samples[len(samples)-1].Time) >= time.Second {
		samples = append(samples, filtered)
	}
	first := 0
	for first < len(samples) && book.Time.Sub(samples[first].Time) > 10*time.Minute {
		first++
	}
	if first > 0 {
		samples = append([]domain.BookSnapshot{}, samples[first:]...)
	}
	d.samples[book.Symbol] = samples
	if book.Time.Sub(d.lastSave[book.Symbol]) >= 5*time.Second {
		if err := s.store.SaveBookSnapshot(ctx, book); err != nil {
			return err
		}
		for _, tr := range d.tracks[book.Symbol] {
			if tr.Emitted {
				if err := s.store.SaveWallEvent(ctx, tr.Event); err != nil {
					return err
				}
			}
		}
		d.lastSave[book.Symbol] = book.Time
	}
	return nil
}
func (s *Service) interruptStaleWalls(ctx context.Context, now time.Time) {
	d := s.dashboard
	d.Lock()
	defer d.Unlock()
	for symbol, tracks := range d.tracks {
		if now.Sub(d.books[symbol].Time) <= 5*time.Second {
			continue
		}
		for k, tr := range tracks {
			if tr.Emitted {
				t := tr.Event.LastSeen
				tr.Event.EndedAt = &t
				tr.Event.EndReason = "disconnected"
				if err := s.store.SaveWallEvent(ctx, tr.Event); err != nil {
					s.log.Error("store interrupted wall", "error", err)
					continue
				}
			}
			delete(tracks, k)
		}
	}
}
func (s *Service) WallView(ctx context.Context, symbol string, halfLife, window int) (domain.WallView, error) {
	out := domain.WallView{State: "unavailable", Events: []domain.WallEvent{}, GhostBids: []domain.DepthLevel{}, GhostAsks: []domain.DepthLevel{}, PeakBids: []domain.DepthLevel{}, PeakAsks: []domain.DepthLevel{}, HalfLifeSeconds: halfLife, WindowMinutes: window}
	d := s.dashboard
	d.RLock()
	book, ok := d.books[symbol]
	samples := append([]domain.BookSnapshot{}, d.samples[symbol]...)
	d.RUnlock()
	if ok {
		out.Book = &book
		out.State = "ok"
		if time.Since(book.Time) > 5*time.Second {
			out.State = "stale"
		}
	}
	ghosts := map[string]map[float64]domain.DepthLevel{"bid": {}, "ask": {}}
	peaks := map[string]map[float64]domain.DepthLevel{"bid": {}, "ask": {}}
	now := time.Now().UTC()
	for _, sample := range samples {
		age := now.Sub(sample.Time)
		if age < 0 || age > 10*time.Minute {
			continue
		}
		factor := math.Pow(.5, age.Seconds()/float64(halfLife))
		for _, side := range []string{"bid", "ask"} {
			levels := sample.Bids
			if side == "ask" {
				levels = sample.Asks
			}
			for _, lv := range levels {
				v := lv
				v.NotionalUSD *= factor
				v.Quantity *= factor
				if v.NotionalUSD > ghosts[side][v.Price].NotionalUSD {
					ghosts[side][v.Price] = v
				}
				if age <= time.Duration(window)*time.Minute && lv.NotionalUSD > peaks[side][lv.Price].NotionalUSD {
					peaks[side][lv.Price] = lv
				}
			}
		}
	}
	list := func(m map[float64]domain.DepthLevel) []domain.DepthLevel {
		r := make([]domain.DepthLevel, 0, len(m))
		for _, v := range m {
			r = append(r, v)
		}
		sort.Slice(r, func(i, j int) bool { return r[i].Price < r[j].Price })
		return r
	}
	out.GhostBids = list(ghosts["bid"])
	out.GhostAsks = list(ghosts["ask"])
	out.PeakBids = list(peaks["bid"])
	out.PeakAsks = list(peaks["ask"])
	h, err := s.store.WallHistory(ctx, symbol, "events", now.AddDate(0, 0, -180), now.Add(time.Second), 50, "")
	out.Events = h.Events
	return out, err
}
func (s *Service) WallHistory(ctx context.Context, symbol, kind string, from, to time.Time, limit int, cursor string) (domain.WallHistory, error) {
	return s.store.WallHistory(ctx, symbol, kind, from, to, limit, cursor)
}
func (s *Service) LiquidationHistory(ctx context.Context, f domain.LiquidationFilter) (domain.LiquidationPage, error) {
	return s.store.LiquidationHistory(ctx, f)
}
