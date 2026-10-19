package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotbinance"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotconfig"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotokx"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotstorage"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

type tradeMonitorExecutor struct {
	signals []trading.Signal
}

func (e *tradeMonitorExecutor) ExecuteSignal(ctx context.Context, signal trading.Signal, cfg trading.RuntimeConfig) (trading.OrderResult, error) {
	e.signals = append(e.signals, signal)
	return trading.OrderResult{
		APIID:          signal.APIID,
		TargetExchange: trading.ExchangeBinance,
		InstID:         "BTCUSDT",
		ClOrdID:        "reentry-client",
		OrdID:          "9901",
		OrdType:        "MARKET",
		Leverage:       signal.Leverage,
	}, nil
}

func (e *tradeMonitorExecutor) Check(ctx context.Context, cfg trading.RuntimeConfig) (map[string]any, error) {
	return map[string]any{"ok": true}, nil
}

func TestTradeMonitorStateMachineClassifiesExitFills(t *testing.T) {
	cases := []struct {
		name        string
		realizedPnl string
		wantStatus  string
	}{
		{name: "sl", realizedPnl: "-5.25", wantStatus: storage.TradeLifecycleSLHit},
		{name: "tp", realizedPnl: "7.50", wantStatus: storage.TradeLifecycleTPHit},
		{name: "flat", realizedPnl: "0", wantStatus: storage.TradeLifecycleExited},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, err := storage.NewSQLiteOrderStore(filepath.Join(t.TempDir(), "tvbot.db"), "")
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			now := time.Date(2026, 7, 30, 2, 0, 0, 0, time.UTC)
			rec := submitMonitorTestOrder(t, store, now, "9001")
			if _, err := store.UpsertTradeLifecycleFromOrder(rec, "", 0, now); err != nil {
				t.Fatal(err)
			}
			srv := &Server{Orders: store, Now: func() time.Time { return now }}
			cfg := config.Default()
			entry := storage.BinanceFill{
				APIID:       "main",
				Symbol:      "BTCUSDT",
				TradeID:     "entry-" + tc.name,
				OrderID:     "9001",
				Side:        "BUY",
				Price:       "50000",
				Qty:         "0.01",
				RealizedPnl: "0",
				FillTime:    now.UnixMilli(),
			}
			if err := srv.processBinanceFill(context.Background(), cfg, binance.Client{}, entry, now); err != nil {
				t.Fatal(err)
			}
			exit := storage.BinanceFill{
				APIID:       "main",
				Symbol:      "BTCUSDT",
				TradeID:     "exit-" + tc.name,
				OrderID:     "9101",
				Side:        "SELL",
				Price:       "49500",
				Qty:         "0.01",
				RealizedPnl: tc.realizedPnl,
				FillTime:    now.Add(time.Minute).UnixMilli(),
			}
			if err := srv.processBinanceFill(context.Background(), cfg, binance.Client{}, exit, now.Add(time.Minute)); err != nil {
				t.Fatal(err)
			}
			lifecycle, found, err := store.FindTradeLifecycle(rec.SignalID)
			if err != nil {
				t.Fatal(err)
			}
			if !found || lifecycle.Status != tc.wantStatus {
				t.Fatalf("bad lifecycle found=%v lifecycle=%#v", found, lifecycle)
			}
		})
	}
}

func TestBinanceLossFillCreatesCooldownWithoutBackfill(t *testing.T) {
	srv := newTestServer(t)
	now := srv.now()
	call := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/fapi/v1/userTrades" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		call++
		fillTime := now.Add(-time.Minute)
		tradeID := 100
		if call > 1 {
			fillTime = now.Add(time.Minute)
			tradeID = 101
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `[{"symbol":"ETHUSDT","side":"SELL","positionSide":"BOTH","price":"2490","qty":"1","realizedPnl":"-5","commission":"0.1","commissionAsset":"USDT","time":%d,"id":%d,"orderId":900}]`, fillTime.UnixMilli(), tradeID)
	}))
	defer ts.Close()
	client := binance.Client{
		BaseURL:     ts.URL,
		Credentials: binance.Credentials{APIKey: "key", SecretKey: "secret"},
		HTTPClient:  ts.Client(),
		Now:         func() time.Time { return now },
	}
	cfg := config.Default()
	if err := srv.pollBinanceSymbolFills(context.Background(), cfg, client, "main", "ETHUSDT", now, 72*time.Hour, false); err != nil {
		t.Fatal(err)
	}
	blocks, err := srv.Orders.ListActiveCoinpairBlocks(now)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 0 {
		t.Fatalf("first Binance poll must not backfill cooldowns: %#v", blocks)
	}
	if err := srv.pollBinanceSymbolFills(context.Background(), cfg, client, "main", "ETHUSDT", now.Add(2*time.Minute), 72*time.Hour, false); err != nil {
		t.Fatal(err)
	}
	blocks, err = srv.Orders.ListActiveCoinpairBlocks(now.Add(2 * time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 || blocks[0].Keyword != "ETH" || blocks[0].TriggerPrice != "2490" || blocks[0].Source != "exchange_fill" {
		t.Fatalf("new Binance loss fill did not create cooldown: %#v", blocks)
	}
}

func TestBinanceProfitableOrderWithLossFillDoesNotCreateCooldown(t *testing.T) {
	srv := newTestServer(t)
	now := srv.now()
	call := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/fapi/v1/userTrades" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		call++
		oldFill := fmt.Sprintf(`{"symbol":"BTCUSDT","side":"SELL","positionSide":"BOTH","price":"49000","qty":"1","realizedPnl":"-5","commission":"0.1","commissionAsset":"USDT","time":%d,"id":100,"orderId":900}`, now.Add(-time.Minute).UnixMilli())
		data := oldFill
		if call > 1 {
			lossFill := fmt.Sprintf(`{"symbol":"NOWUSDT","side":"SELL","positionSide":"BOTH","price":"116.27","qty":"1","realizedPnl":"-5","commission":"0.1","commissionAsset":"USDT","time":%d,"id":101,"orderId":901}`, now.Add(time.Minute).UnixMilli())
			profitFill := fmt.Sprintf(`{"symbol":"NOWUSDT","side":"SELL","positionSide":"BOTH","price":"118.50","qty":"1","realizedPnl":"14.744","commission":"0.1","commissionAsset":"USDT","time":%d,"id":102,"orderId":901}`, now.Add(time.Minute).UnixMilli())
			data = strings.Join([]string{oldFill, lossFill, profitFill}, ",")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `[%s]`, data)
	}))
	defer ts.Close()
	client := binance.Client{
		BaseURL:     ts.URL,
		Credentials: binance.Credentials{APIKey: "key", SecretKey: "secret"},
		HTTPClient:  ts.Client(),
		Now:         func() time.Time { return now },
	}
	cfg := config.Default()
	if err := srv.pollBinanceSymbolFills(context.Background(), cfg, client, "main", "NOWUSDT", now, 72*time.Hour, false); err != nil {
		t.Fatal(err)
	}
	if err := srv.pollBinanceSymbolFills(context.Background(), cfg, client, "main", "NOWUSDT", now.Add(2*time.Minute), 72*time.Hour, false); err != nil {
		t.Fatal(err)
	}
	blocks, err := srv.Orders.ListActiveCoinpairBlocks(now.Add(2 * time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 0 {
		t.Fatalf("profitable Binance order must not create cooldown: %#v", blocks)
	}
}

func TestOKXLossFillCreatesCooldownWithoutBackfill(t *testing.T) {
	srv := newTestServer(t)
	now := srv.now()
	call := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v5/trade/fills-history" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		call++
		oldFill := fmt.Sprintf(`{"instType":"SWAP","instId":"BTC-USDT-SWAP","tradeId":"100","ordId":"900","side":"sell","fillPx":"49000","fillSz":"1","fillPnl":"-5","fillTime":"%d"}`, now.Add(-time.Minute).UnixMilli())
		data := oldFill
		if call > 1 {
			newFill := fmt.Sprintf(`{"instType":"SWAP","instId":"ETH-USDT-SWAP","tradeId":"101","ordId":"901","side":"sell","fillPx":"2490","fillSz":"1","fillPnl":"-3","fillTime":"%d"}`, now.Add(time.Minute).UnixMilli())
			data = newFill + "," + oldFill
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"code":"0","msg":"","data":[%s]}`, data)
	}))
	defer ts.Close()
	client := okx.Client{
		BaseURL: ts.URL,
		Credentials: okx.Credentials{
			APIKey:     "key",
			SecretKey:  "secret",
			Passphrase: "pass",
		},
		HTTPClient: ts.Client(),
		Now:        func() time.Time { return now },
	}
	if err := srv.pollOKXCoinpairCooldownFills(context.Background(), client, "default", now); err != nil {
		t.Fatal(err)
	}
	blocks, err := srv.Orders.ListActiveCoinpairBlocks(now)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 0 {
		t.Fatalf("first OKX poll must not backfill cooldowns: %#v", blocks)
	}
	if err := srv.pollOKXCoinpairCooldownFills(context.Background(), client, "default", now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	blocks, err = srv.Orders.ListActiveCoinpairBlocks(now.Add(2 * time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 || blocks[0].Keyword != "ETH" || blocks[0].TriggerPrice != "2490" || blocks[0].Source != "exchange_fill" {
		t.Fatalf("new OKX loss fill did not create cooldown: %#v", blocks)
	}
}

func TestOKXProfitableOrderWithLossFillDoesNotCreateCooldown(t *testing.T) {
	srv := newTestServer(t)
	now := srv.now()
	call := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v5/trade/fills-history" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		call++
		oldFill := fmt.Sprintf(`{"instType":"SWAP","instId":"BTC-USDT-SWAP","tradeId":"100","ordId":"900","side":"sell","fillPx":"49000","fillSz":"1","fillPnl":"-5","fillTime":"%d"}`, now.Add(-time.Minute).UnixMilli())
		data := oldFill
		if call > 1 {
			lossFill := fmt.Sprintf(`{"instType":"SWAP","instId":"NOW-USDT-SWAP","tradeId":"101","ordId":"901","side":"sell","fillPx":"116.27","fillSz":"1","fillPnl":"-5","fillTime":"%d"}`, now.Add(time.Minute).UnixMilli())
			profitFill := fmt.Sprintf(`{"instType":"SWAP","instId":"NOW-USDT-SWAP","tradeId":"102","ordId":"901","side":"sell","fillPx":"118.50","fillSz":"1","fillPnl":"14.744","fillTime":"%d"}`, now.Add(time.Minute).UnixMilli())
			data = strings.Join([]string{profitFill, lossFill, oldFill}, ",")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"code":"0","msg":"","data":[%s]}`, data)
	}))
	defer ts.Close()
	client := okx.Client{
		BaseURL: ts.URL,
		Credentials: okx.Credentials{
			APIKey:     "key",
			SecretKey:  "secret",
			Passphrase: "pass",
		},
		HTTPClient: ts.Client(),
		Now:        func() time.Time { return now },
	}
	if err := srv.pollOKXCoinpairCooldownFills(context.Background(), client, "default", now); err != nil {
		t.Fatal(err)
	}
	if err := srv.pollOKXCoinpairCooldownFills(context.Background(), client, "default", now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	blocks, err := srv.Orders.ListActiveCoinpairBlocks(now.Add(2 * time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 0 {
		t.Fatalf("profitable OKX order must not create cooldown: %#v", blocks)
	}
}

func TestTradeMonitorAutoReentrySubmitsOnce(t *testing.T) {
	store, err := storage.NewSQLiteOrderStore(filepath.Join(t.TempDir(), "tvbot.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 7, 30, 3, 0, 0, 0, time.UTC)
	rec := submitMonitorTestOrder(t, store, now, "9001")
	if _, err := store.UpsertTradeLifecycleFromOrder(rec, "", 0, now); err != nil {
		t.Fatal(err)
	}
	binanceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/fapi/v3/positionRisk":
			_, _ = w.Write([]byte(`[]`))
		case "/fapi/v1/ticker/bookTicker":
			_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","bidPrice":"49990","bidQty":"1","askPrice":"50010","askQty":"1","time":1784880000000}`))
		default:
			t.Fatalf("unexpected Binance path %s", r.URL.Path)
		}
	}))
	defer binanceServer.Close()
	executor := &tradeMonitorExecutor{}
	srv := &Server{
		Orders:   store,
		Executor: executor,
		Now:      func() time.Time { return now },
	}
	cfg := config.Default()
	cfg.Trading.BinanceDemoBaseURL = binanceServer.URL
	cfg.Trading.AutoReentry.Enabled = true
	cfg.Trading.AutoReentry.ReentryAmountPct = 50
	srv.ConfigStore = config.NewStore("", cfg)
	seedTestMarketScopeCatalog(t, store, now)
	client := binance.Client{
		BaseURL:     binanceServer.URL,
		Credentials: binance.Credentials{APIKey: "key", SecretKey: "secret"},
		HTTPClient:  binanceServer.Client(),
	}
	entry := storage.BinanceFill{
		APIID:       "main",
		Symbol:      "BTCUSDT",
		TradeID:     "entry",
		OrderID:     "9001",
		Side:        "BUY",
		Price:       "50000",
		Qty:         "0.01",
		RealizedPnl: "0",
		FillTime:    now.UnixMilli(),
	}
	if err := srv.processBinanceFill(context.Background(), cfg, client, entry, now); err != nil {
		t.Fatal(err)
	}
	exit := storage.BinanceFill{
		APIID:       "main",
		Symbol:      "BTCUSDT",
		TradeID:     "exit",
		OrderID:     "9101",
		Side:        "SELL",
		Price:       "49500",
		Qty:         "0.01",
		RealizedPnl: "-5",
		FillTime:    now.Add(time.Minute).UnixMilli(),
	}
	if err := srv.processBinanceFill(context.Background(), cfg, client, exit, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(executor.signals) != 1 {
		t.Fatalf("expected one auto reentry signal, got %#v", executor.signals)
	}
	if executor.signals[0].Amount.Value != 50 || executor.signals[0].Exchange != "server_fill_monitor" || executor.signals[0].TargetExchange != trading.ExchangeBinance {
		t.Fatalf("bad auto reentry signal: %#v", executor.signals[0])
	}
	lifecycle, found, err := store.FindTradeLifecycle(rec.SignalID)
	if err != nil {
		t.Fatal(err)
	}
	if !found || lifecycle.Status != storage.TradeLifecycleReentrySubmitted || lifecycle.ReentrySignalID == "" {
		t.Fatalf("bad parent lifecycle after reentry found=%v lifecycle=%#v", found, lifecycle)
	}
	if err := srv.processBinanceFill(context.Background(), cfg, client, exit, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(executor.signals) != 1 {
		t.Fatalf("duplicate stop fill should not submit another reentry, got %#v", executor.signals)
	}
}

func submitMonitorTestOrder(t *testing.T, store *storage.OrderStore, now time.Time, orderID string) storage.OrderRecord {
	t.Helper()
	tp := trading.NewFlexibleFloat(2)
	sl := trading.NewFlexibleFloat(1)
	signal := trading.Signal{
		Action:         trading.ActionLong,
		APIID:          "main",
		TargetExchange: trading.ExchangeBinance,
		Coinpair:       "BTC",
		Ticker:         "BTCUSDT",
		Price:          trading.NewFlexibleFloat(50000),
		SentAt:         now.Format(time.RFC3339Nano),
		Leverage:       10,
		Amount:         trading.NewFlexibleFloat(100),
		Risk: trading.Risk{
			Type:  trading.RiskTPSL,
			TPPct: &tp,
			SLPct: &sl,
		},
	}
	signal.Normalize()
	rec, _, err := store.RecordAccepted(signal, storage.DedupeKey(signal), now)
	if err != nil {
		t.Fatal(err)
	}
	result := trading.OrderResult{
		APIID:          "main",
		TargetExchange: trading.ExchangeBinance,
		InstID:         "BTCUSDT",
		OrdID:          orderID,
		ClOrdID:        "entry-" + orderID,
		OrdType:        "MARKET",
		Leverage:       10,
		RiskOrders: []trading.RiskOrderResult{
			{Exchange: trading.ExchangeBinance, AlgoID: "7001", ClientAlgoID: "entry-" + orderID + "TP", OrderType: "TAKE_PROFIT_MARKET", TriggerPrice: "51000"},
			{Exchange: trading.ExchangeBinance, AlgoID: "7002", ClientAlgoID: "entry-" + orderID + "SL", OrderType: "STOP_MARKET", TriggerPrice: "49500"},
		},
	}
	if err := store.MarkSubmitted(rec.SignalID, result, now); err != nil {
		t.Fatal(err)
	}
	rec, ok := store.Get(rec.SignalID)
	if !ok {
		t.Fatal("submitted order not found")
	}
	return rec
}
