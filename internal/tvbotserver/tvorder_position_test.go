package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotbinance"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotconfig"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotstorage"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

func TestTVOrderTPSLSignalExecutesBinanceLimitClose(t *testing.T) {
	cases := []struct {
		name           string
		positionAmount string
		orderIntent    string
		action         trading.Side
		positionSide   string
		orderSide      string
		orderPrice     string
		cooldownSource string
	}{
		{
			name:           "stop loss short",
			positionAmount: "-221512",
			orderIntent:    "sl_short",
			action:         trading.ActionLong,
			positionSide:   trading.PositionSideShort,
			orderSide:      "BUY",
			orderPrice:     "0.022631",
			cooldownSource: "stop_loss_webhook",
		},
		{
			name:           "take profit long",
			positionAmount: "221512",
			orderIntent:    "tp_long",
			action:         trading.ActionShort,
			positionSide:   trading.PositionSideLong,
			orderSide:      "SELL",
			orderPrice:     "0.022629",
			cooldownSource: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			testTVOrderTPSLSignalExecutesBinanceLimitClose(t, tc.positionAmount, tc.orderIntent, tc.action, tc.positionSide, tc.orderSide, tc.orderPrice, tc.cooldownSource)
		})
	}
}

func testTVOrderTPSLSignalExecutesBinanceLimitClose(t *testing.T, positionAmount, orderIntent string, action trading.Side, positionSide, expectedOrderSide, expectedOrderPrice, cooldownSource string) {
	t.Helper()
	withSlowPositionCloseWatcher(t)

	srv := newTestServer(t)
	var mu sync.Mutex
	var orderForms []url.Values
	binanceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/fapi/v1/exchangeInfo" && r.URL.Path != "/fapi/v1/ticker/bookTicker" && r.Header.Get("X-MBX-APIKEY") != "binance-key" {
			t.Fatalf("missing Binance API key for %s", r.URL.Path)
		}
		switch r.URL.Path {
		case "/fapi/v3/positionRisk":
			if r.URL.Query().Get("symbol") != "ESPORTSUSDT" {
				t.Fatalf("bad positions query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`[{"symbol":"ESPORTSUSDT","positionSide":"BOTH","positionAmt":"` + positionAmount + `","entryPrice":"0.02261","markPrice":"0.02264","unRealizedProfit":"8.86","liquidationPrice":"0.03","isolatedMargin":"508.81","notional":"5013.06","marginAsset":"USDT","leverage":"10","marginType":"isolated","updateTime":1784880000000}]`))
		case "/fapi/v1/exchangeInfo":
			_, _ = w.Write([]byte(`{"symbols":[{"symbol":"ESPORTSUSDT","status":"TRADING","pricePrecision":6,"quantityPrecision":0,"filters":[{"filterType":"PRICE_FILTER","tickSize":"0.000001"},{"filterType":"LOT_SIZE","minQty":"1","stepSize":"1"}]}]}`))
		case "/fapi/v1/ticker/bookTicker":
			if r.URL.Query().Get("symbol") != "ESPORTSUSDT" {
				t.Fatalf("bad book ticker query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"symbol":"ESPORTSUSDT","bidPrice":"0.022620","bidQty":"1","askPrice":"0.022640","askQty":"1","time":1784880000000}`))
		case "/fapi/v1/order":
			if r.Method != http.MethodPost {
				t.Fatalf("unexpected Binance order method %s", r.Method)
			}
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			orderForms = append(orderForms, cloneValues(r.Form))
			mu.Unlock()
			_, _ = w.Write([]byte(`{"orderId":900,"symbol":"ESPORTSUSDT","status":"NEW","clientOrderId":"close-webhook","price":"0.022631","origQty":"221512","executedQty":"0","type":"LIMIT","side":"BUY","positionSide":"BOTH"}`))
		default:
			t.Fatalf("unexpected Binance path %s", r.URL.Path)
		}
	}))
	defer binanceServer.Close()
	configureBinanceCloseTestServer(t, srv, binanceServer)
	cfg := srv.ConfigStore.Get()
	cfg.Trading.IgnoredCoinpairs = []string{"sports"}
	srv.ConfigStore = config.NewStore("", cfg)

	signal := validSignal(t, srv)
	signal.TargetExchange = trading.ExchangeBinance
	signal.APIID = "tvbot"
	signal.Exchange = "BINANCE"
	signal.Coinpair = "ESPORTSUSDT.P"
	signal.Ticker = "ESPORTSUSDT.P"
	signal.Price = trading.NewFlexibleFloat(0.02264)
	signal.Action = action
	signal.OrderIntent = orderIntent
	signal.Token = srv.Token.Generate(signal.CanonicalWebhookTokenPayload())
	resp := postTVOrderSignal(t, srv, signal)
	if resp.Status != "accepted" {
		t.Fatalf("matching close signal should not be filtered: %#v", resp)
	}
	rec := waitOrderStatus(t, srv.Orders, resp.SignalID, storage.StatusSubmitted)

	select {
	case got := <-srv.Executor.(fakeExecutor).calls:
		t.Fatalf("close signal should not use opening executor: %#v", got)
	case <-time.After(50 * time.Millisecond):
	}
	if rec.PositionEffect != trading.PositionEffectClose || rec.PositionSide != positionSide || rec.OrderIntent != orderIntent {
		t.Fatalf("close semantics not stored: %#v", rec)
	}
	if rec.Result.PositionEffect != trading.PositionEffectClose || rec.Result.PositionSide != positionSide || rec.Result.TargetExchange != trading.ExchangeBinance {
		t.Fatalf("close result semantics not stored: %#v", rec.Result)
	}
	blocks, err := srv.Orders.ListActiveCoinpairBlocks(srv.now())
	if err != nil {
		t.Fatal(err)
	}
	if cooldownSource == "" {
		if len(blocks) != 0 {
			t.Fatalf("take-profit webhook must not create cooldown: %#v", blocks)
		}
	} else if len(blocks) != 1 || blocks[0].Keyword != "ESPORTS" || blocks[0].TriggerPrice != expectedOrderPrice || blocks[0].Source != cooldownSource || !blocks[0].ExpiresAt.Equal(srv.now().Add(24*time.Hour)) {
		t.Fatalf("submitted stop-loss webhook did not create default cooldown: %#v", blocks)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(orderForms) != 1 {
		t.Fatalf("expected one Binance close order, got %#v", orderForms)
	}
	form := orderForms[0]
	if form.Get("symbol") != "ESPORTSUSDT" ||
		form.Get("side") != expectedOrderSide ||
		form.Get("type") != "LIMIT" ||
		form.Get("timeInForce") != "GTC" ||
		form.Get("quantity") != "221512" ||
		form.Get("price") != expectedOrderPrice ||
		form.Get("reduceOnly") != "true" ||
		form.Get("positionSide") != "" {
		t.Fatalf("bad Binance close form: %#v", form)
	}
}

func TestTVOrderCloseSignalDoesNotCloseOppositeBinanceNetPosition(t *testing.T) {
	withSlowPositionCloseWatcher(t)

	srv := newTestServer(t)
	binanceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/fapi/v3/positionRisk" {
			t.Fatalf("unexpected Binance path %s", r.URL.Path)
		}
		if r.Header.Get("X-MBX-APIKEY") != "binance-key" {
			t.Fatalf("missing Binance API key for %s", r.URL.Path)
		}
		if r.URL.Query().Get("symbol") != "ESPORTSUSDT" {
			t.Fatalf("bad positions query: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`[{"symbol":"ESPORTSUSDT","positionSide":"BOTH","positionAmt":"10","entryPrice":"0.02261","markPrice":"0.02264","unRealizedProfit":"0.01","isolatedMargin":"1","notional":"0.2264","marginAsset":"USDT","leverage":"10","marginType":"isolated","updateTime":1784880000000}]`))
	}))
	defer binanceServer.Close()
	configureBinanceCloseTestServer(t, srv, binanceServer)

	signal := validSignal(t, srv)
	signal.TargetExchange = trading.ExchangeBinance
	signal.APIID = "tvbot"
	signal.Exchange = "BINANCE"
	signal.Coinpair = "ESPORTSUSDT.P"
	signal.Ticker = "ESPORTSUSDT.P"
	signal.Price = trading.NewFlexibleFloat(0.02264)
	signal.Condition = "空单止损"
	signal.Token = srv.Token.Generate(signal.CanonicalWebhookTokenPayload())
	resp := postTVOrderSignal(t, srv, signal)
	rec := waitOrderStatus(t, srv.Orders, resp.SignalID, storage.StatusFailed)

	select {
	case got := <-srv.Executor.(fakeExecutor).calls:
		t.Fatalf("close signal should not use opening executor: %#v", got)
	case <-time.After(50 * time.Millisecond):
	}
	if rec.ErrorCode != "position_not_open" || rec.PositionEffect != trading.PositionEffectClose || rec.PositionSide != trading.PositionSideShort {
		t.Fatalf("target short position should be reported missing: %#v", rec)
	}
	blocks, err := srv.Orders.ListActiveCoinpairBlocks(srv.now())
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 0 {
		t.Fatalf("failed stop-loss close should not create cooldown: %#v", blocks)
	}
}

func TestTVOrderCloseIntentConflictFailsBeforeExecution(t *testing.T) {
	srv := newTestServer(t)
	signal := validSignal(t, srv)
	signal.TargetExchange = trading.ExchangeBinance
	signal.APIID = "tvbot"
	signal.Action = trading.ActionLong
	signal.OrderIntent = "exit_long"
	signal.Token = srv.Token.Generate(signal.CanonicalWebhookTokenPayload())
	resp := postTVOrderSignal(t, srv, signal)
	rec := waitOrderStatus(t, srv.Orders, resp.SignalID, storage.StatusFailed)

	select {
	case got := <-srv.Executor.(fakeExecutor).calls:
		t.Fatalf("conflicting close intent should not execute: %#v", got)
	case <-time.After(50 * time.Millisecond):
	}
	if rec.ErrorCode != "invalid_position_intent" || rec.PositionEffect != trading.PositionEffectClose || rec.PositionSide != trading.PositionSideLong {
		t.Fatalf("conflicting intent should be recorded as failed close-long: %#v", rec)
	}
}

func TestTVOrderEntryIntentStillUsesOpeningExecutor(t *testing.T) {
	srv := newTestServer(t)
	signal := validSignal(t, srv)
	signal.Action = trading.ActionShort
	signal.OrderIntent = "entry_short"
	signal.Token = srv.Token.Generate(signal.CanonicalTokenPayload())
	resp := postTVOrderSignal(t, srv, signal)
	rec := waitOrderStatus(t, srv.Orders, resp.SignalID, storage.StatusSubmitted)

	select {
	case got := <-srv.Executor.(fakeExecutor).calls:
		if got.PositionEffect != trading.PositionEffectOpen || got.PositionSide != trading.PositionSideShort || got.OrderIntent != "entry_short" {
			t.Fatalf("entry intent should execute as opening short: %#v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("opening executor was not called")
	}
	if rec.PositionEffect != trading.PositionEffectOpen || rec.PositionSide != trading.PositionSideShort || rec.OrderIntent != "entry_short" {
		t.Fatalf("entry semantics not stored: %#v", rec)
	}
}

func TestTVOrderPositionSemanticsSupportExplicitTPSLIntents(t *testing.T) {
	cases := []struct {
		intent string
		action trading.Side
		side   string
	}{
		{intent: "tp_long", action: trading.ActionShort, side: trading.PositionSideLong},
		{intent: "sl_long", action: trading.ActionShort, side: trading.PositionSideLong},
		{intent: "tp_short", action: trading.ActionLong, side: trading.PositionSideShort},
		{intent: "sl_short", action: trading.ActionLong, side: trading.PositionSideShort},
	}
	for _, tc := range cases {
		t.Run(tc.intent, func(t *testing.T) {
			signal := trading.Signal{Action: tc.action, OrderIntent: tc.intent}
			if err := applyTVOrderPositionSemantics(&signal); err != nil {
				t.Fatal(err)
			}
			if signal.PositionEffect != trading.PositionEffectClose || signal.PositionSide != tc.side {
				t.Fatalf("%s should normalize to close %s: %#v", tc.intent, tc.side, signal)
			}
		})
	}
}

func withSlowPositionCloseWatcher(t *testing.T) {
	t.Helper()
	oldPoll := positionClosePollInterval
	oldTimeout := positionCloseLimitTimeout
	oldJobs := positionCloseJobs
	positionClosePollInterval = time.Hour
	positionCloseLimitTimeout = time.Hour
	positionCloseJobs = newPositionCloseRegistry()
	t.Cleanup(func() {
		positionClosePollInterval = oldPoll
		positionCloseLimitTimeout = oldTimeout
		positionCloseJobs = oldJobs
	})
}

func configureBinanceCloseTestServer(t *testing.T, srv *Server, binanceServer *httptest.Server) {
	t.Helper()
	cfg := srv.ConfigStore.Get()
	cfg.Trading.BinanceDemoBaseURL = binanceServer.URL
	srv.ConfigStore = config.NewStore("", cfg)
	srv.BinanceHTTPClient = binanceServer.Client()
	if _, err := srv.BinanceCredentials.UpdateAccount(binance.CredentialAccountUpdate{
		ID:          "tvbot",
		Active:      true,
		Credentials: binance.Credentials{APIKey: "binance-key", SecretKey: "binance-secret"},
	}); err != nil {
		t.Fatal(err)
	}
}

type tvOrderSignalResponse struct {
	Status   string `json:"status"`
	SignalID string `json:"signal_id"`
}

func postTVOrderSignal(t *testing.T, srv *Server, signal trading.Signal) tvOrderSignalResponse {
	t.Helper()
	body, err := json.Marshal(signal)
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/tvorder", bytes.NewReader(body)))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("tvorder status=%d body=%s", rr.Code, rr.Body.String())
	}
	return decodeTVOrderSignalResponse(t, rr.Body.Bytes())
}

func decodeTVOrderSignalResponse(t *testing.T, body []byte) tvOrderSignalResponse {
	t.Helper()
	var resp tvOrderSignalResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.SignalID == "" {
		t.Fatalf("tvorder response missing signal_id: %s", string(body))
	}
	return resp
}
