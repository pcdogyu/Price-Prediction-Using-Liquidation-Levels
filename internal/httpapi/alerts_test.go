package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
	for _, want := range []string{"报警", `data-page="alerts"`, "assets/alerts.js", "assets/alerts.css", "aria-current=\"page\""} {
		if !strings.Contains(body, want) {
			t.Fatalf("the alerts page must contain %q", want)
		}
	}
	// The navigation entry has to sit to the right of the options page.
	options, alerts := strings.Index(body, `href="/options"`), strings.Index(body, `href="/alerts"`)
	if options < 0 || alerts < 0 || alerts < options {
		t.Fatalf("报警 must follow 期权 in the navigation: options %d alerts %d", options, alerts)
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
	if post := serveAlert(srv, http.MethodPost, "/alerts", nil); post.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /alerts status=%d", post.Code)
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
	if view.Thresholds.BTCUSD != 300000 || view.Thresholds.ETHUSD != 250000 || view.Thresholds.OtherUSD != 300000 {
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
	for _, path := range []string{
		"/api/v1/alerts/history?limit=0",
		"/api/v1/alerts/history?limit=999",
		"/api/v1/alerts/history?side=both",
		"/api/v1/alerts/history?cursor=not-a-cursor",
		"/api/v1/alerts/signals?kind=trend",
		"/api/v1/alerts/signals?limit=-1",
		"/api/v1/alerts/signals?symbol=" + strings.Repeat("T", 33),
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
	saved := serveAlert(srv, http.MethodPost, "/api/v1/alerts/settings", strings.NewReader(`{"telegram_enabled":true,"telegram_token":"12345:secretvalue","telegram_chat_id":"-1009","notify_alerts":false,"notify_aggressive":true,"notify_steady":false}`))
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
	if !settings.TelegramEnabled || settings.TelegramChatID != "-1009" || settings.NotifyAlerts || !settings.NotifyAggressive || settings.NotifySteady {
		t.Fatalf("the settings did not round trip: %+v", settings)
	}
	if !settings.HasToken || settings.TokenMask != "12345:••••" || settings.TokenSource != "database" {
		t.Fatalf("the token must only be exposed masked: %+v", settings)
	}
	read := serveAlert(srv, http.MethodGet, "/api/v1/alerts/settings", nil)
	if read.Code != http.StatusOK || strings.Contains(read.Body.String(), "secretvalue") || !strings.Contains(read.Body.String(), "12345:••••") {
		t.Fatalf("reading settings must mask the token: %d %s", read.Code, read.Body.String())
	}
	// Leaving the token empty keeps the stored one instead of wiping it.
	kept := serveAlert(srv, http.MethodPost, "/api/v1/alerts/settings", strings.NewReader(`{"telegram_enabled":true,"telegram_chat_id":"-1009","telegram_token":""}`))
	if kept.Code != http.StatusOK || !strings.Contains(kept.Body.String(), `"token_mask":"12345:••••"`) {
		t.Fatalf("an empty token must keep the stored token: %d %s", kept.Code, kept.Body.String())
	}
	// Disabling notification is always allowed, even with credentials on file.
	off := serveAlert(srv, http.MethodPost, "/api/v1/alerts/settings", strings.NewReader(`{"telegram_enabled":false,"telegram_chat_id":"-1009"}`))
	if off.Code != http.StatusOK || strings.Contains(off.Body.String(), `"telegram_enabled":true`) {
		t.Fatalf("disabling must succeed: %d %s", off.Code, off.Body.String())
	}
	if put := serveAlert(srv, http.MethodPut, "/api/v1/alerts/settings", nil); put.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT settings status=%d", put.Code)
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
