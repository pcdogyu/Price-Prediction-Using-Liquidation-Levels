package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

const (
	EnvDemo = "demo"
	EnvLive = "live"

	MarginIsolated = "isolated"
	MarginCross    = "cross"

	PositionNet       = "net"
	PositionLongShort = "long_short"

	MarketTurnoverScopeTop50  = "top50"
	MarketTurnoverScopeTop100 = "top100"
	MarketTurnoverScopeTop200 = "top200"
	MarketTurnoverScopeTop500 = "top500"
	MarketTurnoverScopeAll    = "all"

	DefaultMarketTurnoverScope = MarketTurnoverScopeTop100
)

type Config struct {
	Server       ServerConfig            `json:"server"`
	DataFile     string                  `json:"data_file"`
	DatabaseFile string                  `json:"database_file"`
	Trading      TradingConfig           `json:"trading"`
	Symbols      map[string]SymbolConfig `json:"symbols"`
	UI           UIConfig                `json:"ui"`
}

type ServerConfig struct {
	Addr string `json:"addr"`
}

type TradingConfig struct {
	Env                       string                       `json:"env"`
	AllowLiveTrading          bool                         `json:"allow_live_trading"`
	BaseURL                   string                       `json:"base_url"`
	BinanceBaseURL            string                       `json:"binance_base_url"`
	BinanceDemoBaseURL        string                       `json:"binance_demo_base_url"`
	GateBaseURL               string                       `json:"gate_base_url"`
	GateDemoBaseURL           string                       `json:"gate_demo_base_url"`
	DefaultMarginMode         string                       `json:"default_margin_mode"`
	PositionMode              string                       `json:"position_mode"`
	MarketTurnoverScope       string                       `json:"market_turnover_scope"`
	SignalTTLSeconds          int                          `json:"signal_ttl_seconds"`
	IgnoredCoinpair           string                       `json:"ignored_coinpair"`
	IgnoredCoinpairs          []string                     `json:"ignored_coinpairs"`
	CoinpairWhitelist         []string                     `json:"coinpair_whitelist"`
	OrderAmountUSDT           float64                      `json:"order_amount_usdt"`
	Leverage                  int                          `json:"leverage"`
	OrderType                 string                       `json:"order_type"`
	RiskType                  string                       `json:"risk_type"`
	TakeProfitPct             float64                      `json:"take_profit_pct"`
	StopLossPct               float64                      `json:"stop_loss_pct"`
	TrailingPct               float64                      `json:"trailing_pct"`
	LongLimitPriceMultiplier  float64                      `json:"long_limit_price_multiplier"`
	ShortLimitPriceMultiplier float64                      `json:"short_limit_price_multiplier"`
	LayeredOrder              trading.LayeredOrderSettings `json:"layered_order"`
	LossCooldown              LossCooldownConfig           `json:"loss_cooldown"`
	FillMonitor               FillMonitorConfig            `json:"fill_monitor"`
	AutoReentry               AutoReentryConfig            `json:"auto_reentry"`
	PositionMonitor           PositionMonitorConfig        `json:"position_monitor"`
}

// LossCooldownConfig controls the global block applied to a coinpair after a
// confirmed losing exit. It is separate from auto_reentry, whose cooldown only
// governs the reentry lifecycle.
type LossCooldownConfig struct {
	Enabled bool `json:"enabled"`
	Hours   int  `json:"hours"`
}

type FillMonitorConfig struct {
	Enabled             bool     `json:"enabled"`
	PollIntervalSeconds int      `json:"poll_interval_seconds"`
	LookbackHours       int      `json:"lookback_hours"`
	Exchanges           []string `json:"exchange"`
}

type AutoReentryConfig struct {
	Enabled                bool    `json:"enabled"`
	MaxReentries           int     `json:"max_reentries"`
	ReentryAmountPct       float64 `json:"reentry_amount_pct"`
	CooldownAfterStopHours int     `json:"cooldown_after_stop_hours"`
	OnlyBotOrders          bool    `json:"only_bot_orders"`
}

type PositionMonitorConfig struct {
	OKXEnabled          bool    `json:"okx_enabled"`
	BinanceEnabled      bool    `json:"binance_enabled"`
	PollIntervalSeconds int     `json:"poll_interval_seconds"`
	TakeProfitPct       float64 `json:"take_profit_pct"`
	StopLossPct         float64 `json:"stop_loss_pct"`
}

type SymbolConfig struct {
	Coinpair string  `json:"coinpair"`
	InstID   string  `json:"inst_id"`
	CtVal    float64 `json:"ct_val"`
	TickSz   float64 `json:"tick_sz,omitempty"`
	LotSz    float64 `json:"lot_sz"`
	MinSz    float64 `json:"min_sz"`
}

type UIConfig struct {
	DefaultTab   string             `json:"default_tab"`
	MenuItems    []MenuItemConfig   `json:"menu_items"`
	TableColumns TableColumnsConfig `json:"table_columns"`
}

type TableColumnsConfig struct {
	Positions     []string `json:"positions"`
	PendingOrders []string `json:"pending_orders"`
	Symbols       []string `json:"symbols"`
}

type MenuItemConfig struct {
	Tab    string `json:"tab"`
	Hidden bool   `json:"hidden"`
	Label  string `json:"label,omitempty"`
}

const (
	DefaultHomeTab  = "dashboard"
	MenuSettingsTab = "menuSettings"
)

var DefaultMenuTabs = []string{
	"dashboard",
	"positions",
	"analysis",
	"symbols",
	"config",
	"apiKeys",
	"orderSettings",
	"template",
	"orders",
	"tradeMonitor",
	MenuSettingsTab,
	"upgrade",
}

var DefaultPositionTableColumns = []string{
	"exchange",
	"symbol",
	"side",
	"strategy",
	"entry_adx",
	"size",
	"avg_price",
	"margin",
	"leverage",
	"position_amount",
	"mark_price",
	"upl",
	"return_rate",
	"entry_time",
	"holding_time",
	"actions",
}

var DefaultPendingOrderTableColumns = []string{
	"exchange",
	"time",
	"symbol",
	"side",
	"position_side",
	"type",
	"price",
	"mid_price",
	"size",
	"margin",
	"filled",
	"pending_age",
	"state",
	"actions",
}

var DefaultSymbolTableColumns = []string{
	"env",
	"symbol",
	"configured",
	"state",
	"base_quote",
	"settle",
	"ct_val",
	"min_size",
	"lot_size",
	"leverage",
	"turnover",
}

var defaultMenuLabels = map[string]string{
	"dashboard":     "资金",
	"positions":     "持仓",
	"analysis":      "订单分析",
	"symbols":       "币对配置",
	"config":        "订单配置",
	"apiKeys":       "API Key",
	"orderSettings": "下单设置",
	"template":      "告警模板",
	"orders":        "订单",
	"tradeMonitor":  "成交监听",
	MenuSettingsTab: "菜单设置",
	"upgrade":       "升级",
}

func Default() Config {
	return Config{
		Server:       ServerConfig{Addr: ":8080"},
		DataFile:     "data/orders.json",
		DatabaseFile: "data/tvbot.db",
		Trading: TradingConfig{
			Env:                       EnvDemo,
			AllowLiveTrading:          false,
			BaseURL:                   "https://www.okx.com",
			BinanceBaseURL:            "https://fapi.binance.com",
			BinanceDemoBaseURL:        "https://demo-fapi.binance.com",
			GateBaseURL:               "https://api.gateio.ws",
			GateDemoBaseURL:           "https://api-testnet.gateapi.io",
			DefaultMarginMode:         MarginIsolated,
			PositionMode:              PositionNet,
			MarketTurnoverScope:       DefaultMarketTurnoverScope,
			SignalTTLSeconds:          120,
			OrderAmountUSDT:           100,
			Leverage:                  5,
			OrderType:                 string(trading.OrderTypeMarket),
			RiskType:                  string(trading.RiskTPSL),
			TakeProfitPct:             2,
			StopLossPct:               1,
			TrailingPct:               1,
			LongLimitPriceMultiplier:  0.997,
			ShortLimitPriceMultiplier: 1.003,
			LayeredOrder:              trading.DefaultLayeredOrderSettings(),
			LossCooldown: LossCooldownConfig{
				Enabled: true,
				Hours:   24,
			},
			FillMonitor: FillMonitorConfig{
				Enabled:             true,
				PollIntervalSeconds: 20,
				LookbackHours:       72,
				Exchanges:           []string{trading.ExchangeBinance},
			},
			AutoReentry: AutoReentryConfig{
				Enabled:                false,
				MaxReentries:           1,
				ReentryAmountPct:       50,
				CooldownAfterStopHours: 24,
				OnlyBotOrders:          true,
			},
			PositionMonitor: PositionMonitorConfig{
				OKXEnabled:          false,
				BinanceEnabled:      false,
				PollIntervalSeconds: 300,
				TakeProfitPct:       5,
				StopLossPct:         8,
			},
		},
		Symbols: map[string]SymbolConfig{
			"BTC": {
				Coinpair: "BTC",
				InstID:   "BTC-USDT-SWAP",
				CtVal:    0.01,
				LotSz:    0.01,
				MinSz:    0.01,
			},
			"ETH": {
				Coinpair: "ETH",
				InstID:   "ETH-USDT-SWAP",
				CtVal:    0.1,
				LotSz:    0.01,
				MinSz:    0.01,
			},
		},
		UI: UIConfig{DefaultTab: DefaultHomeTab, MenuItems: defaultMenuItems(), TableColumns: defaultTableColumns()},
	}
}

func Load(path string) (Config, error) {
	cfg := Default()
	if strings.TrimSpace(path) == "" {
		cfg.Normalize()
		return cfg, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			cfg.Normalize()
			return cfg, nil
		}
		return Config{}, err
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return Config{}, err
	}
	cfg.Normalize()
	return cfg, nil
}

func Save(path string, cfg Config) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("config path is required")
	}
	cfg.Normalize()
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil && filepath.Dir(path) != "." {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (c *Config) Normalize() {
	if strings.TrimSpace(c.Server.Addr) == "" {
		c.Server.Addr = ":8080"
	}
	if strings.TrimSpace(c.DataFile) == "" {
		c.DataFile = "data/orders.json"
	}
	if strings.TrimSpace(c.DatabaseFile) == "" {
		c.DatabaseFile = "data/tvbot.db"
	}
	c.Trading.Env = strings.ToLower(strings.TrimSpace(c.Trading.Env))
	if c.Trading.Env == "" {
		c.Trading.Env = EnvDemo
	}
	c.Trading.BaseURL = strings.TrimRight(strings.TrimSpace(c.Trading.BaseURL), "/")
	if c.Trading.BaseURL == "" {
		c.Trading.BaseURL = "https://www.okx.com"
	}
	c.Trading.BinanceBaseURL = strings.TrimRight(strings.TrimSpace(c.Trading.BinanceBaseURL), "/")
	if c.Trading.BinanceBaseURL == "" {
		c.Trading.BinanceBaseURL = "https://fapi.binance.com"
	}
	c.Trading.BinanceDemoBaseURL = strings.TrimRight(strings.TrimSpace(c.Trading.BinanceDemoBaseURL), "/")
	if c.Trading.BinanceDemoBaseURL == "" {
		c.Trading.BinanceDemoBaseURL = "https://demo-fapi.binance.com"
	}
	c.Trading.GateBaseURL = strings.TrimRight(strings.TrimSpace(c.Trading.GateBaseURL), "/")
	if c.Trading.GateBaseURL == "" {
		c.Trading.GateBaseURL = "https://api.gateio.ws"
	}
	c.Trading.GateDemoBaseURL = strings.TrimRight(strings.TrimSpace(c.Trading.GateDemoBaseURL), "/")
	if c.Trading.GateDemoBaseURL == "" {
		c.Trading.GateDemoBaseURL = "https://api-testnet.gateapi.io"
	}
	c.Trading.DefaultMarginMode = strings.ToLower(strings.TrimSpace(c.Trading.DefaultMarginMode))
	if c.Trading.DefaultMarginMode == "" {
		c.Trading.DefaultMarginMode = MarginIsolated
	}
	c.Trading.PositionMode = strings.ToLower(strings.TrimSpace(c.Trading.PositionMode))
	if c.Trading.PositionMode == "" {
		c.Trading.PositionMode = PositionNet
	}
	c.Trading.MarketTurnoverScope = strings.ToLower(strings.TrimSpace(c.Trading.MarketTurnoverScope))
	if c.Trading.MarketTurnoverScope == "" {
		c.Trading.MarketTurnoverScope = DefaultMarketTurnoverScope
	}
	if c.Trading.SignalTTLSeconds <= 0 {
		c.Trading.SignalTTLSeconds = 120
	}
	c.Trading.IgnoredCoinpairs = normalizeIgnoredCoinpairs(c.Trading.IgnoredCoinpairs, c.Trading.IgnoredCoinpair)
	if len(c.Trading.IgnoredCoinpairs) > 0 {
		c.Trading.IgnoredCoinpair = c.Trading.IgnoredCoinpairs[0]
	} else {
		c.Trading.IgnoredCoinpair = ""
	}
	c.Trading.CoinpairWhitelist = normalizeCoinpairKeywords(c.Trading.CoinpairWhitelist)
	if c.Trading.OrderAmountUSDT <= 0 {
		c.Trading.OrderAmountUSDT = 100
	}
	if c.Trading.Leverage <= 0 {
		c.Trading.Leverage = 5
	}
	c.Trading.OrderType = strings.ToLower(strings.TrimSpace(c.Trading.OrderType))
	if c.Trading.OrderType == "" {
		c.Trading.OrderType = string(trading.OrderTypeMarket)
	}
	c.Trading.RiskType = strings.ToLower(strings.TrimSpace(c.Trading.RiskType))
	if c.Trading.RiskType == "" {
		c.Trading.RiskType = string(trading.RiskTPSL)
	}
	if c.Trading.TakeProfitPct <= 0 {
		c.Trading.TakeProfitPct = 2
	}
	if c.Trading.StopLossPct <= 0 {
		c.Trading.StopLossPct = 1
	}
	if c.Trading.TrailingPct <= 0 {
		c.Trading.TrailingPct = 1
	}
	if c.Trading.LongLimitPriceMultiplier <= 0 {
		c.Trading.LongLimitPriceMultiplier = 0.997
	}
	if c.Trading.ShortLimitPriceMultiplier <= 0 {
		c.Trading.ShortLimitPriceMultiplier = 1.003
	}
	c.Trading.LayeredOrder = c.Trading.LayeredOrder.Normalize()
	if c.Trading.LossCooldown.Hours <= 0 {
		c.Trading.LossCooldown.Hours = 24
	}
	if c.Trading.FillMonitor.PollIntervalSeconds <= 0 {
		c.Trading.FillMonitor.PollIntervalSeconds = 20
	}
	if c.Trading.FillMonitor.LookbackHours <= 0 {
		c.Trading.FillMonitor.LookbackHours = 72
	}
	c.Trading.FillMonitor.Exchanges = normalizeFillMonitorExchanges(c.Trading.FillMonitor.Exchanges)
	if c.Trading.AutoReentry.MaxReentries <= 0 {
		c.Trading.AutoReentry.MaxReentries = 1
	}
	if c.Trading.AutoReentry.ReentryAmountPct <= 0 {
		c.Trading.AutoReentry.ReentryAmountPct = 50
	}
	if c.Trading.AutoReentry.CooldownAfterStopHours <= 0 {
		c.Trading.AutoReentry.CooldownAfterStopHours = 24
	}
	if !c.Trading.AutoReentry.OnlyBotOrders {
		c.Trading.AutoReentry.OnlyBotOrders = true
	}
	if c.Trading.PositionMonitor.PollIntervalSeconds <= 0 {
		c.Trading.PositionMonitor.PollIntervalSeconds = 300
	}
	if c.Trading.PositionMonitor.TakeProfitPct <= 0 {
		c.Trading.PositionMonitor.TakeProfitPct = 5
	}
	if c.Trading.PositionMonitor.StopLossPct <= 0 {
		c.Trading.PositionMonitor.StopLossPct = 8
	}
	if c.Symbols == nil {
		c.Symbols = map[string]SymbolConfig{}
	}
	normalized := make(map[string]SymbolConfig, len(c.Symbols))
	for key, sym := range c.Symbols {
		coin := strings.ToUpper(strings.TrimSpace(sym.Coinpair))
		if coin == "" {
			coin = strings.ToUpper(strings.TrimSpace(key))
		}
		sym.Coinpair = coin
		sym.InstID = strings.ToUpper(strings.TrimSpace(sym.InstID))
		normalized[coin] = sym
	}
	c.Symbols = normalized
	c.UI.DefaultTab = normalizeDefaultMenuTab(c.UI.DefaultTab)
	c.UI.MenuItems = normalizeMenuItems(c.UI.MenuItems)
	c.UI.TableColumns = normalizeTableColumns(c.UI.TableColumns)
}

func (c Config) Validate() error {
	switch c.Trading.Env {
	case EnvDemo, EnvLive:
	default:
		return fmt.Errorf("unsupported trading env %q", c.Trading.Env)
	}
	switch c.Trading.DefaultMarginMode {
	case MarginIsolated, MarginCross:
	default:
		return fmt.Errorf("unsupported margin mode %q", c.Trading.DefaultMarginMode)
	}
	switch c.Trading.PositionMode {
	case PositionNet, PositionLongShort:
	default:
		return fmt.Errorf("unsupported position mode %q", c.Trading.PositionMode)
	}
	if !ValidMarketTurnoverScope(c.Trading.MarketTurnoverScope) {
		return fmt.Errorf("unsupported market turnover scope %q", c.Trading.MarketTurnoverScope)
	}
	switch trading.RiskType(c.Trading.RiskType) {
	case trading.RiskNone, trading.RiskTPSL, trading.RiskTrailing:
	default:
		return fmt.Errorf("unsupported risk type %q", c.Trading.RiskType)
	}
	if c.Trading.OrderAmountUSDT <= 0 {
		return errors.New("order_amount_usdt must be positive")
	}
	if c.Trading.Leverage <= 0 {
		return errors.New("leverage must be positive")
	}
	switch trading.OrderType(c.Trading.OrderType) {
	case trading.OrderTypeMarket, trading.OrderTypeLimit, trading.OrderTypeLayered:
	default:
		return fmt.Errorf("unsupported order_type %q", c.Trading.OrderType)
	}
	if trading.RiskType(c.Trading.RiskType) == trading.RiskTPSL && (c.Trading.TakeProfitPct <= 0 || c.Trading.StopLossPct <= 0) {
		return errors.New("take_profit_pct and stop_loss_pct must be positive for tp_sl")
	}
	if trading.RiskType(c.Trading.RiskType) == trading.RiskTrailing && c.Trading.TrailingPct <= 0 {
		return errors.New("trailing_pct must be positive for trailing")
	}
	if c.Trading.LongLimitPriceMultiplier <= 0 || c.Trading.ShortLimitPriceMultiplier <= 0 {
		return errors.New("limit price multipliers must be positive")
	}
	if err := c.Trading.LayeredOrder.Validate(); err != nil {
		return err
	}
	if c.Trading.LossCooldown.Hours < 1 || c.Trading.LossCooldown.Hours > 8760 {
		return errors.New("loss_cooldown.hours must be between 1 and 8760")
	}
	if c.Trading.FillMonitor.PollIntervalSeconds <= 0 {
		return errors.New("fill_monitor.poll_interval_seconds must be positive")
	}
	if c.Trading.FillMonitor.LookbackHours <= 0 {
		return errors.New("fill_monitor.lookback_hours must be positive")
	}
	for _, exchange := range c.Trading.FillMonitor.Exchanges {
		if trading.NormalizeExchange(exchange) != trading.ExchangeBinance {
			return fmt.Errorf("fill_monitor.exchange only supports %q in this version", trading.ExchangeBinance)
		}
	}
	if c.Trading.AutoReentry.MaxReentries <= 0 {
		return errors.New("auto_reentry.max_reentries must be positive")
	}
	if c.Trading.AutoReentry.ReentryAmountPct <= 0 || c.Trading.AutoReentry.ReentryAmountPct > 100 {
		return errors.New("auto_reentry.reentry_amount_pct must be greater than 0 and at most 100")
	}
	if c.Trading.AutoReentry.CooldownAfterStopHours <= 0 {
		return errors.New("auto_reentry.cooldown_after_stop_hours must be positive")
	}
	if c.Trading.PositionMonitor.PollIntervalSeconds <= 0 {
		return errors.New("position_monitor.poll_interval_seconds must be positive")
	}
	if c.Trading.PositionMonitor.TakeProfitPct <= 0 {
		return errors.New("position_monitor.take_profit_pct must be positive")
	}
	if c.Trading.PositionMonitor.StopLossPct <= 0 {
		return errors.New("position_monitor.stop_loss_pct must be positive")
	}
	for key, sym := range c.Symbols {
		if sym.Coinpair == "" || sym.InstID == "" {
			return fmt.Errorf("symbol %q requires coinpair and inst_id", key)
		}
		if sym.CtVal <= 0 || sym.LotSz <= 0 || sym.MinSz <= 0 {
			return fmt.Errorf("symbol %q requires positive ct_val, lot_sz and min_sz", key)
		}
	}
	if !hasVisibleMenuSettings(c.UI.MenuItems) {
		return errors.New("menuSettings must be visible")
	}
	return nil
}

func ValidMarketTurnoverScope(scope string) bool {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case MarketTurnoverScopeTop50, MarketTurnoverScopeTop100, MarketTurnoverScopeTop200, MarketTurnoverScopeTop500, MarketTurnoverScopeAll:
		return true
	default:
		return false
	}
}

func MarketTurnoverScopeLimit(scope string) int {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case MarketTurnoverScopeTop50:
		return 50
	case MarketTurnoverScopeTop200:
		return 200
	case MarketTurnoverScopeTop500:
		return 500
	case MarketTurnoverScopeAll:
		return 0
	default:
		return 100
	}
}

func normalizeFillMonitorExchanges(exchanges []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(exchanges))
	for _, exchange := range exchanges {
		if strings.TrimSpace(exchange) == "" {
			continue
		}
		normalized := trading.NormalizeExchange(exchange)
		if normalized == "" || seen[normalized] {
			continue
		}
		seen[normalized] = true
		out = append(out, normalized)
	}
	if len(out) == 0 {
		out = append(out, trading.ExchangeBinance)
	}
	return out
}

func defaultMenuItems() []MenuItemConfig {
	items := make([]MenuItemConfig, 0, len(DefaultMenuTabs))
	for _, tab := range DefaultMenuTabs {
		items = append(items, MenuItemConfig{Tab: tab, Label: defaultMenuLabel(tab)})
	}
	return items
}

func normalizeMenuItems(items []MenuItemConfig) []MenuItemConfig {
	known := make(map[string]bool, len(DefaultMenuTabs))
	for _, tab := range DefaultMenuTabs {
		known[tab] = true
	}
	seen := make(map[string]bool, len(DefaultMenuTabs))
	normalized := make([]MenuItemConfig, 0, len(DefaultMenuTabs))
	for _, item := range items {
		tab := strings.TrimSpace(item.Tab)
		if !known[tab] || seen[tab] {
			continue
		}
		if tab == MenuSettingsTab {
			item.Hidden = false
		}
		label := strings.TrimSpace(item.Label)
		if tab == "dashboard" && label == "总览" {
			label = defaultMenuLabel(tab)
		}
		if label == "" {
			label = defaultMenuLabel(tab)
		} else if owner := defaultMenuLabelOwner(label); owner != "" && owner != tab {
			label = defaultMenuLabel(tab)
		}
		normalized = append(normalized, MenuItemConfig{Tab: tab, Hidden: item.Hidden, Label: label})
		seen[tab] = true
	}
	for _, tab := range DefaultMenuTabs {
		if seen[tab] {
			continue
		}
		normalized = append(normalized, MenuItemConfig{Tab: tab, Label: defaultMenuLabel(tab)})
	}
	return normalized
}

func normalizeDefaultMenuTab(tab string) string {
	tab = strings.TrimSpace(tab)
	if menuTabKnown(tab) {
		return tab
	}
	return DefaultHomeTab
}

func menuTabKnown(tab string) bool {
	for _, known := range DefaultMenuTabs {
		if tab == known {
			return true
		}
	}
	return false
}

func defaultMenuLabel(tab string) string {
	if label := defaultMenuLabels[tab]; label != "" {
		return label
	}
	return tab
}

func defaultMenuLabelOwner(label string) string {
	label = strings.TrimSpace(label)
	for tab, defaultLabel := range defaultMenuLabels {
		if label == defaultLabel {
			return tab
		}
	}
	return ""
}

func hasVisibleMenuSettings(items []MenuItemConfig) bool {
	for _, item := range items {
		if item.Tab == MenuSettingsTab && !item.Hidden {
			return true
		}
	}
	return false
}

func defaultTableColumns() TableColumnsConfig {
	return TableColumnsConfig{
		Positions:     cloneStrings(DefaultPositionTableColumns),
		PendingOrders: cloneStrings(DefaultPendingOrderTableColumns),
		Symbols:       cloneStrings(DefaultSymbolTableColumns),
	}
}

func normalizeTableColumns(columns TableColumnsConfig) TableColumnsConfig {
	return TableColumnsConfig{
		Positions:     normalizeColumnOrder(columns.Positions, DefaultPositionTableColumns),
		PendingOrders: normalizePendingOrderColumnOrder(columns.PendingOrders),
		Symbols:       normalizeColumnOrder(columns.Symbols, DefaultSymbolTableColumns),
	}
}

func normalizePendingOrderColumnOrder(columns []string) []string {
	normalized := normalizeColumnOrder(columns, DefaultPendingOrderTableColumns)
	if columnOrderContains(columns, "pending_age") {
		return normalized
	}
	return moveColumnBefore(normalized, "pending_age", "state")
}

func normalizeColumnOrder(columns, defaults []string) []string {
	known := make(map[string]bool, len(defaults))
	for _, col := range defaults {
		known[col] = true
	}
	seen := make(map[string]bool, len(defaults))
	out := make([]string, 0, len(defaults))
	for _, col := range columns {
		col = strings.TrimSpace(col)
		if !known[col] || seen[col] {
			continue
		}
		out = append(out, col)
		seen[col] = true
	}
	for _, col := range defaults {
		if !seen[col] {
			out = append(out, col)
		}
	}
	return out
}

func columnOrderContains(columns []string, want string) bool {
	for _, col := range columns {
		if strings.TrimSpace(col) == want {
			return true
		}
	}
	return false
}

func moveColumnBefore(columns []string, col, before string) []string {
	colIdx := -1
	beforeIdx := -1
	for i, item := range columns {
		switch item {
		case col:
			colIdx = i
		case before:
			beforeIdx = i
		}
	}
	if colIdx < 0 || beforeIdx < 0 || colIdx == beforeIdx-1 {
		return columns
	}
	out := make([]string, 0, len(columns))
	for i, item := range columns {
		if i != colIdx {
			out = append(out, item)
		}
	}
	beforeIdx = -1
	for i, item := range out {
		if item == before {
			beforeIdx = i
			break
		}
	}
	if beforeIdx < 0 {
		return out
	}
	out = append(out[:beforeIdx], append([]string{col}, out[beforeIdx:]...)...)
	return out
}

func cloneStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}

func normalizeIgnoredCoinpairs(values []string, legacy string) []string {
	candidates := make([]string, 0, len(values)+1)
	candidates = append(candidates, values...)
	if strings.TrimSpace(legacy) != "" {
		candidates = append(candidates, legacy)
	}
	out := make([]string, 0, len(candidates))
	seen := make(map[string]bool, len(candidates))
	for _, value := range candidates {
		value = strings.ToUpper(strings.TrimSpace(value))
		key := ignoredCoinpairKey(value)
		if value == "" || key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, value)
	}
	return out
}

func normalizeCoinpairKeywords(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		value = strings.ToUpper(strings.TrimSpace(value))
		key := ignoredCoinpairKey(value)
		if value == "" || key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, value)
	}
	return out
}

func ignoredCoinpairKey(value string) string {
	var normalized strings.Builder
	for _, r := range strings.ToUpper(strings.TrimSpace(value)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			normalized.WriteRune(r)
		}
	}
	return normalized.String()
}

func (c Config) Symbol(coinpair string) (SymbolConfig, bool) {
	sym, ok := c.Symbols[strings.ToUpper(strings.TrimSpace(coinpair))]
	return sym, ok
}

func (c Config) SymbolMeta(coinpair string) (trading.SymbolInfo, bool) {
	sym, ok := c.Symbol(coinpair)
	if !ok {
		return trading.SymbolInfo{}, false
	}
	return trading.SymbolInfo{
		Coinpair: sym.Coinpair,
		InstID:   sym.InstID,
		CtVal:    sym.CtVal,
		TickSz:   sym.TickSz,
		LotSz:    sym.LotSz,
		MinSz:    sym.MinSz,
	}, true
}

func (c Config) DemoTradingHeaderEnabled() bool {
	return c.Trading.Env != EnvLive
}

func (c Config) OKXBaseURL() string {
	return c.Trading.BaseURL
}

func (c Config) BinanceBaseURL() string {
	if c.Trading.Env == EnvLive {
		return c.Trading.BinanceBaseURL
	}
	return c.Trading.BinanceDemoBaseURL
}

func (c Config) GateBaseURL() string {
	if c.Trading.Env == EnvLive {
		return c.Trading.GateBaseURL
	}
	return c.Trading.GateDemoBaseURL
}

func (c Config) MarginMode() string {
	return c.Trading.DefaultMarginMode
}

func (c Config) PositionMode() string {
	return c.Trading.PositionMode
}

func (c Config) OrderSettings() trading.OrderSettings {
	risk := trading.Risk{Type: trading.RiskType(c.Trading.RiskType)}
	switch risk.Type {
	case trading.RiskTPSL:
		tp := trading.NewFlexibleFloat(c.Trading.TakeProfitPct)
		sl := trading.NewFlexibleFloat(c.Trading.StopLossPct)
		risk.TPPct = &tp
		risk.SLPct = &sl
	case trading.RiskTrailing:
		trailing := trading.NewFlexibleFloat(c.Trading.TrailingPct)
		risk.TrailingPct = &trailing
	}
	return trading.OrderSettings{
		Amount:                    trading.NewFlexibleFloat(c.Trading.OrderAmountUSDT),
		Leverage:                  c.Trading.Leverage,
		OrderType:                 trading.OrderType(c.Trading.OrderType),
		Risk:                      risk,
		LongLimitPriceMultiplier:  c.Trading.LongLimitPriceMultiplier,
		ShortLimitPriceMultiplier: c.Trading.ShortLimitPriceMultiplier,
		Layered:                   c.Trading.LayeredOrder,
	}.Normalize()
}

func (c Config) LiveTradingAllowedByEnvironment() bool {
	if c.Trading.Env != EnvLive {
		return true
	}
	return strings.EqualFold(os.Getenv("OKX_ENV"), EnvLive) &&
		strings.EqualFold(os.Getenv("ALLOW_LIVE_TRADING"), "true") &&
		c.Trading.AllowLiveTrading
}

func (c Config) BinanceLiveTradingAllowedByEnvironment() bool {
	if c.Trading.Env != EnvLive {
		return true
	}
	return strings.EqualFold(os.Getenv("BINANCE_ENV"), EnvLive) &&
		strings.EqualFold(os.Getenv("ALLOW_LIVE_TRADING"), "true") &&
		c.Trading.AllowLiveTrading
}

func (c Config) GateLiveTradingAllowedByEnvironment() bool {
	if c.Trading.Env != EnvLive {
		return true
	}
	return strings.EqualFold(os.Getenv("GATE_ENV"), EnvLive) &&
		strings.EqualFold(os.Getenv("ALLOW_LIVE_TRADING"), "true") &&
		c.Trading.AllowLiveTrading
}
