package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/app"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/config"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/store"
)

func TestGo121CompatibleRoutes(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := app.New(config.Config{Symbols: []string{"BTCUSDT", "ETHUSDT"}}, st, log)
	srv := New(":0", svc, log)
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
	r = httptest.NewRequest(http.MethodGet, "/api/v1/market?symbol=BTCUSDT", nil)
	w = httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"source":"median_composite"`) {
		t.Fatalf("market status=%d body=%s", w.Code, w.Body.String())
	}
	r = httptest.NewRequest(http.MethodGet, "/api/v1/market?symbol=SOLUSDT", nil)
	w = httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid symbol status=%d", w.Code)
	}
	r = httptest.NewRequest(http.MethodGet, "/", nil)
	w = httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, r)
	body := w.Body.String()
	if !strings.Contains(body, "new URL('api/v1/'") || strings.Contains(body, "fetch('/api/") {
		t.Fatalf("dashboard API paths are not subpath-safe")
	}
}
