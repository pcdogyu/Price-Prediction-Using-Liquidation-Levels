// Package alert implements the liquidation alert strategy: it watches the top
// Binance USDT perpetual pairs by 24 hour volume, raises an alert when a 3 minute
// sliding window of liquidations on one position side exceeds the configured
// notional threshold, and derives two kinds of order signal from that alert.
//
// The rules, in the order the strategy applies them:
//
//  1. Long liquidations above the threshold inside the window raise a
//     "多单爆仓预警"; short liquidations raise the symmetric "空单爆仓预警".
//  2. While the alert is in its quiet observation period, another liquidation on
//     the same side extends the period instead of producing a signal.
//  3. Once the quiet period elapses without a same side liquidation, an
//     aggressive signal is emitted in the direction of the liquidated side: a
//     long liquidation alert becomes a long order, a short alert a short order.
//  4. If an opposite side liquidation then arrives inside the confirmation
//     window, a steady signal is emitted in the original alert direction.
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
}

// Defaults returns the production strategy configuration.
func Defaults() Config {
	return Config{
		Window:             3 * time.Minute,
		Quiet:              10 * time.Minute,
		Confirm:            30 * time.Minute,
		Refresh:            10 * time.Minute,
		BTCUSD:             300000,
		ETHUSD:             200000,
		OtherUSD:           100000,
		FollowUpMinimumUSD: 0,
		OppositeMinimumUSD: 0,
		TopSymbols:         domain.AlertTopSymbolsDefault,
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
		WindowMinutes:      int(c.Window / time.Minute),
		QuietMinutes:       int(c.Quiet / time.Minute),
		ConfirmMinutes:     int(c.Confirm / time.Minute),
		RefreshMinutes:     int(c.Refresh / time.Minute),
		TopSymbols:         c.TopSymbols,
		BTCUSD:             c.BTCUSD,
		ETHUSD:             c.ETHUSD,
		OtherUSD:           c.OtherUSD,
		FollowUpMinimumUSD: c.FollowUpMinimumUSD,
		OppositeMinimumUSD: c.OppositeMinimumUSD,
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
	id             string
	side           string
	status         string
	threshold      float64
	windowStart    time.Time
	triggeredAt    time.Time
	total          float64
	peak           float64
	count          int
	followUps      int
	lastSameSideAt time.Time
	lastPrice      float64
	quietUntil     time.Time
	aggressiveAt   time.Time
	aggressiveID   string
	steadyAt       time.Time
	steadyID       string
	oppositeUSD    float64
	oppositeCount  int
}

type symbolState struct {
	rank    int
	volume  float64
	windows map[string][]sample
	cycles  map[string]*cycle
	lastAt  map[string]time.Time
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

// Observe feeds one liquidation event into the engine and returns the alerts and
// signals the event produced. Events for symbols outside the watch list, and
// events without a usable notional, are ignored.
func (e *Engine) Observe(event domain.LiquidationEvent) Result {
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
	opposite := "short"
	if side == "short" {
		opposite = "long"
	}
	st.windows[side] = pruneSamples(appendSample(st.windows[side], sample{at: at, usd: event.NotionalUSD, price: event.Price}), at, e.cfg.Window)
	st.windows[opposite] = pruneSamples(st.windows[opposite], at, e.cfg.Window)
	st.lastAt[side] = at
	total, count, windowStart := windowTotal(st.windows[side], at, e.cfg.Window)

	// Rule 4 first: an opposite liquidation confirms an aggressive signal that is
	// still inside its confirmation window.
	if c := st.cycles[opposite]; c != nil && c.status == domain.AlertStateAggressive {
		e.confirmOpposite(symbol, st, c, at, event.Price, event.NotionalUSD, &res)
	}
	// Rule 2: an open cycle in this direction is refreshed and its quiet period
	// extended, so a continuing burst never produces a signal.
	if c := st.cycles[side]; c != nil {
		e.refreshCycle(symbol, c, at, total, count, event.NotionalUSD, event.Price, &res)
		return res
	}
	// Rule 1: raise a new alert when the window total exceeds the threshold.
	threshold := e.cfg.ThresholdFor(symbol)
	if total <= threshold {
		return res
	}
	c := &cycle{id: AlertID(symbol, side, at), side: side, status: domain.AlertStateWatching, threshold: threshold, windowStart: windowStart, triggeredAt: at, total: total, peak: total, count: count, lastSameSideAt: at, lastPrice: event.Price, quietUntil: at.Add(e.cfg.Quiet)}
	st.cycles[side] = c
	res.addAlert(c.view(e.cfg, symbol))
	return res
}

// Seed replays stored events, typically the recent liquidations read from SQLite
// after a restart, so rolling windows and open cycles survive a deployment. The
// returned rows are reported like any other result; the app persists them without
// notifying, and the deterministic IDs keep a replay from duplicating a row.
func (e *Engine) Seed(events []domain.LiquidationEvent) Result {
	var out Result
	for _, event := range events {
		res := e.Observe(event)
		out.Alerts = append(out.Alerts, res.Alerts...)
		out.Signals = append(out.Signals, res.Signals...)
	}
	return out
}

// refreshCycle applies rule 2. The window totals always follow the live window so
// the page shows the ongoing burst; the quiet deadline only moves for a same side
// liquidation that reaches FollowUpMinimumUSD, which defaults to every event.
func (e *Engine) refreshCycle(symbol string, c *cycle, at time.Time, total float64, count int, notional, price float64, res *Result) {
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
	// Rule 2 needs FollowUpMinimumUSD, which is zero by default so every
	// liquidation extends the quiet period.
	if c.status == domain.AlertStateWatching && notional >= e.cfg.FollowUpMinimumUSD && at.After(c.lastSameSideAt) {
		c.followUps++
		c.lastSameSideAt = at
		c.quietUntil = at.Add(e.cfg.Quiet)
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
	res.addSignal(domain.TradeSignal{
		ID: c.steadyID, AlertID: c.id, Symbol: symbol, Kind: domain.SignalSteady,
		Side: c.side, LiquidationSide: c.side, CreatedAt: at, ReferencePrice: price,
		AlertNotionalUSD: c.total, ThresholdUSD: c.threshold, WindowMinutes: int(e.cfg.Window / time.Minute),
		FollowUps: c.followUps, OppositeUSD: c.oppositeUSD, OppositeCount: c.oppositeCount,
		OppositeSeconds: int64(at.Sub(c.aggressiveAt).Seconds()), ConfirmLimitSecs: int64(e.cfg.Confirm / time.Second),
	})
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

// releaseIdle frees window samples that left the rolling window. Cycle state is
// kept until it reaches a terminal status.
func (e *Engine) releaseIdle(st *symbolState, now time.Time) {
	for side, samples := range st.windows {
		st.windows[side] = pruneSamples(samples, now, e.cfg.Window)
	}
}

// view renders the cycle as a persisted alert row.
func (c *cycle) view(cfg Config, symbol string) domain.LiquidationAlert {
	out := domain.LiquidationAlert{
		ID: c.id, Symbol: symbol, Side: c.side, TradeSide: c.side, WindowMinutes: int(cfg.Window / time.Minute),
		NotionalUSD: c.total, PeakUSD: c.peak, ThresholdUSD: c.threshold, EventCount: c.count,
		WindowStart: c.windowStart, TriggeredAt: c.triggeredAt, QuietSeconds: int64(cfg.Quiet / time.Second),
		QuietUntil: c.quietUntil, FollowUps: c.followUps, Status: c.status, ReferencePrice: c.lastPrice,
		AggressiveID: c.aggressiveID, AggressiveAt: timePtr(c.aggressiveAt), SteadyID: c.steadyID,
		SteadyAt: timePtr(c.steadyAt), OppositeUSD: c.oppositeUSD, UpdatedAt: time.Now().UTC(),
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
