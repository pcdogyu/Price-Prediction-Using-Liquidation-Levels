package app

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/coinglass"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/config"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/engine"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/exchange"
	marketview "github.com/pcdogyu/price-prediction-liquidation-levels/internal/market"
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
	candidateMu         sync.RWMutex
	backfillMu          sync.Mutex
	klineCache          map[string]klineCacheEntry
	alertCandleRepairAt map[string]time.Time
	marketCache         map[string]marketCacheEntry
	symbolCatalog       []exchange.ContractSymbol
	symbolCatalogAt     time.Time
	candidateProvider   BackgroundCandidateProvider
	candidateSnapshot   domain.BackgroundSymbolSnapshot
	candidates          []string
	candidateUpdates    chan []string
	maps                map[string]engine.MapResult
	predictions         map[string]domain.Prediction
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
	s := &Service{cfg: cfg, store: st, clients: []exchange.Client{exchange.NewBinance()}, cg: coinglass.New(cfg.CoinGlassDebugURL, cfg.CoinGlassCaptureDir), health: exchange.NewHealthRegistry(), log: log, maps: map[string]engine.MapResult{}, predictions: map[string]domain.Prediction{}, subs: map[chan domain.Prediction]struct{}{}, liquidationSubs: map[chan domain.LiquidationEvent]struct{}{}, priceSubs: map[chan domain.PriceTick]struct{}{}, latestPrices: map[string]domain.PriceTick{}, lastPriceBroadcast: map[string]int64{}, klineCache: map[string]klineCacheEntry{}, alertCandleRepairAt: map[string]time.Time{}, marketCache: map[string]marketCacheEntry{}, volumeLiveReady: make(chan struct{}), alertTenants: make(map[string]*alertRuntime), candidateUpdates: make(chan []string, 1)}
	s.dashboard = newDashboardState()
	s.alerts = s.newAlertRuntime()
	return s
}

func (s *Service) Start(ctx context.Context) {
	s.loadBackgroundCandidateSnapshot(ctx)
	s.alertTenantsMu.Lock()
	s.alertContext = ctx
	s.alertTenantsMu.Unlock()
	exchange.StartLiquidationStreams(ctx, nil, s.RecordLiquidation, s.health, s.log)
	s.startDashboard(ctx)
	go s.priceBroadcastLoop(ctx)
	go s.pollLoop(ctx)
	go s.bootstrap(ctx)
	go s.retentionLoop(ctx)
	go s.backgroundCandidateRefreshLoop(ctx)
	s.startVolumeProfiles(ctx)
	go s.volumeArchiveLoop(ctx)
	go s.startAlerts(ctx)
	go s.alertCandleRepairLoop(ctx)
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
		for _, sym := range s.bootstrapSymbols() {
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
	complete := s.backfillSymbols(ctx, s.bootstrapSymbols())
	s.mu.Lock()
	s.backfillDone = complete
	s.mu.Unlock()
	if ctx.Err() != nil {
		return
	}
}

// backfillSymbols synchronizes the selected background set with the configured
// history window. The mutex prevents a scheduled candidate refresh from
// starting a second, overlapping backfill while startup is still catching up.
func (s *Service) backfillSymbols(ctx context.Context, symbols []string) bool {
	s.backfillMu.Lock()
	defer s.backfillMu.Unlock()

	now := time.Now().UTC()
	backfillDays := s.cfg.BackfillDays
	if backfillDays <= 0 {
		backfillDays = 30
	}
	target := now.AddDate(0, 0, -backfillDays)
	complete := true
	for _, c := range s.clients {
		for _, sym := range symbols {
			if ctx.Err() != nil {
				return false
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
	return complete
}

func (s *Service) bootstrapSymbols() []string {
	if symbols := s.backgroundSymbols(); len(symbols) > 0 {
		return symbols
	}
	if len(s.cfg.BootstrapSymbols) > 0 {
		return append([]string(nil), s.cfg.BootstrapSymbols...)
	}
	return append([]string(nil), s.cfg.Symbols...)
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
	retentionDays := s.cfg.RetentionDays
	if retentionDays <= 0 {
		retentionDays = 180
	}
	if err := s.store.PruneBefore(ctx, started.AddDate(0, 0, -retentionDays)); err != nil && ctx.Err() == nil {
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

const localPredictionDisabledReason = "本地信号与模型预测已停用；清算墙仅使用 CoinGlass 数据"

func (s *Service) Latest(ctx context.Context, symbol string) (domain.Prediction, error) {
	return domain.Prediction{Symbol: strings.ToUpper(strings.TrimSpace(symbol)), DataSource: "disabled", Time: time.Now().UTC(), HorizonMinutes: 60, State: "disabled", Experimental: true, SourceCoverage: s.coverage(), Reason: localPredictionDisabledReason}, nil
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
	return domain.BacktestReport{}, errors.New("model backtest disabled: local ATR and liquidation-wall calculation is disabled")
}
func (s *Service) Health() map[string]exchange.Health { return s.health.Snapshot() }
func (s *Service) Ready(ctx context.Context) bool {
	return s.store.Ping(ctx) == nil
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
