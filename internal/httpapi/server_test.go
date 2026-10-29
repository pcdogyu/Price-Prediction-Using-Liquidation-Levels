package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/app"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/authn"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/config"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/observability"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/store"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/volumeprofile"
)

func testService(t *testing.T, cfg config.Config, logger *slog.Logger, logs *observability.Store) (*Server, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Symbols) == 0 {
		cfg.Symbols = []string{"BTCUSDT", "ETHUSDT"}
	}
	if cfg.Address == "" {
		cfg.Address = ":0"
	}
	svc := app.New(cfg, st, logger)
	srv, err := New(cfg, svc, logger, logs, st)
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	return srv, st
}

func TestSecurityHeadersPermitOnlyEmbeddedCharts(t *testing.T) {
	handler := securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	cases := []struct {
		path           string
		frameOption    string
		frameAncestors string
	}{
		{path: "/bubbles?embed=1", frameOption: "SAMEORIGIN", frameAncestors: "frame-ancestors 'self'"},
		{path: "/alerts/chart?position=1", frameOption: "SAMEORIGIN", frameAncestors: "frame-ancestors 'self'"},
		{path: "/alerts/chart?symbol=BTCUSDT", frameOption: "DENY", frameAncestors: "frame-ancestors 'none'"},
		{path: "/bubbles", frameOption: "DENY", frameAncestors: "frame-ancestors 'none'"},
		{path: "/tvbot?tab=positions", frameOption: "DENY", frameAncestors: "frame-ancestors 'none'"},
	}
	for _, tc := range cases {
		r := httptest.NewRequest(http.MethodGet, tc.path, nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if got := w.Header().Get("X-Frame-Options"); got != tc.frameOption {
			t.Fatalf("%s X-Frame-Options=%q, want %q", tc.path, got, tc.frameOption)
		}
		if got := w.Header().Get("Content-Security-Policy"); !strings.Contains(got, tc.frameAncestors) {
			t.Fatalf("%s Content-Security-Policy=%q, missing %q", tc.path, got, tc.frameAncestors)
		}
	}
}

func TestBusinessPagesRenderVersionFooter(t *testing.T) {
	originalTime, originalRevision, originalBranch := BuildTime, BuildRevision, BuildBranch
	t.Cleanup(func() {
		BuildTime, BuildRevision, BuildBranch = originalTime, originalRevision, originalBranch
	})
	BuildTime = "2026-10-17T20:26:29+08:00"
	BuildRevision = "ec01b5c"
	BuildBranch = "main"

	srv, st := testService(t, config.Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	defer st.Close()
	wantVersion := "Code by Yuhao@jiansutech.com · 2026-10-17T20:26:29+08:00 · ec01b5c · main"
	for _, path := range []string{"/bubbles", "/liquidations", "/hedge-wall", "/market-info", "/options", "/alerts", "/alerts/chart?symbol=BTCUSDT"} {
		w := httptest.NewRecorder()
		srv.http.Handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		body := w.Body.String()
		if w.Code != http.StatusOK || strings.Count(body, wantVersion) != 1 || strings.Count(body, `class="page-footer"`) != 1 {
			t.Fatalf("path=%s status=%d version_count=%d footer_count=%d", path, w.Code, strings.Count(body, wantVersion), strings.Count(body, `class="page-footer"`))
		}
	}
}

func TestRoutesAndDashboardAssets(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	_, logs, err := observability.New(filepath.Join(t.TempDir(), "application.log"), 7)
	if err != nil {
		t.Fatal(err)
	}
	defer logs.Close()
	srv, st := testService(t, config.Config{}, logger, logs)
	defer st.Close()

	r := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("health route status=%d body=%s", w.Code, w.Body.String())
	}
	r = httptest.NewRequest(http.MethodPost, "/healthz", nil)
	w = httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status=%d", w.Code)
	}
	nextCapture := time.Now().UTC().Add(10 * time.Minute).Truncate(time.Second)
	srv.setCoinGlassSchedule(10*time.Minute, nextCapture)
	r = httptest.NewRequest(http.MethodGet, "/api/v1/coinglass/schedule", nil)
	w = httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"interval_seconds":600`) || !strings.Contains(w.Body.String(), `"next_capture_at":"`+nextCapture.Format(time.RFC3339)) {
		t.Fatalf("CoinGlass schedule status=%d body=%s", w.Code, w.Body.String())
	}

	now := time.Now().UTC().Truncate(time.Minute)
	if err = st.UpsertCandles(context.Background(), []domain.Candle{{Exchange: "binance", Symbol: "BTCUSDT", Time: now, Open: 100, High: 102, Low: 99, Close: 101, VolumeUSD: 10}, {Exchange: "okx", Symbol: "BTCUSDT", Time: now, Open: 101, High: 103, Low: 100, Close: 102, VolumeUSD: 20}}); err != nil {
		t.Fatal(err)
	}
	if err = st.SavePrediction(context.Background(), domain.Prediction{Symbol: "BTCUSDT", Time: now, State: "ok", MarkPrice: 101, LeadingClass: domain.UpperFirst, ModelVersion: "test-v1", Probabilities: map[string]float64{domain.UpperFirst: .6}}); err != nil {
		t.Fatal(err)
	}
	if err = st.InsertLiquidation(context.Background(), domain.LiquidationEvent{ID: "binance-live", Exchange: "binance", Symbol: "BTCUSDT", PositionSide: "long", EventTime: now, ReceivedAt: now, Price: 100, Quantity: 300, NotionalUSD: 30_000, Coverage: "sampled"}); err != nil {
		t.Fatal(err)
	}
	if err = st.InsertLiquidation(context.Background(), domain.LiquidationEvent{ID: "binance-small", Exchange: "binance", Symbol: "BTCUSDT", PositionSide: "short", EventTime: now, ReceivedAt: now, Price: 99, Quantity: 30, NotionalUSD: 3_000, Coverage: "sampled"}); err != nil {
		t.Fatal(err)
	}
	if err = st.InsertLiquidation(context.Background(), domain.LiquidationEvent{ID: "other-exchange", Exchange: "okx", Symbol: "BTCUSDT", PositionSide: "short", EventTime: now, ReceivedAt: now, Price: 102, Quantity: 1, NotionalUSD: 102, Coverage: "full"}); err != nil {
		t.Fatal(err)
	}
	srv.svc.RecordAggregateTrade(domain.AggregateTrade{ID: 12345, Symbol: "BTCUSDT", Time: now.Add(30 * time.Second), Price: 101.5, Quantity: 2})
	r = httptest.NewRequest(http.MethodGet, "/api/v1/market?symbol=BTCUSDT", nil)
	w = httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"source":"binance_usdm"`) || !strings.Contains(w.Body.String(), `"exchange_count":1`) || !strings.Contains(w.Body.String(), `"side":"long"`) || !strings.Contains(w.Body.String(), `"id":"binance-live"`) || !strings.Contains(w.Body.String(), `"position_side":"long"`) || !strings.Contains(w.Body.String(), `"liquidations_truncated":false`) || !strings.Contains(w.Body.String(), `"liquidation_minimum_usd":0`) || !strings.Contains(w.Body.String(), `"last_price":101.5`) || !strings.Contains(w.Body.String(), `"realtime_price":{"trade_id":12345`) || !strings.Contains(w.Body.String(), `"id":"binance-small"`) || strings.Contains(w.Body.String(), `"id":"other-exchange"`) || strings.Contains(w.Body.String(), `"last_price":102`) {
		t.Fatalf("market status=%d body=%s", w.Code, w.Body.String())
	}
	r = httptest.NewRequest(http.MethodGet, "/api/v1/map?symbol=BTCUSDT", nil)
	w = httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "CoinGlass liquidation map unavailable") {
		t.Fatalf("CoinGlass-only map status=%d body=%s", w.Code, w.Body.String())
	}
	session, err := volumeprofile.SessionAt(now)
	if err != nil {
		t.Fatal(err)
	}
	trades := []domain.AggregateTrade{{ID: 1, Symbol: "BTCUSDT", Time: session.Start.Add(time.Minute), Price: 100, PriceText: "100", Quantity: 2}, {ID: 2, Symbol: "BTCUSDT", Time: session.Start.Add(2 * time.Minute), Price: 101, PriceText: "101", Quantity: 3}}
	if err = st.ApplyAggregateTrades(context.Background(), "BTCUSDT", session.Start, trades, true); err != nil {
		t.Fatal(err)
	}
	r = httptest.NewRequest(http.MethodGet, "/api/v1/volume-profile?symbol=BTCUSDT", nil)
	w = httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"source":"binance_usdm"`) || !strings.Contains(w.Body.String(), `"rows":24`) || !strings.Contains(w.Body.String(), `"volume_rank":1`) || !strings.Contains(w.Body.String(), `"value_area_fraction":0.7`) || strings.Contains(strings.ToLower(w.Body.String()), "poc") {
		t.Fatalf("volume profile status=%d body=%s", w.Code, w.Body.String())
	}
	for _, interval := range []string{"1m", "2m", "3m", "5m", "10m", "15m", "30m", "1h", "4h", "8h", "12h", "24h"} {
		r = httptest.NewRequest(http.MethodGet, "/api/v1/market?symbol=BTCUSDT&limit=120&interval="+interval, nil)
		w = httptest.NewRecorder()
		srv.http.Handler.ServeHTTP(w, r)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"interval":"`+interval+`"`) {
			t.Fatalf("interval=%s status=%d body=%s", interval, w.Code, w.Body.String())
		}
	}
	r = httptest.NewRequest(http.MethodGet, "/api/v1/market?symbol=not-a-symbol", nil)
	w = httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid symbol status=%d", w.Code)
	}
	r = httptest.NewRequest(http.MethodGet, "/api/v1/market?symbol=BTCUSDT&interval=7m", nil)
	w = httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid interval status=%d body=%s", w.Code, w.Body.String())
	}
	r = httptest.NewRequest(http.MethodGet, "/api/v1/market?symbol=BTCUSDT&interval=1m&limit=501", nil)
	w = httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid limit status=%d body=%s", w.Code, w.Body.String())
	}
	r = httptest.NewRequest(http.MethodGet, "/bubbles", nil)
	w = httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, r)
	if !strings.Contains(w.Body.String(), `src="assets/dashboard.js"`) || strings.Contains(w.Body.String(), `id="classic-pivot-title"`) || strings.Contains(w.Body.String(), `id="upper-status"`) || strings.Contains(w.Body.String(), `id="lower-status"`) || !strings.Contains(w.Body.String(), `data-interval="24h"`) || !strings.Contains(w.Body.String(), `viewBox="0 0 1740 560"`) || !strings.Contains(w.Body.String(), "Binance USDⓈ-M") || !strings.Contains(w.Body.String(), "多单爆仓") || !strings.Contains(w.Body.String(), "空单爆仓") || !strings.Contains(w.Body.String(), `id="coinglass-button"`) || !strings.Contains(w.Body.String(), `id="coinglass-capture"`) || !strings.Contains(w.Body.String(), `id="coinglass-json"`) || !strings.Contains(w.Body.String(), `id="liquidation-top3"`) || !strings.Contains(w.Body.String(), `id="browser-frame"`) || !strings.Contains(w.Body.String(), "正在读取下次时间") || strings.Contains(w.Body.String(), "async function refresh") {
		t.Fatal("dashboard script was not externalized")
	}
	if !strings.Contains(w.Body.String(), `data-symbol="ETHUSDT" class="active"`) || strings.Contains(w.Body.String(), `data-symbol="BTCUSDT" class="active"`) {
		t.Fatal("ETH should be the default dashboard symbol")
	}
	r = httptest.NewRequest(http.MethodGet, "/assets/dashboard.js", nil)
	w = httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "let symbol = 'ETHUSDT'") || !strings.Contains(w.Body.String(), "new URL('api/v1/'") || !strings.Contains(w.Body.String(), "volume-profile?symbol=") || !strings.Contains(w.Body.String(), "coinglass-login/vnc.html") || !strings.Contains(w.Body.String(), "coinglass-login/websockify") || !strings.Contains(w.Body.String(), "coinglass/capture") || !strings.Contains(w.Body.String(), "coinglass/schedule") || !strings.Contains(w.Body.String(), "updateCoinGlassCountdown") || !strings.Contains(w.Body.String(), "captureCountdown") || !strings.Contains(w.Body.String(), "coinglass_binance_liqmap") || !strings.Contains(w.Body.String(), "top_long_liquidations") || !strings.Contains(w.Body.String(), "bin.leverage_usd") || !strings.Contains(w.Body.String(), "layoutRankLabels") || !strings.Contains(w.Body.String(), "drawLiquidationRankLabels") || !strings.Contains(w.Body.String(), "drawLiquidationBubbles") || !strings.Contains(w.Body.String(), "drawLiquidationBiasArrow") || !strings.Contains(w.Body.String(), "liquidation_above_usd") || !strings.Contains(w.Body.String(), "liquidation_below_usd") || !strings.Contains(w.Body.String(), "liquidation_direction") || !strings.Contains(w.Body.String(), "liquidationRadius") || !strings.Contains(w.Body.String(), "chartTimeScale") || !strings.Contains(w.Body.String(), "drawTimeGrid") || !strings.Contains(w.Body.String(), "const fiveMinutes = 5 * 60 * 1000") || !strings.Contains(w.Body.String(), "defaultLiquidationMinimumUSD") || !strings.Contains(w.Body.String(), "defaultLiquidationLongOffset = 20") || !strings.Contains(w.Body.String(), "defaultLiquidationShortOffset = 20") || !strings.Contains(w.Body.String(), "installLiquidationControls") || !strings.Contains(w.Body.String(), "liquidationWallWidthScale = .9") || !strings.Contains(w.Body.String(), "window.BubbleChart.placements") || !strings.Contains(w.Body.String(), "mode = 'chart'") || !strings.Contains(w.Body.String(), "addEventListener('price'") || !strings.Contains(w.Body.String(), "applyPriceTick") || !strings.Contains(w.Body.String(), "aggTrade 实时") || !strings.Contains(w.Body.String(), "addEventListener('liquidation'") || !strings.Contains(w.Body.String(), "rankLabelRight") || !strings.Contains(w.Body.String(), "addEventListener('wheel'") || !strings.Contains(w.Body.String(), "pointerdown") || strings.Contains(w.Body.String(), "signal?.upper_wall?.price") || strings.Contains(w.Body.String(), "signal?.lower_wall?.price") || strings.Contains(w.Body.String(), "Math.round(i * (candles.length - 1) / 4)") {
		t.Fatalf("dashboard asset status=%d", w.Code)
	}
	r = httptest.NewRequest(http.MethodGet, "/assets/dashboard.js", nil)
	w = httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, r)
	// The wheel gesture is regional, so the shipped asset must keep the routing table and
	// the three zoom helpers that the browser regression asserts on.
	for _, marker := range []string{`const candleZoomLimits = { minimum: 24, maximum: 500 };`, `const profileZoomLimits = { minimum: .25, maximum: 12 };`, `function chartRegion(point)`, `function zoomCandleWindow(fraction, factor)`, `function zoomProfilePanel(region, factor)`, `function zoomPriceAxis(point, factor)`, `event.shiftKey || event.ctrlKey ? 'price' : chartRegion(point)`, `if (zoomProfilePanel(region, 1 / factor))`, `svg.setAttribute('data-liquidation-zoom', chart.liquidationZoom);`, `events.onopen = recoverStream;`} {
		if !strings.Contains(w.Body.String(), marker) {
			t.Fatalf("dashboard asset lost the region wheel zoom %q", marker)
		}
	}

	ts := httptest.NewServer(srv.http.Handler)
	defer ts.Close()
	streamResponse, err := ts.Client().Get(ts.URL + "/api/v1/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer streamResponse.Body.Close()
	if got := streamResponse.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("SSE content type=%q", got)
	}
	if got := streamResponse.Header.Get("X-Accel-Buffering"); got != "no" {
		t.Fatalf("SSE buffering header=%q", got)
	}
	reader := bufio.NewReader(streamResponse.Body)
	line, err := reader.ReadString('\n')
	if err != nil || line != "event: health\n" {
		t.Fatalf("initial SSE event=%q error=%v", line, err)
	}
	if _, err = reader.ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	if _, err = reader.ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	if err = srv.svc.RecordLiquidation(context.Background(), domain.LiquidationEvent{ID: "sse-live", Exchange: "binance", Symbol: "BTCUSDT", PositionSide: "short", EventTime: now, ReceivedAt: now, Price: 102, Quantity: 2, NotionalUSD: 204, Coverage: "sampled"}); err != nil {
		t.Fatal(err)
	}
	type readResult struct {
		line string
		err  error
	}
	next := make(chan readResult, 1)
	go func() {
		value, readErr := reader.ReadString('\n')
		next <- readResult{line: value, err: readErr}
	}()
	select {
	case result := <-next:
		if result.err != nil || result.line != "event: liquidation\n" {
			t.Fatalf("liquidation SSE event=%q error=%v", result.line, result.err)
		}
		dataLine, readErr := reader.ReadString('\n')
		if readErr != nil || !strings.HasPrefix(dataLine, "data: ") || !strings.Contains(dataLine, `"id":"sse-live"`) || !strings.Contains(dataLine, `"position_side":"short"`) {
			t.Fatalf("liquidation SSE data=%q error=%v", dataLine, readErr)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for liquidation SSE event")
	}
}

func TestAuthenticationAndProtectedLogs(t *testing.T) {
	const password = "test-password-not-for-production"
	hash, err := authn.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	logger, logs, err := observability.New(filepath.Join(t.TempDir(), "application.log"), 7)
	if err != nil {
		t.Fatal(err)
	}
	defer logs.Close()
	cfg := config.Config{BasePath: "/liquidation/", AuthUsername: "pcdog", AuthPasswordHash: hash}
	srv, st := testService(t, cfg, logger, logs)
	defer st.Close()

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "登录后查看行情") || !strings.Contains(w.Body.String(), "会话有效期 7 天") {
		t.Fatalf("login page status=%d body=%s", w.Code, w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "no-store, max-age=0" || w.Header().Get("Pragma") != "no-cache" || w.Header().Get("Expires") != "0" {
		t.Fatalf("login page must not be cached: %v", w.Header())
	}
	for _, path := range []string{"/bubbles", "/liquidations", "/hedge-wall", "/market-info", "/logs"} {
		page := httptest.NewRecorder()
		srv.http.Handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, path, nil))
		if page.Code != 200 || !strings.Contains(page.Body.String(), `name="password"`) || strings.Contains(page.Body.String(), `class="app-nav"`) {
			t.Fatalf("unprotected page %s status=%d", path, page.Code)
		}
	}
	for _, path := range []string{"/api/v1/liquidations", "/api/v1/hedge-wall?symbol=ETHUSDT", "/api/v1/hedge-wall/history?symbol=ETHUSDT", "/api/v1/market-info?symbol=ETHUSDT"} {
		api := httptest.NewRecorder()
		srv.http.Handler.ServeHTTP(api, httptest.NewRequest(http.MethodGet, path, nil))
		if api.Code != 401 {
			t.Fatalf("unprotected API %s status=%d", path, api.Code)
		}
	}
	r = httptest.NewRequest(http.MethodGet, "/api/v1/logs", nil)
	w = httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated logs status=%d", w.Code)
	}
	r = httptest.NewRequest(http.MethodGet, "/api/v1/auth/check", nil)
	w = httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated auth check status=%d", w.Code)
	}
	r = httptest.NewRequest(http.MethodGet, "/api/v1/auth/check", nil)
	r.Header.Set("Accept", "text/html")
	w = httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, r)
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/liquidation/?next=%2Fapi%2Fv1%2Fauth%2Fcheck" {
		t.Fatalf("browser auth check redirect status=%d location=%s body=%s", w.Code, w.Header().Get("Location"), w.Body.String())
	}
	for _, path := range []string{"/tvbot/view/dashboard", "/tvbot/view/positions", "/tvbot/view/trade-monitor", "/tvbot/view/symbols", "/tvbot/view/upgrade"} {
		page := httptest.NewRecorder()
		srv.http.Handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, path, nil))
		if page.Code != http.StatusUnauthorized {
			t.Fatalf("retired TVBot page %s status=%d location=%s body=%s", path, page.Code, page.Header().Get("Location"), page.Body.String())
		}
	}
	r = httptest.NewRequest(http.MethodPost, "/api/v1/coinglass/capture", nil)
	w = httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated CoinGlass capture status=%d", w.Code)
	}
	r = httptest.NewRequest(http.MethodGet, "/api/v1/coinglass/latest", nil)
	w = httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated latest CoinGlass status=%d", w.Code)
	}
	r = httptest.NewRequest(http.MethodGet, "/api/v1/coinglass/schedule", nil)
	w = httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated CoinGlass schedule status=%d", w.Code)
	}
	r = httptest.NewRequest(http.MethodGet, "/healthz", nil)
	r.RemoteAddr = "127.0.0.1:12345"
	w = httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("local health status=%d", w.Code)
	}
	r = httptest.NewRequest(http.MethodGet, "/healthz", nil)
	r.RemoteAddr = "127.0.0.1:12345"
	r.Header.Set("X-Forwarded-Prefix", "/liquidation")
	r.Header.Set("X-Real-IP", "203.0.113.8")
	w = httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("proxied health status=%d", w.Code)
	}

	badForm := url.Values{"username": {"pcdog"}, "password": {"do-not-record-this"}}
	r = httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(badForm.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("X-Real-IP", "203.0.113.8")
	w = httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized || strings.Contains(w.Body.String(), "do-not-record-this") {
		t.Fatalf("failed login status=%d body=%s", w.Code, w.Body.String())
	}

	form := url.Values{"username": {"pcdog"}, "password": {password}}
	r = httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("X-Real-IP", "203.0.113.9")
	w = httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, r)
	response := w.Result()
	if w.Code != http.StatusSeeOther || response.Header.Get("Location") != "/liquidation/alerts" {
		t.Fatalf("login status=%d location=%s body=%s", w.Code, response.Header.Get("Location"), w.Body.String())
	}
	var session *http.Cookie
	for _, cookie := range response.Cookies() {
		if cookie.Name == sessionCookie {
			session = cookie
		}
	}
	if session == nil || !session.Secure || !session.HttpOnly || session.SameSite != http.SameSiteStrictMode || session.Path != "/liquidation/" {
		t.Fatalf("session cookie=%+v", session)
	}
	if session.MaxAge < int((7*24*time.Hour-time.Minute).Seconds()) || session.MaxAge > int((7*24*time.Hour).Seconds()) {
		t.Fatalf("session cookie max age=%d", session.MaxAge)
	}
	r = httptest.NewRequest(http.MethodGet, "/api/v1/auth/check", nil)
	r.AddCookie(session)
	w = httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, r)
	if w.Code != http.StatusNoContent || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("authenticated auth check status=%d headers=%v", w.Code, w.Header())
	}
	restartedAuth, err := authn.New(cfg.AuthUsername, cfg.AuthPasswordHash, cfg.BasePath, st)
	if err != nil || !restartedAuth.Authenticated(session.Value) {
		t.Fatalf("session did not survive authentication manager restart: %v", err)
	}
	r = httptest.NewRequest(http.MethodGet, "/api/v1/logs?limit=50&level=INFO", nil)
	r.AddCookie(session)
	w = httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("logs status=%d body=%s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err = json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(body)
	if strings.Contains(string(encoded), "do-not-record-this") || !strings.Contains(string(encoded), "login succeeded") {
		t.Fatalf("unexpected log response=%s", encoded)
	}
	r = httptest.NewRequest(http.MethodGet, "/logs", nil)
	r.AddCookie(session)
	w = httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "<th scope=\"col\">时间</th>") || !strings.Contains(w.Body.String(), "<th scope=\"col\">字段</th>") || !strings.Contains(w.Body.String(), "/liquidation/assets/logs.js") || strings.Contains(w.Body.String(), "<dialog") || strings.Contains(w.Body.String(), "shared-logs") {
		t.Fatalf("logs page status=%d body=%s", w.Code, w.Body.String())
	}
	r = httptest.NewRequest(http.MethodGet, "/assets/logs.js", nil)
	r.AddCookie(session)
	w = httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "textContent") || strings.Contains(w.Body.String(), "innerHTML") {
		t.Fatalf("logs script status=%d body=%s", w.Code, w.Body.String())
	}
	r = httptest.NewRequest(http.MethodGet, "/api/v1/logs?limit=10&file=../../etc/passwd", nil)
	r.AddCookie(session)
	w = httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "root:x:") {
		t.Fatalf("log path injection status=%d body=%s", w.Code, w.Body.String())
	}
	r = httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	r.AddCookie(session)
	w = httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, r)
	if w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Set-Cookie"), "Max-Age=0") {
		t.Fatalf("logout status=%d cookie=%s", w.Code, w.Header().Get("Set-Cookie"))
	}
	restartedAuth, err = authn.New(cfg.AuthUsername, cfg.AuthPasswordHash, cfg.BasePath, st)
	if err != nil || restartedAuth.Authenticated(session.Value) {
		t.Fatalf("logged-out session survived authentication manager restart: %v", err)
	}
	r = httptest.NewRequest(http.MethodGet, "/api/v1/logs", nil)
	r.AddCookie(session)
	w = httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("logged-out session status=%d", w.Code)
	}
}

func TestUserThemeAPIIsUserScoped(t *testing.T) {
	const adminPassword = "admin-theme-password"
	adminHash, err := authn.HashPassword(adminPassword)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := config.Config{BasePath: "/liquidation/", AuthUsername: "pcdog", AuthPasswordHash: adminHash}
	srv, st := testService(t, cfg, logger, nil)
	defer st.Close()

	login := func(username, password string) *http.Cookie {
		t.Helper()
		form := url.Values{"username": {username}, "password": {password}}
		r := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		srv.http.Handler.ServeHTTP(w, r)
		if w.Code != http.StatusSeeOther {
			t.Fatalf("login status=%d body=%s", w.Code, w.Body.String())
		}
		for _, cookie := range w.Result().Cookies() {
			if cookie.Name == sessionCookie {
				return cookie
			}
		}
		t.Fatal("theme test login did not return a session cookie")
		return nil
	}

	adminSession := login("pcdog", adminPassword)
	request := func(method, body string, session *http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "/api/v1/user/theme", strings.NewReader(body))
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		if session != nil {
			r.AddCookie(session)
		}
		w := httptest.NewRecorder()
		srv.http.Handler.ServeHTTP(w, r)
		return w
	}

	if w := request(http.MethodGet, "", adminSession); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"theme":"dark"`) {
		t.Fatalf("default theme status=%d body=%s", w.Code, w.Body.String())
	}
	if w := request(http.MethodPut, `{"theme":"light"}`, adminSession); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"theme":"light"`) {
		t.Fatalf("save theme status=%d body=%s", w.Code, w.Body.String())
	}
	if theme, found, loadErr := st.LoadUserTheme(context.Background(), "pcdog"); loadErr != nil || !found || theme != "light" {
		t.Fatalf("stored theme=%q found=%v err=%v", theme, found, loadErr)
	}
	if w := request(http.MethodGet, "", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated theme status=%d body=%s", w.Code, w.Body.String())
	}
	if w := request(http.MethodPut, `{"theme":"blue"}`, adminSession); w.Code != http.StatusBadRequest {
		t.Fatalf("invalid theme status=%d body=%s", w.Code, w.Body.String())
	}
	if w := request(http.MethodPut, `{"theme":"dark","username":"other"}`, adminSession); w.Code != http.StatusBadRequest {
		t.Fatalf("client user field status=%d body=%s", w.Code, w.Body.String())
	}

	const otherPassword = "other-theme-password"
	otherHash, err := authn.HashPassword(otherPassword)
	if err != nil {
		t.Fatal(err)
	}
	if err = st.CreateAuthUser(context.Background(), "other", otherHash, "user"); err != nil {
		t.Fatal(err)
	}
	if err = srv.auth.ReloadUsers(context.Background()); err != nil {
		t.Fatal(err)
	}
	otherSession := login("other", otherPassword)
	if w := request(http.MethodGet, "", otherSession); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"theme":"dark"`) {
		t.Fatalf("other user theme status=%d body=%s", w.Code, w.Body.String())
	}
	if w := request(http.MethodGet, "", adminSession); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"theme":"light"`) {
		t.Fatalf("admin theme after other login status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestUserThemeRequiresAuthentication(t *testing.T) {
	srv, st := testService(t, config.Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	defer st.Close()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/user/theme", nil)
	response := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated/no-auth theme status=%d body=%s", response.Code, response.Body.String())
	}
}
