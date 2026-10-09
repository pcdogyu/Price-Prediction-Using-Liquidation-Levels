package domain

import "time"

type LiquidationFilter struct {
	Symbol, Side, Field, Cursor string
	From, To                    time.Time
	Minimum                     float64
	Limit                       int
}
type LiquidationPeriod struct {
	Label    string  `json:"label"`
	LongUSD  float64 `json:"long_usd"`
	ShortUSD float64 `json:"short_usd"`
	Count    int     `json:"count"`
}
type LiquidationPage struct {
	Rows          []LiquidationEvent  `json:"rows"`
	NextCursor    string              `json:"next_cursor,omitempty"`
	AvailableFrom *time.Time          `json:"available_from"`
	Symbols       []string            `json:"symbols"`
	Periods       []LiquidationPeriod `json:"periods"`
	Coverage      string              `json:"coverage"`
}
type DepthLevel struct {
	Price       float64 `json:"price"`
	Quantity    float64 `json:"quantity"`
	NotionalUSD float64 `json:"notional_usd"`
}
type BookSnapshot struct {
	Symbol      string       `json:"symbol"`
	Time        time.Time    `json:"time"`
	Sequence    int64        `json:"sequence"`
	BucketWidth float64      `json:"bucket_width"`
	BestBid     float64      `json:"best_bid"`
	BestAsk     float64      `json:"best_ask"`
	Bids        []DepthLevel `json:"bids"`
	Asks        []DepthLevel `json:"asks"`
}
type WallEvent struct {
	ID           string     `json:"id"`
	Symbol       string     `json:"symbol"`
	Side         string     `json:"side"`
	Price        float64    `json:"price"`
	ThresholdUSD float64    `json:"threshold_usd"`
	PeakUSD      float64    `json:"peak_usd"`
	StartedAt    time.Time  `json:"started_at"`
	QualifiedAt  time.Time  `json:"qualified_at"`
	LastSeen     time.Time  `json:"last_seen"`
	EndedAt      *time.Time `json:"ended_at"`
	DurationMS   int64      `json:"duration_ms"`
	EndReason    string     `json:"end_reason,omitempty"`
}
type WallView struct {
	State           string        `json:"state"`
	Book            *BookSnapshot `json:"book"`
	GhostBids       []DepthLevel  `json:"ghost_bids"`
	GhostAsks       []DepthLevel  `json:"ghost_asks"`
	PeakBids        []DepthLevel  `json:"peak_bids"`
	PeakAsks        []DepthLevel  `json:"peak_asks"`
	Events          []WallEvent   `json:"events"`
	HalfLifeSeconds int           `json:"half_life_seconds"`
	WindowMinutes   int           `json:"window_minutes"`
}
type WallHistory struct {
	Events     []WallEvent    `json:"events"`
	Snapshots  []BookSnapshot `json:"snapshots"`
	NextCursor string         `json:"next_cursor,omitempty"`
}
type MarketMetric struct {
	Symbol           string     `json:"symbol"`
	Time             time.Time  `json:"time"`
	MarkPrice        *float64   `json:"mark_price"`
	Change24h        *float64   `json:"change_24h"`
	Volume24hUSD     *float64   `json:"volume_24h_usd"`
	High24h          *float64   `json:"high_24h"`
	Low24h           *float64   `json:"low_24h"`
	BestBid          *float64   `json:"best_bid"`
	BestAsk          *float64   `json:"best_ask"`
	OIQuantity       *float64   `json:"oi_quantity"`
	OIUSD            *float64   `json:"oi_usd"`
	FundingRate      *float64   `json:"funding_rate"`
	NextFunding      *time.Time `json:"next_funding"`
	AccountRatio     *float64   `json:"account_ratio"`
	TopPositionRatio *float64   `json:"top_position_ratio"`
	NetPositionUSD   *float64   `json:"net_position_usd"`
	Warnings         []string   `json:"warnings"`
}
type MarketPoint struct {
	Time             time.Time `json:"time"`
	OIUSD            *float64  `json:"oi_usd"`
	AccountRatio     *float64  `json:"account_ratio"`
	TopPositionRatio *float64  `json:"top_position_ratio"`
	BuyUSD           *float64  `json:"buy_usd"`
	SellUSD          *float64  `json:"sell_usd"`
	DeltaUSD         *float64  `json:"delta_usd"`
	CVDUSD           *float64  `json:"cvd_usd"`
}
type MarketWindow struct {
	Label               string   `json:"label"`
	OIDeltaUSD          *float64 `json:"oi_delta_usd"`
	NetPositionDeltaUSD *float64 `json:"net_position_delta_usd"`
	CVDDeltaUSD         *float64 `json:"cvd_delta_usd"`
	Analysis            string   `json:"analysis"`
}
type GammaLevel struct {
	Strike         float64 `json:"strike"`
	CallOI         float64 `json:"call_oi"`
	PutOI          float64 `json:"put_oi"`
	NetGEXUSD      float64 `json:"net_gex_usd"`
	AbsoluteGEXUSD float64 `json:"absolute_gex_usd"`
}
type GammaExpiry struct {
	Expiry    string  `json:"expiry"`
	Contracts int     `json:"contracts"`
	NetGEXUSD float64 `json:"net_gex_usd"`
}
type GammaView struct {
	State                 string        `json:"state"`
	Symbol                string        `json:"symbol"`
	Time                  time.Time     `json:"time"`
	SpotPrice             float64       `json:"spot_price"`
	Contracts             int           `json:"contracts"`
	ExpectedContracts     int           `json:"expected_contracts"`
	NetGEXUSD             float64       `json:"net_gex_usd"`
	AbsoluteGEXUSD        float64       `json:"absolute_gex_usd"`
	GammaWall             *float64      `json:"gamma_wall"`
	GammaFlip             *float64      `json:"gamma_flip"`
	GammaFlips            []float64     `json:"gamma_flips"`
	FlipState             string        `json:"flip_state"`
	FlipContracts         int           `json:"flip_contracts"`
	FlipExpectedContracts int           `json:"flip_expected_contracts"`
	FlipRangeLow          float64       `json:"flip_range_low"`
	FlipRangeHigh         float64       `json:"flip_range_high"`
	FlipMethod            string        `json:"flip_method"`
	Levels                []GammaLevel  `json:"levels"`
	Expiries              []GammaExpiry `json:"expiries"`
	Method                string        `json:"method"`
	Warnings              []string      `json:"warnings"`
}
type MarketInfo struct {
	Symbol  string         `json:"symbol"`
	State   string         `json:"state"`
	Range   string         `json:"range"`
	Current *MarketMetric  `json:"current"`
	Series  []MarketPoint  `json:"series"`
	Windows []MarketWindow `json:"windows"`
	Gamma   GammaView      `json:"gamma"`
}
