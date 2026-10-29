package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotbinance"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotconfig"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotgate"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotokx"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotstorage"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

const (
	analysisPriceInstID        = "USDT-USD"
	analysisPriceBar           = "1H"
	defaultPriceDays           = 3
	defaultPNLDays             = 30
	defaultPNLMinutes          = defaultPNLDays * 24 * 60
	maxAnalysisPNLDays         = 90
	maxAnalysisPNLMinutes      = maxAnalysisPNLDays * 24 * 60
	minPositionLookbackMinutes = 30 * 24 * 60
	analysisCacheTTL           = 5 * time.Minute
	usdtSampleInterval         = 5 * time.Minute
	maxBalanceMinutes          = 90 * 24 * 60

	binanceAnalysisTradeWindow   = 7 * 24 * time.Hour
	binanceAnalysisTradeLimit    = 1000
	maxAnalysisConfiguredSymbols = 32
)

type analysisResponse struct {
	OK                bool                    `json:"ok"`
	APIID             string                  `json:"api_id"`
	BinanceAPIID      string                  `json:"binance_api_id,omitempty"`
	GateAPIID         string                  `json:"gate_api_id,omitempty"`
	Env               string                  `json:"env"`
	PriceDays         int                     `json:"price_days"`
	PNLDays           int                     `json:"pnl_days"`
	PNLMinutes        int                     `json:"pnl_minutes"`
	PriceInstID       string                  `json:"price_inst_id"`
	PriceBar          string                  `json:"price_bar"`
	RefreshedAt       time.Time               `json:"refreshed_at"`
	Cache             analysisCacheStatus     `json:"cache"`
	Source            analysisSourceStatus    `json:"source"`
	Balance           analysisBalance         `json:"balance"`
	PricePoints       []analysisPricePoint    `json:"price_points"`
	BalancePoints     []analysisBalancePoint  `json:"balance_points"`
	Summary           analysisSymbolStats     `json:"summary"`
	ExchangeSummaries []analysisSymbolStats   `json:"exchange_summaries"`
	Symbols           []analysisSymbolStats   `json:"symbols"`
	Trades            []analysisTrade         `json:"trades"`
	PositionTrades    []analysisPositionTrade `json:"position_trades"`
}

type analysisCacheStatus struct {
	Hit      bool      `json:"hit"`
	Stale    bool      `json:"stale"`
	CachedAt time.Time `json:"cached_at,omitempty"`
	CacheKey string    `json:"cache_key"`
}

type analysisSourceStatus struct {
	Balance string `json:"balance"`
	Price   string `json:"price"`
	Fills   string `json:"fills"`
	Funding string `json:"funding,omitempty"`
	Gate    string `json:"gate,omitempty"`
}

type analysisBalance struct {
	TotalEq   string                  `json:"total_eq"`
	AdjEq     string                  `json:"adj_eq,omitempty"`
	AvailEq   string                  `json:"avail_eq,omitempty"`
	Currency  string                  `json:"currency"`
	UpdatedAt string                  `json:"updated_at,omitempty"`
	Details   []analysisBalanceDetail `json:"details"`
}

type analysisBalanceDetail struct {
	Ccy       string `json:"ccy"`
	Eq        string `json:"eq"`
	EqUsd     string `json:"eq_usd"`
	AvailBal  string `json:"avail_bal,omitempty"`
	AvailEq   string `json:"avail_eq,omitempty"`
	CashBal   string `json:"cash_bal,omitempty"`
	FrozenBal string `json:"frozen_bal,omitempty"`
	DisEq     string `json:"dis_eq,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

type analysisPricePoint struct {
	Time    time.Time `json:"time"`
	TS      int64     `json:"ts"`
	Open    string    `json:"open"`
	High    string    `json:"high"`
	Low     string    `json:"low"`
	Close   string    `json:"close"`
	Confirm string    `json:"confirm,omitempty"`
}

type analysisBalancePoint struct {
	Time             time.Time `json:"time"`
	TS               int64     `json:"ts"`
	Value            float64   `json:"value"`
	Eq               string    `json:"eq,omitempty"`
	EqUsd            string    `json:"eq_usd,omitempty"`
	AvailEq          string    `json:"avail_eq,omitempty"`
	AvailBal         string    `json:"avail_bal,omitempty"`
	CashBal          string    `json:"cash_bal,omitempty"`
	FrozenBal        string    `json:"frozen_bal,omitempty"`
	ObservedAt       time.Time `json:"observed_at"`
	BalanceUpdatedAt string    `json:"balance_updated_at,omitempty"`
}

type balanceOverviewResponse struct {
	OK          bool                      `json:"ok"`
	Env         string                    `json:"env"`
	Days        int                       `json:"days"`
	Minutes     int                       `json:"minutes"`
	RefreshedAt time.Time                 `json:"refreshed_at"`
	Exchanges   []exchangeBalanceOverview `json:"exchanges"`
}

type exchangeBalanceOverview struct {
	Exchange      string                 `json:"exchange"`
	Label         string                 `json:"label"`
	Configured    bool                   `json:"configured"`
	Status        string                 `json:"status"`
	APIID         string                 `json:"api_id,omitempty"`
	Error         string                 `json:"error,omitempty"`
	Balance       analysisBalance        `json:"balance"`
	BalancePoints []analysisBalancePoint `json:"balance_points"`
	Window        analysisBalanceWindow  `json:"window"`
	RefreshedAt   time.Time              `json:"refreshed_at,omitempty"`
}

type analysisBalanceWindow struct {
	StartValue     float64   `json:"start_value"`
	CurrentValue   float64   `json:"current_value"`
	Change         float64   `json:"change"`
	ChangePct      float64   `json:"change_pct"`
	MaxDrawdown    float64   `json:"max_drawdown"`
	MaxDrawdownPct float64   `json:"max_drawdown_pct"`
	StartTime      time.Time `json:"start_time,omitempty"`
	CurrentTime    time.Time `json:"current_time,omitempty"`
}

type analysisSymbolStats struct {
	Exchange         string  `json:"exchange,omitempty"`
	InstID           string  `json:"inst_id"`
	TradeCount       int     `json:"trade_count"`
	Wins             int     `json:"wins"`
	Losses           int     `json:"losses"`
	Flats            int     `json:"flats"`
	GrossProfit      float64 `json:"gross_profit"`
	GrossLoss        float64 `json:"gross_loss"`
	Fees             float64 `json:"fees"`
	Turnover         float64 `json:"turnover"`
	NetPnL           float64 `json:"net_pnl"`
	WinRate          float64 `json:"win_rate"`
	ProfitFactor     float64 `json:"profit_factor"`
	ProfitFactorText string  `json:"profit_factor_text,omitempty"`
	PayoffRatio      float64 `json:"payoff_ratio"`
}

type analysisTrade struct {
	Exchange        string    `json:"exchange"`
	APIID           string    `json:"api_id,omitempty"`
	InstID          string    `json:"inst_id"`
	TradeID         string    `json:"trade_id"`
	OrdID           string    `json:"ord_id,omitempty"`
	Side            string    `json:"side,omitempty"`
	PosSide         string    `json:"pos_side,omitempty"`
	PositionEffect  string    `json:"position_effect,omitempty"`
	FillPx          string    `json:"fill_px,omitempty"`
	FillSz          string    `json:"fill_sz,omitempty"`
	Turnover        string    `json:"turnover,omitempty"`
	FillPnl         string    `json:"fill_pnl,omitempty"`
	Fee             string    `json:"fee,omitempty"`
	FeeCcy          string    `json:"fee_ccy,omitempty"`
	Margin          string    `json:"margin"`
	Leverage        int       `json:"leverage"`
	FundingFee      string    `json:"funding_fee"`
	NetPnL          string    `json:"net_pnl"`
	Operation       string    `json:"operation,omitempty"`
	OperationSource string    `json:"operation_source,omitempty"`
	SourceAction    string    `json:"-"`
	FillTime        time.Time `json:"fill_time"`
	FillTS          int64     `json:"fill_ts"`
	FillCount       int       `json:"fill_count"`
}

type analysisPositionTrade struct {
	Exchange          string    `json:"exchange"`
	APIID             string    `json:"api_id,omitempty"`
	InstID            string    `json:"inst_id"`
	Side              string    `json:"side"`
	EntryTime         time.Time `json:"entry_time"`
	EntryTS           int64     `json:"entry_ts"`
	ExitTime          time.Time `json:"exit_time"`
	ExitTS            int64     `json:"exit_ts"`
	EntryPx           string    `json:"entry_px,omitempty"`
	ExitPx            string    `json:"exit_px,omitempty"`
	Qty               string    `json:"qty,omitempty"`
	Margin            string    `json:"margin,omitempty"`
	Leverage          int       `json:"leverage,omitempty"`
	RealizedPnL       string    `json:"realized_pnl,omitempty"`
	Fee               string    `json:"fee,omitempty"`
	FeeCcy            string    `json:"fee_ccy,omitempty"`
	NetPnL            string    `json:"net_pnl,omitempty"`
	Turnover          string    `json:"turnover,omitempty"`
	GammaDirection    string    `json:"gamma_direction,omitempty"`
	EntryOrdID        string    `json:"entry_ord_id,omitempty"`
	ExitOrdID         string    `json:"exit_ord_id,omitempty"`
	FillCount         int       `json:"fill_count"`
	PricePrecision    *int      `json:"price_precision,omitempty"`
	QuantityPrecision *int      `json:"quantity_precision,omitempty"`
}

type analysisGateContractMeta struct {
	Precision          symbolDisplayPrecision
	ContractMultiplier float64
}

func (s *Server) StartUSDTBalanceSampler(ctx context.Context) {
	if s.ConfigStore == nil || s.Orders == nil || (s.OKXCredentials == nil && s.OKXLiveCredentials == nil && s.BinanceCredentials == nil && s.BinanceLiveCredentials == nil && s.GateCredentials == nil && s.GateLiveCredentials == nil) {
		return
	}
	go s.runUSDTBalanceSampler(ctx, usdtSampleInterval)
}

func (s *Server) runUSDTBalanceSampler(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = usdtSampleInterval
	}
	s.sampleConfiguredUSDTBalances(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sampleConfiguredUSDTBalances(ctx)
		}
	}
}

func (s *Server) sampleConfiguredUSDTBalances(ctx context.Context) {
	cfg := s.ConfigStore.Get()
	envName := analysisEnvName(cfg)
	now := s.now()
	okxStore := s.okxCredentialStore(cfg.Trading.Env)
	if okxStore != nil {
		for _, requestedAPIID := range configuredAPIIDs(okxStore.Status()) {
			sampleCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			err := s.sampleUSDTBalance(sampleCtx, cfg, requestedAPIID, envName, now)
			cancel()
			if err != nil && s.Logger != nil {
				s.Logger.Warn("failed to sample USDT balance", "exchange", trading.ExchangeOKX, "api_id", requestedAPIID, "env", envName, "error", err)
			}
		}
	}
	binanceStore := s.binanceCredentialStore(cfg.Trading.Env)
	if binanceStore != nil {
		for _, requestedAPIID := range configuredBinanceAPIIDs(binanceStore.Status()) {
			sampleCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			err := s.sampleBinanceUSDTBalance(sampleCtx, cfg, requestedAPIID, envName, now)
			cancel()
			if err != nil && s.Logger != nil {
				s.Logger.Warn("failed to sample USDT balance", "exchange", trading.ExchangeBinance, "api_id", requestedAPIID, "env", envName, "error", err)
			}
		}
	}
	gateStore := s.gateCredentialStore(cfg.Trading.Env)
	if gateStore != nil {
		for _, requestedAPIID := range configuredGateAPIIDs(gateStore.Status()) {
			sampleCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			err := s.sampleGateUSDTBalance(sampleCtx, cfg, requestedAPIID, envName, now)
			cancel()
			if err != nil && s.Logger != nil {
				s.Logger.Warn("failed to sample USDT balance", "exchange", trading.ExchangeGate, "api_id", requestedAPIID, "env", envName, "error", err)
			}
		}
	}
}

func configuredAPIIDs(status okx.CredentialStatus) []string {
	ids := make([]string, 0, len(status.Credentials))
	seen := map[string]bool{}
	for _, account := range status.Credentials {
		id := strings.TrimSpace(account.ID)
		if id == "" || !account.Configured || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	activeID := strings.TrimSpace(status.ActiveID)
	if len(ids) == 0 && status.Configured && activeID != "" {
		ids = append(ids, activeID)
	}
	return ids
}

func configuredBinanceAPIIDs(status binance.CredentialStatus) []string {
	ids := make([]string, 0, len(status.Credentials))
	seen := map[string]bool{}
	for _, account := range status.Credentials {
		id := strings.TrimSpace(account.ID)
		if id == "" || !account.Configured || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	activeID := strings.TrimSpace(status.ActiveID)
	if len(ids) == 0 && status.Configured && activeID != "" {
		ids = append(ids, activeID)
	}
	return ids
}

func configuredGateAPIIDs(status gate.CredentialStatus) []string {
	ids := make([]string, 0, len(status.Credentials))
	seen := map[string]bool{}
	for _, account := range status.Credentials {
		id := strings.TrimSpace(account.ID)
		if id == "" || !account.Configured || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	activeID := strings.TrimSpace(status.ActiveID)
	if len(ids) == 0 && status.Configured && activeID != "" {
		ids = append(ids, activeID)
	}
	return ids
}

func (s *Server) sampleUSDTBalance(ctx context.Context, cfg config.Config, requestedAPIID, envName string, now time.Time) error {
	store := s.okxCredentialStore(cfg.Trading.Env)
	if store == nil {
		return errors.New("OKX credential store is not configured")
	}
	creds, apiID, err := store.OKXCredentials(requestedAPIID)
	if err != nil {
		return err
	}
	client := s.analysisOKXClient(cfg, creds)
	_, err = s.fetchAnalysisBalance(ctx, client, apiID, envName, now)
	return err
}

func (s *Server) sampleBinanceUSDTBalance(ctx context.Context, cfg config.Config, requestedAPIID, envName string, now time.Time) error {
	store := s.binanceCredentialStore(cfg.Trading.Env)
	if store == nil {
		return errors.New("Binance credential store is not configured")
	}
	creds, apiID, err := store.BinanceCredentials(requestedAPIID)
	if err != nil {
		return err
	}
	client := s.analysisBinanceClient(cfg, creds)
	_, err = s.fetchBinanceAnalysisBalance(ctx, client, apiID, envName, now)
	return err
}

func (s *Server) sampleGateUSDTBalance(ctx context.Context, cfg config.Config, requestedAPIID, envName string, now time.Time) error {
	store := s.gateCredentialStore(cfg.Trading.Env)
	if store == nil {
		return errors.New("Gate credential store is not configured")
	}
	creds, apiID, err := store.GateCredentials(requestedAPIID)
	if err != nil {
		return err
	}
	account, err := (gate.Client{BaseURL: cfg.GateBaseURL(), Credentials: creds, HTTPClient: s.GateHTTPClient}).Account(ctx)
	if err != nil {
		return err
	}
	balance := analysisBalanceFromGate(account, now)
	if err := s.recordGateUSDTBalanceSnapshot(apiID, envName, balance, now); err != nil && s.Logger != nil {
		s.Logger.Warn("failed to write USDT balance snapshot", "exchange", trading.ExchangeGate, "api_id", apiID, "env", envName, "error", err)
	}
	return nil
}

func (s *Server) handleBalanceOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "only GET is allowed")
		return
	}
	if s.Orders == nil {
		writeError(w, http.StatusServiceUnavailable, "not_configured", "order database is not configured")
		return
	}
	cfg := s.ConfigStore.Get()
	minutes, days := balanceWindowQuery(r)
	now := s.now()
	envName := analysisEnvName(cfg)
	okxAPIID := strings.TrimSpace(r.URL.Query().Get("api_id"))
	binanceAPIID := strings.TrimSpace(r.URL.Query().Get("binance_api_id"))
	writeJSON(w, http.StatusOK, balanceOverviewResponse{
		OK:          true,
		Env:         envName,
		Days:        days,
		Minutes:     minutes,
		RefreshedAt: now.UTC(),
		Exchanges: []exchangeBalanceOverview{
			s.balanceOverviewForOKX(r.Context(), cfg, envName, okxAPIID, minutes, now),
			s.balanceOverviewForBinance(r.Context(), cfg, envName, binanceAPIID, minutes, now),
			s.balanceOverviewForGate(r.Context(), cfg, envName, strings.TrimSpace(r.URL.Query().Get("gate_api_id")), minutes, now),
		},
	})
}

func balanceWindowQuery(r *http.Request) (int, int) {
	if raw := strings.TrimSpace(r.URL.Query().Get("minutes")); raw != "" {
		minutes, err := strconv.Atoi(raw)
		if err != nil || minutes < 0 {
			minutes = defaultPriceDays * 24 * 60
		}
		if minutes > maxBalanceMinutes {
			minutes = maxBalanceMinutes
		}
		days := 0
		if minutes > 0 {
			days = int(math.Ceil(float64(minutes) / (24 * 60)))
		}
		return minutes, days
	}
	days := positiveIntQuery(r, "days", defaultPriceDays)
	minutes := days * 24 * 60
	if minutes > maxBalanceMinutes {
		minutes = maxBalanceMinutes
		days = maxBalanceMinutes / (24 * 60)
	}
	return minutes, days
}

func (s *Server) handleAnalysis(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "only GET is allowed")
		return
	}
	if s.Orders == nil {
		writeError(w, http.StatusServiceUnavailable, "not_configured", "order database is not configured")
		return
	}
	cfg := s.ConfigStore.Get()
	priceDays := positiveIntQuery(r, "price_days", defaultPriceDays)
	pnlDays := positiveIntQuery(r, "pnl_days", defaultPNLDays)
	pnlMinutes := analysisPNLMinutesFromQuery(r, pnlDays)
	refresh := strings.EqualFold(r.URL.Query().Get("refresh"), "true")
	apiID := strings.TrimSpace(r.URL.Query().Get("api_id"))
	binanceAPIID := strings.TrimSpace(r.URL.Query().Get("binance_api_id"))
	gateAPIID := strings.TrimSpace(r.URL.Query().Get("gate_api_id"))
	resp, err := s.buildAnalysis(r.Context(), cfg, apiID, binanceAPIID, priceDays, pnlMinutes, refresh, gateAPIID)
	if err != nil {
		writeError(w, http.StatusBadGateway, "analysis_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) buildAnalysis(ctx context.Context, cfg config.Config, requestedAPIID, requestedBinanceAPIID string, priceDays, pnlMinutes int, refresh bool, requestedGateAPIIDs ...string) (analysisResponse, error) {
	pnlMinutes = normalizeAnalysisPNLMinutes(pnlMinutes)
	requestedGateAPIID := ""
	if len(requestedGateAPIIDs) > 0 {
		requestedGateAPIID = strings.TrimSpace(requestedGateAPIIDs[0])
	}
	now := s.now()
	envName := analysisEnvName(cfg)
	okxCreds, apiID, okxConfigured := s.analysisOKXCredentials(cfg, requestedAPIID)
	binanceCreds, binanceAPIID, binanceConfigured := s.analysisBinanceCredentials(cfg, requestedBinanceAPIID)
	gateCreds, gateAPIID, gateConfigured := s.analysisGateCredentials(cfg, requestedGateAPIID)
	if !okxConfigured && !binanceConfigured && !gateConfigured {
		return analysisResponse{}, errors.New("no exchange API credentials are configured")
	}
	cacheKey := analysisCacheKey(apiID, binanceAPIID, envName, priceDays, pnlMinutes, gateAPIID)
	if !refresh {
		if cached, ok, err := s.Orders.CachedPayload(cacheKey); err != nil {
			return analysisResponse{}, err
		} else if ok && now.Sub(cached.RefreshedAt) < analysisCacheTTL {
			var resp analysisResponse
			if err := json.Unmarshal([]byte(cached.PayloadJSON), &resp); err != nil {
				return analysisResponse{}, err
			}
			resp.Cache.Hit = true
			resp.Cache.Stale = false
			resp.Cache.CachedAt = cached.RefreshedAt
			resp.Cache.CacheKey = cacheKey
			return resp, nil
		}
	}
	var balance analysisBalance
	balanceReady := false
	source := analysisSourceStatus{}
	setBalance := func(value analysisBalance, sourceName string) {
		if balanceReady {
			return
		}
		balance = value
		balanceReady = true
		source.Balance = sourceName
	}
	fillSources := []string{}
	if okxConfigured {
		fillSources = append(fillSources, "okx")
	}
	if binanceConfigured {
		fillSources = append(fillSources, "binance")
	}
	if gateConfigured {
		fillSources = append(fillSources, "gate")
	}
	source.Fills = strings.Join(fillSources, "+")

	var okxClient okx.Client
	analysisCfg := cfg
	okxAnalysisPrecisions := map[string]symbolDisplayPrecision{}
	binanceAnalysisPrecisions := map[string]symbolDisplayPrecision{}
	gateAnalysisContracts := map[string]analysisGateContractMeta{}
	if okxConfigured {
		okxClient = s.analysisOKXClient(cfg, okxCreds)
		balanceValue, balanceErr := s.fetchAnalysisBalance(ctx, okxClient, apiID, envName, now)
		if balanceErr != nil {
			source.Balance = "okx_error"
			if s.Logger != nil {
				s.Logger.Warn("failed to fetch OKX analysis balance", "api_id", apiID, "env", envName, "error", balanceErr)
			}
			if !binanceConfigured && !gateConfigured {
				if stale, ok := s.staleAnalysisFromCache(cacheKey); ok {
					return stale, nil
				}
				return analysisResponse{}, balanceErr
			}
		} else {
			setBalance(balanceValue, "okx")
			source.Balance = "okx"
		}
		if _, fundingErr := s.fetchOKXAnalysisFunding(ctx, okxClient, pnlMinutes, now); fundingErr != nil {
			source.Funding = "okx_error"
			if s.Logger != nil {
				s.Logger.Warn("failed to fetch OKX analysis funding fees", "api_id", apiID, "env", envName, "error", fundingErr)
			}
		} else {
			source.Funding = "okx"
		}
		if refreshErr := s.refreshAnalysisData(ctx, okxClient, apiID, priceDays, pnlMinutes, now); refreshErr != nil {
			source.Price = "okx_error"
			if s.Logger != nil {
				s.Logger.Warn("failed to refresh OKX analysis data", "api_id", apiID, "env", envName, "error", refreshErr)
			}
		} else {
			source.Price = "okx"
		}
		if instruments, _, instrumentErr := okxClient.SwapInstruments(ctx); instrumentErr != nil {
			if s.Logger != nil {
				s.Logger.Warn("failed to fetch OKX instruments for analysis contract values", "api_id", apiID, "env", envName, "error", instrumentErr)
			}
		} else {
			analysisCfg = analysisConfigWithOKXCtVals(cfg, instruments)
			okxAnalysisPrecisions = analysisDisplayPrecisionsFromOKXInstruments(instruments)
		}
	}

	binanceTrades := []analysisTrade{}
	binanceDataReady := false
	if binanceConfigured {
		binanceClient := s.analysisBinanceClient(cfg, binanceCreds)
		binanceAnalysisPrecisions = s.binanceDisplayPrecisions(ctx, binanceClient)
		var binanceErr error
		binanceTrades, binanceErr = s.fetchBinanceAnalysisTrades(ctx, binanceClient, binanceAPIID, cfg, pnlMinutes, now)
		binanceDataReady = binanceErr == nil
		if binanceErr != nil {
			source.Fills = strings.ReplaceAll(source.Fills, "binance", "binance_error")
			if s.Logger != nil {
				s.Logger.Warn("failed to fetch Binance analysis trades", "api_id", binanceAPIID, "env", envName, "error", binanceErr)
			}
		}
		if _, fundingErr := s.fetchBinanceAnalysisFunding(ctx, binanceClient, pnlMinutes, now); fundingErr != nil {
			if source.Funding == "" {
				source.Funding = "binance_error"
			} else {
				source.Funding += "+binance_error"
			}
		} else if source.Funding == "" {
			source.Funding = "binance"
		} else {
			source.Funding += "+binance"
		}
		if !balanceReady {
			if balanceValue, balanceErr := s.fetchBinanceAnalysisBalance(ctx, binanceClient, binanceAPIID, envName, now); balanceErr == nil {
				setBalance(balanceValue, "binance")
			} else if s.Logger != nil {
				s.Logger.Warn("failed to fetch Binance analysis balance", "api_id", binanceAPIID, "env", envName, "error", balanceErr)
			}
		}
	}

	gateTrades := []analysisTrade{}
	gatePositions := []analysisPositionTrade{}
	gateDataReady := false
	if gateConfigured {
		gateClient := s.analysisGateClient(cfg, gateCreds)
		if contracts, contractsErr := gateClient.Contracts(ctx); contractsErr != nil {
			if s.Logger != nil {
				s.Logger.Warn("failed to fetch Gate contracts for analysis display data", "api_id", gateAPIID, "env", envName, "error", contractsErr)
			}
		} else {
			gateAnalysisContracts = analysisGateContractMetaFromContracts(contracts)
		}
		if account, accountErr := gateClient.Account(ctx); accountErr == nil {
			balanceValue := analysisBalanceFromGate(account, now)
			if !balanceReady {
				setBalance(balanceValue, "gate")
			}
			if snapshotErr := s.recordGateUSDTBalanceSnapshot(gateAPIID, envName, balanceValue, now); snapshotErr != nil && s.Logger != nil {
				s.Logger.Warn("failed to write Gate analysis balance snapshot", "api_id", gateAPIID, "env", envName, "error", snapshotErr)
			}
			if source.Balance == "" {
				source.Balance = "gate"
			}
		} else {
			source.Balance = "gate_error"
			if s.Logger != nil {
				s.Logger.Warn("failed to fetch Gate analysis balance", "api_id", gateAPIID, "env", envName, "error", accountErr)
			}
		}
		var gateErr error
		gateTrades, gatePositions, gateErr = s.fetchGateAnalysisData(ctx, gateClient, gateAPIID, pnlMinutes, now)
		gateDataReady = gateErr == nil || len(gateTrades) > 0 || len(gatePositions) > 0
		source.Gate = "gate"
		if gateErr != nil {
			source.Gate = "gate_error"
			if s.Logger != nil {
				s.Logger.Warn("failed to fetch Gate analysis data", "api_id", gateAPIID, "env", envName, "error", gateErr)
			}
		}
		gateFundingSource := "gate"
		if gateErr != nil {
			gateFundingSource = "gate_error"
		}
		if source.Funding == "" {
			source.Funding = gateFundingSource
		} else {
			source.Funding += "+" + gateFundingSource
		}
	}

	var resp analysisResponse
	var buildErr error
	if okxConfigured {
		resp, buildErr = s.analysisFromStore(analysisCfg, apiID, binanceAPIID, gateAPIID, envName, priceDays, pnlMinutes, now, source, binanceTrades, gateTrades, gatePositions)
	}
	if !okxConfigured || buildErr != nil {
		if buildErr != nil && !binanceDataReady && !gateDataReady {
			if stale, ok := s.staleAnalysisFromCache(cacheKey); ok {
				return stale, nil
			}
			return analysisResponse{}, buildErr
		}
		balancePoints := []analysisBalancePoint{}
		if balanceReady {
			primaryExchange := trading.ExchangeOKX
			primaryAPIID := apiID
			if !okxConfigured || strings.TrimSpace(apiID) == "" {
				if binanceDataReady {
					primaryExchange, primaryAPIID = trading.ExchangeBinance, binanceAPIID
				} else {
					primaryExchange, primaryAPIID = trading.ExchangeGate, gateAPIID
				}
			}
			balancePoints = s.balanceOverviewPoints(primaryExchange, primaryAPIID, envName, priceDays*24*60, now)
		}
		resp = s.analysisFromExternalData(analysisCfg, apiID, binanceAPIID, gateAPIID, envName, priceDays, pnlMinutes, now, source, balancePoints, binanceTrades, gateTrades, gatePositions)
	}
	if !resp.OK {
		if stale, ok := s.staleAnalysisFromCache(cacheKey); ok {
			return stale, nil
		}
		if buildErr != nil {
			return analysisResponse{}, buildErr
		}
		return analysisResponse{}, errors.New("no exchange analysis data is available")
	}
	applyAnalysisPositionDisplayData(&resp, analysisCfg, okxAnalysisPrecisions, binanceAnalysisPrecisions, gateAnalysisContracts)
	resp.Balance = balance
	resp.Cache.CacheKey = cacheKey
	if err := s.Orders.CachePayload(cacheKey, resp, now); err != nil && s.Logger != nil {
		s.Logger.Warn("failed to write analysis cache", "error", err)
	}
	return resp, nil
}

func (s *Server) staleAnalysisFromCache(cacheKey string) (analysisResponse, bool) {
	cached, ok, err := s.Orders.CachedPayload(cacheKey)
	if err != nil || !ok {
		return analysisResponse{}, false
	}
	var resp analysisResponse
	if err := json.Unmarshal([]byte(cached.PayloadJSON), &resp); err != nil {
		return analysisResponse{}, false
	}
	resp.Cache.Hit = true
	resp.Cache.Stale = true
	resp.Cache.CachedAt = cached.RefreshedAt
	resp.Cache.CacheKey = cacheKey
	resp.Source = analysisSourceStatus{Balance: "cache", Price: "cache", Fills: "cache", Funding: "cache", Gate: "cache"}
	return resp, true
}

func (s *Server) analysisOKXCredentials(cfg config.Config, requestedAPIID string) (okx.Credentials, string, bool) {
	store := s.okxCredentialStore(cfg.Trading.Env)
	if store == nil {
		return okx.Credentials{}, "", false
	}
	status := store.Status()
	if !status.Configured {
		return okx.Credentials{}, strings.TrimSpace(status.ActiveID), false
	}
	creds, apiID, err := store.OKXCredentials(requestedAPIID)
	if err != nil {
		if s.Logger != nil {
			s.Logger.Warn("failed to resolve OKX analysis credentials", "error", err)
		}
		return okx.Credentials{}, strings.TrimSpace(status.ActiveID), false
	}
	return creds, apiID, true
}

func (s *Server) analysisBinanceCredentials(cfg config.Config, requestedAPIID string) (binance.Credentials, string, bool) {
	store := s.binanceCredentialStore(cfg.Trading.Env)
	if store == nil {
		return binance.Credentials{}, "", false
	}
	status := store.Status()
	if !status.Configured {
		return binance.Credentials{}, strings.TrimSpace(status.ActiveID), false
	}
	creds, apiID, err := store.BinanceCredentials(requestedAPIID)
	if err != nil {
		if s.Logger != nil {
			s.Logger.Warn("failed to resolve Binance analysis credentials", "error", err)
		}
		return binance.Credentials{}, strings.TrimSpace(status.ActiveID), false
	}
	return creds, apiID, true
}

func (s *Server) analysisOKXClient(cfg config.Config, creds okx.Credentials) okx.Client {
	return okx.Client{
		BaseURL:     cfg.OKXBaseURL(),
		Credentials: creds,
		Demo:        cfg.DemoTradingHeaderEnabled(),
		HTTPClient:  s.okxHTTPClient(),
	}
}

func (s *Server) analysisBinanceClient(cfg config.Config, creds binance.Credentials) binance.Client {
	return binance.Client{
		BaseURL:     cfg.BinanceBaseURL(),
		Credentials: creds,
		HTTPClient:  s.binanceHTTPClient(),
	}
}

func analysisEnvName(cfg config.Config) string {
	envName := strings.ToLower(strings.TrimSpace(cfg.Trading.Env))
	if envName == "" {
		return config.EnvDemo
	}
	return envName
}

func analysisPNLMinutesFromQuery(r *http.Request, pnlDays int) int {
	raw := strings.TrimSpace(r.URL.Query().Get("pnl_minutes"))
	if raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err == nil && parsed > 0 {
			return normalizeAnalysisPNLMinutes(parsed)
		}
	}
	return normalizeAnalysisPNLMinutes(pnlDays * 24 * 60)
}

func normalizeAnalysisPNLMinutes(minutes int) int {
	if minutes <= 0 {
		return defaultPNLMinutes
	}
	if minutes > maxAnalysisPNLMinutes {
		return maxAnalysisPNLMinutes
	}
	return minutes
}

func analysisPNLDaysForMinutes(minutes int) int {
	minutes = normalizeAnalysisPNLMinutes(minutes)
	days := (minutes + 24*60 - 1) / (24 * 60)
	if days < 1 {
		return 1
	}
	if days > maxAnalysisPNLDays {
		return maxAnalysisPNLDays
	}
	return days
}

func analysisPNLSince(now time.Time, pnlMinutes int) time.Time {
	return now.UTC().Add(-time.Duration(normalizeAnalysisPNLMinutes(pnlMinutes)) * time.Minute)
}

func analysisPositionLookbackMinutes(pnlMinutes int) int {
	pnlMinutes = normalizeAnalysisPNLMinutes(pnlMinutes)
	if pnlMinutes < minPositionLookbackMinutes {
		return minPositionLookbackMinutes
	}
	return pnlMinutes
}

func analysisPositionSince(now time.Time, pnlMinutes int) time.Time {
	return now.UTC().Add(-time.Duration(analysisPositionLookbackMinutes(pnlMinutes)) * time.Minute)
}

func analysisCacheKey(apiID, binanceAPIID, envName string, priceDays, pnlMinutes int, gateAPIIDs ...string) string {
	gateAPIID := ""
	if len(gateAPIIDs) > 0 {
		gateAPIID = strings.TrimSpace(gateAPIIDs[0])
	}
	return "analysis|v2|" + apiID + "|binance:" + binanceAPIID + "|gate:" + gateAPIID + "|" + envName + "|" + strconv.Itoa(priceDays) + "|pnlm:" + strconv.Itoa(normalizeAnalysisPNLMinutes(pnlMinutes))
}

func (s *Server) refreshAnalysisData(ctx context.Context, client okx.Client, apiID string, priceDays, pnlMinutes int, now time.Time) error {
	priceLimit := priceDays * 24
	if priceLimit <= 0 {
		priceLimit = defaultPriceDays * 24
	}
	candles, _, err := client.MarketCandles(ctx, analysisPriceInstID, analysisPriceBar, priceLimit)
	if err != nil {
		return err
	}
	storageCandles := make([]storage.MarketCandle, 0, len(candles))
	for _, candle := range candles {
		ts, err := strconv.ParseInt(strings.TrimSpace(candle.TS), 10, 64)
		if err != nil {
			continue
		}
		storageCandles = append(storageCandles, storage.MarketCandle{
			InstID:  analysisPriceInstID,
			Bar:     analysisPriceBar,
			TS:      ts,
			Open:    candle.Open,
			High:    candle.High,
			Low:     candle.Low,
			Close:   candle.Close,
			Volume:  candle.Volume,
			Confirm: candle.Confirm,
		})
	}
	if err := s.Orders.UpsertMarketCandles(storageCandles, now); err != nil {
		return err
	}
	return s.refreshFillsSince(ctx, client, apiID, analysisPositionSince(now, pnlMinutes), now)
}

func (s *Server) fetchAnalysisBalance(ctx context.Context, client okx.Client, apiID, envName string, now time.Time) (analysisBalance, error) {
	balance, _, err := client.AccountBalanceSnapshot(ctx)
	if err != nil {
		return analysisBalance{}, err
	}
	out := analysisBalanceFromOKX(balance)
	if err := s.recordUSDTBalanceSnapshot(apiID, envName, balance, now); err != nil && s.Logger != nil {
		s.Logger.Warn("failed to write USDT balance snapshot", "api_id", apiID, "env", envName, "error", err)
	}
	return out, nil
}

func (s *Server) fetchOKXAnalysisFunding(ctx context.Context, client okx.Client, pnlMinutes int, now time.Time) ([]okx.AccountBill, error) {
	since := analysisPNLSince(now, pnlMinutes)
	bills, _, err := client.AccountBillsArchive(ctx, "SWAP", since, now.UTC(), "", 100)
	if err != nil {
		return nil, err
	}
	out := make([]okx.AccountBill, 0, len(bills))
	for _, bill := range bills {
		if isOKXFundingBill(bill) {
			out = append(out, bill)
		}
	}
	return out, nil
}

func isOKXFundingBill(bill okx.AccountBill) bool {
	subType := strings.TrimSpace(bill.SubType)
	if subType == "173" {
		return true
	}
	text := strings.ToLower(strings.Join([]string{bill.Type, bill.SubType, bill.RawJSON}, " "))
	return strings.Contains(text, "funding")
}

func (s *Server) fetchBinanceAnalysisBalance(ctx context.Context, client binance.Client, apiID, envName string, now time.Time) (analysisBalance, error) {
	balances, err := client.AccountBalance(ctx)
	if err != nil {
		return analysisBalance{}, err
	}
	positions, err := client.Positions(ctx, "")
	if err != nil {
		return analysisBalance{}, err
	}
	out := analysisBalanceFromBinance(balances, positions)
	if err := s.recordBinanceUSDTBalanceSnapshot(apiID, envName, out, now); err != nil && s.Logger != nil {
		s.Logger.Warn("failed to write USDT balance snapshot", "exchange", trading.ExchangeBinance, "api_id", apiID, "env", envName, "error", err)
	}
	return out, nil
}

func (s *Server) fetchBinanceAnalysisFunding(ctx context.Context, client binance.Client, pnlMinutes int, now time.Time) ([]binance.Income, error) {
	since := analysisPNLSince(now, pnlMinutes)
	incomes, err := client.IncomeHistory(ctx, "", "FUNDING_FEE", since, now.UTC(), 1000)
	if err != nil {
		return nil, err
	}
	out := make([]binance.Income, 0, len(incomes))
	for _, income := range incomes {
		if strings.EqualFold(strings.TrimSpace(income.IncomeType), "FUNDING_FEE") {
			out = append(out, income)
		}
	}
	return out, nil
}

func analysisBalanceFromOKX(balance okx.AccountBalanceData) analysisBalance {
	out := analysisBalance{
		TotalEq:   strings.TrimSpace(balance.TotalEq),
		AdjEq:     strings.TrimSpace(balance.AdjEq),
		AvailEq:   strings.TrimSpace(balance.AvailEq),
		Currency:  "USD",
		UpdatedAt: okxMillisToRFC3339(balance.UTime),
		Details:   make([]analysisBalanceDetail, 0, len(balance.Details)),
	}
	for _, detail := range balance.Details {
		row := analysisBalanceDetail{
			Ccy:       strings.TrimSpace(detail.Ccy),
			Eq:        strings.TrimSpace(detail.Eq),
			EqUsd:     strings.TrimSpace(detail.EqUsd),
			AvailBal:  strings.TrimSpace(detail.AvailBal),
			AvailEq:   strings.TrimSpace(detail.AvailEq),
			CashBal:   strings.TrimSpace(detail.CashBal),
			FrozenBal: strings.TrimSpace(detail.FrozenBal),
			DisEq:     strings.TrimSpace(detail.DisEq),
			UpdatedAt: okxMillisToRFC3339(detail.UTime),
		}
		if row.Ccy == "" {
			continue
		}
		if row.UpdatedAt == "" {
			row.UpdatedAt = out.UpdatedAt
		}
		out.Details = append(out.Details, row)
	}
	sortBalanceDetails(out.Details)
	return out
}

func analysisBalanceFromBinance(balances []binance.Balance, positions []binance.Position) analysisBalance {
	out := analysisBalance{Currency: "USDT"}
	balance, ok := binance.USDTBalanceFromAccount(balances)
	if !ok {
		return out
	}
	updatedAt := binanceMillisToRFC3339(balance.UpdateTime)
	equity := binanceUSDTEq(balance, positions)
	if equity == "" {
		equity = strings.TrimSpace(balance.Balance)
	}
	out.TotalEq = equity
	out.AvailEq = strings.TrimSpace(balance.AvailableBalance)
	out.UpdatedAt = updatedAt
	out.Details = []analysisBalanceDetail{{
		Ccy:       "USDT",
		Eq:        equity,
		EqUsd:     equity,
		AvailBal:  strings.TrimSpace(balance.AvailableBalance),
		AvailEq:   strings.TrimSpace(balance.AvailableBalance),
		CashBal:   strings.TrimSpace(balance.CrossWalletBalance),
		UpdatedAt: updatedAt,
	}}
	return out
}

func binanceUSDTEq(balance binance.Balance, positions []binance.Position) string {
	rawBalance := strings.TrimSpace(balance.Balance)
	total, ok := new(big.Rat).SetString(rawBalance)
	if !ok {
		return rawBalance
	}
	decimals := decimalsFromDecimalString(rawBalance)
	for _, position := range positions {
		if !strings.EqualFold(strings.TrimSpace(position.MarginAsset), "USDT") {
			continue
		}
		positionAmt, ok := new(big.Rat).SetString(strings.TrimSpace(position.PositionAmt))
		if !ok || positionAmt.Sign() == 0 {
			continue
		}
		rawUPL := strings.TrimSpace(position.UnRealizedProfit)
		upl, ok := new(big.Rat).SetString(rawUPL)
		if !ok {
			continue
		}
		total.Add(total, upl)
		if uplDecimals := decimalsFromDecimalString(rawUPL); uplDecimals > decimals {
			decimals = uplDecimals
		}
	}
	return trimDecimalZeros(total.FloatString(decimals))
}

func sortBalanceDetails(details []analysisBalanceDetail) {
	for i := 0; i < len(details); i++ {
		for j := i + 1; j < len(details); j++ {
			if parseFloat(details[j].EqUsd) > parseFloat(details[i].EqUsd) {
				details[i], details[j] = details[j], details[i]
			}
		}
	}
}

func (s *Server) recordUSDTBalanceSnapshot(apiID, envName string, balance okx.AccountBalanceData, observedAt time.Time) error {
	if s.Orders == nil {
		return nil
	}
	for _, detail := range balance.Details {
		if !strings.EqualFold(strings.TrimSpace(detail.Ccy), "USDT") {
			continue
		}
		updatedAt := okxMillisToRFC3339(detail.UTime)
		if updatedAt == "" {
			updatedAt = okxMillisToRFC3339(balance.UTime)
		}
		return s.Orders.UpsertUSDTBalanceSnapshot(storage.USDTBalanceSnapshot{
			Exchange:         trading.ExchangeOKX,
			APIID:            apiID,
			Env:              envName,
			ObservedAt:       observedAt.UTC(),
			TotalEq:          strings.TrimSpace(balance.TotalEq),
			Eq:               strings.TrimSpace(detail.Eq),
			EqUsd:            strings.TrimSpace(detail.EqUsd),
			AvailEq:          strings.TrimSpace(detail.AvailEq),
			AvailBal:         strings.TrimSpace(detail.AvailBal),
			CashBal:          strings.TrimSpace(detail.CashBal),
			FrozenBal:        strings.TrimSpace(detail.FrozenBal),
			DisEq:            strings.TrimSpace(detail.DisEq),
			BalanceUpdatedAt: updatedAt,
		})
	}
	return nil
}

func (s *Server) recordBinanceUSDTBalanceSnapshot(apiID, envName string, balance analysisBalance, observedAt time.Time) error {
	if s.Orders == nil {
		return nil
	}
	var detail analysisBalanceDetail
	for _, row := range balance.Details {
		if strings.EqualFold(strings.TrimSpace(row.Ccy), "USDT") {
			detail = row
			break
		}
	}
	if strings.TrimSpace(detail.Ccy) == "" {
		return nil
	}
	updatedAt := detail.UpdatedAt
	if updatedAt == "" {
		updatedAt = balance.UpdatedAt
	}
	return s.Orders.UpsertUSDTBalanceSnapshot(storage.USDTBalanceSnapshot{
		Exchange:         trading.ExchangeBinance,
		APIID:            apiID,
		Env:              envName,
		ObservedAt:       observedAt.UTC(),
		TotalEq:          strings.TrimSpace(balance.TotalEq),
		Eq:               strings.TrimSpace(detail.Eq),
		EqUsd:            strings.TrimSpace(detail.EqUsd),
		AvailEq:          strings.TrimSpace(detail.AvailEq),
		AvailBal:         strings.TrimSpace(detail.AvailBal),
		CashBal:          strings.TrimSpace(detail.CashBal),
		FrozenBal:        strings.TrimSpace(detail.FrozenBal),
		DisEq:            strings.TrimSpace(detail.DisEq),
		BalanceUpdatedAt: updatedAt,
	})
}

func (s *Server) recordGateUSDTBalanceSnapshot(apiID, envName string, balance analysisBalance, observedAt time.Time) error {
	if s.Orders == nil {
		return nil
	}
	var detail analysisBalanceDetail
	for _, row := range balance.Details {
		if strings.EqualFold(strings.TrimSpace(row.Ccy), "USDT") {
			detail = row
			break
		}
	}
	if strings.TrimSpace(detail.Ccy) == "" {
		return nil
	}
	updatedAt := detail.UpdatedAt
	if updatedAt == "" {
		updatedAt = balance.UpdatedAt
	}
	return s.Orders.UpsertUSDTBalanceSnapshot(storage.USDTBalanceSnapshot{
		Exchange:         trading.ExchangeGate,
		APIID:            apiID,
		Env:              envName,
		ObservedAt:       observedAt.UTC(),
		TotalEq:          strings.TrimSpace(balance.TotalEq),
		Eq:               strings.TrimSpace(detail.Eq),
		EqUsd:            strings.TrimSpace(detail.EqUsd),
		AvailEq:          strings.TrimSpace(detail.AvailEq),
		AvailBal:         strings.TrimSpace(detail.AvailBal),
		CashBal:          strings.TrimSpace(detail.CashBal),
		FrozenBal:        strings.TrimSpace(detail.FrozenBal),
		DisEq:            strings.TrimSpace(detail.DisEq),
		BalanceUpdatedAt: updatedAt,
	})
}

func okxMillisToRFC3339(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	ms, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || ms <= 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339Nano)
}

func binanceMillisToRFC3339(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339Nano)
}

func (s *Server) refreshFills(ctx context.Context, client okx.Client, apiID string, pnlMinutes int, now time.Time) error {
	return s.refreshFillsSince(ctx, client, apiID, analysisPNLSince(now, pnlMinutes), now)
}

func (s *Server) refreshFillsSince(ctx context.Context, client okx.Client, apiID string, cutoff time.Time, now time.Time) error {
	cutoff = cutoff.UTC()
	after := ""
	for page := 0; page < 20; page++ {
		fills, _, err := client.FillsHistory(ctx, "SWAP", after, 100)
		if err != nil {
			return err
		}
		if len(fills) == 0 {
			return nil
		}
		stored := make([]storage.OKXFill, 0, len(fills))
		var oldest time.Time
		for i, fill := range fills {
			fillTimeMS, err := strconv.ParseInt(strings.TrimSpace(fill.FillTime), 10, 64)
			if err != nil {
				continue
			}
			fillTime := time.UnixMilli(fillTimeMS).UTC()
			if oldest.IsZero() || fillTime.Before(oldest) {
				oldest = fillTime
			}
			stored = append(stored, storage.OKXFill{
				APIID:    apiID,
				InstType: fill.InstType,
				InstID:   fill.InstID,
				TradeID:  fill.TradeID,
				OrdID:    fill.OrdID,
				ClOrdID:  fill.ClOrdID,
				Side:     fill.Side,
				FillPx:   fill.FillPx,
				FillSz:   fill.FillSz,
				FillPnl:  fill.FillPnl,
				Fee:      fill.Fee,
				FeeCcy:   fill.FeeCcy,
				FillTime: fillTimeMS,
				RawJSON:  fill.RawJSON,
			})
			if i == len(fills)-1 {
				after = fill.TradeID
				if after == "" {
					after = fill.OrdID
				}
			}
		}
		if err := s.Orders.UpsertOKXFills(stored, now); err != nil {
			return err
		}
		if oldest.IsZero() || oldest.Before(cutoff) || len(fills) < 100 || after == "" {
			return nil
		}
	}
	return nil
}

func (s *Server) fetchBinanceAnalysisTrades(ctx context.Context, client binance.Client, apiID string, cfg config.Config, pnlMinutes int, now time.Time) ([]analysisTrade, error) {
	since := analysisPositionSince(now, pnlMinutes)
	if s.Orders != nil && binanceUserStreamIsActive(client, apiID) {
		if fills, err := s.Orders.ListBinanceFills(apiID, since); err == nil && len(fills) > 0 {
			out := make([]analysisTrade, 0, len(fills))
			for _, fill := range fills {
				if row, ok := analysisTradeFromBinanceFill(fill); ok {
					out = append(out, row)
				}
			}
			if len(out) > 0 {
				return aggregateAnalysisTrades(out), nil
			}
		}
	}
	symbols := s.analysisBinanceSymbols(cfg, since)
	if len(symbols) == 0 {
		return nil, nil
	}
	out := []analysisTrade{}
	symbolErrs := []error{}
	for _, symbol := range symbols {
		if until, blocked := binance.RateLimitPauseUntil(now); blocked {
			symbolErrs = append(symbolErrs, fmt.Errorf("Binance analysis trades paused until %s after REST rate limiting", until.Format(time.RFC3339)))
			break
		}
		trades, err := s.fetchBinanceAnalysisSymbolTrades(ctx, client, apiID, symbol, since, now.UTC())
		out = append(out, trades...)
		if err != nil {
			symbolErr := fmt.Errorf("Binance %s analysis trades: %w", symbol, err)
			symbolErrs = append(symbolErrs, symbolErr)
			if _, blocked := binance.RateLimitPauseUntil(now); blocked {
				break
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
				return out, errors.Join(symbolErrs...)
			}
		}
	}
	return out, errors.Join(symbolErrs...)
}

func (s *Server) fetchBinanceAnalysisSymbolTrades(ctx context.Context, client binance.Client, apiID, symbol string, since, until time.Time) ([]analysisTrade, error) {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	if symbol == "" {
		return nil, nil
	}
	out := []analysisTrade{}
	windowEnd := until.UTC()
	if windowEnd.IsZero() {
		windowEnd = time.Now().UTC()
	}
	for windowEnd.After(since) {
		windowStart := windowEnd.Add(-binanceAnalysisTradeWindow)
		if windowStart.Before(since) {
			windowStart = since
		}
		trades, err := client.UserTrades(ctx, symbol, windowStart, windowEnd, binanceAnalysisTradeLimit)
		if err != nil {
			return out, err
		}
		for _, trade := range trades {
			row, ok := analysisTradeFromBinanceTrade(apiID, trade)
			if !ok || row.FillTime.Before(since) || row.FillTime.After(until) {
				continue
			}
			out = append(out, row)
		}
		if len(trades) >= binanceAnalysisTradeLimit {
			return out, fmt.Errorf("Binance %s 7d trades reached %d limit; analysis trade history may be incomplete", symbol, binanceAnalysisTradeLimit)
		}
		windowEnd = windowStart.Add(-time.Millisecond)
	}
	return out, nil
}

func (s *Server) analysisBinanceSymbols(cfg config.Config, since time.Time) []string {
	seen := map[string]bool{}
	add := func(symbol string) {
		symbol = strings.ToUpper(strings.TrimSpace(symbol))
		if symbol == "" || seen[symbol] {
			return
		}
		seen[symbol] = true
	}
	// cfg.Symbols is the global UI/trading catalog and can contain hundreds of
	// instruments. It is not evidence that this account has traded every one.
	// Keep the config fallback for small/test installations, while production
	// analysis uses durable order/lifecycle symbols below.
	if len(cfg.Symbols) <= maxAnalysisConfiguredSymbols {
		for key, sym := range cfg.Symbols {
			if symbol, ok := deriveBinanceAnalysisSymbol(sym.Coinpair, key, ""); ok {
				add(symbol)
			}
		}
	}
	if s.Orders != nil {
		for _, rec := range s.Orders.List(10000) {
			if rec.AcceptedAt.Before(since) && rec.UpdatedAt.Before(since) {
				continue
			}
			if trading.NormalizeExchange(rec.TargetExchange) != trading.ExchangeBinance {
				continue
			}
			if symbol, ok := deriveBinanceAnalysisSymbol(rec.Result.InstID, rec.Coinpair, rec.Ticker); ok {
				add(symbol)
			}
		}
	}
	symbols := make([]string, 0, len(seen))
	for symbol := range seen {
		symbols = append(symbols, symbol)
	}
	sort.Strings(symbols)
	return symbols
}

func deriveBinanceAnalysisSymbol(candidates ...string) (string, bool) {
	for _, candidate := range candidates {
		raw := strings.ToUpper(strings.TrimSpace(candidate))
		if raw == "" {
			continue
		}
		if strings.Contains(raw, "-") {
			parts := strings.Split(raw, "-")
			if len(parts) >= 2 && analysisBinanceQuoteAsset(parts[1]) {
				raw = parts[0] + parts[1]
			}
		}
		symbol, err := binance.DeriveUSDMSymbol(raw, raw)
		if err != nil {
			continue
		}
		symbol = strings.ToUpper(strings.TrimSpace(symbol))
		if analysisBinanceSymbolSupported(symbol) {
			return symbol, true
		}
	}
	return "", false
}

func analysisBinanceSymbolSupported(symbol string) bool {
	for _, quote := range []string{"USDT", "USDC"} {
		if strings.HasSuffix(symbol, quote) && len(symbol) > len(quote) {
			return true
		}
	}
	return false
}

func analysisBinanceQuoteAsset(quote string) bool {
	for _, supported := range []string{"USDT", "USDC"} {
		if quote == supported {
			return true
		}
	}
	return false
}

func analysisTradeFromOKXFill(fill storage.OKXFill) (analysisTrade, bool) {
	instID := strings.ToUpper(strings.TrimSpace(fill.InstID))
	if instID == "" || fill.FillTime <= 0 {
		return analysisTrade{}, false
	}
	return analysisTrade{
		Exchange:  trading.ExchangeOKX,
		APIID:     strings.TrimSpace(fill.APIID),
		InstID:    instID,
		TradeID:   strings.TrimSpace(fill.TradeID),
		OrdID:     strings.TrimSpace(fill.OrdID),
		Side:      strings.TrimSpace(fill.Side),
		PosSide:   analysisOKXFillPosSide(fill),
		FillPx:    strings.TrimSpace(fill.FillPx),
		FillSz:    strings.TrimSpace(fill.FillSz),
		FillPnl:   strings.TrimSpace(fill.FillPnl),
		Fee:       strings.TrimSpace(fill.Fee),
		FeeCcy:    strings.TrimSpace(fill.FeeCcy),
		FillTime:  time.UnixMilli(fill.FillTime).UTC(),
		FillTS:    fill.FillTime,
		FillCount: 1,
	}, true
}

func analysisOKXFillPosSide(fill storage.OKXFill) string {
	var payload struct {
		PosSide string `json:"posSide"`
	}
	if strings.TrimSpace(fill.RawJSON) != "" && json.Unmarshal([]byte(fill.RawJSON), &payload) == nil {
		return normalizeAnalysisPositionSide(payload.PosSide)
	}
	return ""
}

func analysisTradeFromBinanceTrade(apiID string, trade binance.UserTrade) (analysisTrade, bool) {
	instID := strings.ToUpper(strings.TrimSpace(trade.Symbol))
	if instID == "" || trade.Time <= 0 {
		return analysisTrade{}, false
	}
	fee := normalizedBinanceFee(trade.Commission)
	return analysisTrade{
		Exchange:  trading.ExchangeBinance,
		APIID:     strings.TrimSpace(apiID),
		InstID:    instID,
		TradeID:   strconv.FormatInt(trade.ID, 10),
		OrdID:     strconv.FormatInt(trade.OrderID, 10),
		Side:      strings.TrimSpace(trade.Side),
		PosSide:   normalizeAnalysisPositionSide(trade.PositionSide),
		FillPx:    strings.TrimSpace(trade.Price),
		FillSz:    strings.TrimSpace(trade.Qty),
		FillPnl:   strings.TrimSpace(trade.RealizedPnl),
		Fee:       fee,
		FeeCcy:    strings.TrimSpace(trade.CommissionAsset),
		FillTime:  time.UnixMilli(trade.Time).UTC(),
		FillTS:    trade.Time,
		FillCount: 1,
	}, true
}

func analysisTradeFromBinanceFill(fill storage.BinanceFill) (analysisTrade, bool) {
	if strings.TrimSpace(fill.Symbol) == "" || fill.FillTime <= 0 {
		return analysisTrade{}, false
	}
	return analysisTrade{
		Exchange:  trading.ExchangeBinance,
		APIID:     strings.TrimSpace(fill.APIID),
		InstID:    strings.ToUpper(strings.TrimSpace(fill.Symbol)),
		TradeID:   strings.TrimSpace(fill.TradeID),
		OrdID:     strings.TrimSpace(fill.OrderID),
		Side:      strings.TrimSpace(fill.Side),
		PosSide:   normalizeAnalysisPositionSide(fill.PositionSide),
		FillPx:    strings.TrimSpace(fill.Price),
		FillSz:    strings.TrimSpace(fill.Qty),
		FillPnl:   strings.TrimSpace(fill.RealizedPnl),
		Fee:       normalizedBinanceFee(fill.Commission),
		FeeCcy:    strings.TrimSpace(fill.CommissionAsset),
		FillTime:  time.UnixMilli(fill.FillTime).UTC(),
		FillTS:    fill.FillTime,
		FillCount: 1,
	}, true
}

func normalizedBinanceFee(commission string) string {
	raw := strings.TrimSpace(commission)
	if raw == "" || raw == "0" {
		return raw
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		if strings.HasPrefix(raw, "-") {
			return raw
		}
		return "-" + raw
	}
	if value > 0 {
		value = -value
	}
	return trading.NormalizeFloat(value)
}

type analysisTradeAggregate struct {
	trade            analysisTrade
	totalSize        float64
	totalQuote       float64
	totalTurnover    float64
	turnoverComplete bool
	totalPnl         float64
	totalFee         float64
	feeParsed        bool
	pnlParsed        bool
}

func aggregateAnalysisTrades(trades []analysisTrade) []analysisTrade {
	if len(trades) == 0 {
		return nil
	}
	order := []string{}
	groups := map[string]*analysisTradeAggregate{}
	for _, trade := range trades {
		trade.Exchange = trading.NormalizeExchange(trade.Exchange)
		trade.APIID = strings.TrimSpace(trade.APIID)
		trade.InstID = strings.ToUpper(strings.TrimSpace(trade.InstID))
		trade.OrdID = strings.TrimSpace(trade.OrdID)
		trade.TradeID = strings.TrimSpace(trade.TradeID)
		trade.Side = strings.TrimSpace(trade.Side)
		trade.PosSide = normalizeAnalysisPositionSide(trade.PosSide)
		trade.PositionEffect = normalizeAnalysisPositionEffect(trade.PositionEffect)
		trade.Operation = normalizeAnalysisTradeOperation(trade.Operation, trade.PositionEffect)
		trade.OperationSource = normalizeAnalysisTradeSource(trade.OperationSource)
		if trade.OperationSource == "" && trade.Operation != "" {
			trade.OperationSource = "exchange"
		}
		if trade.InstID == "" || trade.FillTS <= 0 {
			continue
		}
		if trade.FillCount <= 0 {
			trade.FillCount = 1
		}
		key := analysisTradeGroupKey(trade)
		group := groups[key]
		if group == nil {
			group = &analysisTradeAggregate{trade: trade, turnoverComplete: true}
			group.trade.FillCount = 0
			groups[key] = group
			order = append(order, key)
		} else {
			mergeAnalysisTradeText(&group.trade, trade)
		}
		group.trade.FillCount += trade.FillCount
		size, sizeOK := parsePositiveFloat(trade.FillSz)
		price, priceOK := parsePositiveFloat(trade.FillPx)
		if sizeOK {
			group.totalSize += size
			if priceOK {
				group.totalQuote += price * size
			}
		}
		if turnover, turnoverOK := parsePositiveFloat(trade.Turnover); turnoverOK {
			group.totalTurnover += turnover
		} else {
			group.turnoverComplete = false
		}
		if pnl, ok := parseAnyFloat(trade.FillPnl); ok {
			group.totalPnl += pnl
			group.pnlParsed = true
		}
		if fee, ok := parseAnyFloat(trade.Fee); ok {
			group.totalFee += fee
			group.feeParsed = true
		}
		if trade.FillTS > group.trade.FillTS {
			group.trade.FillTS = trade.FillTS
			group.trade.FillTime = trade.FillTime
			group.trade.TradeID = trade.TradeID
		}
	}
	out := make([]analysisTrade, 0, len(groups))
	for _, key := range order {
		group := groups[key]
		if group == nil {
			continue
		}
		trade := group.trade
		if trade.FillCount <= 0 {
			trade.FillCount = 1
		}
		if group.totalSize > 0 {
			trade.FillSz = trading.NormalizeFloat(group.totalSize)
			if group.totalQuote > 0 {
				trade.FillPx = trading.NormalizeFloat(group.totalQuote / group.totalSize)
			}
		}
		if group.turnoverComplete && group.totalTurnover > 0 {
			trade.Turnover = trading.NormalizeFloat(group.totalTurnover)
		}
		if group.pnlParsed {
			trade.FillPnl = trading.NormalizeFloat(group.totalPnl)
		}
		if group.feeParsed {
			trade.Fee = trading.NormalizeFloat(group.totalFee)
		}
		out = append(out, trade)
	}
	return out
}

func analysisTradeGroupKey(trade analysisTrade) string {
	orderID := strings.TrimSpace(trade.OrdID)
	if orderID == "" {
		orderID = "trade:" + strings.TrimSpace(trade.TradeID)
	}
	return trading.NormalizeExchange(trade.Exchange) + "|" +
		strings.TrimSpace(trade.APIID) + "|" +
		strings.ToUpper(strings.TrimSpace(trade.InstID)) + "|" +
		strings.ToLower(strings.TrimSpace(trade.Side)) + "|" +
		orderID
}

func mergeAnalysisTradeText(dst *analysisTrade, src analysisTrade) {
	if dst.OrdID == "" {
		dst.OrdID = src.OrdID
	}
	if dst.Side == "" {
		dst.Side = src.Side
	}
	if dst.PosSide == "" {
		dst.PosSide = src.PosSide
	}
	if dst.PositionEffect == "" {
		dst.PositionEffect = src.PositionEffect
	}
	if dst.Operation == "" {
		dst.Operation = src.Operation
	}
	if dst.OperationSource == "" {
		dst.OperationSource = src.OperationSource
	}
	if dst.SourceAction == "" {
		dst.SourceAction = src.SourceAction
	}
	if dst.FillPx == "" {
		dst.FillPx = src.FillPx
	}
	if dst.FillSz == "" {
		dst.FillSz = src.FillSz
	}
	if dst.FillPnl == "" {
		dst.FillPnl = src.FillPnl
	}
	if dst.Fee == "" {
		dst.Fee = src.Fee
	}
	if dst.FeeCcy == "" {
		dst.FeeCcy = src.FeeCcy
	} else if src.FeeCcy != "" && !strings.EqualFold(dst.FeeCcy, src.FeeCcy) {
		dst.FeeCcy = ""
	}
}

type analysisOrderMeta struct {
	Leverage        int
	PositionEffect  string
	PositionSide    string
	Operation       string
	OperationSource string
	SourceAction    string
}

func enrichAnalysisTrades(cfg config.Config, trades []analysisTrade, records []storage.OrderRecord) {
	enrichAnalysisTradesWithEvents(cfg, trades, records, nil)
}

func (s *Server) analysisTradeMonitorEvents() []storage.TradeMonitorEvent {
	if s == nil || s.Orders == nil {
		return nil
	}
	events, err := s.Orders.ListTradeMonitorEvents(storage.TradeMonitorFilter{}, 1000)
	if err != nil {
		return nil
	}
	return events
}

func enrichAnalysisTradesWithEvents(cfg config.Config, trades []analysisTrade, records []storage.OrderRecord, events []storage.TradeMonitorEvent) {
	orderIndex := analysisOrderMetaIndex(records)
	eventIndex := analysisTradeMonitorEventIndex(events)
	for i := range trades {
		trade := &trades[i]
		if meta, ok := orderIndex[analysisOrderKey(trade.Exchange, trade.APIID, trade.InstID, trade.OrdID)]; ok {
			if meta.Leverage > 0 {
				trade.Leverage = meta.Leverage
			}
			if meta.PositionEffect != "" {
				trade.PositionEffect = meta.PositionEffect
			}
			if meta.PositionSide != "" {
				trade.PosSide = meta.PositionSide
			}
			if meta.Operation != "" {
				trade.Operation = meta.Operation
			}
			if meta.OperationSource != "" {
				trade.OperationSource = meta.OperationSource
			}
			if meta.SourceAction != "" {
				trade.SourceAction = meta.SourceAction
			}
		}
		if meta, ok := eventIndex[analysisOrderKey(trade.Exchange, trade.APIID, trade.InstID, trade.OrdID)]; ok {
			if meta.Operation != "" {
				trade.Operation = meta.Operation
			}
			if meta.OperationSource != "" {
				trade.OperationSource = meta.OperationSource
			}
		}
		// OKX fill history does not include leverage. Keep the recorded order
		// leverage when available, and otherwise use the configured OKX leverage
		// so an account fill without a local order record can still show ROI.
		if trade.Leverage <= 0 && trading.NormalizeExchange(trade.Exchange) == trading.ExchangeOKX && cfg.Trading.Leverage > 0 {
			trade.Leverage = cfg.Trading.Leverage
		}
		trade.PosSide = normalizeAnalysisPositionSide(trade.PosSide)
		trade.PositionEffect = normalizeAnalysisPositionEffect(trade.PositionEffect)
		trade.Operation = normalizeAnalysisTradeOperation(trade.Operation, trade.PositionEffect)
		trade.OperationSource = normalizeAnalysisTradeSource(trade.OperationSource)
		if trade.OperationSource == "" && trade.Operation != "" {
			trade.OperationSource = "exchange"
		}
		if trade.Leverage > 0 {
			if margin, ok := analysisTradeMargin(cfg, *trade); ok {
				trade.Margin = margin
			}
		}
		if strings.TrimSpace(trade.Turnover) == "" {
			if turnover := analysisTradeTurnoverValue(cfg, *trade); turnover > 0 {
				trade.Turnover = trading.NormalizeFloat(turnover)
			}
		}
		trade.NetPnL = analysisTradeNetPnL(*trade)
	}
}

func analysisOrderMetaIndex(records []storage.OrderRecord) map[string]analysisOrderMeta {
	out := map[string]analysisOrderMeta{}
	for _, rec := range records {
		exchange := trading.NormalizeExchange(rec.TargetExchange)
		if strings.TrimSpace(rec.Result.TargetExchange) != "" {
			exchange = trading.NormalizeExchange(rec.Result.TargetExchange)
		}
		instID := strings.ToUpper(strings.TrimSpace(rec.Result.InstID))
		if instID == "" {
			instID = analysisRecordInstID(exchange, rec)
		}
		leverage := rec.Result.Leverage
		if leverage <= 0 {
			leverage = rec.Leverage
		}
		if exchange == "" || instID == "" || leverage <= 0 {
			if exchange == "" || instID == "" {
				continue
			}
		}
		positionEffect := normalizeAnalysisPositionEffect(firstNonEmptyString(rec.Result.PositionEffect, rec.PositionEffect))
		positionSide := normalizeAnalysisPositionSide(firstNonEmptyString(rec.Result.PositionSide, rec.PositionSide))
		operation, operationSource := analysisOrderOperation(rec, positionEffect)
		for _, ordID := range splitAnalysisOrderIDs(rec.Result.OrdID) {
			for _, apiID := range analysisRecordAPIIDs(rec) {
				key := analysisOrderKey(exchange, apiID, instID, ordID)
				if key == "" {
					continue
				}
				if _, exists := out[key]; !exists {
					out[key] = analysisOrderMeta{
						Leverage:        leverage,
						PositionEffect:  positionEffect,
						PositionSide:    positionSide,
						Operation:       operation,
						OperationSource: operationSource,
						SourceAction:    string(rec.SourceAction),
					}
				}
			}
		}
	}
	return out
}

func analysisOrderOperation(rec storage.OrderRecord, positionEffect string) (string, string) {
	var raw struct {
		Condition   string `json:"condition"`
		Text        string `json:"text"`
		OrderIntent string `json:"order_intent"`
		Source      string `json:"source"`
	}
	if strings.TrimSpace(rec.RawJSON) != "" {
		_ = json.Unmarshal([]byte(rec.RawJSON), &raw)
	}
	intent := strings.Join([]string{
		rec.OrderIntent,
		raw.OrderIntent,
		raw.Condition,
		raw.Text,
	}, " ")
	operation := normalizeAnalysisTradeOperation(intent, positionEffect)
	source := normalizeAnalysisTradeSource(strings.Join([]string{raw.Source, raw.Condition, raw.Text}, " "))
	if source == "" && operation != "" {
		source = "auto"
	}
	return operation, source
}

func normalizeAnalysisTradeOperation(raw, positionEffect string) string {
	text := strings.ToLower(strings.TrimSpace(raw))
	switch {
	case strings.Contains(text, "stop_loss"), strings.Contains(text, "stop-loss"), strings.Contains(text, "stop loss"), strings.Contains(text, "stoploss"), strings.Contains(text, "止损"), strings.Contains(text, "止損"), strings.Contains(text, "sl"):
		return "stop_loss"
	case strings.Contains(text, "take_profit"), strings.Contains(text, "take-profit"), strings.Contains(text, "take profit"), strings.Contains(text, "takeprofit"), strings.Contains(text, "止盈"), strings.Contains(text, "tp"):
		return "take_profit"
	case strings.Contains(text, "close"), strings.Contains(text, "exit"), strings.Contains(text, "reduce"), strings.Contains(text, "平仓"), strings.Contains(text, "平倉"), strings.Contains(text, "离场"), strings.Contains(text, "離場"):
		return "close"
	case strings.Contains(text, "open"), strings.Contains(text, "entry"), strings.Contains(text, "enter"), strings.Contains(text, "开仓"), strings.Contains(text, "開倉"), strings.Contains(text, "入场"), strings.Contains(text, "入場"):
		return "open"
	}
	switch normalizeAnalysisPositionEffect(positionEffect) {
	case trading.PositionEffectOpen:
		return "open"
	case trading.PositionEffectClose:
		return "close"
	default:
		return ""
	}
}

func normalizeAnalysisTradeSource(raw string) string {
	text := strings.ToLower(strings.TrimSpace(raw))
	switch {
	case strings.Contains(text, "manual"), strings.Contains(text, "手动"), strings.Contains(text, "手動"):
		return "manual"
	case strings.Contains(text, "auto"), strings.Contains(text, "automated"), strings.Contains(text, "自动"), strings.Contains(text, "自動"), strings.Contains(text, "tradingview"), strings.Contains(text, "liquidation_alert"), strings.Contains(text, "position_monitor"), strings.Contains(text, "reentry"):
		return "auto"
	case text != "":
		return "exchange"
	default:
		return ""
	}
}

func analysisTradeMonitorEventIndex(events []storage.TradeMonitorEvent) map[string]analysisOrderMeta {
	out := map[string]analysisOrderMeta{}
	for _, event := range events {
		if strings.ToLower(strings.TrimSpace(event.EventType)) != "lifecycle_exit" && strings.ToLower(strings.TrimSpace(event.EventType)) != "lifecycle_open" {
			continue
		}
		var fill storage.BinanceFill
		if strings.TrimSpace(event.RawJSON) == "" || json.Unmarshal([]byte(event.RawJSON), &fill) != nil {
			continue
		}
		operation := ""
		switch strings.ToLower(strings.TrimSpace(event.Status)) {
		case storage.TradeLifecycleTPHit:
			operation = "take_profit"
		case storage.TradeLifecycleSLHit:
			operation = "stop_loss"
		case storage.TradeLifecycleExited:
			operation = "close"
		default:
			if strings.EqualFold(event.EventType, "lifecycle_open") {
				operation = "open"
			}
		}
		key := analysisOrderKey(event.Exchange, event.APIID, fill.Symbol, fill.OrderID)
		if key == "" || operation == "" {
			continue
		}
		if _, exists := out[key]; exists {
			continue
		}
		out[key] = analysisOrderMeta{Operation: operation, OperationSource: "auto"}
	}
	return out
}

func analysisRecordInstID(exchange string, rec storage.OrderRecord) string {
	switch trading.NormalizeExchange(exchange) {
	case trading.ExchangeBinance:
		if symbol, ok := deriveBinanceAnalysisSymbol(rec.Result.InstID, rec.Coinpair, rec.Ticker); ok {
			return symbol
		}
	case trading.ExchangeOKX:
		candidates := []string{rec.Result.InstID, rec.Coinpair, rec.Ticker}
		for _, candidate := range candidates {
			instID := strings.ToUpper(strings.TrimSpace(candidate))
			if strings.Contains(instID, "-USDT-SWAP") {
				return instID
			}
		}
	}
	return ""
}

func analysisRecordAPIIDs(rec storage.OrderRecord) []string {
	seen := map[string]bool{}
	out := []string{}
	add := func(id string) {
		id = strings.TrimSpace(id)
		if seen[id] {
			return
		}
		seen[id] = true
		out = append(out, id)
	}
	add(rec.Result.APIID)
	add(rec.APIID)
	if rec.Result.APIID == "" && rec.APIID == "" {
		add("default")
	}
	return out
}

func analysisOrderKey(exchange, apiID, instID, ordID string) string {
	exchange = trading.NormalizeExchange(exchange)
	apiID = strings.TrimSpace(apiID)
	instID = strings.ToUpper(strings.TrimSpace(instID))
	ordID = strings.TrimSpace(ordID)
	if exchange == "" || instID == "" || ordID == "" {
		return ""
	}
	return exchange + "|" + apiID + "|" + instID + "|" + ordID
}

func splitAnalysisOrderIDs(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == '/' || r == ',' || r == ';' || r == ' '
	})
	out := []string{}
	seen := map[string]bool{}
	for _, field := range fields {
		id := strings.TrimSpace(field)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

func analysisTradeMargin(cfg config.Config, trade analysisTrade) (string, bool) {
	price, priceOK := parsePositiveFloat(trade.FillPx)
	size, sizeOK := parsePositiveFloat(trade.FillSz)
	if !priceOK || !sizeOK || trade.Leverage <= 0 {
		return "", false
	}
	notional := price * size
	if trading.NormalizeExchange(trade.Exchange) == trading.ExchangeOKX {
		ctVal := analysisOKXCtVal(cfg, trade.InstID)
		if ctVal <= 0 {
			return "", false
		}
		notional *= ctVal
	}
	margin := notional / float64(trade.Leverage)
	if margin <= 0 || math.IsNaN(margin) || math.IsInf(margin, 0) {
		return "", false
	}
	return trading.NormalizeFloat(margin), true
}

func analysisDisplayPrecisionsFromOKXInstruments(instruments []okx.Instrument) map[string]symbolDisplayPrecision {
	out := make(map[string]symbolDisplayPrecision, len(instruments))
	for _, instrument := range instruments {
		instID := strings.ToUpper(strings.TrimSpace(instrument.InstID))
		if instID == "" {
			continue
		}
		out[instID] = analysisDisplayPrecision(precisionFromOKXInstrument(instrument))
	}
	return out
}

func analysisGateContractMetaFromContracts(contracts []gate.Contract) map[string]analysisGateContractMeta {
	out := make(map[string]analysisGateContractMeta, len(contracts))
	for _, contract := range contracts {
		name := strings.ToUpper(strings.TrimSpace(contract.Name))
		if name == "" {
			continue
		}
		multiplier, _ := parsePositiveFloat(contract.QuantoMultiplier)
		out[name] = analysisGateContractMeta{
			Precision:          analysisDisplayPrecision(analysisGatePricePrecision(contract.OrderPriceRound)),
			ContractMultiplier: multiplier,
		}
	}
	return out
}

func analysisGatePricePrecision(orderPriceRound string) symbolDisplayPrecision {
	return symbolDisplayPrecision{PricePrecision: boundedPrecision(decimalPlacesForDisplay(orderPriceRound))}
}

func analysisDisplayPrecision(precision symbolDisplayPrecision) symbolDisplayPrecision {
	precision.PricePrecision = analysisMinimumPricePrecision(precision.PricePrecision)
	precision.QuantityPrecision = cloneIntPtr(precision.QuantityPrecision)
	return precision
}

func analysisMinimumPricePrecision(value *int) *int {
	if value == nil {
		return nil
	}
	digits := *value
	if digits < 2 {
		digits = 2
	}
	return boundedPrecision(digits)
}

func applyAnalysisPositionDisplayData(resp *analysisResponse, cfg config.Config, okxPrecisions, binancePrecisions map[string]symbolDisplayPrecision, gateContracts map[string]analysisGateContractMeta) {
	if resp == nil {
		return
	}
	for i := range resp.PositionTrades {
		position := &resp.PositionTrades[i]
		exchange := trading.NormalizeExchange(position.Exchange)
		var precision symbolDisplayPrecision
		var found bool
		switch exchange {
		case trading.ExchangeOKX:
			precision, found = okxPrecisions[strings.ToUpper(strings.TrimSpace(position.InstID))]
		case trading.ExchangeBinance:
			precision, found = binancePrecisions[strings.ToUpper(strings.TrimSpace(position.InstID))]
		case trading.ExchangeGate:
			if meta, ok := gateContracts[strings.ToUpper(strings.TrimSpace(position.InstID))]; ok {
				precision, found = meta.Precision, true
			}
		}
		if found {
			position.PricePrecision = cloneIntPtr(precision.PricePrecision)
			position.QuantityPrecision = cloneIntPtr(precision.QuantityPrecision)
		}
		if position.Leverage <= 0 && cfg.Trading.Leverage > 0 {
			position.Leverage = cfg.Trading.Leverage
		}
		if strings.TrimSpace(position.Margin) == "" && position.Leverage > 0 {
			if qty, qtyOK := parsePositiveFloat(position.Qty); qtyOK {
				if entry, entryOK := parsePositiveFloat(position.EntryPx); entryOK {
					multiplier := 1.0
					switch exchange {
					case trading.ExchangeOKX:
						if ctVal := analysisOKXCtVal(cfg, position.InstID); ctVal > 0 {
							multiplier = ctVal
						}
					case trading.ExchangeGate:
						if meta, ok := gateContracts[strings.ToUpper(strings.TrimSpace(position.InstID))]; ok && meta.ContractMultiplier > 0 {
							multiplier = meta.ContractMultiplier
						}
					}
					margin := qty * entry * multiplier / float64(position.Leverage)
					if margin > 0 && !math.IsNaN(margin) && !math.IsInf(margin, 0) {
						position.Margin = trading.NormalizeFloat(margin)
					}
				}
			}
		}
	}
}

func analysisOKXCtVal(cfg config.Config, instID string) float64 {
	instID = strings.ToUpper(strings.TrimSpace(instID))
	for _, sym := range cfg.Symbols {
		if strings.ToUpper(strings.TrimSpace(sym.InstID)) == instID && sym.CtVal > 0 {
			return sym.CtVal
		}
	}
	return 0
}

// analysisConfigWithOKXCtVals preserves configured symbols and adds the current
// OKX contract values for historical fills whose symbols are no longer in the
// bot configuration. The returned config owns a copied Symbols map.
func analysisConfigWithOKXCtVals(cfg config.Config, instruments []okx.Instrument) config.Config {
	if len(instruments) == 0 {
		return cfg
	}
	cloned := make(map[string]config.SymbolConfig, len(cfg.Symbols)+len(instruments))
	byInstID := make(map[string]string, len(cfg.Symbols))
	for key, sym := range cfg.Symbols {
		cloned[key] = sym
		instID := strings.ToUpper(strings.TrimSpace(sym.InstID))
		if instID != "" {
			byInstID[instID] = key
		}
	}
	for _, instrument := range instruments {
		instID := strings.ToUpper(strings.TrimSpace(instrument.InstID))
		ctVal, ok := parsePositiveFloat(instrument.CtVal)
		if instID == "" || !ok {
			continue
		}
		if key, exists := byInstID[instID]; exists {
			sym := cloned[key]
			if sym.CtVal <= 0 {
				sym.CtVal = ctVal
				cloned[key] = sym
			}
			continue
		}
		cloned["analysis:"+instID] = config.SymbolConfig{InstID: instID, CtVal: ctVal}
	}
	cfg.Symbols = cloned
	return cfg
}

func analysisTradeNetPnL(trade analysisTrade) string {
	total := 0.0
	ok := false
	if pnl, parsed := parseAnyFloat(trade.FillPnl); parsed {
		total += pnl
		ok = true
	}
	if fee, parsed := parseAnyFloat(trade.Fee); parsed && (trade.FeeCcy == "" || strings.EqualFold(trade.FeeCcy, "USDT")) {
		total += fee
		ok = true
	}
	if funding, parsed := parseAnyFloat(trade.FundingFee); parsed {
		total += funding
		ok = true
	}
	if !ok {
		return ""
	}
	return trading.NormalizeFloat(total)
}

func normalizeAnalysisPositionEffect(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case trading.PositionEffectOpen, "entry", "enter":
		return trading.PositionEffectOpen
	case trading.PositionEffectClose, "exit", "reduce", "tp", "sl", "take_profit", "stop_loss":
		return trading.PositionEffectClose
	default:
		return strings.ToLower(strings.TrimSpace(raw))
	}
}

func normalizeAnalysisPositionSide(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case trading.PositionSideLong:
		return trading.PositionSideLong
	case trading.PositionSideShort:
		return trading.PositionSideShort
	case "both", "net":
		return ""
	default:
		return strings.ToLower(strings.TrimSpace(raw))
	}
}

func analysisOpeningSideFromTradeSide(side string) string {
	switch strings.ToLower(strings.TrimSpace(side)) {
	case "buy":
		return trading.PositionSideLong
	case "sell":
		return trading.PositionSideShort
	default:
		return ""
	}
}

func analysisClosingSideFromTradeSide(side string) string {
	switch strings.ToLower(strings.TrimSpace(side)) {
	case "buy":
		return trading.PositionSideShort
	case "sell":
		return trading.PositionSideLong
	default:
		return ""
	}
}

func analysisGammaDirection(sourceAction, positionSide string) string {
	source := normalizeAnalysisPositionSide(sourceAction)
	actual := normalizeAnalysisPositionSide(positionSide)
	if source == "" || actual == "" {
		return ""
	}
	if source == actual {
		return "forward"
	}
	if (source == trading.PositionSideLong || source == trading.PositionSideShort) &&
		(actual == trading.PositionSideLong || actual == trading.PositionSideShort) {
		return "reverse"
	}
	return ""
}

type analysisOpenLot struct {
	exchange          string
	apiID             string
	instID            string
	side              string
	gammaDirection    string
	entryGroupKey     string
	entryTime         time.Time
	entryTS           int64
	entryPx           float64
	qty               float64
	remaining         float64
	feeRemaining      float64
	turnoverRemaining float64
	ordID             string
	leverage          int
	fillCount         int
}

type analysisPositionTradeAccumulator struct {
	Exchange       string
	APIID          string
	InstID         string
	Side           string
	GammaDirection string
	EntryTime      time.Time
	EntryTS        int64
	ExitTime       time.Time
	ExitTS         int64
	EntryPxQty     float64
	ExitPxQty      float64
	Qty            float64
	Margin         float64
	Leverage       int
	RealizedPnL    float64
	Fee            float64
	NetPnL         float64
	Turnover       float64
	EntryOrdID     string
	ExitOrdIDs     []string
	seenExitOrdIDs map[string]bool
	seenCloseFills map[string]bool
	EntryFillCount int
	CloseFillCount int
	Remaining      float64
}

func buildAnalysisPositionTrades(cfg config.Config, trades []analysisTrade, closeSince time.Time) []analysisPositionTrade {
	if len(trades) == 0 {
		return []analysisPositionTrade{}
	}
	ordered := append([]analysisTrade(nil), trades...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].FillTS == ordered[j].FillTS {
			return ordered[i].TradeID < ordered[j].TradeID
		}
		return ordered[i].FillTS < ordered[j].FillTS
	})
	books := map[string][]*analysisOpenLot{}
	pending := map[string]*analysisPositionTradeAccumulator{}
	out := []analysisPositionTrade{}
	for _, trade := range ordered {
		qty, qtyOK := parsePositiveFloat(trade.FillSz)
		price, priceOK := parsePositiveFloat(trade.FillPx)
		if !qtyOK || !priceOK {
			continue
		}
		trade.PosSide = normalizeAnalysisPositionSide(trade.PosSide)
		trade.PositionEffect = normalizeAnalysisPositionEffect(trade.PositionEffect)
		switch trade.PositionEffect {
		case trading.PositionEffectOpen:
			positionSide := trade.PosSide
			if positionSide == "" {
				positionSide = analysisOpeningSideFromTradeSide(trade.Side)
			}
			addAnalysisOpenLot(cfg, books, pending, trade, positionSide, qty, price)
		case trading.PositionEffectClose:
			positionSide := trade.PosSide
			if positionSide == "" {
				positionSide = analysisClosingSideFromTradeSide(trade.Side)
			}
			closeAnalysisOpenLots(cfg, books, pending, trade, positionSide, qty, price, closeSince, &out)
		default:
			applyAnalysisTradeByPositionSide(cfg, books, pending, trade, qty, price, closeSince, &out)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].ExitTS == out[j].ExitTS {
			if out[i].Exchange == out[j].Exchange {
				return out[i].InstID < out[j].InstID
			}
			return out[i].Exchange < out[j].Exchange
		}
		return out[i].ExitTS > out[j].ExitTS
	})
	return out
}

func applyAnalysisTradeByPositionSide(cfg config.Config, books map[string][]*analysisOpenLot, pending map[string]*analysisPositionTradeAccumulator, trade analysisTrade, qty, price float64, closeSince time.Time, out *[]analysisPositionTrade) {
	switch trade.PosSide {
	case trading.PositionSideLong:
		if strings.EqualFold(trade.Side, "sell") {
			closeAnalysisOpenLots(cfg, books, pending, trade, trading.PositionSideLong, qty, price, closeSince, out)
			return
		}
		addAnalysisOpenLot(cfg, books, pending, trade, trading.PositionSideLong, qty, price)
	case trading.PositionSideShort:
		if strings.EqualFold(trade.Side, "buy") {
			closeAnalysisOpenLots(cfg, books, pending, trade, trading.PositionSideShort, qty, price, closeSince, out)
			return
		}
		addAnalysisOpenLot(cfg, books, pending, trade, trading.PositionSideShort, qty, price)
	default:
		applyAnalysisNetTrade(cfg, books, pending, trade, qty, price, closeSince, out)
	}
}

func applyAnalysisNetTrade(cfg config.Config, books map[string][]*analysisOpenLot, pending map[string]*analysisPositionTradeAccumulator, trade analysisTrade, qty, price float64, closeSince time.Time, out *[]analysisPositionTrade) {
	switch strings.ToLower(strings.TrimSpace(trade.Side)) {
	case "buy":
		remaining := closeAnalysisOpenLots(cfg, books, pending, trade, trading.PositionSideShort, qty, price, closeSince, out)
		if remaining > positionEntrySizeEpsilon {
			addAnalysisOpenLot(cfg, books, pending, trade, trading.PositionSideLong, remaining, price)
		}
	case "sell":
		remaining := closeAnalysisOpenLots(cfg, books, pending, trade, trading.PositionSideLong, qty, price, closeSince, out)
		if remaining > positionEntrySizeEpsilon {
			addAnalysisOpenLot(cfg, books, pending, trade, trading.PositionSideShort, remaining, price)
		}
	}
}

func addAnalysisOpenLot(cfg config.Config, books map[string][]*analysisOpenLot, pending map[string]*analysisPositionTradeAccumulator, trade analysisTrade, positionSide string, qty, price float64) {
	positionSide = normalizeAnalysisPositionSide(positionSide)
	if positionSide == "" || qty <= 0 {
		return
	}
	fee := analysisTradeUSDTFeeValue(trade)
	turnover := analysisTradeTurnoverValue(cfg, trade)
	totalQty := analysisTradeSizeValue(trade)
	if totalQty > 0 && math.Abs(qty-totalQty) > positionEntrySizeEpsilon {
		ratio := qty / totalQty
		fee *= ratio
		turnover *= ratio
	}
	key := analysisPositionBookKey(trade.Exchange, trade.APIID, trade.InstID, positionSide)
	entryGroupKey := analysisPositionEntryGroupKey(trade, positionSide)
	ensureAnalysisPositionAccumulator(pending, entryGroupKey, trade, positionSide, qty)
	books[key] = append(books[key], &analysisOpenLot{
		exchange:          trading.NormalizeExchange(trade.Exchange),
		apiID:             strings.TrimSpace(trade.APIID),
		instID:            strings.ToUpper(strings.TrimSpace(trade.InstID)),
		side:              positionSide,
		entryGroupKey:     entryGroupKey,
		entryTime:         trade.FillTime,
		entryTS:           trade.FillTS,
		entryPx:           price,
		qty:               qty,
		remaining:         qty,
		feeRemaining:      fee,
		turnoverRemaining: turnover,
		ordID:             strings.TrimSpace(trade.OrdID),
		leverage:          trade.Leverage,
		fillCount:         maxInt(1, trade.FillCount),
		gammaDirection:    analysisGammaDirection(trade.SourceAction, positionSide),
	})
}

func closeAnalysisOpenLots(cfg config.Config, books map[string][]*analysisOpenLot, pending map[string]*analysisPositionTradeAccumulator, trade analysisTrade, positionSide string, closeQty, exitPx float64, closeSince time.Time, out *[]analysisPositionTrade) float64 {
	positionSide = normalizeAnalysisPositionSide(positionSide)
	if positionSide == "" || closeQty <= 0 {
		return closeQty
	}
	key := analysisPositionBookKey(trade.Exchange, trade.APIID, trade.InstID, positionSide)
	lots := books[key]
	if len(lots) == 0 {
		return closeQty
	}
	closeFee := analysisTradeUSDTFeeValue(trade)
	closePnL := parseFloat(trade.FillPnl)
	closeTurnover := analysisTradeTurnoverValue(cfg, trade)
	remainingCloseQty := closeQty
	for remainingCloseQty > positionEntrySizeEpsilon && len(lots) > 0 {
		lot := lots[0]
		if lot.remaining <= positionEntrySizeEpsilon {
			lots = lots[1:]
			continue
		}
		matchedQty := math.Min(remainingCloseQty, lot.remaining)
		lotRatio := matchedQty / lot.remaining
		closeRatio := matchedQty / closeQty
		entryFee := lot.feeRemaining * lotRatio
		entryTurnover := lot.turnoverRemaining * lotRatio
		realizedPnL := closePnL * closeRatio
		matchedCloseFee := closeFee * closeRatio
		matchedCloseTurnover := closeTurnover * closeRatio
		totalFee := entryFee + matchedCloseFee
		totalTurnover := entryTurnover + matchedCloseTurnover
		netPnL := realizedPnL + totalFee
		applyAnalysisPositionCloseMatch(pending, lot, trade, matchedQty, exitPx, entryTurnover, realizedPnL, totalFee, netPnL, totalTurnover)
		lot.remaining -= matchedQty
		lot.feeRemaining -= entryFee
		lot.turnoverRemaining -= entryTurnover
		remainingCloseQty -= matchedQty
		finalizeAnalysisPositionAccumulator(pending, lot.entryGroupKey, closeSince, out)
		if lot.remaining <= positionEntrySizeEpsilon {
			lots = lots[1:]
		}
	}
	books[key] = lots
	return remainingCloseQty
}

func ensureAnalysisPositionAccumulator(pending map[string]*analysisPositionTradeAccumulator, entryGroupKey string, trade analysisTrade, positionSide string, qty float64) *analysisPositionTradeAccumulator {
	if pending == nil || entryGroupKey == "" || qty <= 0 {
		return nil
	}
	acc := pending[entryGroupKey]
	if acc == nil {
		acc = &analysisPositionTradeAccumulator{
			Exchange:       trading.NormalizeExchange(trade.Exchange),
			APIID:          strings.TrimSpace(trade.APIID),
			InstID:         strings.ToUpper(strings.TrimSpace(trade.InstID)),
			Side:           normalizeAnalysisPositionSide(positionSide),
			GammaDirection: analysisGammaDirection(trade.SourceAction, positionSide),
			EntryTime:      trade.FillTime,
			EntryTS:        trade.FillTS,
			Leverage:       trade.Leverage,
			EntryOrdID:     strings.TrimSpace(trade.OrdID),
			seenExitOrdIDs: map[string]bool{},
			seenCloseFills: map[string]bool{},
		}
		pending[entryGroupKey] = acc
	}
	if acc.EntryTime.IsZero() || (trade.FillTS > 0 && (acc.EntryTS <= 0 || trade.FillTS < acc.EntryTS)) {
		acc.EntryTime = trade.FillTime
		acc.EntryTS = trade.FillTS
	}
	if acc.Leverage <= 0 && trade.Leverage > 0 {
		acc.Leverage = trade.Leverage
	}
	if acc.GammaDirection == "" {
		acc.GammaDirection = analysisGammaDirection(trade.SourceAction, positionSide)
	}
	acc.Remaining += qty
	acc.EntryFillCount += maxInt(1, trade.FillCount)
	return acc
}

func applyAnalysisPositionCloseMatch(pending map[string]*analysisPositionTradeAccumulator, lot *analysisOpenLot, trade analysisTrade, matchedQty, exitPx, entryTurnover, realizedPnL, totalFee, netPnL, totalTurnover float64) {
	if pending == nil || lot == nil || lot.entryGroupKey == "" || matchedQty <= 0 {
		return
	}
	acc := pending[lot.entryGroupKey]
	if acc == nil {
		acc = &analysisPositionTradeAccumulator{
			Exchange:       lot.exchange,
			APIID:          lot.apiID,
			InstID:         lot.instID,
			Side:           lot.side,
			GammaDirection: lot.gammaDirection,
			EntryTime:      lot.entryTime,
			EntryTS:        lot.entryTS,
			Leverage:       lot.leverage,
			EntryOrdID:     lot.ordID,
			seenExitOrdIDs: map[string]bool{},
			seenCloseFills: map[string]bool{},
			EntryFillCount: maxInt(1, lot.fillCount),
			Remaining:      lot.remaining,
		}
		pending[lot.entryGroupKey] = acc
	}
	if acc.EntryTime.IsZero() || (lot.entryTS > 0 && (acc.EntryTS <= 0 || lot.entryTS < acc.EntryTS)) {
		acc.EntryTime = lot.entryTime
		acc.EntryTS = lot.entryTS
	}
	if acc.Leverage <= 0 && lot.leverage > 0 {
		acc.Leverage = lot.leverage
	}
	acc.Qty += matchedQty
	acc.EntryPxQty += lot.entryPx * matchedQty
	acc.ExitPxQty += exitPx * matchedQty
	acc.Margin += analysisPositionMarginValue(entryTurnover, lot.leverage)
	acc.RealizedPnL += realizedPnL
	acc.Fee += totalFee
	acc.NetPnL += netPnL
	acc.Turnover += totalTurnover
	if acc.ExitTime.IsZero() || trade.FillTS >= acc.ExitTS {
		acc.ExitTime = trade.FillTime
		acc.ExitTS = trade.FillTS
	}
	analysisAddExitOrdIDs(acc, trade.OrdID)
	closeFillKey := analysisPositionCloseFillKey(trade)
	if closeFillKey != "" && !acc.seenCloseFills[closeFillKey] {
		acc.seenCloseFills[closeFillKey] = true
		acc.CloseFillCount += maxInt(1, trade.FillCount)
	}
	acc.Remaining -= matchedQty
	if acc.Remaining < positionEntrySizeEpsilon {
		acc.Remaining = 0
	}
}

func finalizeAnalysisPositionAccumulator(pending map[string]*analysisPositionTradeAccumulator, entryGroupKey string, closeSince time.Time, out *[]analysisPositionTrade) {
	if pending == nil || entryGroupKey == "" {
		return
	}
	acc := pending[entryGroupKey]
	if acc == nil || acc.Remaining > positionEntrySizeEpsilon {
		return
	}
	delete(pending, entryGroupKey)
	if acc.Qty <= positionEntrySizeEpsilon || acc.ExitTime.IsZero() || acc.ExitTime.Before(closeSince) {
		return
	}
	entryPx := ""
	if acc.EntryPxQty > 0 {
		entryPx = trading.NormalizeFloat(acc.EntryPxQty / acc.Qty)
	}
	exitPx := ""
	if acc.ExitPxQty > 0 {
		exitPx = trading.NormalizeFloat(acc.ExitPxQty / acc.Qty)
	}
	margin := ""
	if acc.Margin > 0 {
		margin = trading.NormalizeFloat(acc.Margin)
	}
	*out = append(*out, analysisPositionTrade{
		Exchange:       acc.Exchange,
		APIID:          acc.APIID,
		InstID:         acc.InstID,
		Side:           acc.Side,
		GammaDirection: acc.GammaDirection,
		EntryTime:      acc.EntryTime,
		EntryTS:        acc.EntryTS,
		ExitTime:       acc.ExitTime,
		ExitTS:         acc.ExitTS,
		EntryPx:        entryPx,
		ExitPx:         exitPx,
		Qty:            trading.NormalizeFloat(acc.Qty),
		Margin:         margin,
		Leverage:       acc.Leverage,
		RealizedPnL:    trading.NormalizeFloat(acc.RealizedPnL),
		Fee:            trading.NormalizeFloat(acc.Fee),
		FeeCcy:         "USDT",
		NetPnL:         trading.NormalizeFloat(acc.NetPnL),
		Turnover:       trading.NormalizeFloat(acc.Turnover),
		EntryOrdID:     acc.EntryOrdID,
		ExitOrdID:      strings.Join(acc.ExitOrdIDs, " / "),
		FillCount:      maxInt(1, acc.EntryFillCount+acc.CloseFillCount),
	})
}

func analysisPositionEntryGroupKey(trade analysisTrade, positionSide string) string {
	entryID := strings.TrimSpace(trade.OrdID)
	prefix := "order"
	if entryID == "" {
		prefix = "lot"
		entryID = strings.TrimSpace(trade.TradeID)
		if entryID == "" {
			entryID = strings.Join([]string{
				strconv.FormatInt(trade.FillTS, 10),
				strings.TrimSpace(trade.FillPx),
				strings.TrimSpace(trade.FillSz),
			}, ":")
		}
	}
	return trading.NormalizeExchange(trade.Exchange) + "|" +
		strings.TrimSpace(trade.APIID) + "|" +
		strings.ToUpper(strings.TrimSpace(trade.InstID)) + "|" +
		normalizeAnalysisPositionSide(positionSide) + "|" +
		prefix + ":" + entryID
}

func analysisPositionCloseFillKey(trade analysisTrade) string {
	return strings.Join([]string{
		strings.TrimSpace(trade.OrdID),
		strings.TrimSpace(trade.TradeID),
		strconv.FormatInt(trade.FillTS, 10),
	}, "|")
}

func analysisAddExitOrdIDs(acc *analysisPositionTradeAccumulator, raw string) {
	if acc == nil {
		return
	}
	for _, ordID := range splitAnalysisOrderIDs(raw) {
		if acc.seenExitOrdIDs[ordID] {
			continue
		}
		acc.seenExitOrdIDs[ordID] = true
		acc.ExitOrdIDs = append(acc.ExitOrdIDs, ordID)
	}
}

func analysisPositionMarginValue(entryTurnover float64, leverage int) float64 {
	if entryTurnover <= 0 || leverage <= 0 {
		return 0
	}
	return entryTurnover / float64(leverage)
}

func analysisPositionMarginText(entryTurnover float64, leverage int) string {
	margin := analysisPositionMarginValue(entryTurnover, leverage)
	if margin <= 0 {
		return ""
	}
	return trading.NormalizeFloat(margin)
}

func analysisPositionBookKey(exchange, apiID, instID, positionSide string) string {
	return trading.NormalizeExchange(exchange) + "|" +
		strings.TrimSpace(apiID) + "|" +
		strings.ToUpper(strings.TrimSpace(instID)) + "|" +
		normalizeAnalysisPositionSide(positionSide)
}

func analysisTradeSizeValue(trade analysisTrade) float64 {
	size, ok := parsePositiveFloat(trade.FillSz)
	if !ok {
		return 0
	}
	return size
}

func analysisTradeUSDTFeeValue(trade analysisTrade) float64 {
	if !(strings.EqualFold(strings.TrimSpace(trade.FeeCcy), "USDT") || strings.TrimSpace(trade.FeeCcy) == "") {
		return 0
	}
	return parseFloat(trade.Fee)
}

func analysisTradeTurnoverValue(cfg config.Config, trade analysisTrade) float64 {
	if trading.NormalizeExchange(trade.Exchange) == trading.ExchangeGate {
		if turnover, ok := parsePositiveFloat(trade.Turnover); ok {
			return turnover
		}
	}
	price, priceOK := parsePositiveFloat(trade.FillPx)
	size, sizeOK := parsePositiveFloat(trade.FillSz)
	if !priceOK || !sizeOK {
		return 0
	}
	notional := price * size
	if trading.NormalizeExchange(trade.Exchange) == trading.ExchangeOKX {
		ctVal := analysisOKXCtVal(cfg, trade.InstID)
		if ctVal <= 0 {
			return 0
		}
		notional *= ctVal
	}
	if notional <= 0 || math.IsNaN(notional) || math.IsInf(notional, 0) {
		return 0
	}
	return notional
}

func parseAnyFloat(v string) (float64, bool) {
	parsed, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	return parsed, err == nil
}

func parsePositiveFloat(v string) (float64, bool) {
	parsed, ok := parseAnyFloat(v)
	return parsed, ok && parsed > 0
}

func (s *Server) analysisFromStore(cfg config.Config, apiID, binanceAPIID, gateAPIID, envName string, priceDays, pnlMinutes int, now time.Time, source analysisSourceStatus, binanceTrades, gateTrades []analysisTrade, gatePositions []analysisPositionTrade) (analysisResponse, error) {
	priceLimit := priceDays * 24
	balanceLimit := priceDays*24*60 + 1
	priceSince := now.AddDate(0, 0, -priceDays)
	pnlMinutes = normalizeAnalysisPNLMinutes(pnlMinutes)
	pnlDays := analysisPNLDaysForMinutes(pnlMinutes)
	pnlSince := analysisPNLSince(now, pnlMinutes)
	positionSince := analysisPositionSince(now, pnlMinutes)
	candles, err := s.Orders.ListMarketCandles(analysisPriceInstID, analysisPriceBar, priceSince, priceLimit)
	if err != nil {
		return analysisResponse{}, err
	}
	points := make([]analysisPricePoint, 0, len(candles))
	for _, candle := range candles {
		points = append(points, analysisPricePoint{
			Time:    time.UnixMilli(candle.TS).UTC(),
			TS:      candle.TS,
			Open:    candle.Open,
			High:    candle.High,
			Low:     candle.Low,
			Close:   candle.Close,
			Confirm: candle.Confirm,
		})
	}
	snapshots, err := s.Orders.ListExchangeUSDTBalanceSnapshots(trading.ExchangeOKX, apiID, envName, priceSince, balanceLimit)
	if err != nil {
		return analysisResponse{}, err
	}
	balancePoints := balancePointsFromSnapshots(snapshots)
	fills, err := s.Orders.ListOKXFills(apiID, positionSince)
	if err != nil {
		return analysisResponse{}, err
	}
	trades := make([]analysisTrade, 0, len(fills)+len(binanceTrades)+len(gateTrades))
	for _, fill := range fills {
		trade, ok := analysisTradeFromOKXFill(fill)
		if ok {
			trades = append(trades, trade)
		}
	}
	trades = append(trades, binanceTrades...)
	positionTradesInput := append([]analysisTrade(nil), trades...)
	trades = append(trades, gateTrades...)
	trades = aggregateAnalysisTrades(trades)
	records := s.Orders.List(10000)
	events := s.analysisTradeMonitorEvents()
	enrichAnalysisTradesWithEvents(cfg, trades, records, events)
	enrichAnalysisTradesWithEvents(cfg, positionTradesInput, records, events)
	sortAnalysisTrades(trades)
	positionTrades := buildAnalysisPositionTrades(cfg, positionTradesInput, pnlSince)
	positionTrades = append(positionTrades, gatePositions...)
	sortAnalysisPositionTrades(positionTrades)
	summary, exchangeSummaries, symbols := computeStats(cfg, positionTrades, trades, pnlSince)
	return analysisResponse{
		OK:           true,
		APIID:        apiID,
		BinanceAPIID: binanceAPIID,
		GateAPIID:    gateAPIID,
		Env:          envName,
		PriceDays:    priceDays,
		PNLDays:      pnlDays,
		PNLMinutes:   pnlMinutes,
		PriceInstID:  analysisPriceInstID,
		PriceBar:     analysisPriceBar,
		RefreshedAt:  now.UTC(),
		Cache: analysisCacheStatus{
			Hit:      false,
			Stale:    false,
			CacheKey: analysisCacheKey(apiID, binanceAPIID, envName, priceDays, pnlMinutes, gateAPIID),
		},
		Source:            source,
		PricePoints:       points,
		BalancePoints:     balancePoints,
		Summary:           summary,
		ExchangeSummaries: exchangeSummaries,
		Symbols:           symbols,
		Trades:            trades,
		PositionTrades:    positionTrades,
	}, nil
}

func (s *Server) analysisFromExternalData(cfg config.Config, apiID, binanceAPIID, gateAPIID, envName string, priceDays, pnlMinutes int, now time.Time, source analysisSourceStatus, balancePoints []analysisBalancePoint, binanceTrades, gateTrades []analysisTrade, gatePositions []analysisPositionTrade) analysisResponse {
	pnlMinutes = normalizeAnalysisPNLMinutes(pnlMinutes)
	pnlDays := analysisPNLDaysForMinutes(pnlMinutes)
	pnlSince := analysisPNLSince(now, pnlMinutes)
	trades := make([]analysisTrade, 0, len(binanceTrades)+len(gateTrades))
	trades = append(trades, binanceTrades...)
	trades = append(trades, gateTrades...)
	trades = aggregateAnalysisTrades(trades)
	positionTradesInput := append([]analysisTrade(nil), binanceTrades...)
	records := s.Orders.List(10000)
	events := s.analysisTradeMonitorEvents()
	enrichAnalysisTradesWithEvents(cfg, trades, records, events)
	enrichAnalysisTradesWithEvents(cfg, positionTradesInput, records, events)
	sortAnalysisTrades(trades)
	positionTrades := buildAnalysisPositionTrades(cfg, positionTradesInput, pnlSince)
	positionTrades = append(positionTrades, gatePositions...)
	sortAnalysisPositionTrades(positionTrades)
	summary, exchangeSummaries, symbols := computeStats(cfg, positionTrades, trades, pnlSince)
	return analysisResponse{
		OK:                true,
		APIID:             apiID,
		BinanceAPIID:      binanceAPIID,
		GateAPIID:         gateAPIID,
		Env:               envName,
		PriceDays:         priceDays,
		PNLDays:           pnlDays,
		PNLMinutes:        pnlMinutes,
		PriceInstID:       analysisPriceInstID,
		PriceBar:          analysisPriceBar,
		RefreshedAt:       now.UTC(),
		Source:            source,
		BalancePoints:     balancePoints,
		Summary:           summary,
		ExchangeSummaries: exchangeSummaries,
		Symbols:           symbols,
		Trades:            trades,
		PositionTrades:    positionTrades,
	}
}

func (s *Server) balanceOverviewForOKX(ctx context.Context, cfg config.Config, envName, requestedAPIID string, minutes int, now time.Time) exchangeBalanceOverview {
	out := exchangeBalanceOverview{Exchange: trading.ExchangeOKX, Label: "OKX", Status: "not_configured", RefreshedAt: now.UTC()}
	okxStore := s.okxCredentialStore(cfg.Trading.Env)
	if okxStore == nil {
		out.Error = "OKX credential store is not configured"
		return out
	}
	status := okxStore.Status()
	out.Configured = status.Configured
	out.APIID = strings.TrimSpace(requestedAPIID)
	if out.APIID == "" {
		out.APIID = strings.TrimSpace(status.ActiveID)
	}
	if !status.Configured {
		return out
	}
	creds, apiID, err := okxStore.OKXCredentials(out.APIID)
	if err != nil {
		out.Status = "error"
		out.Error = err.Error()
		return out
	}
	out.APIID = apiID
	fetchCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	balance, err := s.fetchAnalysisBalance(fetchCtx, s.analysisOKXClient(cfg, creds), apiID, envName, now)
	cancel()
	out.BalancePoints = s.balanceOverviewPoints(trading.ExchangeOKX, apiID, envName, minutes, now)
	if err != nil {
		out.Status = "error"
		out.Error = err.Error()
		if cached, ok := analysisBalanceFromLatestPoint(out.BalancePoints); ok {
			out.Status = "stale"
			out.Balance = cached
		}
	} else {
		out.Status = "ok"
		out.Balance = balance
	}
	out.Window = balanceWindowStats(out.BalancePoints, out.Balance)
	return out
}

func (s *Server) balanceOverviewForBinance(ctx context.Context, cfg config.Config, envName, requestedAPIID string, minutes int, now time.Time) exchangeBalanceOverview {
	out := exchangeBalanceOverview{Exchange: trading.ExchangeBinance, Label: "Binance", Status: "not_configured", RefreshedAt: now.UTC()}
	binanceStore := s.binanceCredentialStore(cfg.Trading.Env)
	if binanceStore == nil {
		out.Error = "Binance credential store is not configured"
		return out
	}
	status := binanceStore.Status()
	out.Configured = status.Configured
	out.APIID = strings.TrimSpace(requestedAPIID)
	if out.APIID == "" {
		out.APIID = strings.TrimSpace(status.ActiveID)
	}
	if !status.Configured {
		return out
	}
	creds, apiID, err := binanceStore.BinanceCredentials(out.APIID)
	if err != nil {
		out.Status = "error"
		out.Error = err.Error()
		return out
	}
	out.APIID = apiID
	fetchCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	balance, err := s.fetchBinanceAnalysisBalance(fetchCtx, s.analysisBinanceClient(cfg, creds), apiID, envName, now)
	cancel()
	out.BalancePoints = s.balanceOverviewPoints(trading.ExchangeBinance, apiID, envName, minutes, now)
	if err != nil {
		out.Status = "error"
		out.Error = err.Error()
		if cached, ok := analysisBalanceFromLatestPoint(out.BalancePoints); ok {
			out.Status = "stale"
			out.Balance = cached
		}
	} else {
		out.Status = "ok"
		out.Balance = balance
	}
	out.Window = balanceWindowStats(out.BalancePoints, out.Balance)
	return out
}

func (s *Server) balanceOverviewForGate(ctx context.Context, cfg config.Config, envName, requestedAPIID string, minutes int, now time.Time) exchangeBalanceOverview {
	out := exchangeBalanceOverview{Exchange: trading.ExchangeGate, Label: "Gate", Status: "not_configured", RefreshedAt: now.UTC()}
	gateStore := s.gateCredentialStore(cfg.Trading.Env)
	if gateStore == nil {
		out.Error = "Gate credential store is not configured"
		return out
	}
	status := gateStore.Status()
	out.Configured = status.Configured
	out.APIID = strings.TrimSpace(requestedAPIID)
	if out.APIID == "" {
		out.APIID = strings.TrimSpace(status.ActiveID)
	}
	if !status.Configured {
		return out
	}
	creds, apiID, err := gateStore.GateCredentials(out.APIID)
	if err != nil {
		out.Status = "error"
		out.Error = err.Error()
		return out
	}
	out.APIID = apiID
	fetchCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	account, err := (gate.Client{BaseURL: cfg.GateBaseURL(), Credentials: creds, HTTPClient: s.GateHTTPClient}).Account(fetchCtx)
	cancel()
	out.BalancePoints = s.balanceOverviewPoints(trading.ExchangeGate, apiID, envName, minutes, now)
	if err != nil {
		out.Status = "error"
		out.Error = err.Error()
		if cached, ok := analysisBalanceFromLatestPoint(out.BalancePoints); ok {
			out.Status = "stale"
			out.Balance = cached
		}
	} else {
		out.Status = "ok"
		out.Balance = analysisBalanceFromGate(account, now)
	}
	out.Window = balanceWindowStats(out.BalancePoints, out.Balance)
	return out
}

func analysisBalanceFromGate(account gate.Account, now time.Time) analysisBalance {
	updatedAt := now.UTC().Format(time.RFC3339Nano)
	currency := strings.TrimSpace(account.Currency)
	if currency == "" {
		currency = "USDT"
	}
	equity := gateUSDTEq(account)
	return analysisBalance{
		TotalEq: equity, AvailEq: account.Available, Currency: currency, UpdatedAt: updatedAt,
		Details: []analysisBalanceDetail{{
			Ccy: currency, Eq: equity, EqUsd: equity, AvailBal: account.Available,
			AvailEq: account.Available, UpdatedAt: updatedAt,
		}},
	}
}

// gateUSDTEq returns the mark-to-market futures equity. Gate's total balance
// excludes unrealised position P&L, so the chart must add it explicitly.
func gateUSDTEq(account gate.Account) string {
	rawTotal := strings.TrimSpace(account.Total)
	rawUnrealised := strings.TrimSpace(account.UnrealisedPnl)
	if rawUnrealised == "" {
		return rawTotal
	}
	total, ok := new(big.Rat).SetString(rawTotal)
	if !ok {
		return rawTotal
	}
	unrealised, ok := new(big.Rat).SetString(rawUnrealised)
	if !ok {
		return rawTotal
	}
	decimals := decimalsFromDecimalString(rawTotal)
	if unrealisedDecimals := decimalsFromDecimalString(rawUnrealised); unrealisedDecimals > decimals {
		decimals = unrealisedDecimals
	}
	total.Add(total, unrealised)
	return trimDecimalZeros(total.FloatString(decimals))
}

func analysisBalanceFromLatestPoint(points []analysisBalancePoint) (analysisBalance, bool) {
	if len(points) == 0 {
		return analysisBalance{}, false
	}
	point := points[len(points)-1]
	updatedAt := strings.TrimSpace(point.BalanceUpdatedAt)
	if updatedAt == "" && !point.ObservedAt.IsZero() {
		updatedAt = point.ObservedAt.UTC().Format(time.RFC3339Nano)
	}
	equity := strings.TrimSpace(point.Eq)
	if equity == "" {
		equity = strings.TrimSpace(point.EqUsd)
	}
	if equity == "" && point.Value != 0 {
		equity = strconv.FormatFloat(point.Value, 'f', -1, 64)
	}
	equityUSD := strings.TrimSpace(point.EqUsd)
	if equityUSD == "" {
		equityUSD = equity
	}
	return analysisBalance{
		TotalEq:   equity,
		AvailEq:   strings.TrimSpace(point.AvailEq),
		Currency:  "USDT",
		UpdatedAt: updatedAt,
		Details: []analysisBalanceDetail{{
			Ccy:       "USDT",
			Eq:        equity,
			EqUsd:     equityUSD,
			AvailBal:  strings.TrimSpace(point.AvailBal),
			AvailEq:   strings.TrimSpace(point.AvailEq),
			CashBal:   strings.TrimSpace(point.CashBal),
			FrozenBal: strings.TrimSpace(point.FrozenBal),
			UpdatedAt: updatedAt,
		}},
	}, true
}

func balanceWindowStats(points []analysisBalancePoint, balance analysisBalance) analysisBalanceWindow {
	values := make([]analysisBalancePoint, 0, len(points)+1)
	for _, point := range points {
		if !math.IsNaN(point.Value) && !math.IsInf(point.Value, 0) {
			values = append(values, point)
		}
	}
	if len(values) == 0 {
		if current, ok := analysisCurrentBalanceValue(balance); ok {
			now := parseAnalysisBalanceUpdatedAt(balance)
			values = append(values, analysisBalancePoint{Time: now, TS: now.UnixMilli(), Value: current})
		}
	}
	if len(values) == 0 {
		return analysisBalanceWindow{}
	}
	start := values[0]
	current := values[len(values)-1]
	window := analysisBalanceWindow{
		StartValue:   start.Value,
		CurrentValue: current.Value,
		Change:       current.Value - start.Value,
		StartTime:    balancePointTime(start),
		CurrentTime:  balancePointTime(current),
	}
	if start.Value != 0 {
		window.ChangePct = window.Change / math.Abs(start.Value)
	}
	peak := values[0].Value
	for _, point := range values[1:] {
		if point.Value > peak {
			peak = point.Value
			continue
		}
		drawdown := peak - point.Value
		if drawdown > window.MaxDrawdown {
			window.MaxDrawdown = drawdown
			if peak != 0 {
				window.MaxDrawdownPct = drawdown / math.Abs(peak)
			}
		}
	}
	return window
}

func analysisCurrentBalanceValue(balance analysisBalance) (float64, bool) {
	for _, detail := range balance.Details {
		if !strings.EqualFold(strings.TrimSpace(detail.Ccy), "USDT") {
			continue
		}
		value, ok := parseAnyFloat(detail.Eq)
		return value, ok
	}
	value, ok := parseAnyFloat(balance.TotalEq)
	return value, ok
}

func parseAnalysisBalanceUpdatedAt(balance analysisBalance) time.Time {
	if strings.TrimSpace(balance.UpdatedAt) != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(balance.UpdatedAt)); err == nil {
			return parsed.UTC()
		}
	}
	return time.Now().UTC()
}

func balancePointTime(point analysisBalancePoint) time.Time {
	if !point.Time.IsZero() {
		return point.Time.UTC()
	}
	if point.TS > 0 {
		return time.UnixMilli(point.TS).UTC()
	}
	return time.Time{}
}

func (s *Server) balanceOverviewPoints(exchange, apiID, envName string, minutes int, now time.Time) []analysisBalancePoint {
	if s.Orders == nil || strings.TrimSpace(apiID) == "" {
		return nil
	}
	if minutes < 0 {
		minutes = 0
	}
	if minutes > maxBalanceMinutes {
		minutes = maxBalanceMinutes
	}
	limit := minutes + 1
	since := now.Add(-time.Duration(minutes) * time.Minute)
	if minutes == 0 {
		limit = 6
		since = now.Add(-5 * time.Minute)
	}
	snapshots, err := s.Orders.ListExchangeUSDTBalanceSnapshots(exchange, apiID, envName, since, limit)
	if err != nil {
		if s.Logger != nil {
			s.Logger.Warn("failed to list USDT balance snapshots", "exchange", exchange, "api_id", apiID, "env", envName, "error", err)
		}
		return nil
	}
	return compactBalancePoints(balancePointsFromSnapshots(snapshots), minutes)
}

func balancePointsFromSnapshots(snapshots []storage.USDTBalanceSnapshot) []analysisBalancePoint {
	balancePoints := make([]analysisBalancePoint, 0, len(snapshots))
	for _, snapshot := range snapshots {
		balancePoints = append(balancePoints, analysisBalancePoint{
			Time:             time.UnixMilli(snapshot.BucketTS).UTC(),
			TS:               snapshot.BucketTS,
			Value:            snapshotBalanceValue(snapshot),
			Eq:               snapshot.Eq,
			EqUsd:            snapshot.EqUsd,
			AvailEq:          snapshot.AvailEq,
			AvailBal:         snapshot.AvailBal,
			CashBal:          snapshot.CashBal,
			FrozenBal:        snapshot.FrozenBal,
			ObservedAt:       snapshot.ObservedAt,
			BalanceUpdatedAt: snapshot.BalanceUpdatedAt,
		})
	}
	return balancePoints
}

func compactBalancePoints(points []analysisBalancePoint, minutes int) []analysisBalancePoint {
	if len(points) == 0 {
		return nil
	}
	if minutes <= 0 {
		return []analysisBalancePoint{points[len(points)-1]}
	}
	bucket := balancePointBucket(minutes)
	if bucket <= time.Minute {
		return points
	}
	out := make([]analysisBalancePoint, 0, len(points))
	lastBucket := int64(-1)
	for _, point := range points {
		ts := point.TS
		if ts <= 0 && !point.Time.IsZero() {
			ts = point.Time.UnixMilli()
		}
		if ts <= 0 {
			continue
		}
		bucketTS := time.UnixMilli(ts).UTC().Truncate(bucket).UnixMilli()
		point.TS = bucketTS
		point.Time = time.UnixMilli(bucketTS).UTC()
		if len(out) > 0 && bucketTS == lastBucket {
			out[len(out)-1] = point
			continue
		}
		out = append(out, point)
		lastBucket = bucketTS
	}
	return out
}

func balancePointBucket(minutes int) time.Duration {
	if minutes >= 30*24*60 {
		return time.Hour
	}
	return time.Minute
}

func snapshotBalanceValue(snapshot storage.USDTBalanceSnapshot) float64 {
	raw := strings.TrimSpace(snapshot.Eq)
	if raw == "" {
		return 0
	}
	parsed, err := strconv.ParseFloat(raw, 64)
	if err == nil {
		return parsed
	}
	return 0
}

func computeStats(cfg config.Config, positions []analysisPositionTrade, periodTrades []analysisTrade, periodSince time.Time) (analysisSymbolStats, []analysisSymbolStats, []analysisSymbolStats) {
	bySymbol := map[string]*analysisSymbolStats{}
	byExchange := map[string]*analysisSymbolStats{}
	summary := analysisSymbolStats{InstID: "ALL"}
	ensure := func(exchange, instID string) (*analysisSymbolStats, *analysisSymbolStats) {
		exchange = trading.NormalizeExchange(exchange)
		instID = strings.ToUpper(strings.TrimSpace(instID))
		if instID == "" {
			return nil, nil
		}
		key := exchange + "|" + instID
		stats := bySymbol[key]
		if stats == nil {
			stats = &analysisSymbolStats{Exchange: exchange, InstID: instID}
			bySymbol[key] = stats
		}
		exchangeStats := byExchange[exchange]
		if exchangeStats == nil {
			exchangeStats = &analysisSymbolStats{Exchange: exchange, InstID: "ALL"}
			byExchange[exchange] = exchangeStats
		}
		return stats, exchangeStats
	}
	for _, position := range positions {
		stats, exchangeStats := ensure(position.Exchange, position.InstID)
		if stats == nil {
			continue
		}
		net := parseFloat(position.NetPnL)
		applyPositionStats(stats, net)
		applyPositionStats(exchangeStats, net)
		applyPositionStats(&summary, net)
	}
	for _, trade := range periodTrades {
		if trade.FillTime.Before(periodSince) {
			continue
		}
		stats, exchangeStats := ensure(trade.Exchange, trade.InstID)
		if stats == nil {
			continue
		}
		fee := analysisTradeUSDTFeeValue(trade)
		turnover := analysisTradeTurnoverValue(cfg, trade)
		applyPeriodFillStats(stats, fee, turnover)
		applyPeriodFillStats(exchangeStats, fee, turnover)
		applyPeriodFillStats(&summary, fee, turnover)
	}
	symbols := make([]analysisSymbolStats, 0, len(bySymbol))
	for _, stats := range bySymbol {
		finalizeStats(stats)
		symbols = append(symbols, *stats)
	}
	exchangeSummaries := make([]analysisSymbolStats, 0, len(byExchange))
	for _, stats := range byExchange {
		finalizeStats(stats)
		exchangeSummaries = append(exchangeSummaries, *stats)
	}
	finalizeStats(&summary)
	sortSymbolStats(symbols)
	sortExchangeSummaries(exchangeSummaries)
	return summary, exchangeSummaries, symbols
}

func applyPositionStats(stats *analysisSymbolStats, net float64) {
	stats.TradeCount++
	stats.NetPnL += net
	switch {
	case net > 0:
		stats.Wins++
		stats.GrossProfit += net
	case net < 0:
		stats.Losses++
		stats.GrossLoss += net
	default:
		stats.Flats++
	}
}

func applyPeriodFillStats(stats *analysisSymbolStats, fee, turnover float64) {
	stats.Fees += fee
	stats.Turnover += turnover
}

func finalizeStats(stats *analysisSymbolStats) {
	decided := stats.Wins + stats.Losses
	if decided > 0 {
		stats.WinRate = float64(stats.Wins) / float64(decided)
	}
	if stats.GrossLoss < 0 {
		stats.ProfitFactor = stats.GrossProfit / math.Abs(stats.GrossLoss)
		if stats.Wins > 0 && stats.Losses > 0 {
			stats.PayoffRatio = (stats.GrossProfit / float64(stats.Wins)) / math.Abs(stats.GrossLoss/float64(stats.Losses))
		}
	} else if stats.GrossProfit > 0 {
		stats.ProfitFactorText = "∞"
	}
}

func sortSymbolStats(symbols []analysisSymbolStats) {
	for i := 0; i < len(symbols); i++ {
		for j := i + 1; j < len(symbols); j++ {
			if symbols[j].NetPnL > symbols[i].NetPnL ||
				(symbols[j].NetPnL == symbols[i].NetPnL && symbols[j].Exchange < symbols[i].Exchange) ||
				(symbols[j].NetPnL == symbols[i].NetPnL && symbols[j].Exchange == symbols[i].Exchange && symbols[j].InstID < symbols[i].InstID) {
				symbols[i], symbols[j] = symbols[j], symbols[i]
			}
		}
	}
}

func sortExchangeSummaries(summaries []analysisSymbolStats) {
	order := map[string]int{trading.ExchangeOKX: 0, trading.ExchangeBinance: 1}
	sort.SliceStable(summaries, func(i, j int) bool {
		left, leftOK := order[summaries[i].Exchange]
		right, rightOK := order[summaries[j].Exchange]
		if leftOK && rightOK {
			return left < right
		}
		if leftOK != rightOK {
			return leftOK
		}
		return summaries[i].Exchange < summaries[j].Exchange
	})
}

func sortAnalysisTrades(trades []analysisTrade) {
	sort.SliceStable(trades, func(i, j int) bool {
		if trades[i].FillTS == trades[j].FillTS {
			if trades[i].Exchange == trades[j].Exchange {
				if trades[i].InstID == trades[j].InstID {
					return trades[i].TradeID > trades[j].TradeID
				}
				return trades[i].InstID < trades[j].InstID
			}
			return trades[i].Exchange < trades[j].Exchange
		}
		return trades[i].FillTS > trades[j].FillTS
	})
}

func parseFloat(v string) float64 {
	parsed, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil {
		return 0
	}
	return parsed
}

func positiveIntQuery(r *http.Request, name string, fallback int) int {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func (s *Server) okxHTTPClient() *http.Client {
	if s.OKXHTTPClient != nil {
		return s.OKXHTTPClient
	}
	return &http.Client{Timeout: 15 * time.Second}
}

func (s *Server) binanceHTTPClient() *http.Client {
	if s.BinanceHTTPClient != nil {
		return s.BinanceHTTPClient
	}
	return &http.Client{Timeout: 15 * time.Second}
}
