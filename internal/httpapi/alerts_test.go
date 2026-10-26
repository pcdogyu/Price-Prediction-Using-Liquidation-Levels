package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/app"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/config"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/observability"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/store"
)

func alertTestServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	_, logs, err := observability.New(t.TempDir()+"/application.log", 7)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logs.Close() })
	srv, st := testService(t, config.Config{}, logger, logs)
	t.Cleanup(func() { _ = st.Close() })
	return srv, st
}

func serveAlert(srv *Server, method, path string, body io.Reader) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, body)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	srv.http.Handler.ServeHTTP(w, req)
	return w
}

// TestAlertPageAndAssets checks the page renders through the shared navigation and that
// its own assets are served with the right content types.
func TestAlertPageAndAssets(t *testing.T) {
	srv, _ := alertTestServer(t)
	w := serveAlert(srv, http.MethodGet, "/alerts", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("alerts page status=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{"报警", "成交额前 100 币对", `data-page="alerts"`, "assets/alerts.js", "assets/alerts.css", "aria-current=\"page\""} {
		if !strings.Contains(body, want) {
			t.Fatalf("the alerts page must contain %q", want)
		}
	}
	// Alerts and signals live in one 20-row paged table, and the live monitor table
	// stays the last section.
	for _, want := range []string{"预警与信号历史", "最近 30 天", "报警回测（过去 2 周）", "alerts-backtest-run", "alerts-backtest-rows", `data-sort-key="signal"`, `data-sort-key="filter"`, `data-sort-key="trades"`, `data-sort-key="additions"`, `data-sort-key="win_rate"`, `data-sort-key="profit_factor"`, `data-sort-key="max_drawdown_pct"`, `data-sort-key="final_equity"`, "alerts-backtest-sort", "报警参数", "策略开关", "信号重算", "alerts-signal-progress", "alerts-signal-progress-fill", "分别控制平仓和开仓", "每页 20 条", "全部或输入 DOGE", "alerts-prev", "alerts-next", "<th>下单</th>", "<th>报警</th>", "<th>周期</th>", "平仓区域预警信号过滤", "激进信号过滤", "稳健信号过滤", "VWAP过滤", "Donchian过滤", "服务端过滤", "alerts-strategy-settings", "alerts-signal-recalculate", "alerts-order-signal-settings", `id="alerts-order-alert-close" type="checkbox"`, `id="alerts-order-alert" type="checkbox"`, `id="alerts-order-aggressive-close" type="checkbox"`, `id="alerts-order-aggressive" type="checkbox"`, `id="alerts-order-steady-close" type="checkbox"`, `id="alerts-order-steady" type="checkbox"`, `id="alerts-threshold-btc" type="number" min="1000"`, `id="alerts-vwap-range-hours"`, `id="alerts-vwap-min-drawdown"`, `id="alerts-vwap-donchian"`, `id="alerts-vwap-filter" type="checkbox"`, `id="alerts-donchian-filter" type="checkbox"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("the merged history must contain %q", want)
		}
	}
	for _, gone := range []string{"最新信号", "alerts-signal-rows", "alerts-signal-status", "alerts-alert-rows", "alerts-limit"} {
		if strings.Contains(body, gone) {
			t.Fatalf("the alerts page must not contain %q", gone)
		}
	}
	history, monitor := strings.Index(body, "信号历史"), strings.Index(body, "实时监控")
	if history < 0 || monitor < 0 || monitor < history {
		t.Fatalf("实时监控 must stay below 信号历史: history %d monitor %d", history, monitor)
	}
	strategy, switches := strings.Index(body, "报警参数"), strings.Index(body, "策略开关")
	if strategy < 0 || switches < strategy {
		t.Fatalf("策略开关 must be placed inside 报警参数 section: strategy %d switches %d", strategy, switches)
	}
	// The unified navigation makes 报警 the primary entry before the options page.
	options, alerts := strings.Index(body, `href="/options"`), strings.Index(body, `href="/alerts"`)
	if options < 0 || alerts < 0 || alerts > options {
		t.Fatalf("报警 must precede 期权 in the navigation: options %d alerts %d", options, alerts)
	}
	for path, kind := range map[string]string{"/assets/alerts.js": "text/javascript", "/assets/alerts.css": "text/css"} {
		asset := serveAlert(srv, http.MethodGet, path, nil)
		if asset.Code != http.StatusOK || !strings.HasPrefix(asset.Header().Get("Content-Type"), kind) {
			t.Fatalf("%s status=%d type=%q", path, asset.Code, asset.Header().Get("Content-Type"))
		}
		if asset.Body.Len() == 0 {
			t.Fatalf("%s must not be empty", path)
		}
	}
	script := serveAlert(srv, http.MethodGet, "/assets/alerts.js", nil).Body.String()
	if !strings.Contains(script, "alerts-trigger") || !strings.Contains(script, "api/v1/alerts/trigger") {
		t.Fatal("the alerts script must expose the manual order trigger")
	}
	css := serveAlert(srv, http.MethodGet, "/assets/alerts.css", nil).Body.String()
	for _, want := range []string{"alerts-toggle-input", "alerts-toggle-track", ".alerts-toggle-input:checked + .alerts-toggle-track", ".alerts-toggle-track::after"} {
		if !strings.Contains(css, want) {
			t.Fatalf("the server-side filter controls must use sliding toggles: missing %q", want)
		}
	}
	if !strings.Contains(script, "alerts-close-only-prefix") || !strings.Contains(script, "closeOnlyIgnored") {
		t.Fatal("the alerts script must mark close-only history records")
	}
	if !strings.Contains(script, "filter_tags") || !strings.Contains(script, "VWAP过滤") || !strings.Contains(script, "Donchian过滤") || !strings.Contains(script, "legacyFilterTags") {
		t.Fatal("the alerts script must render VWAP/Donchian history tags")
	}
	if !strings.Contains(script, "api/v1/alerts/notify") || !strings.Contains(script, "触发报价") {
		t.Fatal("the alerts script must expose the manual Telegram alert trigger")
	}
	if strings.Contains(script, "void runBacktest();") {
		t.Fatal("the alerts page must not start a backtest during initialization")
	}
	if !strings.Contains(script, "下单中") || !strings.Contains(script, "button.textContent = original") || strings.Contains(script, "window.confirm") {
		t.Fatal("manual order trigger must show in-button progress without a confirmation popup")
	}
	if !strings.Contains(script, "api/v1/alerts/order-settings") || !strings.Contains(script, "保存下单信号设置") {
		t.Fatal("the alerts script must expose order signal category settings")
	}
	if !strings.Contains(script, "api/v1/alerts/recalculate") || !strings.Contains(script, "api/v1/alerts/backtest") || !strings.Contains(script, "runBacktest") || !strings.Contains(script, "params.set('replay', '1')") || !strings.Contains(script, "historyReplayed") || !strings.Contains(script, "过去 7 天") || !strings.Contains(script, "signal-recalculate") || !strings.Contains(script, "startSignalProgress") || !strings.Contains(script, "finishSignalProgress") || !strings.Contains(script, "Math.min(99") || !strings.Contains(script, "setSignalProgress(100") {
		t.Fatal("the alerts script must expose the 7-day signal recalculation")
	}
	if !strings.Contains(script, "sortBacktest") || !strings.Contains(script, "alerts-backtest-sort") || !strings.Contains(script, "backtestSortDirection") {
		t.Fatal("the alerts script must support sorting the backtest columns")
	}
	if !strings.Contains(script, "async function readJSON(response)") || !strings.Contains(script, "HTML 错误页") || strings.Contains(script, "response.json()") {
		t.Fatal("the alerts script must handle non-JSON proxy responses without exposing a JSON parser error")
	}
	if strings.Contains(body, `id="alerts-order-alerts"`) || strings.Contains(script, "alerts-order-alerts") {
		t.Fatal("the legacy alert opening toggle must not be presented")
	}
	if post := serveAlert(srv, http.MethodPost, "/alerts", nil); post.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /alerts status=%d", post.Code)
	}
}

func TestManualAlertNotifyEndpoint(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	_, logs, err := observability.New(t.TempDir()+"/application.log", 7)
	if err != nil {
		t.Fatal(err)
	}
	defer logs.Close()
	requests := make(chan url.Values, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		values, parseErr := url.ParseQuery(string(body))
		if parseErr == nil {
			requests <- values
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":1}}`)
	}))
	defer upstream.Close()
	srv, st := testService(t, config.Config{TelegramAPIBase: upstream.URL}, logger, logs)
	defer st.Close()
	ctx := context.Background()
	if saved := serveAlert(srv, http.MethodPost, "/api/v1/alerts/settings", strings.NewReader(`{"telegram_enabled":true,"telegram_token":"12345:secretvalue","telegram_chat_id":"-1009","notify_alerts":true}`)); saved.Code != http.StatusOK {
		t.Fatalf("Telegram settings status=%d body=%s", saved.Code, saved.Body.String())
	}
	at := time.Now().UTC().Truncate(time.Second)
	if _, err := st.SaveLiquidationAlert(ctx, domain.LiquidationAlert{ID: "manual-alert", Symbol: "BTCUSDT", Side: "long", TradeSide: "long", WindowMinutes: 3, NotionalUSD: 400000, ThresholdUSD: 300000, EventCount: 2, TriggeredAt: at, QuietSeconds: 600, QuietUntil: at.Add(10 * time.Minute), Status: domain.AlertStateWatching}); err != nil {
		t.Fatal(err)
	}
	// The manual notification also renders a current 1m chart. Keep this test
	// independent of Binance availability by seeding the same local fallback
	// window that production uses when the public kline endpoint is unavailable.
	minute := time.Now().UTC().Truncate(time.Minute)
	candles := make([]domain.Candle, 0, 721)
	for index := 720; index >= 0; index-- {
		price := 67000 + float64(720-index)
		candles = append(candles, domain.Candle{
			Exchange: "binance", Symbol: "BTCUSDT", Time: minute.Add(-time.Duration(index) * time.Minute),
			Open: price, High: price + 5, Low: price - 5, Close: price + 2, VolumeUSD: 1000,
		})
	}
	if err := st.UpsertCandles(ctx, candles); err != nil {
		t.Fatal(err)
	}
	w := serveAlert(srv, http.MethodPost, "/api/v1/alerts/notify", strings.NewReader(`{"record_type":"alert","record_id":"manual-alert"}`))
	if w.Code != http.StatusAccepted || !strings.Contains(w.Body.String(), `"status":"accepted"`) {
		t.Fatalf("notify status=%d body=%s", w.Code, w.Body.String())
	}
	select {
	case values := <-requests:
		if values.Get("chat_id") != "-1009" || !strings.Contains(values.Get("text"), "手动报警") || !strings.Contains(values.Get("text"), "BTCUSDT") {
			t.Fatalf("unexpected Telegram form: %v", values)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("notify endpoint did not call Telegram")
	}
	missing := serveAlert(srv, http.MethodPost, "/api/v1/alerts/notify", strings.NewReader(`{"record_type":"alert","record_id":"missing"}`))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing notify record status=%d body=%s", missing.Code, missing.Body.String())
	}
}

func TestManualAlertTriggerEndpoint(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	_, logs, err := observability.New(t.TempDir()+"/application.log", 7)
	if err != nil {
		t.Fatal(err)
	}
	defer logs.Close()
	requests := make(chan []byte, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests <- body
		w.WriteHeader(http.StatusAccepted)
	}))
	defer upstream.Close()
	srv, st := testService(t, config.Config{
		SteadyWebhookURL: upstream.URL, SteadyWebhookAPIID: "okx_bot", SteadyWebhookTargetExchange: "okx",
		SteadyWebhookTradeEnv: "demo", SteadyWebhookExchange: "binance", SteadyWebhookInterval: "3m",
		SteadyWebhookTokenNonce: "nonce-value", SteadyWebhookToken: "token-value",
	}, logger, logs)
	defer st.Close()
	at := time.Now().UTC().Truncate(time.Second)
	if _, err := st.SaveTradeSignal(context.Background(), domain.TradeSignal{ID: "manual-signal", Symbol: "BTCUSDT", Kind: domain.SignalSteady, Side: "short", LiquidationSide: "short", CreatedAt: at, ReferencePrice: 67123.45, WindowMinutes: 3}); err != nil {
		t.Fatal(err)
	}
	w := serveAlert(srv, http.MethodPost, "/api/v1/alerts/trigger", strings.NewReader(`{"record_type":"steady","record_id":"manual-signal"}`))
	if w.Code != http.StatusAccepted || !strings.Contains(w.Body.String(), `"status":"accepted"`) || !strings.Contains(w.Body.String(), `"target_exchange":"okx"`) || !strings.Contains(w.Body.String(), `"trade_env":"demo"`) {
		t.Fatalf("trigger status=%d body=%s", w.Code, w.Body.String())
	}
	select {
	case raw := <-requests:
		var got map[string]string
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if got["action"] != "sell" || got["price"] != "67123.45" {
			t.Fatalf("unexpected trigger payload: %s", raw)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("trigger endpoint did not call webhook")
	}
	missing := serveAlert(srv, http.MethodPost, "/api/v1/alerts/trigger", strings.NewReader(`{"record_type":"steady","record_id":"missing"}`))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing record status=%d body=%s", missing.Code, missing.Body.String())
	}
}

// TestAlertOverviewShape documents the payload the alert page polls every few seconds.
func TestAlertOverviewShape(t *testing.T) {
	srv, st := alertTestServer(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Second)
	if _, err := st.SaveLiquidationAlert(ctx, domain.LiquidationAlert{ID: "a1", Symbol: "BTCUSDT", Side: "long", TradeSide: "long", WindowMinutes: 3, NotionalUSD: 400000, ThresholdUSD: 300000, TriggeredAt: base, QuietSeconds: 600, QuietUntil: base.Add(10 * time.Minute), Status: domain.AlertStateWatching, UpdatedAt: base}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveTradeSignal(ctx, domain.TradeSignal{ID: "s1", AlertID: "a1", Symbol: "BTCUSDT", Kind: domain.SignalAggressive, Side: "long", LiquidationSide: "long", CreatedAt: base.Add(10 * time.Minute), AlertNotionalUSD: 400000, ThresholdUSD: 300000, WindowMinutes: 3}); err != nil {
		t.Fatal(err)
	}
	w := serveAlert(srv, http.MethodGet, "/api/v1/alerts", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("overview status=%d body=%s", w.Code, w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("the overview must not be cached: %q", w.Header().Get("Cache-Control"))
	}
	var packet struct {
		Data     domain.AlertsView           `json:"data"`
		Settings domain.NotificationSettings `json:"settings"`
		Sources  map[string]any              `json:"sources"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &packet); err != nil {
		t.Fatal(err)
	}
	view := packet.Data
	if view.Thresholds.WindowMinutes != 3 || view.Thresholds.QuietMinutes != 10 || view.Thresholds.ConfirmMinutes != 30 {
		t.Fatalf("the strategy periods must be published: %+v", view.Thresholds)
	}
	if view.Thresholds.BTCUSD != 300000 || view.Thresholds.ETHUSD != 200000 || view.Thresholds.OtherUSD != 100000 {
		t.Fatalf("the alert thresholds must be published: %+v", view.Thresholds)
	}
	if view.State != "waiting_watch_list" || len(view.Monitor) != 0 {
		t.Fatalf("a fresh engine has no watch list yet: %+v", view)
	}
	if view.AlertTotal != 1 || view.SignalTotal != 1 || view.AggressiveTotal != 1 || view.SteadyTotal != 0 {
		t.Fatalf("the counters must come from the store: %+v", view)
	}
	if len(view.Alerts) != 1 || len(view.Signals) != 1 || view.Method == "" {
		t.Fatalf("the newest rows and the method text must be included: %+v", view)
	}
	if view.AvailableFrom == nil || !view.AvailableFrom.Equal(base) {
		t.Fatalf("the retention start must be reported: %+v", view.AvailableFrom)
	}
	if packet.Settings.HasToken || packet.Settings.TokenMask != "" || packet.Settings.TokenSource != "unset" {
		t.Fatalf("an unconfigured server must report no token: %+v", packet.Settings)
	}
	if packet.Sources == nil {
		t.Fatal("exchange health must be reported like on the other pages")
	}
}

func TestAlertStrategySettingsEndpoint(t *testing.T) {
	srv, st := alertTestServer(t)
	defer st.Close()

	initial := serveAlert(srv, http.MethodGet, "/api/v1/alerts/strategy-settings", nil)
	if initial.Code != http.StatusOK || !strings.Contains(initial.Body.String(), `"window_minutes":3`) || !strings.Contains(initial.Body.String(), `"eth_usd":200000`) || !strings.Contains(initial.Body.String(), `"other_usd":100000`) || !strings.Contains(initial.Body.String(), `"vwap_filter_enabled":true`) || !strings.Contains(initial.Body.String(), `"donchian_filter_enabled":true`) {
		t.Fatalf("default strategy settings status=%d body=%s", initial.Code, initial.Body.String())
	}
	saved := serveAlert(srv, http.MethodPost, "/api/v1/alerts/strategy-settings", strings.NewReader(`{"window_minutes":5,"btc_usd":300000,"eth_usd":200000,"other_usd":100000,"vwap_range_hours":18,"vwap_min_drawdown_pct":2.5,"vwap_donchian_pct":30,"vwap_filter_enabled":false,"donchian_filter_enabled":true}`))
	if saved.Code != http.StatusOK || !strings.Contains(saved.Body.String(), `"window_minutes":5`) || !strings.Contains(saved.Body.String(), `"vwap_range_hours":18`) || !strings.Contains(saved.Body.String(), `"vwap_min_drawdown_pct":2.5`) || !strings.Contains(saved.Body.String(), `"vwap_donchian_pct":30`) || !strings.Contains(saved.Body.String(), `"vwap_filter_enabled":false`) || !strings.Contains(saved.Body.String(), `"donchian_filter_enabled":true`) {
		t.Fatalf("saved strategy settings status=%d body=%s", saved.Code, saved.Body.String())
	}
	legacy := serveAlert(srv, http.MethodPost, "/api/v1/alerts/strategy-settings", strings.NewReader(`{"window_minutes":5,"btc_usd":300000,"eth_usd":200000,"other_usd":100000,"vwap_range_hours":18,"vwap_min_drawdown_pct":2.5,"vwap_donchian_pct":30}`))
	if legacy.Code != http.StatusOK || !strings.Contains(legacy.Body.String(), `"vwap_filter_enabled":false`) || !strings.Contains(legacy.Body.String(), `"donchian_filter_enabled":true`) {
		t.Fatalf("legacy strategy settings submission must preserve switches: status=%d body=%s", legacy.Code, legacy.Body.String())
	}
	view, err := srv.svc.Alerts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if view.Thresholds.WindowMinutes != 5 || view.Thresholds.ETHUSD != 200000 || view.Thresholds.OtherUSD != 100000 || view.Thresholds.VWAPRangeHours != 18 || view.Thresholds.VWAPMinDrawdownPct != 2.5 || view.Thresholds.VWAPDonchianPct != 30 || view.Thresholds.VWAPFilterEnabled || !view.Thresholds.DonchianFilterEnabled {
		t.Fatalf("live strategy settings = %+v", view.Thresholds)
	}
	chart := serveAlert(srv, http.MethodGet, "/api/v1/alerts/chart-signals?symbol=BTCUSDT", nil)
	if chart.Code != http.StatusOK || !strings.Contains(chart.Body.String(), `"btc_usd":300000`) || !strings.Contains(chart.Body.String(), `"window_minutes":5`) {
		t.Fatalf("chart signals must expose the saved strategy settings: %d %s", chart.Code, chart.Body.String())
	}
	chart168 := serveAlert(srv, http.MethodGet, "/api/v1/alerts/chart-signals?symbol=BTCUSDT&window_hours=168", nil)
	if chart168.Code != http.StatusOK || !strings.Contains(chart168.Body.String(), `"replay_hours":168`) {
		t.Fatalf("chart signals must expose the requested 7d replay window: %d %s", chart168.Code, chart168.Body.String())
	}
	badChart := serveAlert(srv, http.MethodGet, "/api/v1/alerts/chart-signals?symbol=bad-symbol", nil)
	if badChart.Code != http.StatusBadRequest {
		t.Fatalf("invalid chart symbol status=%d body=%s", badChart.Code, badChart.Body.String())
	}
	invalid := serveAlert(srv, http.MethodPost, "/api/v1/alerts/strategy-settings", strings.NewReader(`{"window_minutes":0,"btc_usd":300000,"eth_usd":200000,"other_usd":100000}`))
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid strategy settings status=%d body=%s", invalid.Code, invalid.Body.String())
	}
	if err := st.InsertLiquidation(context.Background(), domain.LiquidationEvent{
		ID: "recalculate-history", Exchange: "binance", Symbol: "BTCUSDT", PositionSide: "long",
		EventTime: time.Now().UTC().Add(-time.Minute), ReceivedAt: time.Now().UTC(), Price: 100,
		Quantity: 4000, NotionalUSD: 400000, Coverage: "sampled",
	}); err != nil {
		t.Fatal(err)
	}
	recalculated := serveAlert(srv, http.MethodPost, "/api/v1/alerts/recalculate", nil)
	if recalculated.Code != http.StatusOK || !strings.Contains(recalculated.Body.String(), `"window_hours":168`) {
		t.Fatalf("7d signal recalculation status=%d body=%s", recalculated.Code, recalculated.Body.String())
	}
	replayed := serveAlert(srv, http.MethodGet, "/api/v1/alerts/timeline?symbol=BTCUSDT&replay=1", nil)
	var replayedPage domain.AlertTimelinePage
	if replayed.Code != http.StatusOK || json.Unmarshal(replayed.Body.Bytes(), &replayedPage) != nil || !replayedPage.Replayed || replayedPage.AlertCount == 0 || replayedPage.TotalCount == 0 {
		t.Fatalf("recalculation must refresh the history endpoint: %d %s page=%+v", replayed.Code, replayed.Body.String(), replayedPage)
	}
	badWindow := serveAlert(srv, http.MethodGet, "/api/v1/alerts/chart-signals?symbol=BTCUSDT&window_hours=0", nil)
	if badWindow.Code != http.StatusBadRequest {
		t.Fatalf("invalid chart replay window status=%d body=%s", badWindow.Code, badWindow.Body.String())
	}
}

func TestAlertBacktestEndpointReturnsAttachmentShape(t *testing.T) {
	srv, st := alertTestServer(t)
	defer st.Close()
	w := serveAlert(srv, http.MethodPost, "/api/v1/alerts/backtest", nil)
	if w.Code != http.StatusOK && w.Code != http.StatusAccepted {
		t.Fatalf("backtest status=%d body=%s", w.Code, w.Body.String())
	}
	var status app.AlertBacktestStatus
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; status.Status == "running" && attempt < 50; attempt++ {
		time.Sleep(10 * time.Millisecond)
		poll := serveAlert(srv, http.MethodGet, "/api/v1/alerts/backtest?job_id="+url.QueryEscape(status.JobID), nil)
		if poll.Code != http.StatusOK && poll.Code != http.StatusAccepted {
			t.Fatalf("backtest poll status=%d body=%s", poll.Code, poll.Body.String())
		}
		if err := json.Unmarshal(poll.Body.Bytes(), &status); err != nil {
			t.Fatal(err)
		}
	}
	if status.Status != "completed" || status.Report == nil {
		t.Fatalf("backtest job did not complete: %+v", status)
	}
	report := *status.Report
	if report.WindowDays != 14 || report.HoldingMinutes != 15 || len(report.Rows) != 12 {
		t.Fatalf("unexpected backtest report: %+v", report)
	}
	if got := serveAlert(srv, http.MethodGet, "/api/v1/alerts/backtest", nil).Code; got != http.StatusNotFound {
		t.Fatalf("GET backtest status=%d", got)
	}
}

// TestAlertHistoryEndpoints covers filtering and pagination for both stored feeds, plus
// the query validation the page relies on.
func TestAlertHistoryEndpoints(t *testing.T) {
	srv, st := alertTestServer(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Second)
	for i := 0; i < 4; i++ {
		at := base.Add(time.Duration(i) * time.Minute)
		id := string(rune('0' + i))
		if _, err := st.SaveLiquidationAlert(ctx, domain.LiquidationAlert{ID: "a" + id, Symbol: "BTCUSDT", Side: "long", TradeSide: "long", WindowMinutes: 3, NotionalUSD: 400000, ThresholdUSD: 300000, TriggeredAt: at, QuietSeconds: 600, QuietUntil: at.Add(10 * time.Minute), Status: domain.AlertStateWatching, UpdatedAt: at}); err != nil {
			t.Fatal(err)
		}
		kind := domain.SignalAggressive
		if i == 3 {
			kind = domain.SignalSteady
		}
		if _, err := st.SaveTradeSignal(ctx, domain.TradeSignal{ID: "s" + id, AlertID: "a" + id, Symbol: "BTCUSDT", Kind: kind, Side: "long", LiquidationSide: "long", CreatedAt: at, AlertNotionalUSD: 400000, ThresholdUSD: 300000, WindowMinutes: 3}); err != nil {
			t.Fatal(err)
		}
	}
	w := serveAlert(srv, http.MethodGet, "/api/v1/alerts/history?limit=2", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("alert history status=%d body=%s", w.Code, w.Body.String())
	}
	var alerts domain.AlertPage
	if err := json.Unmarshal(w.Body.Bytes(), &alerts); err != nil {
		t.Fatal(err)
	}
	if len(alerts.Rows) != 2 || alerts.AlertCount != 4 || alerts.NextCursor == "" {
		t.Fatalf("unexpected alert page: %+v", alerts)
	}
	next := serveAlert(srv, http.MethodGet, "/api/v1/alerts/history?limit=2&cursor="+alerts.NextCursor, nil)
	if next.Code != http.StatusOK {
		t.Fatalf("second alert page status=%d body=%s", next.Code, next.Body.String())
	}
	var second domain.AlertPage
	if err := json.Unmarshal(next.Body.Bytes(), &second); err != nil {
		t.Fatal(err)
	}
	if len(second.Rows) != 2 || second.Rows[0].ID != "a1" || second.NextCursor != "" {
		t.Fatalf("the cursor must continue where the first page stopped: %+v", second)
	}
	steady := serveAlert(srv, http.MethodGet, "/api/v1/alerts/signals?kind=steady", nil)
	if steady.Code != http.StatusOK || !strings.Contains(steady.Body.String(), `"steady_count":1`) || !strings.Contains(steady.Body.String(), `"signal_count":1`) {
		t.Fatalf("the signal kind filter failed: %d %s", steady.Code, steady.Body.String())
	}
	unknown := serveAlert(srv, http.MethodGet, "/api/v1/alerts/signals?symbol=ETHUSDT", nil)
	var empty domain.SignalPage
	if unknown.Code != http.StatusOK || json.Unmarshal(unknown.Body.Bytes(), &empty) != nil || len(empty.Rows) != 0 || empty.SignalCount != 0 {
		t.Fatalf("an unknown symbol must return an empty page: %d %s", unknown.Code, unknown.Body.String())
	}
	oldAt := base.Add(-31 * 24 * time.Hour)
	if _, err := st.SaveLiquidationAlert(ctx, domain.LiquidationAlert{ID: "old-alert", Symbol: "BTCUSDT", Side: "long", TradeSide: "long", WindowMinutes: 3, NotionalUSD: 400000, ThresholdUSD: 300000, TriggeredAt: oldAt, QuietSeconds: 600, QuietUntil: oldAt.Add(10 * time.Minute), Status: domain.AlertStateWatching, UpdatedAt: oldAt}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveTradeSignal(ctx, domain.TradeSignal{ID: "old-signal", AlertID: "old-alert", Symbol: "BTCUSDT", Kind: domain.SignalAggressive, Side: "long", LiquidationSide: "long", CreatedAt: oldAt, AlertNotionalUSD: 400000, ThresholdUSD: 300000, WindowMinutes: 3}); err != nil {
		t.Fatal(err)
	}
	timeline := serveAlert(srv, http.MethodGet, "/api/v1/alerts/timeline", nil)
	var merged domain.AlertTimelinePage
	if timeline.Code != http.StatusOK || json.Unmarshal(timeline.Body.Bytes(), &merged) != nil {
		t.Fatalf("merged timeline status=%d body=%s", timeline.Code, timeline.Body.String())
	}
	if len(merged.Rows) != 8 || merged.TotalCount != 8 || merged.AlertCount != 4 || merged.AggressiveCount != 3 || merged.SteadyCount != 1 {
		t.Fatalf("unexpected merged timeline: %+v", merged)
	}
	for i := 1; i < len(merged.Rows); i++ {
		if merged.Rows[i-1].EventAt.Before(merged.Rows[i].EventAt) {
			t.Fatalf("merged timeline must be newest first: %+v", merged.Rows)
		}
	}
	steadyTimeline := serveAlert(srv, http.MethodGet, "/api/v1/alerts/timeline?kind=steady", nil)
	if steadyTimeline.Code != http.StatusOK || !strings.Contains(steadyTimeline.Body.String(), `"total_count":1`) || !strings.Contains(steadyTimeline.Body.String(), `"type":"steady"`) {
		t.Fatalf("the merged kind filter failed: %d %s", steadyTimeline.Code, steadyTimeline.Body.String())
	}
	dogeAt := base.Add(10 * time.Minute)
	if _, err := st.SaveLiquidationAlert(ctx, domain.LiquidationAlert{ID: "adoge", Symbol: "DOGEUSDT", Side: "long", TradeSide: "long", WindowMinutes: 3, NotionalUSD: 400000, ThresholdUSD: 300000, TriggeredAt: dogeAt, QuietSeconds: 600, QuietUntil: dogeAt.Add(10 * time.Minute), Status: domain.AlertStateWatching, UpdatedAt: dogeAt}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveTradeSignal(ctx, domain.TradeSignal{ID: "sdoge", AlertID: "adoge", Symbol: "DOGEUSDT", Kind: domain.SignalAggressive, Side: "long", LiquidationSide: "long", CreatedAt: dogeAt, AlertNotionalUSD: 400000, ThresholdUSD: 300000, WindowMinutes: 3}); err != nil {
		t.Fatal(err)
	}
	fuzzy := serveAlert(srv, http.MethodGet, "/api/v1/alerts/timeline?symbol=doge", nil)
	var fuzzyPage domain.AlertTimelinePage
	if fuzzy.Code != http.StatusOK || json.Unmarshal(fuzzy.Body.Bytes(), &fuzzyPage) != nil || len(fuzzyPage.Rows) != 2 || fuzzyPage.TotalCount != 2 || fuzzyPage.AlertCount != 1 || fuzzyPage.AggressiveCount != 1 {
		t.Fatalf("case-insensitive substring symbol filter failed: %d %s page=%+v", fuzzy.Code, fuzzy.Body.String(), fuzzyPage)
	}
	for _, row := range fuzzyPage.Rows {
		if (row.Alert != nil && row.Alert.Symbol != "DOGEUSDT") || (row.Signal != nil && row.Signal.Symbol != "DOGEUSDT") {
			t.Fatalf("fuzzy symbol filter returned the wrong pair: %+v", row)
		}
	}
	fuzzyAlerts := serveAlert(srv, http.MethodGet, "/api/v1/alerts/history?symbol=doge", nil)
	var fuzzyAlertPage domain.AlertPage
	if fuzzyAlerts.Code != http.StatusOK || json.Unmarshal(fuzzyAlerts.Body.Bytes(), &fuzzyAlertPage) != nil || len(fuzzyAlertPage.Rows) != 1 || fuzzyAlertPage.AlertCount != 1 || fuzzyAlertPage.Rows[0].Symbol != "DOGEUSDT" {
		t.Fatalf("alert substring symbol filter failed: %d %s page=%+v", fuzzyAlerts.Code, fuzzyAlerts.Body.String(), fuzzyAlertPage)
	}
	fuzzySignals := serveAlert(srv, http.MethodGet, "/api/v1/alerts/signals?symbol=doge", nil)
	var fuzzySignalPage domain.SignalPage
	if fuzzySignals.Code != http.StatusOK || json.Unmarshal(fuzzySignals.Body.Bytes(), &fuzzySignalPage) != nil || len(fuzzySignalPage.Rows) != 1 || fuzzySignalPage.SignalCount != 1 || fuzzySignalPage.Rows[0].Symbol != "DOGEUSDT" {
		t.Fatalf("signal substring symbol filter failed: %d %s page=%+v", fuzzySignals.Code, fuzzySignals.Body.String(), fuzzySignalPage)
	}
	for _, path := range []string{
		"/api/v1/alerts/history?limit=0",
		"/api/v1/alerts/history?limit=999",
		"/api/v1/alerts/history?side=both",
		"/api/v1/alerts/history?cursor=not-a-cursor",
		"/api/v1/alerts/signals?kind=trend",
		"/api/v1/alerts/signals?limit=-1",
		"/api/v1/alerts/signals?symbol=" + strings.Repeat("T", 33),
		"/api/v1/alerts/timeline?kind=trend",
		"/api/v1/alerts/timeline?cursor=not-a-cursor",
	} {
		bad := serveAlert(srv, http.MethodGet, path, nil)
		if bad.Code != http.StatusBadRequest {
			t.Fatalf("%s must be rejected, got %d %s", path, bad.Code, bad.Body.String())
		}
	}
	if put := serveAlert(srv, http.MethodPut, "/api/v1/alerts", nil); put.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT /api/v1/alerts status=%d", put.Code)
	}
}

// TestAlertSettingsEndpoints covers the token secrecy rules: the form may set a token,
// but no response may return it, and an empty submission keeps the stored value.
func TestAlertSettingsEndpoints(t *testing.T) {
	srv, _ := alertTestServer(t)
	invalid := serveAlert(srv, http.MethodPost, "/api/v1/alerts/settings", strings.NewReader(`{"telegram_enabled":false,"telegram_token":"not-a-token","telegram_chat_id":"1"}`))
	if invalid.Code != http.StatusBadRequest || !strings.Contains(invalid.Body.String(), "Token") {
		t.Fatalf("a malformed token must be rejected: %d %s", invalid.Code, invalid.Body.String())
	}
	enableWithoutToken := serveAlert(srv, http.MethodPost, "/api/v1/alerts/settings", strings.NewReader(`{"telegram_enabled":true,"telegram_chat_id":"1"}`))
	if enableWithoutToken.Code != http.StatusBadRequest {
		t.Fatalf("enabling without credentials must fail: %d %s", enableWithoutToken.Code, enableWithoutToken.Body.String())
	}
	badChat := serveAlert(srv, http.MethodPost, "/api/v1/alerts/settings", strings.NewReader(`{"telegram_enabled":false,"telegram_chat_id":"not a chat"}`))
	if badChat.Code != http.StatusBadRequest {
		t.Fatalf("a malformed chat id must be rejected: %d %s", badChat.Code, badChat.Body.String())
	}
	if malformed := serveAlert(srv, http.MethodPost, "/api/v1/alerts/settings", strings.NewReader(`{"telegram_enabled":`)); malformed.Code != http.StatusBadRequest {
		t.Fatalf("malformed JSON must be rejected: %d", malformed.Code)
	}
	saved := serveAlert(srv, http.MethodPost, "/api/v1/alerts/settings", strings.NewReader(`{"telegram_enabled":true,"telegram_token":"12345:secretvalue","telegram_chat_id":"-1009","notify_alerts":false,"notify_aggressive":true,"notify_steady":false,"order_alerts":true,"order_aggressive":false,"order_steady":true}`))
	if saved.Code != http.StatusOK {
		t.Fatalf("a valid settings form must save: %d %s", saved.Code, saved.Body.String())
	}
	if strings.Contains(saved.Body.String(), "secretvalue") {
		t.Fatalf("the bot token must never be returned: %s", saved.Body.String())
	}
	var settings domain.NotificationSettings
	if err := json.Unmarshal(saved.Body.Bytes(), &settings); err != nil {
		t.Fatal(err)
	}
	if !settings.TelegramEnabled || settings.TelegramChatID != "-1009" || settings.NotifyAlerts || !settings.NotifyAggressive || settings.NotifySteady || !settings.OrderAlerts || settings.OrderAggressive || !settings.OrderSteady {
		t.Fatalf("the settings did not round trip: %+v", settings)
	}
	if !settings.HasToken || settings.TokenMask != "1234****alue" || settings.TokenSource != "database" {
		t.Fatalf("the token must only be exposed masked: %+v", settings)
	}
	read := serveAlert(srv, http.MethodGet, "/api/v1/alerts/settings", nil)
	if read.Code != http.StatusOK || strings.Contains(read.Body.String(), "secretvalue") || !strings.Contains(read.Body.String(), "1234****alue") {
		t.Fatalf("reading settings must mask the token: %d %s", read.Code, read.Body.String())
	}
	// Leaving the token empty keeps the stored one instead of wiping it.
	kept := serveAlert(srv, http.MethodPost, "/api/v1/alerts/settings", strings.NewReader(`{"telegram_enabled":true,"telegram_chat_id":"-1009","telegram_token":""}`))
	if kept.Code != http.StatusOK || !strings.Contains(kept.Body.String(), `"token_mask":"1234****alue"`) {
		t.Fatalf("an empty token must keep the stored token: %d %s", kept.Code, kept.Body.String())
	}
	// Disabling notification is always allowed, even with credentials on file.
	off := serveAlert(srv, http.MethodPost, "/api/v1/alerts/settings", strings.NewReader(`{"telegram_enabled":false,"telegram_chat_id":"-1009"}`))
	if off.Code != http.StatusOK || strings.Contains(off.Body.String(), `"telegram_enabled":true`) {
		t.Fatalf("disabling must succeed: %d %s", off.Code, off.Body.String())
	}
	orderSaved := serveAlert(srv, http.MethodPost, "/api/v1/alerts/order-settings", strings.NewReader(`{"order_alerts":false,"order_aggressive":true,"order_steady":false,"order_alerts_close":false,"order_aggressive_close":true,"order_steady_close":false}`))
	if orderSaved.Code != http.StatusOK || !strings.Contains(orderSaved.Body.String(), `"order_aggressive":true`) {
		t.Fatalf("order signal settings must save: %d %s", orderSaved.Code, orderSaved.Body.String())
	}
	orderRead := serveAlert(srv, http.MethodGet, "/api/v1/alerts/order-settings", nil)
	if orderRead.Code != http.StatusOK || !strings.Contains(orderRead.Body.String(), `"order_alerts":false`) || !strings.Contains(orderRead.Body.String(), `"order_steady":false`) || !strings.Contains(orderRead.Body.String(), `"order_alerts_close":false`) || !strings.Contains(orderRead.Body.String(), `"order_aggressive_close":true`) {
		t.Fatalf("order signal settings must round trip: %d %s", orderRead.Code, orderRead.Body.String())
	}
	if put := serveAlert(srv, http.MethodPut, "/api/v1/alerts/settings", nil); put.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT settings status=%d", put.Code)
	}
}

func TestAlertOrderSettingsMethodValidation(t *testing.T) {
	srv, _ := alertTestServer(t)
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		w := serveAlert(srv, method, "/api/v1/alerts/order-settings", nil)
		if w.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s order settings status=%d body=%s", method, w.Code, w.Body.String())
		}
	}
}

// TestTelegramTestEndpointReportsFailure proves the test button surfaces the real
// Telegram answer instead of pretending success, without touching the public endpoint.
func TestTelegramTestEndpointReportsFailure(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	_, logs, err := observability.New(t.TempDir()+"/application.log", 7)
	if err != nil {
		t.Fatal(err)
	}
	defer logs.Close()
	var paths []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"ok":false,"error_code":404,"description":"Not Found"}`)
	}))
	defer upstream.Close()
	srv, st := testService(t, config.Config{TelegramAPIBase: upstream.URL}, logger, logs)
	defer st.Close()
	disabled := serveAlert(srv, http.MethodPost, "/api/v1/alerts/settings/test", nil)
	if disabled.Code != http.StatusBadGateway {
		t.Fatalf("a test send without credentials must fail: %d %s", disabled.Code, disabled.Body.String())
	}
	if !strings.Contains(disabled.Body.String(), "Telegram") {
		t.Fatalf("the failure must explain what is missing: %s", disabled.Body.String())
	}
	if saved := serveAlert(srv, http.MethodPost, "/api/v1/alerts/settings", strings.NewReader(`{"telegram_enabled":true,"telegram_token":"12345:secretvalue","telegram_chat_id":"1"}`)); saved.Code != http.StatusOK {
		t.Fatalf("credentials must be accepted for the test send: %d %s", saved.Code, saved.Body.String())
	}
	sending := serveAlert(srv, http.MethodPost, "/api/v1/alerts/settings/test", nil)
	if sending.Code != http.StatusBadGateway {
		t.Fatalf("a rejected test send must report the failure: %d %s", sending.Code, sending.Body.String())
	}
	if !strings.Contains(sending.Body.String(), "Not Found") {
		t.Fatalf("the Telegram answer must reach the caller: %s", sending.Body.String())
	}
	if strings.Contains(sending.Body.String(), "secretvalue") {
		t.Fatalf("the delivery error must not leak the token: %s", sending.Body.String())
	}
	if len(paths) != 1 || paths[0] != "/bot12345:secretvalue/sendMessage" {
		t.Fatalf("the test send must hit the configured base: %v", paths)
	}
}

// TestAlertChartPageAndAssets checks the per symbol K-line page renders inside the
// shared navigation and serves its own three assets.
func TestAlertChartPageAndAssets(t *testing.T) {
	srv, _ := alertTestServer(t)
	w := serveAlert(srv, http.MethodGet, "/alerts/chart?symbol=BTCUSDT", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("chart page status=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if strings.Contains(body, "K 线实时取自 Binance USDⓈ-M klines 接口") {
		t.Fatal("the chart page must not render the removed K-line data note")
	}
	for _, removed := range []string{"peak-decay", "Peak-Decay", "kline-peak-decay-label", "peakDecay"} {
		if strings.Contains(body, removed) {
			t.Fatalf("the chart page must not render removed peak-decay display logic %q", removed)
		}
	}
	for _, removed := range []string{"成交额（下方柱状）", "kline-status", "liquidation-delta-section", "liquidation-delta-chart", "liquidation-delta-head", "10 分钟 · 回看 48 小时"} {
		if strings.Contains(body, removed) {
			t.Fatalf("the chart page must not render the removed status or detached delta panel %q", removed)
		}
	}
	for _, want := range []string{"币对 K 线", "kline-symbol-label", "当前币对", "左右两侧价格轴", "横轴北京时间", `data-page="alerts"`, `aria-current="page"`, "kline-chart", "kline-intervals",
		"kline-alerts", "kline-signals", "kline-aggressive", "kline-steady", "kline-zones", "kline-donchian", "显示预警", "显示下单信号", "VWAP 分区", "Donchian 过滤", "稳健实心 / 激进空心", "◆!", "激进信号", "稳健信号", "价格轴上下拖动缩放垂直价格", "当前已保存的阈值和周期重新回放生成", "回到最新", "重置缩放", "kline-signal-rows",
		"历史分区 · 高位开空", "历史分区 · 中间仅平仓", "历史分区 · 低位接多", "kline-chart-surface", "每根 K 线按当时滚动 VWAP、配置窗口内 HH/LL、最小回撤和 Donchian 分位绘制历史三分区", "VWAP 分区开关只控制图中的分区显示，不负责 Donchian 信号过滤", "Donchian 过滤开关独立控制图中和下方表格的预警、激进及稳健信号", "净多空", "累计净多空（48小时）", "币对 K 线、累计净多空与下单信号", "assets/alerts-chart.css", "assets/alerts-chart.js", "assets/alerts-chart-app.js", "assets/bubble-chart.js", "position-chart-page"} {
		if !strings.Contains(body, want) {
			t.Fatalf("the K-line page must contain %q", want)
		}
	}
	for path, kind := range map[string]string{"/assets/alerts-chart.css": "text/css", "/assets/alerts-chart.js": "text/javascript", "/assets/alerts-chart-app.js": "text/javascript"} {
		asset := serveAlert(srv, http.MethodGet, path, nil)
		if asset.Code != http.StatusOK || !strings.HasPrefix(asset.Header().Get("Content-Type"), kind) {
			t.Fatalf("%s status=%d type=%q", path, asset.Code, asset.Header().Get("Content-Type"))
		}
		if asset.Body.Len() == 0 {
			t.Fatalf("%s must not be empty", path)
		}
	}
	chartScript := serveAlert(srv, http.MethodGet, "/assets/alerts-chart-app.js", nil).Body.String()
	for _, removed := range []string{"peak-decay", "Peak-Decay", "kline-peak-decay-label", "peakDecay"} {
		if strings.Contains(chartScript, removed) {
			t.Fatalf("the chart script must not contain removed peak-decay display logic %q", removed)
		}
	}
	for _, want := range []string{"window_hours", "SIGNAL_REPLAY_WINDOW_HOURS = 7 * 24", "signalReplayWindowHours = positionMode ? 0 : SIGNAL_REPLAY_WINDOW_HOURS", "STORE_SIGNAL_RECALCULATE", "过去 7 天重算", "addEventListener('storage'", "drawRadarZones", "historicalRadarZones", "radarZones", "rawShownSignals", "vwapZoneAt", "donchianAllowsSignal", "upper_open_price", "anchorPrice", "dragPriceRange", "Math.round(baseHeight * 1.1)", "STORE_ZONES", "STORE_DONCHIAN", "仅平仓区"} {
		if !strings.Contains(chartScript, want) {
			t.Fatalf("the chart must refresh after signal recalculation with %q", want)
		}
	}
	chartMath := serveAlert(srv, http.MethodGet, "/assets/alerts-chart.js", nil).Body.String()
	for _, removed := range []string{"peak-decay", "peakDecay"} {
		if strings.Contains(chartMath, removed) {
			t.Fatalf("the chart marker script must not contain removed peak-decay display logic %q", removed)
		}
	}
	for _, want := range []string{"radarZones", "vwapSignalAllowed", "upper_open_price", "lower_open_price"} {
		if !strings.Contains(chartMath, want) {
			t.Fatalf("the chart math must expose VWAP signal filtering with %q", want)
		}
	}
	chartCSS := serveAlert(srv, http.MethodGet, "/assets/alerts-chart.css", nil).Body.String()
	for _, removed := range []string{"peak-decay", "kline-peak-decay-label"} {
		if strings.Contains(chartCSS, removed) {
			t.Fatalf("the chart CSS must not contain removed peak-decay display logic %q", removed)
		}
	}
	if strings.Contains(chartCSS, "width:110%") {
		t.Fatal("the K-line chart must keep its original width")
	}
	if post := serveAlert(srv, http.MethodPost, "/alerts/chart", nil); post.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /alerts/chart status=%d", post.Code)
	}
}

func TestAlertLiquidationDeltaEndpointAggregatesConfiguredWindow(t *testing.T) {
	srv, st := alertTestServer(t)
	ctx := context.Background()
	now := time.Now().UTC()
	bucket := now.Truncate(10 * time.Minute)
	for _, event := range []domain.LiquidationEvent{
		{ID: "delta-long", Exchange: "binance", Symbol: "BTCUSDT", PositionSide: "long", EventTime: bucket.Add(-8 * time.Minute), ReceivedAt: now, NotionalUSD: 100},
		{ID: "delta-short", Exchange: "binance", Symbol: "BTCUSDT", PositionSide: "short", EventTime: bucket.Add(time.Minute), ReceivedAt: now, NotionalUSD: 350},
		{ID: "delta-eth", Exchange: "binance", Symbol: "ETHUSDT", PositionSide: "short", EventTime: bucket.Add(time.Minute), ReceivedAt: now, NotionalUSD: 900},
	} {
		if err := st.InsertLiquidation(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	w := serveAlert(srv, http.MethodGet, "/api/v1/alerts/liquidation-delta?symbol=BTCUSDT", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("delta status=%d body=%s", w.Code, w.Body.String())
	}
	var view domain.LiquidationDeltaView
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Symbol != "BTCUSDT" || view.Interval != "10m" || view.Hours != 48 || len(view.Points) != 288 {
		t.Fatalf("delta view=%+v", view)
	}
	var current, previous *domain.LiquidationDeltaPoint
	for i := range view.Points {
		point := &view.Points[i]
		if point.Time.Equal(bucket) {
			current = point
		}
		if point.Time.Equal(bucket.Add(-10 * time.Minute)) {
			previous = point
		}
	}
	if current == nil || current.ShortUSD != 350 || current.LongUSD != 0 || current.DeltaUSD != 350 || current.CumulativeDeltaUSD != 250 || current.Count != 1 || previous == nil || previous.DeltaUSD != -100 || previous.CumulativeDeltaUSD != -100 {
		t.Fatalf("current=%+v previous=%+v", current, previous)
	}
	rangeFrom := bucket.Add(-19 * time.Minute)
	rangeTo := bucket.Add(19 * time.Minute)
	rangePath := "/api/v1/alerts/liquidation-delta?" + url.Values{
		"symbol": []string{"BTCUSDT"},
		"from":   []string{rangeFrom.Format(time.RFC3339Nano)},
		"to":     []string{rangeTo.Format(time.RFC3339Nano)},
	}.Encode()
	ranged := serveAlert(srv, http.MethodGet, rangePath, nil)
	if ranged.Code != http.StatusOK {
		t.Fatalf("ranged delta status=%d body=%s", ranged.Code, ranged.Body.String())
	}
	var rangedView domain.LiquidationDeltaView
	if err := json.Unmarshal(ranged.Body.Bytes(), &rangedView); err != nil {
		t.Fatal(err)
	}
	if !rangedView.From.Equal(bucket.Add(-20*time.Minute)) || !rangedView.To.Equal(bucket.Add(10*time.Minute)) || len(rangedView.Points) != 3 {
		t.Fatalf("ranged delta view=%+v", rangedView)
	}
	if rangedView.Points[0].DeltaUSD != 0 || rangedView.Points[0].CumulativeDeltaUSD != 0 || rangedView.Points[1].DeltaUSD != -100 || rangedView.Points[1].CumulativeDeltaUSD != -100 || rangedView.Points[2].DeltaUSD != 350 || rangedView.Points[2].CumulativeDeltaUSD != 250 {
		t.Fatalf("ranged delta points=%+v", rangedView.Points)
	}
	longFrom := bucket.Add(-72 * time.Hour)
	longTo := bucket
	longPath := "/api/v1/alerts/liquidation-delta?" + url.Values{
		"symbol": []string{"BTCUSDT"},
		"from":   []string{longFrom.Format(time.RFC3339Nano)},
		"to":     []string{longTo.Format(time.RFC3339Nano)},
	}.Encode()
	longRange := serveAlert(srv, http.MethodGet, longPath, nil)
	if longRange.Code != http.StatusOK {
		t.Fatalf("long ranged delta status=%d body=%s", longRange.Code, longRange.Body.String())
	}
	var longView domain.LiquidationDeltaView
	if err := json.Unmarshal(longRange.Body.Bytes(), &longView); err != nil {
		t.Fatal(err)
	}
	if longView.Hours != 72 || !longView.From.Equal(longFrom) || !longView.To.Equal(longTo) || len(longView.Points) != 72*6 || longView.Points[0].CumulativeDeltaUSD != 0 || longView.Points[len(longView.Points)-1].CumulativeDeltaUSD != -100 {
		t.Fatalf("long ranged delta view=%+v", longView)
	}
	hoursRange := serveAlert(srv, http.MethodGet, "/api/v1/alerts/liquidation-delta?symbol=BTCUSDT&hours=72", nil)
	if hoursRange.Code != http.StatusOK {
		t.Fatalf("hours delta status=%d body=%s", hoursRange.Code, hoursRange.Body.String())
	}
	var hoursView domain.LiquidationDeltaView
	if err := json.Unmarshal(hoursRange.Body.Bytes(), &hoursView); err != nil {
		t.Fatal(err)
	}
	if hoursView.Hours != 72 || len(hoursView.Points) != 72*6 {
		t.Fatalf("hours delta view=%+v", hoursView)
	}
	for _, path := range []string{
		"/api/v1/alerts/liquidation-delta",
		"/api/v1/alerts/liquidation-delta?symbol=BTCUSDT&interval=5m",
		"/api/v1/alerts/liquidation-delta?symbol=BTCUSDT&hours=0",
		"/api/v1/alerts/liquidation-delta?symbol=BTCUSDT&hours=4321",
		"/api/v1/alerts/liquidation-delta?symbol=BTCUSDT&from=2026-10-23T00:00:00Z",
		"/api/v1/alerts/liquidation-delta?symbol=BTCUSDT&to=2026-10-23T01:00:00Z",
		"/api/v1/alerts/liquidation-delta?symbol=BTCUSDT&from=bad&to=2026-10-23T01:00:00Z",
		"/api/v1/alerts/liquidation-delta?symbol=BTCUSDT&from=2026-10-23T02:00:00Z&to=2026-10-23T01:00:00Z",
		"/api/v1/alerts/liquidation-delta?symbol=BTCUSDT&from=2025-10-23T00:00:00Z&to=2026-10-23T01:00:00Z",
	} {
		if got := serveAlert(srv, http.MethodGet, path, nil).Code; got != http.StatusBadRequest {
			t.Fatalf("%s status=%d want 400", path, got)
		}
	}
}

func TestAlertChartStandalonePositionPage(t *testing.T) {
	srv, _ := alertTestServer(t)
	w := serveAlert(srv, http.MethodGet, "/alerts/chart?position=1&standalone=1&symbol=BTCUSDT", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("standalone position chart status=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{"position-chart-page", "position-chart-standalone", "kline-chart", "返回报警"} {
		if !strings.Contains(body, want) {
			t.Fatalf("standalone position chart must contain %q", want)
		}
	}
}

// TestAlertChartPeriods keeps the offered periods exactly as requested: the
// chart builds its buttons from one shared list.
func TestAlertChartPeriods(t *testing.T) {
	srv, _ := alertTestServer(t)
	script := serveAlert(srv, http.MethodGet, "/assets/alerts-chart.js", nil).Body.String()
	if !strings.Contains(script, "['1m', '2m', '3m', '5m', '15m', '30m', '1h', '4h', '8h', '12h']") {
		t.Fatal("the chart must offer the requested periods, in order")
	}
	for _, forbidden := range []string{"'1d'", "'6h'", "'1w'"} {
		if strings.Contains(script, forbidden) {
			t.Fatalf("the chart must not offer period %s", forbidden)
		}
	}
}

// TestAlertPageLinksSymbolsToTheChart documents that every pair name on the alert
// page opens its own K-line tab.
func TestAlertPageLinksSymbolsToTheChart(t *testing.T) {
	srv, _ := alertTestServer(t)
	script := serveAlert(srv, http.MethodGet, "/assets/alerts.js", nil).Body.String()
	for _, want := range []string{"alerts/chart?symbol=", `target="_blank"`, `rel="noopener"`, "encodeURIComponent(symbol)"} {
		if !strings.Contains(script, want) {
			t.Fatalf("the alert page must link pairs to the chart with %q", want)
		}
	}
	// the monitor table, the signal history and the alert history all link out
	if got := strings.Count(script, "chartLink("); got != 3 {
		t.Fatalf("the monitor, signal and alert tables must all link to the chart, found %d uses", got)
	}
}

func TestAlertPageShowsRadarFilterReasonInProcessStatus(t *testing.T) {
	srv, _ := alertTestServer(t)
	script := serveAlert(srv, http.MethodGet, "/assets/alerts.js", nil).Body.String()
	for _, want := range []string{"filter_reason", "filter_applied", "open_allowed", "close_allowed", "alerts-radar-filter", "peak_decay_armed", "deep_liquidation_quota_or_atr_not_ready"} {
		if !strings.Contains(script, want) {
			t.Fatalf("alert process status must render radar filter detail %q", want)
		}
	}
}

// TestAlertKlineQueryValidation documents the guards in front of the proxy.
func TestAlertKlineQueryValidation(t *testing.T) {
	symbol, interval, limit, before, err := alertKlineQuery(url.Values{"symbol": []string{"dogeusdt"}})
	if err != nil {
		t.Fatal(err)
	}
	if symbol != "DOGEUSDT" || interval != alertKlineDefaultInterval || limit != 300 || !before.IsZero() {
		t.Fatalf("defaults wrong: %s %s %d %v", symbol, interval, limit, before)
	}
	until := time.Now().UTC().Add(-time.Hour).Truncate(time.Minute)
	_, mixedCase, bigLimit, gotBefore, err := alertKlineQuery(url.Values{"symbol": []string{"BTCUSDT"}, "interval": []string{"4H"},
		"limit": []string{"1000"}, "before": []string{until.Format(time.RFC3339)}})
	if err != nil || mixedCase != "4h" || bigLimit != 1000 || !gotBefore.Equal(until) {
		t.Fatalf("a valid query must pass through: %v %s %d %v", err, mixedCase, bigLimit, gotBefore)
	}
	// The cursor is the oldest candle of the page currently in view, so a chart
	// opened on the newest data asks for a time only a minute or two old; rejecting
	// that would break paging on every live chart.
	for _, recent := range []time.Duration{-2 * time.Minute, -90 * time.Second, -time.Minute} {
		cursor := time.Now().UTC().Add(recent).Truncate(time.Second)
		if _, _, _, got, err := alertKlineQuery(url.Values{"symbol": []string{"BTCUSDT"}, "before": []string{cursor.Format(time.RFC3339)}}); err != nil {
			t.Fatalf("a cursor %v old must be accepted: %v", -recent, err)
		} else if !got.Equal(cursor) {
			t.Fatalf("the cursor must survive the round trip: %v != %v", got, cursor)
		}
	}
	rejected := []struct {
		name  string
		query url.Values
	}{
		{"missing symbol", url.Values{}},
		{"symbol with a space", url.Values{"symbol": []string{"BTC USDT"}}},
		{"symbol with a query", url.Values{"symbol": []string{"BTCUSDT&limit=1"}}},
		{"symbol too long", url.Values{"symbol": []string{strings.Repeat("A", 33)}}},
		{"period one day", url.Values{"symbol": []string{"BTCUSDT"}, "interval": []string{"1d"}}},
		{"limit zero", url.Values{"symbol": []string{"BTCUSDT"}, "limit": []string{"0"}}},
		{"limit past the page", url.Values{"symbol": []string{"BTCUSDT"}, "limit": []string{"1501"}}},
		{"limit not a number", url.Values{"symbol": []string{"BTCUSDT"}, "limit": []string{"many"}}},
		{"before unparseable", url.Values{"symbol": []string{"BTCUSDT"}, "before": []string{"yesterday"}}},
		{"before the future", url.Values{"symbol": []string{"BTCUSDT"}, "before": []string{time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339)}}},
		{"before coinbase", url.Values{"symbol": []string{"BTCUSDT"}, "before": []string{"2001-01-01T00:00:00Z"}}},
	}
	for _, bad := range rejected {
		if _, _, _, _, err := alertKlineQuery(bad.query); err == nil {
			t.Fatalf("%s must be rejected: %v", bad.name, bad.query)
		}
	}
}

// TestAlertKlineEndpointRejectsBadRequests covers the HTTP surface: a rejected
// query never reaches Binance, so it fails as a client error.
func TestAlertKlineEndpointRejectsBadRequests(t *testing.T) {
	srv, _ := alertTestServer(t)
	for _, path := range []string{"/api/v1/alerts/klines", "/api/v1/alerts/klines?symbol=BTCUSDT&interval=1d",
		"/api/v1/alerts/klines?symbol=%22%3Bdrop%20table", "/api/v1/alerts/klines?symbol=BTCUSDT&limit=5000",
		"/api/v1/alerts/klines?symbol=BTCUSDT&before=soon"} {
		w := serveAlert(srv, http.MethodGet, path, nil)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s status=%d want 400 body=%s", path, w.Code, w.Body.String())
		}
	}
	if post := serveAlert(srv, http.MethodPost, "/api/v1/alerts/klines", nil); post.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /api/v1/alerts/klines status=%d", post.Code)
	}
}

// TestIsDashboardPageCoversChartRoute keeps the K-line page behind the login form
// like the other pages instead of a bare JSON 401.
func TestIsDashboardPageCoversChartRoute(t *testing.T) {
	for _, path := range []string{"/", "/bubbles", "/options", "/alerts", "/alerts/chart", "/menu"} {
		if !isDashboardPage(path) {
			t.Fatalf("%s must render the login page when unauthenticated", path)
		}
	}
	for _, path := range []string{"/api/v1/alerts", "/api/v1/alerts/klines", "/assets/alerts-chart.js"} {
		if isDashboardPage(path) {
			t.Fatalf("%s must answer with a 401 problem", path)
		}
	}
}
