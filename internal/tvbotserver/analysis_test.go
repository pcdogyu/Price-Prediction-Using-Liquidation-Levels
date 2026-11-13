package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotbinance"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotconfig"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotgate"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotokx"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotstorage"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

type analysisRoundTripFunc func(*http.Request) (*http.Response, error)

func (f analysisRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestAnalysisTradeOperationMetadataClassifiesOrderAndMonitorActions(t *testing.T) {
	record := storage.OrderRecord{
		APIID:          "binance-main",
		TargetExchange: trading.ExchangeBinance,
		Coinpair:       "ETHUSDT",
		OrderIntent:    "sl_long",
		PositionEffect: trading.PositionEffectClose,
		MarketStrategy: trading.MarketStrategyScalp,
		RawJSON:        `{"source":"liquidation_alert"}`,
		Result: trading.OrderResult{
			TargetExchange: trading.ExchangeBinance,
			APIID:          "binance-main",
			InstID:         "ETHUSDT",
			OrdID:          "entry-1",
			Leverage:       10,
		},
	}
	operation, source := analysisOrderOperation(record, record.PositionEffect)
	if operation != "stop_loss" || source != "auto" {
		t.Fatalf("unexpected order metadata operation=%q source=%q", operation, source)
	}
	meta, ok := analysisOrderMetaIndex([]storage.OrderRecord{record})[analysisOrderKey(trading.ExchangeBinance, record.APIID, "ETHUSDT", "entry-1")]
	if !ok || meta.Strategy != trading.MarketStrategyScalp {
		t.Fatalf("market strategy should be preferred for analysis metadata: %#v found=%v", meta, ok)
	}
	record.MarketStrategy = ""
	record.RawJSON = `{"condition":"value_area_signal","source":"value_area_signal"}`
	meta, ok = analysisOrderMetaIndex([]storage.OrderRecord{record})[analysisOrderKey(trading.ExchangeBinance, record.APIID, "ETHUSDT", "entry-1")]
	if !ok || meta.Strategy != "value_area" {
		t.Fatalf("signal source should provide a fallback strategy: %#v found=%v", meta, ok)
	}
	for _, test := range []struct {
		name, raw, want string
	}{
		{name: "tradingview", raw: `{"source":"tradingview","condition":"绿1开多成交"}`, want: "tradingview"},
		{name: "value area manual", raw: `{"source":"value_area_manual","condition":"value_area_manual"}`, want: "value_area"},
		{name: "position monitor", raw: `{"source":"position_monitor"}`, want: "position_monitor"},
		{name: "stop loss webhook", raw: `{"source":"stop_loss_webhook"}`, want: "stop_loss_webhook"},
		{name: "take profit webhook", raw: `{"source":"take_profit_webhook"}`, want: "take_profit_webhook"},
		{name: "analysis manual", raw: `{"source":"analysis_manual"}`, want: "analysis_manual"},
	} {
		t.Run(test.name, func(t *testing.T) {
			record.MarketStrategy = ""
			record.RawJSON = test.raw
			meta, ok := analysisOrderMetaIndex([]storage.OrderRecord{record})[analysisOrderKey(trading.ExchangeBinance, record.APIID, "ETHUSDT", "entry-1")]
			if !ok || meta.Strategy != test.want {
				t.Fatalf("source fallback strategy=%q want=%q metadata=%#v found=%v", meta.Strategy, test.want, meta, ok)
			}
		})
	}

	f := storage.BinanceFill{Symbol: "ETHUSDT", OrderID: "exit-1", FillTime: time.Now().UnixMilli()}
	raw, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	events := []storage.TradeMonitorEvent{{
		Exchange:  trading.ExchangeBinance,
		APIID:     "binance-main",
		EventType: "lifecycle_exit",
		Status:    storage.TradeLifecycleTPHit,
		RawJSON:   string(raw),
	}}
	meta, ok = analysisTradeMonitorEventIndex(events)[analysisOrderKey(trading.ExchangeBinance, "binance-main", "ETHUSDT", "exit-1")]
	if !ok || meta.Operation != "take_profit" || meta.OperationSource != "monitor" {
		t.Fatalf("unexpected monitor metadata: %#v found=%v", meta, ok)
	}
}

func TestBuildAnalysisUsesStaleCacheWhenOKXBalanceIsUnavailable(t *testing.T) {
	srv := newTestServer(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v5/account/balance" {
			t.Fatalf("unexpected OKX path %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"code":"50001","data":[],"msg":"Service temporarily unavailable. Please try again later."}`))
	}))
	defer upstream.Close()

	cfg := srv.ConfigStore.Get()
	cfg.Trading.BaseURL = upstream.URL
	srv.ConfigStore = config.NewStore("", cfg)
	srv.OKXHTTPClient = upstream.Client()
	if _, err := srv.OKXCredentials.UpdateAccount(okx.CredentialAccountUpdate{
		ID:     "default",
		Active: true,
		Credentials: okx.Credentials{
			APIKey:     "key",
			SecretKey:  "secret",
			Passphrase: "pass",
		},
	}); err != nil {
		t.Fatal(err)
	}

	cachedAt := srv.now().Add(-15 * time.Minute)
	cacheKey := analysisCacheKey("default", "", analysisEnvName(cfg), 3, 12*60)
	want := analysisResponse{
		OK:          true,
		APIID:       "default",
		Env:         analysisEnvName(cfg),
		PriceDays:   3,
		PNLMinutes:  12 * 60,
		RefreshedAt: cachedAt,
		Balance:     analysisBalance{TotalEq: "9776.287875"},
	}
	if err := srv.Orders.CachePayload(cacheKey, want, cachedAt); err != nil {
		t.Fatal(err)
	}

	got, err := srv.buildAnalysis(context.Background(), cfg, "", "", 3, 12*60, true)
	if err != nil {
		t.Fatalf("build analysis should use stale cache: %v", err)
	}
	if !got.Cache.Hit || !got.Cache.Stale || !got.Cache.CachedAt.Equal(cachedAt) || got.Cache.CacheKey != cacheKey {
		t.Fatalf("bad stale cache metadata: %#v", got.Cache)
	}
	if got.Balance.TotalEq != want.Balance.TotalEq || got.RefreshedAt != want.RefreshedAt {
		t.Fatalf("cached response was not preserved: %#v", got)
	}
	if got.Source.Balance != "cache" || got.Source.Price != "cache" || got.Source.Fills != "cache" || got.Source.Funding != "cache" {
		t.Fatalf("bad stale cache source: %#v", got.Source)
	}
}

func TestAnalysisCacheRejectsResponsesWithUpstreamErrors(t *testing.T) {
	valid := analysisResponse{
		OK: true,
		Source: analysisSourceStatus{
			Balance: "binance",
			Fills:   "binance",
			Funding: "binance",
		},
	}
	if !analysisResponseCacheable(valid) {
		t.Fatal("successful analysis response should be cacheable")
	}
	for _, source := range []analysisSourceStatus{
		{Fills: "binance_error"},
		{Fills: "okx+binance_error"},
		{Funding: "binance_error"},
		{Balance: "gate_error"},
	} {
		if analysisResponseCacheable(analysisResponse{OK: true, Source: source}) {
			t.Fatalf("analysis response with upstream error should not be cacheable: %#v", source)
		}
	}
	if analysisResponseCacheable(analysisResponse{Source: valid.Source}) {
		t.Fatal("analysis response without OK marker should not be cacheable")
	}
}

func TestReadOnlyAnalysisWithoutExchangeCredentialsReturnsEmptyData(t *testing.T) {
	srv := newTestServer(t)
	cfg := srv.ConfigStore.Get()
	if srv.anyExchangeConfigured() {
		t.Fatal("test account should have no exchange credentials configured")
	}

	cacheKey, err := srv.analysisCacheKeyForRequest(cfg, "", "", "", 1, 30*24*60)
	if err != nil {
		t.Fatalf("read-only analysis should not require a selected exchange: %v", err)
	}
	if !strings.Contains(cacheKey, "read-only-no-exchange") {
		t.Fatalf("read-only analysis should use its isolated cache key, got %q", cacheKey)
	}

	for _, tc := range []struct {
		name string
		run  func(http.ResponseWriter, *http.Request)
		path string
	}{
		{name: "full", run: srv.handleAnalysis, path: "/tvbot/analysis?price_days=1&pnl_days=30"},
		{name: "trades", run: srv.handleAnalysisTrades, path: "/tvbot/analysis/trades?price_days=1&pnl_days=30&page=1&page_size=100"},
		{name: "open positions", run: srv.handleAnalysisOpenPositions, path: "/tvbot/analysis/open-positions?price_days=1&pnl_days=30"},
		{name: "summary", run: srv.handleAnalysisSummary, path: "/tvbot/analysis/summary?price_days=1&pnl_days=30"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			tc.run(recorder, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if recorder.Code != http.StatusOK {
				t.Fatalf("read-only analysis status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			if strings.Contains(recorder.Body.String(), `"error"`) {
				t.Fatalf("read-only analysis should return empty data, got error: %s", recorder.Body.String())
			}
		})
	}

	response, err := srv.buildAnalysis(context.Background(), cfg, "", "", 1, 30*24*60, false)
	if err != nil {
		t.Fatalf("read-only analysis should succeed without exchange credentials: %v", err)
	}
	if !response.OK || len(response.PositionTrades) != 0 || len(response.OpenPositionTrades) != 0 || len(response.Trades) != 0 {
		t.Fatalf("read-only analysis should return empty trade collections: %#v", response)
	}
}

func TestStaleAnalysisCacheIgnoresUpstreamErrorPayload(t *testing.T) {
	srv := newTestServer(t)
	cfg := srv.ConfigStore.Get()
	now := srv.now()
	cacheKey := analysisCacheKey("", "binance_bot", analysisEnvName(cfg), 3, 1440)
	failed := analysisResponse{
		OK:          true,
		RefreshedAt: now.Add(-15 * time.Minute),
		Source: analysisSourceStatus{
			Fills:   "binance_error",
			Funding: "binance_error",
		},
	}
	if err := srv.Orders.CachePayload(cacheKey, failed, failed.RefreshedAt); err != nil {
		t.Fatal(err)
	}
	if _, ok := srv.staleAnalysisFromCache(cacheKey); ok {
		t.Fatal("stale analysis should not reuse an upstream-error payload")
	}
}

func TestAnalysisSplitEndpointsUseCachedSnapshotAndPaginate(t *testing.T) {
	srv := newTestServer(t)
	if _, err := srv.OKXCredentials.UpdateAccount(okx.CredentialAccountUpdate{
		ID:     "default",
		Active: true,
		Credentials: okx.Credentials{
			APIKey:     "analysis-key",
			SecretKey:  "analysis-secret",
			Passphrase: "analysis-pass",
		},
	}); err != nil {
		t.Fatal(err)
	}
	now := srv.now()
	positions := make([]analysisPositionTrade, 45)
	for i := range positions {
		exit := now.Add(-time.Duration(i+1) * time.Minute)
		positions[i] = analysisPositionTrade{
			Exchange:  trading.ExchangeOKX,
			APIID:     "default",
			InstID:    "BTC-USDT-SWAP",
			Side:      trading.PositionSideLong,
			EntryTime: exit.Add(-time.Hour),
			EntryTS:   exit.Add(-time.Hour).UnixMilli(),
			ExitTime:  exit,
			ExitTS:    exit.UnixMilli(),
			NetPnL:    strconv.Itoa(i + 1),
		}
	}
	full := analysisResponse{
		OK:             true,
		APIID:          "default",
		Env:            config.EnvDemo,
		PriceDays:      3,
		PNLMinutes:     1440,
		RefreshedAt:    now,
		Source:         analysisSourceStatus{Balance: "okx", Price: "okx", Fills: "okx", Funding: "okx"},
		Summary:        analysisSymbolStats{Exchange: trading.ExchangeOKX, TradeCount: len(positions)},
		PositionTrades: positions,
		Trades: []analysisTrade{{
			Exchange:       trading.ExchangeOKX,
			APIID:          "default",
			InstID:         "BTC-USDT-SWAP",
			TradeID:        "fill-1",
			PositionEffect: trading.PositionEffectClose,
			FillTime:       now,
			FillTS:         now.UnixMilli(),
		}},
	}
	cacheKey := analysisCacheKey("default", "", config.EnvDemo, 3, 1440)
	if err := srv.Orders.CachePayload(cacheKey, full, now); err != nil {
		t.Fatal(err)
	}

	summaryRR := httptest.NewRecorder()
	srv.handleAnalysisSummary(summaryRR, httptest.NewRequest(http.MethodGet, "/tvbot/analysis/summary?price_days=3&pnl_minutes=1440", nil))
	if summaryRR.Code != http.StatusOK {
		t.Fatalf("summary status=%d body=%s", summaryRR.Code, summaryRR.Body.String())
	}
	if strings.Contains(summaryRR.Body.String(), `"position_trades"`) || strings.Contains(summaryRR.Body.String(), `"trades"`) {
		t.Fatalf("summary should not contain raw trade arrays: %s", summaryRR.Body.String())
	}

	pageRR := httptest.NewRecorder()
	srv.handleAnalysisTrades(pageRR, httptest.NewRequest(http.MethodGet, "/tvbot/analysis/trades?price_days=3&pnl_minutes=1440&exchange=okx&page=2&page_size=20", nil))
	if pageRR.Code != http.StatusOK {
		t.Fatalf("trades status=%d body=%s", pageRR.Code, pageRR.Body.String())
	}
	var page analysisTradesPageResponse
	if err := json.NewDecoder(pageRR.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	if page.Page != 2 || page.PageSize != 20 || page.Total != 45 || page.TotalPages != 3 || len(page.Trades) != 20 {
		t.Fatalf("unexpected page metadata: %#v", page)
	}
	if page.Trades[0].ExitTS != positions[20].ExitTS {
		t.Fatalf("page ordering mismatch: got=%d want=%d", page.Trades[0].ExitTS, positions[20].ExitTS)
	}

	openRR := httptest.NewRecorder()
	srv.handleAnalysisOpenPositions(openRR, httptest.NewRequest(http.MethodGet, "/tvbot/analysis/open-positions?price_days=3&pnl_minutes=1440", nil))
	if openRR.Code != http.StatusOK {
		t.Fatalf("open positions status=%d body=%s", openRR.Code, openRR.Body.String())
	}
	var open analysisOpenPositionsResponse
	if err := json.NewDecoder(openRR.Body).Decode(&open); err != nil {
		t.Fatal(err)
	}
	if open.Total != 0 || len(open.Positions) != 0 {
		t.Fatalf("unexpected open positions: %#v", open)
	}
}

func TestBuildAnalysisDoesNotCacheBinanceUpstreamError(t *testing.T) {
	srv := newTestServer(t)
	var userTradeCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/fapi/v1/exchangeInfo":
			_, _ = w.Write([]byte(`{"symbols":[{"symbol":"BADUSDT","status":"TRADING","pricePrecision":2,"quantityPrecision":3}]}`))
		case "/fapi/v1/userTrades":
			userTradeCalls++
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`temporary upstream failure`))
		case "/fapi/v1/income":
			_, _ = w.Write([]byte(`[]`))
		case "/fapi/v3/account":
			_, _ = w.Write([]byte(`{"totalWalletBalance":"100","totalMarginBalance":"100","availableBalance":"100","updateTime":1784862000000,"assets":[{"asset":"USDT","walletBalance":"100","marginBalance":"100","availableBalance":"100","updateTime":1784862000000}]}`))
		default:
			t.Fatalf("unexpected Binance path %s", r.URL.Path)
		}
	}))
	defer upstream.Close()

	cfg := srv.ConfigStore.Get()
	cfg.Symbols = map[string]config.SymbolConfig{"BAD": {Coinpair: "BADUSDT"}}
	cfg.Trading.BinanceDemoBaseURL = upstream.URL
	srv.ConfigStore = config.NewStore("", cfg)
	srv.BinanceHTTPClient = upstream.Client()
	if _, err := srv.BinanceCredentials.UpdateAccount(binance.CredentialAccountUpdate{
		ID:     "binance_bot",
		Active: true,
		Credentials: binance.Credentials{
			APIKey:    "binance-key",
			SecretKey: "binance-secret",
		},
	}); err != nil {
		t.Fatal(err)
	}

	got, err := srv.buildAnalysis(context.Background(), cfg, "", "binance_bot", 3, 1440, true)
	if err != nil {
		t.Fatalf("build analysis should return a diagnostic response: %v", err)
	}
	if got.Source.Fills != "binance_error" {
		t.Fatalf("expected Binance fill source error, got %#v", got.Source)
	}
	if userTradeCalls == 0 {
		t.Fatal("Binance user-trades endpoint was not called")
	}
	cacheKey := analysisCacheKey("", "binance_bot", analysisEnvName(cfg), 3, 1440)
	if _, ok, err := srv.Orders.CachedPayload(cacheKey); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Fatal("analysis with an upstream error must not be cached")
	}
}

func TestBalanceOverviewUsesLatestSnapshotWhenOKXIsUnavailable(t *testing.T) {
	srv := newTestServer(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v5/account/balance" {
			t.Fatalf("unexpected OKX path %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"code":"50001","data":[],"msg":"Service temporarily unavailable. Please try again later."}`))
	}))
	defer upstream.Close()

	cfg := srv.ConfigStore.Get()
	cfg.Trading.BaseURL = upstream.URL
	srv.OKXHTTPClient = upstream.Client()
	if _, err := srv.OKXCredentials.UpdateAccount(okx.CredentialAccountUpdate{
		ID:     "default",
		Active: true,
		Credentials: okx.Credentials{
			APIKey:     "key",
			SecretKey:  "secret",
			Passphrase: "pass",
		},
	}); err != nil {
		t.Fatal(err)
	}
	observedAt := srv.now().Add(-10 * time.Minute)
	if err := srv.Orders.UpsertUSDTBalanceSnapshot(storage.USDTBalanceSnapshot{
		Exchange:         trading.ExchangeOKX,
		APIID:            "default",
		Env:              analysisEnvName(cfg),
		ObservedAt:       observedAt,
		Eq:               "9776.287875",
		EqUsd:            "9776.287875",
		AvailEq:          "9700",
		AvailBal:         "9690",
		CashBal:          "9776.287875",
		BalanceUpdatedAt: observedAt.UTC().Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatal(err)
	}

	got := srv.balanceOverviewForOKX(context.Background(), cfg, analysisEnvName(cfg), "", 12*60, srv.now())
	if got.Status != "stale" || !strings.Contains(got.Error, "50001") {
		t.Fatalf("expected stale overview with diagnostic error: %#v", got)
	}
	if len(got.BalancePoints) != 1 || len(got.Balance.Details) != 1 {
		t.Fatalf("expected cached balance and point: %#v", got)
	}
	if got.Balance.Details[0].Eq != "9776.287875" || got.Balance.UpdatedAt != observedAt.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("bad cached balance: %#v", got.Balance)
	}
	if got.Window.CurrentValue != 9776.287875 {
		t.Fatalf("bad cached window: %#v", got.Window)
	}
}

func TestFetchBinanceAnalysisTradesContinuesAfterSymbolError(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	tradeTime := now.Add(-time.Hour).UnixMilli()
	requested := []string{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/fapi/v1/userTrades" {
			t.Fatalf("unexpected Binance path %s", r.URL.Path)
		}
		symbol := r.URL.Query().Get("symbol")
		requested = append(requested, symbol)
		w.Header().Set("Content-Type", "application/json")
		if symbol == "BADUSDT" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"code":-1121,"msg":"Invalid symbol."}`))
			return
		}
		startMS, _ := strconv.ParseInt(r.URL.Query().Get("startTime"), 10, 64)
		endMS, _ := strconv.ParseInt(r.URL.Query().Get("endTime"), 10, 64)
		if tradeTime < startMS || tradeTime > endMS {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		tradeID := int64(101)
		if symbol == "ZZZUSDT" {
			tradeID = 303
		}
		_ = json.NewEncoder(w).Encode([]binance.UserTrade{{
			Symbol: symbol, Side: "SELL", PositionSide: "BOTH", Price: "10", Qty: "1",
			RealizedPnl: "2", Commission: "0.1", CommissionAsset: "USDT",
			Time: tradeTime, ID: tradeID, OrderID: tradeID + 1000,
		}})
	}))
	defer ts.Close()

	cfg := config.Config{Symbols: map[string]config.SymbolConfig{
		"AAA": {Coinpair: "AAAUSDT"},
		"BAD": {Coinpair: "BADUSDT"},
		"ZZZ": {Coinpair: "ZZZUSDT"},
	}}
	client := binance.Client{
		BaseURL:     ts.URL,
		Credentials: binance.Credentials{APIKey: "key", SecretKey: "secret"},
		HTTPClient:  ts.Client(),
		Now:         func() time.Time { return now },
	}
	trades, err := (&Server{}).fetchBinanceAnalysisTrades(context.Background(), client, "binance-main", cfg, 1440, now)
	if err != nil {
		t.Fatalf("invalid historical symbol should not fail analysis refresh: %v", err)
	}
	if len(trades) != 2 || trades[0].InstID != "AAAUSDT" || trades[1].InstID != "ZZZUSDT" {
		t.Fatalf("successful symbols should be retained, trades=%#v", trades)
	}
	badIndex, zzzIndex := -1, -1
	for i, symbol := range requested {
		if symbol == "BADUSDT" && badIndex < 0 {
			badIndex = i
		}
		if symbol == "ZZZUSDT" && zzzIndex < 0 {
			zzzIndex = i
		}
	}
	if badIndex < 0 || zzzIndex <= badIndex {
		t.Fatalf("expected ZZZUSDT to be requested after BADUSDT failed, requested=%v", requested)
	}
}

func TestFetchBinanceAnalysisTradesRetainsPartialTradesAndAggregatesErrors(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	tradeTime := now.Add(-time.Hour).UnixMilli()
	calls := map[string]int{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		symbol := r.URL.Query().Get("symbol")
		calls[symbol]++
		w.Header().Set("Content-Type", "application/json")
		switch symbol {
		case "PARTIALUSDT":
			if calls[symbol] == 1 {
				_ = json.NewEncoder(w).Encode([]binance.UserTrade{{
					Symbol: symbol, Side: "BUY", PositionSide: "BOTH", Price: "5", Qty: "2",
					RealizedPnl: "1", CommissionAsset: "USDT", Time: tradeTime, ID: 401, OrderID: 1401,
				}})
				return
			}
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`temporary failure`))
		case "SECONDUSDT":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"code":-1121,"msg":"Invalid symbol."}`))
		default:
			_, _ = w.Write([]byte(`[]`))
		}
	}))
	defer ts.Close()

	cfg := config.Config{Symbols: map[string]config.SymbolConfig{
		"PARTIAL": {Coinpair: "PARTIALUSDT"},
		"SECOND":  {Coinpair: "SECONDUSDT"},
		"ZZZ":     {Coinpair: "ZZZUSDT"},
	}}
	client := binance.Client{
		BaseURL:     ts.URL,
		Credentials: binance.Credentials{APIKey: "key", SecretKey: "secret"},
		HTTPClient:  ts.Client(),
		Now:         func() time.Time { return now },
	}
	trades, err := (&Server{}).fetchBinanceAnalysisTrades(context.Background(), client, "binance-main", cfg, 1440, now)
	if err == nil || !strings.Contains(err.Error(), "PARTIALUSDT") || strings.Contains(err.Error(), "SECONDUSDT") {
		t.Fatalf("expected only actionable symbol errors, got %v", err)
	}
	if len(trades) != 1 || trades[0].InstID != "PARTIALUSDT" || trades[0].TradeID != "401" {
		t.Fatalf("partial symbol trades should be retained, trades=%#v", trades)
	}
	if calls["ZZZUSDT"] == 0 {
		t.Fatalf("expected later symbol to be processed, calls=%#v", calls)
	}
}

func TestFetchBinanceAnalysisTradesStopsAfterContextCancellation(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	requested := []string{}
	client := binance.Client{
		BaseURL:     "https://binance.invalid",
		Credentials: binance.Credentials{APIKey: "key", SecretKey: "secret"},
		HTTPClient: &http.Client{Transport: analysisRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			requested = append(requested, req.URL.Query().Get("symbol"))
			return nil, context.Canceled
		})},
		Now: func() time.Time { return now },
	}
	cfg := config.Config{Symbols: map[string]config.SymbolConfig{
		"AAA": {Coinpair: "AAAUSDT"},
		"ZZZ": {Coinpair: "ZZZUSDT"},
	}}

	_, err := (&Server{}).fetchBinanceAnalysisTrades(context.Background(), client, "binance-main", cfg, 1440, now)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
	if len(requested) != 1 || requested[0] != "AAAUSDT" {
		t.Fatalf("later symbols should not be requested after cancellation, requested=%v", requested)
	}
}

func TestBalanceWindowQuerySupportsMinutesAndLegacyDays(t *testing.T) {
	tests := []struct {
		name        string
		target      string
		wantMinutes int
		wantDays    int
	}{
		{name: "current", target: "/tvbot/balances/overview?minutes=0", wantMinutes: 0, wantDays: 0},
		{name: "one hour", target: "/tvbot/balances/overview?minutes=60", wantMinutes: 60, wantDays: 1},
		{name: "ninety days", target: "/tvbot/balances/overview?minutes=129600", wantMinutes: 129600, wantDays: 90},
		{name: "clamped", target: "/tvbot/balances/overview?minutes=999999", wantMinutes: 129600, wantDays: 90},
		{name: "legacy days", target: "/tvbot/balances/overview?days=3", wantMinutes: 4320, wantDays: 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.target, nil)
			gotMinutes, gotDays := balanceWindowQuery(req)
			if gotMinutes != tc.wantMinutes || gotDays != tc.wantDays {
				t.Fatalf("window=(%d,%d), want (%d,%d)", gotMinutes, gotDays, tc.wantMinutes, tc.wantDays)
			}
		})
	}
}

func TestCompactBalancePointsUsesCurrentAndHourlyLongWindows(t *testing.T) {
	now := time.Date(2026, 7, 27, 9, 15, 0, 0, time.UTC)
	points := []analysisBalancePoint{
		{Time: now.Add(-2 * time.Hour), TS: now.Add(-2 * time.Hour).UnixMilli(), Value: 100},
		{Time: now.Add(-90 * time.Minute), TS: now.Add(-90 * time.Minute).UnixMilli(), Value: 110},
		{Time: now.Add(-30 * time.Minute), TS: now.Add(-30 * time.Minute).UnixMilli(), Value: 120},
	}
	current := compactBalancePoints(points, 0)
	if len(current) != 1 || current[0].Value != 120 {
		t.Fatalf("current points=%#v", current)
	}
	longWindow := compactBalancePoints(points, 30*24*60)
	if len(longWindow) != 2 || longWindow[0].Value != 110 || longWindow[1].Value != 120 {
		t.Fatalf("hourly points len=%d points=%#v", len(longWindow), longWindow)
	}
	for _, point := range longWindow {
		if time.UnixMilli(point.TS).Minute() != 0 {
			t.Fatalf("point should be hour-bucketed: %#v", point)
		}
	}
}

func TestSnapshotBalanceValueUsesEquityOnly(t *testing.T) {
	tests := []struct {
		name     string
		snapshot storage.USDTBalanceSnapshot
		want     float64
	}{
		{
			name: "equity preferred over USD valuation and available balances",
			snapshot: storage.USDTBalanceSnapshot{
				CashBal:  "4818.20",
				AvailBal: "4820.00",
				Eq:       "4864.23",
				EqUsd:    "4852.64",
			},
			want: 4864.23,
		},
		{
			name: "missing equity does not fallback to cash balance",
			snapshot: storage.USDTBalanceSnapshot{
				EqUsd:    "4852.64",
				CashBal:  "4818.20",
				AvailBal: "4820.00",
			},
			want: 0,
		},
		{
			name: "invalid equity does not fallback to cash balance",
			snapshot: storage.USDTBalanceSnapshot{
				EqUsd:    "bad",
				Eq:       "bad",
				CashBal:  "4818.20",
				AvailBal: "4820.00",
			},
			want: 0,
		},
		{
			name: "invalid equity does not fallback to available balance",
			snapshot: storage.USDTBalanceSnapshot{
				EqUsd:    "bad",
				Eq:       "bad",
				CashBal:  "bad",
				AvailBal: "4820.00",
			},
			want: 0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := snapshotBalanceValue(tc.snapshot); math.Abs(got-tc.want) > 0.0000001 {
				t.Fatalf("snapshotBalanceValue=%f, want %f", got, tc.want)
			}
		})
	}
}

func TestBalancePointsFromSnapshotsUsesEquityForChartValue(t *testing.T) {
	now := time.Date(2026, 7, 28, 4, 0, 0, 0, time.UTC)
	points := balancePointsFromSnapshots([]storage.USDTBalanceSnapshot{
		{
			BucketTS:   now.UnixMilli(),
			ObservedAt: now,
			EqUsd:      "4852.64",
			Eq:         "4858.04",
			AvailBal:   "4818.20",
			CashBal:    "4818.20",
			FrozenBal:  "39.83",
		},
	})
	if len(points) != 1 {
		t.Fatalf("points len=%d", len(points))
	}
	if math.Abs(points[0].Value-4858.04) > 0.0000001 {
		t.Fatalf("chart value should use USDT equity, points=%#v", points)
	}
	if points[0].EqUsd != "4852.64" || points[0].Eq != "4858.04" || points[0].FrozenBal != "39.83" {
		t.Fatalf("snapshot fields should be preserved: %#v", points[0])
	}
}

func TestAnalysisBalanceFromBinanceUsesOfficialMarginBalance(t *testing.T) {
	got := analysisBalanceFromBinanceAccount(binance.AccountInformation{
		TotalWalletBalance: "10306",
		TotalMarginBalance: "9985.87654322",
		AvailableBalance:   "9900.25",
		UpdateTime:         1785522000000,
		Assets: []binance.AccountAsset{{
			Asset:            "USDT",
			WalletBalance:    "10306",
			UnrealizedProfit: "-320.12345678",
			MarginBalance:    "9985.87654322",
			AvailableBalance: "9900.25",
			UpdateTime:       1785522000000,
		}},
	})
	if got.TotalEq != "9985.87654322" || got.AvailEq != "9900.25" || got.EqSource != "binance_account_v3" {
		t.Fatalf("bad Binance balance: %#v", got)
	}
	if len(got.Details) != 1 || got.Details[0].Eq != "9985.87654322" || got.Details[0].EqUsd != "9985.87654322" || got.Details[0].CashBal != "10306" {
		t.Fatalf("bad Binance USDT detail: %#v", got.Details)
	}
}

func TestAnalysisBalanceFromBinanceIncludesUnrealizedPnLWhenMarginBalanceMissing(t *testing.T) {
	got := analysisBalanceFromBinanceAccount(binance.AccountInformation{
		Assets: []binance.AccountAsset{{
			Asset:            "USDT",
			WalletBalance:    "1000",
			UnrealizedProfit: "-12.3456",
		}},
	})
	if got.TotalEq != "987.6544" || len(got.Details) != 1 || got.Details[0].Eq != "987.6544" {
		t.Fatalf("Binance chart equity should include open-position unrealized PnL: %#v", got)
	}
}

func TestAnalysisBalanceFromBinanceMarksMultiAssetsAsUSD(t *testing.T) {
	got := analysisBalanceFromBinanceAccount(binance.AccountInformation{
		TotalWalletBalance: "1200",
		TotalMarginBalance: "1210.5",
		AvailableBalance:   "1100.25",
		MultiAssetsMargin:  true,
		UpdateTime:         1785522000000,
		Assets:             []binance.AccountAsset{{Asset: "USDT", UpdateTime: 1785522000000}},
	})
	if got.Currency != "USD" || len(got.Details) != 1 || got.Details[0].Ccy != "USD" || got.Details[0].Eq != "1210.5" {
		t.Fatalf("multi-assets account should be represented as USD equity: %#v", got)
	}
}

func TestAnalysisBalanceFromGateIncludesUnrealizedPnL(t *testing.T) {
	now := time.Date(2026, 10, 22, 3, 0, 0, 0, time.UTC)
	got := analysisBalanceFromGate(gate.Account{
		Currency:      "USDT",
		Total:         "1000.125",
		UnrealisedPnl: "-12.3456",
		Available:     "900.5",
	}, now)
	if got.TotalEq != "987.7794" || got.AvailEq != "900.5" || len(got.Details) != 1 {
		t.Fatalf("bad Gate balance: %#v", got)
	}
	if got.Details[0].Eq != "987.7794" || got.Details[0].EqUsd != "987.7794" {
		t.Fatalf("bad Gate USDT detail: %#v", got.Details[0])
	}
}

func TestEnrichAnalysisTradesUsesMatchedOrderLeverageAndComputesNetPnL(t *testing.T) {
	cfg := config.Config{Symbols: map[string]config.SymbolConfig{
		"BTC": {InstID: "BTC-USDT-SWAP", CtVal: 0.01},
	}, Trading: config.TradingConfig{Leverage: 10}}
	trades := []analysisTrade{
		{
			Exchange: trading.ExchangeOKX,
			APIID:    "default",
			InstID:   "BTC-USDT-SWAP",
			OrdID:    "okx-order",
			FillPx:   "50000",
			FillSz:   "2",
			FillPnl:  "3",
			Fee:      "-0.12",
			FeeCcy:   "USDT",
		},
		{
			Exchange: trading.ExchangeBinance,
			APIID:    "binance-alt",
			InstID:   "BTCUSDT",
			OrdID:    "8002",
			FillPx:   "64000",
			FillSz:   "0.03",
			FillPnl:  "5",
			Fee:      "-0.25",
			FeeCcy:   "USDT",
		},
		{
			Exchange: trading.ExchangeOKX,
			APIID:    "default",
			InstID:   "MISSING-USDT-SWAP",
			OrdID:    "missing-ctval",
			FillPx:   "10",
			FillSz:   "1",
			FillPnl:  "1",
			Fee:      "-0.01",
			FeeCcy:   "USDT",
		},
		{
			Exchange: trading.ExchangeOKX,
			APIID:    "default",
			InstID:   "BTC-USDT-SWAP",
			OrdID:    "unrecorded-okx-order",
			FillPx:   "50000",
			FillSz:   "2",
			FillPnl:  "3",
			Fee:      "-0.12",
			FeeCcy:   "USDT",
		},
	}
	records := []storage.OrderRecord{
		{
			APIID:          "default",
			TargetExchange: trading.ExchangeOKX,
			Result:         trading.OrderResult{APIID: "default", TargetExchange: trading.ExchangeOKX, InstID: "BTC-USDT-SWAP", OrdID: "okx-order", Leverage: 5},
		},
		{
			APIID:          "binance-alt",
			TargetExchange: trading.ExchangeBinance,
			Result:         trading.OrderResult{APIID: "binance-alt", TargetExchange: trading.ExchangeBinance, InstID: "BTCUSDT", OrdID: "8001 / 8002", Leverage: 10},
		},
		{
			APIID:          "default",
			TargetExchange: trading.ExchangeOKX,
			Result:         trading.OrderResult{APIID: "default", TargetExchange: trading.ExchangeOKX, InstID: "MISSING-USDT-SWAP", OrdID: "missing-ctval"},
			Leverage:       3,
		},
	}
	enrichAnalysisTrades(cfg, trades, records)
	if trades[0].Leverage != 5 || trades[0].Margin != "200" || trades[0].Turnover != "1000" || trades[0].NetPnL != "2.88" {
		t.Fatalf("bad OKX enrichment: %#v", trades[0])
	}
	if trades[1].Leverage != 10 || trades[1].Margin != "192" || trades[1].Turnover != "1920" || trades[1].NetPnL != "4.75" {
		t.Fatalf("bad Binance enrichment: %#v", trades[1])
	}
	if trades[2].Leverage != 3 || trades[2].Margin != "" || trades[2].Turnover != "" || trades[2].NetPnL != "0.99" {
		t.Fatalf("missing ctVal should keep leverage/net pnl but omit margin: %#v", trades[2])
	}
	if trades[3].Leverage != 10 || trades[3].Margin != "100" || trades[3].Turnover != "1000" || trades[3].NetPnL != "2.88" {
		t.Fatalf("unrecorded OKX fill should use configured leverage: %#v", trades[3])
	}
}

func TestAnalysisConfigWithOKXCtValsAddsHistoricalContractValues(t *testing.T) {
	cfg := config.Config{Symbols: map[string]config.SymbolConfig{
		"BTC": {InstID: "BTC-USDT-SWAP", CtVal: 0.01},
	}}
	analysisCfg := analysisConfigWithOKXCtVals(cfg, []okx.Instrument{
		{InstID: "CRV-USDT-SWAP", CtVal: "1"},
		{InstID: "BTC-USDT-SWAP", CtVal: "99"},
		{InstID: "INVALID-USDT-SWAP", CtVal: "not-a-number"},
	})
	if got := analysisOKXCtVal(analysisCfg, "CRV-USDT-SWAP"); got != 1 {
		t.Fatalf("historical CRV ctVal=%v, want 1", got)
	}
	if got := analysisOKXCtVal(analysisCfg, "BTC-USDT-SWAP"); got != 0.01 {
		t.Fatalf("configured BTC ctVal=%v, want 0.01", got)
	}
	if got := analysisOKXCtVal(analysisCfg, "INVALID-USDT-SWAP"); got != 0 {
		t.Fatalf("invalid instrument ctVal=%v, want 0", got)
	}
	margin, ok := analysisTradeMargin(analysisCfg, analysisTrade{
		Exchange: trading.ExchangeOKX,
		InstID:   "CRV-USDT-SWAP",
		FillPx:   "10",
		FillSz:   "3",
		Leverage: 10,
	})
	if !ok || margin != "3" {
		t.Fatalf("historical CRV margin=%q ok=%v, want 3/true", margin, ok)
	}
	if turnover := analysisTradeTurnoverValue(analysisCfg, analysisTrade{
		Exchange: trading.ExchangeOKX,
		InstID:   "CRV-USDT-SWAP",
		FillPx:   "10",
		FillSz:   "3",
	}); turnover != 30 {
		t.Fatalf("historical CRV turnover=%v, want 30", turnover)
	}
}

func TestDeriveBinanceAnalysisSymbolSupportsUSDCContracts(t *testing.T) {
	cases := []struct {
		candidates []string
		want       string
	}{
		{candidates: []string{"PENGUUSDC"}, want: "PENGUUSDC"},
		{candidates: []string{"BINANCE:PENGUUSDC.P"}, want: "PENGUUSDC"},
		{candidates: []string{"PENGU-USDC-SWAP"}, want: "PENGUUSDC"},
		{candidates: []string{"", "PENGU"}, want: "PENGUUSDT"},
	}
	for _, tc := range cases {
		got, ok := deriveBinanceAnalysisSymbol(tc.candidates...)
		if !ok || got != tc.want {
			t.Fatalf("deriveBinanceAnalysisSymbol(%#v)=%q ok=%v, want %q", tc.candidates, got, ok, tc.want)
		}
	}
}

func TestFetchBinanceAnalysisTradesBackfillsSymbolsMissingFromUserStream(t *testing.T) {
	srv := newTestServer(t)
	now := srv.now().UTC()
	cfg := srv.ConfigStore.Get()
	cfg.Symbols = nil
	apiID := "binance-main"
	tradeEnv := analysisEnvName(cfg)
	if _, err := srv.Orders.UpsertExchangeOrders([]storage.ExchangeOrder{{
		Exchange:      trading.ExchangeBinance,
		APIID:         apiID,
		TradeEnv:      tradeEnv,
		RemoteOrderID: "ethusdc-order",
		InstID:        "ETHUSDC",
		Status:        "FILLED",
		CreatedAt:     now.Add(-time.Hour),
	}}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.Orders.UpsertBinanceFills([]storage.BinanceFill{{
		APIID: apiID, Symbol: "BTCUSDT", TradeID: "btc-fill", OrderID: "btc-order",
		FillTime: now.Add(-time.Hour).UnixMilli(), Price: "60000", Qty: "0.01",
	}}, now); err != nil {
		t.Fatal(err)
	}

	var requested []string
	tradeTime := strconv.FormatInt(now.Add(-30*time.Minute).UnixMilli(), 10)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/fapi/v1/userTrades" {
			t.Fatalf("unexpected Binance path %s", r.URL.Path)
		}
		symbol := r.URL.Query().Get("symbol")
		requested = append(requested, symbol)
		if symbol != "ETHUSDC" {
			t.Fatalf("user stream-covered symbol should not be fetched from REST: %s", symbol)
		}
		_, _ = w.Write([]byte(`[{"symbol":"ETHUSDC","side":"SELL","positionSide":"BOTH","price":"2500","qty":"1","realizedPnl":"3","commission":"0.1","commissionAsset":"USDC","time":` + tradeTime + `,"id":99,"orderId":100}]`))
	}))
	defer upstream.Close()
	client := binance.Client{BaseURL: upstream.URL, HTTPClient: upstream.Client(), Credentials: binance.Credentials{APIKey: "key", SecretKey: "secret"}}
	streamID := binanceUserStreamID(client, apiID)
	binanceUserStreamRegistry.Lock()
	binanceUserStreamRegistry.active[streamID] = struct{}{}
	binanceUserStreamRegistry.Unlock()
	defer func() {
		binanceUserStreamRegistry.Lock()
		delete(binanceUserStreamRegistry.active, streamID)
		binanceUserStreamRegistry.Unlock()
	}()

	trades, err := srv.fetchBinanceAnalysisTrades(context.Background(), client, apiID, cfg, 24*60, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(requested) == 0 {
		t.Fatal("missing-symbol REST backfill was not requested")
	}
	for _, symbol := range requested {
		if symbol != "ETHUSDC" {
			t.Fatalf("user stream-covered symbol should not be fetched from REST: %v", requested)
		}
	}
	var foundBTC, foundETH bool
	for _, trade := range trades {
		switch trade.InstID {
		case "BTCUSDT":
			foundBTC = true
		case "ETHUSDC":
			foundETH = true
		}
	}
	if !foundBTC || !foundETH {
		t.Fatalf("analysis trades should merge local and USDC REST history: %#v", trades)
	}
}

func TestCorrectBinanceAnalysisADLPricesUsesSiblingAutoclosePrice(t *testing.T) {
	base := time.Date(2026, 11, 12, 14, 46, 32, 0, time.UTC)
	trades := []analysisTrade{{
		Exchange: trading.ExchangeBinance,
		APIID:    "binance-main",
		InstID:   "ERAUSDT",
		OrdID:    "adl-order",
		FillPx:   "1000",
		FillTime: base,
	}}
	orders := []storage.ExchangeOrder{
		{
			Exchange:      trading.ExchangeBinance,
			RemoteOrderID: "adl-order",
			ClientOrderID: "adl_autoclose",
			InstID:        "ERAUSDT",
			Side:          "BUY",
			Status:        "FILLED",
			AveragePrice:  "1000",
			FinishedAt:    base,
		},
		{
			Exchange:      trading.ExchangeBinance,
			RemoteOrderID: "autoclose-order",
			ClientOrderID: "autoclose-123",
			InstID:        "ERAUSDT",
			Side:          "BUY",
			Status:        "FILLED",
			AveragePrice:  "0.0934000",
			FinishedAt:    base.Add(time.Second),
		},
	}
	if got := correctBinanceAnalysisADLPrices(trades, orders); got != 1 || trades[0].FillPx != "0.0934" {
		t.Fatalf("ADL price correction=%d trade=%#v, want one correction to 0.0934", got, trades[0])
	}
}

func TestHandleAnalysisIncomeUsesSelectedWindowAndPagesResults(t *testing.T) {
	srv := newTestServer(t)
	now := srv.now().Truncate(time.Minute)
	var requestedStart, requestedEnd int64
	requestCount := 0
	firstIncomeTime := now.Add(-30 * time.Second).UnixMilli()
	secondIncomeTime := now.Add(-29 * time.Second).UnixMilli()
	thirdIncomeTime := now.Add(-28 * time.Second).UnixMilli()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/fapi/v1/income" {
			t.Fatalf("unexpected Binance path %s", r.URL.Path)
		}
		if r.URL.Query().Get("incomeType") != "" {
			t.Fatalf("income history should include all transaction types: %s", r.URL.RawQuery)
		}
		requestedStart, _ = strconv.ParseInt(r.URL.Query().Get("startTime"), 10, 64)
		requestedEnd, _ = strconv.ParseInt(r.URL.Query().Get("endTime"), 10, 64)
		requestCount++
		if requestCount > 1 {
			_, _ = w.Write([]byte(fmt.Sprintf(`[{"symbol":"ETHUSDT","incomeType":"FUNDING_FEE","income":"0.2","asset":"USDT","info":"","tranId":13,"tradeId":"","time":%d}]`, thirdIncomeTime)))
			return
		}
		_, _ = w.Write([]byte(fmt.Sprintf(`[
			{"symbol":"BTCUSDT","incomeType":"REALIZED_PNL","income":"2.5","asset":"USDT","info":"","tranId":11,"tradeId":"7","time":%d},
			{"symbol":"BTCUSDT","incomeType":"COMMISSION","income":"-0.1","asset":"USDT","info":"","tranId":12,"tradeId":"7","time":%d}
		]`, firstIncomeTime, secondIncomeTime)))
	}))
	defer upstream.Close()
	cfg := srv.ConfigStore.Get()
	cfg.Trading.BinanceDemoBaseURL = upstream.URL
	srv.ConfigStore = config.NewStore("", cfg)
	srv.BinanceHTTPClient = upstream.Client()
	if _, err := srv.BinanceCredentials.UpdateAccount(binance.CredentialAccountUpdate{
		ID: "demo-main", Active: true, Credentials: binance.Credentials{APIKey: "demo-key", SecretKey: "demo-secret"},
	}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/tvbot/analysis/income?pnl_minutes=60&page=2&page_size=1", nil)
	rr := httptest.NewRecorder()
	srv.handleAnalysisIncome(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("income handler code=%d body=%s", rr.Code, rr.Body.String())
	}
	var got analysisIncomePageResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	wantStart := now.Add(-time.Hour).UnixMilli()
	if requestedStart != wantStart || requestedEnd != now.UnixMilli() {
		t.Fatalf("unexpected income history window start=%d end=%d want start=%d end=%d", requestedStart, requestedEnd, wantStart, now.UnixMilli())
	}
	if got.PNLMinutes != 60 || got.Total != 2 || got.TotalPages != 2 || got.Page != 2 || len(got.Incomes) != 1 || got.Incomes[0].IncomeType != "REALIZED_PNL" {
		t.Fatalf("income page did not match selected time/page: %#v", got)
	}
	requestedStart, requestedEnd = 0, 0
	incrementalReq := httptest.NewRequest(http.MethodGet, "/tvbot/analysis/income?pnl_minutes=60&page_size=20", nil)
	incrementalRR := httptest.NewRecorder()
	srv.handleAnalysisIncome(incrementalRR, incrementalReq)
	if incrementalRR.Code != http.StatusOK {
		t.Fatalf("incremental income handler code=%d body=%s", incrementalRR.Code, incrementalRR.Body.String())
	}
	if requestedStart != now.Add(-time.Minute).UnixMilli() || requestedEnd != now.UnixMilli() {
		t.Fatalf("incremental sync should overlap only the latest minute: start=%d end=%d", requestedStart, requestedEnd)
	}
	if err := json.Unmarshal(incrementalRR.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Total != 3 || len(got.Incomes) != 3 {
		t.Fatalf("incremental sync should return persisted old and new rows: %#v", got)
	}
}

func TestBuildAnalysisPositionTradesUsesBinancePricePnL(t *testing.T) {
	base := time.Date(2026, 11, 12, 10, 5, 0, 0, time.UTC)
	trades := []analysisTrade{
		{
			Exchange: trading.ExchangeBinance, APIID: "binance-main", InstID: "ERAUSDT",
			TradeID: "open", OrdID: "open-order", Side: "SELL", PosSide: trading.PositionSideShort,
			PositionEffect: trading.PositionEffectOpen, FillPx: "0.0984", FillSz: "5078",
			Fee: "-0.2498376", FeeCcy: "USDT", FillTime: base, FillTS: base.UnixMilli(), FillCount: 1,
		},
		{
			Exchange: trading.ExchangeBinance, APIID: "binance-main", InstID: "ERAUSDT",
			TradeID: "close", OrdID: "close-order", Side: "BUY", PosSide: trading.PositionSideShort,
			PositionEffect: trading.PositionEffectClose, FillPx: "0.0934", FillSz: "5078",
			FillPnl: "-296970.7752", Fee: "-0.2498376", FeeCcy: "USDT",
			FillTime: base.Add(time.Hour), FillTS: base.Add(time.Hour).UnixMilli(), FillCount: 1,
		},
	}
	positions := buildAnalysisPositionTrades(config.Config{}, trades, base.Add(-time.Minute))
	if len(positions) != 1 {
		t.Fatalf("positions len=%d positions=%#v", len(positions), positions)
	}
	if got := parseFloat(positions[0].RealizedPnL); math.Abs(got-25.39) > 0.0000001 {
		t.Fatalf("realized P&L=%v, want 25.39 from prices", got)
	}
	if got := parseFloat(positions[0].NetPnL); math.Abs(got-24.8903248) > 0.0000001 {
		t.Fatalf("net P&L=%v, want price P&L plus fees", got)
	}
}

func TestBuildAnalysisPositionTradesCountsClosedPositionsOnly(t *testing.T) {
	cfg := config.Config{Symbols: map[string]config.SymbolConfig{
		"TEST": {InstID: "TEST-USDT-SWAP", CtVal: 0.01},
	}}
	periodSince := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	trades := []analysisTrade{
		{
			Exchange:  trading.ExchangeOKX,
			APIID:     "default",
			InstID:    "TEST-USDT-SWAP",
			TradeID:   "long-open",
			OrdID:     "long-open-order",
			Side:      "buy",
			PosSide:   "long",
			FillPx:    "100",
			FillSz:    "10",
			Fee:       "-0.1",
			FeeCcy:    "USDT",
			FillTime:  periodSince.Add(-time.Hour),
			FillTS:    periodSince.Add(-time.Hour).UnixMilli(),
			FillCount: 1,
		},
		{
			Exchange:  trading.ExchangeOKX,
			APIID:     "default",
			InstID:    "TEST-USDT-SWAP",
			TradeID:   "long-close",
			OrdID:     "long-close-order",
			Side:      "sell",
			PosSide:   "long",
			FillPx:    "110",
			FillSz:    "10",
			FillPnl:   "10",
			Fee:       "-0.2",
			FeeCcy:    "USDT",
			FillTime:  periodSince.Add(time.Hour),
			FillTS:    periodSince.Add(time.Hour).UnixMilli(),
			FillCount: 1,
		},
		{
			Exchange:  trading.ExchangeOKX,
			APIID:     "default",
			InstID:    "TEST-USDT-SWAP",
			TradeID:   "short-open",
			OrdID:     "short-open-order",
			Side:      "sell",
			PosSide:   "short",
			FillPx:    "200",
			FillSz:    "5",
			Fee:       "-0.05",
			FeeCcy:    "USDT",
			FillTime:  periodSince.Add(2 * time.Hour),
			FillTS:    periodSince.Add(2 * time.Hour).UnixMilli(),
			FillCount: 1,
		},
		{
			Exchange:  trading.ExchangeOKX,
			APIID:     "default",
			InstID:    "TEST-USDT-SWAP",
			TradeID:   "short-close",
			OrdID:     "short-close-order",
			Side:      "buy",
			PosSide:   "short",
			FillPx:    "210",
			FillSz:    "5",
			FillPnl:   "-5",
			Fee:       "-0.07",
			FeeCcy:    "USDT",
			FillTime:  periodSince.Add(3 * time.Hour),
			FillTS:    periodSince.Add(3 * time.Hour).UnixMilli(),
			FillCount: 1,
		},
		{
			Exchange:  trading.ExchangeOKX,
			APIID:     "default",
			InstID:    "TEST-USDT-SWAP",
			TradeID:   "unclosed-open",
			OrdID:     "unclosed-open-order",
			Side:      "buy",
			PosSide:   "long",
			FillPx:    "300",
			FillSz:    "1",
			Fee:       "-0.01",
			FeeCcy:    "USDT",
			FillTime:  periodSince.Add(4 * time.Hour),
			FillTS:    periodSince.Add(4 * time.Hour).UnixMilli(),
			FillCount: 1,
		},
	}
	positions := buildAnalysisPositionTrades(cfg, trades, periodSince)
	if len(positions) != 2 {
		t.Fatalf("positions len=%d positions=%#v", len(positions), positions)
	}
	_, openPositions := buildAnalysisPositionTradesWithOpen(cfg, trades, periodSince)
	if len(openPositions) != 1 || openPositions[0].EntryOrdID != "unclosed-open-order" || openPositions[0].Side != trading.PositionSideLong || openPositions[0].Qty != "1" || openPositions[0].EntryPx != "300" || !openPositions[0].ExitTime.IsZero() || openPositions[0].NetPnL != "" {
		t.Fatalf("unclosed position should be exposed separately: %#v", openPositions)
	}
	if positions[0].Side != "short" || positions[0].NetPnL != "-5.12" {
		t.Fatalf("bad short position: %#v", positions[0])
	}
	if positions[1].Side != "long" || positions[1].NetPnL != "9.7" {
		t.Fatalf("bad long position: %#v", positions[1])
	}
	summary, exchanges, _ := computeStats(cfg, positions, trades, periodSince)
	if summary.TradeCount != 2 || summary.Wins != 1 || summary.Losses != 1 || math.Abs(summary.WinRate-0.5) > 0.0000001 {
		t.Fatalf("bad summary counts: %#v", summary)
	}
	if math.Abs(summary.NetPnL-4.58) > 0.0000001 {
		t.Fatalf("bad summary net pnl: %#v", summary)
	}
	if math.Abs(summary.Fees-(-0.33)) > 0.0000001 || math.Abs(summary.Turnover-34.5) > 0.0000001 {
		t.Fatalf("period fees/turnover should only use in-window fills: %#v", summary)
	}
	if len(exchanges) != 1 || exchanges[0].TradeCount != 2 {
		t.Fatalf("bad exchange stats: %#v", exchanges)
	}
}

func TestBuildAnalysisPositionTradesPropagatesOperationMetadata(t *testing.T) {
	base := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	trade := func(id, ordID, side, positionSide, effect, operation, source string, at time.Time) analysisTrade {
		return analysisTrade{
			Exchange:        trading.ExchangeBinance,
			APIID:           "default",
			InstID:          "TESTUSDT",
			TradeID:         id,
			OrdID:           ordID,
			Side:            side,
			PosSide:         positionSide,
			PositionEffect:  effect,
			Operation:       operation,
			OperationSource: source,
			FillPx:          "100",
			FillSz:          "1",
			FillTime:        at,
			FillTS:          at.UnixMilli(),
			FillCount:       1,
		}
	}
	trades := []analysisTrade{
		trade("long-open", "long-entry", "buy", trading.PositionSideLong, trading.PositionEffectOpen, "open", "auto", base),
		trade("long-close", "long-exit", "sell", trading.PositionSideLong, trading.PositionEffectClose, "take_profit", "monitor", base.Add(time.Hour)),
		trade("short-open", "short-entry", "sell", trading.PositionSideShort, trading.PositionEffectOpen, "open", "manual", base.Add(2*time.Hour)),
		trade("short-close", "short-exit", "buy", trading.PositionSideShort, trading.PositionEffectClose, "stop_loss", "exchange", base.Add(3*time.Hour)),
	}

	positions := buildAnalysisPositionTrades(config.Config{}, trades, base.Add(-time.Minute))
	if len(positions) != 2 {
		t.Fatalf("positions len=%d positions=%#v", len(positions), positions)
	}
	bySide := map[string]analysisPositionTrade{}
	for _, position := range positions {
		bySide[position.Side] = position
	}
	long := bySide[trading.PositionSideLong]
	if long.EntryOperation != "open" || long.EntryOperationSource != "auto" || long.ExitOperation != "take_profit" || long.ExitOperationSource != "monitor" {
		t.Fatalf("long operation metadata=%#v", long)
	}
	short := bySide[trading.PositionSideShort]
	if short.EntryOperation != "open" || short.EntryOperationSource != "manual" || short.ExitOperation != "stop_loss" || short.ExitOperationSource != "exchange" {
		t.Fatalf("short operation metadata=%#v", short)
	}
}

func TestBuildAnalysisPositionTradesMergesMixedOperationMetadata(t *testing.T) {
	base := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	fill := func(id, ordID, side, effect, operation, source string, at time.Time) analysisTrade {
		return analysisTrade{
			Exchange:        trading.ExchangeOKX,
			APIID:           "default",
			InstID:          "TEST-USDT-SWAP",
			TradeID:         id,
			OrdID:           ordID,
			Side:            side,
			PosSide:         trading.PositionSideLong,
			PositionEffect:  effect,
			Operation:       operation,
			OperationSource: source,
			FillPx:          "100",
			FillSz:          "1",
			FillTime:        at,
			FillTS:          at.UnixMilli(),
			FillCount:       1,
		}
	}
	trades := []analysisTrade{
		fill("open-a", "entry", "buy", trading.PositionEffectOpen, "open", "auto", base),
		fill("open-b", "entry", "buy", trading.PositionEffectOpen, "open", "auto", base.Add(time.Minute)),
		fill("close-a", "exit-a", "sell", trading.PositionEffectClose, "take_profit", "monitor", base.Add(time.Hour)),
		fill("close-b", "exit-b", "sell", trading.PositionEffectClose, "close", "exchange", base.Add(2*time.Hour)),
	}

	positions := buildAnalysisPositionTrades(config.Config{}, trades, base.Add(-time.Minute))
	if len(positions) != 1 {
		t.Fatalf("positions len=%d positions=%#v", len(positions), positions)
	}
	position := positions[0]
	if position.EntryOperation != "open" || position.EntryOperationSource != "auto" || position.ExitOperation != "mixed" || position.ExitOperationSource != "mixed" {
		t.Fatalf("mixed operation metadata=%#v", position)
	}
}

func TestBuildAnalysisPositionTradesCapturesGammaDirection(t *testing.T) {
	cfg := config.Config{Symbols: map[string]config.SymbolConfig{
		"TEST": {InstID: "TEST-USDT-SWAP", CtVal: 1},
	}}
	base := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	trade := func(id, ordID, side, positionSide, sourceAction string, at time.Time, pnl string) analysisTrade {
		return analysisTrade{
			Exchange:     trading.ExchangeBinance,
			APIID:        "default",
			InstID:       "TESTUSDT",
			TradeID:      id,
			OrdID:        ordID,
			Side:         side,
			PosSide:      positionSide,
			SourceAction: sourceAction,
			FillPx:       "100",
			FillSz:       "1",
			FillPnl:      pnl,
			Fee:          "-0.01",
			Leverage:     10,
			FillTime:     at,
			FillTS:       at.UnixMilli(),
			FillCount:    1,
		}
	}
	trades := []analysisTrade{
		trade("forward-open", "forward-entry", "buy", trading.PositionSideLong, trading.PositionSideLong, base, ""),
		trade("forward-close", "forward-exit", "sell", trading.PositionSideLong, "", base.Add(time.Hour), "1"),
		trade("reverse-open", "reverse-entry", "sell", trading.PositionSideShort, trading.PositionSideLong, base.Add(2*time.Hour), ""),
		trade("reverse-close", "reverse-exit", "buy", trading.PositionSideShort, "", base.Add(3*time.Hour), "-1"),
	}

	positions := buildAnalysisPositionTrades(cfg, trades, base)
	if len(positions) != 2 {
		t.Fatalf("positions len=%d positions=%#v", len(positions), positions)
	}
	byEntry := map[string]analysisPositionTrade{}
	for _, position := range positions {
		byEntry[position.EntryOrdID] = position
	}
	if got := byEntry["forward-entry"].GammaDirection; got != "forward" {
		t.Fatalf("forward Gamma direction=%q, want forward", got)
	}
	if got := byEntry["reverse-entry"].GammaDirection; got != "reverse" {
		t.Fatalf("reverse Gamma direction=%q, want reverse", got)
	}
}

func TestBuildAnalysisPositionTradesCapturesStrategy(t *testing.T) {
	base := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	trades := []analysisTrade{
		{
			Exchange:       trading.ExchangeBinance,
			APIID:          "default",
			InstID:         "TESTUSDT",
			TradeID:        "strategy-open",
			OrdID:          "strategy-entry",
			Side:           "buy",
			PosSide:        trading.PositionSideLong,
			PositionEffect: trading.PositionEffectOpen,
			SourceAction:   string(trading.ActionLong),
			Strategy:       "value_area",
			FillPx:         "100",
			FillSz:         "1",
			Fee:            "-0.01",
			Leverage:       10,
			FillTime:       base,
			FillTS:         base.UnixMilli(),
			FillCount:      1,
		},
		{
			Exchange:       trading.ExchangeBinance,
			APIID:          "default",
			InstID:         "TESTUSDT",
			TradeID:        "strategy-close",
			OrdID:          "strategy-exit",
			Side:           "sell",
			PosSide:        trading.PositionSideLong,
			PositionEffect: trading.PositionEffectClose,
			FillPx:         "101",
			FillSz:         "1",
			FillPnl:        "1",
			Fee:            "-0.01",
			Leverage:       10,
			FillTime:       base.Add(time.Hour),
			FillTS:         base.Add(time.Hour).UnixMilli(),
			FillCount:      1,
		},
	}

	positions := buildAnalysisPositionTrades(config.Config{}, trades, base.Add(-time.Minute))
	if len(positions) != 1 || positions[0].Strategy != "value_area" {
		t.Fatalf("strategy metadata should follow the entry order: %#v", positions)
	}
}

func TestBuildAnalysisPositionTradesSeparatesExternalShortExit(t *testing.T) {
	base := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	trades := []analysisTrade{
		{
			Exchange:       trading.ExchangeBinance,
			APIID:          "default",
			InstID:         "TESTUSDT",
			TradeID:        "external-open",
			OrdID:          "external-entry",
			Side:           "sell",
			PosSide:        trading.PositionSideShort,
			PositionEffect: trading.PositionEffectOpen,
			Strategy:       "value_area",
			FillPx:         "100",
			FillSz:         "1",
			FillTime:       base,
			FillTS:         base.UnixMilli(),
			FillCount:      1,
		},
		{
			Exchange:       trading.ExchangeBinance,
			APIID:          "default",
			InstID:         "TESTUSDT",
			TradeID:        "external-close",
			OrdID:          "external-exit",
			Side:           "buy",
			PosSide:        trading.PositionSideShort,
			PositionEffect: trading.PositionEffectClose,
			FillPx:         "101",
			FillSz:         "1",
			FillPnl:        "-1",
			FillTime:       base.Add(2 * time.Minute),
			FillTS:         base.Add(2 * time.Minute).UnixMilli(),
			FillCount:      1,
		},
	}

	positions := buildAnalysisPositionTrades(config.Config{}, trades, base.Add(-time.Minute))
	if len(positions) != 1 {
		t.Fatalf("positions len=%d positions=%#v", len(positions), positions)
	}
	position := positions[0]
	if position.Strategy != "value_area" {
		t.Fatalf("entry strategy should remain value_area: %#v", position)
	}
	if position.ExitSource != "exchange" {
		t.Fatalf("unmatched local exit should be marked as exchange: %#v", position)
	}
	if !position.MinHoldingViolation {
		t.Fatalf("two-minute exit should be marked below the minimum holding period: %#v", position)
	}
}

func TestBuildAnalysisPositionTradesEmptyIsJSONArray(t *testing.T) {
	rows := buildAnalysisPositionTrades(config.Config{}, nil, time.Time{})
	if rows == nil {
		t.Fatal("empty position trades should be a non-nil slice")
	}
	payload, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != "[]" {
		t.Fatalf("empty position trades JSON=%s, want []", payload)
	}
}

func TestAnalysisExternalDataExposesOpenGateTradeSeparately(t *testing.T) {
	srv := newTestServer(t)
	cfg := srv.ConfigStore.Get()
	now := srv.now()
	open := analysisTrade{
		Exchange:       trading.ExchangeGate,
		APIID:          "gate-main",
		InstID:         "BTC_USDT",
		TradeID:        "gate-open-trade",
		OrdID:          "gate-open-order",
		Side:           "buy",
		PosSide:        trading.PositionSideLong,
		PositionEffect: trading.PositionEffectOpen,
		FillPx:         "100",
		FillSz:         "2",
		Fee:            "-0.1",
		FeeCcy:         "USDT",
		FillTime:       now.Add(-time.Hour),
		FillTS:         now.Add(-time.Hour).UnixMilli(),
		FillCount:      1,
	}
	resp := srv.analysisFromExternalData(cfg, "", "", "gate-main", "demo", 1, 60, now, analysisSourceStatus{Gate: "gate"}, nil, nil, []analysisTrade{open}, nil)
	if len(resp.PositionTrades) != 0 || len(resp.OpenPositionTrades) != 1 {
		t.Fatalf("open Gate trade should be separate from closed positions: closed=%#v open=%#v", resp.PositionTrades, resp.OpenPositionTrades)
	}
	row := resp.OpenPositionTrades[0]
	if row.Exchange != trading.ExchangeGate || row.InstID != "BTC_USDT" || row.Side != trading.PositionSideLong || row.EntryOrdID != "gate-open-order" || row.Qty != "2" || row.EntryPx != "100" || !row.ExitTime.IsZero() {
		t.Fatalf("unexpected open Gate row: %#v", row)
	}
}

func TestBuildAnalysisPositionTradesMergesClosedFillsByEntryOrder(t *testing.T) {
	cfg := config.Config{Symbols: map[string]config.SymbolConfig{
		"TEST": {InstID: "TEST-USDT-SWAP", CtVal: 1},
	}}
	periodSince := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	tradeAt := func(hours int) (time.Time, int64) {
		ts := periodSince.Add(time.Duration(hours) * time.Hour)
		return ts, ts.UnixMilli()
	}
	fillTime, fillTS := tradeAt(-2)
	trades := []analysisTrade{
		{Exchange: trading.ExchangeOKX, APIID: "default", InstID: "TEST-USDT-SWAP", TradeID: "long-open-a", OrdID: "long-entry", Side: "buy", PosSide: "long", FillPx: "100", FillSz: "2", Fee: "-0.2", FeeCcy: "USDT", Leverage: 10, FillTime: fillTime, FillTS: fillTS, FillCount: 1},
	}
	fillTime, fillTS = tradeAt(-1)
	trades = append(trades, analysisTrade{Exchange: trading.ExchangeOKX, APIID: "default", InstID: "TEST-USDT-SWAP", TradeID: "long-open-b", OrdID: "long-entry", Side: "buy", PosSide: "long", FillPx: "110", FillSz: "3", Fee: "-0.3", FeeCcy: "USDT", Leverage: 10, FillTime: fillTime, FillTS: fillTS, FillCount: 1})
	fillTime, fillTS = tradeAt(1)
	trades = append(trades, analysisTrade{Exchange: trading.ExchangeOKX, APIID: "default", InstID: "TEST-USDT-SWAP", TradeID: "long-close-a", OrdID: "long-exit-a", Side: "sell", PosSide: "long", FillPx: "120", FillSz: "1", FillPnl: "20", Fee: "-0.1", FeeCcy: "USDT", FillTime: fillTime, FillTS: fillTS, FillCount: 1})
	fillTime, fillTS = tradeAt(2)
	trades = append(trades, analysisTrade{Exchange: trading.ExchangeOKX, APIID: "default", InstID: "TEST-USDT-SWAP", TradeID: "long-close-b", OrdID: "long-exit-b", Side: "sell", PosSide: "long", FillPx: "130", FillSz: "4", FillPnl: "80", Fee: "-0.4", FeeCcy: "USDT", FillTime: fillTime, FillTS: fillTS, FillCount: 1})
	fillTime, fillTS = tradeAt(3)
	trades = append(trades, analysisTrade{Exchange: trading.ExchangeOKX, APIID: "default", InstID: "TEST-USDT-SWAP", TradeID: "short-open", OrdID: "short-entry", Side: "sell", PosSide: "short", FillPx: "200", FillSz: "2", Fee: "-0.2", FeeCcy: "USDT", Leverage: 10, FillTime: fillTime, FillTS: fillTS, FillCount: 1})
	fillTime, fillTS = tradeAt(4)
	trades = append(trades, analysisTrade{Exchange: trading.ExchangeOKX, APIID: "default", InstID: "TEST-USDT-SWAP", TradeID: "short-close-a", OrdID: "short-exit-a", Side: "buy", PosSide: "short", FillPx: "210", FillSz: "0.5", FillPnl: "-5", Fee: "-0.05", FeeCcy: "USDT", FillTime: fillTime, FillTS: fillTS, FillCount: 1})
	fillTime, fillTS = tradeAt(5)
	trades = append(trades, analysisTrade{Exchange: trading.ExchangeOKX, APIID: "default", InstID: "TEST-USDT-SWAP", TradeID: "short-close-b", OrdID: "short-exit-b", Side: "buy", PosSide: "short", FillPx: "205", FillSz: "1.5", FillPnl: "-7.5", Fee: "-0.15", FeeCcy: "USDT", FillTime: fillTime, FillTS: fillTS, FillCount: 1})
	fillTime, fillTS = tradeAt(6)
	trades = append(trades, analysisTrade{Exchange: trading.ExchangeOKX, APIID: "default", InstID: "TEST-USDT-SWAP", TradeID: "partial-open", OrdID: "partial-entry", Side: "buy", PosSide: "long", FillPx: "50", FillSz: "2", Fee: "-0.2", FeeCcy: "USDT", Leverage: 10, FillTime: fillTime, FillTS: fillTS, FillCount: 1})
	fillTime, fillTS = tradeAt(7)
	trades = append(trades, analysisTrade{Exchange: trading.ExchangeOKX, APIID: "default", InstID: "TEST-USDT-SWAP", TradeID: "partial-close", OrdID: "partial-exit", Side: "sell", PosSide: "long", FillPx: "55", FillSz: "1", FillPnl: "5", Fee: "-0.1", FeeCcy: "USDT", FillTime: fillTime, FillTS: fillTS, FillCount: 1})

	positions := buildAnalysisPositionTrades(cfg, trades, periodSince)
	if len(positions) != 2 {
		t.Fatalf("positions len=%d positions=%#v", len(positions), positions)
	}
	bySide := map[string]analysisPositionTrade{}
	for _, position := range positions {
		bySide[position.Side] = position
	}
	long := bySide[trading.PositionSideLong]
	if long.EntryOrdID != "long-entry" || long.ExitOrdID != "long-exit-a / long-exit-b" || long.Qty != "5" || long.EntryPx != "106" || long.ExitPx != "128" {
		t.Fatalf("bad merged long identity/prices: %#v", long)
	}
	if long.Margin != "53" || long.RealizedPnL != "100" || long.Fee != "-1" || long.NetPnL != "99" || long.Turnover != "1170" || long.FillCount != 4 {
		t.Fatalf("bad merged long totals: %#v", long)
	}
	short := bySide[trading.PositionSideShort]
	if short.EntryOrdID != "short-entry" || short.ExitOrdID != "short-exit-a / short-exit-b" || short.Qty != "2" || short.EntryPx != "200" || short.ExitPx != "206.25" {
		t.Fatalf("bad merged short identity/prices: %#v", short)
	}
	if short.Margin != "40" || short.RealizedPnL != "-12.5" || short.Fee != "-0.4" || short.NetPnL != "-12.9" || short.Turnover != "812.5" || short.FillCount != 3 {
		t.Fatalf("bad merged short totals: %#v", short)
	}
	summary, _, _ := computeStats(cfg, positions, trades, periodSince)
	if summary.TradeCount != 2 || summary.Wins != 1 || summary.Losses != 1 || math.Abs(summary.NetPnL-86.1) > 0.0000001 {
		t.Fatalf("bad merged summary: %#v", summary)
	}
}

func TestBalanceWindowStatsComputesChangeAndMaxDrawdown(t *testing.T) {
	base := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	got := balanceWindowStats([]analysisBalancePoint{
		{Time: base, TS: base.UnixMilli(), Value: 100},
		{Time: base.Add(time.Hour), TS: base.Add(time.Hour).UnixMilli(), Value: 120},
		{Time: base.Add(2 * time.Hour), TS: base.Add(2 * time.Hour).UnixMilli(), Value: 90},
		{Time: base.Add(3 * time.Hour), TS: base.Add(3 * time.Hour).UnixMilli(), Value: 110},
	}, analysisBalance{})
	if got.StartValue != 100 || got.CurrentValue != 110 || got.Change != 10 || math.Abs(got.ChangePct-0.1) > 0.0000001 {
		t.Fatalf("bad window change: %#v", got)
	}
	if got.MaxDrawdown != 30 || math.Abs(got.MaxDrawdownPct-0.25) > 0.0000001 {
		t.Fatalf("bad max drawdown: %#v", got)
	}
}

func TestBalanceWindowStatsWarnsAboutFundingLikeCashJumpAndSourceMix(t *testing.T) {
	base := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	got := balanceWindowStats([]analysisBalancePoint{
		{Time: base, TS: base.UnixMilli(), Value: 10000, CashBal: "10000", EqSource: "legacy"},
		{Time: base.Add(time.Hour), TS: base.Add(time.Hour).UnixMilli(), Value: 20000, CashBal: "20000", EqSource: "binance_account_v3"},
	}, analysisBalance{})
	if len(got.Warnings) != 2 {
		t.Fatalf("warnings=%#v, want source and cash jump warnings", got.Warnings)
	}
	if got.Warnings[0] != "快照口径已切换，周期变化未跨口径校正" || got.Warnings[1] != "检测到现金余额大幅跳变，周期变化可能包含资金流" {
		t.Fatalf("unexpected warnings=%#v", got.Warnings)
	}
}
