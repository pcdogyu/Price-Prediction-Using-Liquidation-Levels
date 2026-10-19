package httpapi

import (
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/exchange"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/store"
)

//go:embed alerts.html
var alertsHTML []byte

//go:embed alerts.js
var alertsJS []byte

//go:embed alerts.css
var alertsCSS []byte

//go:embed alerts-chart.html
var alertsChartHTML []byte

//go:embed alerts-chart.js
var alertsChartJS []byte

//go:embed alerts-chart-app.js
var alertsChartAppJS []byte

//go:embed alerts-chart.css
var alertsChartCSS []byte

// alertKlineDefaultInterval is what a bare ?symbol=... link opens with; the nine
// supported periods themselves come from the exchange package.
const alertKlineDefaultInterval = "15m"

// alertSymbolPattern accepts Binance perpetual symbols such as BTCUSDT or
// 1000SHIBUSDT and rejects anything that could leak into an upstream query.
var alertSymbolPattern = regexp.MustCompile(`^[A-Z0-9]{2,32}$`)

// registerAlerts exposes the liquidation alert page, its polling endpoints and the
// Telegram and order-signal settings forms.
func (s *Server) registerAlerts(mux *http.ServeMux) {
	mux.HandleFunc("/alerts", getOnly(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(s.renderPage(alertsHTML, "alerts", "报警"))
	}))
	mux.HandleFunc("/alerts/chart", getOnly(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(s.renderPage(alertsChartHTML, "alerts", "币对K线"))
	}))
	mux.HandleFunc("/assets/alerts.js", getOnly(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(alertsJS)
	}))
	mux.HandleFunc("/assets/alerts.css", getOnly(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(alertsCSS)
	}))
	mux.HandleFunc("/assets/alerts-chart.js", getOnly(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(alertsChartJS)
	}))
	mux.HandleFunc("/assets/alerts-chart-app.js", getOnly(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(alertsChartAppJS)
	}))
	mux.HandleFunc("/assets/alerts-chart.css", getOnly(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(alertsChartCSS)
	}))
	mux.HandleFunc("/api/v1/alerts", getOnly(s.alertOverview))
	mux.HandleFunc("/api/v1/alerts/history", getOnly(s.alertHistory))
	mux.HandleFunc("/api/v1/alerts/signals", getOnly(s.alertSignals))
	mux.HandleFunc("/api/v1/alerts/timeline", getOnly(s.alertTimeline))
	mux.HandleFunc("/api/v1/alerts/chart-signals", getOnly(s.alertChartSignals))
	mux.HandleFunc("/api/v1/alerts/klines", getOnly(s.alertKlines))
	mux.HandleFunc("/api/v1/alerts/settings", s.alertSettings)
	mux.HandleFunc("/api/v1/alerts/order-settings", s.alertOrderSettings)
	mux.HandleFunc("/api/v1/alerts/strategy-settings", s.alertStrategySettings)
	mux.HandleFunc("/api/v1/alerts/settings/test", postOnly(s.alertSettingsTest))
	mux.HandleFunc("/api/v1/alerts/trigger", postOnly(s.alertTrigger))
	mux.HandleFunc("/api/v1/alerts/notify", postOnly(s.alertNotify))
}

func (s *Server) alertTrigger(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RecordType string `json:"record_type"`
		RecordID   string `json:"record_id"`
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 8<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil {
		problem(w, http.StatusBadRequest, fmt.Errorf("invalid trigger request: %w", err))
		return
	}
	in.RecordType = strings.ToLower(strings.TrimSpace(in.RecordType))
	in.RecordID = strings.TrimSpace(in.RecordID)
	if (in.RecordType != "alert" && in.RecordType != domain.SignalAggressive && in.RecordType != domain.SignalSteady) || in.RecordID == "" || len(in.RecordID) > 256 {
		problem(w, http.StatusBadRequest, errors.New("record_type and record_id are invalid"))
		return
	}
	result, err := s.svc.TriggerAlert(r.Context(), in.RecordType, in.RecordID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			problem(w, http.StatusNotFound, errors.New("alert or signal not found"))
			return
		}
		problem(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusAccepted, result)
}

// alertNotify sends the selected persisted alert or signal to the configured
// Telegram group. The record is read server-side so the browser cannot alter
// the message contents or destination.
func (s *Server) alertNotify(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RecordType string `json:"record_type"`
		RecordID   string `json:"record_id"`
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 8<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil {
		problem(w, http.StatusBadRequest, fmt.Errorf("invalid notify request: %w", err))
		return
	}
	in.RecordType = strings.ToLower(strings.TrimSpace(in.RecordType))
	in.RecordID = strings.TrimSpace(in.RecordID)
	if (in.RecordType != "alert" && in.RecordType != domain.SignalAggressive && in.RecordType != domain.SignalSteady) || in.RecordID == "" || len(in.RecordID) > 256 {
		problem(w, http.StatusBadRequest, errors.New("record_type and record_id are invalid"))
		return
	}
	result, err := s.svc.NotifyAlert(r.Context(), in.RecordType, in.RecordID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			problem(w, http.StatusNotFound, errors.New("alert or signal not found"))
			return
		}
		problem(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusAccepted, result)
}

// alertTimeline reads the unified alert and signal history. The page size is
// intentionally fixed at 20 so every browser page has the same shape.
func (s *Server) alertTimeline(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	symbol, _, err := alertFilters(query.Get("symbol"), "")
	if err != nil {
		problem(w, http.StatusBadRequest, err)
		return
	}
	kind := strings.ToLower(strings.TrimSpace(query.Get("kind")))
	if kind == "all" {
		kind = ""
	}
	if kind != "" && kind != "alert" && kind != domain.SignalAggressive && kind != domain.SignalSteady {
		problem(w, http.StatusBadRequest, fmt.Errorf("kind must be alert, aggressive, steady or all"))
		return
	}
	if err = alertCursor(query.Get("cursor")); err != nil {
		problem(w, http.StatusBadRequest, err)
		return
	}
	page, err := s.svc.AlertTimeline(r.Context(), symbol, kind, 20, query.Get("cursor"))
	if err != nil {
		problem(w, http.StatusServiceUnavailable, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, page)
}

// alertOverview serves the monitoring snapshot, the newest alerts and signals, and
// the masked Telegram settings in one poll.
func (s *Server) alertOverview(w http.ResponseWriter, r *http.Request) {
	view, err := s.svc.Alerts(r.Context())
	if err != nil {
		problem(w, http.StatusServiceUnavailable, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"data": view, "settings": s.svc.AlertSettings(), "sources": s.svc.Health()})
}

// alertCursor validates a pagination cursor before it reaches the store, so a bad
// value is a client error instead of a service failure.
func alertCursor(value string) error {
	if _, _, err := store.DecodeCursor(value); err != nil {
		return err
	}
	return nil
}

// alertHistory reads stored alerts with cursor pagination.
func (s *Server) alertHistory(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	symbol, side, err := alertFilters(query.Get("symbol"), query.Get("side"))
	if err != nil {
		problem(w, http.StatusBadRequest, err)
		return
	}
	if err = alertCursor(query.Get("cursor")); err != nil {
		problem(w, http.StatusBadRequest, err)
		return
	}
	limit, err := alertLimit(query.Get("limit"))
	if err != nil {
		problem(w, http.StatusBadRequest, err)
		return
	}
	page, err := s.svc.AlertHistory(r.Context(), symbol, side, limit, query.Get("cursor"))
	if err != nil {
		problem(w, http.StatusServiceUnavailable, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, page)
}

// alertSignals reads stored order signals with cursor pagination.
func (s *Server) alertSignals(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	symbol, _, err := alertFilters(query.Get("symbol"), "")
	if err != nil {
		problem(w, http.StatusBadRequest, err)
		return
	}
	kind := strings.ToLower(strings.TrimSpace(query.Get("kind")))
	if kind == "" || kind == "all" {
		kind = ""
	}
	if kind != "" && kind != domain.SignalAggressive && kind != domain.SignalSteady {
		problem(w, http.StatusBadRequest, fmt.Errorf("kind must be aggressive, steady or all"))
		return
	}
	if err = alertCursor(query.Get("cursor")); err != nil {
		problem(w, http.StatusBadRequest, err)
		return
	}
	limit, err := alertLimit(query.Get("limit"))
	if err != nil {
		problem(w, http.StatusBadRequest, err)
		return
	}
	page, err := s.svc.AlertSignals(r.Context(), symbol, kind, limit, query.Get("cursor"))
	if err != nil {
		problem(w, http.StatusServiceUnavailable, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, page)
}

// alertKlines proxies the Binance USDⓈ-M klines for a single pair so the K-line
// page can chart every monitored symbol, not only the ones stored locally.
func (s *Server) alertKlines(w http.ResponseWriter, r *http.Request) {
	chartSymbol, chartInterval, chartLimit, chartBefore, err := alertKlineQuery(r.URL.Query())
	if err != nil {
		problem(w, http.StatusBadRequest, err)
		return
	}
	view, err := s.svc.AlertKlines(r.Context(), chartSymbol, chartInterval, chartLimit, chartBefore)
	if err != nil {
		problem(w, http.StatusBadGateway, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, view)
}

// alertChartSignals replays one pair with the current saved thresholds and
// rolling window. The stored history endpoints remain unchanged because their
// rows describe the settings that were active when they were generated.
func (s *Server) alertChartSignals(w http.ResponseWriter, r *http.Request) {
	symbol, _, err := alertFilters(r.URL.Query().Get("symbol"), "")
	if err != nil || !alertSymbolPattern.MatchString(symbol) {
		if err == nil {
			err = fmt.Errorf("symbol must be 2 to 32 letters or digits")
		}
		problem(w, http.StatusBadRequest, err)
		return
	}
	view, err := s.svc.AlertChartSignals(r.Context(), symbol)
	if err != nil {
		problem(w, http.StatusServiceUnavailable, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, view)
}

// alertKlineQuery validates the K-line query. The symbol is mandatory because the
// chart only ever opens from a symbol on the alert page, and every value is
// checked before it can reach the upstream Binance request.
func alertKlineQuery(query url.Values) (symbol, interval string, limit int, before time.Time, err error) {
	symbol = strings.ToUpper(strings.TrimSpace(query.Get("symbol")))
	if !alertSymbolPattern.MatchString(symbol) {
		return "", "", 0, time.Time{}, fmt.Errorf("symbol must be 2 to 32 letters or digits")
	}
	interval = strings.ToLower(strings.TrimSpace(query.Get("interval")))
	if interval == "" {
		interval = alertKlineDefaultInterval
	}
	if _, ok := exchange.KlineIntervalMinutes[interval]; !ok {
		return "", "", 0, time.Time{}, fmt.Errorf("interval must be one of %s", strings.Join(exchange.KlineIntervals, ", "))
	}
	limit = 300
	if raw := strings.TrimSpace(query.Get("limit")); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > exchange.KlinePageLimit {
			return "", "", 0, time.Time{}, fmt.Errorf("limit must be between 1 and %d", exchange.KlinePageLimit)
		}
	}
	if raw := strings.TrimSpace(query.Get("before")); raw != "" {
		before, err = time.Parse(time.RFC3339, raw)
		if err != nil {
			return "", "", 0, time.Time{}, fmt.Errorf("before must be an RFC3339 timestamp")
		}
		before = before.UTC()
		if before.Before(time.Date(2010, 1, 1, 0, 0, 0, 0, time.UTC)) {
			return "", "", 0, time.Time{}, fmt.Errorf("before is outside the supported range")
		}
		// The cursor is the oldest candle of the page in view, so a chart opened on
		// the newest data asks for a time only one bucket old. Only a timestamp
		// clearly ahead of this server is a bad cursor.
		if before.After(time.Now().UTC().Add(2 * time.Minute)) {
			return "", "", 0, time.Time{}, fmt.Errorf("before is in the future")
		}
	}
	return symbol, interval, limit, before, nil
}

// alertSettings reads or stores the Telegram notification settings. The bot token is
// write only: a read returns a masked value, and an empty submission keeps the
// stored token so the form never has to echo a secret back to the browser.
func (s *Server) alertSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, s.svc.AlertSettings())
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		problem(w, http.StatusMethodNotAllowed, fmt.Errorf("method must be GET or POST"))
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 32<<10))
	if err != nil {
		problem(w, http.StatusBadRequest, err)
		return
	}
	var in domain.NotificationSettings
	if err = json.Unmarshal(body, &in); err != nil {
		problem(w, http.StatusBadRequest, fmt.Errorf("settings body must be JSON: %w", err))
		return
	}
	// Keep the order-signal switches when an older client submits only the
	// Telegram fields. The dedicated order-settings endpoint is the preferred UI
	// path, but this makes the original settings API backward compatible.
	current := s.svc.AlertSettings()
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(body, &fields); err == nil {
		if _, ok := fields["order_alerts"]; !ok {
			in.OrderAlerts = current.OrderAlerts
		}
		if _, ok := fields["order_aggressive"]; !ok {
			in.OrderAggressive = current.OrderAggressive
		}
		if _, ok := fields["order_steady"]; !ok {
			in.OrderSteady = current.OrderSteady
		}
	}
	token, err := alertTokenFromBody(body)
	if err != nil {
		problem(w, http.StatusBadRequest, err)
		return
	}
	in.TelegramToken = token
	stored, err := s.svc.SaveAlertSettings(r.Context(), in)
	if err != nil {
		problem(w, http.StatusBadRequest, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, stored)
}

// alertOrderSettings reads or stores the live opening-signal switches sent to
// tvbot. Every live category still sends a preclose request first; these
// switches are independent from Telegram notification preferences.
func (s *Server) alertOrderSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, s.svc.AlertOrderSignalSettings())
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		problem(w, http.StatusMethodNotAllowed, fmt.Errorf("method must be GET or POST"))
		return
	}
	var in domain.OrderSignalSettings
	decoder := json.NewDecoder(io.LimitReader(r.Body, 8<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil {
		problem(w, http.StatusBadRequest, fmt.Errorf("order settings body must be JSON: %w", err))
		return
	}
	stored, err := s.svc.SaveAlertOrderSignalSettings(r.Context(), in)
	if err != nil {
		problem(w, http.StatusBadRequest, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, stored)
}

// alertStrategySettings reads or stores the editable liquidation threshold and
// rolling-window parameters. The endpoint is protected by the page's existing
// authentication middleware just like the Telegram settings endpoint.
func (s *Server) alertStrategySettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, s.svc.AlertStrategySettings())
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		problem(w, http.StatusMethodNotAllowed, fmt.Errorf("method must be GET or POST"))
		return
	}
	var in domain.AlertStrategySettings
	decoder := json.NewDecoder(io.LimitReader(r.Body, 8<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil {
		problem(w, http.StatusBadRequest, fmt.Errorf("strategy settings body must be JSON: %w", err))
		return
	}
	stored, err := s.svc.SaveAlertStrategySettings(r.Context(), in)
	if err != nil {
		problem(w, http.StatusBadRequest, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, stored)
}

// alertSettingsTest sends one Telegram message immediately and reports the answer so
// the form can show the real API response.
func (s *Server) alertSettingsTest(w http.ResponseWriter, r *http.Request) {
	if err := s.svc.TelegramTest(r.Context()); err != nil {
		problem(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sent": true})
}

// alertFilters validates the symbol and side query parameters.
func alertFilters(rawSymbol, rawSide string) (symbol, side string, err error) {
	symbol = strings.ToUpper(strings.TrimSpace(rawSymbol))
	if len(symbol) > 32 {
		return "", "", fmt.Errorf("symbol is too long")
	}
	side = strings.ToLower(strings.TrimSpace(rawSide))
	if side == "" || side == "all" {
		side = ""
	}
	if side != "" && side != "long" && side != "short" {
		return "", "", fmt.Errorf("side must be long, short or all")
	}
	return symbol, side, nil
}

// alertLimit clamps the page size to the range the store accepts.
func alertLimit(raw string) (int, error) {
	if strings.TrimSpace(raw) == "" {
		return 50, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 1 || limit > 200 {
		return 0, fmt.Errorf("limit must be between 1 and 200")
	}
	return limit, nil
}

// alertTokenFromBody pulls the optional telegram_token out of the raw settings body.
// domain.NotificationSettings hides the token from JSON on purpose, so the submitted
// value has to be decoded without widening the struct's JSON surface.
func alertTokenFromBody(body []byte) (string, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return "", fmt.Errorf("settings body must be a JSON object")
	}
	raw, ok := fields["telegram_token"]
	if !ok || string(raw) == "null" {
		return "", nil
	}
	var token string
	if err := json.Unmarshal(raw, &token); err != nil {
		return "", fmt.Errorf("telegram_token must be a string")
	}
	if len(token) > 128 {
		return "", fmt.Errorf("telegram_token is too long")
	}
	return token, nil
}
