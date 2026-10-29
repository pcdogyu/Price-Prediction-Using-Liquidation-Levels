package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotbinance"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotconfig"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotokx"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotstorage"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

func TestTop100RankingsSortLimitAndFilterMarkets(t *testing.T) {
	okxRows := []symbolInstrument{
		{Instrument: okx.Instrument{InstID: "BBB-USDT-SWAP", BaseCcy: "BBB", State: "live"}, TurnoverUSDT24h: "1000"},
		{Instrument: okx.Instrument{InstID: "AAA-USDT-SWAP", BaseCcy: "AAA", State: "live"}, TurnoverUSDT24h: "1000"},
		{Instrument: okx.Instrument{InstID: "MISMATCH-USDT-SWAP", Uly: "OTHER-USDT", InstFamily: "MISMATCH-USDT", BaseCcy: "MISMATCH", State: "live"}, TurnoverUSDT24h: "100000"},
		{Instrument: okx.Instrument{InstID: "HALT-USDT-SWAP", BaseCcy: "HALT", State: "suspend"}, TurnoverUSDT24h: "99999"},
		{Instrument: okx.Instrument{InstID: "ZERO-USDT-SWAP", BaseCcy: "ZERO", State: "live"}, TurnoverUSDT24h: "0"},
		{Instrument: okx.Instrument{InstID: "USDC-USDT-SWAP", BaseCcy: "USDC", State: "live"}, TurnoverUSDT24h: "99998"},
		{Instrument: okx.Instrument{InstID: "USDT-USDT-SWAP", BaseCcy: "USDT", State: "live"}, TurnoverUSDT24h: "99997"},
		{Instrument: okx.Instrument{InstID: "XAU-USDT-SWAP", BaseCcy: "XAU", State: "live"}, TurnoverUSDT24h: "99996"},
		{Instrument: okx.Instrument{InstID: "XAUT-USDT-SWAP", BaseCcy: "XAUT", State: "live"}, TurnoverUSDT24h: "99995"},
		{Instrument: okx.Instrument{InstID: "XAG-USDT-SWAP", BaseCcy: "XAG", State: "live"}, TurnoverUSDT24h: "99994"},
		{Instrument: okx.Instrument{InstID: "FDUSD-USDT-SWAP", BaseCcy: "FDUSD", State: "live"}, TurnoverUSDT24h: "99993"},
		{Instrument: okx.Instrument{InstID: "PAXG-USDT-SWAP", BaseCcy: "PAXG", State: "live"}, TurnoverUSDT24h: "99992"},
	}
	binanceRows := []binanceSymbolInstrument{
		{SymbolInfo: binance.SymbolInfo{Symbol: "BBBUSDT", BaseAsset: "BBB", Status: "TRADING"}, TurnoverUSDT24h: "1000"},
		{SymbolInfo: binance.SymbolInfo{Symbol: "AAAUSDT", BaseAsset: "AAA", Status: "TRADING"}, TurnoverUSDT24h: "1000"},
		{SymbolInfo: binance.SymbolInfo{Symbol: "HALTUSDT", BaseAsset: "HALT", Status: "BREAK"}, TurnoverUSDT24h: "99999"},
		{SymbolInfo: binance.SymbolInfo{Symbol: "USDCUSDT", BaseAsset: "USDC", Status: "TRADING"}, TurnoverUSDT24h: "99998"},
		{SymbolInfo: binance.SymbolInfo{Symbol: "USDTUSDT", BaseAsset: "USDT", Status: "TRADING"}, TurnoverUSDT24h: "99997"},
		{SymbolInfo: binance.SymbolInfo{Symbol: "XAUUSDT", BaseAsset: "XAU", Status: "TRADING"}, TurnoverUSDT24h: "99996"},
		{SymbolInfo: binance.SymbolInfo{Symbol: "XAUTUSDT", BaseAsset: "XAUT", Status: "TRADING"}, TurnoverUSDT24h: "99995"},
		{SymbolInfo: binance.SymbolInfo{Symbol: "XAGUSDT", BaseAsset: "XAG", Status: "TRADING"}, TurnoverUSDT24h: "99994"},
		{SymbolInfo: binance.SymbolInfo{Symbol: "FDUSDUSDT", BaseAsset: "FDUSD", Status: "TRADING"}, TurnoverUSDT24h: "99993"},
		{SymbolInfo: binance.SymbolInfo{Symbol: "PAXGUSDT", BaseAsset: "PAXG", Status: "TRADING"}, TurnoverUSDT24h: "99992"},
	}
	for i := 0; i < 120; i++ {
		base := fmt.Sprintf("S%02d", i)
		turnover := fmt.Sprintf("%d", 900-i)
		okxRows = append(okxRows, symbolInstrument{Instrument: okx.Instrument{InstID: base + "-USDT-SWAP", BaseCcy: base, State: "live"}, TurnoverUSDT24h: turnover})
		binanceRows = append(binanceRows, binanceSymbolInstrument{SymbolInfo: binance.SymbolInfo{Symbol: base + "USDT", BaseAsset: base, Status: "TRADING"}, TurnoverUSDT24h: turnover})
	}
	limit := config.MarketTurnoverScopeLimit(config.DefaultMarketTurnoverScope)
	okxTop := topOKXInstruments(okxRows, limit)
	binanceTop := topBinanceInstruments(binanceRows, limit)
	if len(okxTop) != 100 || len(binanceTop) != 100 {
		t.Fatalf("top sizes okx=%d binance=%d", len(okxTop), len(binanceTop))
	}
	if okxTop[0].InstID != "AAA-USDT-SWAP" || okxTop[1].InstID != "BBB-USDT-SWAP" {
		t.Fatalf("OKX equal turnover ordering is not stable: %#v", okxTop[:2])
	}
	if binanceTop[0].Symbol != "AAAUSDT" || binanceTop[1].Symbol != "BBBUSDT" {
		t.Fatalf("Binance equal turnover ordering is not stable: %#v", binanceTop[:2])
	}
	for _, row := range okxTop {
		if row.BaseCcy == "HALT" || row.BaseCcy == "ZERO" || row.BaseCcy == "MISMATCH" || row.BaseCcy == "S98" || excludedRankingBase(row.BaseCcy, row.InstID) {
			t.Fatalf("ineligible or below-cutoff OKX symbol ranked: %#v", row)
		}
	}
	for _, row := range binanceTop {
		if row.BaseAsset == "HALT" || row.BaseAsset == "S98" || excludedRankingBase(row.BaseAsset, row.Symbol) {
			t.Fatalf("ineligible or below-cutoff Binance symbol ranked: %#v", row)
		}
	}
}

func TestExcludedRankingBasesDoNotMakeCompleteRankingsUnavailable(t *testing.T) {
	okxSet := okxInstrumentSet{Count: 5, Instruments: []symbolInstrument{
		{Instrument: okx.Instrument{InstID: "BTC-USDT-SWAP", BaseCcy: "BTC", State: "live"}, TurnoverUSDT24h: "100"},
		{Instrument: okx.Instrument{InstID: "ZERO-USDT-SWAP", BaseCcy: "ZERO", State: "live"}, TurnoverUSDT24h: "0"},
		{Instrument: okx.Instrument{InstID: "USDC-USDT-SWAP", BaseCcy: "USDC", State: "live"}, TurnoverUSDT24h: "200"},
		{Instrument: okx.Instrument{InstID: "XAUT-USDT-SWAP", BaseCcy: "XAUT", State: "live"}, TurnoverUSDT24h: "300"},
		{Instrument: okx.Instrument{InstID: "STRK-USDT-SWAP", Uly: "TSLA-USDT", InstFamily: "STRK-USDT", BaseCcy: "STRK", State: "live"}, TurnoverUSDT24h: "400"},
	}}
	limit := config.MarketTurnoverScopeLimit(config.DefaultMarketTurnoverScope)
	okxSet.TopInstruments = topOKXInstruments(okxSet.Instruments, limit)
	markOKXRankingUnavailable(&okxSet, limit)
	if okxSet.TickerError != "" || len(okxSet.TopInstruments) != 1 || okxSet.TopInstruments[0].BaseCcy != "BTC" {
		t.Fatalf("excluded OKX bases or mismatched underlyings should not make ranking unavailable: %#v", okxSet)
	}

	binanceSet := binanceInstrumentSet{Count: 4, Instruments: []binanceSymbolInstrument{
		{SymbolInfo: binance.SymbolInfo{Symbol: "BTCUSDT", BaseAsset: "BTC", Status: "TRADING"}, TurnoverUSDT24h: "100"},
		{SymbolInfo: binance.SymbolInfo{Symbol: "ZEROUSDT", BaseAsset: "ZERO", Status: "TRADING"}, TurnoverUSDT24h: "0"},
		{SymbolInfo: binance.SymbolInfo{Symbol: "USDTUSDT", BaseAsset: "USDT", Status: "TRADING"}, TurnoverUSDT24h: "200"},
		{SymbolInfo: binance.SymbolInfo{Symbol: "XAGUSDT", BaseAsset: "XAG", Status: "TRADING"}, TurnoverUSDT24h: "300"},
	}}
	binanceSet.TopInstruments = topBinanceInstruments(binanceSet.Instruments, limit)
	markBinanceRankingUnavailable(&binanceSet, limit)
	if binanceSet.TickerError != "" || len(binanceSet.TopInstruments) != 1 || binanceSet.TopInstruments[0].BaseAsset != "BTC" {
		t.Fatalf("excluded Binance bases should not make ranking unavailable: %#v", binanceSet)
	}
}

func TestMarketScopeDecisionUsesExactTargetMarketAndSymbolFormats(t *testing.T) {
	srv := newTestServer(t)
	upsertTestOKXMarket(t, srv, trading.TradeEnvDemo, "ETHFI")
	upsertTestBinanceMarket(t, srv, trading.TradeEnvLive, "BETA")

	decision, err := srv.marketScopeDecision(trading.Signal{TargetExchange: trading.ExchangeOKX, TradeEnv: trading.TradeEnvDemo, Coinpair: "OKX:ETHFIUSDT.P"})
	if err != nil || !decision.Available || !decision.Allowed || decision.Symbol != "ETHFI-USDT-SWAP" {
		t.Fatalf("formatted OKX symbol not matched: decision=%#v err=%v", decision, err)
	}
	decision, err = srv.marketScopeDecision(trading.Signal{TargetExchange: trading.ExchangeOKX, TradeEnv: trading.TradeEnvDemo, Coinpair: "ETH"})
	if err != nil || !decision.Available || decision.Allowed {
		t.Fatalf("exact matching should not treat ETH as ETHFI: decision=%#v err=%v", decision, err)
	}
	decision, err = srv.marketScopeDecision(trading.Signal{TargetExchange: trading.ExchangeBinance, TradeEnv: trading.TradeEnvLive, Ticker: "BINANCE:BETAUSDT"})
	if err != nil || !decision.Available || !decision.Allowed || decision.Symbol != "BETAUSDT" {
		t.Fatalf("Binance live market not matched: decision=%#v err=%v", decision, err)
	}
	decision, err = srv.marketScopeDecision(trading.Signal{TargetExchange: trading.ExchangeBinance, Ticker: "BINANCE:BETAUSDT"})
	if err != nil || !decision.Available || decision.Allowed || decision.TradeEnv != trading.TradeEnvDemo {
		t.Fatalf("missing trade_env should use the independent demo ranking: decision=%#v err=%v", decision, err)
	}
}

func TestMarketTop100DecisionAllowsRanks51To100AndRejectsRank101(t *testing.T) {
	srv := newTestServer(t)
	bases := make([]string, 110)
	for i := range bases {
		bases[i] = fmt.Sprintf("R%02d", i)
	}
	upsertTestOKXMarket(t, srv, trading.TradeEnvDemo, bases...)
	upsertTestBinanceMarket(t, srv, trading.TradeEnvLive, bases...)

	checks := []struct {
		name    string
		signal  trading.Signal
		allowed bool
	}{
		{name: "OKX rank 60", signal: trading.Signal{TargetExchange: trading.ExchangeOKX, TradeEnv: trading.TradeEnvDemo, Coinpair: "R59"}, allowed: true},
		{name: "OKX rank 100", signal: trading.Signal{TargetExchange: trading.ExchangeOKX, TradeEnv: trading.TradeEnvDemo, Coinpair: "R99"}, allowed: true},
		{name: "OKX rank 101", signal: trading.Signal{TargetExchange: trading.ExchangeOKX, TradeEnv: trading.TradeEnvDemo, Coinpair: "R100"}, allowed: false},
		{name: "Binance rank 60", signal: trading.Signal{TargetExchange: trading.ExchangeBinance, TradeEnv: trading.TradeEnvLive, Coinpair: "R59"}, allowed: true},
		{name: "Binance rank 100", signal: trading.Signal{TargetExchange: trading.ExchangeBinance, TradeEnv: trading.TradeEnvLive, Coinpair: "R99"}, allowed: true},
		{name: "Binance rank 101", signal: trading.Signal{TargetExchange: trading.ExchangeBinance, TradeEnv: trading.TradeEnvLive, Coinpair: "R100"}, allowed: false},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			decision, err := srv.marketScopeDecision(check.signal)
			if err != nil || !decision.Available || decision.Allowed != check.allowed {
				t.Fatalf("decision=%#v err=%v expected allowed=%v", decision, err, check.allowed)
			}
		})
	}
}

func TestMarketTurnoverScopesControlCachedRankingsAndDecisions(t *testing.T) {
	checks := []struct {
		scope string
		want  int
	}{
		{scope: config.MarketTurnoverScopeTop5, want: 5},
		{scope: config.MarketTurnoverScopeTop10, want: 10},
		{scope: config.MarketTurnoverScopeTop20, want: 20},
		{scope: config.MarketTurnoverScopeTop50, want: 50},
		{scope: config.MarketTurnoverScopeTop100, want: 100},
		{scope: config.MarketTurnoverScopeTop200, want: 200},
		{scope: config.MarketTurnoverScopeTop500, want: 500},
		{scope: config.MarketTurnoverScopeAll, want: 510},
	}
	for _, check := range checks {
		t.Run(check.scope, func(t *testing.T) {
			srv := newTestServer(t)
			cfg := srv.ConfigStore.Get()
			cfg.Trading.MarketTurnoverScope = check.scope
			srv.ConfigStore = config.NewStore("", cfg)
			bases := make([]string, 510)
			for i := range bases {
				bases[i] = fmt.Sprintf("R%03d", i)
			}
			upsertTestOKXMarket(t, srv, trading.TradeEnvDemo, bases...)
			upsertTestBinanceMarket(t, srv, trading.TradeEnvLive, bases...)

			resp, err := srv.cachedSymbolsResponse(srv.ConfigStore.Get())
			if err != nil {
				t.Fatal(err)
			}
			if len(resp.OKX.Demo.TopInstruments) != check.want || len(resp.Binance.Live.TopInstruments) != check.want {
				t.Fatalf("scope=%s sizes OKX=%d Binance=%d want=%d", check.scope, len(resp.OKX.Demo.TopInstruments), len(resp.Binance.Live.TopInstruments), check.want)
			}

			lastAllowed := bases[check.want-1]
			for _, signal := range []trading.Signal{
				{TargetExchange: trading.ExchangeOKX, TradeEnv: trading.TradeEnvDemo, Coinpair: lastAllowed},
				{TargetExchange: trading.ExchangeBinance, TradeEnv: trading.TradeEnvLive, Coinpair: lastAllowed},
			} {
				decision, err := srv.marketScopeDecision(signal)
				if err != nil || !decision.Available || !decision.Allowed || decision.Scope != check.scope {
					t.Fatalf("last allowed decision=%#v err=%v", decision, err)
				}
			}

			outside := "NOTTOP"
			if check.want < len(bases) {
				outside = bases[check.want]
			}
			decision, err := srv.marketScopeDecision(trading.Signal{TargetExchange: trading.ExchangeOKX, TradeEnv: trading.TradeEnvDemo, Coinpair: outside})
			if err != nil || !decision.Available || decision.Allowed {
				t.Fatalf("outside decision=%#v err=%v", decision, err)
			}
		})
	}
}

func TestSymbolsAPIRecalculatesCachedCatalogImmediatelyAfterScopeChange(t *testing.T) {
	srv := newTestServer(t)
	bases := make([]string, 220)
	for i := range bases {
		bases[i] = fmt.Sprintf("API%03d", i)
	}
	upsertTestOKXMarket(t, srv, trading.TradeEnvDemo, bases...)

	getSymbols := func() symbolsResponse {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/tvbot/symbols", nil)
		req.SetBasicAuth("admin", "Admin123")
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("symbols status=%d body=%s", rr.Code, rr.Body.String())
		}
		var resp symbolsResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		return resp
	}

	if got := len(getSymbols().OKX.Demo.TopInstruments); got != 100 {
		t.Fatalf("default scope returned %d symbols, want 100", got)
	}
	put := httptest.NewRequest(http.MethodPut, "/tvbot/config", bytes.NewReader([]byte(`{"trading":{"market_turnover_scope":"top200"}}`)))
	put.SetBasicAuth("admin", "Admin123")
	putResult := httptest.NewRecorder()
	srv.ServeHTTP(putResult, put)
	if putResult.Code != http.StatusOK {
		t.Fatalf("scope update status=%d body=%s", putResult.Code, putResult.Body.String())
	}
	if got := len(getSymbols().OKX.Demo.TopInstruments); got != 200 {
		t.Fatalf("updated scope returned %d symbols, want 200", got)
	}
}

func TestTVOrderMarketScopeIgnoresOutsideEntryAllowsCloseAndFailsClosedWithoutRanking(t *testing.T) {
	srv := newTestServer(t)

	outside := validSignal(t, srv)
	outside.Coinpair = "NOTTOP"
	outside.Ticker = "OKX:NOTTOPUSDT.P"
	outside.Token = srv.Token.Generate(outside.CanonicalTokenPayload())
	rr := postTVOrder(t, srv, outside)
	if rr.Code != http.StatusAccepted || !bytes.Contains(rr.Body.Bytes(), []byte(`"reason":"outside_market_top100"`)) || !bytes.Contains(rr.Body.Bytes(), []byte(`"trade_env":"demo"`)) {
		t.Fatalf("outside top 100 response status=%d body=%s", rr.Code, rr.Body.String())
	}
	select {
	case signal := <-srv.Executor.(fakeExecutor).calls:
		t.Fatalf("outside top 100 signal reached executor: %#v", signal)
	case <-time.After(50 * time.Millisecond):
	}

	closeSignal := outside
	closeSignal.PositionEffect = trading.PositionEffectClose
	closeSignal.PositionSide = trading.PositionSideShort
	closeSignal.OrderIntent = "close_short"
	closeSignal.Token = srv.Token.Generate(closeSignal.CanonicalTokenPayload())
	rr = postTVOrder(t, srv, closeSignal)
	if rr.Code != http.StatusAccepted || !bytes.Contains(rr.Body.Bytes(), []byte(`"status":"accepted"`)) {
		t.Fatalf("close signal should bypass top 100 status=%d body=%s", rr.Code, rr.Body.String())
	}

	clearTestMarket(t, srv, trading.ExchangeOKX, trading.TradeEnvDemo)
	unavailable := validSignal(t, srv)
	rr = postTVOrder(t, srv, unavailable)
	if rr.Code != http.StatusServiceUnavailable || !bytes.Contains(rr.Body.Bytes(), []byte(`"error":"top100_unavailable"`)) {
		t.Fatalf("missing ranking should fail closed status=%d body=%s", rr.Code, rr.Body.String())
	}
	if symbolCatalogSyncInterval != 24*time.Hour {
		t.Fatalf("symbol sync interval=%s", symbolCatalogSyncInterval)
	}
}

func TestTVOrderUsesConfiguredMarketScopeErrorCodes(t *testing.T) {
	srv := newTestServer(t)
	cfg := srv.ConfigStore.Get()
	cfg.Trading.MarketTurnoverScope = config.MarketTurnoverScopeTop50
	srv.ConfigStore = config.NewStore("", cfg)
	bases := make([]string, 60)
	for i := range bases {
		bases[i] = fmt.Sprintf("CODE%02d", i)
	}
	upsertTestOKXMarket(t, srv, trading.TradeEnvDemo, bases...)

	signal := validSignal(t, srv)
	signal.Coinpair = bases[50]
	signal.Ticker = "OKX:" + bases[50] + "USDT.P"
	signal.Token = srv.Token.Generate(signal.CanonicalTokenPayload())
	rr := postTVOrder(t, srv, signal)
	if rr.Code != http.StatusAccepted || !bytes.Contains(rr.Body.Bytes(), []byte(`"reason":"outside_market_top50"`)) || !bytes.Contains(rr.Body.Bytes(), []byte(`"market_scope":"top50"`)) {
		t.Fatalf("top50 outside response status=%d body=%s", rr.Code, rr.Body.String())
	}

	clearTestMarket(t, srv, trading.ExchangeOKX, trading.TradeEnvDemo)
	signal = validSignal(t, srv)
	rr = postTVOrder(t, srv, signal)
	if rr.Code != http.StatusServiceUnavailable || !bytes.Contains(rr.Body.Bytes(), []byte(`"error":"top50_unavailable"`)) {
		t.Fatalf("top50 unavailable response status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestOrderRetryBypassesMarketScopeForFailedAndIgnoredSources(t *testing.T) {
	for _, sourceStatus := range []storage.OrderStatus{storage.StatusFailed, storage.StatusIgnored} {
		t.Run(string(sourceStatus), func(t *testing.T) {
			srv := newTestServer(t)
			now := srv.now()
			signal := validSignal(t, srv)
			signal.Coinpair = "NOTTOP"
			signal.Ticker = "OKX:NOTTOPUSDT.P"
			signal.TargetExchange = trading.ExchangeOKX
			signal.TradeEnv = trading.TradeEnvDemo
			signal.PositionEffect = trading.PositionEffectOpen
			signal.Normalize()

			var record storage.OrderRecord
			var err error
			if sourceStatus == storage.StatusFailed {
				var duplicate bool
				record, duplicate, err = srv.Orders.RecordAccepted(signal, "outside-retry-failed", now)
				if err == nil && !duplicate {
					err = srv.Orders.MarkFailed(record.SignalID, fmt.Errorf("seed failure"), now)
				}
			} else {
				record, err = srv.Orders.RecordIgnoredReason(signal, "outside_market_top100", "outside configured market scope", now)
			}
			if err != nil {
				t.Fatal(err)
			}

			installOKXRetryTicker(t, srv, "NOTTOP-USDT-SWAP", "100", "102", "101")
			req := httptest.NewRequest(http.MethodPost, "/tvbot/orders/"+record.SignalID+"/retry", nil)
			req.SetBasicAuth("admin", "Admin123")
			rr := httptest.NewRecorder()
			srv.ServeHTTP(rr, req)
			if rr.Code != http.StatusAccepted || !bytes.Contains(rr.Body.Bytes(), []byte(`"status":"accepted"`)) || !bytes.Contains(rr.Body.Bytes(), []byte(`"retry_of":"`+record.SignalID+`"`)) || !bytes.Contains(rr.Body.Bytes(), []byte(`"price":"101"`)) {
				t.Fatalf("outside retry status=%d body=%s", rr.Code, rr.Body.String())
			}
			var response struct {
				SignalID string `json:"signal_id"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			select {
			case got := <-srv.Executor.(fakeExecutor).calls:
				if got.Coinpair != "NOTTOP" || got.Price.Value != 101 {
					t.Fatalf("bad outside-scope retry signal: %#v", got)
				}
			case <-time.After(time.Second):
				t.Fatal("outside-scope retry did not reach executor")
			}
			waitOrderStatus(t, srv.Orders, response.SignalID, storage.StatusSubmitted)
		})
	}
}

func TestAutoReentryOutsideMarketScopeStopsBeforeExchange(t *testing.T) {
	srv := newTestServer(t)
	// This test asserts a freshly recorded monitor event; keep its injected
	// clock within the live 30-day retention window.
	srv.Now = time.Now
	now := srv.now()
	signal := validSignal(t, srv)
	signal.TargetExchange = trading.ExchangeBinance
	signal.TradeEnv = trading.TradeEnvDemo
	signal.APIID = "main"
	signal.Coinpair = "NOTTOP"
	signal.Ticker = "NOTTOPUSDT"
	signal.PositionEffect = trading.PositionEffectOpen
	signal.Normalize()
	record, duplicate, err := srv.Orders.RecordAccepted(signal, "outside-auto-reentry", now)
	if err != nil || duplicate {
		t.Fatalf("seed order: duplicate=%v err=%v", duplicate, err)
	}
	if err := srv.Orders.MarkSubmitted(record.SignalID, trading.OrderResult{
		TargetExchange: trading.ExchangeBinance, APIID: "main", InstID: "NOTTOPUSDT", OrdID: "1", ClOrdID: "entry-1", OrdType: "MARKET",
	}, now); err != nil {
		t.Fatal(err)
	}
	record, _ = srv.Orders.Get(record.SignalID)
	lifecycle, err := srv.Orders.UpsertTradeLifecycleFromOrder(record, "", 0, now)
	if err != nil {
		t.Fatal(err)
	}
	exchangeCalls := 0
	exchangeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		exchangeCalls++
		http.Error(w, "unexpected exchange request", http.StatusInternalServerError)
	}))
	defer exchangeServer.Close()
	cfg := srv.ConfigStore.Get()
	cfg.Trading.AutoReentry.Enabled = true
	client := binance.Client{BaseURL: exchangeServer.URL, HTTPClient: exchangeServer.Client()}
	if err := srv.maybeSubmitAutoReentry(t.Context(), cfg, client, lifecycle, now); err != nil {
		t.Fatal(err)
	}
	if exchangeCalls != 0 {
		t.Fatalf("outside top 100 auto reentry made %d exchange calls", exchangeCalls)
	}
	updated, found, err := srv.Orders.FindTradeLifecycle(record.SignalID)
	if err != nil || !found || updated.Status != storage.TradeLifecycleBlocked {
		t.Fatalf("auto reentry lifecycle not blocked: found=%v lifecycle=%#v err=%v", found, updated, err)
	}
	events, err := srv.Orders.ListTradeMonitorEvents(storage.TradeMonitorFilter{Symbol: "NOTTOPUSDT"}, 20)
	if err != nil || len(events) == 0 || events[0].EventType != "auto_reentry_top100_blocked" {
		t.Fatalf("missing top 100 blocked event: events=%#v err=%v", events, err)
	}
}

func postTVOrder(t *testing.T, srv *Server, signal trading.Signal) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(signal)
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/tvorder", bytes.NewReader(body)))
	return rr
}

func upsertTestOKXMarket(t *testing.T, srv *Server, env string, bases ...string) {
	t.Helper()
	set := okxInstrumentSet{Env: env, Demo: env != trading.TradeEnvLive, Instruments: []symbolInstrument{}}
	for i, base := range bases {
		set.Instruments = append(set.Instruments, symbolInstrument{
			Instrument:      okx.Instrument{InstType: "SWAP", InstID: base + "-USDT-SWAP", BaseCcy: base, QuoteCcy: "USDT", SettleCcy: "USDT", State: "live"},
			TurnoverUSDT24h: fmt.Sprintf("%d", len(bases)-i),
		})
	}
	set.Count = len(set.Instruments)
	set.TopInstruments = topOKXInstruments(set.Instruments, config.MarketTurnoverScopeLimit(srv.ConfigStore.Get().Trading.MarketTurnoverScope))
	upsertTestMarketPayload(t, srv, trading.ExchangeOKX, env, set, len(set.Instruments), srv.now())
}

func upsertTestBinanceMarket(t *testing.T, srv *Server, env string, bases ...string) {
	t.Helper()
	set := binanceInstrumentSet{Env: env, Demo: env != trading.TradeEnvLive, Instruments: []binanceSymbolInstrument{}}
	for i, base := range bases {
		set.Instruments = append(set.Instruments, binanceSymbolInstrument{
			SymbolInfo:      binance.SymbolInfo{Symbol: base + "USDT", Pair: base + "USDT", ContractType: "PERPETUAL", Status: "TRADING", BaseAsset: base, QuoteAsset: "USDT", MarginAsset: "USDT"},
			TurnoverUSDT24h: fmt.Sprintf("%d", len(bases)-i),
		})
	}
	set.Count = len(set.Instruments)
	set.TopInstruments = topBinanceInstruments(set.Instruments, config.MarketTurnoverScopeLimit(srv.ConfigStore.Get().Trading.MarketTurnoverScope))
	upsertTestMarketPayload(t, srv, trading.ExchangeBinance, env, set, len(set.Instruments), srv.now())
}

func clearTestMarket(t *testing.T, srv *Server, exchange, env string) {
	t.Helper()
	payload := map[string]any{"env": env, "demo": env != trading.TradeEnvLive, "count": 0, "instruments": []any{}, "top_instruments": []any{}}
	upsertTestMarketPayload(t, srv, exchange, env, payload, 0, time.Time{})
}

func upsertTestMarketPayload(t *testing.T, srv *Server, exchange, env string, payload any, count int, syncedAt time.Time) {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Orders.UpsertSymbolCatalogCaches([]storage.SymbolCatalogCache{{
		Exchange: exchange, PayloadJSON: string(raw), Env: env, Count: count, SyncedAt: syncedAt, AttemptedAt: srv.now(),
	}}); err != nil {
		t.Fatal(err)
	}
}
