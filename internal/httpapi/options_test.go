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

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/authn"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/config"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

func TestOptionsEndpointWindowAndZero(t *testing.T) {
	srv, st := testService(t, config.Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	defer st.Close()
	z := 0.
	p := domain.OptionGammaPoint{Symbol: "ETHUSDT", Underlying: "ETH", Source: "deribit", Time: time.Now().UTC().Add(-time.Second), Gamma: &z, Contracts: 80, SelectedContracts: 80, EligibleContracts: 100, State: "ok"}
	if err := st.SaveOptionGamma(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	for _, hours := range []string{"", "1", "2160"} {
		w := httptest.NewRecorder()
		srv.http.Handler.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/options?hours="+hours, nil))
		var packet struct {
			Data domain.OptionsView `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &packet); err != nil || w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(w.Code, w.Body.String(), err)
		}
		if len(packet.Data.Series) != 2 || packet.Data.Series[0].Symbol != "BTCUSDT" || packet.Data.Series[0].Latest != nil || len(packet.Data.Series[1].Points) != 1 || *packet.Data.Series[1].Latest.Gamma != 0 || packet.Data.To.Sub(packet.Data.From) != time.Duration(packet.Data.Hours)*time.Hour {
			t.Fatal(packet.Data)
		}
		if strings.Contains(w.Body.String(), `"points":null`) {
			t.Fatal("missing series not an empty array")
		}
		if hours == "" && packet.Data.Hours != 24 {
			t.Fatalf("default options window=%d, want 24", packet.Data.Hours)
		}
		if packet.Data.RefreshSeconds != 300 || packet.Data.RetentionDays != 90 || packet.Data.DenseHours != 24 || packet.Data.ArchiveMinutes != 30 {
			t.Fatal(packet.Data)
		}
	}
	w := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/options", nil))
	if w.Code != 405 {
		t.Fatal(w.Code)
	}
}

func TestOptionsAssetFollowsServerCadence(t *testing.T) {
	srv, st := testService(t, config.Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	defer st.Close()
	w := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/assets/options.js", nil))
	body := w.Body.String()
	// The chart and the poller must adopt the cadence the API reports instead of
	// a hardcoded minute, otherwise the copy and the data drift apart.
	for _, marker := range []string{"cadence(data.refresh_seconds)", "cadenceSeconds=data.refresh_seconds", "scheduleRefresh()", "setInterval(() => { if (!document.hidden) load(); }, next)"} {
		if !strings.Contains(body, marker) {
			t.Fatalf("options asset lost %q", marker)
		}
	}
	if strings.Contains(body, "每分钟更新") || strings.Contains(body, "},60000)") {
		t.Fatal("options asset still hardcodes a one minute refresh")
	}
	w = httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/options", nil))
	if !strings.Contains(w.Body.String(), "每 5 分钟") || !strings.Contains(w.Body.String(), "最长保留 90 天") || !strings.Contains(w.Body.String(), "整点和半点") {
		t.Fatal("options page copy does not describe tiered retention")
	}
}

func TestOptionsAuthenticationAndSubpath(t *testing.T) {
	hash, err := authn.HashPassword("test-options-password")
	if err != nil {
		t.Fatal(err)
	}
	srv, st := testService(t, config.Config{BasePath: "/liquidation/", AuthUsername: "fixture", AuthPasswordHash: hash}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	defer st.Close()
	// Nginx strips the external base path before forwarding requests.
	for _, path := range []string{"/options", "/api/v1/options"} {
		w := httptest.NewRecorder()
		srv.http.Handler.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		want := 401
		if strings.HasSuffix(path, "/options") && !strings.Contains(path, "api/") {
			want = 200
			if !strings.Contains(w.Body.String(), "password") {
				t.Fatal("page bypassed login")
			}
		}
		if w.Code != want {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
}
