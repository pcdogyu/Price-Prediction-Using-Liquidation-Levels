package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
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
	for _, hours := range []string{"", "1", "72"} {
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
		if packet.Data.RefreshSeconds != 60 || packet.Data.RetentionDays != 3 {
			t.Fatal(packet.Data)
		}
	}
	w := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/options", nil))
	if w.Code != 405 {
		t.Fatal(w.Code)
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
