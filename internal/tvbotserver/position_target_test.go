package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tvbotbinance "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotbinance"
	tvbotconfig "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotconfig"
	tvbotokx "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotokx"
)

func requestPositionTarget(t *testing.T, srv *Server, user string) positionTargetResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/tvbot/position-target", nil)
	if user == "" {
		req.SetBasicAuth("admin", "Admin123")
	} else {
		req.Header.Set("X-Test-User", user)
	}
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("position target status=%d body=%s", rr.Code, rr.Body.String())
	}
	var target positionTargetResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &target); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(rr.Body.Bytes(), []byte("secret")) || bytes.Contains(rr.Body.Bytes(), []byte("key")) {
		t.Fatalf("position target leaked credential material: %s", rr.Body.String())
	}
	return target
}

func configureTestOKX(t *testing.T, store *tvbotokx.CredentialStore, id string) {
	t.Helper()
	if _, err := store.UpdateAccount(tvbotokx.CredentialAccountUpdate{
		ID: id, Active: true,
		Credentials: tvbotokx.Credentials{APIKey: id + "-key", SecretKey: id + "-secret", Passphrase: id + "-pass"},
	}); err != nil {
		t.Fatal(err)
	}
}

func configureTestBinance(t *testing.T, store *tvbotbinance.CredentialStore, id string) {
	t.Helper()
	if _, err := store.UpdateAccount(tvbotbinance.CredentialAccountUpdate{
		ID: id, Active: true,
		Credentials: tvbotbinance.Credentials{APIKey: id + "-key", SecretKey: id + "-secret"},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPositionTargetSelection(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, *Server)
		want  positionTargetResponse
	}{
		{name: "no api", want: positionTargetResponse{}},
		{
			name:  "single demo",
			setup: func(t *testing.T, srv *Server) { configureTestOKX(t, srv.OKXCredentials, "demo") },
			want:  positionTargetResponse{Unique: true, Exchange: "okx", Env: tvbotconfig.EnvDemo},
		},
		{
			name: "single live",
			setup: func(t *testing.T, srv *Server) {
				live, err := tvbotokx.NewCredentialStore(filepath.Join(t.TempDir(), "okx-live.json"), tvbotokx.Credentials{})
				if err != nil {
					t.Fatal(err)
				}
				srv.OKXLiveCredentials = live
				configureTestOKX(t, live, "live")
			},
			want: positionTargetResponse{Unique: true, Exchange: "okx", Env: tvbotconfig.EnvLive},
		},
		{
			name: "live wins over demo",
			setup: func(t *testing.T, srv *Server) {
				configureTestOKX(t, srv.OKXCredentials, "demo")
				live, err := tvbotokx.NewCredentialStore(filepath.Join(t.TempDir(), "okx-live.json"), tvbotokx.Credentials{})
				if err != nil {
					t.Fatal(err)
				}
				srv.OKXLiveCredentials = live
				configureTestOKX(t, live, "live")
			},
			want: positionTargetResponse{Unique: true, Exchange: "okx", Env: tvbotconfig.EnvLive},
		},
		{
			name: "multiple exchanges in preferred environment",
			setup: func(t *testing.T, srv *Server) {
				configureTestOKX(t, srv.OKXCredentials, "demo-okx")
				configureTestBinance(t, srv.BinanceCredentials, "demo-binance")
			},
			want: positionTargetResponse{},
		},
		{
			name: "incomplete credentials are ignored",
			setup: func(t *testing.T, srv *Server) {
				path := filepath.Join(t.TempDir(), "partial-okx.json")
				if err := os.WriteFile(path, []byte(`{"credentials":[{"id":"partial","api_key":"partial-key","secret_key":""}]}`), 0o600); err != nil {
					t.Fatal(err)
				}
				partial, err := tvbotokx.NewCredentialStore(path, tvbotokx.Credentials{})
				if err != nil {
					t.Fatal(err)
				}
				srv.OKXCredentials = partial
			},
			want: positionTargetResponse{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newTestServer(t)
			if tt.setup != nil {
				tt.setup(t, srv)
			}
			if got := requestPositionTarget(t, srv, ""); got != tt.want {
				t.Fatalf("target=%#v want=%#v", got, tt.want)
			}
		})
	}
}

func TestPositionTargetIsolatedByAuthenticatedUser(t *testing.T) {
	srv := newTestServer(t)
	root := t.TempDir()
	alice, err := tvbotokx.NewCredentialStore(filepath.Join(root, "alice-okx.json"), tvbotokx.Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	configureTestOKX(t, alice, "alice")
	bobBinance, err := tvbotbinance.NewCredentialStore(filepath.Join(root, "bob-binance.json"), tvbotbinance.Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	configureTestBinance(t, bobBinance, "bob")
	srv.UserCredentialResolver = testUserCredentialResolver{
		"alice": {OKX: alice},
		"bob":   {Binance: bobBinance},
	}
	srv.RequireUnifiedUser = func(http.ResponseWriter, *http.Request) bool { return true }
	srv.RequireUnifiedAdmin = func(http.ResponseWriter, *http.Request) bool { return false }
	srv.CurrentUser = func(r *http.Request) (string, bool) { return r.Header.Get("X-Test-User"), true }

	if got := requestPositionTarget(t, srv, "alice"); got != (positionTargetResponse{Unique: true, Exchange: "okx", Env: tvbotconfig.EnvDemo}) {
		t.Fatalf("alice target=%#v", got)
	}
	if got := requestPositionTarget(t, srv, "bob"); got != (positionTargetResponse{Unique: true, Exchange: "binance", Env: tvbotconfig.EnvDemo}) {
		t.Fatalf("bob target=%#v", got)
	}
}

func TestPositionsExplicitLiveEnvironmentUsesLiveCredentialStore(t *testing.T) {
	srv := newTestServer(t)
	live, err := tvbotokx.NewCredentialStore(filepath.Join(t.TempDir(), "okx-live.json"), tvbotokx.Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	configureTestOKX(t, live, "live-api")
	srv.OKXLiveCredentials = live

	var seenAPIKey string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if key := r.Header.Get("OK-ACCESS-KEY"); key != "" {
			seenAPIKey = key
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[]}`))
	}))
	defer upstream.Close()
	cfg := srv.ConfigStore.Get()
	cfg.Trading.BaseURL = upstream.URL
	srv.ConfigStore = tvbotconfig.NewStore("", cfg)
	srv.OKXHTTPClient = upstream.Client()

	req := httptest.NewRequest(http.MethodGet, "/tvbot/positions?exchange=okx&env=live&api_id=live-api", nil)
	req.SetBasicAuth("admin", "Admin123")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("live positions status=%d body=%s", rr.Code, rr.Body.String())
	}
	if seenAPIKey != "live-api-key" {
		t.Fatalf("positions used API key %q instead of live credential: %s", seenAPIKey, rr.Body.String())
	}
}

func TestRequestedTradeEnvironmentIsStrictAndLegacyCompatible(t *testing.T) {
	cfg := tvbotconfig.Default()
	cfg.Trading.Env = tvbotconfig.EnvLive
	legacy, err := configForRequestedTradeEnv(cfg, "")
	if err != nil || legacy.Trading.Env != tvbotconfig.EnvLive {
		t.Fatalf("legacy environment=%q err=%v", legacy.Trading.Env, err)
	}
	demo, err := configForRequestedTradeEnv(cfg, "demo")
	if err != nil || demo.Trading.Env != tvbotconfig.EnvDemo {
		t.Fatalf("demo environment=%q err=%v", demo.Trading.Env, err)
	}
	if _, err := configForRequestedTradeEnv(cfg, "sandbox"); err == nil || !strings.Contains(err.Error(), "demo") {
		t.Fatalf("invalid environment error=%v", err)
	}
}
