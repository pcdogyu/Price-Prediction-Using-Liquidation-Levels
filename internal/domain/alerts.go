package domain

import "time"

// Signal kinds separate the two order recommendations produced by the
// liquidation alert strategy. Both kinds are stored independently so a later
// backtest can score the aggressive and the steady variant separately.
const (
	SignalAggressive = "aggressive"
	SignalSteady     = "steady"
)

// Alert cycle states. A cycle starts as watching, becomes aggressive once the
// quiet period elapsed without a same side liquidation, and ends as steady once
// an opposite liquidation confirmed the move, or expired when it never arrived.
const (
	AlertStateWatching   = "watching"
	AlertStateAggressive = "aggressive"
	AlertStateSteady     = "steady"
	AlertStateExpired    = "expired"
)

const (
	// AlertTopSymbolsDefault is how many Binance USDM pairs by 24h quote volume
	// the alert strategy watches.
	AlertTopSymbolsDefault = 50
	// AlertRetentionDays keeps alerts and signals long enough for backtesting.
	AlertRetentionDays = 180
	// AlertMonitorMethod documents the live 3 minute totals and the watch list.
	AlertMonitorMethod = "监控名单为 Binance USDⓈ-M 24 小时成交额前 50 个 USDT 永续（每 10 分钟刷新，BTCUSDT/ETHUSDT 恒含）。金额为公开采样强平流在 3 分钟滑动窗口内的累计名义价值；币安每个币对每秒只推送最近一笔，因此窗口金额为下限值。预警方向为被爆仓的一侧：多单爆仓预警后按做多下单，空单爆仓预警后按做空下单。"
)

// LiquidationAlert is one alert cycle for a symbol and the liquidated position
// side. Side=long means long positions were liquidated, which the strategy
// trades as a long recommendation.
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
	AggressiveID   string     `json:"aggressive_id,omitempty"`
	AggressiveAt   *time.Time `json:"aggressive_at,omitempty"`
	SteadyID       string     `json:"steady_id,omitempty"`
	SteadyAt       *time.Time `json:"steady_at,omitempty"`
	OppositeUSD    float64    `json:"opposite_usd,omitempty"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// TradeSignal is one order recommendation. Kind distinguishes the aggressive
// signal, which only required the quiet period, from the steady signal, which
// was additionally confirmed by an opposite direction liquidation.
type TradeSignal struct {
	ID               string    `json:"id"`
	AlertID          string    `json:"alert_id"`
	Symbol           string    `json:"symbol"`
	Kind             string    `json:"kind"`
	Side             string    `json:"side"`
	LiquidationSide  string    `json:"liquidation_side"`
	CreatedAt        time.Time `json:"created_at"`
	ReferencePrice   float64   `json:"reference_price,omitempty"`
	AlertNotionalUSD float64   `json:"alert_notional_usd"`
	ThresholdUSD     float64   `json:"threshold_usd"`
	WindowMinutes    int       `json:"window_minutes"`
	// QuietElapsedSeconds is how long the liquidated side stayed quiet and only
	// applies to the aggressive signal.
	QuietElapsedSeconds int64 `json:"quiet_elapsed_seconds,omitempty"`
	FollowUps           int   `json:"follow_ups"`
	// OppositeUSD, OppositeCount and OppositeSeconds describe the confirming
	// liquidation and only apply to the steady signal.
	OppositeUSD      float64   `json:"opposite_usd,omitempty"`
	OppositeCount    int       `json:"opposite_count,omitempty"`
	OppositeSeconds  int64     `json:"opposite_elapsed_seconds,omitempty"`
	ConfirmLimitSecs int64     `json:"confirm_limit_seconds,omitempty"`
	NotifyStatus     string    `json:"notify_status,omitempty"`
	NotifyError      string    `json:"notify_error,omitempty"`
	NotifiedAt       time.Time `json:"notified_at,omitempty"`
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

// AlertThresholds describes the effective strategy parameters.
type AlertThresholds struct {
	WindowMinutes      int     `json:"window_minutes"`
	QuietMinutes       int     `json:"quiet_minutes"`
	ConfirmMinutes     int     `json:"confirm_minutes"`
	RefreshMinutes     int     `json:"refresh_minutes"`
	TopSymbols         int     `json:"top_symbols"`
	BTCUSD             float64 `json:"btc_usd"`
	ETHUSD             float64 `json:"eth_usd"`
	OtherUSD           float64 `json:"other_usd"`
	FollowUpMinimumUSD float64 `json:"follow_up_minimum_usd"`
	OppositeMinimumUSD float64 `json:"opposite_minimum_usd"`
}

// AlertPage and SignalPage are cursor paginated history pages.
type AlertPage struct {
	Rows       []LiquidationAlert `json:"rows"`
	NextCursor string             `json:"next_cursor,omitempty"`
	AlertCount int64              `json:"alert_count"`
}
type SignalPage struct {
	Rows            []TradeSignal `json:"rows"`
	NextCursor      string        `json:"next_cursor,omitempty"`
	SignalCount     int64         `json:"signal_count"`
	AggressiveCount int64         `json:"aggressive_count"`
	SteadyCount     int64         `json:"steady_count"`
}

// NotificationSettings is persisted in SQLite. The bot token is deliberately
// excluded from JSON so it can never leave the server in an API response.
type NotificationSettings struct {
	TelegramEnabled  bool      `json:"telegram_enabled"`
	TelegramToken    string    `json:"-"`
	TelegramChatID   string    `json:"telegram_chat_id"`
	NotifyAlerts     bool      `json:"notify_alerts"`
	NotifyAggressive bool      `json:"notify_aggressive"`
	NotifySteady     bool      `json:"notify_steady"`
	HasToken         bool      `json:"has_token"`
	TokenMask        string    `json:"token_mask"`
	TokenSource      string    `json:"token_source"`
	UpdatedAt        time.Time `json:"updated_at"`
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
	Notifications   NotificationStatus `json:"notifications"`
	GeneratedAt     time.Time          `json:"generated_at"`
}
