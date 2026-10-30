package server

import (
	"context"
	"encoding/json"
	"errors"
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
		OrderIntent:    "sl_long",
		PositionEffect: trading.PositionEffectClose,
		RawJSON:        `{"source":"liquidation_alert"}`,
	}
	operation, source := analysisOrderOperation(record, record.PositionEffect)
	if operation != "stop_loss" || source != "auto" {
		t.Fatalf("unexpected order metadata operation=%q source=%q", operation, source)
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
	meta, ok := analysisTradeMonitorEventIndex(events)[analysisOrderKey(trading.ExchangeBinance, "binance-main", "ETHUSDT", "exit-1")]
	if !ok || meta.Operation != "take_profit" || meta.OperationSource != "auto" {
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
	if err == nil || !strings.Contains(err.Error(), "BADUSDT") || !strings.Contains(err.Error(), "-1121") {
		t.Fatalf("expected symbol-specific Binance error, got %v", err)
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
	if err == nil || !strings.Contains(err.Error(), "PARTIALUSDT") || !strings.Contains(err.Error(), "SECONDUSDT") {
		t.Fatalf("expected aggregated symbol errors, got %v", err)
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

func TestAnalysisBalanceFromBinanceIncludesUSDTPositionUnrealizedPnL(t *testing.T) {
	got := analysisBalanceFromBinance([]binance.Balance{
		{
			Asset:              "USDT",
			Balance:            "10306",
			AvailableBalance:   "9900.25",
			CrossWalletBalance: "10000",
			UpdateTime:         1785522000000,
		},
	}, []binance.Position{
		{
			Symbol:           "MANTAUSDT",
			PositionAmt:      "63222.3",
			MarginAsset:      "USDT",
			UnRealizedProfit: "-340.12345678",
		},
		{
			Symbol:           "MEWUSDT",
			PositionAmt:      "-15342129",
			MarginAsset:      "USDT",
			UnRealizedProfit: "20",
		},
		{
			Symbol:           "ETHUSDT",
			PositionAmt:      "0",
			MarginAsset:      "USDT",
			UnRealizedProfit: "999",
		},
		{
			Symbol:           "BTCUSDC",
			PositionAmt:      "1",
			MarginAsset:      "USDC",
			UnRealizedProfit: "999",
		},
	})
	if got.TotalEq != "9985.87654322" || got.AvailEq != "9900.25" {
		t.Fatalf("bad Binance balance: %#v", got)
	}
	if len(got.Details) != 1 || got.Details[0].Eq != "9985.87654322" || got.Details[0].EqUsd != "9985.87654322" || got.Details[0].CashBal != "10000" {
		t.Fatalf("bad Binance USDT detail: %#v", got.Details)
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
