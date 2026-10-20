package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotconfig"
)

func TestFetchGateInstrumentSetBuildsUSDTCatalogAndTurnoverRanking(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v4/futures/usdt/contracts":
			_, _ = w.Write([]byte(`[
                {"name":"ETH_USDT","type":"direct","quanto_multiplier":"0.01","order_size_min":"1","order_size_max":"100000","order_price_round":"0.01","status":"trading","enable_decimal":false},
                {"name":"BTC_USDT","type":"direct","quanto_multiplier":"0.0001","order_size_min":"1","order_size_max":"100000","order_price_round":"0.1","status":"trading","enable_decimal":false},
                {"name":"USDC_USDT","type":"direct","quanto_multiplier":"1","order_size_min":"1","order_size_max":"100000","order_price_round":"0.01","status":"trading","enable_decimal":false},
                {"name":"OLD_USDT","type":"direct","status":"delisted"},
                {"name":"BTC_USDC","type":"direct","status":"trading"}
            ]`))
		case "/api/v4/futures/usdt/tickers":
			_, _ = w.Write([]byte(`[
                {"contract":"ETH_USDT","last":"3000","volume_24h_quote":"100"},
                {"contract":"BTC_USDT","last":"60000","volume_24h_quote":"200"},
                {"contract":"USDC_USDT","last":"1","volume_24h_quote":"999"},
                {"contract":"OLD_USDT","last":"1","volume_24h_quote":"300"}
            ]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	srv := newTestServer(t)
	cfg := srv.ConfigStore.Get()
	cfg.Trading.GateDemoBaseURL = server.URL
	srv.ConfigStore = config.NewStore("", cfg)
	srv.GateHTTPClient = server.Client()

	set := srv.fetchGateInstrumentSet(context.Background(), cfg, true)
	if set.Error != "" || set.TickerError != "" {
		t.Fatalf("Gate catalog errors: %#v", set)
	}
	if set.Env != config.EnvDemo || !set.Demo || set.Count != 4 || len(set.Instruments) != 4 {
		t.Fatalf("unexpected Gate catalog: %#v", set)
	}
	if set.Instruments[0].Symbol != "BTC_USDT" || set.Instruments[0].BaseAsset != "BTC" || set.Instruments[0].TickSize != "0.1" {
		t.Fatalf("Gate instrument fields were not mapped: %#v", set.Instruments[0])
	}
	top := topGateInstruments(set.Instruments, 2)
	if len(top) != 2 || top[0].Symbol != "BTC_USDT" || top[1].Symbol != "ETH_USDT" {
		t.Fatalf("unexpected Gate turnover ranking: %#v", top)
	}
	if len(topGateInstruments(set.Instruments, 2)) != 2 {
		t.Fatal("Gate ranking limit was not applied")
	}
}
