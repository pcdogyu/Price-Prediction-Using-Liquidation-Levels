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
	srv, err := New(cfg, svc, logger, logs)
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	return srv, st
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

	now := time.Now().UTC().Truncate(time.Minute)
	if err = st.UpsertCandles(context.Background(), []domain.Candle{{Exchange: "binance", Symbol: "BTCUSDT", Time: now, Open: 100, High: 102, Low: 99, Close: 101, VolumeUSD: 10}, {Exchange: "okx", Symbol: "BTCUSDT", Time: now, Open: 101, High: 103, Low: 100, Close: 102, VolumeUSD: 20}}); err != nil {
		t.Fatal(err)
	}
	if err = st.SavePrediction(context.Background(), domain.Prediction{Symbol: "BTCUSDT", Time: now, State: "ok", MarkPrice: 101, LeadingClass: domain.UpperFirst, ModelVersion: "test-v1", Probabilities: map[string]float64{domain.UpperFirst: .6}}); err != nil {
		t.Fatal(err)
	}
	r = httptest.NewRequest(http.MethodGet, "/api/v1/market?symbol=BTCUSDT", nil)
	w = httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"source":"median_composite"`) || !strings.Contains(w.Body.String(), `"side":"long"`) {
		t.Fatalf("market status=%d body=%s", w.Code, w.Body.String())
	}
	for _, interval := range []string{"1m", "2m", "3m", "5m", "10m", "15m", "30m", "1h", "4h", "8h", "12h", "24h"} {
		r = httptest.NewRequest(http.MethodGet, "/api/v1/market?symbol=BTCUSDT&limit=120&interval="+interval, nil)
		w = httptest.NewRecorder()
		srv.http.Handler.ServeHTTP(w, r)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"interval":"`+interval+`"`) {
			t.Fatalf("interval=%s status=%d body=%s", interval, w.Code, w.Body.String())
		}
	}
	r = httptest.NewRequest(http.MethodGet, "/api/v1/market?symbol=SOLUSDT", nil)
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
	r = httptest.NewRequest(http.MethodGet, "/", nil)
	w = httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, r)
	if !strings.Contains(w.Body.String(), `src="assets/dashboard.js"`) || !strings.Contains(w.Body.String(), `data-interval="24h"`) || strings.Contains(w.Body.String(), "async function refresh") {
		t.Fatal("dashboard script was not externalized")
	}
	r = httptest.NewRequest(http.MethodGet, "/assets/dashboard.js", nil)
	w = httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "new URL('api/v1/'") || !strings.Contains(w.Body.String(), "addEventListener('wheel'") || !strings.Contains(w.Body.String(), "pointerdown") {
		t.Fatalf("dashboard asset status=%d", w.Code)
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
	line, err := bufio.NewReader(streamResponse.Body).ReadString('\n')
	if err != nil || line != "event: health\n" {
		t.Fatalf("initial SSE event=%q error=%v", line, err)
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
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "登录后查看行情") {
		t.Fatalf("login page status=%d body=%s", w.Code, w.Body.String())
	}
	r = httptest.NewRequest(http.MethodGet, "/api/v1/logs", nil)
	w = httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated logs status=%d", w.Code)
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
	if w.Code != http.StatusSeeOther || response.Header.Get("Location") != "/liquidation/" {
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
	r = httptest.NewRequest(http.MethodGet, "/api/v1/logs", nil)
	r.AddCookie(session)
	w = httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("logged-out session status=%d", w.Code)
	}
}
