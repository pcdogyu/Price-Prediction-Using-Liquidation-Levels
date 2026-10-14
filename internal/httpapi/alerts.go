package httpapi

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/store"
)

//go:embed alerts.html
var alertsHTML []byte

//go:embed alerts.js
var alertsJS []byte

//go:embed alerts.css
var alertsCSS []byte

// registerAlerts exposes the liquidation alert page, its polling endpoints and the
// Telegram settings form.
func (s *Server) registerAlerts(mux *http.ServeMux) {
	mux.HandleFunc("/alerts", getOnly(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(s.renderPage(alertsHTML, "alerts", "报警"))
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
	mux.HandleFunc("/api/v1/alerts", getOnly(s.alertOverview))
	mux.HandleFunc("/api/v1/alerts/history", getOnly(s.alertHistory))
	mux.HandleFunc("/api/v1/alerts/signals", getOnly(s.alertSignals))
	mux.HandleFunc("/api/v1/alerts/settings", s.alertSettings)
	mux.HandleFunc("/api/v1/alerts/settings/test", postOnly(s.alertSettingsTest))
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
