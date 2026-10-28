package app

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/coinglass"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/config"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/engine"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/exchange"
	marketview "github.com/pcdogyu/price-prediction-liquidation-levels/internal/market"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/ml"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/store"
)

// WebhookTarget is the per-user destination selected in the TVBot API-key
// settings. The resolver is kept in the application layer so alert generation
// can remain independent from the TVBot tenant implementation.
type WebhookTarget struct {
	Exchange string
	APIID    string
	TradeEnv string
	// Token is generated for the same tenant/target tuple as the payload. The
	// process-wide webhook token remains the fallback for legacy tenants.
	Token string
}

type WebhookTargetResolver func(tenantID string) (WebhookTarget, bool)
type GammaPolicyResolver func(tenantID string) string

type Service struct {
	dashboard           *dashboardState
	alerts              *alertRuntime
	alertTenantsMu      sync.Mutex
	alertTenants        map[string]*alertRuntime
	alertContext        context.Context
	cfg                 config.Config
	store               *store.Store
	clients             []exchange.Client
	cg                  *coinglass.Capturer
	health              *exchange.HealthRegistry
	log                 *slog.Logger
	mu                  sync.RWMutex
	priceMu             sync.Mutex
	klineMu             sync.Mutex
	alertCandleRepairMu sync.Mutex
	marketMu            sync.Mutex
	symbolCatalogMu     sync.Mutex
	klineCache          map[string]klineCacheEntry
	alertCandleRepairAt map[string]time.Time
	marketCache         map[string]marketCacheEntry
	symbolCatalog       []exchange.ContractSymbol
	symbolCatalogAt     time.Time
	trainMu             sync.Mutex
	maps                map[string]engine.MapResult
	predictions         map[string]domain.Prediction
	model               domain.ModelArtifact
	subs                map[chan domain.Prediction]struct{}
	liquidationSubs     map[chan domain.LiquidationEvent]struct{}
	priceSubs           map[chan domain.PriceTick]struct{}
	latestPrices        map[string]domain.PriceTick
	lastPriceBroadcast  map[string]int64
	backfillDone        bool
	volumeHistoryReady  bool
	volumeLiveReady     chan struct{}
	webhookTargetMu     sync.RWMutex
	webhookTarget       WebhookTargetResolver
	gammaPolicyMu       sync.RWMutex
	gammaPolicy         GammaPolicyResolver
}

func (s *Service) SetWebhookTargetResolver(resolver WebhookTargetResolver) {
	if s == nil {
		return
	}
	s.webhookTargetMu.Lock()
	s.webhookTarget = resolver
	s.webhookTargetMu.Unlock()
}

func (s *Service) SetGammaPolicyResolver(resolver GammaPolicyResolver) {
	if s == nil {
		return
	}
	s.gammaPolicyMu.Lock()
	s.gammaPolicy = resolver
	s.gammaPolicyMu.Unlock()
}

func (s *Service) gammaPolicyForTenant(tenantID string) string {
	if s == nil {
		return "ignore"
	}
	s.gammaPolicyMu.RLock()
	resolver := s.gammaPolicy
	s.gammaPolicyMu.RUnlock()
	if resolver == nil {
		return "ignore"
	}
	switch policy := strings.ToLower(strings.TrimSpace(resolver(strings.TrimSpace(tenantID)))); policy {
	case "ignore", "passthrough", "flip":
		return policy
	default:
		return "ignore"
	}
}

func (s *Service) webhookTargetForTenant(tenantID string) (WebhookTarget, bool) {
	if s == nil || strings.TrimSpace(tenantID) == "" {
		return WebhookTarget{}, false
	}
	s.webhookTargetMu.RLock()
	resolver := s.webhookTarget
	s.webhookTargetMu.RUnlock()
	if resolver == nil {
		return WebhookTarget{}, false
	}
	target, ok := resolver(strings.TrimSpace(tenantID))
	if !ok {
		return WebhookTarget{}, false
	}
	target.Exchange = strings.TrimSpace(target.Exchange)
	target.APIID = strings.TrimSpace(target.APIID)
	target.TradeEnv = strings.TrimSpace(target.TradeEnv)
	target.Token = strings.TrimSpace(target.Token)
	if target.Exchange == "" || target.TradeEnv == "" {
		return WebhookTarget{}, false
	}
	return target, true
}

func New(cfg config.Config, st *store.Store, log *slog.Logger) *Service {
	if strings.TrimSpace(cfg.AlertTenantDataPath) == "" && strings.TrimSpace(cfg.DatabasePath) != "" {
		cfg.AlertTenantDataPath = filepath.Join(filepath.Dir(cfg.DatabasePath), "alert-tenants")
	}
	s := &Service{cfg: cfg, store: st, clients: []exchange.Client{exchange.NewBinance()}, cg: coinglass.New(cfg.CoinGlassDebugURL, cfg.CoinGlassCaptureDir), health: exchange.NewHealthRegistry(), log: log, maps: map[string]engine.MapResult{}, predictions: map[string]domain.Prediction{}, subs: map[chan domain.Prediction]struct{}{}, liquidationSubs: map[chan domain.LiquidationEvent]struct{}{}, priceSubs: map[chan domain.PriceTick]struct{}{}, latestPrices: map[string]domain.PriceTick{}, lastPriceBroadcast: map[string]int64{}, klineCache: map[string]klineCacheEntry{}, alertCandleRepairAt: map[string]time.Time{}, marketCache: map[string]marketCacheEntry{}, volumeLiveReady: make(chan struct{}), alertTenants: make(map[string]*alertRuntime)}
	s.dashboard = newDashboardState()
	s.alerts = s.newAlertRuntime()
	if a, e := ml.Load(cfg.ModelPath); e == nil {
		if a.DataSource == domain.DataSourceBinanceUSDM && strings.HasPrefix(a.Version, ml.ModelVersionPrefix) && sameStrings(a.FeatureNames, engine.FeatureNames) {
			s.model = a
		} else {
			log.Warn("legacy or incompatible model ignored", "version", a.Version, "data_source", a.DataSource)
		}
	}
	return s
}

func (s *Service) Start(ctx context.Context) {
	s.alertTenantsMu.Lock()
	s.alertContext = ctx
	s.alertTenantsMu.Unlock()
	exchange.StartLiquidationStreams(ctx, nil, s.RecordLiquidation, s.health, s.log)
	s.startDashboard(ctx)
	go s.priceBroadcastLoop(ctx)
	go s.pollLoop(ctx)
	go s.predictionLoop(ctx)
	go s.rebuild(ctx)
	go s.bootstrap(ctx)
	go s.retrainLoop(ctx)
	go s.retentionLoop(ctx)
	s.startVolumeProfiles(ctx)
	go s.volumeArchiveLoop(ctx)
	go s.startAlerts(ctx)
	go s.alertCandleRepairLoop(ctx)
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
	bootstrapSymbols := s.bootstrapSymbols()
	for _, c := range s.clients {
		for _, sym := range bootstrapSymbols {
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

func (s *Service) bootstrapSymbols() []string {
	if len(s.cfg.BootstrapSymbols) > 0 {
		return s.cfg.BootstrapSymbols
	}
	return s.cfg.Symbols
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
		m.DataSource = domain.DataSourceBinanceUSDM
		p := domain.Prediction{Symbol: sym, DataSource: domain.DataSourceBinanceUSDM, Time: time.Now().UTC(), HorizonMinutes: 60, State: "data_insufficient", Experimental: true, SourceCoverage: s.coverage(), Reason: "清算墙或历史数据不足"}
		if e == nil {
			p.MarkPrice, p.ATR, p.UpperWall, p.LowerWall = m.MarkPrice, m.ATR, m.Upper, m.Lower
			p.DataAgeSeconds = time.Since(cs[len(cs)-1].Time).Seconds()
			if m.Upper != nil && m.Lower != nil && p.DataAgeSeconds <= s.cfg.StaleAfter.Seconds() {
				ll, sl, _ := s.store.LiquidationTotals(ctx, sym, time.Now().Add(-15*time.Minute))
				profile, profileErr := s.VolumeProfile(ctx, sym)
				var profileSnapshot *domain.VolumeProfileSnapshot
				if profileErr == nil && profile.State == "ok" && profile.VAL != nil && profile.VAH != nil {
					profileSnapshot = &domain.VolumeProfileSnapshot{Symbol: sym, Time: profile.DataThrough, SessionStart: profile.SessionStart, VAL: *profile.VAL, VAH: *profile.VAH, TotalVolumeUSD: profile.TotalVolumeUSD, Complete: true}
				} else {
					p.Reason = "成交量价值区尚未完整同步"
				}
				fv, fe := engine.BuildFeaturesWithProfile(sym, cs, m, ll, sl, profileSnapshot)
				s.mu.RLock()
				model := s.model
				volumeHistoryReady := s.volumeHistoryReady
				s.mu.RUnlock()
				if fe == nil && profileSnapshot != nil && volumeHistoryReady && len(model.Weights) > 0 {
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
				} else if !volumeHistoryReady {
					p.Reason = "正在重建30天逐笔成交量特征"
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
	s.trainMu.Lock()
	defer s.trainMu.Unlock()

	s.mu.RLock()
	volumeHistoryReady := s.volumeHistoryReady
	s.mu.RUnlock()
	if !volumeHistoryReady {
		s.log.Info("training deferred until thirty day volume profile history is complete")
		return
	}
	var samples []ml.Sample
	trainingDays := s.cfg.TrainingDays
	if trainingDays <= 0 {
		trainingDays = 30
	}
	for _, sym := range s.cfg.Symbols {
		from := time.Now().AddDate(0, 0, -(trainingDays + 5))
		cs, e := s.store.Candles(ctx, sym, from)
		if e != nil {
			continue
		}
		profiles, pe := s.store.VolumeProfileSnapshots(ctx, sym, from, time.Now().Add(time.Minute))
		if pe != nil {
			s.log.Warn("volume profile snapshots unavailable for training", "symbol", sym, "error", pe)
			continue
		}
		samples = append(samples, engine.HistoricalSamplesWithProfiles(sym, cs, engine.DefaultMapConfig(), profiles)...)
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
	s.runDailyMaintenance(ctx)
	for {
		timer := time.NewTimer(time.Until(nextDailyMaintenance(time.Now())))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			s.runDailyMaintenance(ctx)
		}
	}
}

// runDailyMaintenance is the whole daily cleanup: row retention, the capture
// files the browser leaves behind, and the space reclamation retention frees.
func (s *Service) runDailyMaintenance(ctx context.Context) {
	started := time.Now().UTC()
	failed := false
	if err := s.store.PruneBefore(ctx, started.AddDate(0, 0, -s.cfg.BackfillDays)); err != nil && ctx.Err() == nil {
		failed = true
		s.log.Warn("market retention cleanup failed", "error", err)
	}
	if err := s.store.PruneDashboard(ctx, started); err != nil && ctx.Err() == nil {
		failed = true
		s.log.Warn("dashboard retention cleanup failed", "error", err)
	}
	optionCleanup, err := s.store.CleanupOptionGamma(ctx, started)
	if err != nil && ctx.Err() == nil {
		failed = true
		s.log.Warn("option Gamma cleanup failed", "error", err)
	} else if optionCleanup.Expired > 0 || optionCleanup.Downsampled > 0 {
		s.log.Info("option Gamma cleanup", "expired", optionCleanup.Expired, "downsampled", optionCleanup.Downsampled, "dense_hours", domain.OptionGammaDenseHours, "retention_days", domain.OptionGammaRetentionDays)
	}
	if err := s.store.PruneAlerts(ctx, started.AddDate(0, 0, -domain.AlertRetentionDays)); err != nil && ctx.Err() == nil {
		failed = true
		s.log.Warn("alert retention cleanup failed", "error", err)
	}
	// The captures the CoinGlass browser writes are only read through
	// latest.json, so anything past the rolling window is pure disk pressure.
	removed, err := coinglass.PruneCaptures(s.cfg.CoinGlassCaptureDir, started, domain.CoinGlassCaptureRetention)
	if err != nil && ctx.Err() == nil {
		failed = true
		s.log.Warn("coinglass capture cleanup failed", "error", err)
	} else if removed > 0 {
		s.log.Info("coinglass capture cleanup", "removed", removed, "retention", domain.CoinGlassCaptureRetention)
	}
	compact, err := s.store.Compact(ctx, domain.DatabaseCompactMinimumFreeBytes)
	if err != nil && ctx.Err() == nil {
		failed = true
		s.log.Warn("database space cleanup failed", "error", err)
	} else if compact.Compacted {
		s.log.Info("database space reclaimed", "free_bytes", compact.FreeBytes, "bytes_before", compact.BytesBefore, "bytes_after", compact.BytesAfter, "elapsed", compact.Elapsed)
	}
	if ctx.Err() == nil && !failed {
		s.log.Info("daily database maintenance complete", "elapsed", time.Since(started))
	}
}

var maintenanceLocation = time.FixedZone("Asia/Shanghai", 8*60*60)

// nextDailyMaintenance schedules retention for 03:30 Beijing time, outside the
// busiest monitoring period and independently of the server's local timezone.
func nextDailyMaintenance(now time.Time) time.Time {
	local := now.In(maintenanceLocation)
	next := time.Date(local.Year(), local.Month(), local.Day(), 3, 30, 0, 0, maintenanceLocation)
	if !next.After(local) {
		next = next.AddDate(0, 0, 1)
	}
	return next
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
		if !s.backgroundSymbol(symbol) {
			return domain.Prediction{Symbol: symbol, DataSource: domain.DataSourceBinanceUSDM, State: "unavailable", Reason: "该币对未配置后台预测"}, nil
		}
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
	if !s.backgroundSymbol(symbol) {
		return s.onDemandMarket(ctx, symbol, intervalName, before, limit)
	}
	now := time.Now().UTC()
	latestRequest := before.IsZero()
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
	view.LiquidationMinimumUSD = domain.DefaultLiquidationMinimumUSD
	if latestRequest {
		s.priceMu.Lock()
		tick, hasTick := s.latestPrices[symbol]
		s.priceMu.Unlock()
		if hasTick {
			applyRealtimePrice(&view, tick, interval, limit)
		}
	}
	if len(view.Candles) > 0 {
		start := view.Candles[0].Time
		end := view.Candles[len(view.Candles)-1].Time.Add(interval)
		predictions, predictionErr := s.store.Predictions(ctx, symbol, start, end)
		if predictionErr != nil {
			return domain.MarketView{}, predictionErr
		}
		view.ModelSignals = marketview.DirectionalSignals(predictions, interval)
		liquidations, truncated, liquidationErr := s.store.Liquidations(ctx, symbol, start, end, 5000, domain.DefaultLiquidationMinimumUSD)
		if liquidationErr != nil {
			return domain.MarketView{}, liquidationErr
		}
		view.Liquidations = liquidations
		view.LiquidationsTruncated = truncated
	}
	return view, nil
}
func (s *Service) Map(symbol string) (engine.MapResult, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m, ok := s.maps[symbol]
	return m, ok
}

// CoinGlassMap returns the latest recent CoinGlass map for chart overlays.
// Keeping this read behind Service lets alert screenshots use the same capture
// directory and freshness rule as the dashboard without exposing the capturer
// to the notification package.
func (s *Service) CoinGlassMap(symbol string) (engine.MapResult, error) {
	if s == nil || s.cg == nil {
		return engine.MapResult{}, errors.New("CoinGlass capturer unavailable")
	}
	return s.cg.LiquidationMap(symbol, 2*time.Hour)
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

func (s *Service) RecordLiquidation(ctx context.Context, event domain.LiquidationEvent) error {
	if err := s.store.InsertLiquidation(ctx, event); err != nil {
		return err
	}
	// The alert engine only tracks the top volume watch list, so this stays cheap
	// for every event the all-symbol liquidation stream delivers.
	if s.alerts != nil {
		s.alerts.observe(ctx, event)
	}
	s.alertTenantsMu.Lock()
	for _, rtm := range s.alertTenants {
		rtm.observe(ctx, event)
	}
	s.alertTenantsMu.Unlock()
	s.mu.Lock()
	for ch := range s.liquidationSubs {
		select {
		case ch <- event:
		default:
		}
	}
	s.mu.Unlock()
	return nil
}

// Close stops tenant-owned alert databases. The main database remains owned by
// the process caller, just as it was before tenant alert storage was added.
func (s *Service) Close() error {
	if s == nil {
		return nil
	}
	s.alertTenantsMu.Lock()
	defer s.alertTenantsMu.Unlock()
	var first error
	for tenantID, rtm := range s.alertTenants {
		if rtm == nil || rtm.store == nil || rtm.store == s.store {
			continue
		}
		if err := rtm.store.Close(); err != nil && first == nil {
			first = errors.New("close alert tenant " + tenantID + ": " + err.Error())
		}
	}
	return first
}

func (s *Service) SubscribeLiquidations() (chan domain.LiquidationEvent, func()) {
	ch := make(chan domain.LiquidationEvent, 128)
	s.mu.Lock()
	s.liquidationSubs[ch] = struct{}{}
	s.mu.Unlock()
	return ch, func() { s.mu.Lock(); delete(s.liquidationSubs, ch); close(ch); s.mu.Unlock() }
}

func (s *Service) RecordAggregateTrade(trade domain.AggregateTrade) {
	if trade.ID <= 0 || trade.Symbol == "" || trade.Price <= 0 || trade.Time.IsZero() {
		return
	}
	tick := domain.PriceTick{TradeID: trade.ID, Symbol: trade.Symbol, Time: trade.Time.UTC(), Price: trade.Price}
	s.priceMu.Lock()
	current, exists := s.latestPrices[trade.Symbol]
	if !exists || tick.TradeID > current.TradeID {
		s.latestPrices[trade.Symbol] = tick
	}
	s.priceMu.Unlock()
}

func (s *Service) priceBroadcastLoop(ctx context.Context) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.broadcastLatestPrices()
		}
	}
}

func (s *Service) broadcastLatestPrices() {
	s.priceMu.Lock()
	defer s.priceMu.Unlock()
	for symbol, tick := range s.latestPrices {
		if tick.TradeID <= s.lastPriceBroadcast[symbol] {
			continue
		}
		for ch := range s.priceSubs {
			select {
			case ch <- tick:
			default:
			}
		}
		s.lastPriceBroadcast[symbol] = tick.TradeID
	}
}

func (s *Service) SubscribePrices() (chan domain.PriceTick, func()) {
	ch := make(chan domain.PriceTick, 16)
	s.priceMu.Lock()
	s.priceSubs[ch] = struct{}{}
	s.priceMu.Unlock()
	return ch, func() {
		s.priceMu.Lock()
		delete(s.priceSubs, ch)
		close(ch)
		s.priceMu.Unlock()
	}
}

func applyRealtimePrice(view *domain.MarketView, tick domain.PriceTick, interval time.Duration, limit int) {
	if view == nil || tick.Symbol != view.Symbol || tick.Price <= 0 || tick.Time.IsZero() {
		return
	}
	open24h := view.Summary.LastPrice - view.Summary.Change24h
	view.Summary.LastPrice = tick.Price
	view.Summary.High24h = max(view.Summary.High24h, tick.Price)
	if view.Summary.Low24h <= 0 {
		view.Summary.Low24h = tick.Price
	} else {
		view.Summary.Low24h = min(view.Summary.Low24h, tick.Price)
	}
	view.Summary.UpdatedAt = tick.Time.UTC()
	if open24h > 0 {
		view.Summary.Change24h = tick.Price - open24h
		view.Summary.ChangePct24h = view.Summary.Change24h / open24h * 100
	}
	view.RealtimePrice = &tick
	bucketSeconds := int64(interval / time.Second)
	bucketUnix := tick.Time.UTC().Unix()
	bucket := time.Unix(bucketUnix-bucketUnix%bucketSeconds, 0).UTC()
	last := len(view.Candles) - 1
	if last >= 0 && view.Candles[last].Time.Equal(bucket) {
		view.Candles[last].Close = tick.Price
		view.Candles[last].High = max(view.Candles[last].High, tick.Price)
		view.Candles[last].Low = min(view.Candles[last].Low, tick.Price)
		view.Candles[last].Complete = false
		view.Candles[last].Patterns = nil
	} else if last < 0 || view.Candles[last].Time.Before(bucket) {
		view.Candles = append(view.Candles, domain.MarketCandle{Time: bucket, Open: tick.Price, High: tick.Price, Low: tick.Price, Close: tick.Price, ExchangeCount: 1, Complete: false})
		if limit > 0 && len(view.Candles) > limit {
			view.Candles = view.Candles[len(view.Candles)-limit:]
			view.HasMore = true
		}
	}
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

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
