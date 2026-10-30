package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/config"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

func TestDashboardRoutesAndValidation(t *testing.T) {
	srv, st := testService(t, config.Config{BasePath: "/liquidation/"}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	defer st.Close()
	w := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 302 || w.Header().Get("Location") != "/liquidation/alerts" {
		t.Fatal(w.Code, w.Header())
	}
	for _, path := range []string{"/bubbles", "/liquidations", "/hedge-wall", "/market-info", "/options", "/menu"} {
		w = httptest.NewRecorder()
		srv.http.Handler.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 || !strings.Contains(w.Body.String(), `href="/liquidation/options"`) || !strings.Contains(w.Body.String(), `content="/liquidation/"`) {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
	for _, path := range []string{"/api/v1/liquidations?minimum=NaN", "/api/v1/liquidations?minimum=-1", "/api/v1/liquidations?side=buy", "/api/v1/liquidations?field=bad", "/api/v1/liquidations?cursor=bad", "/api/v1/hedge-wall?symbol=ETHUSDT&half_life=0", "/api/v1/hedge-wall/history?symbol=ETHUSDT&kind=bad", "/api/v1/market-info?symbol=ETHUSDT&range=100d", "/api/v1/options?hours=0", "/api/v1/options?hours=73", "/api/v1/options?hours=1.5", "/api/v1/options?hours=NaN"} {
		w = httptest.NewRecorder()
		srv.http.Handler.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 400 {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
	at := time.Now().UTC().Add(-time.Second)
	e := domain.LiquidationEvent{ID: "small-sol", Exchange: "binance", Symbol: "SOLUSDT", PositionSide: "short", EventTime: at, ReceivedAt: at, Quantity: 1, Price: 1, NotionalUSD: 1, Coverage: "sampled"}
	if err := st.InsertLiquidation(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/liquidations", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "small-sol") {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestHedgeWallDepthChartIncludesPriceGridAndTopFiveMarkers(t *testing.T) {
	srv, st := testService(t, config.Config{BasePath: "/liquidation/"}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	defer st.Close()
	w := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, httptest.NewRequest("GET", "/assets/pages.js", nil))
	if w.Code != 200 {
		t.Fatalf("pages.js status=%d", w.Code)
	}
	body := w.Body.String()
	for _, marker := range []string{
		"integerStart=Math.ceil(low)",
		"gridStep=integerCount>120",
		"topFive=(rows,side)",
		"ctx.font='bold 14px system-ui'",
		"label=price(item.level.price)",
		"candidateYs=[baseY,baseY-28,baseY+28",
		"ctx.fillStyle='#ffffff'",
	} {
		if !strings.Contains(body, marker) {
			t.Fatalf("depth chart missing %q", marker)
		}
	}
	if strings.Contains(body, "priceRows") || strings.Contains(body, "右侧价格 · Top 5") || strings.Contains(body, "item.side==='bid'?'B':'A'") {
		t.Fatal("depth chart must not render the separate right-side A/B price list or A/B marker labels")
	}
	w = httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, httptest.NewRequest("GET", "/hedge-wall", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "柱线上仅显示 Top 5 价格") || strings.Contains(w.Body.String(), "B/A1–5") {
		t.Fatalf("hedge wall legend missing chart guidance: status=%d", w.Code)
	}
	w = httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, httptest.NewRequest("GET", "/assets/pages.css", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), ".depth-canvas{height:480px}") || !strings.Contains(w.Body.String(), ".depth-canvas{height:396px}") {
		t.Fatalf("depth chart height override missing: status=%d", w.Code)
	}
}

func TestBubbleChartUsesConfiguredMultipleSymbols(t *testing.T) {
	cfg := config.Config{BasePath: "/liquidation/", Symbols: []string{"BTCUSDT", "ETHUSDT", "SOLUSDT", "ETHUSDT"}}
	srv, st := testService(t, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	defer st.Close()
	w := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, httptest.NewRequest("GET", "/bubbles", nil))
	body := w.Body.String()
	if w.Code != 200 || !strings.Contains(body, `data-symbol="BTCUSDT"`) || !strings.Contains(body, `data-symbol="ETHUSDT" class="active"`) || !strings.Contains(body, `id="symbol-search"`) || !strings.Contains(body, `id="symbol-options"`) {
		t.Fatalf("primary buttons or symbol search were not rendered: status=%d", w.Code)
	}
	if strings.Contains(body, `data-symbol="SOLUSDT"`) {
		t.Fatal("non-primary symbols should use the search box instead of shortcut buttons")
	}
	w = httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, httptest.NewRequest("GET", "/assets/dashboard.js", nil))
	if strings.Contains(w.Body.String(), `$('symbol-search').value = symbol`) {
		t.Fatal("symbol search input must start empty")
	}
	w = httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/market?symbol=SOLUSDT&interval=15m", nil))
	if w.Code == 400 || strings.Contains(w.Body.String(), "symbol must be one of") {
		t.Fatalf("configured SOL symbol was rejected: status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestDashboardNavigationUsesScrollableMobileMenu(t *testing.T) {
	srv, st := testService(t, config.Config{BasePath: "/liquidation/"}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	defer st.Close()
	w := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, httptest.NewRequest("GET", "/alerts", nil))
	if w.Code != 200 {
		t.Fatalf("alerts status=%d", w.Code)
	}
	body := w.Body.String()
	w = httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, httptest.NewRequest("GET", "/assets/pages.css", nil))
	if w.Code != 200 {
		t.Fatalf("pages.css status=%d", w.Code)
	}
	styles := w.Body.String()
	for _, marker := range []string{
		`.app-nav{flex:0 0 auto;flex-wrap:nowrap;overflow:visible}`,
		`.app-nav>.app-links{flex:1 1 auto;min-width:0;display:flex;flex-wrap:nowrap;overflow-x:auto;overflow-y:hidden;scrollbar-width:thin}`,
		`@media(max-width:720px){.app-nav{gap:6px;flex-wrap:wrap}`,
		`.app-links{order:3;flex:1 0 100%;flex-basis:100%;min-width:0;scrollbar-width:thin}.app-links a{flex:0 0 auto}`,
		`@media(max-width:720px){.app-nav>#nav-theme{order:-1;min-height:38px;touch-action:manipulation}}`,
	} {
		if !strings.Contains(styles, marker) {
			t.Fatalf("alerts navigation missing mobile scroll override %q", marker)
		}
	}
	for _, marker := range []string{`data-system-menu="true"`, `交易所信息`} {
		if !strings.Contains(body, marker) {
			t.Fatalf("alerts navigation missing %q", marker)
		}
	}
	for _, marker := range []string{
		`data-tab="orders" href="/liquidation/tvbot?tab=orders">信号`,
		`data-tab="positions" href="/liquidation/tvbot?tab=positions">持仓`,
		`data-tab="orderSettings" href="/liquidation/tvbot?tab=orderSettings">下单设置`,
	} {
		if !strings.Contains(body, marker) {
			t.Fatalf("alerts navigation missing restored TVBot route %q", marker)
		}
	}
	if strings.Contains(body, `class="app-brand-name"`) || strings.Contains(body, `class="app-logo"`) || strings.Contains(body, `aria-label="清算墙雷达首页"`) {
		t.Fatal("alerts navigation still contains the removed product name")
	}
}

func TestPagesCSSHasUnifiedButtonMotion(t *testing.T) {
	styles := string(pagesCSS)
	for _, marker := range []string{
		`:where(button, .btn, [role="button"], .kline-back a)`,
		`button:not(:disabled):not([aria-disabled="true"]):not(.is-disabled):hover`,
		`button:not(:disabled):not([aria-disabled="true"]):not(.is-disabled):active`,
		`button:focus-visible`,
		`button[aria-busy="true"]::before`,
		`@keyframes shared-button-spin`,
		`@media(prefers-reduced-motion:reduce)`,
	} {
		if !strings.Contains(styles, marker) {
			t.Fatalf("shared button motion is missing %q", marker)
		}
	}
}

func TestLoginHTMLUsesUnifiedButtonMotion(t *testing.T) {
	page := string(loginHTML)
	for _, marker := range []string{
		`transition:transform .18s ease`,
		`button:hover`,
		`button:active`,
		`button:focus-visible`,
		`@media(prefers-reduced-motion:reduce)`,
	} {
		if !strings.Contains(page, marker) {
			t.Fatalf("login page button motion is missing %q", marker)
		}
	}
}

func TestThemeToggleUsesDelegatedMobileHandler(t *testing.T) {
	if !strings.Contains(string(themeJS), `closest('[data-theme-toggle="true"]')`) {
		t.Fatal("theme asset should delegate clicks from the document for mobile navigation")
	}
	if strings.Contains(string(sharedJS), `themeButton.addEventListener('click'`) {
		t.Fatal("shared asset should not register a second theme toggle handler")
	}
}

func TestBubbleChartHasManualChartHeightControls(t *testing.T) {
	srv, st := testService(t, config.Config{BasePath: "/liquidation/"}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	defer st.Close()
	w := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, httptest.NewRequest("GET", "/bubbles", nil))
	if w.Code != 200 {
		t.Fatalf("bubbles status=%d", w.Code)
	}
	body := w.Body.String()
	for _, marker := range []string{
		"orientation:landscape",
		"max-height:900px",
	} {
		if !strings.Contains(body, marker) {
			t.Fatalf("bubble chart missing television viewport support %q", marker)
		}
	}
	if strings.Index(body, `data-interval="24h"`) > strings.Index(body, `id="liquidation-minimum"`) || strings.Index(body, `id="liquidation-short-offset"`) > strings.Index(body, `id="chart-latest"`) {
		t.Fatal("bubble chart controls are not placed beside their requested buttons")
	}
	for _, marker := range []string{
		"calculateBubbleChartViewportHeight",
		"const bubbleChartDefaultHeight = 874",
		"const bubbleChartViewportScale = 1.3",
		"wrap.getBoundingClientRect().top",
		"chartHeightLimits.minimum",
		"setChartHeight(height, true, render)",
		"scheduleAutomaticMarketBackfill",
		"pageLimit: 3",
		"delayMs: 350",
		"K线自动回填",
	} {
		if !strings.Contains(string(dashboardJS), marker) {
			t.Fatalf("bubble chart script missing television viewport support %q", marker)
		}
	}
	for _, marker := range []string{
		"tvbotAPI",
		"analysis?price_days=1&pnl_days=30",
		"position_trades",
		"Array.isArray(packet?.position_trades) ? packet.position_trades : []",
		"历史开平点",
		"tradeView.historyRows",
		"tradeView.referenceMarkers",
		"collapseTradeMarkers",
	} {
		if !strings.Contains(string(dashboardJS), marker) {
			t.Fatalf("bubble chart script missing historical trade marker support %q", marker)
		}
	}
	if strings.Contains(string(dashboardJS), "K线形态") || strings.Contains(body, "△ 形态") || strings.Contains(body, "空心箭头为K线形态") {
		t.Fatal("bubble chart should not display candlestick pattern labels")
	}
	for _, marker := range []string{"B</b> 开多", "S</b> 开空", "平多</b>", "平空</b>"} {
		if !strings.Contains(body, marker) {
			t.Fatalf("bubble chart legend missing historical trade label %q", marker)
		}
	}
	for _, marker := range []string{
		"window.visualViewport?.addEventListener",
		"new ResizeObserver",
		"requestAnimationFrame(fitBubbleChartViewport)",
	} {
		if strings.Contains(string(dashboardJS), marker) {
			t.Fatalf("bubble chart should not auto-adjust height %q", marker)
		}
	}
	for _, marker := range []string{
		`id="p-up"`,
		`id="p-down"`,
		`id="p-none"`,
		"先触上墙",
		"先触下墙",
		"60分钟均未触及",
	} {
		if strings.Contains(body, marker) {
			t.Fatalf("bubble chart should not render probability card %q", marker)
		}
	}
	for _, marker := range []string{
		`id="chart-height"`,
		`id="chart-height-apply"`,
		`id="chart-height-auto"`,
		`id="chart-resize-handle"`,
		`id="liquidation-minimum"`,
		`id="liquidation-minimum-apply"`,
		`id="liquidation-long-offset"`,
		`id="liquidation-short-offset"`,
		`拖动调整K线图高度`,
	} {
		if !strings.Contains(body, marker) {
			t.Fatalf("bubble chart missing enlarged chart height %q", marker)
		}
	}
	for _, marker := range []string{
		"liquidation.chartHeight",
		"installChartSizeControls",
		"setChartHeight",
		"useAutomaticChartHeight",
		"const rankLabelWidthScale = .8",
	} {
		if !strings.Contains(string(dashboardJS), marker) {
			t.Fatalf("bubble chart missing manual sizing support %q", marker)
		}
	}
}

func TestLiquidationAnalysisRules(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed; run node --test liquidation-analysis.test.cjs separately")
	}
	output, err := exec.Command(node, "--test", "liquidation-analysis.test.cjs", "bubble-chart.test.cjs").CombinedOutput()
	if err != nil {
		t.Fatalf("liquidation analysis rules: %v\n%s", err, output)
	}
}

func TestMarketZeroMinimumAndCappedWindowHistory(t *testing.T) {
	srv, st := testService(t, config.Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	defer st.Close()
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Minute).Add(-time.Minute)
	for _, symbol := range []string{"BTCUSDT", "ETHUSDT"} {
		if err := st.UpsertCandles(ctx, []domain.Candle{{Exchange: "binance", Symbol: symbol, Time: at, Open: 100, High: 101, Low: 99, Close: 100}}); err != nil {
			t.Fatal(err)
		}
		count := 1
		if symbol == "ETHUSDT" {
			count = 5003
		}
		for i := 0; i < count; i++ {
			e := domain.LiquidationEvent{ID: fmt.Sprintf("%s-%05d", symbol, i), Exchange: "binance", Symbol: symbol, PositionSide: "long", EventTime: at.Add(30 * time.Second), ReceivedAt: at, Price: 100, Quantity: .01, NotionalUSD: 1, Coverage: "sampled"}
			if err := st.InsertLiquidation(ctx, e); err != nil {
				t.Fatal(err)
			}
		}
		for _, interval := range []string{"1m", "2m", "3m", "5m", "10m", "15m", "30m", "1h", "4h", "8h", "12h", "24h"} {
			w := httptest.NewRecorder()
			srv.http.Handler.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/market?symbol="+symbol+"&interval="+interval, nil))
			var view domain.MarketView
			if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
				t.Fatal(err)
			}
			want := count
			if want > 5000 {
				want = 5000
			}
			if w.Code != 200 || view.LiquidationMinimumUSD != 0 || len(view.Liquidations) != want || view.LiquidationsTruncated != (count > 5000) {
				t.Fatalf("%s %s: status=%d events=%d truncated=%v minimum=%v", symbol, interval, w.Code, len(view.Liquidations), view.LiquidationsTruncated, view.LiquidationMinimumUSD)
			}
		}
	}
	params := url.Values{"symbol": {"ETHUSDT"}, "side": {"all"}, "field": {"notional_usd"}, "minimum": {"0"}, "limit": {"500"}, "from": {at.Format(time.RFC3339Nano)}, "to": {at.Add(time.Minute).Format(time.RFC3339Nano)}}
	ids := map[string]bool{}
	for pages := 0; ; pages++ {
		if pages > 11 {
			t.Fatal("pagination did not finish")
		}
		w := httptest.NewRecorder()
		srv.http.Handler.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/liquidations?"+params.Encode(), nil))
		var packet struct {
			Data domain.LiquidationPage `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &packet); err != nil || w.Code != 200 {
			t.Fatalf("history status=%d error=%v", w.Code, err)
		}
		for _, row := range packet.Data.Rows {
			if ids[row.ID] {
				t.Fatalf("duplicate ID across timestamp-tied pages: %s", row.ID)
			}
			ids[row.ID] = true
		}
		if packet.Data.NextCursor == "" {
			break
		}
		params.Set("cursor", packet.Data.NextCursor)
	}
	if len(ids) != 5003 {
		t.Fatalf("supplemented IDs=%d", len(ids))
	}
}

// Optional real-browser check uses an isolated SQLite fixture and never starts
// exchange collectors or reads production credentials.
func TestDashboardBrowser(t *testing.T) {
	script := os.Getenv("DASHBOARD_UI_SCRIPT")
	if script == "" {
		t.Skip("DASHBOARD_UI_SCRIPT is not set")
	}
	srv, st := testService(t, config.Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	defer st.Close()
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Minute)
	price, oi, r, zero := 2500., 1000000., 2., 0.
	for _, symbol := range []string{"ETHUSDT", "BTCUSDT"} {
		for i := 0; i < 60; i++ {
			ts := at.Add(-time.Duration(60-i) * time.Minute)
			value := .2 + float64(i)/300
			underlying := "BTC"
			if symbol == "ETHUSDT" {
				value = -value
				underlying = "ETH"
			}
			if err := st.SaveOptionGamma(ctx, domain.OptionGammaPoint{Symbol: symbol, Underlying: underlying, Source: "deribit", Time: ts, Gamma: &value, CallGamma: .01, PutGamma: .005, Contracts: 80, SelectedContracts: 80, EligibleContracts: 100, State: "ok", SourceFrom: ts, SourceTo: ts}); err != nil {
				t.Fatal(err)
			}
		}
		cs := []domain.Candle{}
		for i := 0; i < 1500; i++ {
			ts := at.Add(-time.Duration(1500-i) * time.Minute)
			cs = append(cs, domain.Candle{Exchange: "binance", Symbol: symbol, Time: ts, Open: price, High: price + 1, Low: price - 1, Close: price, VolumeUSD: 1000, TakerBuyUSD: 600})
			if i%5 == 0 {
				if err := st.SaveMarketMetric(ctx, domain.MarketMetric{Symbol: symbol, Time: ts, MarkPrice: &price, OIUSD: &oi, TopPositionRatio: &r, AccountRatio: &r, FundingRate: &zero}); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := st.UpsertCandles(ctx, cs); err != nil {
			t.Fatal(err)
		}
		if err := st.SaveMarketMetric(ctx, domain.MarketMetric{Symbol: symbol, Time: at, MarkPrice: &price, OIUSD: &oi, TopPositionRatio: &r, AccountRatio: &r, FundingRate: &zero}); err != nil {
			t.Fatal(err)
		}
		b := domain.BookSnapshot{Symbol: symbol, Time: at, BucketWidth: .1, BestBid: 2499.9, BestAsk: 2500.1, Bids: []domain.DepthLevel{{Price: 2499.9, Quantity: 200, NotionalUSD: 499980}, {Price: 2499.8, Quantity: 250, NotionalUSD: 624950}}, Asks: []domain.DepthLevel{{Price: 2500.1, Quantity: 180, NotionalUSD: 450018}, {Price: 2500.2, Quantity: 300, NotionalUSD: 750060}}}
		if err := st.SaveBookSnapshot(ctx, b); err != nil {
			t.Fatal(err)
		}
		if err := st.SaveWallEvent(ctx, domain.WallEvent{ID: symbol, Symbol: symbol, Side: "bid", Price: 2499.9, QualifiedAt: at, StartedAt: at.Add(-time.Minute), LastSeen: at, PeakUSD: 500000, DurationMS: 60000}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 65; i++ {
		err := st.InsertLiquidation(ctx, domain.LiquidationEvent{ID: time.Unix(int64(i), 0).String(), Exchange: "binance", Symbol: "SOLUSDT", PositionSide: "long", EventTime: at.Add(-time.Duration(i) * time.Second), ReceivedAt: at, Price: 100, Quantity: 2, NotionalUSD: 200, Coverage: "sampled"})
		if err != nil {
			t.Fatal(err)
		}
	}
	web := httptest.NewServer(srv.http.Handler)
	defer web.Close()
	command := exec.Command("python", script, web.URL)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("browser check: %v\n%s", err, output)
	}
	t.Log(string(output))
}
