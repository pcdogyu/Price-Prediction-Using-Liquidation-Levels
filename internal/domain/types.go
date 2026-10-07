package domain

import "time"

const (
	UpperFirst = "upper_first"
	LowerFirst = "lower_first"
	Neither    = "neither"
)

type Candle struct {
	Exchange, Symbol       string
	Time                   time.Time
	Open, High, Low, Close float64
	VolumeUSD              float64
	TakerBuyUSD            float64
	OpenInterestUSD        float64
	FundingRate            float64
	LongShortRatio         float64
}

type MarketSnapshot struct {
	Exchange, Symbol                          string
	Time                                      time.Time
	MarkPrice, OpenInterestUSD, FundingRate   float64
	LongShortRatio, TakerBuyUSD, TakerSellUSD float64
	Coverage                                  float64
}

type LiquidationEvent struct {
	ID, Exchange, Symbol, PositionSide string
	EventTime, ReceivedAt              time.Time
	Price, Quantity, NotionalUSD       float64
	Coverage                           string
}

const DataSourceBinanceUSDM = "binance_usdm"

type AggregateTrade struct {
	ID        int64     `json:"id"`
	Symbol    string    `json:"symbol"`
	Time      time.Time `json:"time"`
	Price     float64   `json:"price"`
	Quantity  float64   `json:"quantity"`
	PriceText string    `json:"-"`
}

type VolumeProfileBin struct {
	PriceLow    float64 `json:"price_low"`
	PriceHigh   float64 `json:"price_high"`
	VolumeUSD   float64 `json:"volume_usd"`
	VolumeShare float64 `json:"volume_percent"`
	InValueArea bool    `json:"in_value_area"`
}

type VolumeProfile struct {
	Symbol            string             `json:"symbol"`
	Source            string             `json:"source"`
	State             string             `json:"state"`
	SessionStart      time.Time          `json:"session_start"`
	SessionEnd        time.Time          `json:"session_end"`
	NextReset         time.Time          `json:"next_reset"`
	ResetKind         string             `json:"reset_kind"`
	ValueAreaFraction float64            `json:"value_area_fraction"`
	VAL               *float64           `json:"val,omitempty"`
	VAH               *float64           `json:"vah,omitempty"`
	TotalVolumeUSD    float64            `json:"total_volume_usd"`
	Bins              []VolumeProfileBin `json:"bins"`
	DataThrough       time.Time          `json:"data_through,omitempty"`
	BackfillProgress  float64            `json:"backfill_progress"`
	UpdatedAt         time.Time          `json:"updated_at"`
}

type VolumeProfileSnapshot struct {
	Symbol         string
	Time           time.Time
	SessionStart   time.Time
	VAL            float64
	VAH            float64
	TotalVolumeUSD float64
	Complete       bool
}

type MapBin struct {
	Price    float64 `json:"price"`
	LongUSD  float64 `json:"long_usd"`
	ShortUSD float64 `json:"short_usd"`
	TotalUSD float64 `json:"total_usd"`
}

type Wall struct {
	Side         string  `json:"side"`
	Price        float64 `json:"price"`
	IntensityUSD float64 `json:"intensity_usd"`
	DistanceATR  float64 `json:"distance_atr"`
	Score        float64 `json:"score"`
}

type CandlePattern struct {
	Name string `json:"name"`
	Bias string `json:"bias"`
}

type MarketCandle struct {
	Time          time.Time       `json:"time"`
	Open          float64         `json:"open"`
	High          float64         `json:"high"`
	Low           float64         `json:"low"`
	Close         float64         `json:"close"`
	VolumeUSD     float64         `json:"volume_usd"`
	ExchangeCount int             `json:"exchange_count"`
	Complete      bool            `json:"complete"`
	Patterns      []CandlePattern `json:"patterns,omitempty"`
}

type PriceSummary struct {
	LastPrice     float64   `json:"last_price"`
	Change24h     float64   `json:"change_24h"`
	ChangePct24h  float64   `json:"change_pct_24h"`
	High24h       float64   `json:"high_24h"`
	Low24h        float64   `json:"low_24h"`
	Volume24hUSD  float64   `json:"volume_24h_usd"`
	ExchangeCount int       `json:"exchange_count"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type DirectionalSignal struct {
	Time         time.Time `json:"time"`
	CandleTime   time.Time `json:"candle_time"`
	Side         string    `json:"side"`
	Probability  float64   `json:"probability"`
	Price        float64   `json:"price"`
	ModelVersion string    `json:"model_version,omitempty"`
}

type MarketView struct {
	Symbol           string              `json:"symbol"`
	Interval         string              `json:"interval"`
	Source           string              `json:"source"`
	Summary          PriceSummary        `json:"summary"`
	Candles          []MarketCandle      `json:"candles"`
	HasMore          bool                `json:"has_more"`
	NextBefore       *time.Time          `json:"next_before,omitempty"`
	AvailableFrom    time.Time           `json:"available_from,omitempty"`
	BackfillComplete bool                `json:"backfill_complete"`
	ModelSignals     []DirectionalSignal `json:"model_signals"`
}

type TriggerInfo struct {
	Side            string     `json:"side"`
	Status          string     `json:"status"`
	TargetPrice     float64    `json:"target_price"`
	CurrentPrice    float64    `json:"current_price"`
	DistancePrice   float64    `json:"distance_price"`
	DistancePercent float64    `json:"distance_percent"`
	DistanceATR     float64    `json:"distance_atr"`
	Probability     float64    `json:"probability"`
	PredictedAt     time.Time  `json:"predicted_at"`
	ExpiresAt       time.Time  `json:"expires_at"`
	TriggeredAt     *time.Time `json:"triggered_at,omitempty"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

type FeatureVector struct {
	Time    time.Time       `json:"time"`
	Symbol  string          `json:"symbol"`
	Names   []string        `json:"names"`
	Values  []float64       `json:"values"`
	Missing map[string]bool `json:"missing"`
}

type Label struct {
	Class string    `json:"class"`
	Time  time.Time `json:"time"`
}

type ModelArtifact struct {
	Version      string      `json:"version"`
	DataSource   string      `json:"data_source"`
	FeatureNames []string    `json:"feature_names"`
	Means        []float64   `json:"means"`
	StdDevs      []float64   `json:"std_devs"`
	Weights      [][]float64 `json:"weights"`
	Biases       []float64   `json:"biases"`
	Temperature  float64     `json:"temperature"`
	Lambda       float64     `json:"lambda"`
	TrainedAt    time.Time   `json:"trained_at"`
	Experimental bool        `json:"experimental"`
}

type Prediction struct {
	Symbol         string             `json:"symbol"`
	DataSource     string             `json:"data_source"`
	Time           time.Time          `json:"time"`
	HorizonMinutes int                `json:"horizon_minutes"`
	State          string             `json:"state"`
	Reason         string             `json:"reason,omitempty"`
	MarkPrice      float64            `json:"mark_price"`
	ATR            float64            `json:"atr"`
	UpperWall      *Wall              `json:"upper_wall,omitempty"`
	LowerWall      *Wall              `json:"lower_wall,omitempty"`
	Probabilities  map[string]float64 `json:"probabilities,omitempty"`
	ModelVersion   string             `json:"model_version,omitempty"`
	Experimental   bool               `json:"experimental"`
	DataAgeSeconds float64            `json:"data_age_seconds"`
	SourceCoverage map[string]float64 `json:"source_coverage"`
	Contributions  map[string]float64 `json:"contributions,omitempty"`
	LeadingClass   string             `json:"leading_class,omitempty"`
	UpperTrigger   *TriggerInfo       `json:"upper_trigger,omitempty"`
	LowerTrigger   *TriggerInfo       `json:"lower_trigger,omitempty"`
	TriggerOrder   string             `json:"trigger_order,omitempty"`
}

type BacktestReport struct {
	Symbol            string             `json:"symbol"`
	DataSource        string             `json:"data_source"`
	GeneratedAt       time.Time          `json:"generated_at"`
	Samples           int                `json:"samples"`
	LogLoss           float64            `json:"log_loss"`
	BrierScore        float64            `json:"brier_score"`
	ECE               float64            `json:"ece"`
	BaselineLogLoss   float64            `json:"baseline_log_loss"`
	Baselines         map[string]float64 `json:"baselines"`
	Improvement       float64            `json:"improvement"`
	PromotionEligible bool               `json:"promotion_eligible"`
	Note              string             `json:"note"`
}
