package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	tvbotbinance "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotbinance"
	tvbotconfig "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotconfig"
	tvbotgate "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotgate"
	tvbotokx "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotokx"
	tvbotstorage "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotstorage"
	tvbottrading "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

func TestLoadTradeAccountSnapshotCalculatesEquityForAllExchanges(t *testing.T) {
	tests := []struct {
		name       string
		exchange   string
		equity     float64
		positions  int
		pendingKey string
		handler    func(http.ResponseWriter, *http.Request)
		configure  func(*Server, *tvbotconfig.Config, string)
	}{
		{
			name:       "okx",
			exchange:   tvbottrading.ExchangeOKX,
			equity:     1000,
			positions:  1,
			pendingKey: "ETHUSDT",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/v5/account/balance":
					_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"totalEq":"1000"}]}`))
				case "/api/v5/account/positions":
					_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instId":"BTC-USDT-SWAP","posSide":"long","pos":"1","avgPx":"100"}]}`))
				case "/api/v5/trade/orders-pending":
					_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instId":"ETH-USDT-SWAP","reduceOnly":false,"closePosition":false}]}`))
				default:
					http.Error(w, "unexpected OKX path", http.StatusNotFound)
				}
			},
			configure: func(s *Server, cfg *tvbotconfig.Config, base string) {
				store, err := tvbotokx.NewCredentialStore("", tvbotokx.Credentials{APIKey: "test-key", SecretKey: "test-secret", Passphrase: "test-pass"})
				if err != nil {
					t.Fatal(err)
				}
				s.OKXCredentials = store
				cfg.Trading.BaseURL = base
			},
		},
		{
			name:       "binance",
			exchange:   tvbottrading.ExchangeBinance,
			equity:     1010,
			positions:  1,
			pendingKey: "ETHUSDT",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/fapi/v3/balance":
					_, _ = w.Write([]byte(`[{"asset":"USDT","balance":"1000","crossUnPnl":"25"}]`))
				case "/fapi/v3/positionRisk":
					_, _ = w.Write([]byte(`[{"symbol":"BTCUSDT","positionSide":"LONG","positionAmt":"1","entryPrice":"100","unRealizedProfit":"10","marginAsset":"USDT"}]`))
				case "/fapi/v1/openOrders":
					_, _ = w.Write([]byte(`[{"symbol":"ETHUSDT","reduceOnly":false,"closePosition":false}]`))
				default:
					http.Error(w, "unexpected Binance path", http.StatusNotFound)
				}
			},
			configure: func(s *Server, cfg *tvbotconfig.Config, base string) {
				store, err := tvbotbinance.NewCredentialStore("", tvbotbinance.Credentials{APIKey: "test-key", SecretKey: "test-secret"})
				if err != nil {
					t.Fatal(err)
				}
				s.BinanceCredentials = store
				cfg.Trading.BinanceBaseURL = base
				cfg.Trading.BinanceDemoBaseURL = base
			},
		},
		{
			name:       "gate",
			exchange:   tvbottrading.ExchangeGate,
			equity:     1025,
			positions:  1,
			pendingKey: "ETHUSDT",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/v4/futures/usdt/accounts":
					_, _ = w.Write([]byte(`{"currency":"USDT","total":"1000","unrealised_pnl":"25"}`))
				case "/api/v4/futures/usdt/positions":
					_, _ = w.Write([]byte(`[{"contract":"BTC_USDT","size":"1","entry_price":"100"}]`))
				case "/api/v4/futures/usdt/orders":
					_, _ = w.Write([]byte(`[{"contract":"ETH_USDT","size":"1","is_reduce_only":false,"is_close":false}]`))
				default:
					http.Error(w, "unexpected Gate path", http.StatusNotFound)
				}
			},
			configure: func(s *Server, cfg *tvbotconfig.Config, base string) {
				store, err := tvbotgate.NewCredentialStore("", tvbotgate.Credentials{APIKey: "test-key", SecretKey: "test-secret"})
				if err != nil {
					t.Fatal(err)
				}
				s.GateCredentials = store
				cfg.Trading.GateBaseURL = base
				cfg.Trading.GateDemoBaseURL = base
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			httpServer := httptest.NewServer(http.HandlerFunc(test.handler))
			defer httpServer.Close()
			cfg := tvbotconfig.Default()
			cfg.Trading.Env = tvbotconfig.EnvDemo
			store, err := tvbotstorage.NewOrderStore("")
			if err != nil {
				t.Fatal(err)
			}
			s := &Server{Orders: store, OKXHTTPClient: httpServer.Client(), BinanceHTTPClient: httpServer.Client(), GateHTTPClient: httpServer.Client()}
			test.configure(s, &cfg, httpServer.URL)
			snapshot, err := s.loadTradeAccountSnapshot(context.Background(), cfg, tvbottrading.Signal{TargetExchange: test.exchange, TradeEnv: tvbottrading.TradeEnvDemo}, true, true, true)
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.Equity != test.equity || snapshot.EquityErr != nil || len(snapshot.Positions) != test.positions {
				t.Fatalf("unexpected snapshot: equity=%v equity_err=%v positions=%#v", snapshot.Equity, snapshot.EquityErr, snapshot.Positions)
			}
			if _, ok := snapshot.PendingSymbols[test.pendingKey]; !ok {
				t.Fatalf("pending symbol %q missing: %#v", test.pendingKey, snapshot.PendingSymbols)
			}
		})
	}
}

func TestActiveEntrySymbolsIncludesAcceptedSubmittedAndPartialOrders(t *testing.T) {
	store, err := tvbotstorage.NewOrderStore("")
	if err != nil {
		t.Fatal(err)
	}
	now := fixedAdmissionTestTime()
	server := &Server{Orders: store}
	for index, status := range []tvbotstorage.OrderStatus{tvbotstorage.StatusAccepted, tvbotstorage.StatusSubmitted, tvbotstorage.StatusPartial} {
		signal := tvbottrading.Signal{
			Action:         tvbottrading.ActionLong,
			TargetExchange: tvbottrading.ExchangeBinance,
			TradeEnv:       tvbottrading.TradeEnvDemo,
			APIID:          "account-a",
			Coinpair:       []string{"BTCUSDT", "ETHUSDT", "SOLUSDT"}[index],
			PositionEffect: tvbottrading.PositionEffectOpen,
			PositionSide:   tvbottrading.PositionSideLong,
			Price:          tvbottrading.NewFlexibleFloat(100),
			Amount:         tvbottrading.NewFlexibleFloat(10),
		}
		record, _, err := store.RecordAccepted(signal, string(signal.Coinpair), now.Add(time.Duration(index)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		switch status {
		case tvbotstorage.StatusSubmitted:
			if err := store.MarkSubmitted(record.SignalID, tvbottrading.OrderResult{OrdID: "submitted"}, now); err != nil {
				t.Fatal(err)
			}
		case tvbotstorage.StatusPartial:
			if err := store.MarkPartiallySubmitted(record.SignalID, tvbottrading.OrderResult{OrdID: "partial"}, context.Canceled, now); err != nil {
				t.Fatal(err)
			}
		}
	}
	symbols, err := server.activeAcceptedEntrySymbols(tvbottrading.Signal{TargetExchange: tvbottrading.ExchangeBinance, TradeEnv: tvbottrading.TradeEnvDemo}, "account-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(symbols) != 3 {
		t.Fatalf("active entry symbols=%#v", symbols)
	}
}

func TestRecordPyramidEntryRequiresSubmittedOrder(t *testing.T) {
	store, err := tvbotstorage.NewOrderStore("")
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{Orders: store}
	signal := tvbottrading.Signal{
		TargetExchange: tvbottrading.ExchangeGate,
		TradeEnv:       tvbottrading.TradeEnvDemo,
		APIID:          "account-a",
		Coinpair:       "BTC_USDT",
		Action:         tvbottrading.ActionLong,
		PositionSide:   tvbottrading.PositionSideLong,
		Price:          tvbottrading.NewFlexibleFloat(105),
		PyramidEnabled: true,
		PyramidLevel:   1,
	}
	now := func() time.Time { return fixedAdmissionTestTime() }
	server.recordPyramidEntry(signal, tvbottrading.OrderResult{}, now)
	if _, found, err := store.GetPyramidState(tvbotstorage.PyramidStateKey{TargetExchange: tvbottrading.ExchangeGate, TradeEnv: tvbottrading.TradeEnvDemo, APIID: "account-a", Coinpair: "BTCUSDT", PositionSide: "long"}); err != nil || found {
		t.Fatalf("failed order should not advance pyramid state: found=%v err=%v", found, err)
	}
	server.recordPyramidEntry(signal, tvbottrading.OrderResult{OrdID: "gate-order"}, now)
	state, found, err := store.GetPyramidState(tvbotstorage.PyramidStateKey{TargetExchange: tvbottrading.ExchangeGate, TradeEnv: tvbottrading.TradeEnvDemo, APIID: "account-a", Coinpair: "BTCUSDT", PositionSide: "long"})
	if err != nil || !found || state.Level != 1 {
		t.Fatalf("submitted order should persist pyramid state: state=%#v found=%v err=%v", state, found, err)
	}
}

func fixedAdmissionTestTime() time.Time {
	return time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
}
