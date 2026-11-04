package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	tvbotconfig "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotconfig"
	tvbotgate "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotgate"
	tvbottrading "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

func TestBuildAnalysisSupportsGateWithoutOKX(t *testing.T) {
	srv := newTestServer(t)
	now := srv.now()
	entryTime := now.Add(-2 * time.Hour)
	exitTime := now.Add(-time.Hour)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v4/futures/usdt/contracts":
			_, _ = w.Write([]byte(`[{"name":"BTC_USDT","order_price_round":"0.1","quanto_multiplier":"0.0001","enable_decimal":false}]`))
		case "/api/v4/futures/usdt/accounts":
			_, _ = w.Write([]byte(`{"currency":"USDT","total":"1000","available":"900"}`))
		case "/api/v4/futures/usdt/my_trades_timerange":
			_, _ = w.Write([]byte(`[
                {"id":101,"contract":"BTC_USDT","order_id":1001,"size":2,"price":100,"create_time":` + formatGateTestTime(entryTime) + `,"close_size":0,"trade_value":20,"fee":-0.1},
                {"id":102,"contract":"BTC_USDT","order_id":1002,"size":-2,"price":110,"create_time":` + formatGateTestTime(exitTime) + `,"close_size":-2,"trade_value":22,"fee":-0.1}
            ]`))
		case "/api/v4/futures/usdt/position_close":
			_, _ = w.Write([]byte(`[{"time":` + formatGateTestTime(exitTime) + `,"contract":"BTC_USDT","side":"long","pnl":19.8,"pnl_pnl":20,"pnl_fund":0,"pnl_fee":-0.2,"max_size":2,"accum_size":2,"first_open_time":` + formatGateTestTime(entryTime) + `,"long_price":100,"short_price":110}]`))
		default:
			t.Fatalf("unexpected Gate path: %s", r.URL.Path)
		}
	}))
	defer upstream.Close()

	gateStore, err := tvbotgate.NewCredentialStore("", tvbotgate.Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gateStore.UpdateAccount(tvbotgate.CredentialAccountUpdate{
		ID: "gate-main", Active: true,
		Credentials: tvbotgate.Credentials{APIKey: "key", SecretKey: "secret"},
	}); err != nil {
		t.Fatal(err)
	}
	srv.GateCredentials = gateStore
	srv.GateHTTPClient = upstream.Client()
	cfg := srv.ConfigStore.Get()
	cfg.Trading.GateBaseURL = upstream.URL
	cfg.Trading.GateDemoBaseURL = upstream.URL
	srv.ConfigStore = tvbotconfig.NewStore("", cfg)

	got, err := srv.buildAnalysis(context.Background(), cfg, "", "", 3, 12*60, true, "gate-main")
	if err != nil {
		t.Fatalf("Gate-only analysis failed: %v", err)
	}
	if !got.OK || got.GateAPIID != "gate-main" || !strings.Contains(got.Cache.CacheKey, "gate:gate-main") {
		t.Fatalf("bad Gate-only response metadata: %#v", got)
	}
	if got.Source.Gate != "gate" {
		t.Fatalf("Gate source status=%q", got.Source.Gate)
	}
	if len(got.ExchangeSummaries) != 1 || got.ExchangeSummaries[0].Exchange != tvbottrading.ExchangeGate {
		t.Fatalf("exchange summaries=%#v", got.ExchangeSummaries)
	}
	stats := got.ExchangeSummaries[0]
	if stats.TradeCount != 1 || stats.Wins != 1 || stats.Losses != 0 || stats.NetPnL != 19.8 || stats.Fees != -0.2 || stats.Turnover != 42 {
		t.Fatalf("Gate stats=%#v", stats)
	}
	if len(got.PositionTrades) != 1 || got.PositionTrades[0].Exchange != tvbottrading.ExchangeGate || !got.PositionTrades[0].EntryTime.Equal(entryTime) || !got.PositionTrades[0].ExitTime.Equal(exitTime) || got.PositionTrades[0].EntryPx != "100" || got.PositionTrades[0].ExitPx != "110" || got.PositionTrades[0].Leverage != cfg.Trading.Leverage || got.PositionTrades[0].Margin != "0.004" || got.PositionTrades[0].PricePrecision == nil || *got.PositionTrades[0].PricePrecision != 2 {
		t.Fatalf("Gate positions=%#v", got.PositionTrades)
	}
}

func TestApplyGatePositionStrategiesFromOpeningTrades(t *testing.T) {
	base := time.Date(2026, 11, 4, 1, 0, 0, 0, time.UTC)
	positions := []analysisPositionTrade{{
		Exchange:  tvbottrading.ExchangeGate,
		APIID:     "gate-main",
		InstID:    "BTC_USDT",
		Side:      tvbottrading.PositionSideLong,
		EntryTime: base,
		ExitTime:  base.Add(time.Hour),
	}}
	trades := []analysisTrade{{
		Exchange:       tvbottrading.ExchangeGate,
		APIID:          "gate-main",
		InstID:         "BTC_USDT",
		PosSide:        tvbottrading.PositionSideLong,
		PositionEffect: tvbottrading.PositionEffectOpen,
		Strategy:       "value_area",
		FillTime:       base,
	}}

	applyGatePositionStrategies(positions, trades)
	if positions[0].Strategy != "value_area" {
		t.Fatalf("Gate position strategy=%q, want value_area", positions[0].Strategy)
	}
}

func TestAnalysisExternalDataEnrichesGateClosedPositionStrategy(t *testing.T) {
	srv := newTestServer(t)
	cfg := srv.ConfigStore.Get()
	base := time.Date(2026, 11, 4, 1, 0, 0, 0, time.UTC)
	open := analysisTrade{
		Exchange:       tvbottrading.ExchangeGate,
		APIID:          "gate-main",
		InstID:         "BTC_USDT",
		TradeID:        "gate-open",
		OrdID:          "gate-entry",
		PosSide:        tvbottrading.PositionSideLong,
		PositionEffect: tvbottrading.PositionEffectOpen,
		Strategy:       "value_area",
		FillPx:         "100",
		FillSz:         "2",
		FillTime:       base,
		FillTS:         base.UnixMilli(),
		FillCount:      1,
	}
	closed := analysisPositionTrade{
		Exchange:    tvbottrading.ExchangeGate,
		APIID:       "gate-main",
		InstID:      "BTC_USDT",
		Side:        tvbottrading.PositionSideLong,
		EntryTime:   base,
		EntryTS:     base.UnixMilli(),
		ExitTime:    base.Add(time.Hour),
		ExitTS:      base.Add(time.Hour).UnixMilli(),
		EntryPx:     "100",
		ExitPx:      "110",
		Qty:         "2",
		RealizedPnL: "20",
	}
	resp := srv.analysisFromExternalData(cfg, "", "", "gate-main", "demo", 1, 120, base.Add(2*time.Hour), analysisSourceStatus{Gate: "gate"}, nil, nil, []analysisTrade{open}, []analysisPositionTrade{closed})
	if len(resp.PositionTrades) != 1 || resp.PositionTrades[0].Strategy != "value_area" {
		t.Fatalf("Gate closed position strategy=%#v", resp.PositionTrades)
	}
}

func TestHandleAnalysisReturnsGateDataWhenOKXIsNotConfigured(t *testing.T) {
	srv := newTestServer(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v4/futures/usdt/contracts":
			_, _ = w.Write([]byte(`[]`))
		case "/api/v4/futures/usdt/accounts":
			_, _ = w.Write([]byte(`{"currency":"USDT","total":"1000","available":"900"}`))
		case "/api/v4/futures/usdt/my_trades_timerange":
			_, _ = w.Write([]byte(`[]`))
		case "/api/v4/futures/usdt/position_close":
			_, _ = w.Write([]byte(`[]`))
		default:
			t.Fatalf("unexpected Gate path: %s", r.URL.Path)
		}
	}))
	defer upstream.Close()
	gateStore, err := tvbotgate.NewCredentialStore("", tvbotgate.Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gateStore.UpdateAccount(tvbotgate.CredentialAccountUpdate{ID: "gate-main", Active: true, Credentials: tvbotgate.Credentials{APIKey: "key", SecretKey: "secret"}}); err != nil {
		t.Fatal(err)
	}
	srv.GateCredentials = gateStore
	srv.GateHTTPClient = upstream.Client()
	cfg := srv.ConfigStore.Get()
	cfg.Trading.GateBaseURL = upstream.URL
	cfg.Trading.GateDemoBaseURL = upstream.URL
	srv.ConfigStore = tvbotconfig.NewStore("", cfg)

	req := httptest.NewRequest(http.MethodGet, "/tvbot/analysis?pnl_minutes=720&refresh=true", nil)
	resp := httptest.NewRecorder()
	srv.handleAnalysis(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
	}
	var payload analysisResponse
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.OK || payload.GateAPIID != "gate-main" {
		t.Fatalf("payload=%#v", payload)
	}
}

func formatGateTestTime(value time.Time) string {
	return strconv.FormatInt(value.Unix(), 10)
}
