package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotbinance"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotconfig"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotstorage"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

func TestCoinpairCooldownIdentityUsesFuzzyBaseKeyword(t *testing.T) {
	cases := map[string]string{
		"ETHUSDT":          "ETH",
		"ETH-USDT-SWAP":    "ETH",
		"OKX:ETHUSDT.P":    "ETH",
		"1000PEPEUSDT":     "1000PEPE",
		"BINANCE:BTCFDUSD": "BTC",
	}
	for input, want := range cases {
		got, _ := coinpairCooldownIdentity(input)
		if got != want {
			t.Fatalf("identity(%q)=%q want %q", input, got, want)
		}
	}
}

func TestNegativeRealizedPnLClassification(t *testing.T) {
	for raw, want := range map[string]bool{
		"-0.0001": true,
		"-5":      true,
		"0":       false,
		"2.5":     false,
		"invalid": false,
	} {
		if got := negativeRealizedPnL(raw); got != want {
			t.Fatalf("negativeRealizedPnL(%q)=%v want %v", raw, got, want)
		}
	}
}

func TestConfiguredLossCooldownDurationAndDisable(t *testing.T) {
	srv := newTestServer(t)
	now := srv.now()
	cfg := config.Default()
	cfg.Trading.LossCooldown.CoinpairHours = 3
	block, created, err := srv.recordLossCoinpairCooldown(cfg, "loss:1", "exchange_fill", trading.ExchangeOKX, "main", "2500", now, "ETHUSDT")
	if err != nil || !created || !block.ExpiresAt.Equal(now.Add(3*time.Hour)) {
		t.Fatalf("custom loss cooldown was not created: created=%v block=%#v err=%v", created, block, err)
	}

	cfg.Trading.LossCooldown.Enabled = false
	if block, created, err := srv.recordLossCoinpairCooldown(cfg, "loss:2", "exchange_fill", trading.ExchangeOKX, "main", "2600", now, "BTCUSDT"); err != nil || created || block.Keyword != "" {
		t.Fatalf("disabled loss cooldown should not create a block: created=%v block=%#v err=%v", created, block, err)
	}
}

func TestLossCooldownUsesSeparateScopeDurations(t *testing.T) {
	srv := newTestServer(t)
	now := srv.now()
	cfg := config.Default()
	cfg.Trading.LossCooldown.CoinpairHours = 3
	cfg.Trading.LossCooldown.GlobalHours = 5
	cfg.Trading.LossCooldown.GlobalEnabled = true
	srv.ConfigStore = config.NewStore("", cfg)

	if _, created, err := srv.recordLossCoinpairCooldown(cfg, "split-loss:1", "exchange_fill", trading.ExchangeBinance, "main", "49000", now, "BTCUSDT"); err != nil || !created {
		t.Fatalf("separate scope cooldowns were not created: created=%v err=%v", created, err)
	}
	blocks, err := srv.Orders.ListActiveCoinpairBlocks(now)
	if err != nil || len(blocks) != 2 {
		t.Fatalf("unexpected separate scope cooldown blocks: blocks=%#v err=%v", blocks, err)
	}
	for _, block := range blocks {
		want := 3 * time.Hour
		if block.Keyword == globalCoinpairCooldownKeyword {
			want = 5 * time.Hour
		}
		if !block.ExpiresAt.Equal(now.Add(want)) {
			t.Fatalf("cooldown %q used %s, want %s: %#v", block.Keyword, block.ExpiresAt.Sub(now), want, block)
		}
	}
}

func TestConfiguredGlobalLossCooldownBlocksEveryCoinpair(t *testing.T) {
	srv := newTestServer(t)
	now := srv.now()
	cfg := config.Default()
	cfg.Trading.LossCooldown.Enabled = false
	cfg.Trading.LossCooldown.GlobalEnabled = true
	cfg.Trading.LossCooldown.GlobalHours = 5
	srv.ConfigStore = config.NewStore("", cfg)

	block, created, err := srv.recordLossCoinpairCooldown(cfg, "global-loss:1", "exchange_fill", trading.ExchangeBinance, "main", "49000", now, "BTCUSDT")
	if err != nil || !created || block.Keyword != globalCoinpairCooldownKeyword || !block.ExpiresAt.Equal(now.Add(5*time.Hour)) {
		t.Fatalf("global loss cooldown was not created: created=%v block=%#v err=%v", created, block, err)
	}
	blocks, err := srv.Orders.ListActiveCoinpairBlocks(now)
	if err != nil || len(blocks) != 1 || blocks[0].Keyword != globalCoinpairCooldownKeyword {
		t.Fatalf("unexpected global loss cooldown blocks: blocks=%#v err=%v", blocks, err)
	}
	for _, symbol := range []string{"BTCUSDT", "ETHUSDT", "SOLUSDT"} {
		probe := trading.Signal{Coinpair: symbol, Ticker: "BINANCE:" + symbol, PositionEffect: trading.PositionEffectOpen}
		if matched, blocked, err := srv.activeCoinpairCooldown(probe, now); err != nil || !blocked || matched.Keyword != globalCoinpairCooldownKeyword {
			t.Fatalf("global cooldown did not block %s: blocked=%v block=%#v err=%v", symbol, blocked, matched, err)
		}
	}
	closeProbe := trading.Signal{Coinpair: "ETHUSDT", PositionEffect: trading.PositionEffectClose}
	if _, blocked, err := srv.activeCoinpairCooldown(closeProbe, now); err != nil || blocked {
		t.Fatalf("global cooldown should not block close signals: blocked=%v err=%v", blocked, err)
	}

	cfg.Trading.LossCooldown.GlobalEnabled = false
	srv.ConfigStore = config.NewStore("", cfg)
	if _, blocked, err := srv.activeCoinpairCooldown(trading.Signal{Coinpair: "BTCUSDT", PositionEffect: trading.PositionEffectOpen}, now); err != nil || blocked {
		t.Fatalf("disabled global cooldown should not block open signals: blocked=%v err=%v", blocked, err)
	}
}

func TestWebhookCloseCooldownSourceRequiresStopLoss(t *testing.T) {
	cases := []struct {
		signal trading.Signal
		want   string
	}{
		{signal: trading.Signal{PositionEffect: trading.PositionEffectClose, OrderIntent: "sl_short"}, want: "stop_loss_webhook"},
		{signal: trading.Signal{PositionEffect: trading.PositionEffectClose, OrderIntent: "tp_long"}, want: ""},
		{signal: trading.Signal{PositionEffect: trading.PositionEffectClose, Condition: "空单止损"}, want: "stop_loss_webhook"},
		{signal: trading.Signal{PositionEffect: trading.PositionEffectClose, Condition: "空单止盈"}, want: ""},
		{signal: trading.Signal{PositionEffect: trading.PositionEffectClose, Condition: "空单止盈止损"}, want: "stop_loss_webhook"},
		{signal: trading.Signal{PositionEffect: trading.PositionEffectOpen, Condition: "多单止损"}, want: ""},
	}
	for _, tc := range cases {
		got, ok := webhookCloseCooldownSource(tc.signal)
		if got != tc.want || ok != (tc.want != "") {
			t.Fatalf("webhookCloseCooldownSource(%#v)=(%q,%v) want %q", tc.signal, got, ok, tc.want)
		}
	}
}

func TestTVBotCreatesManualCoinpairCooldownWithoutExtendingActiveRule(t *testing.T) {
	srv := newTestServer(t)
	now := srv.now()
	type response struct {
		Status string                `json:"status"`
		Block  storage.CoinpairBlock `json:"block"`
	}
	post := func(symbol, price, exchange, apiID string) (int, response) {
		t.Helper()
		body, err := json.Marshal(map[string]string{
			"symbol":        symbol,
			"trigger_price": price,
			"exchange":      exchange,
			"api_id":        apiID,
		})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/tvbot/coinpair-blocks", bytes.NewReader(body))
		req.SetBasicAuth("admin", "Admin123")
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		var got response
		if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode response: %v body=%s", err, rr.Body.String())
		}
		return rr.Code, got
	}

	code, created := post("ETH-USDT-SWAP", "2525.5", trading.ExchangeOKX, "main")
	if code != http.StatusCreated || created.Status != "created" {
		t.Fatalf("create status=%d response=%#v", code, created)
	}
	if created.Block.Keyword != "ETH" || created.Block.Symbol != "ETH-USDT-SWAP" || created.Block.TriggerPrice != "2525.5" ||
		created.Block.Source != "analysis_manual" || created.Block.Exchange != trading.ExchangeOKX || created.Block.APIID != "main" {
		t.Fatalf("bad manual block: %#v", created.Block)
	}
	if !created.Block.StartedAt.Equal(now) || !created.Block.ExpiresAt.Equal(now.Add(coinpairCooldownDuration)) {
		t.Fatalf("bad manual cooldown window: %#v", created.Block)
	}

	code, active := post("ETHFIUSDT", "0.75", trading.ExchangeBinance, "secondary")
	if code != http.StatusOK || active.Status != "active" {
		t.Fatalf("duplicate status=%d response=%#v", code, active)
	}
	if active.Block.Keyword != created.Block.Keyword || !active.Block.ExpiresAt.Equal(created.Block.ExpiresAt) || active.Block.TriggerPrice != created.Block.TriggerPrice {
		t.Fatalf("active fuzzy rule should be returned without extension: created=%#v active=%#v", created.Block, active.Block)
	}

	probe := trading.Signal{Coinpair: "EETH", Ticker: "BINANCE:EETHUSDT", PositionEffect: trading.PositionEffectOpen}
	if block, blocked, err := srv.activeCoinpairCooldown(probe, now); err != nil || !blocked || block.Keyword != "ETH" {
		t.Fatalf("manual block should fuzzily cover open signal: blocked=%v block=%#v err=%v", blocked, block, err)
	}
	closeProbe := probe
	closeProbe.PositionEffect = trading.PositionEffectClose
	if _, blocked, err := srv.activeCoinpairCooldown(closeProbe, now); err != nil || blocked {
		t.Fatalf("manual block should not cover close signal: blocked=%v err=%v", blocked, err)
	}

	later := now.Add(25 * time.Hour)
	srv.Now = func() time.Time { return later }
	code, recreated := post("ETHUSDT", "2600", trading.ExchangeOKX, "main")
	if code != http.StatusCreated || recreated.Status != "created" || !recreated.Block.StartedAt.Equal(later) || !recreated.Block.ExpiresAt.Equal(later.Add(coinpairCooldownDuration)) {
		t.Fatalf("expired rule should be recreated for a fresh 24 hours: status=%d response=%#v", code, recreated)
	}
}

func TestTVBotManualCoinpairCooldownValidatesAuthAndPayload(t *testing.T) {
	srv := newTestServer(t)
	unauthorized := httptest.NewRecorder()
	srv.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodPost, "/tvbot/coinpair-blocks", strings.NewReader(`{"symbol":"ETHUSDT","exchange":"okx"}`)))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d body=%s", unauthorized.Code, unauthorized.Body.String())
	}

	for name, body := range map[string]string{
		"missing symbol":   `{"exchange":"okx"}`,
		"missing exchange": `{"symbol":"ETHUSDT"}`,
		"invalid exchange": `{"symbol":"ETHUSDT","exchange":"bybit"}`,
		"invalid symbol":   `{"symbol":"---","exchange":"okx"}`,
		"bad json":         `{`,
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/tvbot/coinpair-blocks", strings.NewReader(body))
			req.SetBasicAuth("admin", "Admin123")
			rr := httptest.NewRecorder()
			srv.ServeHTTP(rr, req)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestTVBotDeletesCoinpairCooldownWithAdminAuth(t *testing.T) {
	srv := newTestServer(t)
	now := srv.now()
	if _, _, err := srv.recordCoinpairCooldown("delete:1", "exchange_fill", trading.ExchangeOKX, "main", "2500", now, "ETHUSDT"); err != nil {
		t.Fatal(err)
	}
	unauthorized := httptest.NewRecorder()
	srv.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodDelete, "/tvbot/coinpair-blocks/ETH", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized delete status=%d body=%s", unauthorized.Code, unauthorized.Body.String())
	}

	request := func(keyword string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodDelete, "/tvbot/coinpair-blocks/"+keyword, nil)
		req.SetBasicAuth("admin", "Admin123")
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		return rr
	}
	rr := request("ETHUSDT")
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"status":"removed"`) || !strings.Contains(rr.Body.String(), `"keyword":"ETH"`) {
		t.Fatalf("delete status=%d body=%s", rr.Code, rr.Body.String())
	}
	if blocks, err := srv.Orders.ListActiveCoinpairBlocks(now); err != nil || len(blocks) != 0 {
		t.Fatalf("deleted cooldown remains active: blocks=%#v err=%v", blocks, err)
	}
	rr = request("ETH")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("missing delete status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestTVBotDeletesGlobalCoinpairCooldownWithAdminAuth(t *testing.T) {
	srv := newTestServer(t)
	now := srv.now()
	if _, _, err := srv.recordCoinpairCooldownWithDurationAndKeyword("global-delete:1", "exchange_fill", trading.ExchangeBinance, "main", "49000", now, time.Hour, globalCoinpairCooldownKeyword, "BTCUSDT"); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodDelete, "/tvbot/coinpair-blocks/"+globalCoinpairCooldownKeyword, nil)
	request.SetBasicAuth("admin", "Admin123")
	response := httptest.NewRecorder()
	srv.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"removed"`) || !strings.Contains(response.Body.String(), globalCoinpairCooldownKeyword) {
		t.Fatalf("global cooldown delete status=%d body=%s", response.Code, response.Body.String())
	}
	if blocks, err := srv.Orders.ListActiveCoinpairBlocks(now); err != nil || len(blocks) != 0 {
		t.Fatalf("deleted global cooldown remains active: blocks=%#v err=%v", blocks, err)
	}
}

func TestTVOrderCooldownBlocksBeforeExecutorAndListsRule(t *testing.T) {
	srv := newTestServer(t)
	now := srv.now()
	block, created, err := srv.recordCoinpairCooldown(
		"test-stop:1",
		"exchange_fill",
		trading.ExchangeOKX,
		"default",
		"2500",
		now,
		"ETH-USDT-SWAP",
	)
	if err != nil || !created || block.Keyword != "ETH" {
		t.Fatalf("add cooldown: created=%v block=%#v err=%v", created, block, err)
	}
	signal := validSignal(t, srv)
	signal.Coinpair = "ETHFI"
	signal.Ticker = "OKX:ETHFIUSDT.P"
	signal.Normalize()
	signal.Token = srv.Token.Generate(signal.CanonicalTokenPayload())
	body, err := json.Marshal(signal)
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/tvorder", bytes.NewReader(body)))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Status       string    `json:"status"`
		Reason       string    `json:"reason"`
		SignalID     string    `json:"signal_id"`
		Keyword      string    `json:"keyword"`
		BlockedUntil time.Time `json:"blocked_until"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Status != "ignored" || resp.Reason != "coinpair_cooldown" || resp.Keyword != "ETH" || !resp.BlockedUntil.Equal(now.Add(24*time.Hour)) {
		t.Fatalf("bad cooldown response: %#v", resp)
	}
	select {
	case got := <-srv.Executor.(fakeExecutor).calls:
		t.Fatalf("cooldown signal reached executor: %#v", got)
	case <-time.After(50 * time.Millisecond):
	}
	rec, found := srv.Orders.Get(resp.SignalID)
	if !found || rec.Status != storage.StatusIgnored || rec.ErrorCode != "coinpair_cooldown" || !strings.Contains(rec.Error, "ETH") {
		t.Fatalf("bad cooldown history: found=%v record=%#v", found, rec)
	}

	listRR := httptest.NewRecorder()
	listReq := httptest.NewRequest(http.MethodGet, "/tvbot/coinpair-blocks", nil)
	listReq.SetBasicAuth("admin", "Admin123")
	srv.ServeHTTP(listRR, listReq)
	if listRR.Code != http.StatusOK || !strings.Contains(listRR.Body.String(), `"keyword":"ETH"`) || !strings.Contains(listRR.Body.String(), `"trigger_price":"2500"`) {
		t.Fatalf("bad coinpair block list: status=%d body=%s", listRR.Code, listRR.Body.String())
	}

	closeProbe := trading.Signal{Coinpair: "ETHFI", Ticker: "ETHFIUSDT", PositionEffect: trading.PositionEffectClose}
	if _, blocked, err := srv.activeCoinpairCooldown(closeProbe, now); err != nil || blocked {
		t.Fatalf("close signal should bypass cooldown: blocked=%v err=%v", blocked, err)
	}
}

func TestAutoReentryStopsBeforeExchangeWhenCooldownActive(t *testing.T) {
	srv := newTestServer(t)
	now := srv.now()
	rec := submitMonitorTestOrder(t, srv.Orders, now, "auto-entry")
	lifecycle, err := srv.Orders.UpsertTradeLifecycleFromOrder(rec, "", 0, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := srv.recordCoinpairCooldown("auto-stop:1", "exchange_fill", trading.ExchangeBinance, "main", "49000", now, "BTCUSDT"); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Trading.AutoReentry.Enabled = true
	if err := srv.maybeSubmitAutoReentry(context.Background(), cfg, binance.Client{}, lifecycle, now); err != nil {
		t.Fatal(err)
	}
	updated, found, err := srv.Orders.FindTradeLifecycle(lifecycle.LifecycleID)
	if err != nil {
		t.Fatal(err)
	}
	if !found || updated.Status != storage.TradeLifecycleCooldown || updated.CooldownUntil == "" {
		t.Fatalf("auto reentry was not cooled down: found=%v lifecycle=%#v", found, updated)
	}
}
