// Package alert implements the liquidation alert strategy: it watches the top
// Binance USDT perpetual pairs by 24 hour volume, raises an alert when a sliding
// window of liquidations on one position side exceeds the configured notional
// threshold, and derives filtered order signals from that alert.
//
// The rules, in the order the strategy applies them:
//
//  1. A context-aware threshold breach enters ARMED; the legacy Observe path
//     remains available only for isolated backwards-compatible callers.
//  2. ARMED becomes FIRED only after peak-decay and price-absorption confirm
//     the cascade has exhausted, then VWAP zone, quota/ATR, and grid filters
//     decide whether to open or only close the opposite position.
//  3. A FIRED cycle can still emit its steady confirmation signal when the
//     opposite liquidation arrives inside the confirmation window.
//
// The engine is a pure state machine over LiquidationEvent values with an
// injectable clock, so every transition is unit testable without SQLite or a
// network connection. Persistence and notification live in the app package.
package alert

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

// Config carries the strategy parameters. Zero or negative values fall back to
// the documented defaults, so a hand built Config (tests, an empty
// config.Config{}) still behaves like production.
type Config struct {
	Window  time.Duration
	Quiet   time.Duration
	Confirm time.Duration
	Refresh time.Duration
	// BTCUSD, ETHUSD and OtherUSD are the liquidation notional thresholds that
	// raise an alert inside Window.
	BTCUSD   float64
	ETHUSD   float64
	OtherUSD float64
	// FollowUpMinimumUSD is the minimum notional of a same side liquidation that
	// extends the quiet observation period. Zero counts every liquidation.
	FollowUpMinimumUSD float64
	// OppositeMinimumUSD is the minimum notional of an opposite side liquidation
	// that confirms an aggressive signal. Zero counts every liquidation.
	OppositeMinimumUSD float64
	TopSymbols         int

	// Radar filters. A context-aware observation uses all four layers; the
	// legacy Observe path stays available for replay/backward-compatible callers.
	VWAPPeriodMinutes   int
	VWAPZScore          float64
	PeakDecayWindow     time.Duration
	PeakDecayRatio      float64
	AbsorptionWickRatio float64
	DeepQuotaUSD        float64
	DeepQuotaMultiplier float64
	ATRPeriod           int
	DeepDrawdownATR     float64
	MinProfitSpread     float64
	GridStep0Price      float64
	GridStep0Pct        float64
	GridStepMultiplier  float64
	GridThresholdSlope  float64
}

// Defaults returns the production strategy configuration.
func Defaults() Config {
	return Config{
		Window:              3 * time.Minute,
		Quiet:               10 * time.Minute,
		Confirm:             30 * time.Minute,
		Refresh:             10 * time.Minute,
		BTCUSD:              300000,
		ETHUSD:              200000,
		OtherUSD:            100000,
		FollowUpMinimumUSD:  0,
		OppositeMinimumUSD:  0,
		TopSymbols:          domain.AlertTopSymbolsDefault,
		VWAPPeriodMinutes:   240,
		VWAPZScore:          0.8,
		PeakDecayWindow:     30 * time.Second,
		PeakDecayRatio:      0.4,
		AbsorptionWickRatio: 0.35,
		DeepQuotaUSD:        0,
		DeepQuotaMultiplier: 2,
		ATRPeriod:           60,
		DeepDrawdownATR:     1.5,
		MinProfitSpread:     0.007,
		GridStep0Price:      0,
		GridStep0Pct:        0.003,
		GridStepMultiplier:  1.4,
		GridThresholdSlope:  0.5,
	}
}

func (c Config) withDefaults() Config {
	d := Defaults()
	if c.Window <= 0 {
		c.Window = d.Window
	}
	if c.Quiet <= 0 {
		c.Quiet = d.Quiet
	}
	if c.Confirm <= 0 {
		c.Confirm = d.Confirm
	}
	if c.Refresh <= 0 {
		c.Refresh = d.Refresh
	}
	if c.BTCUSD <= 0 {
		c.BTCUSD = d.BTCUSD
	}
	if c.ETHUSD <= 0 {
		c.ETHUSD = d.ETHUSD
	}
	if c.OtherUSD <= 0 {
		c.OtherUSD = d.OtherUSD
	}
	if c.FollowUpMinimumUSD < 0 {
		c.FollowUpMinimumUSD = d.FollowUpMinimumUSD
	}
	if c.OppositeMinimumUSD < 0 {
		c.OppositeMinimumUSD = d.OppositeMinimumUSD
	}
	if c.TopSymbols <= 0 {
		c.TopSymbols = d.TopSymbols
	}
	if c.VWAPPeriodMinutes <= 0 {
		c.VWAPPeriodMinutes = d.VWAPPeriodMinutes
	}
	if c.VWAPZScore <= 0 {
		c.VWAPZScore = d.VWAPZScore
	}
	if c.PeakDecayWindow <= 0 {
		c.PeakDecayWindow = d.PeakDecayWindow
	}
	if c.PeakDecayRatio <= 0 || c.PeakDecayRatio >= 1 {
		c.PeakDecayRatio = d.PeakDecayRatio
	}
	if c.AbsorptionWickRatio <= 0 || c.AbsorptionWickRatio >= 1 {
		c.AbsorptionWickRatio = d.AbsorptionWickRatio
	}
	if c.DeepQuotaUSD < 0 {
		c.DeepQuotaUSD = d.DeepQuotaUSD
	}
	if c.DeepQuotaMultiplier <= 0 {
		c.DeepQuotaMultiplier = d.DeepQuotaMultiplier
	}
	if c.ATRPeriod <= 0 {
		c.ATRPeriod = d.ATRPeriod
	}
	if c.DeepDrawdownATR <= 0 {
		c.DeepDrawdownATR = d.DeepDrawdownATR
	}
	if c.MinProfitSpread <= 0 || c.MinProfitSpread >= 1 {
		c.MinProfitSpread = d.MinProfitSpread
	}
	if c.GridStep0Price < 0 {
		c.GridStep0Price = d.GridStep0Price
	}
	if c.GridStep0Pct <= 0 {
		c.GridStep0Pct = d.GridStep0Pct
	}
	if c.GridStepMultiplier <= 1 {
		c.GridStepMultiplier = d.GridStepMultiplier
	}
	if c.GridThresholdSlope <= 0 {
		c.GridThresholdSlope = d.GridThresholdSlope
	}
	return c
}

// ThresholdFor returns the alert threshold of a symbol. BTC and ETH use their
// dedicated thresholds, every other USDT pair uses the general one.
func (c Config) ThresholdFor(symbol string) float64 {
	switch strings.ToUpper(strings.TrimSuffix(strings.ToUpper(strings.TrimSpace(symbol)), "USDT")) {
	case "BTC":
		return c.BTCUSD
	case "ETH":
		return c.ETHUSD
	default:
		return c.OtherUSD
	}
}

// Thresholds publishes the effective parameters for the alert page.
func (c Config) Thresholds() domain.AlertThresholds {
	return domain.AlertThresholds{
		WindowMinutes:       int(c.Window / time.Minute),
		QuietMinutes:        int(c.Quiet / time.Minute),
		ConfirmMinutes:      int(c.Confirm / time.Minute),
		RefreshMinutes:      int(c.Refresh / time.Minute),
		TopSymbols:          c.TopSymbols,
		BTCUSD:              c.BTCUSD,
		ETHUSD:              c.ETHUSD,
		OtherUSD:            c.OtherUSD,
		FollowUpMinimumUSD:  c.FollowUpMinimumUSD,
		OppositeMinimumUSD:  c.OppositeMinimumUSD,
		VWAPPeriodMinutes:   c.VWAPPeriodMinutes,
		VWAPZScore:          c.VWAPZScore,
		PeakDecaySeconds:    int(c.PeakDecayWindow / time.Second),
		PeakDecayRatio:      c.PeakDecayRatio,
		AbsorptionWickRatio: c.AbsorptionWickRatio,
		DeepQuotaUSD:        c.DeepQuotaUSD,
		DeepQuotaMultiplier: c.DeepQuotaMultiplier,
		ATRPeriod:           c.ATRPeriod,
		DeepDrawdownATR:     c.DeepDrawdownATR,
		MinProfitSpread:     c.MinProfitSpread,
		GridStep0Price:      c.GridStep0Price,
		GridStep0Pct:        c.GridStep0Pct,
		GridStepMultiplier:  c.GridStepMultiplier,
		GridThresholdSlope:  c.GridThresholdSlope,
	}
}

// WatchEntry is one watch list symbol, decoupled from the exchange client.
type WatchEntry struct {
	Symbol         string
	Rank           int
	QuoteVolumeUSD float64
}

// Result carries what a single engine call produced. The app layer persists every
// row and notifies according to the notification settings.
type Result struct {
	Alerts  []domain.LiquidationAlert
	Signals []domain.TradeSignal
}

func (r *Result) addAlert(a domain.LiquidationAlert) { r.Alerts = append(r.Alerts, a) }
func (r *Result) addSignal(s domain.TradeSignal)     { r.Signals = append(r.Signals, s) }

// empty reports whether a call produced nothing, which keeps the tests and the app
// layer from touching SQLite for a no-op.
func (r *Result) empty() bool { return len(r.Alerts) == 0 && len(r.Signals) == 0 }

type sample struct {
	at    time.Time
	usd   float64
	price float64
}

// cycle is one alert cycle for a symbol and liquidation side. Only watching and
// aggressive cycles stay tracked; steady and expired cycles are terminal and get
// dropped after their final status update is reported.
type cycle struct {
	id                   string
	side                 string
	status               string
	threshold            float64
	windowStart          time.Time
	triggeredAt          time.Time
	total                float64
	peak                 float64
	count                int
	followUps            int
	lastSameSideAt       time.Time
	lastPrice            float64
	quietUntil           time.Time
	aggressiveAt         time.Time
	aggressiveID         string
	steadyAt             time.Time
	steadyID             string
	oppositeUSD          float64
	oppositeCount        int
	filterApplied        bool
	decision             FilterDecision
	lastContext          MarketContext
	contextAware         bool
	lowestPrice          float64
	highestPrice         float64
	peakRateUSDPerSecond float64
}

type symbolState struct {
	rank                int
	volume              float64
	windows             map[string][]sample
	cycles              map[string]*cycle
	lastAt              map[string]time.Time
	highWaterPrice      float64
	longLiqCumulative   float64
	lastLongEntryPrice  float64
	lastShortEntryPrice float64
	longEntryCount      int
	shortEntryCount     int
}

// Engine tracks the rolling windows and alert cycles of the watch list.
type Engine struct {
	mu    sync.Mutex
	cfg   Config
	watch map[string]*symbolState
	clock func() time.Time
}

// New builds an engine with normalized configuration.
func New(cfg Config) *Engine {
	return &Engine{cfg: cfg.withDefaults(), watch: map[string]*symbolState{}, clock: func() time.Time { return time.Now().UTC() }}
}

// SetClock replaces the clock used when an event carries no timestamp and for
// Snapshot; tests use it to keep the produced times deterministic.
func (e *Engine) SetClock(clock func() time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if clock != nil {
		e.clock = clock
	}
}

// Config returns the effective configuration.
func (e *Engine) Config() Config {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cfg
}

// Thresholds returns the effective thresholds and periods.
func (e *Engine) Thresholds() domain.AlertThresholds {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cfg.Thresholds()
}

// UpdateConfig applies editable strategy parameters without discarding the
// current watch list, rolling samples, or open cycles.
func (e *Engine) UpdateConfig(cfg Config) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.cfg = cfg.withDefaults()
}

// Watch replaces the watched symbol set. Symbols that drop out lose their window
// and cycle state, so a delisted pair cannot emit further alerts.
func (e *Engine) Watch(next []WatchEntry) {
	e.mu.Lock()
	defer e.mu.Unlock()
	updated := make(map[string]*symbolState, len(next))
	for _, entry := range next {
		symbol := strings.ToUpper(strings.TrimSpace(entry.Symbol))
		if symbol == "" {
			continue
		}
		if old, ok := e.watch[symbol]; ok {
			old.rank, old.volume = entry.Rank, entry.QuoteVolumeUSD
			updated[symbol] = old
			continue
		}
		updated[symbol] = &symbolState{rank: entry.Rank, volume: entry.QuoteVolumeUSD, windows: map[string][]sample{}, cycles: map[string]*cycle{}, lastAt: map[string]time.Time{}}
	}
	e.watch = updated
}

// WatchCount reports how many symbols are currently watched.
func (e *Engine) WatchCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.watch)
}

// Observe feeds one liquidation event into the legacy strategy path. Callers that
// have current candles should use ObserveWithContext so the radar filters are
// applied.
func (e *Engine) Observe(event domain.LiquidationEvent) Result {
	return e.observe(event, MarketContext{}, false)
}

// ObserveWithContext feeds one liquidation event through the four-layer radar.
// The context is captured on the cycle, allowing Tick to confirm a decayed pulse
// without requiring a second liquidation event.
func (e *Engine) ObserveWithContext(event domain.LiquidationEvent, ctx MarketContext) Result {
	return e.observe(event, ctx, true)
}

func (e *Engine) observe(event domain.LiquidationEvent, ctx MarketContext, forceRadar bool) Result {
	var res Result
	side := strings.ToLower(strings.TrimSpace(event.PositionSide))
	if side != "long" && side != "short" {
		return res
	}
	symbol := strings.ToUpper(strings.TrimSpace(event.Symbol))
	e.mu.Lock()
	defer e.mu.Unlock()
	st := e.watch[symbol]
	if st == nil || event.NotionalUSD <= 0 {
		return res
	}
	at := event.EventTime.UTC()
	if at.IsZero() {
		at = e.clock()
	}
	if ctx.Price <= 0 {
		ctx.Price = event.Price
	}
	if !ctx.Ready && len(ctx.Candles) >= e.cfg.VWAPPeriodMinutes {
		ctx.Ready = true
	}
	// The explicit context API is the production radar entry point. It must stay
	// ARMED when candles are temporarily unavailable; falling back to the legacy
	// Observe path here would recreate the unsafe threshold-first entry.
	contextAware := forceRadar || ctx.Ready || len(ctx.Candles) > 0
	e.updateMarketState(st, side, event.NotionalUSD, ctx)
	opposite := "short"
	if side == "short" {
		opposite = "long"
	}
	st.windows[side] = pruneSamples(appendSample(st.windows[side], sample{at: at, usd: event.NotionalUSD, price: event.Price}), at, e.cfg.Window)
	st.windows[opposite] = pruneSamples(st.windows[opposite], at, e.cfg.Window)
	st.lastAt[side] = at
	total, count, windowStart := windowTotal(st.windows[side], at, e.cfg.Window)
	currentRate := liquidationRate(st.windows[side], at, e.cfg.PeakDecayWindow)

	// Rule 4 first: an opposite liquidation confirms an aggressive signal that is
	// still inside its confirmation window.
	if c := st.cycles[opposite]; c != nil && c.status == domain.AlertStateAggressive {
		e.confirmOpposite(symbol, st, c, at, event.Price, event.NotionalUSD, &res)
	}
	// Rule 2: an open cycle in this direction is refreshed and its quiet period
	// extended, so a continuing burst never produces a signal.
	if c := st.cycles[side]; c != nil {
		if currentRate > c.peakRateUSDPerSecond {
			c.peakRateUSDPerSecond = currentRate
		}
		e.refreshCycle(symbol, c, at, total, count, event.NotionalUSD, event.Price, ctx, &res)
		return res
	}
	// Rule 1: raise a new alert when the window total exceeds the threshold.
	threshold := e.cfg.ThresholdFor(symbol)
	if contextAware && side == "long" {
		layers := st.longEntryCount
		if ctx.PositionDataAvailable && ctx.LongPositionCount > layers {
			layers = ctx.LongPositionCount
		}
		threshold = scaledThreshold(threshold, layers, e.cfg.GridThresholdSlope)
	}
	if total <= threshold {
		return res
	}
	cyclePrice := ctx.Price
	if cyclePrice <= 0 {
		cyclePrice = ctx.CurrentCandle.Close
	}
	c := &cycle{id: AlertID(symbol, side, at), side: side, status: domain.AlertStateWatching, threshold: threshold, windowStart: windowStart, triggeredAt: at, total: total, peak: total, count: count, lastSameSideAt: at, lastPrice: event.Price, quietUntil: at.Add(e.cfg.Quiet), contextAware: contextAware, filterApplied: contextAware, lastContext: ctx, lowestPrice: cyclePrice, highestPrice: cyclePrice, peakRateUSDPerSecond: currentRate}
	if contextAware {
		c.quietUntil = at.Add(e.cfg.PeakDecayWindow)
		c.decision = filterDecision(e.cfg, side, ctx, *st, e.cfg.ThresholdFor(symbol))
		c.decision.OpenAllowed = false
		c.decision.CloseAllowed = false
		c.decision.Ready = false
		c.decision.Reason = "peak_decay_armed;" + c.decision.Reason
	}
	st.cycles[side] = c
	res.addAlert(c.view(e.cfg, symbol))
	return res
}

// UpdateMarketContext refreshes the latest candle/price snapshot for active
// radar cycles. It intentionally does not move the liquidation extreme: that
// extreme is derived from liquidation observations, so a new low/high in the
// live candle remains a failed absorption until price recovers from it.
func (e *Engine) UpdateMarketContext(symbol string, ctx MarketContext) {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	if symbol == "" {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	st := e.watch[symbol]
	if st == nil {
		return
	}
	for _, c := range st.cycles {
		if c.contextAware && c.status == domain.AlertStateWatching {
			c.lastContext = ctx
		}
	}
}

// RadarSymbols returns symbols with a live ARMED radar cycle. The application
// uses this bounded list to refresh current 1m candles before each Tick without
// querying all watched pairs every second.
func (e *Engine) RadarSymbols() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, 0)
	for symbol, st := range e.watch {
		for _, c := range st.cycles {
			if c.contextAware && c.status == domain.AlertStateWatching {
				out = append(out, symbol)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

func (e *Engine) updateCycleContext(c *cycle, ctx MarketContext) {
	c.lastContext = ctx
	price := ctx.Price
	if price <= 0 {
		price = ctx.CurrentCandle.Close
	}
	if price <= 0 {
		return
	}
	if c.side == "short" {
		if c.highestPrice <= 0 || price > c.highestPrice {
			c.highestPrice = price
		}
		return
	}
	if c.lowestPrice <= 0 || price < c.lowestPrice {
		c.lowestPrice = price
	}
}

// Seed replays stored events, typically the recent liquidations read from SQLite
// after a restart, so rolling windows and open cycles survive a deployment. The
// returned rows are reported like any other result; the app persists them without
// notifying, and the deterministic IDs keep a replay from duplicating a row.
func (e *Engine) Seed(events []domain.LiquidationEvent) Result {
	var out Result
	for _, event := range events {
		// Restarts must rebuild ARMED cycles without manufacturing a signal from
		// a static threshold. The live tick loop refreshes the current candle
		// after this replay and can then confirm a genuine decay/absorption.
		res := e.ObserveWithContext(event, MarketContext{})
		out.Alerts = append(out.Alerts, res.Alerts...)
		out.Signals = append(out.Signals, res.Signals...)
	}
	return out
}

// refreshCycle applies rule 2. The window totals always follow the live window so
// the page shows the ongoing burst; the quiet deadline only moves for a same side
// liquidation that reaches FollowUpMinimumUSD, which defaults to every event.
func (e *Engine) refreshCycle(symbol string, c *cycle, at time.Time, total float64, count int, notional, price float64, ctx MarketContext, res *Result) {
	if c.status != domain.AlertStateWatching && c.status != domain.AlertStateAggressive {
		return
	}
	changed := false
	if total > c.total {
		c.total = total
		changed = true
	}
	if total > c.peak {
		c.peak = total
		changed = true
	}
	if count > c.count {
		c.count = count
		changed = true
	}
	if c.contextAware && ctx.Price > 0 {
		e.updateCycleContext(c, ctx)
	}
	// Rule 2 needs FollowUpMinimumUSD, which is zero by default so every
	// liquidation extends the quiet period.
	if c.status == domain.AlertStateWatching && notional >= e.cfg.FollowUpMinimumUSD && at.After(c.lastSameSideAt) {
		c.followUps++
		c.lastSameSideAt = at
		if c.contextAware {
			c.quietUntil = at.Add(e.cfg.PeakDecayWindow)
		} else {
			c.quietUntil = at.Add(e.cfg.Quiet)
		}
		if price > 0 {
			c.lastPrice = price
		}
		changed = true
	}
	if changed {
		res.addAlert(c.view(e.cfg, symbol))
	}
}

// confirmOpposite applies rule 4. The opposite notional is always accumulated for
// reporting; the steady signal only fires once an event reaches OppositeMinimumUSD,
// which defaults to every event.
func (e *Engine) confirmOpposite(symbol string, st *symbolState, c *cycle, at time.Time, price, usd float64, res *Result) {
	c.oppositeUSD += usd
	c.oppositeCount++
	if usd < e.cfg.OppositeMinimumUSD {
		res.addAlert(c.view(e.cfg, symbol))
		return
	}
	c.status = domain.AlertStateSteady
	c.steadyAt = at
	c.steadyID = SteadySignalID(symbol, c.side, at)
	signal := domain.TradeSignal{
		ID: c.steadyID, AlertID: c.id, Symbol: symbol, Kind: domain.SignalSteady,
		Side: c.side, LiquidationSide: c.side, CreatedAt: at, ReferencePrice: price,
		AlertNotionalUSD: c.total, ThresholdUSD: c.threshold, WindowMinutes: int(e.cfg.Window / time.Minute),
		FollowUps: c.followUps, OppositeUSD: c.oppositeUSD, OppositeCount: c.oppositeCount,
		OppositeSeconds: int64(at.Sub(c.aggressiveAt).Seconds()), ConfirmLimitSecs: int64(e.cfg.Confirm / time.Second),
	}
	applyDecisionToSignal(&signal, c.decision)
	res.addSignal(signal)
	res.addAlert(c.view(e.cfg, symbol))
	// The cycle is terminal, so the next breach of this direction can raise a
	// fresh alert.
	delete(st.cycles, c.side)
}

// Tick advances the quiet and confirmation deadlines. It runs on a short timer
// because a quiet period usually ends without any new liquidation event to trigger
// an evaluation, and must not depend on the next event arriving.
func (e *Engine) Tick(now time.Time) Result {
	var res Result
	e.mu.Lock()
	defer e.mu.Unlock()
	if now.IsZero() {
		now = e.clock()
	}
	now = now.UTC()
	for symbol, st := range e.watch {
		for side, c := range st.cycles {
			switch c.status {
			case domain.AlertStateWatching:
				if now.Before(c.quietUntil) {
					continue
				}
				if c.contextAware {
					if !e.tryFireRadar(symbol, st, c, now, &res) {
						continue
					}
					continue
				}
				// Rule 3: the quiet period elapsed, so the aggressive signal is
				// emitted in the direction of the liquidated positions.
				c.status = domain.AlertStateAggressive
				c.aggressiveAt = c.quietUntil
				c.aggressiveID = AggressiveSignalID(symbol, c.side, c.aggressiveAt)
				res.addSignal(domain.TradeSignal{
					ID: c.aggressiveID, AlertID: c.id, Symbol: symbol, Kind: domain.SignalAggressive,
					Side: c.side, LiquidationSide: c.side, CreatedAt: c.aggressiveAt, ReferencePrice: c.lastPrice,
					AlertNotionalUSD: c.total, ThresholdUSD: c.threshold, WindowMinutes: int(e.cfg.Window / time.Minute),
					QuietElapsedSeconds: int64(c.aggressiveAt.Sub(c.lastSameSideAt).Seconds()), FollowUps: c.followUps,
					ConfirmLimitSecs: int64(e.cfg.Confirm / time.Second),
				})
				res.addAlert(c.view(e.cfg, symbol))
			case domain.AlertStateAggressive:
				if now.Before(c.aggressiveAt.Add(e.cfg.Confirm)) {
					continue
				}
				// No opposite liquidation arrived in time; keep only the
				// aggressive signal and allow a fresh alert afterwards.
				c.status = domain.AlertStateExpired
				res.addAlert(c.view(e.cfg, symbol))
				delete(st.cycles, side)
			}
		}
		e.releaseIdle(st, now)
	}
	return res
}

// tryFireRadar implements ARMED -> FIRED. A threshold breach alone never emits
// an opening signal on the context-aware path: the latest decay window must be
// below the configured fraction of the pulse peak and price must show absorption.
func (e *Engine) tryFireRadar(symbol string, st *symbolState, c *cycle, now time.Time, res *Result) bool {
	currentRate := liquidationRate(st.windows[c.side], now, e.cfg.PeakDecayWindow)
	if currentRate > c.peakRateUSDPerSecond {
		c.peakRateUSDPerSecond = currentRate
	}
	decayRatio := 1.0
	if c.peakRateUSDPerSecond > 0 {
		decayRatio = currentRate / c.peakRateUSDPerSecond
	}
	ctx := c.lastContext
	absorbed := false
	recovered := false
	if ctx.Price > 0 {
		if c.side == "short" && c.highestPrice > 0 {
			recovered = ctx.Price <= c.highestPrice
		} else if c.side == "long" && c.lowestPrice > 0 {
			recovered = ctx.Price >= c.lowestPrice
		}
	}
	if ctx.CurrentCandle.Close > 0 || ctx.CurrentCandle.Low > 0 {
		absorbed = recovered || candleAbsorbed(c.side, ctx.CurrentCandle, ctx.Price, e.cfg.AbsorptionWickRatio)
	} else {
		absorbed = recovered
	}
	d := filterDecision(e.cfg, c.side, ctx, *st, e.cfg.ThresholdFor(symbol))
	d.PeakRateUSDPerSecond = c.peakRateUSDPerSecond
	d.CurrentRateUSDPerSecond = currentRate
	d.PeakDecayRatio = decayRatio
	d.AbsorptionConfirmed = absorbed
	if decayRatio >= e.cfg.PeakDecayRatio || !absorbed {
		if decayRatio >= e.cfg.PeakDecayRatio {
			d.Reason += ";peak_decay_not_confirmed"
		}
		if !absorbed {
			d.Reason += ";price_absorption_not_confirmed"
		}
		c.decision = d
		return false
	}
	if !d.Ready {
		c.decision = d
		return false
	}

	c.decision = d
	c.status = domain.AlertStateAggressive
	c.aggressiveAt = now
	c.aggressiveID = AggressiveSignalID(symbol, c.side, c.aggressiveAt)
	signal := domain.TradeSignal{
		ID: c.aggressiveID, AlertID: c.id, Symbol: symbol, Kind: domain.SignalAggressive,
		Side: c.side, LiquidationSide: c.side, CreatedAt: c.aggressiveAt, ReferencePrice: c.lastPrice,
		AlertNotionalUSD: c.total, ThresholdUSD: c.threshold, WindowMinutes: int(e.cfg.Window / time.Minute),
		QuietElapsedSeconds: int64(c.aggressiveAt.Sub(c.lastSameSideAt).Seconds()), FollowUps: c.followUps,
		ConfirmLimitSecs: int64(e.cfg.Confirm / time.Second),
	}
	applyDecisionToSignal(&signal, d)
	res.addSignal(signal)
	res.addAlert(c.view(e.cfg, symbol))
	if d.OpenAllowed {
		if c.side == "long" {
			st.longEntryCount++
			st.lastLongEntryPrice = c.lastPrice
		} else {
			st.shortEntryCount++
			st.lastShortEntryPrice = c.lastPrice
		}
	}
	return true
}

func applyDecisionToSignal(signal *domain.TradeSignal, decision FilterDecision) {
	if signal == nil || !decision.Applied {
		return
	}
	signal.Phase = domain.AlertPhaseFired
	signal.FilterApplied = true
	signal.OpenAllowed = decision.OpenAllowed
	signal.CloseAllowed = decision.CloseAllowed
	signal.FilterReason = decision.Reason
	signal.VWAPZScore = decision.ZScore
	signal.PeakRateUSDPerSecond = decision.PeakRateUSDPerSecond
	signal.CurrentRateUSDPerSecond = decision.CurrentRateUSDPerSecond
	signal.PeakDecayRatio = decision.PeakDecayRatio
	signal.AbsorptionConfirmed = decision.AbsorptionConfirmed
	signal.CumulativeLongLiquidationUSD = decision.CumulativeLongUSD
	signal.DeepQuotaUSD = decision.DeepQuotaUSD
	signal.DrawdownATR = decision.DrawdownATR
	signal.RequiredGridStepPrice = decision.RequiredGridStepPrice
	signal.RequiredThresholdUSD = decision.RequiredThresholdUSD
	signal.MinProfitSpread = decision.MinProfitSpread
}

func (e *Engine) updateMarketState(st *symbolState, side string, notional float64, ctx MarketContext) {
	price := ctx.Price
	if price <= 0 {
		price = ctx.CurrentCandle.Close
	}
	if price <= 0 {
		return
	}
	if st.highWaterPrice <= 0 {
		st.highWaterPrice = price
		for _, candle := range ctx.Candles {
			if candle.High > st.highWaterPrice {
				st.highWaterPrice = candle.High
			}
		}
		if side == "long" && price < st.highWaterPrice {
			st.longLiqCumulative += notional
		}
		return
	}
	if price > st.highWaterPrice {
		st.highWaterPrice = price
		st.longLiqCumulative = 0
	} else if side == "long" && price < st.highWaterPrice {
		st.longLiqCumulative += notional
	}
}

func liquidationRate(samples []sample, now time.Time, window time.Duration) float64 {
	if window <= 0 {
		return 0
	}
	total, _, _ := windowTotal(samples, now, window)
	return total / window.Seconds()
}

// releaseIdle frees window samples that left the rolling window. Cycle state is
// kept until it reaches a terminal status.
func (e *Engine) releaseIdle(st *symbolState, now time.Time) {
	for side, samples := range st.windows {
		st.windows[side] = pruneSamples(samples, now, e.cfg.Window)
	}
}

// view renders the cycle as a persisted alert row.
func (c *cycle) view(cfg Config, symbol string) domain.LiquidationAlert {
	quiet := cfg.Quiet
	if c.contextAware {
		quiet = cfg.PeakDecayWindow
	}
	out := domain.LiquidationAlert{
		ID: c.id, Symbol: symbol, Side: c.side, TradeSide: c.side, WindowMinutes: int(cfg.Window / time.Minute),
		NotionalUSD: c.total, PeakUSD: c.peak, ThresholdUSD: c.threshold, EventCount: c.count,
		WindowStart: c.windowStart, TriggeredAt: c.triggeredAt, QuietSeconds: int64(quiet / time.Second),
		QuietUntil: c.quietUntil, FollowUps: c.followUps, Status: c.status, ReferencePrice: c.lastPrice,
		AggressiveID: c.aggressiveID, AggressiveAt: timePtr(c.aggressiveAt), SteadyID: c.steadyID,
		SteadyAt: timePtr(c.steadyAt), OppositeUSD: c.oppositeUSD, UpdatedAt: time.Now().UTC(),
	}
	if c.filterApplied {
		if c.status == domain.AlertStateWatching {
			out.Phase = domain.AlertPhaseArmed
		} else if c.status == domain.AlertStateAggressive || c.status == domain.AlertStateSteady {
			out.Phase = domain.AlertPhaseFired
		}
		out.FilterApplied = true
		out.OpenAllowed = c.decision.OpenAllowed
		out.CloseAllowed = c.decision.CloseAllowed
		out.FilterReason = c.decision.Reason
		out.VWAPZScore = c.decision.ZScore
		out.PeakRateUSDPerSecond = c.decision.PeakRateUSDPerSecond
		out.CurrentRateUSDPerSecond = c.decision.CurrentRateUSDPerSecond
		out.PeakDecayRatio = c.decision.PeakDecayRatio
		out.AbsorptionConfirmed = c.decision.AbsorptionConfirmed
		out.CumulativeLongLiquidationUSD = c.decision.CumulativeLongUSD
		out.DeepQuotaUSD = c.decision.DeepQuotaUSD
		out.DrawdownATR = c.decision.DrawdownATR
		out.RequiredGridStepPrice = c.decision.RequiredGridStepPrice
		out.RequiredThresholdUSD = c.decision.RequiredThresholdUSD
		out.MinProfitSpread = c.decision.MinProfitSpread
	}
	if !c.lastSameSideAt.IsZero() {
		out.LastSameSideAt = timePtr(c.lastSameSideAt)
	}
	return out
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	v := t
	return &v
}

// Snapshot reports the live window state of every watched symbol, ordered by
// volume rank. It powers the monitoring table so the page can show why an alert
// did or did not fire instead of only the events that already happened.
func (e *Engine) Snapshot(now time.Time) []domain.AlertMonitor {
	e.mu.Lock()
	defer e.mu.Unlock()
	if now.IsZero() {
		now = e.clock()
	}
	now = now.UTC()
	out := make([]domain.AlertMonitor, 0, len(e.watch))
	for symbol, st := range e.watch {
		for side, samples := range st.windows {
			st.windows[side] = pruneSamples(samples, now, e.cfg.Window)
		}
		row := domain.AlertMonitor{Symbol: symbol, Rank: st.rank, QuoteVolumeUSD: st.volume, ThresholdUSD: e.cfg.ThresholdFor(symbol), WindowMinutes: int(e.cfg.Window / time.Minute), State: "idle"}
		row.LongUSD, row.LongCount, _ = windowTotal(st.windows["long"], now, e.cfg.Window)
		row.ShortUSD, row.ShortCount, _ = windowTotal(st.windows["short"], now, e.cfg.Window)
		var last time.Time
		for _, at := range st.lastAt {
			if at.After(last) {
				last = at
			}
		}
		row.LastEventAt = timePtr(last)
		// An aggressive cycle waiting for confirmation outranks a watching cycle
		// in the status column, because it is the actionable one.
		rank := map[string]int{"idle": 0, domain.AlertStateWatching: 1, domain.AlertStateAggressive: 2}[row.State]
		for side, c := range st.cycles {
			priority, known := map[string]int{domain.AlertStateWatching: 1, domain.AlertStateAggressive: 2}[c.status]
			if !known || priority <= rank {
				continue
			}
			rank = priority
			row.State, row.ActiveSide, row.AlertID = c.status, side, c.id
			row.QuietUntil = timePtr(c.quietUntil)
		}
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Rank != out[j].Rank {
			return out[i].Rank < out[j].Rank
		}
		return out[i].Symbol < out[j].Symbol
	})
	return out
}

// WatchList returns the current watch list ordered by volume rank.
func (e *Engine) WatchList() []WatchEntry {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]WatchEntry, 0, len(e.watch))
	for symbol, st := range e.watch {
		out = append(out, WatchEntry{Symbol: symbol, Rank: st.rank, QuoteVolumeUSD: st.volume})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Rank != out[j].Rank {
			return out[i].Rank < out[j].Rank
		}
		return out[i].Symbol < out[j].Symbol
	})
	return out
}

// appendSample keeps the sample slice ordered by time even when Binance delivers
// a slightly out of order event.
func appendSample(samples []sample, s sample) []sample {
	samples = append(samples, s)
	if n := len(samples); n > 1 && samples[n-2].at.After(s.at) {
		sort.SliceStable(samples, func(i, j int) bool { return samples[i].at.Before(samples[j].at) })
	}
	// A burst on one symbol must not grow the slice without bound.
	if n := len(samples); n > maxSamples {
		trimmed := make([]sample, maxSamples)
		copy(trimmed, samples[n-maxSamples:])
		samples = trimmed
	}
	return samples
}

const maxSamples = 4096

// pruneSamples drops samples that left the rolling window.
func pruneSamples(samples []sample, now time.Time, window time.Duration) []sample {
	cutoff := now.Add(-window)
	i := 0
	for i < len(samples) && samples[i].at.Before(cutoff) {
		i++
	}
	if i == 0 {
		return samples
	}
	return samples[i:]
}

// windowTotal sums the samples inside the rolling window and reports how many
// contributed plus the time of the oldest one.
func windowTotal(samples []sample, now time.Time, window time.Duration) (float64, int, time.Time) {
	cutoff := now.Add(-window)
	total := 0.
	count := 0
	var start time.Time
	for i := len(samples) - 1; i >= 0; i-- {
		if samples[i].at.Before(cutoff) {
			break
		}
		total += samples[i].usd
		start = samples[i].at
		count++
	}
	return total, count, start
}

// AlertID and the signal IDs are derived from the symbol, direction and the exact
// trigger time, so replaying stored events after a restart cannot create a second
// row for the same alert or signal.
func AlertID(symbol, side string, at time.Time) string {
	return hashID("alert", symbol, side, at)
}

// AggressiveSignalID is the deterministic ID of an aggressive signal.
func AggressiveSignalID(symbol, side string, at time.Time) string {
	return hashID("aggressive", symbol, side, at)
}

// SteadySignalID is the deterministic ID of a steady signal.
func SteadySignalID(symbol, side string, at time.Time) string {
	return hashID("steady", symbol, side, at)
}

func hashID(kind, symbol, side string, at time.Time) string {
	sum := sha256.Sum256([]byte(kind + "|" + symbol + "|" + side + "|" + at.UTC().Format(time.RFC3339Nano)))
	return hex.EncodeToString(sum[:])[:32]
}
