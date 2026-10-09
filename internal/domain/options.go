package domain

import "time"

const DeribitGammaMethod = "按 OI 选取 BTC / ETH 各前 80 个未到期、有持仓的 Deribit 期权合约。归一化 Gamma = (CALL Gamma 之和 − PUT Gamma 之和) / 全部样本 Gamma 绝对值之和，范围 −1～1；OI 仅用于选取样本，不乘以 OI。该指标不是美元 GEX 或做市商真实净仓。"

type OptionGammaPoint struct {
	Symbol            string    `json:"symbol"`
	Underlying        string    `json:"underlying"`
	Source            string    `json:"source"`
	Time              time.Time `json:"time"`
	Gamma             *float64  `json:"gamma"`
	CallGamma         float64   `json:"call_gamma"`
	PutGamma          float64   `json:"put_gamma"`
	Contracts         int       `json:"contracts"`
	SelectedContracts int       `json:"selected_contracts"`
	EligibleContracts int       `json:"eligible_contracts"`
	State             string    `json:"state"`
	SourceFrom        time.Time `json:"source_from"`
	SourceTo          time.Time `json:"source_to"`
	Warning           string    `json:"warning,omitempty"`
}

type OptionGammaSeries struct {
	Symbol      string             `json:"symbol"`
	Underlying  string             `json:"underlying"`
	State       string             `json:"state"`
	Latest      *OptionGammaPoint  `json:"latest"`
	Points      []OptionGammaPoint `json:"points"`
	LastAttempt *time.Time         `json:"last_attempt,omitempty"`
	LastError   string             `json:"last_error,omitempty"`
}

type OptionsView struct {
	Source         string              `json:"source"`
	Method         string              `json:"method"`
	From           time.Time           `json:"from"`
	To             time.Time           `json:"to"`
	Hours          int                 `json:"hours"`
	RefreshSeconds int                 `json:"refresh_seconds"`
	RetentionDays  int                 `json:"retention_days"`
	AvailableFrom  *time.Time          `json:"available_from"`
	Series         []OptionGammaSeries `json:"series"`
}
