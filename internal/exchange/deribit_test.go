package exchange

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type gammaContract struct {
	name  string
	oi    float64
	gamma *float64
	bad   string
}

func TestNewDeribitClientUsesTickerWebSocket(t *testing.T) {
	client := NewDeribitClient()
	if client.Spacing < 500*time.Millisecond {
		t.Fatalf("Deribit REST spacing=%s, want at least 500ms", client.Spacing)
	}
	if !strings.HasPrefix(client.WebSocketURL, "wss://") {
		t.Fatalf("Deribit WebSocket URL=%q, want wss://", client.WebSocketURL)
	}
}

func gammaFixture(t *testing.T, contracts []gammaContract) (*DeribitClient, *[]string) {
	t.Helper()
	requests := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "get_book_summary_by_currency") {
			if r.URL.Query().Get("kind") != "option" {
				t.Error("missing option filter")
			}
			rows := []map[string]any{}
			for _, c := range contracts {
				rows = append(rows, map[string]any{"instrument_name": c.name, "open_interest": c.oi})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"result": rows})
			return
		}
		if r.URL.Path != "/ws/api/v2" {
			http.NotFound(w, r)
			return
		}
		connection, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer connection.Close()
		var request struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
			Params struct {
				Channels []string `json:"channels"`
			} `json:"params"`
		}
		if err = connection.ReadJSON(&request); err != nil {
			t.Error(err)
			return
		}
		if request.Method != "public/subscribe" || len(request.Params.Channels) == 0 {
			t.Errorf("invalid subscription request: %+v", request)
			return
		}
		for _, c := range contracts {
			if c.bad == "rate" {
				_ = connection.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": 10028, "message": "too_many_requests"}})
				return
			}
		}
		_ = connection.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": request.Params.Channels})
		for _, channel := range request.Params.Channels {
			name := strings.TrimSuffix(strings.TrimPrefix(channel, "ticker."), ".100ms")
			requests = append(requests, name)
			var contract *gammaContract
			for i := range contracts {
				if contracts[i].name == name {
					contract = &contracts[i]
					break
				}
			}
			if contract == nil {
				continue
			}
			at := time.Now()
			if contract.bad == "stale" {
				at = at.Add(-3 * time.Minute)
			}
			data := map[string]any{"instrument_name": name, "timestamp": at.UnixMilli()}
			if contract.bad != "http" {
				data["greeks"] = map[string]any{"gamma": contract.gamma}
			}
			if err = connection.WriteJSON(map[string]any{"jsonrpc": "2.0", "method": "subscription", "params": map[string]any{"channel": channel, "data": data}}); err != nil {
				t.Error(err)
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	return &DeribitClient{HTTP: server.Client(), BaseURL: server.URL, WebSocketURL: "ws" + strings.TrimPrefix(server.URL, "http") + "/ws/api/v2"}, &requests
}

func gammaName(currency, side string, strike int) string {
	return fmt.Sprintf("%s-%s-%d-%s", currency, strings.ToUpper(time.Now().AddDate(0, 1, 0).Format("2Jan06")), strike, side)
}

func TestDeribitGammaMatchesReferenceWithoutOIWeighting(t *testing.T) {
	call, put := .00002, .00001
	for _, currency := range []string{"BTC", "ETH"} {
		client, _ := gammaFixture(t, []gammaContract{{gammaName(currency, "C", 3000), 2, &call, ""}, {gammaName(currency, "P", 3000), 3, &put, ""}})
		p, err := client.Gamma(context.Background(), currency, time.Now())
		if err != nil || p.Gamma == nil || math.Abs(*p.Gamma-1.0/3) > 1e-12 || p.Contracts != 2 || p.State != "ok" || p.SourceTo.IsZero() || p.Time.Before(p.SourceTo) {
			t.Fatalf("currency=%s point=%+v err=%v", currency, p, err)
		}
	}
}

func TestDeribitGammaSignsAndMissingSnapshots(t *testing.T) {
	positive, zero, negative := .1, 0., -.1
	for _, tc := range []struct {
		name, bad string
		call, put *float64
		want      float64
		state     string
		fail      bool
	}{
		{"positive", "", &positive, nil, 1, "partial", false},
		{"negative", "", nil, &positive, -1, "partial", false},
		{"balanced", "", &positive, &positive, 0, "ok", false},
		{"zero denominator", "", &zero, &zero, 0, "", true},
		{"missing gamma", "", nil, nil, 0, "", true},
		{"invalid gamma", "", &negative, nil, 0, "", true},
		{"stale", "stale", &positive, &positive, 0, "", true},
		{"one missing ticker value", "http", &positive, &positive, 1, "partial", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cs := []gammaContract{{gammaName("ETH", "C", 3000), 3, tc.call, ""}, {gammaName("ETH", "P", 3000), 2, tc.put, tc.bad}}
			if tc.bad == "stale" {
				cs[0].bad = "stale"
			}
			client, requests := gammaFixture(t, cs)
			p, err := client.Gamma(context.Background(), "ETH", time.Now())
			if tc.fail {
				if err == nil || p.Gamma != nil {
					t.Fatalf("missing data became valid: %+v %v", p, err)
				}
				return
			}
			if err != nil || p.Gamma == nil || *p.Gamma != tc.want || p.State != tc.state {
				t.Fatalf("point=%+v err=%v", p, err)
			}
			if tc.state == "partial" && p.Warning == "" {
				t.Fatal("missing partial warning")
			}
			if len(*requests) != len(cs) {
				t.Fatalf("received ticker snapshots=%d, want %d", len(*requests), len(cs))
			}
		})
	}
}

func TestDeribitGammaSubscriptionRateLimit(t *testing.T) {
	g := .1
	cs := []gammaContract{{gammaName("ETH", "C", 3000), 3, &g, "rate"}}
	client, _ := gammaFixture(t, cs)
	p, err := client.Gamma(context.Background(), "ETH", time.Now())
	if err == nil || !strings.Contains(err.Error(), "10028") || p.Gamma != nil {
		t.Fatalf("subscription rate limit was not surfaced: point=%+v err=%v", p, err)
	}
}

func TestDeribitGammaSelectsTop80AndRejectsExpired(t *testing.T) {
	g := .001
	cs := []gammaContract{{"BTC-1JAN20-100-C", 100000, &g, ""}, {gammaName("ETH", "C", 3000), 100000, &g, ""}, {"BTC-PERPETUAL", 100000, &g, ""}}
	for i := 1; i <= 85; i++ {
		cs = append(cs, gammaContract{gammaName("BTC", "C", i), float64(i), &g, ""})
	}
	cs = append(cs, cs[len(cs)-1]) // Duplicate summary must not consume a sample slot.
	client, requests := gammaFixture(t, cs)
	p, err := client.Gamma(context.Background(), "BTC", time.Now())
	if err != nil || p.Contracts != 80 || p.SelectedContracts != 80 || p.EligibleContracts != 85 || *p.Gamma != 1 || len(*requests) != 80 {
		t.Fatalf("point=%+v err=%v requests=%d", p, err, len(*requests))
	}
	if (*requests)[0] != gammaName("BTC", "C", 85) || (*requests)[79] != gammaName("BTC", "C", 6) {
		t.Fatal("samples were not ranked by OI")
	}
	client, _ = gammaFixture(t, nil)
	if p, err = client.Gamma(context.Background(), "BTC", time.Now()); err == nil || p.Gamma != nil {
		t.Fatal("empty chain became zero")
	}
}

func TestDeribitGammaCancellation(t *testing.T) {
	g := .1
	client, requests := gammaFixture(t, []gammaContract{{gammaName("BTC", "C", 3000), 1, &g, ""}})
	client.next = time.Now().Add(time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Gamma(ctx, "BTC", time.Now()); err != context.Canceled || len(*requests) != 0 {
		t.Fatalf("cancel error=%v requests=%d", err, len(*requests))
	}
}
