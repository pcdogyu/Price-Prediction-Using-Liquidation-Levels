package app

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/config"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/engine"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/exchange"
	marketview "github.com/pcdogyu/price-prediction-liquidation-levels/internal/market"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/ml"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/store"
)

type Service struct {
	cfg          config.Config
	store        *store.Store
	clients      []exchange.Client
	health       *exchange.HealthRegistry
	log          *slog.Logger
	mu           sync.RWMutex
	maps         map[string]engine.MapResult
	predictions  map[string]domain.Prediction
	model        domain.ModelArtifact
	subs         map[chan domain.Prediction]struct{}
	backfillDone bool
}

func New(cfg config.Config, st *store.Store, log *slog.Logger) *Service {
	s := &Service{cfg: cfg, store: st, clients: []exchange.Client{exchange.NewBinance(), exchange.NewBybit(), exchange.NewOKX()}, health: exchange.NewHealthRegistry(), log: log, maps: map[string]engine.MapResult{}, predictions: map[string]domain.Prediction{}, subs: map[chan domain.Prediction]struct{}{}}
	if a, e := ml.Load(cfg.ModelPath); e == nil {
		s.model = a
	}
	return s
}

func (s *Service) Start(ctx context.Context) {
	exchange.StartLiquidationStreams(ctx, s.cfg.Symbols, s.store.InsertLiquidation, s.health, s.log)
	go s.pollLoop(ctx)
	go s.predictionLoop(ctx)
	go s.bootstrap(ctx)
	go s.retrainLoop(ctx)
	go s.retentionLoop(ctx)
}

func (s *Service) predictionLoop(ctx context.Context) {
	t := time.NewTicker(s.cfg.SnapshotInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.rebuild(ctx)
		}
	}
}

func (s *Service) pollLoop(ctx context.Context) {
	s.poll(ctx)
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.poll(ctx)
		}
	}
}
func (s *Service) poll(ctx context.Context) {
	var wg sync.WaitGroup
	for _, c := range s.clients {
		for _, sym := range s.cfg.Symbols {
			wg.Add(1)
			go func(c exchange.Client, sym string) {
				defer wg.Done()
				cc, cancel := context.WithTimeout(ctx, 20*time.Second)
				defer cancel()
				row, e := c.Current(cc, sym)
				name := c.Name() + "_market"
				if e != nil {
					s.health.Fail(name, e)
					s.log.Warn("market poll failed", "exchange", c.Name(), "symbol", sym, "error", e)
					return
				}
				if e = s.store.UpsertCandles(ctx, []domain.Candle{row}); e != nil {
					s.log.Error("store candle failed", "error", e)
					return
				}
				s.health.Touch(name, 1)
			}(c, sym)
		}
	}
	wg.Wait()
}

func (s *Service) bootstrap(ctx context.Context) {
	now := time.Now().UTC()
	target := now.AddDate(0, 0, -s.cfg.BackfillDays)
	complete := true
	for _, c := range s.clients {
		for _, sym := range s.cfg.Symbols {
			if ctx.Err() != nil {
				return
			}
			first, last, exists, e := s.store.CandleBounds(ctx, c.Name(), sym)
			if e != nil {
				complete = false
				s.log.Warn("backfill bounds failed", "exchange", c.Name(), "symbol", sym, "error", e)
				continue
			}
			type backfillRange struct{ from, until time.Time }
			var ranges []backfillRange
			if !exists {
				ranges = append(ranges, backfillRange{target, now.Add(time.Minute)})
			} else {
				if first.After(target.Add(2 * time.Minute)) {
					ranges = append(ranges, backfillRange{target, first})
				}
				if last.Before(now.Add(-2 * time.Minute)) {
					ranges = append(ranges, backfillRange{last.Add(time.Minute), now.Add(time.Minute)})
				}
			}
			for _, window := range ranges {
				s.log.Info("backfill started", "exchange", c.Name(), "symbol", sym, "from", window.from, "until", window.until)
				rows, historyErr := s.historyWithRetry(ctx, c, sym, window.from, window.until)
				if historyErr != nil {
					complete = false
					s.log.Warn("backfill failed", "exchange", c.Name(), "symbol", sym, "from", window.from, "until", window.until, "error", historyErr)
					continue
				}
				expected := int(window.until.Sub(window.from) / time.Minute)
				if expected > 0 && len(rows)*5 < expected*4 {
					complete = false
					s.log.Warn("backfill range has insufficient coverage", "exchange", c.Name(), "symbol", sym, "from", window.from, "until", window.until, "candles", len(rows), "expected", expected)
				}
				if historyErr = s.store.UpsertCandles(ctx, rows); historyErr != nil {
					complete = false
					s.log.Error("backfill store failed", "exchange", c.Name(), "symbol", sym, "error", historyErr)
				} else {
					s.log.Info("backfill range complete", "exchange", c.Name(), "symbol", sym, "from", window.from, "until", window.until, "candles", len(rows))
				}
			}
		}
	}
	s.mu.Lock()
	s.backfillDone = complete
	s.mu.Unlock()
	s.rebuild(ctx)
	s.train(ctx)
}

func (s *Service) historyWithRetry(ctx context.Context, client exchange.Client, symbol string, from, until time.Time) ([]domain.Candle, error) {
	var rows []domain.Candle
	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		rows, err = client.History(ctx, symbol, from, until)
		if err == nil {
			return rows, nil
		}
		if attempt == 3 {
			break
		}
		s.log.Warn("backfill retry scheduled", "exchange", client.Name(), "symbol", symbol, "attempt", attempt, "error", err)
		timer := time.NewTimer(time.Duration(1<<(attempt-1)) * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return rows, ctx.Err()
		case <-timer.C:
		}
	}
	return rows, err
}

func (s *Service) rebuild(ctx context.Context) {
	for _, sym := range s.cfg.Symbols {
		cs, e := s.store.Candles(ctx, sym, time.Now().UTC().Add(-7*24*time.Hour))
		if e != nil {
			continue
		}
		m, e := engine.BuildMap(cs, engine.DefaultMapConfig())
		p := domain.Prediction{Symbol: sym, Time: time.Now().UTC(), HorizonMinutes: 60, State: "data_insufficient", Experimental: true, SourceCoverage: s.coverage(), Reason: "清算墙或历史数据不足"}
		if e == nil {
			p.MarkPrice, p.ATR, p.UpperWall, p.LowerWall = m.MarkPrice, m.ATR, m.Upper, m.Lower
			p.DataAgeSeconds = time.Since(cs[len(cs)-1].Time).Seconds()
			if m.Upper != nil && m.Lower != nil && p.DataAgeSeconds <= s.cfg.StaleAfter.Seconds() {
				ll, sl, _ := s.store.LiquidationTotals(ctx, sym, time.Now().Add(-15*time.Minute))
				fv, fe := engine.BuildFeatures(sym, cs, m, ll, sl)
				s.mu.RLock()
				model := s.model
				s.mu.RUnlock()
				if fe == nil && len(model.Weights) > 0 {
					probs, pe := ml.Predict(model, fv.Values)
					if pe == nil {
						p.State = "ok"
						p.Reason = ""
						p.Probabilities = probs
						p.ModelVersion = model.Version
						// Product output stays experimental until 60 days of live coverage and
						// two consecutive weekly promotion checks are persisted.
						p.Experimental = true
						p.Contributions = topContributions(ml.Contributions(model, fv.Values), 6)
					}
				} else if len(model.Weights) == 0 {
					p.Reason = "模型尚未完成首次训练"
				}
			}
		}
		p = marketview.EnrichPrediction(p, marketview.CompositeMinutes(cs), time.Now().UTC())
		s.mu.Lock()
		s.maps[sym] = m
		s.predictions[sym] = p
		for ch := range s.subs {
			select {
			case ch <- p:
			default:
			}
		}
		s.mu.Unlock()
		_ = s.store.SavePrediction(ctx, p)
	}
}

func (s *Service) train(ctx context.Context) {
	var samples []ml.Sample
	trainingDays := s.cfg.TrainingDays
	if trainingDays <= 0 {
		trainingDays = 30
	}
	for _, sym := range s.cfg.Symbols {
		cs, e := s.store.Candles(ctx, sym, time.Now().AddDate(0, 0, -trainingDays))
		if e != nil {
			continue
		}
		samples = append(samples, engine.HistoricalSamples(sym, cs, engine.DefaultMapConfig())...)
	}
	report, model, e := ml.WalkForward("BTCUSDT,ETHUSDT", samples, engine.FeatureNames)
	if e != nil {
		s.log.Warn("training skipped", "samples", len(samples), "error", e)
		return
	}
	if e = ml.Save(s.cfg.ModelPath, model); e != nil {
		s.log.Error("model save failed", "error", e)
		return
	}
	s.mu.Lock()
	s.model = model
	s.mu.Unlock()
	for _, sym := range s.cfg.Symbols {
		r := report
		r.Symbol = sym
		_ = s.store.SaveBacktest(ctx, r)
	}
	s.log.Info("model trained", "samples", report.Samples, "log_loss", report.LogLoss, "eligible", report.PromotionEligible)
	s.rebuild(ctx)
}
func (s *Service) retentionLoop(ctx context.Context) {
	prune := func() {
		if err := s.store.PruneBefore(ctx, time.Now().UTC().AddDate(0, 0, -s.cfg.BackfillDays)); err != nil && ctx.Err() == nil {
			s.log.Warn("market retention cleanup failed", "error", err)
		}
	}
	prune()
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			prune()
		}
	}
}
func (s *Service) retrainLoop(ctx context.Context) {
	for {
		now := time.Now()
		next := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 10, 0, 0, time.UTC)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(next)):
			s.train(ctx)
		}
	}
}

func (s *Service) Latest(ctx context.Context, symbol string) (domain.Prediction, error) {
	s.mu.RLock()
	p, ok := s.predictions[symbol]
	s.mu.RUnlock()
	if ok {
		if age := time.Since(p.Time); age > s.cfg.SnapshotInterval+s.cfg.StaleAfter {
			p.State = "data_insufficient"
			p.Reason = "预测快照已过期"
			p.Probabilities = nil
		}
		if cs, err := s.store.Candles(ctx, symbol, p.Time.Add(-time.Minute)); err == nil {
			p = marketview.EnrichPrediction(p, marketview.CompositeMinutes(cs), time.Now().UTC())
		}
		return p, nil
	}
	p, e := s.store.LatestPrediction(ctx, symbol)
	if errors.Is(e, sql.ErrNoRows) {
		return p, errors.New("prediction unavailable")
	}
	if e == nil {
		if cs, err := s.store.Candles(ctx, symbol, p.Time.Add(-time.Minute)); err == nil {
			p = marketview.EnrichPrediction(p, marketview.CompositeMinutes(cs), time.Now().UTC())
		}
	}
	return p, e
}

func (s *Service) Market(ctx context.Context, symbol, intervalName string, before time.Time, limit int) (domain.MarketView, error) {
	now := time.Now().UTC()
	interval, ok := marketview.ParseInterval(intervalName)
	if !ok {
		return domain.MarketView{}, errors.New("unsupported market interval")
	}
	if before.IsZero() {
		before = now.Add(time.Minute)
	}
	from := before.Add(-time.Duration(limit+3) * interval)
	cs, err := s.store.CandlesRange(ctx, symbol, from, before)
	if err != nil {
		return domain.MarketView{}, err
	}
	summary, err := s.store.CandlesRange(ctx, symbol, now.Add(-25*time.Hour), now.Add(time.Minute))
	if err != nil {
		return domain.MarketView{}, err
	}
	availableFrom, _, exists, err := s.store.SymbolBounds(ctx, symbol)
	if err != nil {
		return domain.MarketView{}, err
	}
	if !exists {
		availableFrom = time.Time{}
	}
	s.mu.RLock()
	backfillDone := s.backfillDone
	s.mu.RUnlock()
	view, err := marketview.BuildPagedView(symbol, cs, summary, nil, intervalName, before, limit, now, availableFrom, backfillDone)
	if err != nil {
		return domain.MarketView{}, err
	}
	if len(view.Candles) > 0 {
		start := view.Candles[0].Time
		end := view.Candles[len(view.Candles)-1].Time.Add(interval)
		predictions, predictionErr := s.store.Predictions(ctx, symbol, start, end)
		if predictionErr != nil {
			return domain.MarketView{}, predictionErr
		}
		view.ModelSignals = marketview.DirectionalSignals(predictions, interval)
	}
	return view, nil
}
func (s *Service) Map(symbol string) (engine.MapResult, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m, ok := s.maps[symbol]
	return m, ok
}
func (s *Service) Backtest(ctx context.Context, symbol string) (domain.BacktestReport, error) {
	return s.store.Backtest(ctx, symbol)
}
func (s *Service) Health() map[string]exchange.Health { return s.health.Snapshot() }
func (s *Service) Ready(ctx context.Context) bool {
	if s.store.Ping(ctx) != nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.predictions) > 0
}
func (s *Service) Subscribe() (chan domain.Prediction, func()) {
	ch := make(chan domain.Prediction, 8)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	return ch, func() { s.mu.Lock(); delete(s.subs, ch); close(ch); s.mu.Unlock() }
}
func (s *Service) coverage() map[string]float64 {
	out := map[string]float64{}
	for k, v := range s.health.Snapshot() {
		out[k] = v.Coverage
	}
	return out
}
func topContributions(in map[string]float64, n int) map[string]float64 {
	type kv struct {
		k string
		v float64
	}
	xs := make([]kv, 0, len(in))
	for k, v := range in {
		xs = append(xs, kv{k, v})
	}
	sort.Slice(xs, func(i, j int) bool {
		a, b := xs[i].v, xs[j].v
		if a < 0 {
			a = -a
		}
		if b < 0 {
			b = -b
		}
		return a > b
	})
	if len(xs) > n {
		xs = xs[:n]
	}
	out := map[string]float64{}
	for _, x := range xs {
		out[x.k] = x.v
	}
	return out
}
