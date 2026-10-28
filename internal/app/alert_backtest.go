package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/valuearea"
)

var ErrAlertBacktestNotFound = errors.New("alert backtest job not found")

// AlertBacktestStatus is the short-poll response used by the alerts page. The
// actual replay runs outside the request context because a two-week raw replay
// can take longer than the reverse proxy's request timeout.
type AlertBacktestStatus struct {
	JobID  string                      `json:"job_id"`
	Status string                      `json:"status"`
	Report *domain.AlertBacktestReport `json:"report,omitempty"`
	Error  string                      `json:"error,omitempty"`
}

// AlertBacktestParameters are ephemeral parameters for one backtest run. They
// deliberately do not overlap with the persisted live alert settings.
type AlertBacktestParameters struct {
	InitialEquity      float64 `json:"initial_equity"`
	Leverage           float64 `json:"leverage"`
	TakeProfitPct      float64 `json:"take_profit_pct"`
	StopLossPct        float64 `json:"stop_loss_pct"`
	EntryNotionalUSD   float64 `json:"entry_notional_usd"`
	MaxAddOns          int     `json:"max_add_ons"`
	VWAPRangeHours     int     `json:"vwap_range_hours"`
	VWAPMinDrawdownPct float64 `json:"vwap_min_drawdown_pct"`
	VWAPDonchianPct    float64 `json:"vwap_donchian_pct"`
}

// DefaultAlertBacktestParameters returns the page/API defaults. The fixed
// 14-day window and holding-period comparison set are not request parameters.
func DefaultAlertBacktestParameters() AlertBacktestParameters {
	return AlertBacktestParameters{
		InitialEquity:      10000,
		Leverage:           1,
		TakeProfitPct:      1.2,
		StopLossPct:        1.5,
		EntryNotionalUSD:   1000,
		MaxAddOns:          1,
		VWAPRangeHours:     24,
		VWAPMinDrawdownPct: 2,
		VWAPDonchianPct:    35,
	}
}

// Validate checks all request-controlled backtest values before any replay
// goroutine is started.
func (p AlertBacktestParameters) Validate() error {
	positive := map[string]float64{
		"initial_equity":     p.InitialEquity,
		"leverage":           p.Leverage,
		"take_profit_pct":    p.TakeProfitPct,
		"stop_loss_pct":      p.StopLossPct,
		"entry_notional_usd": p.EntryNotionalUSD,
	}
	for name, value := range positive {
		if math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 {
			return fmt.Errorf("%s must be finite and positive", name)
		}
	}
	if p.MaxAddOns < 0 {
		return errors.New("max_add_ons must be a non-negative integer")
	}
	if p.VWAPRangeHours < minVWAPRangeHours || p.VWAPRangeHours > maxVWAPRangeHours {
		return fmt.Errorf("vwap_range_hours must be between %d and %d", minVWAPRangeHours, maxVWAPRangeHours)
	}
	if math.IsNaN(p.VWAPMinDrawdownPct) || math.IsInf(p.VWAPMinDrawdownPct, 0) || p.VWAPMinDrawdownPct < minVWAPMinDrawdownPct || p.VWAPMinDrawdownPct > maxVWAPMinDrawdownPct {
		return fmt.Errorf("vwap_min_drawdown_pct must be between %.1f and %.1f", minVWAPMinDrawdownPct, float64(maxVWAPMinDrawdownPct))
	}
	if math.IsNaN(p.VWAPDonchianPct) || math.IsInf(p.VWAPDonchianPct, 0) || p.VWAPDonchianPct < minVWAPDonchianPct || p.VWAPDonchianPct > maxVWAPDonchianPct {
		return fmt.Errorf("vwap_donchian_pct must be between %d and %d", minVWAPDonchianPct, maxVWAPDonchianPct)
	}
	return nil
}

func (p AlertBacktestParameters) equal(other AlertBacktestParameters) bool {
	return p == other
}

type alertBacktestJob struct {
	id          string
	status      string
	params      AlertBacktestParameters
	report      domain.AlertBacktestReport
	err         string
	completedAt time.Time
}

func (j *alertBacktestJob) snapshot() AlertBacktestStatus {
	status := AlertBacktestStatus{JobID: j.id, Status: j.status, Error: j.err}
	if j.status == "completed" {
		report := j.report
		status.Report = &report
	}
	return status
}

func newAlertBacktestJobID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return hex.EncodeToString(raw[:])
	}
	return fmt.Sprintf("%x", time.Now().UnixNano())
}

// StartAlertBacktestForContext starts or reuses the current tenant's one active
// replay. Completed reports are kept briefly so a page reload does not launch
// another expensive scan immediately.
func (s *Service) StartAlertBacktestForContext(ctx context.Context, requested ...AlertBacktestParameters) (AlertBacktestStatus, error) {
	params := DefaultAlertBacktestParameters()
	if len(requested) > 0 {
		params = requested[0]
	}
	if err := params.Validate(); err != nil {
		return AlertBacktestStatus{}, err
	}
	rtm, err := s.alertRuntimeForContext(ctx)
	if err != nil {
		return AlertBacktestStatus{}, err
	}
	rtm.backtestMu.Lock()
	now := time.Now().UTC()
	if existing := rtm.backtestJob; existing != nil {
		if existing.params.equal(params) && (existing.status == "running" || (existing.status == "completed" && now.Sub(existing.completedAt) < 10*time.Minute)) {
			status := existing.snapshot()
			rtm.backtestMu.Unlock()
			return status, nil
		}
	}
	job := &alertBacktestJob{id: newAlertBacktestJobID(), status: "running", params: params}
	rtm.backtestJob = job
	status := job.snapshot()
	rtm.backtestMu.Unlock()

	go func() {
		// Keep the tenant value but let the replay finish after the browser or
		// proxy closes the original request.
		report, replayErr := s.AlertBacktestForContext(context.WithoutCancel(ctx), params)
		rtm.backtestMu.Lock()
		defer rtm.backtestMu.Unlock()
		if rtm.backtestJob != job {
			return
		}
		job.completedAt = time.Now().UTC()
		if replayErr != nil {
			job.status = "failed"
			job.err = replayErr.Error()
			return
		}
		job.status = "completed"
		job.report = report
	}()
	return status, nil
}

// AlertBacktestStatusForContext returns only the job belonging to the current
// tenant, so an opaque job ID cannot be used to read another tenant's report.
func (s *Service) AlertBacktestStatusForContext(ctx context.Context, jobID string) (AlertBacktestStatus, error) {
	rtm, err := s.alertRuntimeForContext(ctx)
	if err != nil {
		return AlertBacktestStatus{}, err
	}
	rtm.backtestMu.Lock()
	defer rtm.backtestMu.Unlock()
	if rtm.backtestJob == nil || strings.TrimSpace(jobID) == "" || rtm.backtestJob.id != strings.TrimSpace(jobID) {
		return AlertBacktestStatus{}, ErrAlertBacktestNotFound
	}
	return rtm.backtestJob.snapshot(), nil
}

const (
	alertBacktestWindow        = 14 * 24 * time.Hour
	alertBacktestLegacyHolding = 15 * time.Minute
)

var alertBacktestHoldingDurations = [...]time.Duration{
	15 * time.Minute,
	1 * time.Hour,
	4 * time.Hour,
	8 * time.Hour,
	12 * time.Hour,
	24 * time.Hour,
}

func alertBacktestHoldingOptions() []int {
	options := make([]int, 0, len(alertBacktestHoldingDurations))
	for _, duration := range alertBacktestHoldingDurations {
		options = append(options, int(duration/time.Minute))
	}
	return options
}

type alertBacktestFilter struct {
	key      string
	label    string
	vwap     bool
	donchian bool
}

var alertBacktestFilters = []alertBacktestFilter{
	{key: "none", label: "无"},
	{key: "vwap", label: "VWAP", vwap: true},
	{key: "donchian", label: "Donchian", donchian: true},
	{key: "vwap_donchian", label: "VWAP + Donchian", vwap: true, donchian: true},
}

var alertBacktestSignals = []struct {
	key   string
	label string
}{
	{key: "alert", label: "预警"},
	{key: "aggressive", label: "激进"},
	{key: "steady", label: "稳健"},
	{key: domain.SignalValueArea, label: "交易所刷单"},
}

type alertBacktestEntry struct {
	at        time.Time
	price     float64
	side      string
	additions int
	candles   []domain.Candle
	// addOnPrices is only useful to deterministic unit tests. Historical
	// records leave it empty, which deliberately makes every add-on use price.
	addOnPrices []float64
}

type alertBacktestBucket struct {
	entries []alertBacktestEntry
}

// AlertBacktestForContext replays the current tenant's raw liquidation stream
// over the last two weeks under each opening-zone filter combination and also
// evaluates the independent VAL/VAH strategy from stored profile/Gamma history.
// It is an ephemeral research report: no alert history, strategy settings,
// orders, or notifications are written or changed.
func (s *Service) AlertBacktestForContext(ctx context.Context, requested ...AlertBacktestParameters) (domain.AlertBacktestReport, error) {
	params := DefaultAlertBacktestParameters()
	if len(requested) > 0 {
		params = requested[0]
	}
	if err := params.Validate(); err != nil {
		return domain.AlertBacktestReport{}, err
	}
	rtm, err := s.alertRuntimeForContext(ctx)
	if err != nil {
		return domain.AlertBacktestReport{}, err
	}
	to := time.Now().UTC()
	from := to.Add(-alertBacktestWindow)
	cfg, err := s.alertStrategyConfigForContext(ctx, rtm)
	if err != nil {
		return domain.AlertBacktestReport{}, err
	}
	// The live tenant settings above remain the source for thresholds and
	// filter switches. Only the three backtest zone parameters are overridden
	// in this ephemeral replay configuration.
	cfg.VWAPRangeMinutes = params.VWAPRangeHours * 60
	cfg.VWAPMinDrawdown = params.VWAPMinDrawdownPct / 100
	cfg.VWAPDonchianFraction = params.VWAPDonchianPct / 100
	// Match the explicit historical signal recalculation path. Peak-Decay is a
	// live timing gate and cannot be inferred reliably from sparse sampled
	// liquidations; the market/VWAP/Donchian gates remain active in the replay.
	cfg.SkipPeakDecay = true
	symbols := s.alertReplaySymbols(rtm)
	buckets := make(map[string]*alertBacktestBucket, len(alertBacktestSignals)*len(alertBacktestFilters))
	for _, signal := range alertBacktestSignals {
		for _, filter := range backtestFiltersForSignal(signal.key) {
			buckets[alertBacktestBucketKey(signal.key, filter.key)] = &alertBacktestBucket{}
		}
	}

	for _, symbol := range symbols {
		// Include the longest forward holding window in the candle read so all
		// six periods can score the last completed entries without another query.
		events, candles, replayErr := rtm.alertChartReplayData(ctx, symbol, cfg, to.Add(alertBacktestHoldingDurations[len(alertBacktestHoldingDurations)-1]), from)
		if replayErr != nil {
			return domain.AlertBacktestReport{}, replayErr
		}
		events = backtestEventsThrough(events, to)
		markets := replayAlertChartMarkets(events, cfg, candles)
		type filterReplay struct {
			filter string
			view   domain.AlertChartSignals
		}
		results := make(chan filterReplay, len(alertBacktestFilters))
		var filters sync.WaitGroup
		for _, filter := range alertBacktestFilters {
			filter := filter
			filters.Add(1)
			go func() {
				defer filters.Done()
				replayCfg := cfg
				replayCfg.VWAPFilterEnabled = filter.vwap
				replayCfg.DonchianFilterEnabled = filter.donchian
				replayCfg.FilterSettingsSet = true
				results <- filterReplay{filter: filter.key, view: rtm.replayAlertChartSignalsWithMarkets(ctx, symbol, replayCfg, to, from, events, markets, candles)}
			}()
		}
		filters.Wait()
		close(results)
		for result := range results {
			for _, row := range result.view.Alerts {
				if !backtestEntryAllowed(row.FilterApplied, row.OpenAllowed) {
					continue
				}
				bucket := buckets[alertBacktestBucketKey("alert", result.filter)]
				bucket.entries = append(bucket.entries, alertBacktestEntry{at: row.TriggeredAt, price: row.ReferencePrice, side: row.TradeSide, additions: row.FollowUps, candles: candles})
			}
			for _, row := range result.view.Signals {
				if !backtestEntryAllowed(row.FilterApplied, row.OpenAllowed) {
					continue
				}
				bucket := buckets[alertBacktestBucketKey(row.Kind, result.filter)]
				bucket.entries = append(bucket.entries, alertBacktestEntry{at: row.CreatedAt, price: row.ReferencePrice, side: row.Side, additions: row.FollowUps, candles: candles})
			}
		}
		valueAreaEntries, valueAreaErr := rtm.valueAreaBacktestEntries(ctx, symbol, from, to, candles)
		if valueAreaErr != nil {
			return domain.AlertBacktestReport{}, valueAreaErr
		}
		valueAreaBucket := buckets[alertBacktestBucketKey(domain.SignalValueArea, "none")]
		valueAreaBucket.entries = append(valueAreaBucket.entries, valueAreaEntries...)
	}

	report := domain.AlertBacktestReport{
		WindowDays:            int(alertBacktestWindow / (24 * time.Hour)),
		HoldingMinutes:        int(alertBacktestLegacyHolding / time.Minute),
		HoldingMinutesOptions: alertBacktestHoldingOptions(),
		InitialEquity:         params.InitialEquity,
		Leverage:              params.Leverage,
		TakeProfitPct:         params.TakeProfitPct,
		StopLossPct:           params.StopLossPct,
		EntryNotionalUSD:      params.EntryNotionalUSD,
		AddOnNotionalUSD:      params.EntryNotionalUSD,
		MaxAddOns:             params.MaxAddOns,
		VWAPRangeHours:        params.VWAPRangeHours,
		VWAPMinDrawdownPct:    params.VWAPMinDrawdownPct,
		VWAPDonchianPct:       params.VWAPDonchianPct,
		From:                  from,
		To:                    to,
		GeneratedAt:           to,
		SymbolCount:           len(symbols),
		Rows:                  make([]domain.AlertBacktestRow, 0, len(alertBacktestSignals)*len(alertBacktestFilters)*len(alertBacktestHoldingDurations)),
	}
	for _, signal := range alertBacktestSignals {
		for _, filter := range backtestFiltersForSignal(signal.key) {
			entries := buckets[alertBacktestBucketKey(signal.key, filter.key)].entries
			for _, holding := range alertBacktestHoldingDurations {
				metrics := scoreAlertBacktestForHolding(entries, params, holding)
				report.Rows = append(report.Rows, backtestRowForHolding(signal.key, filter.key, holding, metrics))
			}
		}
	}
	return report, nil
}

func backtestEventsThrough(events []domain.LiquidationEvent, to time.Time) []domain.LiquidationEvent {
	filtered := make([]domain.LiquidationEvent, 0, len(events))
	for _, event := range events {
		if !event.EventTime.IsZero() && event.EventTime.After(to) {
			continue
		}
		filtered = append(filtered, event)
	}
	return filtered
}

func backtestFiltersForSignal(signal string) []alertBacktestFilter {
	if signal == domain.SignalValueArea {
		return []alertBacktestFilter{{key: "none", label: "无"}}
	}
	return alertBacktestFilters
}

func (rtm *alertRuntime) valueAreaBacktestEntries(ctx context.Context, symbol string, from, to time.Time, candles []domain.Candle) ([]alertBacktestEntry, error) {
	source := rtm.sourceStore
	if source == nil {
		source = rtm.store
	}
	profileFrom := from.Add(-24 * time.Hour)
	profiles, err := source.VolumeProfileSnapshots(ctx, symbol, profileFrom, to.Add(time.Minute))
	if err != nil {
		return nil, err
	}
	anchor := "ETHUSDT"
	if strings.EqualFold(strings.TrimSpace(symbol), "BTCUSDT") {
		anchor = "BTCUSDT"
	}
	gammaPoints, err := source.OptionGammaHistory(ctx, anchor, profileFrom, to.Add(time.Minute))
	if err != nil {
		return nil, err
	}
	signals := valueAreaBacktestSignals(symbol, candles, profiles, gammaPoints, from, to)
	entries := make([]alertBacktestEntry, 0, len(signals))
	for _, signal := range signals {
		if signal.PositionEffect != "open" || signal.CreatedAt.Before(from) || signal.CreatedAt.After(to) {
			continue
		}
		entries = append(entries, alertBacktestEntry{at: signal.CreatedAt, price: signal.ReferencePrice, side: signal.Side, candles: candles})
	}
	return entries, nil
}

func valueAreaBacktestSignals(symbol string, candles []domain.Candle, snapshots []domain.VolumeProfileSnapshot, gammaPoints []domain.OptionGammaPoint, from, to time.Time) []domain.TradeSignal {
	engine := valuearea.New()
	profileIndex, gammaIndex := 0, 0
	var profile domain.VolumeProfile
	var haveProfile bool
	anchor := "ETHUSDT"
	if strings.EqualFold(strings.TrimSpace(symbol), "BTCUSDT") {
		anchor = "BTCUSDT"
	}
	signals := []domain.TradeSignal{}
	for _, candle := range candles {
		if candle.Time.After(to) {
			break
		}
		if candle.Close <= 0 {
			continue
		}
		for profileIndex < len(snapshots) && !snapshots[profileIndex].Time.After(candle.Time) {
			profile = volumeProfileFromSnapshot(snapshots[profileIndex])
			haveProfile = true
			profileIndex++
		}
		for gammaIndex < len(gammaPoints) && !gammaPoints[gammaIndex].Time.After(candle.Time) {
			gammaIndex++
		}
		var gamma domain.AlertGamma
		if gammaIndex > 0 {
			point := gammaPoints[gammaIndex-1]
			gamma = alertGammaFromPoint(anchor, point)
		} else {
			gamma = domain.AlertGamma{AnchorSymbol: anchor, State: "unavailable"}
		}
		if !haveProfile {
			profile = domain.VolumeProfile{Symbol: symbol, State: "unavailable"}
		}
		tick := domain.PriceTick{TradeID: candle.Time.UnixMilli(), Symbol: symbol, Time: candle.Time, Price: candle.Close}
		signal, ok := engine.Process(tick, profile, gamma)
		if ok && !signal.CreatedAt.Before(from) && !signal.CreatedAt.After(to) {
			signals = append(signals, signal)
		}
	}
	return signals
}

func volumeProfileFromSnapshot(snapshot domain.VolumeProfileSnapshot) domain.VolumeProfile {
	val, vah := snapshot.VAL, snapshot.VAH
	state := "unavailable"
	if snapshot.Complete {
		state = "ok"
	}
	return domain.VolumeProfile{Symbol: snapshot.Symbol, State: state, SessionStart: snapshot.SessionStart, VAL: &val, VAH: &vah, DataThrough: snapshot.Time, UpdatedAt: snapshot.Time}
}

func alertGammaFromPoint(anchor string, point domain.OptionGammaPoint) domain.AlertGamma {
	value := point.Gamma
	updated := point.Time.UTC()
	return domain.AlertGamma{AnchorSymbol: anchor, State: point.State, Gamma: value, UpdatedAt: &updated}
}

func alertBacktestBucketKey(signal, filter string) string {
	return signal + "\x00" + filter
}

func backtestEntryAllowed(applied, openAllowed bool) bool {
	return !applied || openAllowed
}

type alertBacktestMetrics struct {
	trades      int
	additions   int
	wins        int
	grossProfit float64
	grossLoss   float64
	maxDrawdown float64
	finalEquity float64
}

// scoreAlertBacktest keeps the legacy 15-minute helper available to focused
// tests and older in-package callers. A candle close triggers TP/SL or the
// fixed holding deadline; the next complete candle close is the exit price.
func scoreAlertBacktest(entries []alertBacktestEntry, params AlertBacktestParameters) alertBacktestMetrics {
	return scoreAlertBacktestForHolding(entries, params, alertBacktestLegacyHolding)
}

func scoreAlertBacktestForHolding(entries []alertBacktestEntry, params AlertBacktestParameters, holding time.Duration) alertBacktestMetrics {
	metrics := alertBacktestMetrics{finalEquity: params.InitialEquity}
	if len(entries) == 0 {
		return metrics
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].at.Before(entries[j].at) })
	peak := params.InitialEquity
	for _, entry := range entries {
		if entry.at.IsZero() || entry.price <= 0 || (entry.side != "long" && entry.side != "short") {
			continue
		}
		candles := entry.candles
		if len(candles) == 0 {
			continue
		}
		additions := maxInt(entry.additions, 0)
		if additions > params.MaxAddOns {
			additions = params.MaxAddOns
		}
		// Historical rows contain only a follow-up count and signal reference
		// price, so every order in the production replay uses that reference
		// price. Build the aggregate position explicitly so the accounting also
		// remains correct if independent add-on prices become available later.
		prices := make([]float64, 0, additions+1)
		prices = append(prices, entry.price)
		for index := 0; index < additions; index++ {
			price := entry.price
			if index < len(entry.addOnPrices) && entry.addOnPrices[index] > 0 {
				price = entry.addOnPrices[index]
			}
			prices = append(prices, price)
		}
		var totalQuantity, weightedEntry float64
		for _, price := range prices {
			quantity := params.EntryNotionalUSD * params.Leverage / price
			totalQuantity += quantity
			weightedEntry += quantity * price
		}
		if totalQuantity <= 0 {
			continue
		}
		avgEntry := weightedEntry / totalQuantity
		takeProfit := params.TakeProfitPct / 100
		stopLoss := params.StopLossPct / 100
		exitPrice := 0.0
		for index, candle := range candles {
			if !candle.Time.After(entry.at) || candle.Close <= 0 {
				continue
			}
			change := (candle.Close - avgEntry) / avgEntry
			if entry.side == "short" {
				change = -change
			}
			triggered := change >= takeProfit || change <= -stopLoss
			heldLongEnough := !candle.Time.Before(entry.at.Add(holding))
			if !triggered && !heldLongEnough {
				continue
			}
			if index+1 >= len(candles) || candles[index+1].Close <= 0 {
				// The rule requires a complete next candle for the exit.
				exitPrice = 0
				break
			}
			exitPrice = candles[index+1].Close
			break
		}
		if exitPrice <= 0 {
			continue
		}
		pnl := (exitPrice - avgEntry) * totalQuantity
		if entry.side == "short" {
			pnl = -pnl
		}
		metrics.trades++
		metrics.additions += additions
		if pnl > 0 {
			metrics.wins++
			metrics.grossProfit += pnl
		} else if pnl < 0 {
			metrics.grossLoss -= pnl
		}
		metrics.finalEquity += pnl
		if metrics.finalEquity > peak {
			peak = metrics.finalEquity
		}
		if peak > 0 {
			drawdown := (peak - metrics.finalEquity) / peak * 100
			if drawdown > metrics.maxDrawdown {
				metrics.maxDrawdown = drawdown
			}
		}
	}
	return metrics
}

func backtestRow(signal, filter string, metrics alertBacktestMetrics) domain.AlertBacktestRow {
	return backtestRowForHolding(signal, filter, alertBacktestLegacyHolding, metrics)
}

func backtestRowForHolding(signal, filter string, holding time.Duration, metrics alertBacktestMetrics) domain.AlertBacktestRow {
	row := domain.AlertBacktestRow{
		Signal:         signal,
		Filter:         filter,
		HoldingMinutes: int(holding / time.Minute),
		Trades:         metrics.trades,
		Additions:      metrics.additions,
		MaxDrawdown:    metrics.maxDrawdown,
		FinalEquity:    metrics.finalEquity,
	}
	if metrics.trades > 0 {
		row.WinRate = float64(metrics.wins) / float64(metrics.trades) * 100
	}
	if metrics.grossLoss > 0 {
		row.ProfitFactor = metrics.grossProfit / metrics.grossLoss
	}
	return row
}
