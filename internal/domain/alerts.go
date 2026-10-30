package domain

import (
	"fmt"
	"time"
)

// Signal kinds separate the two order recommendations produced by the
// liquidation alert strategy. Both kinds are stored independently so a later
// backtest can score the aggressive and the steady variant separately.
const (
	SignalAggressive = "aggressive"
	SignalSteady     = "steady"
	SignalValueArea  = "value_area"
)

// Alert cycle states. A radar cycle starts as watching/ARMED, becomes
// aggressive/FIRED only after peak-decay and price absorption, and ends as
// steady once an opposite liquidation confirms the move, or expires when it
// never arrives.
const (
	AlertStateWatching   = "watching"
	AlertStateAggressive = "aggressive"
	AlertStateSteady     = "steady"
	AlertStateExpired    = "expired"
)

// The persisted status names above remain backward compatible with existing
// history/UI clients. In the radar state machine they correspond to ARMED and
// FIRED respectively.
const (
	AlertStateArmed = AlertStateWatching
	AlertStateFired = AlertStateAggressive
	AlertPhaseArmed = "ARMED"
	AlertPhaseFired = "FIRED"
)

const (
	// AlertTopSymbolsDefault is how many configured Binance USDM pairs by 24h
	// quote volume the alert strategy watches.
	AlertTopSymbolsDefault = 20
	// AlertTimelinePageSize is the fixed page size used by the alert history UI.
	AlertTimelinePageSize = 20
	// AlertRetentionDays keeps alerts and signals for one year for backtesting
	// and historical review.
	AlertRetentionDays = 365
)

// AlertMonitorMethod describes the live monitor using the currently effective
// strategy settings rather than hard-coding the default window.
func AlertMonitorMethod(windowMinutes, refreshMinutes, topSymbols int) string {
	_ = refreshMinutes
	if topSymbols <= 0 {
		topSymbols = AlertTopSymbolsDefault
	}
	return fmt.Sprintf("监控名单来自 APP_SYMBOLS 配置范围内按 Binance 24H 成交额排序的前 %d 个 USDⓈ-M USDT 永续，持仓或挂单币对例外保留（服务启动时刷新，之后每天北京时间 07:50 刷新）。金额为公开采样强平流在 %d 分钟滑动窗口内的累计名义价值；币安每个币对每秒只推送最近一笔，因此窗口金额为下限值。超过阈值先进入 ARMED，等待 15~30 秒爆仓速率回落至峰值 40%% 以下且价格出现吸收后才 FIRED；VWAP Z-Score 加大周期 Donchian 双边边界决定开仓区/仅平仓区，深度配额、指数网格和最小获利间距继续拦截不合格信号。", topSymbols, windowMinutes)
}

// LiquidationAlert is one alert cycle for a symbol and the liquidated position
// side. Side=long means long positions were liquidated; TradeSide is the
// opposite position direction recommended by the strategy.
type LiquidationAlert struct {
	ID            string    `json:"id"`
	Symbol        string    `json:"symbol"`
	Side          string    `json:"side"`
	TradeSide     string    `json:"trade_side"`
	WindowMinutes int       `json:"window_minutes"`
	NotionalUSD   float64   `json:"notional_usd"`
	PeakUSD       float64   `json:"peak_usd"`
	ThresholdUSD  float64   `json:"threshold_usd"`
	EventCount    int       `json:"event_count"`
	WindowStart   time.Time `json:"window_start"`
	TriggeredAt   time.Time `json:"triggered_at"`
	// QuietSeconds is the required quiet period. QuietUntil is the aggressive
	// signal deadline and moves whenever the same side keeps liquidating.
	QuietSeconds   int64      `json:"quiet_seconds"`
	QuietUntil     time.Time  `json:"quiet_until"`
	LastSameSideAt *time.Time `json:"last_same_side_at,omitempty"`
	FollowUps      int        `json:"follow_ups"`
	Status         string     `json:"status"`
	ReferencePrice float64    `json:"reference_price,omitempty"`
	Phase          string     `json:"phase,omitempty"`
	// GammaIgnored records whether the opening side was suppressed by the
	// negative anchor Gamma gate when this alert was created. It is immutable
	// for history display even though the alert row is refreshed later.
	GammaIgnored bool       `json:"gamma_ignored"`
	AggressiveID string     `json:"aggressive_id,omitempty"`
	AggressiveAt *time.Time `json:"aggressive_at,omitempty"`
	SteadyID     string     `json:"steady_id,omitempty"`
	SteadyAt     *time.Time `json:"steady_at,omitempty"`
	OppositeUSD  float64    `json:"opposite_usd,omitempty"`
	// FilterApplied distinguishes a radar decision from legacy/manual rows. The
	// latter retain the historical open-and-preclose behavior.
	FilterApplied                bool      `json:"filter_applied,omitempty"`
	OpenAllowed                  bool      `json:"open_allowed,omitempty"`
	CloseAllowed                 bool      `json:"close_allowed,omitempty"`
	FilterReason                 string    `json:"filter_reason,omitempty"`
	FilterTags                   []string  `json:"filter_tags"`
	VWAPZScore                   float64   `json:"vwap_z_score,omitempty"`
	DonchianHigh                 float64   `json:"donchian_high,omitempty"`
	DonchianLow                  float64   `json:"donchian_low,omitempty"`
	GreenZoneUpper               float64   `json:"green_zone_upper,omitempty"`
	RedZoneLower                 float64   `json:"red_zone_lower,omitempty"`
	PeakRateUSDPerSecond         float64   `json:"peak_rate_usd_per_second,omitempty"`
	CurrentRateUSDPerSecond      float64   `json:"current_rate_usd_per_second,omitempty"`
	PeakDecayRatio               float64   `json:"peak_decay_ratio,omitempty"`
	AbsorptionConfirmed          bool      `json:"absorption_confirmed,omitempty"`
	CumulativeLongLiquidationUSD float64   `json:"cumulative_long_liquidation_usd,omitempty"`
	DeepQuotaUSD                 float64   `json:"deep_quota_usd,omitempty"`
	DrawdownATR                  float64   `json:"drawdown_atr,omitempty"`
	RequiredGridStepPrice        float64   `json:"required_grid_step_price,omitempty"`
	RequiredThresholdUSD         float64   `json:"required_threshold_usd,omitempty"`
	MinProfitSpread              float64   `json:"min_profit_spread,omitempty"`
	UpdatedAt                    time.Time `json:"updated_at"`
}

// TradeSignal is one order recommendation. Kind distinguishes the aggressive
// signal, which is the first FIRED radar decision, from the steady signal,
// which is additionally confirmed by an opposite direction liquidation.
type TradeSignal struct {
	ID               string    `json:"id"`
	TenantID         string    `json:"tenant_id,omitempty"`
	AlertID          string    `json:"alert_id"`
	Symbol           string    `json:"symbol"`
	Kind             string    `json:"kind"`
	Side             string    `json:"side"`
	LiquidationSide  string    `json:"liquidation_side"`
	CreatedAt        time.Time `json:"created_at"`
	ReferencePrice   float64   `json:"reference_price,omitempty"`
	Phase            string    `json:"phase,omitempty"`
	AlertNotionalUSD float64   `json:"alert_notional_usd"`
	ThresholdUSD     float64   `json:"threshold_usd"`
	WindowMinutes    int       `json:"window_minutes"`
	// GammaIgnored records the live Gamma decision made when this signal was
	// created; it must not be recomputed from the current Gamma for history.
	GammaIgnored bool `json:"gamma_ignored"`
	// QuietElapsedSeconds is how long the liquidated side stayed quiet and only
	// applies to the aggressive signal.
	QuietElapsedSeconds int64 `json:"quiet_elapsed_seconds,omitempty"`
	FollowUps           int   `json:"follow_ups"`
	// OppositeUSD, OppositeCount and OppositeSeconds describe the confirming
	// liquidation and only apply to the steady signal.
	OppositeUSD                  float64   `json:"opposite_usd,omitempty"`
	OppositeCount                int       `json:"opposite_count,omitempty"`
	OppositeSeconds              int64     `json:"opposite_elapsed_seconds,omitempty"`
	ConfirmLimitSecs             int64     `json:"confirm_limit_seconds,omitempty"`
	FilterApplied                bool      `json:"filter_applied,omitempty"`
	OpenAllowed                  bool      `json:"open_allowed,omitempty"`
	CloseAllowed                 bool      `json:"close_allowed,omitempty"`
	FilterReason                 string    `json:"filter_reason,omitempty"`
	FilterTags                   []string  `json:"filter_tags"`
	VWAPZScore                   float64   `json:"vwap_z_score,omitempty"`
	DonchianHigh                 float64   `json:"donchian_high,omitempty"`
	DonchianLow                  float64   `json:"donchian_low,omitempty"`
	GreenZoneUpper               float64   `json:"green_zone_upper,omitempty"`
	RedZoneLower                 float64   `json:"red_zone_lower,omitempty"`
	PeakRateUSDPerSecond         float64   `json:"peak_rate_usd_per_second,omitempty"`
	CurrentRateUSDPerSecond      float64   `json:"current_rate_usd_per_second,omitempty"`
	PeakDecayRatio               float64   `json:"peak_decay_ratio,omitempty"`
	AbsorptionConfirmed          bool      `json:"absorption_confirmed,omitempty"`
	CumulativeLongLiquidationUSD float64   `json:"cumulative_long_liquidation_usd,omitempty"`
	DeepQuotaUSD                 float64   `json:"deep_quota_usd,omitempty"`
	DrawdownATR                  float64   `json:"drawdown_atr,omitempty"`
	RequiredGridStepPrice        float64   `json:"required_grid_step_price,omitempty"`
	RequiredThresholdUSD         float64   `json:"required_threshold_usd,omitempty"`
	MinProfitSpread              float64   `json:"min_profit_spread,omitempty"`
	NotifyStatus                 string    `json:"notify_status,omitempty"`
	NotifyError                  string    `json:"notify_error,omitempty"`
	NotifiedAt                   time.Time `json:"notified_at,omitempty"`
	PositionEffect               string    `json:"position_effect,omitempty"`
	Boundary                     string    `json:"boundary,omitempty"`
	BoundaryPrice                float64   `json:"boundary_price,omitempty"`
	VAL                          float64   `json:"val,omitempty"`
	VAH                          float64   `json:"vah,omitempty"`
	GammaValue                   *float64  `json:"gamma_value,omitempty"`
	GammaState                   string    `json:"gamma_state,omitempty"`
}

// WatchSymbol is one entry of the dynamic top volume watch list.
type WatchSymbol struct {
	Symbol         string    `json:"symbol"`
	Rank           int       `json:"rank"`
	QuoteVolumeUSD float64   `json:"quote_volume_usd"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// AlertMonitor is the live per symbol view of the rolling alert window.
type AlertMonitor struct {
	Symbol         string     `json:"symbol"`
	Rank           int        `json:"rank"`
	QuoteVolumeUSD float64    `json:"quote_volume_usd"`
	ThresholdUSD   float64    `json:"threshold_usd"`
	WindowMinutes  int        `json:"window_minutes"`
	LongUSD        float64    `json:"long_usd"`
	ShortUSD       float64    `json:"short_usd"`
	LongCount      int        `json:"long_count"`
	ShortCount     int        `json:"short_count"`
	State          string     `json:"state"`
	ActiveSide     string     `json:"active_side,omitempty"`
	QuietUntil     *time.Time `json:"quiet_until,omitempty"`
	LastEventAt    *time.Time `json:"last_event_at,omitempty"`
	AlertID        string     `json:"alert_id,omitempty"`
}

// AlertStrategySettings are the editable parameters exposed by the alert page.
// Quiet/confirmation periods remain deployment settings because changing them
// alters the lifecycle of already-open alert cycles.
type AlertStrategySettings struct {
	WindowMinutes         int       `json:"window_minutes"`
	BTCUSD                float64   `json:"btc_usd"`
	ETHUSD                float64   `json:"eth_usd"`
	OtherUSD              float64   `json:"other_usd"`
	VWAPRangeHours        int       `json:"vwap_range_hours"`
	VWAPMinDrawdownPct    float64   `json:"vwap_min_drawdown_pct"`
	VWAPDonchianPct       float64   `json:"vwap_donchian_pct"`
	VWAPFilterEnabled     bool      `json:"vwap_filter_enabled"`
	DonchianFilterEnabled bool      `json:"donchian_filter_enabled"`
	UpdatedAt             time.Time `json:"updated_at"`
}

// AlertThresholds describes the effective strategy parameters.
type AlertThresholds struct {
	WindowMinutes         int     `json:"window_minutes"`
	QuietMinutes          int     `json:"quiet_minutes"`
	ConfirmMinutes        int     `json:"confirm_minutes"`
	RefreshMinutes        int     `json:"refresh_minutes"`
	TopSymbols            int     `json:"top_symbols"`
	BTCUSD                float64 `json:"btc_usd"`
	ETHUSD                float64 `json:"eth_usd"`
	OtherUSD              float64 `json:"other_usd"`
	FollowUpMinimumUSD    float64 `json:"follow_up_minimum_usd"`
	OppositeMinimumUSD    float64 `json:"opposite_minimum_usd"`
	VWAPPeriodMinutes     int     `json:"vwap_period_minutes"`
	VWAPZScore            float64 `json:"vwap_z_score_threshold"`
	VWAPRangeHours        int     `json:"vwap_range_hours"`
	VWAPMinDrawdownPct    float64 `json:"vwap_min_drawdown_pct"`
	VWAPDonchianPct       float64 `json:"vwap_donchian_pct"`
	VWAPFilterEnabled     bool    `json:"vwap_filter_enabled"`
	DonchianFilterEnabled bool    `json:"donchian_filter_enabled"`
	PeakDecaySeconds      int     `json:"peak_decay_seconds"`
	PeakDecayRatio        float64 `json:"peak_decay_ratio"`
	AbsorptionWickRatio   float64 `json:"absorption_wick_ratio"`
	DeepQuotaUSD          float64 `json:"deep_quota_usd"`
	DeepQuotaMultiplier   float64 `json:"deep_quota_multiplier"`
	ATRPeriod             int     `json:"atr_period"`
	DeepDrawdownATR       float64 `json:"deep_drawdown_atr"`
	MinProfitSpread       float64 `json:"min_profit_spread"`
	GridStep0Price        float64 `json:"grid_step0_price"`
	GridStep0Pct          float64 `json:"grid_step0_pct"`
	GridStepMultiplier    float64 `json:"grid_step_multiplier"`
	GridThresholdSlope    float64 `json:"grid_threshold_slope"`
}

// AlertPage and SignalPage are cursor paginated history pages.
type AlertPage struct {
	Rows       []LiquidationAlert `json:"rows"`
	NextCursor string             `json:"next_cursor,omitempty"`
	AlertCount int64              `json:"alert_count"`
	Gamma      AlertGamma         `json:"gamma"`
}
type SignalPage struct {
	Rows            []TradeSignal `json:"rows"`
	NextCursor      string        `json:"next_cursor,omitempty"`
	SignalCount     int64         `json:"signal_count"`
	AggressiveCount int64         `json:"aggressive_count"`
	SteadyCount     int64         `json:"steady_count"`
	ValueAreaCount  int64         `json:"value_area_count"`
	Gamma           AlertGamma    `json:"gamma"`
}

// AlertTimelineItem is one row in the unified alert and signal history. Type is
// alert, aggressive, steady or value_area; exactly one of Alert and Signal is populated.
type AlertTimelineItem struct {
	Type    string            `json:"type"`
	EventAt time.Time         `json:"event_at"`
	Alert   *LiquidationAlert `json:"alert,omitempty"`
	Signal  *TradeSignal      `json:"signal,omitempty"`
}

// AlertTimelinePage cursor-paginates the unified history. The three counters
// describe all rows for the selected symbol, while TotalCount honors the type
// filter and therefore matches the result set being paged.
type AlertTimelinePage struct {
	Rows            []AlertTimelineItem `json:"rows"`
	NextCursor      string              `json:"next_cursor,omitempty"`
	TotalCount      int64               `json:"total_count"`
	AlertCount      int64               `json:"alert_count"`
	AggressiveCount int64               `json:"aggressive_count"`
	SteadyCount     int64               `json:"steady_count"`
	ValueAreaCount  int64               `json:"value_area_count"`
	Replayed        bool                `json:"replayed,omitempty"`
	ReplayedAt      *time.Time          `json:"replayed_at,omitempty"`
	Gamma           AlertGamma          `json:"gamma"`
}

// AlertGamma is the current option-regime snapshot used by every liquidation
// pair. It is a live gate status, not a historical decision for stored rows.
// The value comes from the BTCUSDT or ETHUSDT Deribit normalized Gamma cache; a
// missing or stale snapshot is never treated as zero and therefore never
// suppresses an entry.
type AlertGamma struct {
	AnchorSymbol string     `json:"anchor_symbol"`
	State        string     `json:"state"`
	Gamma        *float64   `json:"gamma,omitempty"`
	UpdatedAt    *time.Time `json:"updated_at,omitempty"`
	Ignored      bool       `json:"ignored"`
}

// AlertChartSignals is the signal layer for a symbol chart. Unlike the stored
// alert and signal history, these rows are replayed with the currently saved
// strategy parameters so changing the alert form immediately changes what the
// chart displays without rewriting historical audit records.
type AlertChartSignals struct {
	Symbol      string             `json:"symbol"`
	Thresholds  AlertThresholds    `json:"thresholds"`
	Zone        AlertChartZone     `json:"zone"`
	Alerts      []LiquidationAlert `json:"alerts"`
	Signals     []TradeSignal      `json:"signals"`
	ReplayHours int                `json:"replay_hours"`
	GeneratedAt time.Time          `json:"generated_at"`
}

// AlertChartZone is the latest 1m VWAP/Donchian zone used by the radar. Prices
// above RedZoneLower are the high-side opening zone, prices below
// GreenZoneUpper are the low-side opening zone, and the band between them is
// close-only. The legacy upper/lower fields mirror those effective boundaries.
type AlertChartZone struct {
	Ready           bool    `json:"ready"`
	PeriodMinutes   int     `json:"period_minutes"`
	ZScoreThreshold float64 `json:"z_score_threshold"`
	RangeHours      int     `json:"range_hours"`
	MinDrawdownPct  float64 `json:"min_drawdown_pct"`
	DonchianPct     float64 `json:"donchian_pct"`
	VWAP            float64 `json:"vwap"`
	Sigma           float64 `json:"sigma"`
	CurrentPrice    float64 `json:"current_price"`
	CurrentZScore   float64 `json:"current_z_score"`
	DonchianHigh    float64 `json:"donchian_high"`
	DonchianLow     float64 `json:"donchian_low"`
	GreenZoneUpper  float64 `json:"green_zone_upper"`
	RedZoneLower    float64 `json:"red_zone_lower"`
	UpperOpenPrice  float64 `json:"upper_open_price"`
	LowerOpenPrice  float64 `json:"lower_open_price"`
	BoundaryOverlap bool    `json:"boundary_overlap"`
}

// AlertSignalRecalculation is the result of an ephemeral signal replay. The
// replay is intentionally not written back to the historical audit tables.
type AlertSignalRecalculation struct {
	WindowHours    int       `json:"window_hours"`
	From           time.Time `json:"from"`
	To             time.Time `json:"to"`
	RecalculatedAt time.Time `json:"recalculated_at"`
	SymbolCount    int       `json:"symbol_count"`
	AlertCount     int       `json:"alert_count"`
	SignalCount    int       `json:"signal_count"`
}

// AlertBacktestRow is one signal/filter/holding-period combination in the two-week alert
// backtest shown on the alert page. The value_area strategy has one independent
// "none" row because it uses VAL/VAH and Gamma rather than VWAP/Donchian filters.
// Metrics are based only on entries with a complete next candle for the exit;
// ProfitFactor is zero when no losing trade exists so the JSON remains finite
// and the UI can render it as unavailable.
type AlertBacktestRow struct {
	Signal         string  `json:"signal"`
	Filter         string  `json:"filter"`
	HoldingMinutes int     `json:"holding_minutes"`
	Trades         int     `json:"trades"`
	Additions      int     `json:"additions"`
	WinRate        float64 `json:"win_rate"`
	ProfitFactor   float64 `json:"profit_factor"`
	MaxDrawdown    float64 `json:"max_drawdown_pct"`
	FinalEquity    float64 `json:"final_equity"`
}

// AlertBacktestReport is an ephemeral, tenant-scoped alert replay report. It
// intentionally does not write rows to the alert audit tables or change live
// strategy/order settings.
type AlertBacktestReport struct {
	WindowDays            int                `json:"window_days"`
	HoldingMinutes        int                `json:"holding_minutes"`
	HoldingMinutesOptions []int              `json:"holding_minutes_options"`
	InitialEquity         float64            `json:"initial_equity"`
	Leverage              float64            `json:"leverage"`
	TakeProfitPct         float64            `json:"take_profit_pct"`
	StopLossPct           float64            `json:"stop_loss_pct"`
	EntryNotionalUSD      float64            `json:"entry_notional_usd"`
	AddOnNotionalUSD      float64            `json:"add_on_notional_usd"`
	MaxAddOns             int                `json:"max_add_ons"`
	VWAPRangeHours        int                `json:"vwap_range_hours"`
	VWAPMinDrawdownPct    float64            `json:"vwap_min_drawdown_pct"`
	VWAPDonchianPct       float64            `json:"vwap_donchian_pct"`
	From                  time.Time          `json:"from"`
	To                    time.Time          `json:"to"`
	GeneratedAt           time.Time          `json:"generated_at"`
	SymbolCount           int                `json:"symbol_count"`
	Rows                  []AlertBacktestRow `json:"rows"`
}

// KlineView backs the per symbol K-line chart on the alert page. The candles come
// straight from the Binance USDⓈ-M klines endpoint, so every monitored pair can be
// charted even though only the configured symbols are stored locally. NextBefore
// is the cursor the chart sends back to page further into history.
type KlineView struct {
	Symbol     string         `json:"symbol"`
	Interval   string         `json:"interval"`
	IntervalMS int64          `json:"interval_ms"`
	Candles    []MarketCandle `json:"candles"`
	HasMore    bool           `json:"has_more"`
	NextBefore *time.Time     `json:"next_before,omitempty"`
	Source     string         `json:"source"`
	FetchedAt  time.Time      `json:"fetched_at"`
}

// NotificationSettings is persisted in SQLite. The bot token is deliberately
// excluded from JSON so it can never leave the server in an API response.
type NotificationSettings struct {
	TelegramEnabled  bool   `json:"telegram_enabled"`
	TelegramToken    string `json:"-"`
	TelegramChatID   string `json:"telegram_chat_id"`
	NotifyAlerts     bool   `json:"notify_alerts"`
	NotifyAggressive bool   `json:"notify_aggressive"`
	NotifySteady     bool   `json:"notify_steady"`
	NotifyValueArea  bool   `json:"notify_value_area"`
	// OrderAlerts, OrderAggressive and OrderSteady are the opening switches. They
	// retain their original JSON names for API compatibility.
	OrderAlerts          bool      `json:"order_alerts"`
	OrderAggressive      bool      `json:"order_aggressive"`
	OrderSteady          bool      `json:"order_steady"`
	OrderAlertsClose     bool      `json:"order_alerts_close"`
	OrderAggressiveClose bool      `json:"order_aggressive_close"`
	OrderSteadyClose     bool      `json:"order_steady_close"`
	OrderValueArea       bool      `json:"order_value_area"`
	OrderValueAreaClose  bool      `json:"order_value_area_close"`
	HasToken             bool      `json:"has_token"`
	TokenMask            string    `json:"token_mask"`
	TokenSource          string    `json:"token_source"`
	UpdatedAt            time.Time `json:"updated_at"`
}

// OrderSignalSettings controls the close and opening requests for each live
// liquidation category. The first three fields are the legacy opening switch
// names retained for existing clients.
type OrderSignalSettings struct {
	Alerts          bool      `json:"order_alerts"`
	Aggressive      bool      `json:"order_aggressive"`
	Steady          bool      `json:"order_steady"`
	AlertsClose     bool      `json:"order_alerts_close"`
	AggressiveClose bool      `json:"order_aggressive_close"`
	SteadyClose     bool      `json:"order_steady_close"`
	ValueArea       bool      `json:"order_value_area"`
	ValueAreaClose  bool      `json:"order_value_area_close"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// NotificationStatus reports the state of the Telegram delivery worker.
type NotificationStatus struct {
	Enabled       bool       `json:"enabled"`
	Configured    bool       `json:"configured"`
	QueueDepth    int        `json:"queue_depth"`
	Sent          int64      `json:"sent"`
	Suppressed    int64      `json:"suppressed"`
	Failed        int64      `json:"failed"`
	LastAttemptAt *time.Time `json:"last_attempt_at,omitempty"`
	LastSuccessAt *time.Time `json:"last_success_at,omitempty"`
	LastError     string     `json:"last_error,omitempty"`
}

// AlertsView backs the alert page. Settings and exchange health are attached by
// the HTTP handler so they stay out of the strategy owned view.
type AlertsView struct {
	State           string             `json:"state"`
	Thresholds      AlertThresholds    `json:"thresholds"`
	Method          string             `json:"method"`
	Gamma           AlertGamma         `json:"gamma"`
	Monitor         []AlertMonitor     `json:"monitor"`
	Watch           []WatchSymbol      `json:"watch"`
	WatchUpdatedAt  *time.Time         `json:"watch_updated_at"`
	WatchError      string             `json:"watch_error,omitempty"`
	Alerts          []LiquidationAlert `json:"alerts"`
	Signals         []TradeSignal      `json:"signals"`
	AvailableFrom   *time.Time         `json:"available_from"`
	AlertTotal      int64              `json:"alert_total"`
	SignalTotal     int64              `json:"signal_total"`
	AggressiveTotal int64              `json:"aggressive_total"`
	SteadyTotal     int64              `json:"steady_total"`
	ValueAreaTotal  int64              `json:"value_area_total"`
	Notifications   NotificationStatus `json:"notifications"`
	GeneratedAt     time.Time          `json:"generated_at"`
}
