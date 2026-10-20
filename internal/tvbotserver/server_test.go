package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotbinance"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotconfig"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotgate"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotokx"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotsecurity"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotstorage"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotupgrade"
)

type fakeExecutor struct {
	calls     chan trading.Signal
	demoFlags chan bool
}

func (f fakeExecutor) ExecuteSignal(ctx context.Context, signal trading.Signal, cfg trading.RuntimeConfig) (trading.OrderResult, error) {
	if f.demoFlags != nil {
		f.demoFlags <- cfg.DemoTradingHeaderEnabled()
	}
	f.calls <- signal
	return trading.OrderResult{InstID: "BTC-USDT-SWAP", ClOrdID: "test", OrdID: "1", OKXCode: "0"}, nil
}

func (f fakeExecutor) Check(ctx context.Context, cfg trading.RuntimeConfig) (map[string]any, error) {
	return map[string]any{"ok": true}, nil
}

type fakeUpgradeRunner struct {
	done chan struct{}
}

type testUserCredentialResolver map[string]UserCredentials

func (r testUserCredentialResolver) ForUser(username string) (UserCredentials, error) {
	credentials, ok := r[username]
	if !ok {
		return UserCredentials{}, fmt.Errorf("unknown test user %q", username)
	}
	return credentials, nil
}

func (f fakeUpgradeRunner) Run(ctx context.Context) (upgrade.Result, error) {
	f.done <- struct{}{}
	return upgrade.Result{
		Steps: []upgrade.Step{{Name: "git_pull", Command: "git pull --ff-only", Output: "ok"}},
	}, nil
}

func TestRoutes(t *testing.T) {
	srv := newTestServer(t)
	root := httptest.NewRecorder()
	srv.ServeHTTP(root, httptest.NewRequest(http.MethodGet, "/", nil))
	if root.Code != http.StatusFound || root.Header().Get("Location") != "https://www.mext.go.jp/" {
		t.Fatalf("root response code=%d location=%q", root.Code, root.Header().Get("Location"))
	}
	bad := httptest.NewRecorder()
	srv.ServeHTTP(bad, httptest.NewRequest(http.MethodGet, "/anything", nil))
	if bad.Code != http.StatusNotFound {
		t.Fatalf("bad path code=%d", bad.Code)
	}
	method := httptest.NewRecorder()
	srv.ServeHTTP(method, httptest.NewRequest(http.MethodGet, "/tvorder", nil))
	if method.Code != http.StatusMethodNotAllowed {
		t.Fatalf("tvorder GET code=%d", method.Code)
	}
	admin := httptest.NewRecorder()
	srv.ServeHTTP(admin, httptest.NewRequest(http.MethodGet, "/tvbot/config", nil))
	if admin.Code != http.StatusUnauthorized {
		t.Fatalf("tvbot without token code=%d", admin.Code)
	}
	if admin.Header().Get("WWW-Authenticate") != "" {
		t.Fatal("tvbot should not trigger browser Basic auth challenge")
	}
	docReq := httptest.NewRequest(http.MethodGet, "/tvbot/", nil)
	docReq.Header.Set("Accept", "text/html")
	doc := httptest.NewRecorder()
	srv.ServeHTTP(doc, docReq)
	if doc.Code != http.StatusFound || !strings.HasPrefix(doc.Header().Get("Location"), "/tvbot/login") {
		t.Fatalf("tvbot document without session code=%d location=%q", doc.Code, doc.Header().Get("Location"))
	}
	basicReq := httptest.NewRequest(http.MethodGet, "/tvbot/config", nil)
	basicReq.SetBasicAuth("admin", "Admin123")
	basic := httptest.NewRecorder()
	srv.ServeHTTP(basic, basicReq)
	if basic.Code != http.StatusOK {
		t.Fatalf("tvbot with basic auth code=%d body=%s", basic.Code, basic.Body.String())
	}
	for _, route := range tvbotViewRoutes {
		if route.Tab == "menuSettings" || route.Tab == "system" || route.Tab == "upgrade" {
			continue
		}
		viewReq := httptest.NewRequest(http.MethodGet, "/tvbot/view/"+route.Slug, nil)
		viewReq.SetBasicAuth("admin", "Admin123")
		view := httptest.NewRecorder()
		srv.ServeHTTP(view, viewReq)
		if view.Code != http.StatusOK || !strings.Contains(view.Body.String(), `data-tab="`+route.Tab+`" href="/tvbot/view/`+route.Slug+`"`) {
			t.Fatalf("tvbot view route %s code=%d body contains expected link=%v", route.Slug, view.Code, strings.Contains(view.Body.String(), `data-tab="`+route.Tab+`" href="/tvbot/view/`+route.Slug+`"`))
		}
	}
	invalidViewReq := httptest.NewRequest(http.MethodGet, "/tvbot/view/not-a-page", nil)
	invalidViewReq.SetBasicAuth("admin", "Admin123")
	invalidView := httptest.NewRecorder()
	srv.ServeHTTP(invalidView, invalidViewReq)
	if invalidView.Code != http.StatusNotFound {
		t.Fatalf("invalid tvbot view code=%d body=%s", invalidView.Code, invalidView.Body.String())
	}
	viewMethodReq := httptest.NewRequest(http.MethodPost, "/tvbot/view/positions", nil)
	viewMethodReq.SetBasicAuth("admin", "Admin123")
	viewMethod := httptest.NewRecorder()
	srv.ServeHTTP(viewMethod, viewMethodReq)
	if viewMethod.Code != http.StatusMethodNotAllowed {
		t.Fatalf("tvbot view POST code=%d body=%s", viewMethod.Code, viewMethod.Body.String())
	}
	monitorReq := httptest.NewRequest(http.MethodGet, "/tvbot/trade-monitor", nil)
	monitorReq.SetBasicAuth("admin", "Admin123")
	monitor := httptest.NewRecorder()
	srv.ServeHTTP(monitor, monitorReq)
	if monitor.Code != http.StatusOK ||
		!bytes.Contains(monitor.Body.Bytes(), []byte(`"fill_monitor"`)) ||
		!bytes.Contains(monitor.Body.Bytes(), []byte(`"auto_reentry"`)) {
		t.Fatalf("trade monitor status code=%d body=%s", monitor.Code, monitor.Body.String())
	}
	loginPage := httptest.NewRecorder()
	srv.ServeHTTP(loginPage, httptest.NewRequest(http.MethodGet, "/tvbot/login?next=/tvbot/config", nil))
	if loginPage.Code != http.StatusOK || !bytes.Contains(loginPage.Body.Bytes(), []byte("管理员登录")) {
		t.Fatalf("login page code=%d body=%s", loginPage.Code, loginPage.Body.String())
	}
	loginReq := httptest.NewRequest(http.MethodPost, "/tvbot/login", strings.NewReader("username=admin&password=Admin123&next=%2Ftvbot%2Fconfig"))
	loginReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	login := httptest.NewRecorder()
	srv.ServeHTTP(login, loginReq)
	if login.Code != http.StatusSeeOther || login.Header().Get("Location") != "/tvbot/config" {
		t.Fatalf("login code=%d location=%q body=%s", login.Code, login.Header().Get("Location"), login.Body.String())
	}
	var sessionCookie *http.Cookie
	for _, cookie := range login.Result().Cookies() {
		if cookie.Name == adminSessionCookieName {
			sessionCookie = cookie
			break
		}
	}
	if sessionCookie == nil || sessionCookie.Value == "" || !sessionCookie.HttpOnly {
		t.Fatalf("login did not set admin session cookie: %#v", login.Result().Cookies())
	}
	cookieReq := httptest.NewRequest(http.MethodGet, "/tvbot/config", nil)
	cookieReq.AddCookie(sessionCookie)
	cookieRR := httptest.NewRecorder()
	srv.ServeHTTP(cookieRR, cookieReq)
	if cookieRR.Code != http.StatusOK {
		t.Fatalf("tvbot with session cookie code=%d body=%s", cookieRR.Code, cookieRR.Body.String())
	}
	uiReq := httptest.NewRequest(http.MethodGet, "/tvbot/", nil)
	uiReq.SetBasicAuth("admin", "Admin123")
	ui := httptest.NewRecorder()
	srv.ServeHTTP(ui, uiReq)
	if ui.Code != http.StatusOK {
		t.Fatalf("tvbot ui code=%d body=%s", ui.Code, ui.Body.String())
	}
	if got := ui.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Fatalf("tvbot ui content-type=%q", got)
	}
	if got := ui.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("tvbot ui cache-control=%q", got)
	}
	if !bytes.Contains(ui.Body.Bytes(), []byte("OKX Bot")) || !bytes.Contains(ui.Body.Bytes(), []byte("/tvbot/config")) {
		t.Fatalf("tvbot ui body does not look like dashboard")
	}
	if !bytes.Contains(ui.Body.Bytes(), []byte("data-rename-active-api-id")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("renameActiveAPIID")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte(`method: "PATCH"`)) {
		t.Fatalf("tvbot ui should include API ID rename controls")
	}
	if !bytes.Contains(ui.Body.Bytes(), []byte(`id="order-info-status" hidden`)) ||
		!bytes.Contains(ui.Body.Bytes(), []byte(".status[hidden]")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("syncOrderInfoVisibility")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte(`tabID !== "orderSettings"`)) {
		t.Fatalf("tvbot ui should only show order info status on order settings tab")
	}
	if bytes.Contains(ui.Body.Bytes(), []byte("max-width: 1240px")) || !bytes.Contains(ui.Body.Bytes(), []byte("Asia/Shanghai")) {
		t.Fatalf("tvbot ui should use full-width layout and Shanghai order times")
	}
	if !bytes.Contains(ui.Body.Bytes(), []byte("订单分析")) {
		t.Fatalf("tvbot ui should include order analysis tab")
	}
	if !bytes.Contains(ui.Body.Bytes(), []byte("持仓")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("/tvbot/positions")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("/tvbot/pending-orders")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("position-exchange-summary")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("positionExchanges")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("position-rows")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("positions-table")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte(".positions-table td")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("font-size: 12px")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("pos-actions-col")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte(".positions-table .pos-actions-col { width: 23.3%; }")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("pos-position-amount-col")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("pos-entry-time-col")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("pos-holding-time-col")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("仓位金额")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte(`title: "下单时间"`)) ||
		!bytes.Contains(ui.Body.Bytes(), []byte(`title: "持仓时间"`)) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("displayInstID(row.instId)")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("formatFixed2(row.margin)")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("positionAmount(row)")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("positionReturnRatio")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("const uplRatio = positionNumber(row ? row.uplRatio : null);")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("if (uplRatio !== null) return uplRatio;")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("formatHoldingSeconds")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("positionEntryTimeCell")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("交易所持仓时间")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("upl / Math.abs(margin)")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte(`{ label: "10%", ratio: "0.1" }`)) ||
		!bytes.Contains(ui.Body.Bytes(), []byte(`data-position-ratio`)) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("position-percent-close-btn")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("width: 36px")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("border-radius: 999px")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("font-size: 7px")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte(".positions-table .position-actions")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("gap: 6px")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("missingPositionEntrySyncIntervalMs = 180000")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("positionEntryTimesMissing")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("syncMissingPositionEntryTimes")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte(`qs.set("refresh", "true")`)) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("loadPositionView(true)")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("positionTableColumnDefs")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte(`tableColumnCount("positions")`)) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("pending-order-rows")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("pending-margin-col")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("pending-age-col")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("pending-actions-col")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("pendingOrderTableColumnDefs")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte(`tableColumnCount("pending_orders")`)) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("当前挂单")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("pending-order-switch")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte(`data-pending-order-group="normal" aria-selected="true"`)) ||
		!bytes.Contains(ui.Body.Bytes(), []byte(`data-pending-order-group="algo"`)) ||
		!bytes.Contains(ui.Body.Bytes(), []byte(`pendingOrderGroup: "normal"`)) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("pendingOrderDisplayRows")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("renderPendingOrders")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("pendingOrdersSummaryText")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("normal_count")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("algo_count")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("total_count")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("algo_orders")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("OKX 普通单")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("算法订单")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("Binance 普通单")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("算法单")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("renderTableStructure")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("saveTableColumnOrder")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("initTableColumnDrag")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("data-table-columns")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("table_columns")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("pending_orders")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("委托价格")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("中间价")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("保证金")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("挂单计时")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("pendingOrderAgeCell")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("formatPendingOrderAgeSeconds")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("追单")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("停止追单")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("取消")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("data-pending-cancel")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("data-order-group")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("data-algo-id")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("algo_cl_ord_id")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("pendingOrderPriceText")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("/tvbot/pending-orders/chase")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("/tvbot/pending-orders/chase/stop")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("/tvbot/pending-orders/cancel")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("signed-profit")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("signed-loss")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("signedCell")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("positionSideKind")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("positionSideCell")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("positionDirectionLabel")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("orderHistoryDirectionText")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("pendingOrderDirectionText")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("开仓")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("平仓")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("多单")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("空单")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("市价平仓")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("限价平仓")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("/tvbot/positions/close")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("data-position-close")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte(`title: "操作"`)) ||
		bytes.Contains(ui.Body.Bytes(), []byte(`<th>可用</th>`)) ||
		bytes.Contains(ui.Body.Bytes(), []byte("pos-available-col")) ||
		bytes.Contains(ui.Body.Bytes(), []byte("<th>保证金模式</th>")) ||
		bytes.Contains(ui.Body.Bytes(), []byte("<th>强平价</th>")) ||
		bytes.Contains(ui.Body.Bytes(), []byte("<th>客户端 ID</th>")) ||
		bytes.Contains(ui.Body.Bytes(), []byte("<th>更新时间</th>")) ||
		bytes.Contains(ui.Body.Bytes(), []byte("挂单数 ")) {
		t.Fatalf("tvbot ui should include current positions tab")
	}
	if !bytes.Contains(ui.Body.Bytes(), []byte("币对配置")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("订单配置")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("/tvbot/symbols")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("symbol-exchange")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("symbol-turnover-scope")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte(`value="top50"`)) ||
		!bytes.Contains(ui.Body.Bytes(), []byte(`value="top100"`)) ||
		!bytes.Contains(ui.Body.Bytes(), []byte(`value="top200"`)) ||
		!bytes.Contains(ui.Body.Bytes(), []byte(`value="top500"`)) ||
		!bytes.Contains(ui.Body.Bytes(), []byte(`value="all"`)) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("market_turnover_scope")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("saveMarketTurnoverScope")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("symbol-search-label-text")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("搜索成交量前 100 币对")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("clear-symbol-search")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("没有匹配的币对")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("symbolSearchCompact")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("symbolSearchMatches")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("symbolSearchFields")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("Binance")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("交易所 / 环境")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("今日累计成交金额")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("symbolTableColumnDefs")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("nav button {")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte(".symbol-catalog-table th,")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("padding: 7px 6px")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte(".symbol-catalog-table .symbol-template-btn")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("data-symbol-sort")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("symbol-head")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("symbol-cols")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("生成报警")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("symbolTemplateButtonCell")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("data-symbol-template")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("data-template-exchange")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("data-template-env")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("data-template-symbol")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("templatePageURLFromButton")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("openTemplateFromSymbolButton")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte(`window.open(templatePageURLFromButton(button), "_blank")`)) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("data.binance")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("formatSymbolTurnover")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("setSymbolSort")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte(`symbols: currentTableColumnOrder("symbols")`)) {
		t.Fatalf("tvbot ui should include symbol and order config tabs")
	}
	if !bytes.Contains(ui.Body.Bytes(), []byte("成交监听")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("/tvbot/trade-monitor")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("trade-monitor-lifecycles")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("refresh-trade-monitor")) {
		t.Fatalf("tvbot ui should include trade monitor tab")
	}
	if !bytes.Contains(ui.Body.Bytes(), []byte("菜单设置")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("menu-settings-rows")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("saveMenuSettings")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("applyMenuSettings")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("<th>首页</th>")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("menu-default-tab")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("data-menu-home")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("default_tab")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("data-menu-label")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("data-menu-hidden")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("data-menu-move")) {
		t.Fatalf("tvbot ui should include menu settings tab")
	}
	if !bytes.Contains(ui.Body.Bytes(), []byte("formatUpgradeLog")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("appendUpgradeBlock")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("finished status=")) ||
		bytes.Contains(ui.Body.Bytes(), []byte("JSON.stringify(state.upgrade")) {
		t.Fatalf("tvbot ui should render upgrade status as line-oriented logs")
	}
	if !bytes.Contains(ui.Body.Bytes(), []byte("订单类型")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte(`id="order-type"`)) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("市价单")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("限价单")) {
		t.Fatalf("tvbot ui should include market/limit order type setting")
	}
	if !bytes.Contains(ui.Body.Bytes(), []byte("USDT估值")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("USDT余额")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("/tvbot/balances/overview")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("global-exchange-switch")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte(`data-global-exchange="okx"`)) ||
		!bytes.Contains(ui.Body.Bytes(), []byte(`data-global-exchange="binance"`)) ||
		!bytes.Contains(ui.Body.Bytes(), []byte(`data-exchange-view="okx"`)) ||
		!bytes.Contains(ui.Body.Bytes(), []byte(`data-exchange-view="binance"`)) ||
		!bytes.Contains(ui.Body.Bytes(), []byte(`globalExchangeStorageKey = "tvbot.selectedExchange"`)) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("OKX USDT 余额")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("Binance USDT 余额")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("订单时间")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte(`binance_api_id`)) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("OKX 订单")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("Binance 订单")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("USDT 估值表")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("USDT 权益图")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte(`data-balance-minutes="129600"`)) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("重置基准")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("同步历史")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("overview-okx-usdt-chart")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("overview-binance-usdt-chart")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("analysis-usdt-eq")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("#analysis .mini-usdt-chart {\n      height: 360px;")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("#analysis .mini-usdt-chart { height: 346px; }")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("balance-pnl-block")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("analysis-okx-net-pnl")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("analysis-binance-net-pnl")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("analysisBalanceRefreshIntervalMs = 60000")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("refreshAnalysisBalanceOverviewAuto")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("visibilitychange")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("formatPriceAmount")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("formatQuantityAmount")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("symbolPrecisions")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("analysisPNLWindowMinutes")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("pnl_minutes")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("下单盈亏分析")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("analysis-trade-rows")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("analysis-trade-head")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("analysis-trade-cols")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("analysis-trade-page-info")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("min-width: 1560px")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("analysisTradePageSize = 20")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte(`analysisTradeColumnStorageKey = "tvbot.analysisTradeColumns.v3"`)) ||
		!bytes.Contains(ui.Body.Bytes(), []byte(`window.localStorage.getItem(analysisTradeColumnStorageKey)`)) ||
		!bytes.Contains(ui.Body.Bytes(), []byte(`window.localStorage.setItem(analysisTradeColumnStorageKey`)) ||
		!bytes.Contains(ui.Body.Bytes(), []byte(`data-analysis-trade-column`)) ||
		!bytes.Contains(ui.Body.Bytes(), []byte(`cols.innerHTML = columns.map`)) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("handleAnalysisTradeColumnDrop")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("analysis-exit-time-col")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("analysis-inst-id-col")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("analysis-side-col")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte(`title: "保证金"`)) ||
		!bytes.Contains(ui.Body.Bytes(), []byte(`title: "杠杆"`)) ||
		!bytes.Contains(ui.Body.Bytes(), []byte(`title: "开仓价"`)) ||
		!bytes.Contains(ui.Body.Bytes(), []byte(`title: "平仓价"`)) ||
		!bytes.Contains(ui.Body.Bytes(), []byte(`title: "盈亏比"`)) ||
		!bytes.Contains(ui.Body.Bytes(), []byte(`title: "成交额"`)) ||
		!bytes.Contains(ui.Body.Bytes(), []byte(`title: "净盈亏"`)) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("analysisAmountText")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte(`ccy.toUpperCase() !== "USDT"`)) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("analysisInstIDText")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("analysisPnLRatioText")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("analysisPositionSideToneClass")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("renderAnalysisTradeHistory")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("exchange: activeExchange()")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("loadPositionExchange(activeExchange()")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("loadPendingOrdersExchange(activeExchange()")) {
		t.Fatalf("tvbot ui should include exchange balance analysis")
	}
	for _, removed := range [][]byte{
		[]byte("analysis-avail-eq"),
		[]byte("analysis-adj-eq"),
		[]byte("analysis-asset-count"),
		[]byte("analysis-binance-avail"),
		[]byte(`id="analysis-binance-api"`),
		[]byte(`id="analysis-okx-api-id"`),
		[]byte(`id="analysis-binance-api-id"`),
		[]byte(`id="refresh-analysis"`),
		[]byte("刷新分析"),
		[]byte("<tr><th>时间</th><th>币对</th><th>方向</th><th>成交价</th><th>数量</th><th>盈亏</th><th>手续费</th><th>订单号</th><th>成交笔数</th></tr>"),
		[]byte("analysis-trade-history-section"),
		[]byte("OKX 盈亏分析"),
		[]byte("Binance 盈亏分析"),
		[]byte("analysis-okx-symbol-status"),
		[]byte("analysis-binance-symbol-status"),
		[]byte("analysis-okx-rows"),
		[]byte("analysis-binance-rows"),
		[]byte("analysis-symbol-table"),
		[]byte("analysis-table-wrap"),
		[]byte("analysis-balance-rows"),
		[]byte("analysis-binance-balance-rows"),
		[]byte("balance-table-wrap"),
		[]byte("balanceRowsHTML"),
		[]byte(`id="analysis-api-id"`),
		[]byte(`交易 API<select id="analysis-api-id"`),
	} {
		if bytes.Contains(ui.Body.Bytes(), removed) {
			t.Fatalf("tvbot analysis balance UI should not include removed metric %q", removed)
		}
	}
	if !bytes.Contains(ui.Body.Bytes(), []byte("chart-grid")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("chartTimeLabel")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("chartTickIndexes")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("usdtBalancePoints")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("const raw = row && row.eq")) {
		t.Fatalf("tvbot ui should render equity chart grid and time axis")
	}
	if !bytes.Contains(ui.Body.Bytes(), []byte("账户名称")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte(`data-api-key-exchange="okx"`)) ||
		!bytes.Contains(ui.Body.Bytes(), []byte(`data-api-key-exchange="binance"`)) ||
		!bytes.Contains(ui.Body.Bytes(), []byte(`data-api-key-exchange="gate"`)) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("Binance API Key")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("交易所 / 返回")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("信号来源")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("下单去向")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("raw-json-dialog")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("data-order-json-index")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("showOrderRawJSON")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("copy-raw-json")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("Webhook URL")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("template-webhook-url")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("copy-webhook-url")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("报警标题")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("template-title")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("copy-template-title")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("templateAlertTitle")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("renderTemplateTitle")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte(`new URL("/tvorder"`)) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("target_exchange")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("trade_env")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("tpl-target-exchange")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("tpl-trade-env")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("tpl-coinpair")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("tpl-coinpair-list")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("tpl-direction")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("多空都做")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("只做多")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("只做空")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("applyTemplateQueryParams")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("tabFromViewPath")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte(`$("tpl-target-exchange").value = normalizeExchange(params.get("target_exchange")`)) ||
		!bytes.Contains(ui.Body.Bytes(), []byte(`$("tpl-trade-env").value = params.get("trade_env")`)) ||
		!bytes.Contains(ui.Body.Bytes(), []byte(`$("tpl-coinpair").value = symbol`)) ||
		!bytes.Contains(ui.Body.Bytes(), []byte(`url.search = params.toString()`)) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("await makeTemplate()")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("templateCoinpairOptions")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("syncSymbols")) ||
		bytes.Contains(ui.Body.Bytes(), []byte("position-exchange\"><option")) ||
		bytes.Contains(ui.Body.Bytes(), []byte("position-api-id")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("order-okx")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("order-status")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("width: 8.6%;")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("order-status-actions")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("flex-wrap: nowrap")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("white-space: nowrap")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("apiDisplayName")) {
		t.Fatalf("tvbot ui should render exchange-aware API, template and order history controls")
	}
	if !bytes.Contains(ui.Body.Bytes(), []byte("activateTab")) ||
		bytes.Contains(ui.Body.Bytes(), []byte("location.hash")) ||
		bytes.Contains(ui.Body.Bytes(), []byte("history.replaceState")) ||
		bytes.Contains(ui.Body.Bytes(), []byte("tvbot#")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("configuredDefaultTab")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("effectiveDefaultTab")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("syncActiveTabAfterMenuSettings")) ||
		bytes.Contains(ui.Body.Bytes(), []byte("tvbot.active_tab")) {
		t.Fatalf("tvbot ui should use hash/default tab navigation without localStorage override")
	}
	if !bytes.Contains(ui.Body.Bytes(), []byte("USDT 可用")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("usdt_balance")) {
		t.Fatalf("tvbot ui should render USDT balance after API tests")
	}
	if !bytes.Contains(ui.Body.Bytes(), []byte("Code by Yuhao@jiansutech.com - 2026-07-24 11:00:00 - testhash - testbranch")) {
		t.Fatalf("tvbot ui should include build footer")
	}
	for _, removed := range [][]byte{
		[]byte(`{ label: "平10%"`),
		[]byte(`{ label: "平25%"`),
		[]byte(`{ label: "平50%"`),
		[]byte(`{ label: "平75%"`),
	} {
		if bytes.Contains(ui.Body.Bytes(), removed) {
			t.Fatalf("tvbot position percent close button should not include removed text %q", removed)
		}
	}
	if !bytes.Contains(ui.Body.Bytes(), []byte("data-retry-id")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("/retry")) {
		t.Fatalf("tvbot ui should include retry controls")
	}
	if !bytes.Contains(ui.Body.Bytes(), []byte("自动扫描持仓")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("position-monitor-okx-enabled")) ||
		!bytes.Contains(ui.Body.Bytes(), []byte("position_monitor")) {
		t.Fatalf("tvbot ui should render position monitor controls and config payload")
	}
}

func TestTVBotUIOrderHistorySearchControls(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/tvbot/", nil)
	req.SetBasicAuth("admin", "Admin123")
	resp := httptest.NewRecorder()
	srv.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("tvbot ui code=%d body=%s", resp.Code, resp.Body.String())
	}
	body := resp.Body.Bytes()
	for _, marker := range [][]byte{
		[]byte(`class="order-history-actions"`),
		[]byte(`id="order-status-filter"`),
		[]byte(`<option value="">全部状态</option>`),
		[]byte(`<option value="submitted">已下单</option>`),
		[]byte(`<option value="ignored">已忽略</option>`),
		[]byte(`<option value="failed">下单失败</option>`),
		[]byte(`<option value="rejected">已拒绝</option>`),
		[]byte(`<option value="duplicate">重复信号</option>`),
		[]byte(`<option value="accepted">处理中</option>`),
		[]byte(`id="order-search"`),
		[]byte(`placeholder="币对 / 金额 / 订单号"`),
		[]byte(`id="search-orders"`),
		[]byte(`id="clear-order-search"`),
		[]byte(`class="order-target">下单去向`),
		[]byte(`td class="order-target"`),
		[]byte(`class="order-signal-route">信号执行`),
		[]byte(`class="order-adx">ADX`),
		[]byte(`class="order-market">市场类型`),
		[]byte(`td class="order-signal-route"`),
		[]byte(`td class="order-adx"`),
		[]byte(`td class="order-market"`),
		[]byte(`class="orders-table"`),
		[]byte(`colspan="12"`),
		[]byte(`function orderHistorySignalRouteText(order)`),
		[]byte(`return source === executed ? "同步信号" : "反转信号"`),
		[]byte(`function orderHistoryADXText(order)`),
		[]byte(`value.toFixed(2)`),
		[]byte(`function orderHistoryMarketText(order)`),
		[]byte(`if (strategy === "trend") return "单边市场"`),
		[]byte(`if (strategy === "scalp") return "震荡市场"`),
		[]byte(`if (strategy === "transition") return "过渡市场"`),
		[]byte(`if (adx >= 25) return "单边市场"`),
		[]byte(`ordersSearch: ""`),
		[]byte(`ordersStatus: ""`),
		[]byte(`qs.set("q", state.ordersSearch)`),
		[]byte(`qs.set("status", state.ordersStatus)`),
		[]byte(`function applyOrderSearch()`),
		[]byte(`function applyOrderStatusFilter()`),
		[]byte(`id="ignored-coinpair"`),
		[]byte(`id="add-ignored-coinpair"`),
		[]byte(`id="ignored-coinpair-list"`),
		[]byte(`id="coinpair-cooldown-list"`),
		[]byte(`动态冷静期（按来源生效）`),
		[]byte(`function loadCoinpairBlocks()`),
		[]byte(`function renderCoinpairCooldownList()`),
		[]byte(`data-cooldown-keyword`),
		[]byte(`function removeCoinpairCooldown(button)`),
		[]byte(`method: "DELETE"`),
		[]byte(`已移出冷静期`),
		[]byte(`coinpairBlockPollIntervalMs = 20000`),
		[]byte(`function coinpairCooldownPriceLabel(source)`),
		[]byte(`价格待确认`),
		[]byte(`data-ignored-coinpair-index`),
		[]byte(`ignored_coinpairs: keywords`),
		[]byte(`function addIgnoredCoinpair()`),
		[]byte(`function removeIgnoredCoinpair(index)`),
		[]byte(`只过滤开仓信号`),
		[]byte(`orderHistoryStatusText`),
		[]byte(`function orderCanRetry(order)`),
		[]byte(`function unsupportedOKXInstrumentMessage(order)`),
		[]byte(`order.status === "failed" || order.status === "ignored"`),
		[]byte(`OKX " + tradeEnv + "不支持 " + instID + "，无法重试`),
		[]byte(`重试仍被忽略`),
		[]byte(`重试信号重复`),
	} {
		if !bytes.Contains(body, marker) {
			t.Fatalf("tvbot ui missing order search marker %q", marker)
		}
	}
	if bytes.Contains(body, []byte(`id="clear-ignored-coinpair"`)) || bytes.Contains(body, []byte(`function clearIgnoredCoinpair()`)) {
		t.Fatal("tvbot UI should only remove ignored coinpairs one at a time")
	}
	if bytes.Contains(body, []byte(`确认提前解除`)) {
		t.Fatal("cooldown remove should not show a confirmation prompt")
	}
	retryStart := bytes.Index(body, []byte(`async function retryOrder(signalID)`))
	retryEnd := bytes.Index(body, []byte(`async function closePosition(button)`))
	if retryStart < 0 || retryEnd <= retryStart {
		t.Fatal("retry function boundaries are missing")
	}
	if bytes.Contains(body[retryStart:retryEnd], []byte(`confirm(`)) {
		t.Fatal("order retry should execute without a confirmation dialog")
	}
}

func TestTVBotUIAnalysisManualCooldownControls(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/tvbot/", nil)
	req.SetBasicAuth("admin", "Admin123")
	resp := httptest.NewRecorder()
	srv.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("tvbot ui code=%d body=%s", resp.Code, resp.Body.String())
	}
	body := resp.Body.Bytes()
	for _, marker := range [][]byte{
		[]byte(`renderHTML: (row) => analysisCooldownCellHTML(row)`),
		[]byte(`class="btn small analysis-cooldown-btn"`),
		[]byte(`data-analysis-cooldown="true"`),
		[]byte(`function addAnalysisCoinpairCooldown(button)`),
		[]byte(`window.confirm(`),
		[]byte(`"/tvbot/coinpair-blocks"`),
		[]byte(`trigger_price: button.dataset.triggerPrice`),
		[]byte(`冷静中`),
		[]byte(`手动冷静期`),
		[]byte(`function coinpairCooldownPriceLabel(source)`),
		[]byte(`activeTab !== "orders" && activeTab !== "analysis"`),
	} {
		if !bytes.Contains(body, marker) {
			t.Fatalf("tvbot ui missing analysis cooldown marker %q", marker)
		}
	}
}

func TestTVBotConfigSavesIgnoredCoinpairListAndSupportsLegacyPatch(t *testing.T) {
	srv := newTestServer(t)
	put := func(tradingPatch map[string]any) config.Config {
		t.Helper()
		body, err := json.Marshal(map[string]any{"trading": tradingPatch})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPut, "/tvbot/config", bytes.NewReader(body))
		req.SetBasicAuth("admin", "Admin123")
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("config status=%d body=%s", rr.Code, rr.Body.String())
		}
		var cfg config.Config
		if err := json.Unmarshal(rr.Body.Bytes(), &cfg); err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	cfg := put(map[string]any{"ignored_coinpairs": []string{" syrup ", "ETH-USDT", "ethusdt", "btc"}})
	if !reflect.DeepEqual(cfg.Trading.IgnoredCoinpairs, []string{"SYRUP", "ETH-USDT", "BTC"}) || cfg.Trading.IgnoredCoinpair != "SYRUP" {
		t.Fatalf("ignored coinpair list not normalized: %#v", cfg.Trading)
	}
	cfg = put(map[string]any{"ignored_coinpair": " sol "})
	if !reflect.DeepEqual(cfg.Trading.IgnoredCoinpairs, []string{"SOL"}) || cfg.Trading.IgnoredCoinpair != "SOL" {
		t.Fatalf("legacy ignored coinpair patch not supported: %#v", cfg.Trading)
	}
	cfg = put(map[string]any{"ignored_coinpairs": []string{}})
	if len(cfg.Trading.IgnoredCoinpairs) != 0 || cfg.Trading.IgnoredCoinpair != "" {
		t.Fatalf("ignored coinpair list not cleared: %#v", cfg.Trading)
	}
}

func TestTVBotConfigUpdatesLossCooldownAndClearsAutomaticBlocks(t *testing.T) {
	srv := newTestServer(t)
	now := srv.now()
	if _, _, err := srv.recordCoinpairCooldown("manual:1", "analysis_manual", trading.ExchangeOKX, "main", "2500", now, "ETHUSDT"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := srv.recordCoinpairCooldown("loss:1", "exchange_fill", trading.ExchangeOKX, "main", "49000", now, "BTCUSDT"); err != nil {
		t.Fatal(err)
	}

	put := func(payload string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPut, "/tvbot/config", strings.NewReader(payload))
		req.SetBasicAuth("admin", "Admin123")
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		return rr
	}
	rr := put(`{"trading":{"loss_cooldown":{"enabled":false,"hours":12}}}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("disable loss cooldown status=%d body=%s", rr.Code, rr.Body.String())
	}
	var cfg config.Config
	if err := json.Unmarshal(rr.Body.Bytes(), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Trading.LossCooldown.Enabled || cfg.Trading.LossCooldown.Hours != 12 {
		t.Fatalf("bad saved loss cooldown config: %#v", cfg.Trading.LossCooldown)
	}
	blocks, err := srv.Orders.ListActiveCoinpairBlocks(now)
	if err != nil || len(blocks) != 1 || blocks[0].Keyword != "ETH" || blocks[0].Source != "analysis_manual" {
		t.Fatalf("disabling loss cooldown should retain only manual blocks: blocks=%#v err=%v", blocks, err)
	}
	if _, blocked, err := srv.activeCoinpairCooldown(trading.Signal{Coinpair: "BTCUSDT", PositionEffect: trading.PositionEffectOpen}, now); err != nil || blocked {
		t.Fatalf("disabled loss cooldown should not block automatic rules: blocked=%v err=%v", blocked, err)
	}
	if invalid := put(`{"trading":{"loss_cooldown":{"hours":8761}}}`); invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid loss cooldown hours status=%d body=%s", invalid.Code, invalid.Body.String())
	}
}

func TestTVBotUIIncludesLossCooldownControls(t *testing.T) {
	for _, marker := range []string{
		`id="loss-cooldown-enabled"`,
		`id="loss-cooldown-hours"`,
		`id="save-loss-cooldown"`,
		`function saveLossCooldownSettings()`,
		`loss_cooldown: { enabled: enabled.checked, hours: value }`,
	} {
		if !strings.Contains(tvbotHTML, marker) {
			t.Fatalf("tvbot UI missing loss cooldown marker %q", marker)
		}
	}
}

func TestPositionActionsExcludeProtectionButtons(t *testing.T) {
	start := strings.Index(tvbotHTML, "function positionActionCell(row)")
	end := strings.Index(tvbotHTML, "function positionEntryTimeTitle(row)")
	if start < 0 || end < 0 || end <= start {
		t.Fatal("position action cell boundaries are missing")
	}
	actions := tvbotHTML[start:end]
	for _, removed := range []string{
		"data-position-protection",
		"position-protection-btn",
		">止盈</button>",
		">止损</button>",
		">移动</button>",
	} {
		if strings.Contains(actions, removed) {
			t.Fatalf("position action cell should not include %q", removed)
		}
	}
	for _, kept := range []string{"data-position-ratio", "data-position-close=\"market\"", "data-position-close=\"limit\""} {
		if !strings.Contains(actions, kept) {
			t.Fatalf("position action cell should retain %q", kept)
		}
	}
}

func TestTVBotConfigSavesOrderType(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest(http.MethodPut, "/tvbot/config", bytes.NewReader([]byte(`{"trading":{"order_type":"limit"}}`)))
	req.SetBasicAuth("admin", "Admin123")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("config status=%d body=%s", rr.Code, rr.Body.String())
	}
	var cfg config.Config
	if err := json.Unmarshal(rr.Body.Bytes(), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Trading.OrderType != string(trading.OrderTypeLimit) || cfg.OrderSettings().OrderType != trading.OrderTypeLimit {
		t.Fatalf("order type not saved: %#v", cfg.Trading)
	}

	badReq := httptest.NewRequest(http.MethodPut, "/tvbot/config", bytes.NewReader([]byte(`{"trading":{"order_type":"post_only"}}`)))
	badReq.SetBasicAuth("admin", "Admin123")
	bad := httptest.NewRecorder()
	srv.ServeHTTP(bad, badReq)
	if bad.Code != http.StatusBadRequest || !bytes.Contains(bad.Body.Bytes(), []byte("unsupported order_type")) {
		t.Fatalf("bad order type status=%d body=%s", bad.Code, bad.Body.String())
	}
}

func TestTVBotConfigSavesMarketTurnoverScopeAndRejectsInvalidValue(t *testing.T) {
	srv := newTestServer(t)
	for _, scope := range []string{"top50", "top100", "top200", "top500", "all"} {
		req := httptest.NewRequest(http.MethodPut, "/tvbot/config", bytes.NewReader([]byte(`{"trading":{"market_turnover_scope":"`+scope+`"}}`)))
		req.SetBasicAuth("admin", "Admin123")
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("scope %s status=%d body=%s", scope, rr.Code, rr.Body.String())
		}
		var cfg config.Config
		if err := json.Unmarshal(rr.Body.Bytes(), &cfg); err != nil {
			t.Fatal(err)
		}
		if cfg.Trading.MarketTurnoverScope != scope || srv.ConfigStore.Get().Trading.MarketTurnoverScope != scope {
			t.Fatalf("scope %s not persisted: response=%q store=%q", scope, cfg.Trading.MarketTurnoverScope, srv.ConfigStore.Get().Trading.MarketTurnoverScope)
		}
	}

	badReq := httptest.NewRequest(http.MethodPut, "/tvbot/config", bytes.NewReader([]byte(`{"trading":{"market_turnover_scope":"top75"}}`)))
	badReq.SetBasicAuth("admin", "Admin123")
	bad := httptest.NewRecorder()
	srv.ServeHTTP(bad, badReq)
	if bad.Code != http.StatusBadRequest || !bytes.Contains(bad.Body.Bytes(), []byte(`"error":"invalid_config"`)) {
		t.Fatalf("invalid scope status=%d body=%s", bad.Code, bad.Body.String())
	}
}

func TestTVBotConfigSavesPositionMonitor(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest(http.MethodPut, "/tvbot/config", bytes.NewReader([]byte(`{"trading":{"position_monitor":{"okx_enabled":true,"binance_enabled":true,"poll_interval_seconds":300,"take_profit_pct":5,"stop_loss_pct":8}}}`)))
	req.SetBasicAuth("admin", "Admin123")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("config status=%d body=%s", rr.Code, rr.Body.String())
	}
	var cfg config.Config
	if err := json.Unmarshal(rr.Body.Bytes(), &cfg); err != nil {
		t.Fatal(err)
	}
	monitor := cfg.Trading.PositionMonitor
	if !monitor.OKXEnabled || !monitor.BinanceEnabled || monitor.PollIntervalSeconds != 300 || monitor.TakeProfitPct != 5 || monitor.StopLossPct != 8 {
		t.Fatalf("position monitor not saved: %#v", monitor)
	}

	badReq := httptest.NewRequest(http.MethodPut, "/tvbot/config", bytes.NewReader([]byte(`{"trading":{"position_monitor":{"okx_enabled":true,"poll_interval_seconds":0,"take_profit_pct":5,"stop_loss_pct":8}}}`)))
	badReq.SetBasicAuth("admin", "Admin123")
	bad := httptest.NewRecorder()
	srv.ServeHTTP(bad, badReq)
	if bad.Code != http.StatusBadRequest || !bytes.Contains(bad.Body.Bytes(), []byte("position_monitor.poll_interval_seconds")) {
		t.Fatalf("bad position monitor status=%d body=%s", bad.Code, bad.Body.String())
	}
}

func TestTVBotConfigSavesMenuSettings(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest(http.MethodPut, "/tvbot/config", bytes.NewReader([]byte(`{
		"ui": {
			"default_tab": "orders",
			"menu_items": [
				{"tab":"orders","hidden":true},
				{"tab":"dashboard","hidden":false,"label":"首页"},
				{"tab":"menuSettings","hidden":true,"label":"菜单管理"},
				{"tab":"upgrade","hidden":false,"label":"   "},
				{"tab":"apiKeys","hidden":false,"label":"下单设置"},
				{"tab":"unknown","hidden":false}
			]
		}
	}`)))
	req.SetBasicAuth("admin", "Admin123")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("config status=%d body=%s", rr.Code, rr.Body.String())
	}
	var cfg config.Config
	if err := json.Unmarshal(rr.Body.Bytes(), &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.UI.MenuItems) != len(config.DefaultMenuTabs) {
		t.Fatalf("menu items should be normalized to defaults: %#v", cfg.UI.MenuItems)
	}
	if cfg.UI.DefaultTab != "orders" {
		t.Fatalf("default tab should be saved: %#v", cfg.UI)
	}
	if cfg.UI.MenuItems[0].Tab != "orders" || !cfg.UI.MenuItems[0].Hidden {
		t.Fatalf("first menu item should preserve hidden orders: %#v", cfg.UI.MenuItems[0])
	}
	if cfg.UI.MenuItems[1].Tab != "dashboard" || cfg.UI.MenuItems[1].Hidden {
		t.Fatalf("second menu item should preserve visible dashboard: %#v", cfg.UI.MenuItems[1])
	}
	if cfg.UI.MenuItems[1].Label != "首页" {
		t.Fatalf("custom menu label should be saved: %#v", cfg.UI.MenuItems[1])
	}
	if cfg.UI.MenuItems[2].Tab != config.MenuSettingsTab || cfg.UI.MenuItems[2].Hidden || cfg.UI.MenuItems[2].Label != "菜单管理" {
		t.Fatalf("menu settings should be forced visible: %#v", cfg.UI.MenuItems[2])
	}
	if cfg.UI.MenuItems[3].Tab != "upgrade" || cfg.UI.MenuItems[3].Label != "升级" {
		t.Fatalf("blank menu label should fall back to default: %#v", cfg.UI.MenuItems[3])
	}
	if cfg.UI.MenuItems[4].Tab != "apiKeys" || cfg.UI.MenuItems[4].Label != "API Key" {
		t.Fatalf("menu label that belongs to another tab should be repaired: %#v", cfg.UI.MenuItems[4])
	}
	for _, item := range cfg.UI.MenuItems {
		if item.Tab == "unknown" {
			t.Fatalf("unknown menu item should be removed: %#v", cfg.UI.MenuItems)
		}
	}
}

func TestTVBotConfigSavesTableColumns(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest(http.MethodPut, "/tvbot/config", bytes.NewReader([]byte(`{
		"ui": {
			"table_columns": {
				"positions": ["upl", "exchange", "bad", "upl"],
				"pending_orders": ["actions", "symbol", "unknown", "actions"],
				"symbols": ["turnover", "symbol", "missing", "turnover"]
			}
		}
	}`)))
	req.SetBasicAuth("admin", "Admin123")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("config status=%d body=%s", rr.Code, rr.Body.String())
	}
	var cfg config.Config
	if err := json.Unmarshal(rr.Body.Bytes(), &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.UI.TableColumns.Positions) != len(config.DefaultPositionTableColumns) ||
		cfg.UI.TableColumns.Positions[0] != "upl" ||
		cfg.UI.TableColumns.Positions[1] != "exchange" ||
		strings.Contains(strings.Join(cfg.UI.TableColumns.Positions, ","), "bad") {
		t.Fatalf("position table columns should be normalized and saved: %#v", cfg.UI.TableColumns.Positions)
	}
	if len(cfg.UI.TableColumns.PendingOrders) != len(config.DefaultPendingOrderTableColumns) ||
		cfg.UI.TableColumns.PendingOrders[0] != "actions" ||
		cfg.UI.TableColumns.PendingOrders[1] != "symbol" ||
		strings.Contains(strings.Join(cfg.UI.TableColumns.PendingOrders, ","), "unknown") {
		t.Fatalf("pending order table columns should be normalized and saved: %#v", cfg.UI.TableColumns.PendingOrders)
	}
	if len(cfg.UI.TableColumns.Symbols) != len(config.DefaultSymbolTableColumns) ||
		cfg.UI.TableColumns.Symbols[0] != "turnover" ||
		cfg.UI.TableColumns.Symbols[1] != "symbol" ||
		strings.Contains(strings.Join(cfg.UI.TableColumns.Symbols, ","), "missing") {
		t.Fatalf("symbol table columns should be normalized and saved: %#v", cfg.UI.TableColumns.Symbols)
	}
}

func TestTVBotConfigRepairsInvalidDefaultTab(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest(http.MethodPut, "/tvbot/config", bytes.NewReader([]byte(`{"ui":{"default_tab":"missing-tab"}}`)))
	req.SetBasicAuth("admin", "Admin123")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("config status=%d body=%s", rr.Code, rr.Body.String())
	}
	var cfg config.Config
	if err := json.Unmarshal(rr.Body.Bytes(), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.UI.DefaultTab != config.DefaultHomeTab {
		t.Fatalf("invalid default tab should fall back to %q: %#v", config.DefaultHomeTab, cfg.UI)
	}
}

func TestTVBotSymbolsSyncsConfiguredAndExchangeCatalog(t *testing.T) {
	var sawLive, sawDemo, sawLiveTickers, sawDemoTickers bool
	okxTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("OK-ACCESS-KEY") != "" {
			t.Fatal("public instruments/tickers request should not be signed")
		}
		switch r.URL.Path {
		case "/api/v5/public/instruments":
			if r.URL.Query().Get("instType") != "SWAP" {
				t.Fatalf("bad instruments query: %s", r.URL.RawQuery)
			}
			if r.Header.Get("x-simulated-trading") == "1" {
				sawDemo = true
				_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[
					{"instType":"SWAP","instId":"BTC-USDT-SWAP","baseCcy":"BTC","quoteCcy":"USDT","settleCcy":"USDT","ctVal":"0.01","ctValCcy":"BTC","lotSz":"0.01","minSz":"0.01","lever":"100","state":"live"},
					{"instType":"SWAP","instId":"DOGE-USDT-SWAP","baseCcy":"DOGE","quoteCcy":"USDT","settleCcy":"USDT","ctVal":"1000","ctValCcy":"DOGE","lotSz":"1","minSz":"1","lever":"50","state":"live"},
					{"instType":"SWAP","instId":"SOL-USDC-SWAP","baseCcy":"SOL","quoteCcy":"USDC","settleCcy":"USDC","ctVal":"1","ctValCcy":"SOL","lotSz":"0.01","minSz":"0.01","lever":"50","state":"live"},
					{"instType":"SWAP","instId":"USDC-USDT-SWAP","uly":"USDC-USDT","instFamily":"USDC-USDT","settleCcy":"USDT","ctVal":"1","ctValCcy":"USDC","lotSz":"1","minSz":"1","lever":"50","state":"live"}
				]}`))
				return
			}
			sawLive = true
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[
				{"instType":"SWAP","instId":"ETH-USDT-SWAP","baseCcy":"ETH","quoteCcy":"USDT","settleCcy":"USDT","ctVal":"0.1","ctValCcy":"ETH","lotSz":"0.01","minSz":"0.01","lever":"100","state":"live"},
				{"instType":"SWAP","instId":"BTC-USDT-SWAP","baseCcy":"BTC","quoteCcy":"USDT","settleCcy":"USDT","ctVal":"0.01","ctValCcy":"BTC","lotSz":"0.01","minSz":"0.01","lever":"100","state":"live"},
				{"instType":"SWAP","instId":"BTC-USDC-SWAP","baseCcy":"BTC","quoteCcy":"USDC","settleCcy":"USDC","ctVal":"0.01","ctValCcy":"BTC","lotSz":"0.01","minSz":"0.01","lever":"100","state":"live"},
				{"instType":"SWAP","instId":"USDC-USDT-SWAP","uly":"USDC-USDT","instFamily":"USDC-USDT","settleCcy":"USDT","ctVal":"1","ctValCcy":"USDC","lotSz":"1","minSz":"1","lever":"50","state":"live"}
			]}`))
		case "/api/v5/market/tickers":
			if r.URL.Query().Get("instType") != "SWAP" {
				t.Fatalf("bad tickers query: %s", r.URL.RawQuery)
			}
			if r.Header.Get("x-simulated-trading") == "1" {
				sawDemoTickers = true
			} else {
				sawLiveTickers = true
			}
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[
				{"instType":"SWAP","instId":"BTC-USDT-SWAP","last":"50000","volCcy24h":"2","ts":"1784880000000"},
				{"instType":"SWAP","instId":"DOGE-USDT-SWAP","last":"0.2","volCcy24h":"10000","ts":"1784880001000"},
				{"instType":"SWAP","instId":"ETH-USDT-SWAP","last":"2500","volCcy24h":"3","ts":"1784880002000"}
			]}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer okxTS.Close()

	var sawBinanceLiveInfo, sawBinanceDemoInfo, sawBinanceLiveTickers, sawBinanceDemoTickers bool
	binanceLiveTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("X-MBX-APIKEY") != "" {
			t.Fatal("public Binance catalog request should not be signed")
		}
		switch r.URL.Path {
		case "/fapi/v1/exchangeInfo":
			sawBinanceLiveInfo = true
			_, _ = w.Write([]byte(`{"symbols":[
				{"symbol":"BTCUSDT","pair":"BTCUSDT","contractType":"PERPETUAL","status":"TRADING","baseAsset":"BTC","quoteAsset":"USDT","marginAsset":"USDT","pricePrecision":2,"quantityPrecision":3,"filters":[{"filterType":"PRICE_FILTER","tickSize":"0.10"},{"filterType":"LOT_SIZE","minQty":"0.001","maxQty":"100","stepSize":"0.001"},{"filterType":"MIN_NOTIONAL","notional":"5"}]},
				{"symbol":"ETHUSDT","pair":"ETHUSDT","contractType":"PERPETUAL","status":"TRADING","baseAsset":"ETH","quoteAsset":"USDT","marginAsset":"USDT","pricePrecision":2,"quantityPrecision":3,"filters":[{"filterType":"PRICE_FILTER","tickSize":"0.01"},{"filterType":"LOT_SIZE","minQty":"0.001","maxQty":"200","stepSize":"0.001"}]},
				{"symbol":"BTCUSDC","pair":"BTCUSDC","contractType":"PERPETUAL","status":"TRADING","baseAsset":"BTC","quoteAsset":"USDC","marginAsset":"USDC","filters":[]},
				{"symbol":"USDCUSDT","pair":"USDCUSDT","contractType":"PERPETUAL","status":"TRADING","baseAsset":"USDC","quoteAsset":"USDT","marginAsset":"USDT","filters":[]},
				{"symbol":"BNBUSDT_260925","pair":"BNBUSDT","contractType":"CURRENT_QUARTER","status":"TRADING","baseAsset":"BNB","quoteAsset":"USDT","marginAsset":"USDT","filters":[]}
			]}`))
		case "/fapi/v1/ticker/24hr":
			sawBinanceLiveTickers = true
			_, _ = w.Write([]byte(`[
				{"symbol":"BTCUSDT","lastPrice":"64000","volume":"1","quoteVolume":"64000","closeTime":1784880000000},
				{"symbol":"ETHUSDT","lastPrice":"2500","volume":"3","quoteVolume":"7500","closeTime":1784880001000}
			]`))
		default:
			t.Fatalf("unexpected Binance live path %s", r.URL.Path)
		}
	}))
	defer binanceLiveTS.Close()

	binanceDemoTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("X-MBX-APIKEY") != "" {
			t.Fatal("public Binance demo catalog request should not be signed")
		}
		switch r.URL.Path {
		case "/fapi/v1/exchangeInfo":
			sawBinanceDemoInfo = true
			_, _ = w.Write([]byte(`{"symbols":[
				{"symbol":"DOGEUSDT","pair":"DOGEUSDT","contractType":"PERPETUAL","status":"TRADING","baseAsset":"DOGE","quoteAsset":"USDT","marginAsset":"USDT","pricePrecision":5,"quantityPrecision":0,"filters":[{"filterType":"PRICE_FILTER","tickSize":"0.00001"},{"filterType":"LOT_SIZE","minQty":"1","maxQty":"1000000","stepSize":"1"}]},
				{"symbol":"SOLUSDC","pair":"SOLUSDC","contractType":"PERPETUAL","status":"TRADING","baseAsset":"SOL","quoteAsset":"USDC","marginAsset":"USDC","filters":[]}
			]}`))
		case "/fapi/v1/ticker/24hr":
			sawBinanceDemoTickers = true
			_, _ = w.Write([]byte(`[
				{"symbol":"DOGEUSDT","lastPrice":"0.2","volume":"10000","quoteVolume":"2000","closeTime":1784880002000}
			]`))
		default:
			t.Fatalf("unexpected Binance demo path %s", r.URL.Path)
		}
	}))
	defer binanceDemoTS.Close()

	srv := newTestServer(t)
	cfg := srv.ConfigStore.Get()
	cfg.Trading.BaseURL = okxTS.URL
	cfg.Trading.BinanceBaseURL = binanceLiveTS.URL
	cfg.Trading.BinanceDemoBaseURL = binanceDemoTS.URL
	srv.ConfigStore = config.NewStore("", cfg)
	srv.OKXHTTPClient = okxTS.Client()
	srv.BinanceHTTPClient = binanceLiveTS.Client()

	req := httptest.NewRequest(http.MethodPost, "/tvbot/symbols", nil)
	req.SetBasicAuth("admin", "Admin123")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("symbols status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !sawLive || !sawDemo || !sawLiveTickers || !sawDemoTickers {
		t.Fatalf("expected live/demo instruments and tickers requests live=%v demo=%v liveTickers=%v demoTickers=%v", sawLive, sawDemo, sawLiveTickers, sawDemoTickers)
	}
	if !sawBinanceLiveInfo || !sawBinanceDemoInfo || !sawBinanceLiveTickers || !sawBinanceDemoTickers {
		t.Fatalf("expected Binance live/demo exchangeInfo and tickers liveInfo=%v demoInfo=%v liveTickers=%v demoTickers=%v", sawBinanceLiveInfo, sawBinanceDemoInfo, sawBinanceLiveTickers, sawBinanceDemoTickers)
	}
	var resp symbolsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Symbols["BTC"].InstID != "BTC-USDT-SWAP" {
		t.Fatalf("response lost configured symbols: %#v", resp.Symbols)
	}
	if resp.OKX.Live.Count != 2 || resp.OKX.Demo.Count != 2 {
		t.Fatalf("bad OKX counts: %#v", resp.OKX)
	}
	if resp.Binance.Live.Count != 2 || resp.Binance.Demo.Count != 1 {
		t.Fatalf("bad Binance counts: %#v", resp.Binance)
	}
	if resp.OKX.Live.Instruments[0].InstID != "BTC-USDT-SWAP" || resp.OKX.Demo.Instruments[1].InstID != "DOGE-USDT-SWAP" {
		t.Fatalf("instruments should be sorted and parsed: %#v", resp.OKX)
	}
	if resp.Binance.Live.Instruments[0].Symbol != "BTCUSDT" || resp.Binance.Demo.Instruments[0].Symbol != "DOGEUSDT" {
		t.Fatalf("Binance instruments should be sorted and parsed: %#v", resp.Binance)
	}
	if resp.OKX.Live.Instruments[0].TurnoverUSDT24h != "100000" || resp.OKX.Demo.Instruments[1].TurnoverUSDT24h != "2000" {
		t.Fatalf("instruments should include 24h turnover: %#v", resp.OKX)
	}
	if resp.Binance.Live.Instruments[0].TurnoverUSDT24h != "64000" || resp.Binance.Demo.Instruments[0].TurnoverUSDT24h != "2000" {
		t.Fatalf("Binance instruments should include 24h turnover: %#v", resp.Binance)
	}
	if resp.OKX.Live.Instruments[0].TickerUpdatedAt == "" {
		t.Fatalf("instruments should include ticker updated time: %#v", resp.OKX.Live.Instruments[0])
	}
	if resp.Binance.Live.Instruments[0].TickerUpdatedAt == "" ||
		resp.Binance.Live.Instruments[0].TickSize != "0.10" ||
		resp.Binance.Live.Instruments[0].StepSize != "0.001" ||
		resp.Binance.Live.Instruments[0].MinQty != "0.001" ||
		resp.Binance.Live.Instruments[0].MaxQty != "100" ||
		resp.Binance.Live.Instruments[0].MinNotional != "5" {
		t.Fatalf("Binance instruments should include ticker time and filters: %#v", resp.Binance.Live.Instruments[0])
	}
	for _, inst := range append(resp.OKX.Live.Instruments, resp.OKX.Demo.Instruments...) {
		if strings.Contains(inst.InstID, "USDC") || inst.QuoteCcy == "USDC" || inst.SettleCcy == "USDC" {
			t.Fatalf("USDC instruments should be filtered out: %#v", resp.OKX)
		}
	}
	for _, inst := range append(resp.Binance.Live.Instruments, resp.Binance.Demo.Instruments...) {
		if strings.Contains(inst.Symbol, "USDC") || inst.BaseAsset == "USDC" || inst.QuoteAsset == "USDC" || inst.MarginAsset == "USDC" {
			t.Fatalf("Binance USDC instruments should be filtered out: %#v", resp.Binance)
		}
	}
	if resp.OKX.Live.Error != "" || resp.OKX.Demo.Error != "" {
		t.Fatalf("unexpected OKX errors: %#v", resp.OKX)
	}
	if resp.Binance.Live.Error != "" || resp.Binance.Demo.Error != "" {
		t.Fatalf("unexpected Binance errors: %#v", resp.Binance)
	}
}

func TestTVBotSymbolsKeepsCatalogWhenTickerFetchFails(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v5/public/instruments":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[
				{"instType":"SWAP","instId":"BTC-USDT-SWAP","baseCcy":"BTC","quoteCcy":"USDT","settleCcy":"USDT","ctVal":"0.01","ctValCcy":"BTC","lotSz":"0.01","minSz":"0.01","lever":"100","state":"live"}
			]}`))
		case "/api/v5/market/tickers":
			http.Error(w, "ticker unavailable", http.StatusBadGateway)
		case "/fapi/v1/exchangeInfo":
			if r.Header.Get("X-MBX-APIKEY") != "" {
				t.Fatal("public Binance catalog request should not be signed")
			}
			_, _ = w.Write([]byte(`{"symbols":[
				{"symbol":"BTCUSDT","pair":"BTCUSDT","contractType":"PERPETUAL","status":"TRADING","baseAsset":"BTC","quoteAsset":"USDT","marginAsset":"USDT","filters":[{"filterType":"LOT_SIZE","minQty":"0.001","stepSize":"0.001"}]}
			]}`))
		case "/fapi/v1/ticker/24hr":
			if r.Header.Get("X-MBX-APIKEY") != "" {
				t.Fatal("public Binance ticker request should not be signed")
			}
			http.Error(w, "binance ticker unavailable", http.StatusBadGateway)
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer ts.Close()

	srv := newTestServer(t)
	cfg := srv.ConfigStore.Get()
	cfg.Trading.BaseURL = ts.URL
	cfg.Trading.BinanceBaseURL = ts.URL
	cfg.Trading.BinanceDemoBaseURL = ts.URL
	srv.ConfigStore = config.NewStore("", cfg)
	srv.OKXHTTPClient = ts.Client()
	srv.BinanceHTTPClient = ts.Client()

	req := httptest.NewRequest(http.MethodPost, "/tvbot/symbols", nil)
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
	if resp.OKX.Live.Count != 15 || resp.OKX.Demo.Count != 15 {
		t.Fatalf("ticker failure should retain the previous OKX catalog: %#v", resp.OKX)
	}
	if resp.Binance.Live.Count != 15 || resp.Binance.Demo.Count != 15 {
		t.Fatalf("ticker failure should retain the previous Binance catalog: %#v", resp.Binance)
	}
	if resp.OKX.Live.TickerError == "" || resp.OKX.Demo.TickerError == "" {
		t.Fatalf("ticker failure should be reported: %#v", resp.OKX)
	}
	if resp.Binance.Live.TickerError == "" || resp.Binance.Demo.TickerError == "" {
		t.Fatalf("Binance ticker failure should be reported: %#v", resp.Binance)
	}
	if resp.OKX.Live.Instruments[0].TurnoverUSDT24h == "" {
		t.Fatalf("previous OKX turnover should be preserved when ticker is unavailable: %#v", resp.OKX.Live.Instruments[0])
	}
	if resp.Binance.Live.Instruments[0].TurnoverUSDT24h == "" {
		t.Fatalf("previous Binance turnover should be preserved when ticker is unavailable: %#v", resp.Binance.Live.Instruments[0])
	}
}

func TestTVBotSymbolsGETReadsSQLiteCache(t *testing.T) {
	srv := newTestServer(t)
	now := srv.now()
	okxPayload := `{"env":"live","demo":false,"count":1,"instruments":[{"instType":"SWAP","instId":"BTC-USDT-SWAP","baseCcy":"BTC","quoteCcy":"USDT","settleCcy":"USDT","ctVal":"0.01","lotSz":"0.01","minSz":"0.01","state":"live"}]}`
	binancePayload := `{"env":"demo","demo":true,"count":1,"instruments":[{"symbol":"DOGEUSDT","pair":"DOGEUSDT","contractType":"PERPETUAL","status":"TRADING","baseAsset":"DOGE","quoteAsset":"USDT","marginAsset":"USDT"}]}`
	if err := srv.Orders.UpsertSymbolCatalogCaches([]storage.SymbolCatalogCache{
		{Exchange: trading.ExchangeOKX, Env: config.EnvLive, PayloadJSON: okxPayload, Count: 1, SyncedAt: now, AttemptedAt: now},
		{Exchange: trading.ExchangeOKX, Env: config.EnvDemo, PayloadJSON: `{"env":"demo","demo":true,"count":0,"instruments":[]}`, Count: 0, AttemptedAt: now},
		{Exchange: trading.ExchangeBinance, Env: config.EnvLive, PayloadJSON: `{"env":"live","demo":false,"count":0,"instruments":[]}`, Count: 0, AttemptedAt: now},
		{Exchange: trading.ExchangeBinance, Env: config.EnvDemo, PayloadJSON: binancePayload, Count: 1, SyncedAt: now, AttemptedAt: now},
	}); err != nil {
		t.Fatal(err)
	}
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
	if resp.OKX.Live.Count != 1 || resp.OKX.Live.Instruments[0].InstID != "BTC-USDT-SWAP" || resp.OKX.Live.SyncedAt == "" {
		t.Fatalf("OKX live cache not returned: %#v", resp.OKX.Live)
	}
	if resp.Binance.Demo.Count != 1 || resp.Binance.Demo.Instruments[0].Symbol != "DOGEUSDT" || !resp.Binance.Demo.Demo {
		t.Fatalf("Binance demo cache not returned: %#v", resp.Binance.Demo)
	}
	if resp.OKX.Demo.Count != 0 || resp.Binance.Live.Count != 0 {
		t.Fatalf("missing cache entries should remain empty sets: %#v", resp)
	}
}

func TestTVOrderAcceptsAndDeduplicates(t *testing.T) {
	srv := newTestServer(t)
	signal := validSignal(t, srv)
	body, err := json.Marshal(signal)
	if err != nil {
		t.Fatal(err)
	}
	first := httptest.NewRecorder()
	srv.ServeHTTP(first, httptest.NewRequest(http.MethodPost, "/tvorder", bytes.NewReader(body)))
	if first.Code != http.StatusAccepted {
		t.Fatalf("first status=%d body=%s", first.Code, first.Body.String())
	}
	firstResp := decodeTVOrderSignalResponse(t, first.Body.Bytes())
	select {
	case got := <-srv.Executor.(fakeExecutor).calls:
		if got.Coinpair != "BTC" || got.Action != trading.ActionLong {
			t.Fatalf("bad executed signal: %#v", got)
		}
		if got.TargetExchange != trading.ExchangeOKX {
			t.Fatalf("old webhook should default to OKX target exchange: %#v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("executor was not called")
	}
	waitOrderStatus(t, srv.Orders, firstResp.SignalID, storage.StatusSubmitted)
	second := httptest.NewRecorder()
	srv.ServeHTTP(second, httptest.NewRequest(http.MethodPost, "/tvorder", bytes.NewReader(body)))
	if second.Code != http.StatusAccepted {
		t.Fatalf("second status=%d body=%s", second.Code, second.Body.String())
	}
	if !bytes.Contains(second.Body.Bytes(), []byte(`"status":"duplicate"`)) {
		t.Fatalf("expected duplicate response, got %s", second.Body.String())
	}
	select {
	case <-srv.Executor.(fakeExecutor).calls:
		t.Fatal("duplicate webhook executed again")
	case <-time.After(50 * time.Millisecond):
	}
	ordersReq := httptest.NewRequest(http.MethodGet, "/tvbot/orders", nil)
	ordersReq.SetBasicAuth("admin", "Admin123")
	ordersRR := httptest.NewRecorder()
	srv.ServeHTTP(ordersRR, ordersReq)
	if ordersRR.Code != http.StatusOK {
		t.Fatalf("orders status=%d body=%s", ordersRR.Code, ordersRR.Body.String())
	}
	if bytes.Contains(ordersRR.Body.Bytes(), []byte(signal.Token)) {
		t.Fatalf("orders response leaked webhook token: %s", ordersRR.Body.String())
	}
	var list struct {
		Orders []storage.OrderRecord `json:"orders"`
	}
	if err := json.Unmarshal(ordersRR.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	var foundDuplicate bool
	for _, order := range list.Orders {
		if order.TargetExchange != trading.ExchangeOKX {
			t.Fatalf("orders should default target exchange to OKX: %#v", list.Orders)
		}
		if order.RawJSON == "" || !strings.Contains(order.RawJSON, `"token": "[redacted]"`) || strings.Contains(order.RawJSON, signal.Token) {
			t.Fatalf("order should include redacted raw json: %#v", order)
		}
		if order.Status == storage.StatusDuplicate {
			foundDuplicate = true
		}
	}
	if len(list.Orders) != 2 || !foundDuplicate {
		t.Fatalf("duplicate signal should be listed in history: %#v", list.Orders)
	}
}

func TestIgnoredEntrySignalUsesCaseInsensitiveFuzzyMatching(t *testing.T) {
	cfg := config.Default()
	cfg.Trading.IgnoredCoinpairs = []string{"SYRUP", " eth "}
	for _, coinpair := range []string{"EETH", "ETHFI", "ETHUSDT", "ETHusd", "ETHusdc"} {
		t.Run(coinpair, func(t *testing.T) {
			filter, ignored := ignoredEntrySignal(cfg, trading.Signal{Coinpair: coinpair, PositionEffect: trading.PositionEffectOpen})
			if !ignored || filter != "ETH" {
				t.Fatalf("coinpair %q ignored=%v filter=%q", coinpair, ignored, filter)
			}
		})
	}
	if _, ignored := ignoredEntrySignal(cfg, trading.Signal{Coinpair: "BTCUSDT", Ticker: "OKX:BTCUSDT.P"}); ignored {
		t.Fatal("BTCUSDT should not match ETH")
	}
	cfg.Trading.IgnoredCoinpairs = []string{"SYRUP", "eth-usdt"}
	if _, ignored := ignoredEntrySignal(cfg, trading.Signal{Ticker: "OKX:ETHUSDT.P"}); !ignored {
		t.Fatal("common separators should be ignored during matching")
	}
	if _, ignored := ignoredEntrySignal(cfg, trading.Signal{Coinpair: "ETH-USDT-SWAP", PositionEffect: trading.PositionEffectClose}); ignored {
		t.Fatal("close signals should not be filtered")
	}
	cfg.Trading.IgnoredCoinpairs = []string{"SYRUP"}
	if _, ignored := ignoredEntrySignal(cfg, trading.Signal{Coinpair: "ETHUSDT"}); ignored {
		t.Fatal("removed ETH rule should restore matching signals")
	}
	if filter, ignored := ignoredEntrySignal(cfg, trading.Signal{Coinpair: "SYRUPUSDT"}); !ignored || filter != "SYRUP" {
		t.Fatalf("remaining rule should stay active: ignored=%v filter=%q", ignored, filter)
	}
}

func TestTVOrderRecordsMatchingEntrySignalAsIgnored(t *testing.T) {
	srv := newTestServer(t)
	cfg := srv.ConfigStore.Get()
	cfg.Trading.IgnoredCoinpairs = []string{"eth"}
	srv.ConfigStore = config.NewStore("", cfg)
	signal := validSignal(t, srv)
	signal.Coinpair = "ETHusdc"
	signal.Ticker = "OKX:ETHUSDC.P"
	body, err := json.Marshal(signal)
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/tvorder", bytes.NewReader(body)))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	resp := decodeTVOrderSignalResponse(t, rr.Body.Bytes())
	if resp.Status != "ignored" {
		t.Fatalf("response should be ignored: %#v", resp)
	}
	select {
	case got := <-srv.Executor.(fakeExecutor).calls:
		t.Fatalf("ignored signal should not execute: %#v", got)
	case <-time.After(50 * time.Millisecond):
	}
	rec, ok := srv.Orders.Get(resp.SignalID)
	if !ok || rec.Status != storage.StatusIgnored || rec.ErrorCode != "coinpair_filtered" || !strings.Contains(rec.Error, `"ETH"`) {
		t.Fatalf("bad ignored history record: %#v found=%v", rec, ok)
	}
	if rec.Coinpair != "ETHUSDC" || rec.PositionEffect != trading.PositionEffectOpen {
		t.Fatalf("ignored history lost normalized signal fields: %#v", rec)
	}
}

func TestTVOrderTradeEnvDefaultsDemoAndLiveOverridesExecutionConfig(t *testing.T) {
	srv := newTestServer(t)
	srv.Executor = fakeExecutor{calls: make(chan trading.Signal, 2), demoFlags: make(chan bool, 2)}
	missingEnv := validSignal(t, srv)
	body, err := json.Marshal(missingEnv)
	if err != nil {
		t.Fatal(err)
	}
	first := httptest.NewRecorder()
	srv.ServeHTTP(first, httptest.NewRequest(http.MethodPost, "/tvorder", bytes.NewReader(body)))
	if first.Code != http.StatusAccepted {
		t.Fatalf("missing env status=%d body=%s", first.Code, first.Body.String())
	}
	firstResp := decodeTVOrderSignalResponse(t, first.Body.Bytes())
	select {
	case got := <-srv.Executor.(fakeExecutor).calls:
		if got.TradeEnv != trading.TradeEnvDemo {
			t.Fatalf("missing trade_env should default to demo: %#v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("executor was not called for missing trade_env")
	}
	select {
	case demo := <-srv.Executor.(fakeExecutor).demoFlags:
		if !demo {
			t.Fatal("missing trade_env should execute with demo config")
		}
	case <-time.After(time.Second):
		t.Fatal("executor config was not recorded")
	}
	firstRecord := waitOrderStatus(t, srv.Orders, firstResp.SignalID, storage.StatusSubmitted)
	if firstRecord.TradeEnv != trading.TradeEnvDemo {
		t.Fatalf("order record should save default demo env: %#v", firstRecord)
	}

	live := validSignal(t, srv)
	live.SentAt = "2026-07-24T03:00:01Z"
	live.TradeEnv = trading.TradeEnvLive
	live.Token = srv.Token.Generate(live.CanonicalWebhookTokenPayload())
	body, err = json.Marshal(live)
	if err != nil {
		t.Fatal(err)
	}
	second := httptest.NewRecorder()
	srv.ServeHTTP(second, httptest.NewRequest(http.MethodPost, "/tvorder", bytes.NewReader(body)))
	if second.Code != http.StatusAccepted {
		t.Fatalf("live env status=%d body=%s", second.Code, second.Body.String())
	}
	secondResp := decodeTVOrderSignalResponse(t, second.Body.Bytes())
	select {
	case got := <-srv.Executor.(fakeExecutor).calls:
		if got.TradeEnv != trading.TradeEnvLive {
			t.Fatalf("explicit live trade_env not preserved: %#v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("executor was not called for live trade_env")
	}
	select {
	case demo := <-srv.Executor.(fakeExecutor).demoFlags:
		if demo {
			t.Fatal("live trade_env should execute with live config")
		}
	case <-time.After(time.Second):
		t.Fatal("executor config was not recorded for live trade_env")
	}
	secondRecord := waitOrderStatus(t, srv.Orders, secondResp.SignalID, storage.StatusSubmitted)
	if secondRecord.TradeEnv != trading.TradeEnvLive {
		t.Fatalf("order record should save live env: %#v", secondRecord)
	}
}

func TestTVOrderExplicitTradeEnvRejectsLegacyTokenAndDedupesByEnv(t *testing.T) {
	srv := newTestServer(t)
	explicitDemoOldToken := validSignal(t, srv)
	explicitDemoOldToken.TradeEnv = trading.TradeEnvDemo
	explicitDemoOldToken.Token = srv.Token.Generate(explicitDemoOldToken.CanonicalTokenPayload())
	body, err := json.Marshal(explicitDemoOldToken)
	if err != nil {
		t.Fatal(err)
	}
	rejected := httptest.NewRecorder()
	srv.ServeHTTP(rejected, httptest.NewRequest(http.MethodPost, "/tvorder", bytes.NewReader(body)))
	if rejected.Code != http.StatusUnauthorized {
		t.Fatalf("explicit trade_env with legacy token should reject, status=%d body=%s", rejected.Code, rejected.Body.String())
	}
	select {
	case <-srv.Executor.(fakeExecutor).calls:
		t.Fatal("rejected explicit trade_env executed")
	case <-time.After(50 * time.Millisecond):
	}

	srv.Executor = fakeExecutor{calls: make(chan trading.Signal, 2)}
	demo := validSignal(t, srv)
	demo.TradeEnv = trading.TradeEnvDemo
	demo.Token = srv.Token.Generate(demo.CanonicalWebhookTokenPayload())
	live := demo
	live.TradeEnv = trading.TradeEnvLive
	live.Token = srv.Token.Generate(live.CanonicalWebhookTokenPayload())
	for _, signal := range []trading.Signal{demo, live} {
		body, err := json.Marshal(signal)
		if err != nil {
			t.Fatal(err)
		}
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/tvorder", bytes.NewReader(body)))
		if rr.Code != http.StatusAccepted {
			t.Fatalf("status=%d body=%s signal=%#v", rr.Code, rr.Body.String(), signal)
		}
	}
	for i := 0; i < 2; i++ {
		select {
		case <-srv.Executor.(fakeExecutor).calls:
		case <-time.After(time.Second):
			t.Fatal("expected demo and live signals to execute separately")
		}
	}
	records := srv.Orders.List(10)
	seen := map[string]bool{}
	for _, rec := range records {
		if rec.Status == storage.StatusAccepted || rec.Status == storage.StatusSubmitted {
			seen[rec.TradeEnv] = true
		}
	}
	if !seen[trading.TradeEnvDemo] || !seen[trading.TradeEnvLive] {
		t.Fatalf("demo/live records should not dedupe each other: %#v", records)
	}
}

func TestTVOrderRoutesTargetExchangeBinance(t *testing.T) {
	srv := newTestServer(t)
	signal := validSignal(t, srv)
	signal.TargetExchange = trading.ExchangeBinance
	signal.APIID = "binance-main"
	signal.Token = srv.Token.Generate(signal.CanonicalWebhookTokenPayload())
	body, err := json.Marshal(signal)
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/tvorder", bytes.NewReader(body)))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	resp := decodeTVOrderSignalResponse(t, rr.Body.Bytes())
	select {
	case got := <-srv.Executor.(fakeExecutor).calls:
		if got.TargetExchange != trading.ExchangeBinance || got.APIID != "binance-main" {
			t.Fatalf("signal should route to Binance target exchange: %#v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("executor was not called")
	}
	waitOrderStatus(t, srv.Orders, resp.SignalID, storage.StatusSubmitted)
	records := srv.Orders.List(10)
	if len(records) != 1 || records[0].TargetExchange != trading.ExchangeBinance {
		t.Fatalf("order record should save Binance target exchange: %#v", records)
	}
}

func TestHandleOrdersFiltersByTargetExchange(t *testing.T) {
	srv := newTestServer(t)
	now := time.Date(2026, 7, 24, 4, 0, 0, 0, time.UTC)
	okxSignal := validSignal(t, srv)
	okxSignal.TargetExchange = trading.ExchangeOKX
	okxSignal.Coinpair = "BTC"
	okxSignal.Ticker = "OKX:BTCUSDT.P"
	if _, _, err := srv.Orders.RecordAccepted(okxSignal, "okx-order", now); err != nil {
		t.Fatal(err)
	}
	binanceSignal := validSignal(t, srv)
	binanceSignal.TargetExchange = trading.ExchangeBinance
	binanceSignal.APIID = "binance-main"
	binanceSignal.Coinpair = "ETH"
	binanceSignal.Ticker = "BINANCE:ETHUSDT.P"
	if _, _, err := srv.Orders.RecordAccepted(binanceSignal, "binance-order", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	fetch := func(target string) (int, []storage.OrderRecord, string) {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.SetBasicAuth("admin", "Admin123")
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		var list struct {
			Orders []storage.OrderRecord `json:"orders"`
		}
		if rr.Code == http.StatusOK {
			if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
				t.Fatal(err)
			}
		}
		return rr.Code, list.Orders, rr.Body.String()
	}
	if code, orders, body := fetch("/tvbot/orders?limit=50"); code != http.StatusOK || len(orders) != 2 {
		t.Fatalf("unfiltered orders code=%d len=%d body=%s", code, len(orders), body)
	}
	if code, orders, body := fetch("/tvbot/orders?limit=50&exchange=okx"); code != http.StatusOK || len(orders) != 1 || orders[0].TargetExchange != trading.ExchangeOKX {
		t.Fatalf("OKX orders code=%d orders=%#v body=%s", code, orders, body)
	}
	if code, orders, body := fetch("/tvbot/orders?limit=50&exchange=binance"); code != http.StatusOK || len(orders) != 1 || orders[0].TargetExchange != trading.ExchangeBinance {
		t.Fatalf("Binance orders code=%d orders=%#v body=%s", code, orders, body)
	}
	if code, _, body := fetch("/tvbot/orders?exchange=bybit"); code != http.StatusBadRequest || !strings.Contains(body, "invalid_exchange") {
		t.Fatalf("invalid exchange code=%d body=%s", code, body)
	}
}

func TestHandleOrdersFiltersByStatus(t *testing.T) {
	srv := newTestServer(t)
	now := time.Date(2026, 8, 29, 1, 0, 0, 0, time.UTC)
	signal := func(coinpair, exchange string) trading.Signal {
		item := validSignal(t, srv)
		item.TargetExchange = exchange
		item.Coinpair = coinpair
		item.Ticker = strings.ToUpper(exchange) + ":" + coinpair + "USDT.P"
		return item
	}
	if _, _, err := srv.Orders.RecordAccepted(signal("ACCEPTED", trading.ExchangeOKX), "http-status-accepted", now); err != nil {
		t.Fatal(err)
	}
	submitted, _, err := srv.Orders.RecordAccepted(signal("SUBMITTED", trading.ExchangeOKX), "http-status-submitted", now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Orders.MarkSubmitted(submitted.SignalID, trading.OrderResult{OrdID: "submitted-1"}, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	failed, _, err := srv.Orders.RecordAccepted(signal("FAILED", trading.ExchangeOKX), "http-status-failed", now.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Orders.MarkFailed(failed.SignalID, errors.New("seed failure"), now.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.Orders.RecordRejected(signal("REJECTED", trading.ExchangeOKX), "invalid_signal", errors.New("seed rejection"), now.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.Orders.RecordIgnoredReason(signal("BTC-IGNORED", trading.ExchangeOKX), "adx_transition", "seed ignored", now.Add(6*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.Orders.RecordIgnoredReason(signal("BTC-IGNORED", trading.ExchangeBinance), "outside_market_top100", "seed ignored", now.Add(7*time.Second)); err != nil {
		t.Fatal(err)
	}
	duplicateSignal := signal("DUPLICATE", trading.ExchangeOKX)
	if _, duplicate, err := srv.Orders.RecordAccepted(duplicateSignal, "http-status-duplicate", now.Add(8*time.Second)); err != nil || duplicate {
		t.Fatalf("seed duplicate source: duplicate=%v err=%v", duplicate, err)
	}
	if _, duplicate, err := srv.Orders.RecordAccepted(duplicateSignal, "http-status-duplicate", now.Add(9*time.Second)); err != nil || !duplicate {
		t.Fatalf("seed duplicate: duplicate=%v err=%v", duplicate, err)
	}

	type ordersResponse struct {
		Orders     []storage.OrderRecord `json:"orders"`
		Total      int                   `json:"total"`
		Limit      int                   `json:"limit"`
		Offset     int                   `json:"offset"`
		Page       int                   `json:"page"`
		TotalPages int                   `json:"total_pages"`
	}
	fetch := func(target string) (int, ordersResponse, string) {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.SetBasicAuth("admin", "Admin123")
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		var response ordersResponse
		if rr.Code == http.StatusOK {
			if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
		}
		return rr.Code, response, rr.Body.String()
	}

	expectedCounts := map[storage.OrderStatus]int{
		storage.StatusAccepted:  2,
		storage.StatusDuplicate: 1,
		storage.StatusSubmitted: 1,
		storage.StatusFailed:    1,
		storage.StatusRejected:  1,
		storage.StatusIgnored:   2,
	}
	for status, expected := range expectedCounts {
		code, response, body := fetch("/tvbot/orders?limit=50&status=" + string(status))
		if code != http.StatusOK || response.Total != expected || len(response.Orders) != expected {
			t.Fatalf("status %s code=%d response=%#v body=%s", status, code, response, body)
		}
		for _, order := range response.Orders {
			if order.Status != status {
				t.Fatalf("status %s returned %#v", status, order)
			}
		}
	}
	if code, response, body := fetch("/tvbot/orders?limit=50"); code != http.StatusOK || response.Total != 8 || len(response.Orders) != 8 {
		t.Fatalf("all statuses code=%d response=%#v body=%s", code, response, body)
	}
	if code, response, body := fetch("/tvbot/orders?limit=10&exchange=okx&status=ignored&q=BTC"); code != http.StatusOK || response.Total != 1 || len(response.Orders) != 1 || response.Orders[0].Coinpair != "BTC-IGNORED" {
		t.Fatalf("combined filter code=%d response=%#v body=%s", code, response, body)
	}
	if code, response, body := fetch("/tvbot/orders?limit=1&offset=1&status=accepted"); code != http.StatusOK || response.Total != 2 || response.Page != 2 || response.TotalPages != 2 || len(response.Orders) != 1 {
		t.Fatalf("status pagination code=%d response=%#v body=%s", code, response, body)
	}
	if code, _, body := fetch("/tvbot/orders?status=unknown"); code != http.StatusBadRequest || !strings.Contains(body, "invalid_status") {
		t.Fatalf("invalid status code=%d body=%s", code, body)
	}
}

func TestHandleOrdersReturnsPaginatedMetadata(t *testing.T) {
	srv := newTestServer(t)
	now := time.Date(2026, 7, 24, 4, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		signal := validSignal(t, srv)
		signal.TargetExchange = trading.ExchangeOKX
		signal.Coinpair = fmt.Sprintf("COIN%d", i)
		signal.Ticker = fmt.Sprintf("OKX:COIN%dUSDT.P", i)
		if _, _, err := srv.Orders.RecordAccepted(signal, fmt.Sprintf("paged-okx-%d", i), now.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	binanceSignal := validSignal(t, srv)
	binanceSignal.TargetExchange = trading.ExchangeBinance
	binanceSignal.Coinpair = "BNB"
	binanceSignal.Ticker = "BINANCE:BNBUSDT.P"
	if _, _, err := srv.Orders.RecordAccepted(binanceSignal, "paged-binance", now.Add(10*time.Second)); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/tvbot/orders?limit=2&offset=2&exchange=okx", nil)
	req.SetBasicAuth("admin", "Admin123")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("orders status=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Orders     []storage.OrderRecord `json:"orders"`
		Total      int                   `json:"total"`
		Limit      int                   `json:"limit"`
		Offset     int                   `json:"offset"`
		Page       int                   `json:"page"`
		TotalPages int                   `json:"total_pages"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Total != 5 || resp.Limit != 2 || resp.Offset != 2 || resp.Page != 2 || resp.TotalPages != 3 {
		t.Fatalf("bad pagination metadata: %#v", resp)
	}
	if len(resp.Orders) != 2 || resp.Orders[0].Coinpair != "COIN2" || resp.Orders[1].Coinpair != "COIN1" {
		t.Fatalf("bad page orders: %#v", resp.Orders)
	}
}

func TestHandleOrdersSearchesHistoryWithPagination(t *testing.T) {
	srv := newTestServer(t)
	now := time.Date(2026, 7, 24, 4, 0, 0, 0, time.UTC)
	btcSignal := validSignal(t, srv)
	btcSignal.TargetExchange = trading.ExchangeOKX
	btcSignal.APIID = "main"
	btcSignal.Exchange = "OKX"
	btcSignal.Coinpair = "BTCUSDT.P"
	btcSignal.Ticker = "OKX:BTCUSDT.P"
	btcSignal.Price = trading.NewFlexibleFloat(61000)
	btcSignal.Amount = trading.NewFlexibleFloat(500)
	btc, _, err := srv.Orders.RecordAccepted(btcSignal, "search-http-btc", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Orders.MarkSubmitted(btc.SignalID, trading.OrderResult{TargetExchange: trading.ExchangeOKX, InstID: "BTC-USDT-SWAP", ClOrdID: "client-btc", OrdID: "okx-999"}, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	ethSignal := validSignal(t, srv)
	ethSignal.TargetExchange = trading.ExchangeBinance
	ethSignal.APIID = "binance-main"
	ethSignal.Exchange = "BINANCE"
	ethSignal.Coinpair = "ETHUSDT.P"
	ethSignal.Ticker = "BINANCE:ETHUSDT.P"
	ethSignal.Price = trading.NewFlexibleFloat(3400)
	ethSignal.Amount = trading.NewFlexibleFloat(750)
	eth, _, err := srv.Orders.RecordAccepted(ethSignal, "search-http-eth", now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Orders.MarkSubmitted(eth.SignalID, trading.OrderResult{TargetExchange: trading.ExchangeBinance, InstID: "ETHUSDT", ClOrdID: "client-eth", OrdID: "bn-321"}, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	solSignal := validSignal(t, srv)
	solSignal.TargetExchange = trading.ExchangeOKX
	solSignal.Exchange = "OKX"
	solSignal.Coinpair = "SOLUSDT.P"
	solSignal.Ticker = "OKX:SOLUSDT.P"
	solSignal.Price = trading.NewFlexibleFloat(120)
	solSignal.Amount = trading.NewFlexibleFloat(250)
	sol, _, err := srv.Orders.RecordAccepted(solSignal, "search-http-sol", now.Add(4*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Orders.MarkFailedCode(sol.SignalID, "51001", errors.New("Instrument ID does not exist"), now.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}

	fetch := func(target string) struct {
		Orders     []storage.OrderRecord `json:"orders"`
		Total      int                   `json:"total"`
		Limit      int                   `json:"limit"`
		Offset     int                   `json:"offset"`
		Page       int                   `json:"page"`
		TotalPages int                   `json:"total_pages"`
	} {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.SetBasicAuth("admin", "Admin123")
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", target, rr.Code, rr.Body.String())
		}
		var resp struct {
			Orders     []storage.OrderRecord `json:"orders"`
			Total      int                   `json:"total"`
			Limit      int                   `json:"limit"`
			Offset     int                   `json:"offset"`
			Page       int                   `json:"page"`
			TotalPages int                   `json:"total_pages"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		return resp
	}
	if resp := fetch("/tvbot/orders?limit=10&q=btcusdt"); resp.Total != 1 || len(resp.Orders) != 1 || resp.Orders[0].Coinpair != "BTCUSDT.P" {
		t.Fatalf("bad symbol search response: %#v", resp)
	}
	if resp := fetch("/tvbot/orders?limit=10&q=500"); resp.Total != 1 || len(resp.Orders) != 1 || resp.Orders[0].Amount != "500" {
		t.Fatalf("bad amount search response: %#v", resp)
	}
	if resp := fetch("/tvbot/orders?limit=10&q=okx-999"); resp.Total != 1 || len(resp.Orders) != 1 || resp.Orders[0].Result.OrdID != "okx-999" {
		t.Fatalf("bad order id search response: %#v", resp)
	}
	if resp := fetch("/tvbot/orders?limit=10&q=usdt.p&exchange=okx"); resp.Total != 2 || len(resp.Orders) != 2 {
		t.Fatalf("bad OKX scoped search response: %#v", resp)
	}
	if resp := fetch("/tvbot/orders?limit=10&q=btc&exchange=binance"); resp.Total != 0 || len(resp.Orders) != 0 {
		t.Fatalf("bad Binance scoped search response: %#v", resp)
	}
	if resp := fetch("/tvbot/orders?limit=1&offset=1&q=usdt.p"); resp.Total != 3 || resp.Limit != 1 || resp.Offset != 1 || resp.Page != 2 || resp.TotalPages != 3 || len(resp.Orders) != 1 {
		t.Fatalf("bad search pagination response: %#v", resp)
	}
}

func TestTVOrderAllowsUnconfiguredCoinpair(t *testing.T) {
	srv := newTestServer(t)
	cfg := srv.ConfigStore.Get()
	cfg.Symbols = map[string]config.SymbolConfig{}
	srv.ConfigStore = config.NewStore("", cfg)
	signal := validSignal(t, srv)
	signal.Action = trading.ActionShort
	signal.Coinpair = "DOGEUSDT.P"
	signal.Ticker = "OKX:DOGEUSDT.P"
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
		SignalID string `json:"signal_id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-srv.Executor.(fakeExecutor).calls:
		if got.Coinpair != "DOGEUSDT.P" || got.Action != trading.ActionShort {
			t.Fatalf("bad executed signal: %#v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("executor was not called")
	}
	waitOrderStatus(t, srv.Orders, resp.SignalID, storage.StatusSubmitted)
}

func TestOrderRetryCreatesNewOrderAndExecutes(t *testing.T) {
	srv := newTestServer(t)
	installOKXRetryTicker(t, srv, "BTC-USDT-SWAP", "50119", "50121", "50120")
	signal := validSignal(t, srv)
	signal.APIID = "backup"
	signal.OrderIntent = "entry_long|script=4.1.0|adx=18.42|adx_tf=60"
	signal.Text = signal.OrderIntent
	signal.Token = srv.Token.Generate(signal.CanonicalTokenPayload())
	body, err := json.Marshal(signal)
	if err != nil {
		t.Fatal(err)
	}
	first := httptest.NewRecorder()
	srv.ServeHTTP(first, httptest.NewRequest(http.MethodPost, "/tvorder", bytes.NewReader(body)))
	if first.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", first.Code, first.Body.String())
	}
	var firstResp struct {
		SignalID string `json:"signal_id"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &firstResp); err != nil {
		t.Fatal(err)
	}
	waitOrderStatus(t, srv.Orders, firstResp.SignalID, storage.StatusSubmitted)
	select {
	case <-srv.Executor.(fakeExecutor).calls:
	case <-time.After(time.Second):
		t.Fatal("initial order was not executed")
	}
	sourceID := firstResp.SignalID
	if err := srv.Orders.MarkFailed(sourceID, fmt.Errorf("okx failed"), srv.now()); err != nil {
		t.Fatal(err)
	}
	cfg := srv.ConfigStore.Get()
	cfg.Trading.Leverage = 8
	srv.ConfigStore = config.NewStore("", cfg)

	req := httptest.NewRequest(http.MethodPost, "/tvbot/orders/"+sourceID+"/retry", nil)
	req.SetBasicAuth("admin", "Admin123")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("retry status=%d body=%s", rr.Code, rr.Body.String())
	}
	var retryResp struct {
		SignalID string `json:"signal_id"`
		RetryOf  string `json:"retry_of"`
		Price    string `json:"price"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &retryResp); err != nil {
		t.Fatal(err)
	}
	if retryResp.RetryOf != sourceID || retryResp.SignalID == "" || retryResp.SignalID == sourceID {
		t.Fatalf("bad retry response: %#v", retryResp)
	}
	if retryResp.Price != "50120" {
		t.Fatalf("retry response should include refreshed market price, got %#v", retryResp)
	}
	select {
	case got := <-srv.Executor.(fakeExecutor).calls:
		if got.Action != trading.ActionShort || got.APIID != "backup" || got.Coinpair != "BTC" || got.Price.Value != 50120 {
			t.Fatalf("bad retry signal: %#v", got)
		}
		if got.SourceAction != trading.ActionLong || got.PositionSide != trading.PositionSideShort || got.MarketStrategy != trading.MarketStrategyScalp || got.ADX == nil || *got.ADX != 18.42 {
			t.Fatalf("retry signal lost ADX routing metadata: %#v", got)
		}
		if got.Amount.Value != 100 || got.Risk.Type == "" {
			t.Fatalf("retry signal lost order settings: %#v", got)
		}
		if got.Leverage != 8 {
			t.Fatalf("retry should use current configured leverage, got signal: %#v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("retry order was not executed")
	}
	gotRetry := waitOrderStatus(t, srv.Orders, retryResp.SignalID, storage.StatusSubmitted)
	orders := srv.Orders.List(10)
	if len(orders) != 2 {
		t.Fatalf("orders len=%d records=%#v", len(orders), orders)
	}
	if gotRetry.SignalID == sourceID {
		t.Fatalf("retry should create a new record: %#v", gotRetry)
	}
	if gotRetry.Price != "50120" {
		t.Fatalf("retry record should store refreshed market price: %#v", gotRetry)
	}
	if gotRetry.SourceAction != trading.ActionLong || gotRetry.MarketStrategy != trading.MarketStrategyScalp || gotRetry.ADX == nil || *gotRetry.ADX != 18.42 {
		t.Fatalf("retry record lost ADX routing metadata: %#v", gotRetry)
	}
}

func TestOrderRetryMatchingFilterIsIgnoredBeforeMarketRequest(t *testing.T) {
	srv := newTestServer(t)
	signal := validSignal(t, srv)
	signal.PositionEffect = trading.PositionEffectOpen
	source, err := srv.Orders.RecordIgnored(signal, "BTC", srv.now())
	if err != nil {
		t.Fatal(err)
	}
	marketRequests := 0
	marketServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		marketRequests++
		http.Error(w, "market request should have been filtered", http.StatusInternalServerError)
	}))
	defer marketServer.Close()
	cfg := srv.ConfigStore.Get()
	cfg.Trading.BaseURL = marketServer.URL
	cfg.Trading.IgnoredCoinpairs = []string{"btc"}
	srv.ConfigStore = config.NewStore("", cfg)
	srv.OKXHTTPClient = marketServer.Client()

	req := httptest.NewRequest(http.MethodPost, "/tvbot/orders/"+source.SignalID+"/retry", nil)
	req.SetBasicAuth("admin", "Admin123")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("retry status=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Status   string `json:"status"`
		Reason   string `json:"reason"`
		SignalID string `json:"signal_id"`
		RetryOf  string `json:"retry_of"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Status != "ignored" || resp.Reason != "coinpair_filtered" || resp.SignalID == "" || resp.RetryOf != source.SignalID {
		t.Fatalf("bad ignored retry response: %#v", resp)
	}
	if marketRequests != 0 {
		t.Fatalf("ignored retry made %d market requests", marketRequests)
	}
	select {
	case got := <-srv.Executor.(fakeExecutor).calls:
		t.Fatalf("ignored retry should not execute: %#v", got)
	case <-time.After(50 * time.Millisecond):
	}
	rec, ok := srv.Orders.Get(resp.SignalID)
	if !ok || rec.Status != storage.StatusIgnored || rec.ErrorCode != "coinpair_filtered" {
		t.Fatalf("bad ignored retry record: %#v found=%v", rec, ok)
	}
}

func TestOrderRetryIgnoredFilterExecutesAfterFilterRemoval(t *testing.T) {
	srv := newTestServer(t)
	signal := validSignal(t, srv)
	signal.PositionEffect = trading.PositionEffectOpen
	source, err := srv.Orders.RecordIgnored(signal, "BTC", srv.now())
	if err != nil {
		t.Fatal(err)
	}
	installOKXRetryTicker(t, srv, "BTC-USDT-SWAP", "49999", "50001", "50000")

	req := httptest.NewRequest(http.MethodPost, "/tvbot/orders/"+source.SignalID+"/retry", nil)
	req.SetBasicAuth("admin", "Admin123")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted || !bytes.Contains(rr.Body.Bytes(), []byte(`"status":"accepted"`)) || !bytes.Contains(rr.Body.Bytes(), []byte(`"retry_of":"`+source.SignalID+`"`)) {
		t.Fatalf("removed-filter retry status=%d body=%s", rr.Code, rr.Body.String())
	}
	select {
	case got := <-srv.Executor.(fakeExecutor).calls:
		if got.Coinpair != "BTC" || got.Price.Value != 50000 {
			t.Fatalf("bad removed-filter retry signal: %#v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("removed-filter retry did not reach executor")
	}
}

func TestOrderRetryActiveCooldownIsIgnoredBeforeMarketRequest(t *testing.T) {
	srv := newTestServer(t)
	signal := validSignal(t, srv)
	signal.PositionEffect = trading.PositionEffectOpen
	source, err := srv.Orders.RecordIgnoredReason(signal, "coinpair_cooldown", "seed cooldown", srv.now())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := srv.recordCoinpairCooldown("retry-stop:1", "exchange_fill", trading.ExchangeOKX, "default", "49000", srv.now(), "BTC-USDT-SWAP"); err != nil {
		t.Fatal(err)
	}
	marketRequests := 0
	marketServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		marketRequests++
		http.Error(w, "market request should have been blocked", http.StatusInternalServerError)
	}))
	defer marketServer.Close()
	cfg := srv.ConfigStore.Get()
	cfg.Trading.BaseURL = marketServer.URL
	srv.ConfigStore = config.NewStore("", cfg)
	srv.OKXHTTPClient = marketServer.Client()

	req := httptest.NewRequest(http.MethodPost, "/tvbot/orders/"+source.SignalID+"/retry", nil)
	req.SetBasicAuth("admin", "Admin123")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("retry status=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Status   string `json:"status"`
		Reason   string `json:"reason"`
		SignalID string `json:"signal_id"`
		RetryOf  string `json:"retry_of"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Status != "ignored" || resp.Reason != "coinpair_cooldown" || resp.SignalID == "" || resp.RetryOf != source.SignalID {
		t.Fatalf("bad cooldown retry response: %#v", resp)
	}
	if marketRequests != 0 {
		t.Fatalf("cooldown retry made %d market requests", marketRequests)
	}
	select {
	case got := <-srv.Executor.(fakeExecutor).calls:
		t.Fatalf("cooldown retry should not execute: %#v", got)
	case <-time.After(50 * time.Millisecond):
	}
	rec, ok := srv.Orders.Get(resp.SignalID)
	if !ok || rec.Status != storage.StatusIgnored || rec.ErrorCode != "coinpair_cooldown" {
		t.Fatalf("bad cooldown retry record: %#v found=%v", rec, ok)
	}
}

func TestOrderRetryRejectsSubmittedRecord(t *testing.T) {
	srv := newTestServer(t)
	signal := validSignal(t, srv)
	body, err := json.Marshal(signal)
	if err != nil {
		t.Fatal(err)
	}
	first := httptest.NewRecorder()
	srv.ServeHTTP(first, httptest.NewRequest(http.MethodPost, "/tvorder", bytes.NewReader(body)))
	if first.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", first.Code, first.Body.String())
	}
	var firstResp struct {
		SignalID string `json:"signal_id"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &firstResp); err != nil {
		t.Fatal(err)
	}
	waitOrderStatus(t, srv.Orders, firstResp.SignalID, storage.StatusSubmitted)
	select {
	case <-srv.Executor.(fakeExecutor).calls:
	case <-time.After(time.Second):
		t.Fatal("initial order was not executed")
	}
	req := httptest.NewRequest(http.MethodPost, "/tvbot/orders/"+firstResp.SignalID+"/retry", nil)
	req.SetBasicAuth("admin", "Admin123")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("retry submitted status=%d body=%s", rr.Code, rr.Body.String())
	}
	select {
	case got := <-srv.Executor.(fakeExecutor).calls:
		t.Fatalf("non-failed order should not retry: %#v", got)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestOrderRetryRejectsUnsupportedOKXDemoInstrument(t *testing.T) {
	srv := newTestServer(t)
	signal := validSignal(t, srv)
	signal.Coinpair = "PUMPUSDT.P"
	signal.Ticker = "OKX:PUMPUSDT.P"
	signal.TradeEnv = trading.TradeEnvDemo
	source, _, err := srv.Orders.RecordAccepted(signal, "retry-unsupported-okx-source", srv.now())
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Orders.MarkFailedCode(source.SignalID, "51001", errors.New("Instrument ID, Instrument ID code, or Spread ID doesn't exist."), srv.now()); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/tvbot/orders/"+source.SignalID+"/retry", nil)
	req.SetBasicAuth("admin", "Admin123")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusConflict || !strings.Contains(rr.Body.String(), "OKX 模拟盘不支持 PUMP-USDT-SWAP，无法重试") {
		t.Fatalf("unsupported instrument retry status=%d body=%s", rr.Code, rr.Body.String())
	}
	select {
	case got := <-srv.Executor.(fakeExecutor).calls:
		t.Fatalf("unsupported instrument reached executor: %#v", got)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestOrderRetryMarksInstrumentUnsupportedWhenPriceRefreshReturns51001(t *testing.T) {
	srv := newTestServer(t)
	signal := validSignal(t, srv)
	signal.Coinpair = "PUMPUSDT.P"
	signal.Ticker = "OKX:PUMPUSDT.P"
	signal.TradeEnv = trading.TradeEnvDemo
	source, _, err := srv.Orders.RecordAccepted(signal, "retry-price-unsupported-okx-source", srv.now())
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Orders.MarkFailed(source.SignalID, errors.New("seed failure"), srv.now()); err != nil {
		t.Fatal(err)
	}

	marketServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v5/market/ticker" || r.URL.Query().Get("instId") != "PUMP-USDT-SWAP" {
			t.Fatalf("unexpected OKX retry ticker request %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":"1","msg":"Instrument ID doesn't exist.","data":[{"sCode":"51001","sMsg":"Instrument ID, Instrument ID code, or Spread ID doesn't exist."}]}`))
	}))
	t.Cleanup(marketServer.Close)
	cfg := srv.ConfigStore.Get()
	cfg.Trading.BaseURL = marketServer.URL
	srv.ConfigStore = config.NewStore("", cfg)
	srv.OKXHTTPClient = marketServer.Client()

	req := httptest.NewRequest(http.MethodPost, "/tvbot/orders/"+source.SignalID+"/retry", nil)
	req.SetBasicAuth("admin", "Admin123")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusConflict || !strings.Contains(rr.Body.String(), "OKX 模拟盘不支持 PUMP-USDT-SWAP，无法重试") {
		t.Fatalf("unsupported price refresh status=%d body=%s", rr.Code, rr.Body.String())
	}
	updated, ok := srv.Orders.Get(source.SignalID)
	if !ok || updated.ErrorCode != "okx_instrument_unsupported" || updated.Error != "OKX 模拟盘不支持 PUMP-USDT-SWAP，无法重试" {
		t.Fatalf("unsupported source was not marked non-retriable: %#v", updated)
	}
}

func TestOrderRetryRejectsAcceptedDuplicateRejectedAndUnsafeIgnoredRecords(t *testing.T) {
	srv := newTestServer(t)
	now := srv.now()
	signal := validSignal(t, srv)

	accepted, duplicate, err := srv.Orders.RecordAccepted(signal, "retry-status-source", now)
	if err != nil || duplicate {
		t.Fatalf("seed accepted: duplicate=%v err=%v", duplicate, err)
	}
	duplicateRecord, duplicate, err := srv.Orders.RecordAccepted(signal, "retry-status-source", now.Add(time.Second))
	if err != nil || !duplicate {
		t.Fatalf("seed duplicate: duplicate=%v err=%v", duplicate, err)
	}
	rejected, err := srv.Orders.RecordRejected(signal, "invalid_signal", errors.New("seed rejection"), now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	adxIgnored, err := srv.Orders.RecordIgnoredReason(signal, "adx_transition", "seed transition", now.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	unknownIgnored, err := srv.Orders.RecordIgnoredReason(signal, "future_safety_gate", "seed unknown gate", now.Add(4*time.Second))
	if err != nil {
		t.Fatal(err)
	}

	for _, source := range []storage.OrderRecord{accepted, duplicateRecord, rejected, adxIgnored, unknownIgnored} {
		req := httptest.NewRequest(http.MethodPost, "/tvbot/orders/"+source.SignalID+"/retry", nil)
		req.SetBasicAuth("admin", "Admin123")
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		if rr.Code != http.StatusConflict || !bytes.Contains(rr.Body.Bytes(), []byte(`"error":"not_retriable"`)) {
			t.Fatalf("source status=%s code=%s retry status=%d body=%s", source.Status, source.ErrorCode, rr.Code, rr.Body.String())
		}
	}
	select {
	case got := <-srv.Executor.(fakeExecutor).calls:
		t.Fatalf("non-retriable order reached executor: %#v", got)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestTVOrderPassesSelectedAPIID(t *testing.T) {
	srv := newTestServer(t)
	signal := validSignal(t, srv)
	signal.APIID = "backup"
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
	resp := decodeTVOrderSignalResponse(t, rr.Body.Bytes())
	select {
	case got := <-srv.Executor.(fakeExecutor).calls:
		if got.APIID != "backup" {
			t.Fatalf("api id = %q", got.APIID)
		}
	case <-time.After(time.Second):
		t.Fatal("executor was not called")
	}
	waitOrderStatus(t, srv.Orders, resp.SignalID, storage.StatusSubmitted)
}

func TestTVOrderRecordsRejectedSignals(t *testing.T) {
	srv := newTestServer(t)
	body := []byte(`{
		"token": "present-but-not-checked-before-signal-validation",
		"sent_at": "2026-07-24T03:00:00Z",
		"action": "{{strategy.order.action}}",
		"ticker": "ETHUSDT.P",
		"coinpair": "ETHUSDT.P",
		"price": "1893.55",
		"exchange": "OKX",
		"interval": "15",
		"condition": "{{strategy.order.comment}}",
		"text": "{{strategy.order.alert_message}}",
		"order_intent": "{{strategy.order.alert_message}}",
		"token_nonce": "raw-nonce-value",
		"source": "tradingview"
	}`)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/tvorder", bytes.NewReader(body)))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !bytes.Contains(rr.Body.Bytes(), []byte(`"error":"invalid_signal"`)) {
		t.Fatalf("expected invalid_signal response, got %s", rr.Body.String())
	}
	select {
	case got := <-srv.Executor.(fakeExecutor).calls:
		t.Fatalf("rejected signal should not execute: %#v", got)
	case <-time.After(50 * time.Millisecond):
	}
	ordersReq := httptest.NewRequest(http.MethodGet, "/tvbot/orders", nil)
	ordersReq.SetBasicAuth("admin", "Admin123")
	ordersRR := httptest.NewRecorder()
	srv.ServeHTTP(ordersRR, ordersReq)
	if ordersRR.Code != http.StatusOK {
		t.Fatalf("orders status=%d body=%s", ordersRR.Code, ordersRR.Body.String())
	}
	var list struct {
		Orders []storage.OrderRecord `json:"orders"`
	}
	if err := json.Unmarshal(ordersRR.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Orders) != 1 {
		t.Fatalf("orders len=%d records=%#v", len(list.Orders), list.Orders)
	}
	got := list.Orders[0]
	if got.Status != storage.StatusRejected || got.ErrorCode != "invalid_signal" || !strings.Contains(got.Error, "action must be") {
		t.Fatalf("bad rejected record: %#v", got)
	}
	if got.Action != trading.Side("{{strategy.order.action}}") || got.Coinpair != "ETHUSDT.P" || got.Price != "1893.55" || got.Amount != "100" {
		t.Fatalf("rejected record lost signal fields: %#v", got)
	}
	if got.OrderIntent != "{{strategy.order.alert_message}}" {
		t.Fatalf("rejected record lost order intent: %#v", got)
	}
	if got.RawJSON == "" ||
		!strings.Contains(got.RawJSON, `"token": "[redacted]"`) ||
		!strings.Contains(got.RawJSON, `"token_nonce": "[redacted]"`) ||
		strings.Contains(got.RawJSON, "present-but-not-checked-before-signal-validation") ||
		strings.Contains(got.RawJSON, "raw-nonce-value") {
		t.Fatalf("rejected record should include redacted raw json: %#v", got)
	}
}

func TestTVOrderRecordsBadJSONSignals(t *testing.T) {
	srv := newTestServer(t)
	body := []byte(`{"action":"buy","ticker":"BTCUSDT","price":"{{close}}","token":"x"}`)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/tvorder", bytes.NewReader(body)))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	ordersReq := httptest.NewRequest(http.MethodGet, "/tvbot/orders", nil)
	ordersReq.SetBasicAuth("admin", "Admin123")
	ordersRR := httptest.NewRecorder()
	srv.ServeHTTP(ordersRR, ordersReq)
	if ordersRR.Code != http.StatusOK {
		t.Fatalf("orders status=%d body=%s", ordersRR.Code, ordersRR.Body.String())
	}
	var list struct {
		Orders []storage.OrderRecord `json:"orders"`
	}
	if err := json.Unmarshal(ordersRR.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Orders) != 1 || list.Orders[0].Status != storage.StatusRejected || list.Orders[0].ErrorCode != "bad_json" {
		t.Fatalf("bad_json signal should be listed in history: %#v", list.Orders)
	}
	if list.Orders[0].Action != trading.ActionLong || list.Orders[0].Ticker != "BTCUSDT" {
		t.Fatalf("bad_json preview lost readable fields: %#v", list.Orders[0])
	}
}

func TestTVOrderAppliesConfiguredOrderSettings(t *testing.T) {
	srv := newTestServer(t)
	cfg := srv.ConfigStore.Get()
	cfg.Trading.OrderAmountUSDT = 250
	cfg.Trading.Leverage = 8
	cfg.Trading.RiskType = string(trading.RiskTrailing)
	cfg.Trading.TrailingPct = 1.5
	srv.ConfigStore = config.NewStore("", cfg)
	signal := trading.Signal{
		Action:   trading.ActionLong,
		APIID:    "main",
		Coinpair: "BTC",
		Price:    trading.NewFlexibleFloat(50000),
		SentAt:   "2026-07-24T03:00:00Z",
		Ticker:   "BTCUSDT",
	}
	signal.Normalize()
	signal.Token = srv.Token.Generate(signal.CanonicalWebhookTokenPayload())
	body, err := json.Marshal(signal)
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/tvorder", bytes.NewReader(body)))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	resp := decodeTVOrderSignalResponse(t, rr.Body.Bytes())
	select {
	case got := <-srv.Executor.(fakeExecutor).calls:
		if got.Amount.Value != 250 || got.Leverage != 8 || got.Risk.Type != trading.RiskTrailing {
			t.Fatalf("configured settings not applied: %#v", got)
		}
		if got.Risk.TrailingPct == nil || got.Risk.TrailingPct.Value != 1.5 {
			t.Fatalf("trailing setting not applied: %#v", got.Risk)
		}
	case <-time.After(time.Second):
		t.Fatal("executor was not called")
	}
	waitOrderStatus(t, srv.Orders, resp.SignalID, storage.StatusSubmitted)
}

func TestTVBotTemplatesRequiresAdminAndReturnsJSON(t *testing.T) {
	srv := newTestServer(t)
	reqBody := []byte(`{"price_source":"high","api_id":"backup","coinpair":"ethusdt.p","direction":"long","leverage":3,"amount":50}`)
	req := httptest.NewRequest(http.MethodPost, "/tvbot/templates", bytes.NewReader(reqBody))
	req.Header.Set("X-Admin-Token", "admin")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("template cache-control=%q", got)
	}
	var resp trading.TemplateResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Token) != 88 ||
		!bytes.Contains([]byte(resp.JSON), []byte(`"price": "{{high}}"`)) ||
		!bytes.Contains([]byte(resp.JSON), []byte(`"api_id": "backup"`)) ||
		!bytes.Contains([]byte(resp.JSON), []byte(`"action": "buy"`)) ||
		!bytes.Contains([]byte(resp.JSON), []byte(`"ticker": "ETHUSDT.P"`)) ||
		!bytes.Contains([]byte(resp.JSON), []byte(`"coinpair": "ETHUSDT.P"`)) ||
		!bytes.Contains([]byte(resp.JSON), []byte(`"order_intent": "{{strategy.order.alert_message}}"`)) {
		t.Fatalf("bad template response: %#v", resp)
	}
	if bytes.Contains([]byte(resp.JSON), []byte(`"risk"`)) {
		t.Fatalf("template should not include risk: %s", resp.JSON)
	}
	if bytes.Contains([]byte(resp.JSON), []byte(`"amount"`)) || bytes.Contains([]byte(resp.JSON), []byte(`"leverage"`)) {
		t.Fatalf("template should not include server-side order settings: %s", resp.JSON)
	}
	if strings.LastIndex(resp.JSON, `"token"`) < strings.LastIndex(resp.JSON, `"source"`) {
		t.Fatalf("token should be the final JSON field: %s", resp.JSON)
	}
}

func TestTVBotAPIKeysSaveAndMask(t *testing.T) {
	srv := newTestServer(t)
	reqBody := []byte(`{"api_key":"abcd12345678wxyz","secret_key":"super-secret-value","passphrase":"phrase-value"}`)
	req := httptest.NewRequest(http.MethodPut, "/tvbot/api-keys", bytes.NewReader(reqBody))
	req.SetBasicAuth("admin", "Admin123")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("save status=%d body=%s", rr.Code, rr.Body.String())
	}
	if bytes.Contains(rr.Body.Bytes(), []byte("super-secret-value")) || bytes.Contains(rr.Body.Bytes(), []byte("phrase-value")) {
		t.Fatalf("response leaked secret material: %s", rr.Body.String())
	}
	if !bytes.Contains(rr.Body.Bytes(), []byte("abcd...wxyz")) {
		t.Fatalf("response did not include masked api key: %s", rr.Body.String())
	}
	getReq := httptest.NewRequest(http.MethodGet, "/tvbot/api-keys", nil)
	getReq.SetBasicAuth("admin", "Admin123")
	get := httptest.NewRecorder()
	srv.ServeHTTP(get, getReq)
	if get.Code != http.StatusOK || bytes.Contains(get.Body.Bytes(), []byte("super-secret-value")) || bytes.Contains(get.Body.Bytes(), []byte("phrase-value")) {
		t.Fatalf("get status=%d body=%s", get.Code, get.Body.String())
	}
}

func TestTVBotAPIKeysAreScopedToAuthenticatedUser(t *testing.T) {
	srv := newTestServer(t)
	root := t.TempDir()
	aliceOKX, err := okx.NewCredentialStore(filepath.Join(root, "alice-okx.json"), okx.Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	bobOKX, err := okx.NewCredentialStore(filepath.Join(root, "bob-okx.json"), okx.Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	aliceBinance, err := binance.NewCredentialStore(filepath.Join(root, "alice-binance.json"), binance.Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	bobBinance, err := binance.NewCredentialStore(filepath.Join(root, "bob-binance.json"), binance.Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	srv.UserCredentialResolver = testUserCredentialResolver{
		"alice": {OKX: aliceOKX, Binance: aliceBinance},
		"bob":   {OKX: bobOKX, Binance: bobBinance},
	}
	srv.RequireUnifiedUser = func(http.ResponseWriter, *http.Request) bool { return true }
	adminChecks := 0
	srv.RequireUnifiedAdmin = func(http.ResponseWriter, *http.Request) bool {
		adminChecks++
		return false
	}
	srv.CurrentUser = func(r *http.Request) (string, bool) { return r.Header.Get("X-Test-User"), true }

	put := func(user, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, "/tvbot/api-keys?exchange=okx", strings.NewReader(body))
		req.Header.Set("X-Test-User", user)
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		return rr
	}
	get := func(user string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/tvbot/api-keys?exchange=okx", nil)
		req.Header.Set("X-Test-User", user)
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		return rr
	}

	if rr := put("alice", `{"id":"alice-main","api_key":"alice-okx-key","secret_key":"alice-okx-secret","passphrase":"alice-okx-pass","active":true}`); rr.Code != http.StatusOK {
		t.Fatalf("alice save status=%d body=%s", rr.Code, rr.Body.String())
	}
	if adminChecks != 0 {
		t.Fatalf("user-scoped API key save unexpectedly required administrator auth: checks=%d", adminChecks)
	}
	bobBefore := get("bob")
	if bobBefore.Code != http.StatusOK || bytes.Contains(bobBefore.Body.Bytes(), []byte("alice")) || bytes.Contains(bobBefore.Body.Bytes(), []byte("alic...-key")) {
		t.Fatalf("bob can see alice credentials: status=%d body=%s", bobBefore.Code, bobBefore.Body.String())
	}
	alice := get("alice")
	if alice.Code != http.StatusOK || !bytes.Contains(alice.Body.Bytes(), []byte("alic...-key")) {
		t.Fatalf("alice cannot see her masked credentials: status=%d body=%s", alice.Code, alice.Body.String())
	}
	if rr := put("bob", `{"id":"bob-main","api_key":"bob-okx-key","secret_key":"bob-okx-secret","passphrase":"bob-okx-pass","active":true}`); rr.Code != http.StatusOK {
		t.Fatalf("bob save status=%d body=%s", rr.Code, rr.Body.String())
	}
	if aliceAfter := get("alice"); !bytes.Contains(aliceAfter.Body.Bytes(), []byte("alic...-key")) || bytes.Contains(aliceAfter.Body.Bytes(), []byte("bob-")) {
		t.Fatalf("alice view changed after bob save: %s", aliceAfter.Body.String())
	}
}

func TestTVBotBinanceAPIKeysSaveAndTest(t *testing.T) {
	srv := newTestServer(t)
	var sawAPIKey string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/fapi/v3/balance":
			sawAPIKey = r.Header.Get("X-MBX-APIKEY")
			_, _ = w.Write([]byte(`[{"asset":"USDT","balance":"1000.25","availableBalance":"900.25","crossWalletBalance":"980.25","updateTime":1784886000000}]`))
		case "/fapi/v1/exchangeInfo":
			_, _ = w.Write([]byte(`{"symbols":[{"symbol":"BTCUSDT","status":"TRADING"}]}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer ts.Close()
	cfg := srv.ConfigStore.Get()
	cfg.Trading.BinanceDemoBaseURL = ts.URL
	srv.ConfigStore = config.NewStore("", cfg)
	reqBody := []byte(`{"exchange":"binance","id":"main","name":"Binance Main","api_key":"bnabcd12345678wxyz","secret_key":"binance-secret-value","active":true}`)
	req := httptest.NewRequest(http.MethodPut, "/tvbot/api-keys?exchange=binance", bytes.NewReader(reqBody))
	req.SetBasicAuth("admin", "Admin123")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("save status=%d body=%s", rr.Code, rr.Body.String())
	}
	if bytes.Contains(rr.Body.Bytes(), []byte("binance-secret-value")) || !bytes.Contains(rr.Body.Bytes(), []byte("bnab...wxyz")) {
		t.Fatalf("bad save response: %s", rr.Body.String())
	}

	testReq := httptest.NewRequest(http.MethodPost, "/tvbot/api-keys/test", bytes.NewReader([]byte(`{"exchange":"binance","id":"main"}`)))
	testReq.SetBasicAuth("admin", "Admin123")
	testRR := httptest.NewRecorder()
	srv.ServeHTTP(testRR, testReq)
	if testRR.Code != http.StatusOK {
		t.Fatalf("test status=%d body=%s", testRR.Code, testRR.Body.String())
	}
	if sawAPIKey != "bnabcd12345678wxyz" {
		t.Fatalf("used api key %q", sawAPIKey)
	}
	if !bytes.Contains(testRR.Body.Bytes(), []byte(`"exchange":"binance"`)) || !bytes.Contains(testRR.Body.Bytes(), []byte(`"api_id":"main"`)) || !bytes.Contains(testRR.Body.Bytes(), []byte(`"eq":"1000.25"`)) {
		t.Fatalf("bad test response: %s", testRR.Body.String())
	}
}

func TestTVBotGateAPIKeysSaveAndTest(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v4/futures/usdt/accounts":
			_, _ = w.Write([]byte(`{"currency":"USDT","total":"1000.25","available":"900.25"}`))
		case "/api/v4/futures/usdt/contracts":
			_, _ = w.Write([]byte(`[{"name":"BTC_USDT","status":"trading"}]`))
		default:
			t.Fatalf("unexpected Gate path %s", r.URL.Path)
		}
	}))
	defer ts.Close()
	cfg := srv.ConfigStore.Get()
	cfg.Trading.GateDemoBaseURL = ts.URL
	srv.ConfigStore = config.NewStore("", cfg)
	store, err := gate.NewCredentialStore(filepath.Join(t.TempDir(), "gate.json"), gate.Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	srv.GateCredentials = store
	req := httptest.NewRequest(http.MethodPut, "/tvbot/api-keys?exchange=gate", strings.NewReader(`{"exchange":"gate","id":"main","name":"Gate Main","api_key":"gateabcd12345678wxyz","secret_key":"gate-secret-value","active":true}`))
	req.SetBasicAuth("admin", "Admin123")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || bytes.Contains(rr.Body.Bytes(), []byte("gate-secret-value")) || !bytes.Contains(rr.Body.Bytes(), []byte("gate...wxyz")) {
		t.Fatalf("bad Gate save response: %d %s", rr.Code, rr.Body.String())
	}
	testReq := httptest.NewRequest(http.MethodPost, "/tvbot/api-keys/test", strings.NewReader(`{"exchange":"gate","id":"main"}`))
	testReq.SetBasicAuth("admin", "Admin123")
	testRR := httptest.NewRecorder()
	srv.ServeHTTP(testRR, testReq)
	if testRR.Code != http.StatusOK || !bytes.Contains(testRR.Body.Bytes(), []byte(`"exchange":"gate"`)) || !bytes.Contains(testRR.Body.Bytes(), []byte(`"eq":"1000.25"`)) {
		t.Fatalf("bad Gate test response: %d %s", testRR.Code, testRR.Body.String())
	}
}

func TestTVBotAPIKeysSeparateLiveAndDemoCredentials(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v4/futures/usdt/accounts":
			_, _ = w.Write([]byte(`{"currency":"USDT","total":"1000","available":"900"}`))
		case "/api/v4/futures/usdt/contracts":
			_, _ = w.Write([]byte(`[{"name":"BTC_USDT","status":"trading"}]`))
		default:
			t.Fatalf("unexpected Gate path %s", r.URL.Path)
		}
	}))
	defer ts.Close()
	cfg := srv.ConfigStore.Get()
	cfg.Trading.GateDemoBaseURL = ts.URL
	cfg.Trading.GateBaseURL = ts.URL
	srv.ConfigStore = config.NewStore("", cfg)
	demoStore, err := gate.NewCredentialStore(filepath.Join(t.TempDir(), "gate-demo.json"), gate.Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	liveStore, err := gate.NewCredentialStore(filepath.Join(t.TempDir(), "gate-live.json"), gate.Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	srv.GateCredentials = demoStore
	srv.GateLiveCredentials = liveStore

	put := func(env, apiKey string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, "/tvbot/api-keys?exchange=gate&env="+env, strings.NewReader(`{"exchange":"gate","env":"`+env+`","id":"same","api_key":"`+apiKey+`","secret_key":"secret-`+env+`","active":true}`))
		req.SetBasicAuth("admin", "Admin123")
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		return rr
	}
	if rr := put("demo", "demoabcd12345678wxyz"); rr.Code != http.StatusOK {
		t.Fatalf("demo save status=%d body=%s", rr.Code, rr.Body.String())
	}
	if rr := put("live", "liveabcd12345678wxyz"); rr.Code != http.StatusOK {
		t.Fatalf("live save status=%d body=%s", rr.Code, rr.Body.String())
	}
	get := func(env string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/tvbot/api-keys?exchange=gate&env="+env, nil)
		req.SetBasicAuth("admin", "Admin123")
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		return rr
	}
	demo := get("demo")
	live := get("live")
	if demo.Code != http.StatusOK || !bytes.Contains(demo.Body.Bytes(), []byte("demo...wxyz")) || bytes.Contains(demo.Body.Bytes(), []byte("live...wxyz")) {
		t.Fatalf("demo credentials were not isolated: %d %s", demo.Code, demo.Body.String())
	}
	if live.Code != http.StatusOK || !bytes.Contains(live.Body.Bytes(), []byte("live...wxyz")) || bytes.Contains(live.Body.Bytes(), []byte("demo...wxyz")) {
		t.Fatalf("live credentials were not isolated: %d %s", live.Code, live.Body.String())
	}
	testReq := httptest.NewRequest(http.MethodPost, "/tvbot/api-keys/test", strings.NewReader(`{"exchange":"gate","env":"live","id":"same"}`))
	testReq.SetBasicAuth("admin", "Admin123")
	testRR := httptest.NewRecorder()
	srv.ServeHTTP(testRR, testReq)
	if testRR.Code != http.StatusOK || !bytes.Contains(testRR.Body.Bytes(), []byte(`"api_id":"same"`)) {
		t.Fatalf("live credential test failed: %d %s", testRR.Code, testRR.Body.String())
	}
}

func TestTVBotAPIKeysRenameActiveID(t *testing.T) {
	srv := newTestServer(t)
	reqBody := []byte(`{"exchange":"binance","id":"tvbot","name":"binance-moni","api_key":"bnabcd12345678wxyz","secret_key":"binance-secret-value","active":true}`)
	req := httptest.NewRequest(http.MethodPut, "/tvbot/api-keys?exchange=binance", bytes.NewReader(reqBody))
	req.SetBasicAuth("admin", "Admin123")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("save status=%d body=%s", rr.Code, rr.Body.String())
	}

	renameReq := httptest.NewRequest(http.MethodPatch, "/tvbot/api-keys?exchange=binance", bytes.NewReader([]byte(`{"exchange":"binance","id":"tvbot","new_id":"binance-prod"}`)))
	renameReq.SetBasicAuth("admin", "Admin123")
	renameRR := httptest.NewRecorder()
	srv.ServeHTTP(renameRR, renameReq)
	if renameRR.Code != http.StatusOK {
		t.Fatalf("rename status=%d body=%s", renameRR.Code, renameRR.Body.String())
	}
	if !bytes.Contains(renameRR.Body.Bytes(), []byte(`"active_id":"binance-prod"`)) ||
		!bytes.Contains(renameRR.Body.Bytes(), []byte(`"id":"binance-prod"`)) ||
		bytes.Contains(renameRR.Body.Bytes(), []byte(`"id":"tvbot"`)) ||
		bytes.Contains(renameRR.Body.Bytes(), []byte("binance-secret-value")) {
		t.Fatalf("bad rename response: %s", renameRR.Body.String())
	}
	creds, id, err := srv.BinanceCredentials.BinanceCredentials("")
	if err != nil {
		t.Fatal(err)
	}
	if id != "binance-prod" || creds.APIKey != "bnabcd12345678wxyz" || creds.SecretKey != "binance-secret-value" {
		t.Fatalf("bad renamed active credentials id=%q creds=%#v", id, creds)
	}
	if _, _, err := srv.BinanceCredentials.BinanceCredentials("tvbot"); err == nil {
		t.Fatal("old API ID should not resolve after rename")
	}
}

func TestTVBotAnalysisRequiresAdminAndReturnsExchangeSeparatedStats(t *testing.T) {
	srv := newTestServer(t)
	oldTradeTime := srv.now().Add(-25 * time.Hour).UnixMilli()
	fillTime1 := time.Date(2026, 7, 23, 3, 0, 0, 0, time.UTC).UnixMilli()
	fillTime2 := time.Date(2026, 7, 23, 4, 0, 0, 0, time.UTC).UnixMilli()
	binanceTradeTime := time.Date(2026, 7, 23, 5, 0, 0, 0, time.UTC).UnixMilli()
	candleTime1 := time.Date(2026, 7, 23, 2, 0, 0, 0, time.UTC).UnixMilli()
	candleTime2 := time.Date(2026, 7, 23, 3, 0, 0, 0, time.UTC).UnixMilli()
	var sawBalance, sawCandles, sawFills, sawOKXFunding, sawBinanceFunding, sawInstruments bool
	expectedBinanceStart := int64(0)
	expectedBinanceAPIKey := "binance-alt-key"
	sawBinanceSymbols := map[string]bool{}
	okxServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v5/account/balance":
			sawBalance = true
			if r.URL.RawQuery != "" {
				t.Fatalf("balance should request all assets, got query: %s", r.URL.RawQuery)
			}
			if r.Header.Get("x-simulated-trading") != "1" || r.Header.Get("OK-ACCESS-KEY") != "key" {
				t.Fatalf("missing private OKX headers")
			}
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"totalEq":"80078.07","adjEq":"80000","availEq":"79900","uTime":"1784880000000","details":[
				{"ccy":"BTC","eq":"1","eqUsd":"64973.4","availBal":"1","cashBal":"1","frozenBal":"0","uTime":"1784880000000"},
				{"ccy":"USDT","eq":"5000","eqUsd":"4996.65","availBal":"5000","cashBal":"5000","frozenBal":"0","uTime":"1784880000000"}
			]}]}`))
		case "/api/v5/market/candles":
			sawCandles = true
			if r.URL.Query().Get("instId") != "USDT-USD" || r.URL.Query().Get("bar") != "1H" || r.URL.Query().Get("limit") != "72" {
				t.Fatalf("bad candle query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(fmt.Sprintf(`{"code":"0","msg":"","data":[["%d","0.9990","0.9992","0.9989","0.9991","10","10","10","1"],["%d","0.9991","0.9993","0.9990","0.9992","12","12","12","1"]]}`, candleTime2, candleTime1)))
		case "/api/v5/trade/fills-history":
			sawFills = true
			if r.Header.Get("x-simulated-trading") != "1" || r.Header.Get("OK-ACCESS-KEY") != "key" {
				t.Fatalf("missing private OKX headers")
			}
			if r.URL.Query().Get("instType") != "SWAP" || r.URL.Query().Get("limit") != "100" {
				t.Fatalf("bad fills query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(fmt.Sprintf(`{"code":"0","msg":"","data":[
				{"instType":"SWAP","instId":"BTC-USDT-SWAP","tradeId":"t1","ordId":"o-btc-close","side":"sell","posSide":"long","fillPx":"50000","fillSz":"1","fillPnl":"2.5","fee":"-0.1","feeCcy":"USDT","fillTime":"%d"},
				{"instType":"SWAP","instId":"BTC-USDT-SWAP","tradeId":"t1b","ordId":"o-btc-close","side":"sell","posSide":"long","fillPx":"50100","fillSz":"1","fillPnl":"0.5","fee":"-0.02","feeCcy":"USDT","fillTime":"%d"},
				{"instType":"SWAP","instId":"ETH-USDT-SWAP","tradeId":"t2","ordId":"o-eth-close","side":"buy","posSide":"short","fillPx":"2510","fillSz":"1","fillPnl":"-1","fee":"-0.05","feeCcy":"USDT","fillTime":"%d"},
				{"instType":"SWAP","instId":"ETH-USDT-SWAP","tradeId":"t2-open","ordId":"o-eth-open","side":"sell","posSide":"short","fillPx":"2500","fillSz":"1","fillPnl":"0","fee":"-0.05","feeCcy":"USDT","fillTime":"%d"},
				{"instType":"SWAP","instId":"BTC-USDT-SWAP","tradeId":"t1-open","ordId":"o-btc-open","side":"buy","posSide":"long","fillPx":"50000","fillSz":"2","fillPnl":"0","fee":"-0.1","feeCcy":"USDT","fillTime":"%d"},
				{"instType":"SWAP","instId":"OLD-USDT-SWAP","tradeId":"t-old","ordId":"o-old","side":"sell","fillPx":"1","fillSz":"1","fillPnl":"999","fee":"0","feeCcy":"USDT","fillTime":"%d"}
			]}`, fillTime2, fillTime2+1000, fillTime2+2000, fillTime1, fillTime1, oldTradeTime)))
		case "/api/v5/account/bills-archive":
			sawOKXFunding = true
			if r.Header.Get("x-simulated-trading") != "1" || r.Header.Get("OK-ACCESS-KEY") != "key" {
				t.Fatalf("missing private OKX funding headers")
			}
			if r.URL.Query().Get("instType") != "SWAP" || r.URL.Query().Get("limit") != "100" || r.URL.Query().Get("begin") == "" || r.URL.Query().Get("end") == "" {
				t.Fatalf("bad bills query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[]}`))
		case "/api/v5/public/instruments":
			sawInstruments = true
			if r.URL.Query().Get("instType") != "SWAP" || r.Header.Get("OK-ACCESS-KEY") != "" {
				t.Fatalf("bad public instruments request: query=%s api_key=%q", r.URL.RawQuery, r.Header.Get("OK-ACCESS-KEY"))
			}
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[
				{"instId":"BTC-USDT-SWAP","ctVal":"0.01"},
				{"instId":"ETH-USDT-SWAP","ctVal":"0.1"}
			]}`))
		default:
			t.Fatalf("unexpected OKX path %s", r.URL.Path)
		}
	}))
	defer okxServer.Close()
	binanceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/fapi/v1/income" {
			sawBinanceFunding = true
			if r.Header.Get("X-MBX-APIKEY") != expectedBinanceAPIKey {
				t.Fatalf("bad Binance funding API key: got %q want %q", r.Header.Get("X-MBX-APIKEY"), expectedBinanceAPIKey)
			}
			query := r.URL.Query()
			if query.Get("incomeType") != "FUNDING_FEE" || query.Get("limit") != "1000" || query.Get("startTime") == "" || query.Get("endTime") == "" {
				t.Fatalf("bad Binance income query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`[]`))
			return
		}
		if r.URL.Path != "/fapi/v1/userTrades" {
			t.Fatalf("unexpected Binance path %s", r.URL.Path)
		}
		if r.Header.Get("X-MBX-APIKEY") != expectedBinanceAPIKey {
			t.Fatalf("bad Binance API key: got %q want %q", r.Header.Get("X-MBX-APIKEY"), expectedBinanceAPIKey)
		}
		query := r.URL.Query()
		symbol := query.Get("symbol")
		if symbol == "" || query.Get("limit") != "1000" {
			t.Fatalf("bad Binance user trades query: %s", r.URL.RawQuery)
		}
		startMS, _ := strconv.ParseInt(query.Get("startTime"), 10, 64)
		endMS, _ := strconv.ParseInt(query.Get("endTime"), 10, 64)
		if startMS <= 0 || endMS <= 0 || endMS-startMS > int64((7*24*time.Hour).Milliseconds()) {
			t.Fatalf("bad Binance analysis time window: %s", r.URL.RawQuery)
		}
		if expectedBinanceStart > 0 && startMS != expectedBinanceStart {
			t.Fatalf("bad Binance analysis start time: got %d want %d query=%s", startMS, expectedBinanceStart, r.URL.RawQuery)
		}
		sawBinanceSymbols[symbol] = true
		if symbol == "BADUSDT" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"code":-1121,"msg":"Invalid symbol."}`))
			return
		}
		if symbol == "BTCUSDT" && startMS <= binanceTradeTime && endMS >= binanceTradeTime {
			_, _ = w.Write([]byte(fmt.Sprintf(`[
				{"symbol":"BTCUSDT","side":"BUY","positionSide":"LONG","price":"64000","qty":"0.03","realizedPnl":"0","commission":"0.1","commissionAsset":"USDT","time":%d,"id":9000,"orderId":7001},
				{"symbol":"BTCUSDT","side":"SELL","positionSide":"BOTH","price":"64000","qty":"0.01","realizedPnl":"4.2","commission":"0.2","commissionAsset":"USDT","time":%d,"id":9001,"orderId":8001},
				{"symbol":"BTCUSDT","side":"SELL","positionSide":"BOTH","price":"64010","qty":"0.02","realizedPnl":"0.8","commission":"0.05","commissionAsset":"USDT","time":%d,"id":9002,"orderId":8001}
			]`, binanceTradeTime-1000, binanceTradeTime, binanceTradeTime+1000)))
			return
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	defer binanceServer.Close()
	cfg := srv.ConfigStore.Get()
	cfg.Trading.BaseURL = okxServer.URL
	cfg.Trading.BinanceDemoBaseURL = binanceServer.URL
	cfg.Symbols["BAD"] = config.SymbolConfig{Coinpair: "BADUSDT"}
	srv.ConfigStore = config.NewStore("", cfg)
	srv.OKXHTTPClient = okxServer.Client()
	srv.BinanceHTTPClient = binanceServer.Client()
	if _, err := srv.OKXCredentials.UpdateAccount(okx.CredentialAccountUpdate{
		ID:     "default",
		Active: true,
		Credentials: okx.Credentials{
			APIKey:     "key",
			SecretKey:  "secret",
			Passphrase: "pass",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.BinanceCredentials.UpdateAccount(binance.CredentialAccountUpdate{
		ID:     "binance-main",
		Active: true,
		Credentials: binance.Credentials{
			APIKey:    "binance-key",
			SecretKey: "binance-secret",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.BinanceCredentials.UpdateAccount(binance.CredentialAccountUpdate{
		ID:   "binance-alt",
		Name: "Binance Alt",
		Credentials: binance.Credentials{
			APIKey:    "binance-alt-key",
			SecretKey: "binance-alt-secret",
		},
	}); err != nil {
		t.Fatal(err)
	}

	unauth := httptest.NewRecorder()
	srv.ServeHTTP(unauth, httptest.NewRequest(http.MethodGet, "/tvbot/analysis", nil))
	if unauth.Code != http.StatusUnauthorized {
		t.Fatalf("analysis without auth code=%d", unauth.Code)
	}
	req := httptest.NewRequest(http.MethodGet, "/tvbot/analysis?refresh=true&pnl_days=60&pnl_minutes=1440&binance_api_id=binance-alt", nil)
	req.SetBasicAuth("admin", "Admin123")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("analysis code=%d body=%s", rr.Code, rr.Body.String())
	}
	if !sawBalance || !sawCandles || !sawFills || !sawOKXFunding || !sawBinanceFunding || !sawInstruments {
		t.Fatalf("expected analysis calls balance=%v candles=%v fills=%v okxFunding=%v binanceFunding=%v instruments=%v", sawBalance, sawCandles, sawFills, sawOKXFunding, sawBinanceFunding, sawInstruments)
	}
	if !sawBinanceSymbols["BADUSDT"] || !sawBinanceSymbols["BTCUSDT"] || !sawBinanceSymbols["ETHUSDT"] {
		t.Fatalf("expected Binance configured symbols to be queried, seen=%#v", sawBinanceSymbols)
	}
	var resp analysisResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.PNLMinutes != 1440 || resp.PNLDays != 1 || resp.BinanceAPIID != "binance-alt" || !strings.Contains(resp.Cache.CacheKey, "binance:binance-alt") {
		t.Fatalf("bad analysis API/window metadata: %#v", resp)
	}
	if resp.Source.Fills != "okx+binance_error" {
		t.Fatalf("expected partial Binance fill status, source=%#v", resp.Source)
	}
	if resp.Balance.TotalEq != "80078.07" || len(resp.Balance.Details) != 2 || resp.Balance.Details[0].Ccy != "BTC" {
		t.Fatalf("bad balance data: %#v", resp.Balance)
	}
	if len(resp.PricePoints) != 2 || resp.PriceInstID != "USDT-USD" || resp.PriceBar != "1H" {
		t.Fatalf("bad price data: %#v", resp)
	}
	if len(resp.BalancePoints) != 1 || math.Abs(resp.BalancePoints[0].Value-5000) > 0.0000001 || resp.BalancePoints[0].CashBal != "5000" {
		t.Fatalf("bad balance points: %#v", resp.BalancePoints)
	}
	snapshots, err := srv.Orders.ListUSDTBalanceSnapshots("default", cfg.Trading.Env, time.Date(2026, 7, 24, 2, 0, 0, 0, time.UTC), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 1 || snapshots[0].EqUsd != "4996.65" || snapshots[0].BucketTS != time.Date(2026, 7, 24, 3, 0, 0, 0, time.UTC).UnixMilli() {
		t.Fatalf("analysis did not write USDT balance snapshot: %#v", snapshots)
	}
	if resp.Summary.TradeCount != 3 || resp.Summary.Wins != 2 || resp.Summary.Losses != 1 {
		t.Fatalf("bad summary counts: %#v", resp.Summary)
	}
	if math.Abs(resp.Summary.NetPnL-6.33) > 0.0000001 || math.Abs(resp.Summary.WinRate-(2.0/3.0)) > 0.0000001 {
		t.Fatalf("bad summary metrics: %#v", resp.Summary)
	}
	if math.Abs(resp.Summary.Fees-(-0.67)) > 0.0000001 || math.Abs(resp.Summary.Turnover-6342.2) > 0.0000001 {
		t.Fatalf("bad summary period fill metrics: %#v", resp.Summary)
	}
	if len(resp.ExchangeSummaries) != 2 {
		t.Fatalf("expected exchange summaries: %#v", resp.ExchangeSummaries)
	}
	byExchange := map[string]analysisSymbolStats{}
	for _, stats := range resp.ExchangeSummaries {
		byExchange[stats.Exchange] = stats
	}
	if byExchange["okx"].TradeCount != 2 || math.Abs(byExchange["okx"].NetPnL-1.68) > 0.0000001 {
		t.Fatalf("bad OKX exchange summary: %#v", resp.ExchangeSummaries)
	}
	if byExchange["binance"].TradeCount != 1 || math.Abs(byExchange["binance"].NetPnL-4.65) > 0.0000001 {
		t.Fatalf("bad Binance exchange summary: %#v", resp.ExchangeSummaries)
	}
	if len(resp.Symbols) != 3 {
		t.Fatalf("expected symbol stats: %#v", resp.Symbols)
	}
	byExchangeSymbol := map[string]analysisSymbolStats{}
	for _, stats := range resp.Symbols {
		byExchangeSymbol[stats.Exchange+"|"+stats.InstID] = stats
	}
	if byExchangeSymbol["okx|BTC-USDT-SWAP"].TradeCount != 1 || byExchangeSymbol["binance|BTCUSDT"].TradeCount != 1 {
		t.Fatalf("expected exchange-separated symbol stats: %#v", resp.Symbols)
	}
	if len(resp.PositionTrades) != 3 {
		t.Fatalf("bad position trades: %#v", resp.PositionTrades)
	}
	if resp.PositionTrades[0].Exchange != trading.ExchangeBinance || resp.PositionTrades[0].InstID != "BTCUSDT" || resp.PositionTrades[0].Side != "long" || resp.PositionTrades[0].NetPnL != "4.65" {
		t.Fatalf("bad Binance position trade: %#v", resp.PositionTrades)
	}
	if resp.PositionTrades[1].Exchange != trading.ExchangeOKX || resp.PositionTrades[1].InstID != "ETH-USDT-SWAP" || resp.PositionTrades[1].Side != "short" || resp.PositionTrades[1].NetPnL != "-1.1" {
		t.Fatalf("bad OKX short position trade: %#v", resp.PositionTrades)
	}
	if resp.PositionTrades[2].Exchange != trading.ExchangeOKX || resp.PositionTrades[2].InstID != "BTC-USDT-SWAP" || resp.PositionTrades[2].Side != "long" || resp.PositionTrades[2].NetPnL != "2.78" {
		t.Fatalf("bad OKX long position trade: %#v", resp.PositionTrades)
	}
	if len(resp.Trades) == 0 || resp.Trades[0].Exchange != trading.ExchangeBinance || resp.Trades[0].InstID != "BTCUSDT" || resp.Trades[0].Fee != "-0.25" || resp.Trades[0].FillCount != 2 || resp.Trades[0].FillSz != "0.03" {
		t.Fatalf("bad trade history: %#v", resp.Trades)
	}

	expectedBinanceStart = 0
	expectedBinanceAPIKey = "binance-key"
	capReq := httptest.NewRequest(http.MethodGet, "/tvbot/analysis?refresh=true&pnl_minutes=999999", nil)
	capReq.SetBasicAuth("admin", "Admin123")
	capRR := httptest.NewRecorder()
	srv.ServeHTTP(capRR, capReq)
	if capRR.Code != http.StatusOK {
		t.Fatalf("analysis cap code=%d body=%s", capRR.Code, capRR.Body.String())
	}
	var capResp analysisResponse
	if err := json.Unmarshal(capRR.Body.Bytes(), &capResp); err != nil {
		t.Fatal(err)
	}
	if capResp.PNLMinutes != maxAnalysisPNLMinutes || capResp.PNLDays != maxAnalysisPNLDays {
		t.Fatalf("bad capped analysis window: %#v", capResp)
	}
}

func TestUSDTBalanceSamplerStoresConfiguredAccounts(t *testing.T) {
	srv := newTestServer(t)
	if usdtSampleInterval != time.Minute {
		t.Fatalf("USDT balance sampler interval = %s, want 1m", usdtSampleInterval)
	}
	observedAt := time.Date(2026, 7, 24, 3, 4, 45, 0, time.UTC)
	srv.Now = func() time.Time { return observedAt }
	seen := map[string]bool{}
	okxServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v5/account/balance" {
			t.Fatalf("unexpected OKX path %s", r.URL.Path)
		}
		if r.Header.Get("x-simulated-trading") != "1" {
			t.Fatalf("missing demo header")
		}
		apiKey := r.Header.Get("OK-ACCESS-KEY")
		seen[apiKey] = true
		eqUSD := map[string]string{"main-key": "100.5", "backup-key": "200.5"}[apiKey]
		if eqUSD == "" {
			t.Fatalf("unexpected api key %q", apiKey)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"totalEq":"` + eqUSD + `","uTime":"1784880000000","details":[{"ccy":"USDT","eq":"` + eqUSD + `","eqUsd":"` + eqUSD + `","availEq":"` + eqUSD + `","uTime":"1784880000000"}]}]}`))
	}))
	defer okxServer.Close()
	cfg := srv.ConfigStore.Get()
	cfg.Trading.BaseURL = okxServer.URL
	srv.ConfigStore = config.NewStore("", cfg)
	srv.OKXHTTPClient = okxServer.Client()
	if _, err := srv.OKXCredentials.UpdateAccount(okx.CredentialAccountUpdate{
		ID:     "main",
		Active: true,
		Credentials: okx.Credentials{
			APIKey:     "main-key",
			SecretKey:  "main-secret",
			Passphrase: "main-pass",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.OKXCredentials.UpdateAccount(okx.CredentialAccountUpdate{
		ID: "backup",
		Credentials: okx.Credentials{
			APIKey:     "backup-key",
			SecretKey:  "backup-secret",
			Passphrase: "backup-pass",
		},
	}); err != nil {
		t.Fatal(err)
	}

	srv.sampleConfiguredUSDTBalances(context.Background())
	if !seen["main-key"] || !seen["backup-key"] {
		t.Fatalf("expected both accounts sampled, seen=%#v", seen)
	}
	for apiID, eqUSD := range map[string]string{"main": "100.5", "backup": "200.5"} {
		snapshots, err := srv.Orders.ListUSDTBalanceSnapshots(apiID, cfg.Trading.Env, time.Date(2026, 7, 24, 2, 0, 0, 0, time.UTC), 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(snapshots) != 1 || snapshots[0].EqUsd != eqUSD || snapshots[0].BucketTS != observedAt.Truncate(time.Minute).UnixMilli() {
			t.Fatalf("bad %s snapshots: %#v", apiID, snapshots)
		}
	}
}

func TestTVBotPositionsRequiresAdminAndReturnsCurrentPositions(t *testing.T) {
	srv := newTestServer(t)
	entryFillTime := srv.now().Add(-2 * time.Hour)
	reduceFillTime := srv.now().Add(-time.Hour)
	var sawPositions bool
	fillsCalls := 0
	okxServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v5/account/positions":
			sawPositions = true
			if r.URL.Query().Get("instType") != "SWAP" {
				t.Fatalf("bad positions query: %s", r.URL.RawQuery)
			}
			if r.Header.Get("x-simulated-trading") != "1" || r.Header.Get("OK-ACCESS-KEY") != "key" {
				t.Fatalf("missing private OKX headers")
			}
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[
				{"instType":"SWAP","instId":"BTC-USDT-SWAP","mgnMode":"isolated","posId":"1","posSide":"long","pos":"0.5","availPos":"0.5","avgPx":"64000","markPx":"65000","upl":"500","uplRatio":"0.015","lever":"5","liqPx":"51000","notionalUsd":"32500","margin":"6500","mgnRatio":"100","uTime":"1784880000000"},
				{"instType":"SWAP","instId":"ETH-USDT-SWAP","mgnMode":"isolated","posId":"2","posSide":"short","pos":"0","availPos":"0","avgPx":"2500","markPx":"2490","upl":"0","uplRatio":"0","lever":"5","notionalUsd":"0","uTime":"1784880000000"}
			]}`))
		case "/api/v5/trade/fills-history":
			fillsCalls++
			if r.URL.Query().Get("instType") != "SWAP" || r.URL.Query().Get("limit") != "100" {
				t.Fatalf("bad fills query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(fmt.Sprintf(`{"code":"0","msg":"","data":[
				{"instType":"SWAP","instId":"BTC-USDT-SWAP","tradeId":"trade-2","ordId":"order-2","side":"sell","posSide":"long","fillSz":"0.2","fillTime":"%d"},
				{"instType":"SWAP","instId":"BTC-USDT-SWAP","tradeId":"trade-1","ordId":"order-1","side":"buy","posSide":"long","fillSz":"0.7","fillTime":"%d"}
			]}`, reduceFillTime.UnixMilli(), entryFillTime.UnixMilli())))
		case "/api/v5/public/instruments":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[
				{"instId":"BTC-USDT-SWAP","tickSz":"0.1","ctVal":"0.01","lotSz":"0.01","minSz":"0.01"},
				{"instId":"ETH-USDT-SWAP","tickSz":"0.01","ctVal":"0.1","lotSz":"0.001","minSz":"0.001"}
			]}`))
		default:
			t.Fatalf("unexpected OKX path %s", r.URL.Path)
		}
	}))
	defer okxServer.Close()
	cfg := srv.ConfigStore.Get()
	cfg.Trading.BaseURL = okxServer.URL
	srv.ConfigStore = config.NewStore("", cfg)
	srv.OKXHTTPClient = okxServer.Client()
	if _, err := srv.OKXCredentials.UpdateAccount(okx.CredentialAccountUpdate{
		ID:     "default",
		Active: true,
		Credentials: okx.Credentials{
			APIKey:     "key",
			SecretKey:  "secret",
			Passphrase: "pass",
		},
	}); err != nil {
		t.Fatal(err)
	}

	unauth := httptest.NewRecorder()
	srv.ServeHTTP(unauth, httptest.NewRequest(http.MethodGet, "/tvbot/positions", nil))
	if unauth.Code != http.StatusUnauthorized {
		t.Fatalf("positions without auth code=%d", unauth.Code)
	}
	req := httptest.NewRequest(http.MethodGet, "/tvbot/positions?api_id=default", nil)
	req.SetBasicAuth("admin", "Admin123")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("positions code=%d body=%s", rr.Code, rr.Body.String())
	}
	if !sawPositions {
		t.Fatal("expected OKX positions call")
	}
	if fillsCalls != 1 {
		t.Fatal("expected OKX fills-history call")
	}
	var resp positionsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK || resp.APIID != "default" || resp.Count != 1 || len(resp.Positions) != 1 {
		t.Fatalf("bad positions response: %#v", resp)
	}
	if resp.Positions[0].InstID != "BTC-USDT-SWAP" || resp.Positions[0].Upl != "500" {
		t.Fatalf("bad position data: %#v", resp.Positions[0])
	}
	if resp.Positions[0].EntryFillTime != entryFillTime.UTC().Format(time.RFC3339Nano) || resp.Positions[0].HoldingSeconds != int64((2*time.Hour).Seconds()) || resp.Positions[0].EntryTimeSource != entryTimeSourceOKXFills {
		t.Fatalf("bad position entry time: %#v", resp.Positions[0])
	}
	if resp.Positions[0].PricePrecision == nil || *resp.Positions[0].PricePrecision != 1 || resp.Positions[0].QuantityPrecision == nil || *resp.Positions[0].QuantityPrecision != 2 {
		t.Fatalf("bad OKX position precision: %#v", resp.Positions[0])
	}

	cachedReq := httptest.NewRequest(http.MethodGet, "/tvbot/positions?api_id=default", nil)
	cachedReq.SetBasicAuth("admin", "Admin123")
	cachedRR := httptest.NewRecorder()
	srv.ServeHTTP(cachedRR, cachedReq)
	if cachedRR.Code != http.StatusOK {
		t.Fatalf("cached positions code=%d body=%s", cachedRR.Code, cachedRR.Body.String())
	}
	if fillsCalls != 1 {
		t.Fatalf("expected cached positions request to reuse entry fills, calls=%d", fillsCalls)
	}

	refreshReq := httptest.NewRequest(http.MethodGet, "/tvbot/positions?api_id=default&refresh=true", nil)
	refreshReq.SetBasicAuth("admin", "Admin123")
	refreshRR := httptest.NewRecorder()
	srv.ServeHTTP(refreshRR, refreshReq)
	if refreshRR.Code != http.StatusOK {
		t.Fatalf("refresh positions code=%d body=%s", refreshRR.Code, refreshRR.Body.String())
	}
	if fillsCalls != 2 {
		t.Fatalf("expected refresh=true to reload entry fills, calls=%d", fillsCalls)
	}
}

func TestTVBotBinancePositionsPendingOrdersAndBalanceOverview(t *testing.T) {
	srv := newTestServer(t)
	seen := map[string]bool{}
	userTradesCalls := 0
	entryTradeTime := srv.now().Add(-2 * time.Hour)
	reduceTradeTime := srv.now().Add(-time.Hour)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/fapi/v1/exchangeInfo" && r.URL.Path != "/fapi/v1/ticker/bookTicker" && r.Header.Get("X-MBX-APIKEY") != "binance-key" {
			t.Fatalf("missing Binance API key for %s", r.URL.Path)
		}
		seen[r.URL.Path] = true
		switch r.URL.Path {
		case "/fapi/v3/positionRisk":
			_, _ = w.Write([]byte(`[
				{"symbol":"BTCUSDT","positionSide":"BOTH","positionAmt":"0.2","entryPrice":"50000","markPrice":"50100","unRealizedProfit":"20","liquidationPrice":"40000","isolatedMargin":"1000","notional":"10020","marginAsset":"USDT","leverage":"10","marginType":"isolated","updateTime":1784880000000},
				{"symbol":"ETHUSDT","positionSide":"BOTH","positionAmt":"0","entryPrice":"3000","markPrice":"3000","unRealizedProfit":"0","notional":"0","marginAsset":"USDT","updateTime":1784880000000}
			]`))
		case "/fapi/v1/openOrders":
			_, _ = w.Write([]byte(`[{"symbol":"BTCUSDT","orderId":123456,"clientOrderId":"client-1","price":"49900","origQty":"0.2","executedQty":"0.1","side":"BUY","positionSide":"BOTH","type":"LIMIT","status":"NEW","time":1784880000000,"updateTime":1784880005000}]`))
		case "/fapi/v1/openAlgoOrders":
			if r.URL.Query().Get("algoType") != "CONDITIONAL" {
				t.Fatalf("bad Binance open algo query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`[
				{"symbol":"BTCUSDT","algoId":900,"clientAlgoId":"tp-900","algoType":"CONDITIONAL","orderType":"TAKE_PROFIT_MARKET","side":"SELL","positionSide":"BOTH","quantity":"0.1","algoStatus":"NEW","reduceOnly":true,"triggerPrice":"51000","workingType":"MARK_PRICE"},
				{"symbol":"ETHUSDT","algoId":901,"clientAlgoId":"sl-901","algoType":"CONDITIONAL","orderType":"STOP_MARKET","side":"BUY","positionSide":"BOTH","quantity":"0.2","algoStatus":"NEW","reduceOnly":true,"triggerPrice":"2900"}
			]`))
		case "/fapi/v1/userTrades":
			userTradesCalls++
			if r.URL.Query().Get("symbol") != "BTCUSDT" || r.URL.Query().Get("limit") != "1000" {
				t.Fatalf("bad user trades query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(fmt.Sprintf(`[
				{"symbol":"BTCUSDT","side":"SELL","positionSide":"BOTH","qty":"0.1","time":%d,"id":2,"orderId":2},
				{"symbol":"BTCUSDT","side":"BUY","positionSide":"BOTH","qty":"0.3","time":%d,"id":1,"orderId":1}
			]`, reduceTradeTime.UnixMilli(), entryTradeTime.UnixMilli())))
		case "/fapi/v1/exchangeInfo":
			_, _ = w.Write([]byte(`{"symbols":[{"symbol":"BTCUSDT","status":"TRADING","pricePrecision":2,"quantityPrecision":3,"filters":[{"filterType":"PRICE_FILTER","tickSize":"0.10"},{"filterType":"LOT_SIZE","minQty":"0.001","stepSize":"0.001"}]}]}`))
		case "/fapi/v1/ticker/bookTicker":
			if r.URL.Query().Get("symbol") != "BTCUSDT" {
				t.Fatalf("bad book ticker query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","bidPrice":"49999.9","bidQty":"1","askPrice":"50000.1","askQty":"2","time":1784880000000}`))
		case "/fapi/v3/balance":
			_, _ = w.Write([]byte(`[{"asset":"USDT","balance":"2000.5","availableBalance":"1500.25","crossWalletBalance":"1800.25","updateTime":1784886000000}]`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer ts.Close()
	cfg := srv.ConfigStore.Get()
	cfg.Trading.BinanceDemoBaseURL = ts.URL
	srv.ConfigStore = config.NewStore("", cfg)
	srv.BinanceHTTPClient = ts.Client()
	if _, err := srv.BinanceCredentials.UpdateAccount(binance.CredentialAccountUpdate{
		ID:     "main",
		Active: true,
		Credentials: binance.Credentials{
			APIKey:    "binance-key",
			SecretKey: "binance-secret",
		},
	}); err != nil {
		t.Fatal(err)
	}

	positionsReq := httptest.NewRequest(http.MethodGet, "/tvbot/positions?exchange=binance", nil)
	positionsReq.SetBasicAuth("admin", "Admin123")
	positionsRR := httptest.NewRecorder()
	srv.ServeHTTP(positionsRR, positionsReq)
	if positionsRR.Code != http.StatusOK {
		t.Fatalf("positions status=%d body=%s", positionsRR.Code, positionsRR.Body.String())
	}
	var positions positionsResponse
	if err := json.Unmarshal(positionsRR.Body.Bytes(), &positions); err != nil {
		t.Fatal(err)
	}
	if positions.Exchange != trading.ExchangeBinance || positions.APIID != "main" || len(positions.Positions) != 1 || positions.Positions[0].InstID != "BTCUSDT" {
		t.Fatalf("bad Binance positions response: %#v", positions)
	}
	if positions.Positions[0].EntryFillTime != entryTradeTime.UTC().Format(time.RFC3339Nano) || positions.Positions[0].HoldingSeconds != int64((2*time.Hour).Seconds()) || positions.Positions[0].EntryTimeSource != entryTimeSourceBinanceTrade {
		t.Fatalf("bad Binance position entry time: %#v", positions.Positions[0])
	}
	if positions.Positions[0].PricePrecision == nil || *positions.Positions[0].PricePrecision != 2 || positions.Positions[0].QuantityPrecision == nil || *positions.Positions[0].QuantityPrecision != 3 {
		t.Fatalf("bad Binance position precision: %#v", positions.Positions[0])
	}
	if userTradesCalls != 1 {
		t.Fatalf("expected Binance user trades call, calls=%d", userTradesCalls)
	}

	cachedPositionsReq := httptest.NewRequest(http.MethodGet, "/tvbot/positions?exchange=binance", nil)
	cachedPositionsReq.SetBasicAuth("admin", "Admin123")
	cachedPositionsRR := httptest.NewRecorder()
	srv.ServeHTTP(cachedPositionsRR, cachedPositionsReq)
	if cachedPositionsRR.Code != http.StatusOK {
		t.Fatalf("cached Binance positions status=%d body=%s", cachedPositionsRR.Code, cachedPositionsRR.Body.String())
	}
	if userTradesCalls != 1 {
		t.Fatalf("expected cached Binance positions request to reuse user trades, calls=%d", userTradesCalls)
	}

	refreshPositionsReq := httptest.NewRequest(http.MethodGet, "/tvbot/positions?exchange=binance&refresh=true", nil)
	refreshPositionsReq.SetBasicAuth("admin", "Admin123")
	refreshPositionsRR := httptest.NewRecorder()
	srv.ServeHTTP(refreshPositionsRR, refreshPositionsReq)
	if refreshPositionsRR.Code != http.StatusOK {
		t.Fatalf("refresh Binance positions status=%d body=%s", refreshPositionsRR.Code, refreshPositionsRR.Body.String())
	}
	if userTradesCalls != 2 {
		t.Fatalf("expected refresh=true to reload Binance user trades, calls=%d", userTradesCalls)
	}

	pendingReq := httptest.NewRequest(http.MethodGet, "/tvbot/pending-orders?exchange=binance", nil)
	pendingReq.SetBasicAuth("admin", "Admin123")
	pendingRR := httptest.NewRecorder()
	srv.ServeHTTP(pendingRR, pendingReq)
	if pendingRR.Code != http.StatusOK {
		t.Fatalf("pending status=%d body=%s", pendingRR.Code, pendingRR.Body.String())
	}
	var pending pendingOrdersResponse
	if err := json.Unmarshal(pendingRR.Body.Bytes(), &pending); err != nil {
		t.Fatal(err)
	}
	if pending.Exchange != trading.ExchangeBinance || pending.APIID != "main" || pending.Count != 1 || pending.NormalCount != 1 || pending.AlgoCount != 2 || pending.TotalCount != 3 || len(pending.Orders) != 1 || pending.Orders[0].OrdID != "123456" || pending.Orders[0].MidPx != "50000" || pending.Orders[0].ChasePx != "49999.9" || pending.Orders[0].Margin != "998" || pending.Orders[0].PriceError != "" {
		t.Fatalf("bad Binance pending response: %#v", pending)
	}
	if pending.Orders[0].PricePrecision == nil || *pending.Orders[0].PricePrecision != 2 || pending.Orders[0].QuantityPrecision == nil || *pending.Orders[0].QuantityPrecision != 3 {
		t.Fatalf("bad Binance pending precision: %#v", pending.Orders[0])
	}
	if len(pending.AlgoOrders) != 2 || pending.AlgoOrders[0].OrderGroup != "algo" || pending.AlgoOrders[0].AlgoID != "900" || pending.AlgoOrders[0].AlgoClOrdID != "tp-900" || pending.AlgoOrders[0].TriggerPx != "51000" || !pending.AlgoOrders[0].Chaseable || pending.AlgoOrders[0].ChasePx != "49999.9" {
		t.Fatalf("bad Binance pending algo orders: %#v", pending.AlgoOrders)
	}
	if pending.AlgoOrders[0].PricePrecision == nil || *pending.AlgoOrders[0].PricePrecision != 2 || pending.AlgoOrders[0].QuantityPrecision == nil || *pending.AlgoOrders[0].QuantityPrecision != 3 {
		t.Fatalf("bad Binance pending algo precision: %#v", pending.AlgoOrders[0])
	}

	overviewReq := httptest.NewRequest(http.MethodGet, "/tvbot/balances/overview?days=3", nil)
	overviewReq.SetBasicAuth("admin", "Admin123")
	overviewRR := httptest.NewRecorder()
	srv.ServeHTTP(overviewRR, overviewReq)
	if overviewRR.Code != http.StatusOK {
		t.Fatalf("overview status=%d body=%s", overviewRR.Code, overviewRR.Body.String())
	}
	var overview balanceOverviewResponse
	if err := json.Unmarshal(overviewRR.Body.Bytes(), &overview); err != nil {
		t.Fatal(err)
	}
	var binanceOverview exchangeBalanceOverview
	for _, item := range overview.Exchanges {
		if item.Exchange == trading.ExchangeBinance {
			binanceOverview = item
		}
	}
	if binanceOverview.Status != "ok" || binanceOverview.APIID != "main" || len(binanceOverview.BalancePoints) != 1 || binanceOverview.Balance.Details[0].Eq != "2020.5" || math.Abs(binanceOverview.BalancePoints[0].Value-2020.5) > 0.0000001 {
		t.Fatalf("bad Binance overview: %#v", overview)
	}
	for _, path := range []string{"/fapi/v3/positionRisk", "/fapi/v1/userTrades", "/fapi/v1/openOrders", "/fapi/v1/openAlgoOrders", "/fapi/v1/exchangeInfo", "/fapi/v1/ticker/bookTicker", "/fapi/v3/balance"} {
		if !seen[path] {
			t.Fatalf("expected %s to be called, seen=%#v", path, seen)
		}
	}
}

func TestTVBotPendingOrdersRequiresAdminAndReturnsCurrentOrders(t *testing.T) {
	srv := newTestServer(t)
	var sawPendingOrders, sawAlgoOrders bool
	okxServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v5/trade/orders-pending":
			sawPendingOrders = true
			if r.URL.Query().Get("instType") != "SWAP" {
				t.Fatalf("bad pending orders query: %s", r.URL.RawQuery)
			}
			if r.Header.Get("x-simulated-trading") != "1" || r.Header.Get("OK-ACCESS-KEY") != "key" {
				t.Fatalf("missing private OKX headers")
			}
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[
				{"instType":"SWAP","instId":"ETH-USDT-SWAP","ordId":"200","clOrdId":"client-200","tdMode":"cross","side":"sell","posSide":"short","ordType":"limit","px":"2500","sz":"1","accFillSz":"0","state":"live","lever":"5","cTime":"1784880060000","uTime":"1784880060000"},
				{"instType":"SWAP","instId":"BTC-USDT-SWAP","ordId":"100","clOrdId":"client-100","tdMode":"isolated","side":"buy","posSide":"long","ordType":"limit","px":"64000","sz":"0.5","accFillSz":"0.1","avgPx":"63950","state":"partially_filled","lever":"5","cTime":"1784880000000","uTime":"1784880060000"}
			]}`))
		case "/api/v5/trade/orders-algo-pending":
			sawAlgoOrders = true
			ordType := r.URL.Query().Get("ordType")
			if r.URL.Query().Get("instType") != "SWAP" || ordType == "" {
				t.Fatalf("bad pending algo query: %s", r.URL.RawQuery)
			}
			if ordType == "twap" {
				_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[
					{"instType":"SWAP","instId":"ETH-USDT-SWAP","algoId":"901","algoClOrdId":"algo-901","side":"buy","posSide":"short","ordType":"twap","sz":"1","state":"live","triggerPx":"2400","cTime":"1784880010000","uTime":"1784880010000"}
				]}`))
				return
			}
			if ordType != "conditional" {
				_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[]}`))
				return
			}
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[
				{"instType":"SWAP","instId":"BTC-USDT-SWAP","algoId":"900","algoClOrdId":"algo-900","side":"sell","posSide":"long","ordType":"conditional","sz":"1","state":"live","triggerPx":"65000","triggerPxType":"last","orderPx":"-1","cTime":"1784880020000","uTime":"1784880020000"}
			]}`))
		case "/api/v5/public/instruments":
			instID := r.URL.Query().Get("instId")
			tick := "0.1"
			if instID == "ETH-USDT-SWAP" {
				tick = "0.01"
			}
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instId":"` + instID + `","tickSz":"` + tick + `","ctVal":"1","lotSz":"1","minSz":"1"}]}`))
		case "/api/v5/market/ticker":
			switch r.URL.Query().Get("instId") {
			case "BTC-USDT-SWAP":
				_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instId":"BTC-USDT-SWAP","bidPx":"63999","askPx":"64001","last":"64000"}]}`))
			case "ETH-USDT-SWAP":
				_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instId":"ETH-USDT-SWAP","bidPx":"2500","askPx":"2500.1","last":"2500.05"}]}`))
			default:
				t.Fatalf("unexpected ticker instId %s", r.URL.Query().Get("instId"))
			}
		default:
			t.Fatalf("unexpected OKX path %s", r.URL.Path)
		}
	}))
	defer okxServer.Close()
	cfg := srv.ConfigStore.Get()
	cfg.Trading.BaseURL = okxServer.URL
	srv.ConfigStore = config.NewStore("", cfg)
	srv.OKXHTTPClient = okxServer.Client()
	if _, err := srv.OKXCredentials.UpdateAccount(okx.CredentialAccountUpdate{
		ID:     "default",
		Active: true,
		Credentials: okx.Credentials{
			APIKey:     "key",
			SecretKey:  "secret",
			Passphrase: "pass",
		},
	}); err != nil {
		t.Fatal(err)
	}

	unauth := httptest.NewRecorder()
	srv.ServeHTTP(unauth, httptest.NewRequest(http.MethodGet, "/tvbot/pending-orders", nil))
	if unauth.Code != http.StatusUnauthorized {
		t.Fatalf("pending orders without auth code=%d", unauth.Code)
	}
	req := httptest.NewRequest(http.MethodGet, "/tvbot/pending-orders?api_id=default", nil)
	req.SetBasicAuth("admin", "Admin123")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("pending orders code=%d body=%s", rr.Code, rr.Body.String())
	}
	if !sawPendingOrders {
		t.Fatal("expected OKX pending orders call")
	}
	if !sawAlgoOrders {
		t.Fatal("expected OKX pending algo orders call")
	}
	var resp pendingOrdersResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK || resp.APIID != "default" || resp.Count != 2 || resp.NormalCount != 2 || resp.AlgoCount != 2 || resp.TotalCount != 4 || len(resp.Orders) != 2 || len(resp.AlgoOrders) != 2 {
		t.Fatalf("bad pending orders response: %#v", resp)
	}
	if resp.Orders[0].InstID != "BTC-USDT-SWAP" || resp.Orders[0].OrdID != "100" || resp.Orders[0].AccFillSz != "0.1" || resp.Orders[0].MidPx != "64000" || resp.Orders[0].ChasePx != "63999.9" || resp.Orders[0].Margin != "5120" {
		t.Fatalf("bad pending order sorting/data: %#v", resp.Orders)
	}
	if resp.Orders[0].PricePrecision == nil || *resp.Orders[0].PricePrecision != 1 || resp.Orders[0].QuantityPrecision == nil || *resp.Orders[0].QuantityPrecision != 0 {
		t.Fatalf("bad OKX pending precision: %#v", resp.Orders[0])
	}
	if resp.Orders[0].OrderGroup != "normal" || !resp.Orders[0].Chaseable {
		t.Fatalf("bad OKX normal order group: %#v", resp.Orders[0])
	}
	if resp.AlgoOrders[0].OrderGroup != "algo" || resp.AlgoOrders[0].AlgoID != "900" || resp.AlgoOrders[0].AlgoClOrdID != "algo-900" || resp.AlgoOrders[0].TriggerPx != "65000" || !resp.AlgoOrders[0].Chaseable || resp.AlgoOrders[0].ChasePx != "63999" {
		t.Fatalf("bad OKX chaseable algo order: %#v", resp.AlgoOrders)
	}
	if resp.AlgoOrders[1].OrderGroup != "algo" || resp.AlgoOrders[1].AlgoID != "901" || resp.AlgoOrders[1].Chaseable || resp.AlgoOrders[1].ChaseUnavailableReason == "" {
		t.Fatalf("bad OKX unsupported algo order: %#v", resp.AlgoOrders)
	}
}

func TestTVBotPendingOrderChaseAmendsPassiveBuyAndStopsWhenOrderDisappears(t *testing.T) {
	oldInterval := pendingOrderChaseInterval
	oldJobs := pendingOrderChaseJobs
	pendingOrderChaseInterval = 10 * time.Millisecond
	pendingOrderChaseJobs = newPendingOrderChaseRegistry()
	defer func() {
		pendingOrderChaseInterval = oldInterval
		pendingOrderChaseJobs = oldJobs
	}()

	srv := newTestServer(t)
	var mu sync.Mutex
	pendingCalls := 0
	var amendBodies []map[string]string
	okxServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v5/trade/orders-pending":
			mu.Lock()
			pendingCalls++
			call := pendingCalls
			mu.Unlock()
			if r.Header.Get("OK-ACCESS-KEY") != "key" {
				t.Fatalf("missing private OKX headers")
			}
			if call == 1 {
				_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instType":"SWAP","instId":"BTC-USDT-SWAP","ordId":"100","clOrdId":"client-100","tdMode":"isolated","side":"buy","posSide":"long","ordType":"limit","px":"64000","sz":"0.5","accFillSz":"0","state":"live","cTime":"1784880000000"}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[]}`))
		case "/api/v5/public/instruments":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instId":"BTC-USDT-SWAP","tickSz":"0.1","ctVal":"1","lotSz":"1","minSz":"1"}]}`))
		case "/api/v5/market/ticker":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instId":"BTC-USDT-SWAP","bidPx":"63999","askPx":"64001","last":"64000"}]}`))
		case "/api/v5/trade/amend-order":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			amendBodies = append(amendBodies, body)
			mu.Unlock()
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"ordId":"100","clOrdId":"client-100","sCode":"0","sMsg":""}]}`))
		case "/api/v5/trade/cancel-order":
			t.Fatal("追单不应调用撤单接口")
		default:
			t.Fatalf("unexpected OKX path %s", r.URL.Path)
		}
	}))
	defer okxServer.Close()
	cfg := srv.ConfigStore.Get()
	cfg.Trading.BaseURL = okxServer.URL
	cfg.Trading.RiskType = string(trading.RiskNone)
	srv.ConfigStore = config.NewStore("", cfg)
	srv.OKXHTTPClient = okxServer.Client()
	if _, err := srv.OKXCredentials.UpdateAccount(okx.CredentialAccountUpdate{
		ID:          "default",
		Active:      true,
		Credentials: okx.Credentials{APIKey: "key", SecretKey: "secret", Passphrase: "pass"},
	}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/tvbot/pending-orders/chase", strings.NewReader(`{"api_id":"default","inst_id":"BTC-USDT-SWAP","ord_id":"100","cl_ord_id":"client-100"}`))
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("admin", "Admin123")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("chase code=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp pendingOrderChaseResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Px != "63999.9" || resp.MidPx != "64000" || resp.Status != "running" {
		t.Fatalf("bad chase response: %#v", resp)
	}
	key := pendingOrderChaseKey(pendingOrderChaseRequest{APIID: "default", InstID: "BTC-USDT-SWAP", OrdID: "100", ClOrdID: "client-100"})
	for i := 0; i < 100 && pendingOrderChaseJobs.activeKey(key); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if pendingOrderChaseJobs.activeKey(key) {
		t.Fatal("chase job should stop when pending order disappears")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(amendBodies) != 1 || amendBodies[0]["instId"] != "BTC-USDT-SWAP" || amendBodies[0]["ordId"] != "100" || amendBodies[0]["newPx"] != "63999.9" {
		t.Fatalf("bad amend bodies: %#v", amendBodies)
	}
}

func TestTVBotPendingOrderChaseSkipsEquivalentPriceAmend(t *testing.T) {
	oldInterval := pendingOrderChaseInterval
	oldTimeout := pendingOrderChaseTimeout
	oldJobs := pendingOrderChaseJobs
	pendingOrderChaseInterval = time.Hour
	pendingOrderChaseTimeout = time.Hour
	pendingOrderChaseJobs = newPendingOrderChaseRegistry()
	defer func() {
		pendingOrderChaseInterval = oldInterval
		pendingOrderChaseTimeout = oldTimeout
		pendingOrderChaseJobs = oldJobs
	}()

	srv := newTestServer(t)
	var amendCalled bool
	okxServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v5/trade/orders-pending":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instType":"SWAP","instId":"BTC-USDT-SWAP","ordId":"100","clOrdId":"client-100","tdMode":"isolated","side":"buy","posSide":"long","ordType":"limit","px":"63999.90","sz":"0.5","accFillSz":"0","state":"live","cTime":"1784880000000"}]}`))
		case "/api/v5/public/instruments":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instId":"BTC-USDT-SWAP","tickSz":"0.1","ctVal":"1","lotSz":"1","minSz":"1"}]}`))
		case "/api/v5/market/ticker":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instId":"BTC-USDT-SWAP","bidPx":"63999","askPx":"64001","last":"64000"}]}`))
		case "/api/v5/trade/amend-order":
			amendCalled = true
			t.Fatal("equivalent chase price should not call OKX amend-order")
		default:
			t.Fatalf("unexpected OKX path %s", r.URL.Path)
		}
	}))
	defer okxServer.Close()
	cfg := srv.ConfigStore.Get()
	cfg.Trading.BaseURL = okxServer.URL
	cfg.Trading.RiskType = string(trading.RiskNone)
	srv.ConfigStore = config.NewStore("", cfg)
	srv.OKXHTTPClient = okxServer.Client()
	if _, err := srv.OKXCredentials.UpdateAccount(okx.CredentialAccountUpdate{
		ID:          "default",
		Active:      true,
		Credentials: okx.Credentials{APIKey: "key", SecretKey: "secret", Passphrase: "pass"},
	}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/tvbot/pending-orders/chase", strings.NewReader(`{"api_id":"default","inst_id":"BTC-USDT-SWAP","ord_id":"100","cl_ord_id":"client-100"}`))
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("admin", "Admin123")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("chase code=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp pendingOrderChaseResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Px != "63999.9" || resp.MidPx != "64000" || resp.Status != "running" {
		t.Fatalf("bad chase response: %#v", resp)
	}
	key := pendingOrderChaseKey(pendingOrderChaseRequest{APIID: "default", InstID: "BTC-USDT-SWAP", OrdID: "100", ClOrdID: "client-100"})
	defer pendingOrderChaseJobs.stop(key)
	if amendCalled {
		t.Fatal("equivalent price should not amend")
	}
}

func TestTVBotPendingOrderChaseRejectsNonLimitOKXOrder(t *testing.T) {
	srv := newTestServer(t)
	var amendCalled bool
	okxServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v5/trade/orders-pending":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instType":"SWAP","instId":"BTC-USDT-SWAP","ordId":"100","clOrdId":"client-100","tdMode":"isolated","side":"buy","posSide":"long","ordType":"market","px":"0","sz":"0.5","accFillSz":"0","state":"live","cTime":"1784880000000"}]}`))
		case "/api/v5/trade/amend-order":
			amendCalled = true
			t.Fatal("non-limit order should not call OKX amend-order")
		default:
			t.Fatalf("unexpected OKX path %s", r.URL.Path)
		}
	}))
	defer okxServer.Close()
	cfg := srv.ConfigStore.Get()
	cfg.Trading.BaseURL = okxServer.URL
	cfg.Trading.RiskType = string(trading.RiskNone)
	srv.ConfigStore = config.NewStore("", cfg)
	srv.OKXHTTPClient = okxServer.Client()
	if _, err := srv.OKXCredentials.UpdateAccount(okx.CredentialAccountUpdate{
		ID:          "default",
		Active:      true,
		Credentials: okx.Credentials{APIKey: "key", SecretKey: "secret", Passphrase: "pass"},
	}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/tvbot/pending-orders/chase", strings.NewReader(`{"api_id":"default","inst_id":"BTC-USDT-SWAP","ord_id":"100","cl_ord_id":"client-100"}`))
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("admin", "Admin123")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadGateway || !strings.Contains(rr.Body.String(), "普通追单只支持限价单") {
		t.Fatalf("chase code=%d body=%s", rr.Code, rr.Body.String())
	}
	if amendCalled {
		t.Fatal("non-limit order should not amend")
	}
}

func TestTVBotBinancePendingOrderChaseAmendsPassiveBuyAndStops(t *testing.T) {
	oldInterval := pendingOrderChaseInterval
	oldTimeout := pendingOrderChaseTimeout
	oldJobs := pendingOrderChaseJobs
	pendingOrderChaseInterval = time.Hour
	pendingOrderChaseTimeout = time.Hour
	pendingOrderChaseJobs = newPendingOrderChaseRegistry()
	defer func() {
		pendingOrderChaseInterval = oldInterval
		pendingOrderChaseTimeout = oldTimeout
		pendingOrderChaseJobs = oldJobs
	}()

	srv := newTestServer(t)
	var modifyForms []url.Values
	var cancelCalled bool
	var marketCalled bool
	binanceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/fapi/v1/exchangeInfo" && r.URL.Path != "/fapi/v1/ticker/bookTicker" && r.Header.Get("X-MBX-APIKEY") != "binance-key" {
			t.Fatalf("missing Binance API key for %s", r.URL.Path)
		}
		switch r.URL.Path {
		case "/fapi/v1/openOrders":
			if r.URL.Query().Get("symbol") != "BTCUSDT" {
				t.Fatalf("bad open orders query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`[{"symbol":"BTCUSDT","orderId":123456,"clientOrderId":"client-1","price":"50000","origQty":"0.2","executedQty":"0","side":"BUY","positionSide":"BOTH","type":"LIMIT","status":"NEW","time":1784880000000,"updateTime":1784880005000}]`))
		case "/fapi/v1/exchangeInfo":
			_, _ = w.Write([]byte(`{"symbols":[{"symbol":"BTCUSDT","status":"TRADING","pricePrecision":2,"quantityPrecision":3,"filters":[{"filterType":"PRICE_FILTER","tickSize":"0.10"},{"filterType":"LOT_SIZE","minQty":"0.001","stepSize":"0.001"}]}]}`))
		case "/fapi/v1/ticker/bookTicker":
			_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","bidPrice":"49999.9","bidQty":"1","askPrice":"50000.1","askQty":"2","time":1784880000000}`))
		case "/fapi/v1/order":
			switch r.Method {
			case http.MethodPut:
				if err := r.ParseForm(); err != nil {
					t.Fatal(err)
				}
				modifyForms = append(modifyForms, cloneValues(r.Form))
				_, _ = w.Write([]byte(`{"orderId":123456,"symbol":"BTCUSDT","status":"NEW","clientOrderId":"client-1","price":"49999.9","origQty":"0.2","executedQty":"0","type":"LIMIT","side":"BUY","positionSide":"BOTH"}`))
			case http.MethodDelete:
				cancelCalled = true
				_, _ = w.Write([]byte(`{"orderId":123456,"symbol":"BTCUSDT","status":"CANCELED","clientOrderId":"client-1","price":"49999.9","origQty":"0.2","executedQty":"0","type":"LIMIT","side":"BUY","positionSide":"BOTH"}`))
			case http.MethodPost:
				marketCalled = true
				_, _ = w.Write([]byte(`{"orderId":999,"symbol":"BTCUSDT","status":"NEW","clientOrderId":"market-1","price":"0","origQty":"0.2","executedQty":"0","type":"MARKET","side":"BUY","positionSide":"BOTH"}`))
			default:
				t.Fatalf("unexpected order method %s", r.Method)
			}
		default:
			t.Fatalf("unexpected Binance path %s", r.URL.Path)
		}
	}))
	defer binanceServer.Close()
	cfg := srv.ConfigStore.Get()
	cfg.Trading.BinanceDemoBaseURL = binanceServer.URL
	cfg.Trading.RiskType = string(trading.RiskNone)
	srv.ConfigStore = config.NewStore("", cfg)
	srv.BinanceHTTPClient = binanceServer.Client()
	if _, err := srv.BinanceCredentials.UpdateAccount(binance.CredentialAccountUpdate{
		ID:          "main",
		Active:      true,
		Credentials: binance.Credentials{APIKey: "binance-key", SecretKey: "binance-secret"},
	}); err != nil {
		t.Fatal(err)
	}

	body := `{"exchange":"binance","api_id":"main","inst_id":"BTCUSDT","ord_id":"123456","cl_ord_id":"client-1"}`
	start := httptest.NewRequest(http.MethodPost, "/tvbot/pending-orders/chase", strings.NewReader(body))
	start.Header.Set("Content-Type", "application/json")
	start.SetBasicAuth("admin", "Admin123")
	startRR := httptest.NewRecorder()
	srv.ServeHTTP(startRR, start)
	if startRR.Code != http.StatusAccepted {
		t.Fatalf("start chase code=%d body=%s", startRR.Code, startRR.Body.String())
	}
	var resp pendingOrderChaseResponse
	if err := json.Unmarshal(startRR.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Px != "49999.9" || resp.MidPx != "50000" || resp.Status != "running" {
		t.Fatalf("bad Binance chase response: %#v", resp)
	}
	key := pendingOrderChaseKey(pendingOrderChaseRequest{Exchange: trading.ExchangeBinance, APIID: "main", InstID: "BTCUSDT", OrdID: "123456", ClOrdID: "client-1"})
	if !pendingOrderChaseJobs.activeKey(key) {
		t.Fatal("Binance chase job should be active")
	}
	stop := httptest.NewRequest(http.MethodPost, "/tvbot/pending-orders/chase/stop", strings.NewReader(body))
	stop.Header.Set("Content-Type", "application/json")
	stop.SetBasicAuth("admin", "Admin123")
	stopRR := httptest.NewRecorder()
	srv.ServeHTTP(stopRR, stop)
	if stopRR.Code != http.StatusOK {
		t.Fatalf("stop chase code=%d body=%s", stopRR.Code, stopRR.Body.String())
	}
	if len(modifyForms) != 1 {
		t.Fatalf("expected one modify request, got %#v", modifyForms)
	}
	modify := modifyForms[0]
	if modify.Get("symbol") != "BTCUSDT" || modify.Get("side") != "BUY" || modify.Get("quantity") != "0.2" || modify.Get("price") != "49999.9" || modify.Get("orderId") != "123456" {
		t.Fatalf("bad modify form: %#v", modify)
	}
	if cancelCalled || marketCalled {
		t.Fatalf("stop should not cancel or market fallback cancel=%v market=%v", cancelCalled, marketCalled)
	}
}

func TestTVBotBinancePendingOrderChaseFallsBackToMarketAfterTimeout(t *testing.T) {
	oldInterval := pendingOrderChaseInterval
	oldTimeout := pendingOrderChaseTimeout
	oldJobs := pendingOrderChaseJobs
	pendingOrderChaseInterval = time.Hour
	pendingOrderChaseTimeout = 20 * time.Millisecond
	pendingOrderChaseJobs = newPendingOrderChaseRegistry()
	defer func() {
		pendingOrderChaseInterval = oldInterval
		pendingOrderChaseTimeout = oldTimeout
		pendingOrderChaseJobs = oldJobs
	}()

	srv := newTestServer(t)
	var mu sync.Mutex
	var cancelForms []url.Values
	var marketForms []url.Values
	binanceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/fapi/v1/exchangeInfo" && r.URL.Path != "/fapi/v1/ticker/bookTicker" && r.Header.Get("X-MBX-APIKEY") != "binance-key" {
			t.Fatalf("missing Binance API key for %s", r.URL.Path)
		}
		switch r.URL.Path {
		case "/fapi/v1/openOrders":
			_, _ = w.Write([]byte(`[{"symbol":"BTCUSDT","orderId":123456,"clientOrderId":"client-1","price":"50000","origQty":"0.2","executedQty":"0.1","side":"BUY","positionSide":"BOTH","type":"LIMIT","status":"NEW","time":1784880000000,"updateTime":1784880005000}]`))
		case "/fapi/v1/exchangeInfo":
			_, _ = w.Write([]byte(`{"symbols":[{"symbol":"BTCUSDT","status":"TRADING","pricePrecision":2,"quantityPrecision":3,"filters":[{"filterType":"PRICE_FILTER","tickSize":"0.10"},{"filterType":"LOT_SIZE","minQty":"0.001","stepSize":"0.001"}]}]}`))
		case "/fapi/v1/ticker/bookTicker":
			_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","bidPrice":"49999.9","bidQty":"1","askPrice":"50000.1","askQty":"2","time":1784880000000}`))
		case "/fapi/v1/order":
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			switch r.Method {
			case http.MethodPut:
				_, _ = w.Write([]byte(`{"orderId":123456,"symbol":"BTCUSDT","status":"NEW","clientOrderId":"client-1","price":"49999.9","origQty":"0.2","executedQty":"0.1","type":"LIMIT","side":"BUY","positionSide":"BOTH"}`))
			case http.MethodDelete:
				mu.Lock()
				cancelForms = append(cancelForms, cloneValues(r.Form))
				mu.Unlock()
				_, _ = w.Write([]byte(`{"orderId":123456,"symbol":"BTCUSDT","status":"CANCELED","clientOrderId":"client-1","price":"49999.9","origQty":"0.2","executedQty":"0.1","type":"LIMIT","side":"BUY","positionSide":"BOTH"}`))
			case http.MethodPost:
				mu.Lock()
				marketForms = append(marketForms, cloneValues(r.Form))
				mu.Unlock()
				_, _ = w.Write([]byte(`{"orderId":999,"symbol":"BTCUSDT","status":"NEW","clientOrderId":"market-1","price":"0","origQty":"0.1","executedQty":"0","type":"MARKET","side":"BUY","positionSide":"BOTH"}`))
			default:
				t.Fatalf("unexpected order method %s", r.Method)
			}
		default:
			t.Fatalf("unexpected Binance path %s", r.URL.Path)
		}
	}))
	defer binanceServer.Close()
	cfg := srv.ConfigStore.Get()
	cfg.Trading.BinanceDemoBaseURL = binanceServer.URL
	cfg.Trading.RiskType = string(trading.RiskNone)
	srv.ConfigStore = config.NewStore("", cfg)
	srv.BinanceHTTPClient = binanceServer.Client()
	if _, err := srv.BinanceCredentials.UpdateAccount(binance.CredentialAccountUpdate{
		ID:          "main",
		Active:      true,
		Credentials: binance.Credentials{APIKey: "binance-key", SecretKey: "binance-secret"},
	}); err != nil {
		t.Fatal(err)
	}

	body := `{"exchange":"binance","api_id":"main","inst_id":"BTCUSDT","ord_id":"123456","cl_ord_id":"client-1"}`
	start := httptest.NewRequest(http.MethodPost, "/tvbot/pending-orders/chase", strings.NewReader(body))
	start.Header.Set("Content-Type", "application/json")
	start.SetBasicAuth("admin", "Admin123")
	startRR := httptest.NewRecorder()
	srv.ServeHTTP(startRR, start)
	if startRR.Code != http.StatusAccepted {
		t.Fatalf("start chase code=%d body=%s", startRR.Code, startRR.Body.String())
	}
	for i := 0; i < 100; i++ {
		mu.Lock()
		done := len(marketForms) > 0
		mu.Unlock()
		if done {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	key := pendingOrderChaseKey(pendingOrderChaseRequest{Exchange: trading.ExchangeBinance, APIID: "main", InstID: "BTCUSDT", OrdID: "123456", ClOrdID: "client-1"})
	for i := 0; i < 100 && pendingOrderChaseJobs.activeKey(key); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if pendingOrderChaseJobs.activeKey(key) {
		t.Fatal("Binance chase job should stop after market fallback")
	}
	if len(cancelForms) != 1 || cancelForms[0].Get("symbol") != "BTCUSDT" || cancelForms[0].Get("orderId") != "123456" {
		t.Fatalf("bad cancel forms: %#v", cancelForms)
	}
	if len(marketForms) != 1 {
		t.Fatalf("expected one market order, got %#v", marketForms)
	}
	market := marketForms[0]
	if market.Get("symbol") != "BTCUSDT" || market.Get("side") != "BUY" || market.Get("type") != "MARKET" || market.Get("quantity") != "0.1" || market.Get("positionSide") != "BOTH" || market.Get("newClientOrderId") == "" {
		t.Fatalf("bad market fallback form: %#v", market)
	}
}

func TestTVBotOKXPendingOrderCancelStopsChaseAndCancels(t *testing.T) {
	oldJobs := pendingOrderChaseJobs
	pendingOrderChaseJobs = newPendingOrderChaseRegistry()
	defer func() {
		pendingOrderChaseJobs = oldJobs
	}()

	srv := newTestServer(t)
	var cancelBodies []map[string]string
	okxServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v5/trade/orders-pending":
			if r.Header.Get("OK-ACCESS-KEY") != "key" {
				t.Fatalf("missing private OKX headers")
			}
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instType":"SWAP","instId":"BTC-USDT-SWAP","ordId":"100","clOrdId":"client-100","tdMode":"isolated","side":"buy","posSide":"long","ordType":"limit","px":"64000","sz":"0.5","accFillSz":"0","state":"live","cTime":"1784880000000"}]}`))
		case "/api/v5/trade/cancel-order":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			cancelBodies = append(cancelBodies, body)
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"ordId":"100","clOrdId":"client-100","sCode":"0","sMsg":""}]}`))
		default:
			t.Fatalf("unexpected OKX path %s", r.URL.Path)
		}
	}))
	defer okxServer.Close()
	cfg := srv.ConfigStore.Get()
	cfg.Trading.BaseURL = okxServer.URL
	srv.ConfigStore = config.NewStore("", cfg)
	srv.OKXHTTPClient = okxServer.Client()
	if _, err := srv.OKXCredentials.UpdateAccount(okx.CredentialAccountUpdate{
		ID:          "default",
		Active:      true,
		Credentials: okx.Credentials{APIKey: "key", SecretKey: "secret", Passphrase: "pass"},
	}); err != nil {
		t.Fatal(err)
	}

	body := `{"api_id":"default","inst_id":"BTC-USDT-SWAP","ord_id":"100","cl_ord_id":"client-100"}`
	key := pendingOrderChaseKey(pendingOrderChaseRequest{APIID: "default", InstID: "BTC-USDT-SWAP", OrdID: "100", ClOrdID: "client-100"})
	_, chaseCancel := context.WithCancel(context.Background())
	if !pendingOrderChaseJobs.start(key, chaseCancel) {
		t.Fatal("failed to seed chase job")
	}
	req := httptest.NewRequest(http.MethodPost, "/tvbot/pending-orders/cancel", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("admin", "Admin123")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("cancel code=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp pendingOrderChaseResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Status != "canceled" || resp.OrderGroup != "normal" || resp.APIID != "default" || resp.OrdID != "100" || resp.ClOrdID != "client-100" {
		t.Fatalf("bad cancel response: %#v", resp)
	}
	if pendingOrderChaseJobs.activeKey(key) {
		t.Fatal("cancel should stop active OKX chase job")
	}
	if len(cancelBodies) != 1 || cancelBodies[0]["instId"] != "BTC-USDT-SWAP" || cancelBodies[0]["ordId"] != "100" {
		t.Fatalf("bad OKX cancel bodies: %#v", cancelBodies)
	}
}

func TestTVBotOKXPendingAlgoOrderChaseStopAndCancel(t *testing.T) {
	oldInterval := pendingOrderChaseInterval
	oldTimeout := pendingOrderChaseTimeout
	oldJobs := pendingOrderChaseJobs
	pendingOrderChaseInterval = time.Hour
	pendingOrderChaseTimeout = time.Hour
	pendingOrderChaseJobs = newPendingOrderChaseRegistry()
	defer func() {
		pendingOrderChaseInterval = oldInterval
		pendingOrderChaseTimeout = oldTimeout
		pendingOrderChaseJobs = oldJobs
	}()

	srv := newTestServer(t)
	var amendBodies []map[string]any
	var cancelBodies [][]map[string]string
	okxServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v5/trade/orders-algo-pending":
			if r.Header.Get("OK-ACCESS-KEY") != "key" {
				t.Fatalf("missing private OKX headers")
			}
			if r.URL.Query().Get("instType") != "SWAP" || r.URL.Query().Get("instId") != "BTC-USDT-SWAP" {
				t.Fatalf("bad pending algo query: %s", r.URL.RawQuery)
			}
			if r.URL.Query().Get("ordType") != "trigger" {
				_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[]}`))
				return
			}
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instType":"SWAP","instId":"BTC-USDT-SWAP","algoId":"900","algoClOrdId":"algo-900","side":"buy","posSide":"long","ordType":"trigger","sz":"0.5","state":"live","triggerPx":"99","triggerPxType":"last","orderPx":"-1","reduceOnly":true,"cTime":"1784880000000","uTime":"1784880000000"}]}`))
		case "/api/v5/public/instruments":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instId":"BTC-USDT-SWAP","tickSz":"0.1","ctVal":"1","lotSz":"0.1","minSz":"0.1"}]}`))
		case "/api/v5/market/ticker":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instId":"BTC-USDT-SWAP","bidPx":"99.9","askPx":"100.1","last":"100"}]}`))
		case "/api/v5/trade/amend-algos":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			amendBodies = append(amendBodies, body)
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"algoId":"900","algoClOrdId":"algo-900","sCode":"0","sMsg":""}]}`))
		case "/api/v5/trade/cancel-algos":
			var body []map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			cancelBodies = append(cancelBodies, body)
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"algoId":"900","algoClOrdId":"algo-900","sCode":"0","sMsg":""}]}`))
		default:
			t.Fatalf("unexpected OKX path %s", r.URL.Path)
		}
	}))
	defer okxServer.Close()
	cfg := srv.ConfigStore.Get()
	cfg.Trading.BaseURL = okxServer.URL
	srv.ConfigStore = config.NewStore("", cfg)
	srv.OKXHTTPClient = okxServer.Client()
	if _, err := srv.OKXCredentials.UpdateAccount(okx.CredentialAccountUpdate{
		ID:          "default",
		Active:      true,
		Credentials: okx.Credentials{APIKey: "key", SecretKey: "secret", Passphrase: "pass"},
	}); err != nil {
		t.Fatal(err)
	}

	body := `{"api_id":"default","order_group":"algo","inst_id":"BTC-USDT-SWAP","algo_id":"900","algo_cl_ord_id":"algo-900"}`
	start := httptest.NewRequest(http.MethodPost, "/tvbot/pending-orders/chase", strings.NewReader(body))
	start.Header.Set("Content-Type", "application/json")
	start.SetBasicAuth("admin", "Admin123")
	startRR := httptest.NewRecorder()
	srv.ServeHTTP(startRR, start)
	if startRR.Code != http.StatusAccepted {
		t.Fatalf("start algo chase code=%d body=%s", startRR.Code, startRR.Body.String())
	}
	var startResp pendingOrderChaseResponse
	if err := json.Unmarshal(startRR.Body.Bytes(), &startResp); err != nil {
		t.Fatal(err)
	}
	if startResp.Status != "running" || startResp.OrderGroup != "algo" || startResp.Px != "100.1" || startResp.AlgoID != "900" {
		t.Fatalf("bad OKX algo chase response: %#v", startResp)
	}
	key := pendingOrderChaseKey(pendingOrderChaseRequest{APIID: "default", OrderGroup: "algo", InstID: "BTC-USDT-SWAP", AlgoID: "900", AlgoClOrdID: "algo-900"})
	if !pendingOrderChaseJobs.activeKey(key) {
		t.Fatal("OKX algo chase job should be active")
	}
	if len(amendBodies) != 1 || amendBodies[0]["instId"] != "BTC-USDT-SWAP" || amendBodies[0]["algoId"] != "900" || amendBodies[0]["newTriggerPx"] != "100.1" || amendBodies[0]["newOrderPx"] != "-1" {
		t.Fatalf("bad OKX amend algo bodies: %#v", amendBodies)
	}

	stop := httptest.NewRequest(http.MethodPost, "/tvbot/pending-orders/chase/stop", strings.NewReader(body))
	stop.Header.Set("Content-Type", "application/json")
	stop.SetBasicAuth("admin", "Admin123")
	stopRR := httptest.NewRecorder()
	srv.ServeHTTP(stopRR, stop)
	if stopRR.Code != http.StatusOK {
		t.Fatalf("stop algo chase code=%d body=%s", stopRR.Code, stopRR.Body.String())
	}
	var stopResp pendingOrderChaseResponse
	if err := json.Unmarshal(stopRR.Body.Bytes(), &stopResp); err != nil {
		t.Fatal(err)
	}
	if stopResp.Status != "stopped" || pendingOrderChaseJobs.activeKey(key) {
		t.Fatalf("bad OKX algo stop response=%#v active=%v", stopResp, pendingOrderChaseJobs.activeKey(key))
	}
	if len(cancelBodies) != 0 {
		t.Fatalf("stop should not cancel OKX algo order: %#v", cancelBodies)
	}

	cancelReq := httptest.NewRequest(http.MethodPost, "/tvbot/pending-orders/cancel", strings.NewReader(body))
	cancelReq.Header.Set("Content-Type", "application/json")
	cancelReq.SetBasicAuth("admin", "Admin123")
	cancelRR := httptest.NewRecorder()
	srv.ServeHTTP(cancelRR, cancelReq)
	if cancelRR.Code != http.StatusOK {
		t.Fatalf("cancel algo code=%d body=%s", cancelRR.Code, cancelRR.Body.String())
	}
	var cancelResp pendingOrderChaseResponse
	if err := json.Unmarshal(cancelRR.Body.Bytes(), &cancelResp); err != nil {
		t.Fatal(err)
	}
	if cancelResp.Status != "canceled" || cancelResp.OrderGroup != "algo" || cancelResp.AlgoID != "900" {
		t.Fatalf("bad OKX algo cancel response: %#v", cancelResp)
	}
	if len(cancelBodies) != 1 || len(cancelBodies[0]) != 1 || cancelBodies[0][0]["instId"] != "BTC-USDT-SWAP" || cancelBodies[0][0]["algoId"] != "900" {
		t.Fatalf("bad OKX cancel algo bodies: %#v", cancelBodies)
	}
}

func TestTVBotBinancePendingAlgoOrderChaseRecreatesStopAndCancel(t *testing.T) {
	oldInterval := pendingOrderChaseInterval
	oldTimeout := pendingOrderChaseTimeout
	oldJobs := pendingOrderChaseJobs
	pendingOrderChaseInterval = time.Hour
	pendingOrderChaseTimeout = time.Hour
	pendingOrderChaseJobs = newPendingOrderChaseRegistry()
	defer func() {
		pendingOrderChaseInterval = oldInterval
		pendingOrderChaseTimeout = oldTimeout
		pendingOrderChaseJobs = oldJobs
	}()

	srv := newTestServer(t)
	var mu sync.Mutex
	recreated := false
	var cancelForms []url.Values
	var newForms []url.Values
	binanceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/fapi/v1/exchangeInfo" && r.URL.Path != "/fapi/v1/ticker/bookTicker" && r.Header.Get("X-MBX-APIKEY") != "binance-key" {
			t.Fatalf("missing Binance API key for %s", r.URL.Path)
		}
		switch r.URL.Path {
		case "/fapi/v1/openAlgoOrders":
			if r.URL.Query().Get("symbol") != "BTCUSDT" || r.URL.Query().Get("algoType") != "CONDITIONAL" {
				t.Fatalf("bad open algo query: %s", r.URL.RawQuery)
			}
			mu.Lock()
			isRecreated := recreated
			mu.Unlock()
			if isRecreated {
				_, _ = w.Write([]byte(`[{"symbol":"BTCUSDT","algoId":901,"clientAlgoId":"new-algo-901","algoType":"CONDITIONAL","orderType":"STOP_MARKET","side":"SELL","positionSide":"BOTH","quantity":"0.1","algoStatus":"NEW","reduceOnly":true,"triggerPrice":"49999.9","workingType":"MARK_PRICE"}]`))
				return
			}
			_, _ = w.Write([]byte(`[{"symbol":"BTCUSDT","algoId":900,"clientAlgoId":"algo-900","algoType":"CONDITIONAL","orderType":"STOP_MARKET","side":"SELL","positionSide":"BOTH","quantity":"0.1","algoStatus":"NEW","reduceOnly":true,"triggerPrice":"51000","workingType":"MARK_PRICE"}]`))
		case "/fapi/v1/exchangeInfo":
			_, _ = w.Write([]byte(`{"symbols":[{"symbol":"BTCUSDT","status":"TRADING","pricePrecision":2,"quantityPrecision":3,"filters":[{"filterType":"PRICE_FILTER","tickSize":"0.10"},{"filterType":"LOT_SIZE","minQty":"0.001","stepSize":"0.001"}]}]}`))
		case "/fapi/v1/ticker/bookTicker":
			if r.URL.Query().Get("symbol") != "BTCUSDT" {
				t.Fatalf("bad book ticker query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","bidPrice":"49999.9","bidQty":"1","askPrice":"50000.1","askQty":"2","time":1784880000000}`))
		case "/fapi/v1/algoOrder":
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			switch r.Method {
			case http.MethodDelete:
				mu.Lock()
				cancelForms = append(cancelForms, cloneValues(r.Form))
				mu.Unlock()
				algoID := r.Form.Get("algoId")
				if algoID == "" {
					algoID = "900"
				}
				_, _ = w.Write([]byte(`{"algoId":` + algoID + `,"clientAlgoId":"algo-` + algoID + `","code":"200","msg":"success"}`))
			case http.MethodPost:
				mu.Lock()
				newForms = append(newForms, cloneValues(r.Form))
				recreated = true
				mu.Unlock()
				_, _ = w.Write([]byte(`{"algoId":901,"clientAlgoId":"new-algo-901","algoType":"CONDITIONAL","orderType":"STOP_MARKET","symbol":"BTCUSDT","side":"SELL","positionSide":"BOTH","quantity":"0.1","algoStatus":"NEW","triggerPrice":"49999.9"}`))
			default:
				t.Fatalf("unexpected algo method %s", r.Method)
			}
		default:
			t.Fatalf("unexpected Binance path %s", r.URL.Path)
		}
	}))
	defer binanceServer.Close()
	cfg := srv.ConfigStore.Get()
	cfg.Trading.BinanceDemoBaseURL = binanceServer.URL
	srv.ConfigStore = config.NewStore("", cfg)
	srv.BinanceHTTPClient = binanceServer.Client()
	if _, err := srv.BinanceCredentials.UpdateAccount(binance.CredentialAccountUpdate{
		ID:          "main",
		Active:      true,
		Credentials: binance.Credentials{APIKey: "binance-key", SecretKey: "binance-secret"},
	}); err != nil {
		t.Fatal(err)
	}

	body := `{"exchange":"binance","api_id":"main","order_group":"algo","inst_id":"BTCUSDT","algo_id":"900","algo_cl_ord_id":"algo-900"}`
	start := httptest.NewRequest(http.MethodPost, "/tvbot/pending-orders/chase", strings.NewReader(body))
	start.Header.Set("Content-Type", "application/json")
	start.SetBasicAuth("admin", "Admin123")
	startRR := httptest.NewRecorder()
	srv.ServeHTTP(startRR, start)
	if startRR.Code != http.StatusAccepted {
		t.Fatalf("start Binance algo chase code=%d body=%s", startRR.Code, startRR.Body.String())
	}
	var startResp pendingOrderChaseResponse
	if err := json.Unmarshal(startRR.Body.Bytes(), &startResp); err != nil {
		t.Fatal(err)
	}
	if startResp.Status != "running" || startResp.OrderGroup != "algo" || startResp.Px != "49999.9" || startResp.AlgoID != "901" || startResp.AlgoClOrdID != "new-algo-901" {
		t.Fatalf("bad Binance algo chase response: %#v", startResp)
	}
	oldKey := pendingOrderChaseKey(pendingOrderChaseRequest{Exchange: trading.ExchangeBinance, APIID: "main", OrderGroup: "algo", InstID: "BTCUSDT", AlgoID: "900", AlgoClOrdID: "algo-900"})
	newKey := pendingOrderChaseKey(pendingOrderChaseRequest{Exchange: trading.ExchangeBinance, APIID: "main", OrderGroup: "algo", InstID: "BTCUSDT", AlgoID: "901", AlgoClOrdID: "new-algo-901"})
	if pendingOrderChaseJobs.activeKey(oldKey) || !pendingOrderChaseJobs.activeKey(newKey) {
		t.Fatalf("expected Binance algo chase to track new key old=%v new=%v", pendingOrderChaseJobs.activeKey(oldKey), pendingOrderChaseJobs.activeKey(newKey))
	}
	mu.Lock()
	if len(cancelForms) != 1 || cancelForms[0].Get("algoId") != "900" {
		t.Fatalf("bad Binance algo chase cancel forms: %#v", cancelForms)
	}
	if len(newForms) != 1 {
		t.Fatalf("expected one Binance algo recreate form, got %#v", newForms)
	}
	recreate := newForms[0]
	if recreate.Get("symbol") != "BTCUSDT" || recreate.Get("side") != "SELL" || recreate.Get("positionSide") != "BOTH" || recreate.Get("type") != "STOP_MARKET" || recreate.Get("quantity") != "0.1" || recreate.Get("triggerPrice") != "49999.9" || recreate.Get("workingType") != "MARK_PRICE" || recreate.Get("reduceOnly") != "true" || recreate.Get("clientAlgoId") == "" {
		t.Fatalf("bad Binance algo recreate form: %#v", recreate)
	}
	mu.Unlock()

	stopBody := `{"exchange":"binance","api_id":"main","order_group":"algo","inst_id":"BTCUSDT","algo_id":"901","algo_cl_ord_id":"new-algo-901"}`
	stop := httptest.NewRequest(http.MethodPost, "/tvbot/pending-orders/chase/stop", strings.NewReader(stopBody))
	stop.Header.Set("Content-Type", "application/json")
	stop.SetBasicAuth("admin", "Admin123")
	stopRR := httptest.NewRecorder()
	srv.ServeHTTP(stopRR, stop)
	if stopRR.Code != http.StatusOK {
		t.Fatalf("stop Binance algo chase code=%d body=%s", stopRR.Code, stopRR.Body.String())
	}
	var stopResp pendingOrderChaseResponse
	if err := json.Unmarshal(stopRR.Body.Bytes(), &stopResp); err != nil {
		t.Fatal(err)
	}
	if stopResp.Status != "stopped" || pendingOrderChaseJobs.activeKey(newKey) {
		t.Fatalf("bad Binance algo stop response=%#v active=%v", stopResp, pendingOrderChaseJobs.activeKey(newKey))
	}
	mu.Lock()
	cancelCountAfterStop := len(cancelForms)
	mu.Unlock()
	if cancelCountAfterStop != 1 {
		t.Fatalf("stop should not cancel Binance algo order, forms=%#v", cancelForms)
	}

	cancelReq := httptest.NewRequest(http.MethodPost, "/tvbot/pending-orders/cancel", strings.NewReader(stopBody))
	cancelReq.Header.Set("Content-Type", "application/json")
	cancelReq.SetBasicAuth("admin", "Admin123")
	cancelRR := httptest.NewRecorder()
	srv.ServeHTTP(cancelRR, cancelReq)
	if cancelRR.Code != http.StatusOK {
		t.Fatalf("cancel Binance algo code=%d body=%s", cancelRR.Code, cancelRR.Body.String())
	}
	var cancelResp pendingOrderChaseResponse
	if err := json.Unmarshal(cancelRR.Body.Bytes(), &cancelResp); err != nil {
		t.Fatal(err)
	}
	if cancelResp.Status != "canceled" || cancelResp.OrderGroup != "algo" || cancelResp.AlgoID != "901" || cancelResp.AlgoClOrdID != "new-algo-901" {
		t.Fatalf("bad Binance algo cancel response: %#v", cancelResp)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(cancelForms) != 2 || cancelForms[1].Get("algoId") != "901" {
		t.Fatalf("bad Binance final algo cancel forms: %#v", cancelForms)
	}
}

func TestTVBotBinancePendingOrderCancelStopsChaseAndCancels(t *testing.T) {
	oldJobs := pendingOrderChaseJobs
	pendingOrderChaseJobs = newPendingOrderChaseRegistry()
	defer func() {
		pendingOrderChaseJobs = oldJobs
	}()

	srv := newTestServer(t)
	var cancelForms []url.Values
	binanceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("X-MBX-APIKEY") != "binance-key" {
			t.Fatalf("missing Binance API key for %s", r.URL.Path)
		}
		switch r.URL.Path {
		case "/fapi/v1/openOrders":
			if r.URL.Query().Get("symbol") != "BTCUSDT" {
				t.Fatalf("bad open orders query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`[{"symbol":"BTCUSDT","orderId":123456,"clientOrderId":"client-1","price":"50000","origQty":"0.2","executedQty":"0","side":"BUY","positionSide":"BOTH","type":"LIMIT","status":"NEW","time":1784880000000,"updateTime":1784880005000}]`))
		case "/fapi/v1/order":
			if r.Method != http.MethodDelete {
				t.Fatalf("cancel should only call DELETE /fapi/v1/order, got %s", r.Method)
			}
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			cancelForms = append(cancelForms, cloneValues(r.Form))
			_, _ = w.Write([]byte(`{"orderId":123456,"symbol":"BTCUSDT","status":"CANCELED","clientOrderId":"client-1","price":"50000","origQty":"0.2","executedQty":"0","type":"LIMIT","side":"BUY","positionSide":"BOTH"}`))
		default:
			t.Fatalf("unexpected Binance path %s", r.URL.Path)
		}
	}))
	defer binanceServer.Close()
	cfg := srv.ConfigStore.Get()
	cfg.Trading.BinanceDemoBaseURL = binanceServer.URL
	srv.ConfigStore = config.NewStore("", cfg)
	srv.BinanceHTTPClient = binanceServer.Client()
	if _, err := srv.BinanceCredentials.UpdateAccount(binance.CredentialAccountUpdate{
		ID:          "main",
		Active:      true,
		Credentials: binance.Credentials{APIKey: "binance-key", SecretKey: "binance-secret"},
	}); err != nil {
		t.Fatal(err)
	}

	body := `{"exchange":"binance","api_id":"main","inst_id":"BTCUSDT","ord_id":"123456","cl_ord_id":"client-1"}`
	key := pendingOrderChaseKey(pendingOrderChaseRequest{Exchange: trading.ExchangeBinance, APIID: "main", InstID: "BTCUSDT", OrdID: "123456", ClOrdID: "client-1"})
	_, chaseCancel := context.WithCancel(context.Background())
	if !pendingOrderChaseJobs.start(key, chaseCancel) {
		t.Fatal("failed to seed chase job")
	}
	req := httptest.NewRequest(http.MethodPost, "/tvbot/pending-orders/cancel", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("admin", "Admin123")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("cancel code=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp pendingOrderChaseResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Status != "canceled" || resp.APIID != "main" || resp.OrdID != "123456" || resp.ClOrdID != "client-1" {
		t.Fatalf("bad cancel response: %#v", resp)
	}
	if pendingOrderChaseJobs.activeKey(key) {
		t.Fatal("cancel should stop active Binance chase job")
	}
	if len(cancelForms) != 1 || cancelForms[0].Get("symbol") != "BTCUSDT" || cancelForms[0].Get("orderId") != "123456" {
		t.Fatalf("bad cancel forms: %#v", cancelForms)
	}
}

func TestTVBotBinancePendingOrderCancelTreatsUnknownOrderAsFinished(t *testing.T) {
	oldJobs := pendingOrderChaseJobs
	pendingOrderChaseJobs = newPendingOrderChaseRegistry()
	defer func() {
		pendingOrderChaseJobs = oldJobs
	}()

	srv := newTestServer(t)
	var cancelForms []url.Values
	binanceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("X-MBX-APIKEY") != "binance-key" {
			t.Fatalf("missing Binance API key for %s", r.URL.Path)
		}
		switch r.URL.Path {
		case "/fapi/v1/openOrders":
			if r.URL.Query().Get("symbol") != "BTCUSDT" {
				t.Fatalf("bad open orders query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`[{"symbol":"BTCUSDT","orderId":123456,"clientOrderId":"client-1","price":"50000","origQty":"0.2","executedQty":"0.1","side":"BUY","positionSide":"BOTH","type":"LIMIT","status":"PARTIALLY_FILLED","time":1784880000000,"updateTime":1784880005000}]`))
		case "/fapi/v1/order":
			if r.Method != http.MethodDelete {
				t.Fatalf("cancel should only call DELETE /fapi/v1/order, got %s", r.Method)
			}
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			cancelForms = append(cancelForms, cloneValues(r.Form))
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"code":-2011,"msg":"Unknown order sent."}`))
		default:
			t.Fatalf("unexpected Binance path %s", r.URL.Path)
		}
	}))
	defer binanceServer.Close()
	cfg := srv.ConfigStore.Get()
	cfg.Trading.BinanceDemoBaseURL = binanceServer.URL
	srv.ConfigStore = config.NewStore("", cfg)
	srv.BinanceHTTPClient = binanceServer.Client()
	if _, err := srv.BinanceCredentials.UpdateAccount(binance.CredentialAccountUpdate{
		ID:          "main",
		Active:      true,
		Credentials: binance.Credentials{APIKey: "binance-key", SecretKey: "binance-secret"},
	}); err != nil {
		t.Fatal(err)
	}

	body := `{"exchange":"binance","api_id":"main","inst_id":"BTCUSDT","ord_id":"123456","cl_ord_id":"client-1"}`
	key := pendingOrderChaseKey(pendingOrderChaseRequest{Exchange: trading.ExchangeBinance, APIID: "main", InstID: "BTCUSDT", OrdID: "123456", ClOrdID: "client-1"})
	_, chaseCancel := context.WithCancel(context.Background())
	if !pendingOrderChaseJobs.start(key, chaseCancel) {
		t.Fatal("failed to seed chase job")
	}
	req := httptest.NewRequest(http.MethodPost, "/tvbot/pending-orders/cancel", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("admin", "Admin123")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("cancel code=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp pendingOrderChaseResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Status != "finished" || resp.APIID != "main" || resp.OrdID != "123456" || resp.ClOrdID != "client-1" {
		t.Fatalf("bad cancel response: %#v", resp)
	}
	if pendingOrderChaseJobs.activeKey(key) {
		t.Fatal("cancel should stop active Binance chase job when order is gone")
	}
	if len(cancelForms) != 1 || cancelForms[0].Get("symbol") != "BTCUSDT" || cancelForms[0].Get("orderId") != "123456" {
		t.Fatalf("bad cancel forms: %#v", cancelForms)
	}
}

func TestTVBotPendingOrderChaseRebuildsNakedOrderWithRiskControls(t *testing.T) {
	oldInterval := pendingOrderChaseInterval
	oldTimeout := pendingOrderChaseTimeout
	oldJobs := pendingOrderChaseJobs
	pendingOrderChaseInterval = time.Hour
	pendingOrderChaseTimeout = time.Hour
	pendingOrderChaseJobs = newPendingOrderChaseRegistry()
	defer func() {
		pendingOrderChaseInterval = oldInterval
		pendingOrderChaseTimeout = oldTimeout
		pendingOrderChaseJobs = oldJobs
	}()

	srv := newTestServer(t)
	var cancelBodies []map[string]string
	var orderBodies []map[string]any
	okxServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v5/trade/orders-pending":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instType":"SWAP","instId":"BTC-USDT-SWAP","ordId":"100","clOrdId":"client-100","tdMode":"isolated","side":"buy","posSide":"long","ordType":"limit","px":"64000","sz":"0.5","accFillSz":"0.1","state":"live","cTime":"1784880000000"}]}`))
		case "/api/v5/public/instruments":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instId":"BTC-USDT-SWAP","tickSz":"0.1","ctVal":"1","lotSz":"1","minSz":"1"}]}`))
		case "/api/v5/market/ticker":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instId":"BTC-USDT-SWAP","bidPx":"63999","askPx":"64001","last":"64000"}]}`))
		case "/api/v5/trade/cancel-order":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			cancelBodies = append(cancelBodies, body)
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"ordId":"100","clOrdId":"client-100","sCode":"0","sMsg":""}]}`))
		case "/api/v5/trade/order":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			orderBodies = append(orderBodies, body)
			clOrdID, _ := body["clOrdId"].(string)
			_, _ = w.Write([]byte(fmt.Sprintf(`{"code":"0","msg":"","data":[{"ordId":"101","clOrdId":%q,"sCode":"0","sMsg":""}]}`, clOrdID)))
		case "/api/v5/trade/amend-order":
			t.Fatal("naked protected chase should rebuild instead of amend original order")
		default:
			t.Fatalf("unexpected OKX path %s", r.URL.Path)
		}
	}))
	defer okxServer.Close()
	cfg := srv.ConfigStore.Get()
	cfg.Trading.BaseURL = okxServer.URL
	cfg.Trading.TakeProfitPct = 2
	cfg.Trading.StopLossPct = 1
	srv.ConfigStore = config.NewStore("", cfg)
	srv.OKXHTTPClient = okxServer.Client()
	if _, err := srv.OKXCredentials.UpdateAccount(okx.CredentialAccountUpdate{
		ID:          "default",
		Active:      true,
		Credentials: okx.Credentials{APIKey: "key", SecretKey: "secret", Passphrase: "pass"},
	}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/tvbot/pending-orders/chase", strings.NewReader(`{"api_id":"default","inst_id":"BTC-USDT-SWAP","ord_id":"100","cl_ord_id":"client-100"}`))
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("admin", "Admin123")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("chase code=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp pendingOrderChaseResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Status != "running" || resp.OrdID != "101" || resp.ClOrdID == "" || resp.Px != "63999.9" {
		t.Fatalf("bad chase response: %#v", resp)
	}
	key := pendingOrderChaseKey(pendingOrderChaseRequest{APIID: "default", InstID: "BTC-USDT-SWAP", OrdID: "101", ClOrdID: resp.ClOrdID})
	defer pendingOrderChaseJobs.stop(key)
	if !pendingOrderChaseJobs.activeKey(key) {
		t.Fatal("rebuilt order should be tracked for chase")
	}
	if len(cancelBodies) != 1 || cancelBodies[0]["ordId"] != "100" {
		t.Fatalf("bad cancel bodies: %#v", cancelBodies)
	}
	if len(orderBodies) != 1 {
		t.Fatalf("expected rebuilt order body, got %#v", orderBodies)
	}
	order := orderBodies[0]
	if order["instId"] != "BTC-USDT-SWAP" || order["tdMode"] != "isolated" || order["side"] != "buy" || order["posSide"] != "long" || order["ordType"] != "limit" || order["px"] != "63999.9" || order["sz"] != "0.4" {
		t.Fatalf("bad rebuilt limit order: %#v", order)
	}
	attach, ok := order["attachAlgoOrds"].([]any)
	if !ok || len(attach) != 1 {
		t.Fatalf("missing attach algo on rebuilt order: %#v", order)
	}
	first, ok := attach[0].(map[string]any)
	if !ok || first["tpTriggerRatio"] != "0.02" || first["slTriggerRatio"] != "-0.01" || first["tpOrdPx"] != "-1" || first["slOrdPx"] != "-1" {
		t.Fatalf("bad rebuilt attach algo: %#v", attach)
	}
}

func TestTVBotPendingOrderChasePreservesExistingRiskControlsWhileAmendingPrice(t *testing.T) {
	oldInterval := pendingOrderChaseInterval
	oldTimeout := pendingOrderChaseTimeout
	oldJobs := pendingOrderChaseJobs
	pendingOrderChaseInterval = time.Hour
	pendingOrderChaseTimeout = time.Hour
	pendingOrderChaseJobs = newPendingOrderChaseRegistry()
	defer func() {
		pendingOrderChaseInterval = oldInterval
		pendingOrderChaseTimeout = oldTimeout
		pendingOrderChaseJobs = oldJobs
	}()

	srv := newTestServer(t)
	var amendBodies []map[string]any
	var cancelCalled bool
	okxServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v5/trade/orders-pending":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instType":"SWAP","instId":"BTC-USDT-SWAP","ordId":"100","clOrdId":"client-100","tdMode":"isolated","side":"sell","posSide":"short","ordType":"limit","px":"64000","sz":"0.5","accFillSz":"0","state":"live","attachAlgoOrds":[{"attachAlgoClOrdId":"client-100A","tpTriggerRatio":"-0.03","slTriggerRatio":"0.015"}],"cTime":"1784880000000"}]}`))
		case "/api/v5/public/instruments":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instId":"BTC-USDT-SWAP","tickSz":"0.1","ctVal":"1","lotSz":"1","minSz":"1"}]}`))
		case "/api/v5/market/ticker":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instId":"BTC-USDT-SWAP","bidPx":"63999","askPx":"64001","last":"64000"}]}`))
		case "/api/v5/trade/amend-order":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			amendBodies = append(amendBodies, body)
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"ordId":"100","clOrdId":"client-100","sCode":"0","sMsg":""}]}`))
		case "/api/v5/trade/cancel-order":
			cancelCalled = true
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[]}`))
		case "/api/v5/trade/order":
			t.Fatal("existing risk controls should amend, not rebuild")
		default:
			t.Fatalf("unexpected OKX path %s", r.URL.Path)
		}
	}))
	defer okxServer.Close()
	cfg := srv.ConfigStore.Get()
	cfg.Trading.BaseURL = okxServer.URL
	cfg.Trading.TakeProfitPct = 3
	cfg.Trading.StopLossPct = 1.5
	srv.ConfigStore = config.NewStore("", cfg)
	srv.OKXHTTPClient = okxServer.Client()
	if _, err := srv.OKXCredentials.UpdateAccount(okx.CredentialAccountUpdate{
		ID:          "default",
		Active:      true,
		Credentials: okx.Credentials{APIKey: "key", SecretKey: "secret", Passphrase: "pass"},
	}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/tvbot/pending-orders/chase", strings.NewReader(`{"api_id":"default","inst_id":"BTC-USDT-SWAP","ord_id":"100","cl_ord_id":"client-100"}`))
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("admin", "Admin123")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("chase code=%d body=%s", rr.Code, rr.Body.String())
	}
	key := pendingOrderChaseKey(pendingOrderChaseRequest{APIID: "default", InstID: "BTC-USDT-SWAP", OrdID: "100", ClOrdID: "client-100"})
	defer pendingOrderChaseJobs.stop(key)
	if cancelCalled || len(amendBodies) != 1 {
		t.Fatalf("expected one amend and no cancel: amend=%#v cancel=%v", amendBodies, cancelCalled)
	}
	amend := amendBodies[0]
	if amend["instId"] != "BTC-USDT-SWAP" || amend["ordId"] != "100" || amend["newPx"] != "64000.1" {
		t.Fatalf("bad amend body: %#v", amend)
	}
	if _, ok := amend["attachAlgoOrds"]; ok {
		t.Fatalf("chase amend should only change price and preserve existing TP/SL on OKX: %#v", amend)
	}
}

func TestTVBotPendingOrderChaseRebuildsStaleTPSLRiskControls(t *testing.T) {
	oldInterval := pendingOrderChaseInterval
	oldTimeout := pendingOrderChaseTimeout
	oldJobs := pendingOrderChaseJobs
	pendingOrderChaseInterval = time.Hour
	pendingOrderChaseTimeout = time.Hour
	pendingOrderChaseJobs = newPendingOrderChaseRegistry()
	defer func() {
		pendingOrderChaseInterval = oldInterval
		pendingOrderChaseTimeout = oldTimeout
		pendingOrderChaseJobs = oldJobs
	}()

	srv := newTestServer(t)
	var cancelBodies []map[string]string
	var orderBodies []map[string]any
	okxServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v5/trade/orders-pending":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instType":"SWAP","instId":"BTC-USDT-SWAP","ordId":"100","clOrdId":"client-100","tdMode":"isolated","side":"sell","posSide":"short","ordType":"limit","px":"64000","sz":"0.5","accFillSz":"0.1","state":"partially_filled","attachAlgoOrds":[{"attachAlgoClOrdId":"client-100A","tpTriggerRatio":"-0.008","slTriggerRatio":"0.0095"}],"cTime":"1784880000000"}]}`))
		case "/api/v5/public/instruments":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instId":"BTC-USDT-SWAP","tickSz":"0.1","ctVal":"1","lotSz":"1","minSz":"1"}]}`))
		case "/api/v5/market/ticker":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instId":"BTC-USDT-SWAP","bidPx":"63999","askPx":"64001","last":"64000"}]}`))
		case "/api/v5/trade/cancel-order":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			cancelBodies = append(cancelBodies, body)
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"ordId":"100","clOrdId":"client-100","sCode":"0","sMsg":""}]}`))
		case "/api/v5/trade/order":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			orderBodies = append(orderBodies, body)
			clOrdID, _ := body["clOrdId"].(string)
			_, _ = w.Write([]byte(fmt.Sprintf(`{"code":"0","msg":"","data":[{"ordId":"101","clOrdId":%q,"sCode":"0","sMsg":""}]}`, clOrdID)))
		case "/api/v5/trade/amend-order":
			t.Fatal("stale TP/SL risk controls should rebuild instead of amend")
		default:
			t.Fatalf("unexpected OKX path %s", r.URL.Path)
		}
	}))
	defer okxServer.Close()
	cfg := srv.ConfigStore.Get()
	cfg.Trading.BaseURL = okxServer.URL
	cfg.Trading.RiskType = string(trading.RiskTPSL)
	cfg.Trading.TakeProfitPct = 0.75
	cfg.Trading.StopLossPct = 1
	srv.ConfigStore = config.NewStore("", cfg)
	srv.OKXHTTPClient = okxServer.Client()
	if _, err := srv.OKXCredentials.UpdateAccount(okx.CredentialAccountUpdate{
		ID:          "default",
		Active:      true,
		Credentials: okx.Credentials{APIKey: "key", SecretKey: "secret", Passphrase: "pass"},
	}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/tvbot/pending-orders/chase", strings.NewReader(`{"api_id":"default","inst_id":"BTC-USDT-SWAP","ord_id":"100","cl_ord_id":"client-100"}`))
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("admin", "Admin123")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("chase code=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp pendingOrderChaseResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	key := pendingOrderChaseKey(pendingOrderChaseRequest{APIID: "default", InstID: "BTC-USDT-SWAP", OrdID: "101", ClOrdID: resp.ClOrdID})
	defer pendingOrderChaseJobs.stop(key)
	if resp.Status != "running" || resp.OrdID != "101" || resp.Px != "64000.1" {
		t.Fatalf("bad chase response: %#v", resp)
	}
	if len(cancelBodies) != 1 || cancelBodies[0]["ordId"] != "100" {
		t.Fatalf("bad cancel bodies: %#v", cancelBodies)
	}
	if len(orderBodies) != 1 {
		t.Fatalf("expected rebuilt order body, got %#v", orderBodies)
	}
	order := orderBodies[0]
	if order["instId"] != "BTC-USDT-SWAP" || order["side"] != "sell" || order["ordType"] != "limit" || order["px"] != "64000.1" || order["sz"] != "0.4" {
		t.Fatalf("bad rebuilt order: %#v", order)
	}
	attach, ok := order["attachAlgoOrds"].([]any)
	if !ok || len(attach) != 1 {
		t.Fatalf("missing attach algo on rebuilt order: %#v", order)
	}
	first, ok := attach[0].(map[string]any)
	if !ok || first["tpTriggerRatio"] != "-0.0075" || first["slTriggerRatio"] != "0.01" || first["tpOrdPx"] != "-1" || first["slOrdPx"] != "-1" {
		t.Fatalf("bad rebuilt attach algo: %#v", attach)
	}
}

func TestTVBotPendingOrderRiskRebuildPreservesPriceAndRefreshesTPSL(t *testing.T) {
	srv := newTestServer(t)
	var cancelBodies []map[string]string
	var orderBodies []map[string]any
	okxServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v5/trade/orders-pending":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instType":"SWAP","instId":"DOT-USDT-SWAP","ordId":"100","clOrdId":"client-100","tdMode":"isolated","side":"sell","posSide":"net","ordType":"limit","px":"0.78","sz":"3846.1","accFillSz":"601.6","avgPx":"0.78","state":"partially_filled","attachAlgoOrds":[{"attachAlgoClOrdId":"client-100A","tpTriggerRatio":"-0.01","slTriggerRatio":"0.05"}],"cTime":"1784880000000"}]}`))
		case "/api/v5/trade/cancel-order":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			cancelBodies = append(cancelBodies, body)
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"ordId":"100","clOrdId":"client-100","sCode":"0","sMsg":""}]}`))
		case "/api/v5/trade/order":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			orderBodies = append(orderBodies, body)
			clOrdID, _ := body["clOrdId"].(string)
			_, _ = w.Write([]byte(fmt.Sprintf(`{"code":"0","msg":"","data":[{"ordId":"101","clOrdId":%q,"sCode":"0","sMsg":""}]}`, clOrdID)))
		default:
			t.Fatalf("unexpected OKX path %s", r.URL.Path)
		}
	}))
	defer okxServer.Close()
	cfg := srv.ConfigStore.Get()
	cfg.Trading.BaseURL = okxServer.URL
	cfg.Trading.RiskType = string(trading.RiskTPSL)
	cfg.Trading.TakeProfitPct = 0.75
	cfg.Trading.StopLossPct = 1
	srv.ConfigStore = config.NewStore("", cfg)
	srv.OKXHTTPClient = okxServer.Client()
	if _, err := srv.OKXCredentials.UpdateAccount(okx.CredentialAccountUpdate{
		ID:          "default",
		Active:      true,
		Credentials: okx.Credentials{APIKey: "key", SecretKey: "secret", Passphrase: "pass"},
	}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/tvbot/pending-orders/risk", strings.NewReader(`{"api_id":"default","inst_id":"DOT-USDT-SWAP","ord_id":"100","cl_ord_id":"client-100"}`))
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("admin", "Admin123")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("risk rebuild code=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp pendingOrderChaseResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Status != "rebuilt" || resp.OrdID != "101" || resp.Px != "0.78" {
		t.Fatalf("bad risk rebuild response: %#v", resp)
	}
	if len(cancelBodies) != 1 || cancelBodies[0]["ordId"] != "100" {
		t.Fatalf("bad cancel bodies: %#v", cancelBodies)
	}
	if len(orderBodies) != 1 {
		t.Fatalf("expected one replacement order, got %#v", orderBodies)
	}
	order := orderBodies[0]
	if order["instId"] != "DOT-USDT-SWAP" || order["tdMode"] != "isolated" || order["side"] != "sell" || order["ordType"] != "limit" || order["px"] != "0.78" || order["sz"] != "3244.5" {
		t.Fatalf("bad replacement order: %#v", order)
	}
	attach, ok := order["attachAlgoOrds"].([]any)
	if !ok || len(attach) != 1 {
		t.Fatalf("missing attach algo on replacement order: %#v", order)
	}
	first, ok := attach[0].(map[string]any)
	if !ok || first["tpTriggerRatio"] != "-0.0075" || first["slTriggerRatio"] != "0.01" || first["tpOrdPx"] != "-1" || first["slOrdPx"] != "-1" {
		t.Fatalf("bad replacement attach algo: %#v", attach)
	}
}

func TestTVBotPendingOrderChaseRebuildsExistingTrailingRiskControls(t *testing.T) {
	oldInterval := pendingOrderChaseInterval
	oldTimeout := pendingOrderChaseTimeout
	oldJobs := pendingOrderChaseJobs
	pendingOrderChaseInterval = time.Hour
	pendingOrderChaseTimeout = time.Hour
	pendingOrderChaseJobs = newPendingOrderChaseRegistry()
	defer func() {
		pendingOrderChaseInterval = oldInterval
		pendingOrderChaseTimeout = oldTimeout
		pendingOrderChaseJobs = oldJobs
	}()

	srv := newTestServer(t)
	var cancelBodies []map[string]string
	var orderBodies []map[string]any
	okxServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v5/trade/orders-pending":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instType":"SWAP","instId":"TRX-USDT-SWAP","ordId":"3779916965005234176","clOrdId":"TV17851527039509FE4C27EA093","tdMode":"isolated","side":"buy","posSide":"net","ordType":"limit","px":"0.3298","sz":"1.51","accFillSz":"0","state":"live","attachAlgoOrds":[{"attachAlgoClOrdId":"TV17851527039509FE4C27EA093T","attachAlgoId":"3779916964978577408","callbackRatio":"0.035"}],"cTime":"1785152703989"}]}`))
		case "/api/v5/public/instruments":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instId":"TRX-USDT-SWAP","tickSz":"0.0001","ctVal":"100","lotSz":"0.01","minSz":"0.01"}]}`))
		case "/api/v5/market/ticker":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instId":"TRX-USDT-SWAP","bidPx":"0.3306","askPx":"0.3308","last":"0.3307"}]}`))
		case "/api/v5/trade/cancel-order":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			cancelBodies = append(cancelBodies, body)
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"ordId":"3779916965005234176","clOrdId":"TV17851527039509FE4C27EA093","sCode":"0","sMsg":""}]}`))
		case "/api/v5/trade/order":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			orderBodies = append(orderBodies, body)
			clOrdID, _ := body["clOrdId"].(string)
			_, _ = w.Write([]byte(fmt.Sprintf(`{"code":"0","msg":"","data":[{"ordId":"new-trx","clOrdId":%q,"sCode":"0","sMsg":""}]}`, clOrdID)))
		case "/api/v5/trade/amend-order":
			t.Fatal("existing trailing protection should rebuild instead of amend")
		default:
			t.Fatalf("unexpected OKX path %s", r.URL.Path)
		}
	}))
	defer okxServer.Close()
	cfg := srv.ConfigStore.Get()
	cfg.Trading.BaseURL = okxServer.URL
	cfg.Trading.RiskType = string(trading.RiskTrailing)
	cfg.Trading.TrailingPct = 3.5
	srv.ConfigStore = config.NewStore("", cfg)
	srv.OKXHTTPClient = okxServer.Client()
	if _, err := srv.OKXCredentials.UpdateAccount(okx.CredentialAccountUpdate{
		ID:          "default",
		Active:      true,
		Credentials: okx.Credentials{APIKey: "key", SecretKey: "secret", Passphrase: "pass"},
	}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/tvbot/pending-orders/chase", strings.NewReader(`{"api_id":"default","inst_id":"TRX-USDT-SWAP","ord_id":"3779916965005234176","cl_ord_id":"TV17851527039509FE4C27EA093"}`))
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("admin", "Admin123")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("chase code=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(cancelBodies) != 1 || cancelBodies[0]["instId"] != "TRX-USDT-SWAP" || cancelBodies[0]["ordId"] != "3779916965005234176" {
		t.Fatalf("bad cancel bodies: %#v", cancelBodies)
	}
	if len(orderBodies) != 1 {
		t.Fatalf("expected one replacement order, got %#v", orderBodies)
	}
	replacement := orderBodies[0]
	if replacement["instId"] != "TRX-USDT-SWAP" || replacement["tdMode"] != "isolated" || replacement["side"] != "buy" || replacement["ordType"] != "limit" || replacement["px"] != "0.3306" || replacement["sz"] != "1.51" {
		t.Fatalf("bad replacement order: %#v", replacement)
	}
	replacementClOrdID, _ := replacement["clOrdId"].(string)
	key := pendingOrderChaseKey(pendingOrderChaseRequest{APIID: "default", InstID: "TRX-USDT-SWAP", OrdID: "new-trx", ClOrdID: replacementClOrdID})
	defer pendingOrderChaseJobs.stop(key)
	if _, ok := replacement["posSide"]; ok {
		t.Fatalf("net-mode replacement should not send posSide: %#v", replacement)
	}
	attach, ok := replacement["attachAlgoOrds"].([]any)
	if !ok || len(attach) != 1 {
		t.Fatalf("missing trailing attach algo: %#v", replacement)
	}
	first, ok := attach[0].(map[string]any)
	if !ok || first["ordType"] != "move_order_stop" || first["callbackRatio"] != "0.035" {
		t.Fatalf("bad trailing attach algo: %#v", attach)
	}
}

func TestTVBotPendingOrderChaseStopDoesNotCancelOrder(t *testing.T) {
	oldInterval := pendingOrderChaseInterval
	oldJobs := pendingOrderChaseJobs
	pendingOrderChaseInterval = time.Hour
	pendingOrderChaseJobs = newPendingOrderChaseRegistry()
	defer func() {
		pendingOrderChaseInterval = oldInterval
		pendingOrderChaseJobs = oldJobs
	}()

	srv := newTestServer(t)
	var amendCount int
	var cancelCalled bool
	okxServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v5/trade/orders-pending":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instType":"SWAP","instId":"BTC-USDT-SWAP","ordId":"100","clOrdId":"client-100","tdMode":"isolated","side":"sell","posSide":"short","ordType":"limit","px":"64000","sz":"0.5","accFillSz":"0","state":"live","cTime":"1784880000000"}]}`))
		case "/api/v5/public/instruments":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instId":"BTC-USDT-SWAP","tickSz":"0.1","ctVal":"1","lotSz":"1","minSz":"1"}]}`))
		case "/api/v5/market/ticker":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instId":"BTC-USDT-SWAP","bidPx":"63999","askPx":"64001","last":"64000"}]}`))
		case "/api/v5/trade/amend-order":
			amendCount++
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"ordId":"100","clOrdId":"client-100","sCode":"0","sMsg":""}]}`))
		case "/api/v5/trade/cancel-order":
			cancelCalled = true
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[]}`))
		default:
			t.Fatalf("unexpected OKX path %s", r.URL.Path)
		}
	}))
	defer okxServer.Close()
	cfg := srv.ConfigStore.Get()
	cfg.Trading.BaseURL = okxServer.URL
	cfg.Trading.RiskType = string(trading.RiskNone)
	srv.ConfigStore = config.NewStore("", cfg)
	srv.OKXHTTPClient = okxServer.Client()
	if _, err := srv.OKXCredentials.UpdateAccount(okx.CredentialAccountUpdate{
		ID:          "default",
		Active:      true,
		Credentials: okx.Credentials{APIKey: "key", SecretKey: "secret", Passphrase: "pass"},
	}); err != nil {
		t.Fatal(err)
	}

	body := `{"api_id":"default","inst_id":"BTC-USDT-SWAP","ord_id":"100","cl_ord_id":"client-100"}`
	start := httptest.NewRequest(http.MethodPost, "/tvbot/pending-orders/chase", strings.NewReader(body))
	start.Header.Set("Content-Type", "application/json")
	start.SetBasicAuth("admin", "Admin123")
	startRR := httptest.NewRecorder()
	srv.ServeHTTP(startRR, start)
	if startRR.Code != http.StatusAccepted {
		t.Fatalf("start chase code=%d body=%s", startRR.Code, startRR.Body.String())
	}
	stop := httptest.NewRequest(http.MethodPost, "/tvbot/pending-orders/chase/stop", strings.NewReader(body))
	stop.Header.Set("Content-Type", "application/json")
	stop.SetBasicAuth("admin", "Admin123")
	stopRR := httptest.NewRecorder()
	srv.ServeHTTP(stopRR, stop)
	if stopRR.Code != http.StatusOK {
		t.Fatalf("stop chase code=%d body=%s", stopRR.Code, stopRR.Body.String())
	}
	var resp pendingOrderChaseResponse
	if err := json.Unmarshal(stopRR.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Status != "stopped" {
		t.Fatalf("bad stop response: %#v", resp)
	}
	if amendCount != 1 || cancelCalled {
		t.Fatalf("stop should only cancel background job: amendCount=%d cancelCalled=%v", amendCount, cancelCalled)
	}
}

func TestTVBotPendingOrderChaseFallsBackToMarketAfterTimeout(t *testing.T) {
	oldInterval := pendingOrderChaseInterval
	oldTimeout := pendingOrderChaseTimeout
	oldJobs := pendingOrderChaseJobs
	pendingOrderChaseInterval = time.Hour
	pendingOrderChaseTimeout = 20 * time.Millisecond
	pendingOrderChaseJobs = newPendingOrderChaseRegistry()
	defer func() {
		pendingOrderChaseInterval = oldInterval
		pendingOrderChaseTimeout = oldTimeout
		pendingOrderChaseJobs = oldJobs
	}()

	srv := newTestServer(t)
	var mu sync.Mutex
	cancelled := false
	var cancelBodies []map[string]string
	var marketBodies []map[string]any
	okxServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v5/trade/orders-pending":
			mu.Lock()
			isCancelled := cancelled
			mu.Unlock()
			if isCancelled {
				_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[]}`))
				return
			}
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instType":"SWAP","instId":"BTC-USDT-SWAP","ordId":"100","clOrdId":"client-100","tdMode":"cross","side":"sell","posSide":"net","ordType":"limit","px":"64000","sz":"0.5","accFillSz":"0.1","state":"live","reduceOnly":"true","cTime":"1784880000000"}]}`))
		case "/api/v5/public/instruments":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instId":"BTC-USDT-SWAP","tickSz":"0.1","ctVal":"1","lotSz":"1","minSz":"1"}]}`))
		case "/api/v5/market/ticker":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instId":"BTC-USDT-SWAP","bidPx":"63999","askPx":"64001","last":"64000"}]}`))
		case "/api/v5/trade/amend-order":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"ordId":"100","clOrdId":"client-100","sCode":"0","sMsg":""}]}`))
		case "/api/v5/trade/cancel-order":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			cancelBodies = append(cancelBodies, body)
			cancelled = true
			mu.Unlock()
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"ordId":"100","clOrdId":"client-100","sCode":"0","sMsg":""}]}`))
		case "/api/v5/trade/order":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			marketBodies = append(marketBodies, body)
			mu.Unlock()
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"ordId":"200","clOrdId":"market-200","sCode":"0","sMsg":""}]}`))
		default:
			t.Fatalf("unexpected OKX path %s", r.URL.Path)
		}
	}))
	defer okxServer.Close()
	cfg := srv.ConfigStore.Get()
	cfg.Trading.BaseURL = okxServer.URL
	srv.ConfigStore = config.NewStore("", cfg)
	srv.OKXHTTPClient = okxServer.Client()
	if _, err := srv.OKXCredentials.UpdateAccount(okx.CredentialAccountUpdate{
		ID:          "default",
		Active:      true,
		Credentials: okx.Credentials{APIKey: "key", SecretKey: "secret", Passphrase: "pass"},
	}); err != nil {
		t.Fatal(err)
	}

	body := `{"api_id":"default","inst_id":"BTC-USDT-SWAP","ord_id":"100","cl_ord_id":"client-100"}`
	start := httptest.NewRequest(http.MethodPost, "/tvbot/pending-orders/chase", strings.NewReader(body))
	start.Header.Set("Content-Type", "application/json")
	start.SetBasicAuth("admin", "Admin123")
	startRR := httptest.NewRecorder()
	srv.ServeHTTP(startRR, start)
	if startRR.Code != http.StatusAccepted {
		t.Fatalf("start chase code=%d body=%s", startRR.Code, startRR.Body.String())
	}

	for i := 0; i < 100; i++ {
		mu.Lock()
		done := len(marketBodies) > 0
		mu.Unlock()
		if done {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	key := pendingOrderChaseKey(pendingOrderChaseRequest{APIID: "default", InstID: "BTC-USDT-SWAP", OrdID: "100", ClOrdID: "client-100"})
	for i := 0; i < 100 && pendingOrderChaseJobs.activeKey(key); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if pendingOrderChaseJobs.activeKey(key) {
		t.Fatal("chase job should stop after market fallback")
	}
	if len(cancelBodies) != 1 || cancelBodies[0]["instId"] != "BTC-USDT-SWAP" || cancelBodies[0]["ordId"] != "100" {
		t.Fatalf("bad cancel bodies: %#v", cancelBodies)
	}
	if len(marketBodies) != 1 {
		t.Fatalf("expected one market order, got %#v", marketBodies)
	}
	market := marketBodies[0]
	if market["instId"] != "BTC-USDT-SWAP" || market["tdMode"] != "cross" || market["side"] != "sell" || market["ordType"] != "market" || market["sz"] != "0.4" || market["reduceOnly"] != true {
		t.Fatalf("bad market fallback order: %#v", market)
	}
	if _, ok := market["attachAlgoOrds"]; ok {
		t.Fatalf("reduce-only fallback should not attach risk controls: %#v", market)
	}
	if _, ok := market["posSide"]; ok {
		t.Fatalf("net-mode market fallback should not send posSide: %#v", market)
	}
}

func TestTVBotPendingOrderChaseFallbackMarketIncludesRiskControls(t *testing.T) {
	oldInterval := pendingOrderChaseInterval
	oldTimeout := pendingOrderChaseTimeout
	oldJobs := pendingOrderChaseJobs
	pendingOrderChaseInterval = time.Hour
	pendingOrderChaseTimeout = 20 * time.Millisecond
	pendingOrderChaseJobs = newPendingOrderChaseRegistry()
	defer func() {
		pendingOrderChaseInterval = oldInterval
		pendingOrderChaseTimeout = oldTimeout
		pendingOrderChaseJobs = oldJobs
	}()

	srv := newTestServer(t)
	var mu sync.Mutex
	cancelled := false
	var marketBodies []map[string]any
	okxServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v5/trade/orders-pending":
			mu.Lock()
			isCancelled := cancelled
			mu.Unlock()
			if isCancelled {
				_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[]}`))
				return
			}
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instType":"SWAP","instId":"BTC-USDT-SWAP","ordId":"100","clOrdId":"client-100","tdMode":"cross","side":"buy","posSide":"net","ordType":"limit","px":"64000","sz":"0.5","accFillSz":"0.1","state":"live","attachAlgoOrds":[{"attachAlgoClOrdId":"client-100A","tpTriggerRatio":"0.04","slTriggerRatio":"-0.02"}],"cTime":"1784880000000"}]}`))
		case "/api/v5/public/instruments":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instId":"BTC-USDT-SWAP","tickSz":"0.1","ctVal":"1","lotSz":"1","minSz":"1"}]}`))
		case "/api/v5/market/ticker":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instId":"BTC-USDT-SWAP","bidPx":"63999","askPx":"64001","last":"64000"}]}`))
		case "/api/v5/trade/amend-order":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"ordId":"100","clOrdId":"client-100","sCode":"0","sMsg":""}]}`))
		case "/api/v5/trade/cancel-order":
			mu.Lock()
			cancelled = true
			mu.Unlock()
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"ordId":"100","clOrdId":"client-100","sCode":"0","sMsg":""}]}`))
		case "/api/v5/trade/order":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			marketBodies = append(marketBodies, body)
			mu.Unlock()
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"ordId":"200","clOrdId":"market-200","sCode":"0","sMsg":""}]}`))
		default:
			t.Fatalf("unexpected OKX path %s", r.URL.Path)
		}
	}))
	defer okxServer.Close()
	cfg := srv.ConfigStore.Get()
	cfg.Trading.BaseURL = okxServer.URL
	cfg.Trading.TakeProfitPct = 4
	cfg.Trading.StopLossPct = 2
	srv.ConfigStore = config.NewStore("", cfg)
	srv.OKXHTTPClient = okxServer.Client()
	if _, err := srv.OKXCredentials.UpdateAccount(okx.CredentialAccountUpdate{
		ID:          "default",
		Active:      true,
		Credentials: okx.Credentials{APIKey: "key", SecretKey: "secret", Passphrase: "pass"},
	}); err != nil {
		t.Fatal(err)
	}

	body := `{"api_id":"default","inst_id":"BTC-USDT-SWAP","ord_id":"100","cl_ord_id":"client-100"}`
	start := httptest.NewRequest(http.MethodPost, "/tvbot/pending-orders/chase", strings.NewReader(body))
	start.Header.Set("Content-Type", "application/json")
	start.SetBasicAuth("admin", "Admin123")
	startRR := httptest.NewRecorder()
	srv.ServeHTTP(startRR, start)
	if startRR.Code != http.StatusAccepted {
		t.Fatalf("start chase code=%d body=%s", startRR.Code, startRR.Body.String())
	}

	for i := 0; i < 100; i++ {
		mu.Lock()
		done := len(marketBodies) > 0
		mu.Unlock()
		if done {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	key := pendingOrderChaseKey(pendingOrderChaseRequest{APIID: "default", InstID: "BTC-USDT-SWAP", OrdID: "100", ClOrdID: "client-100"})
	for i := 0; i < 100 && pendingOrderChaseJobs.activeKey(key); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if pendingOrderChaseJobs.activeKey(key) {
		t.Fatal("chase job should stop after market fallback")
	}
	if len(marketBodies) != 1 {
		t.Fatalf("expected one market order, got %#v", marketBodies)
	}
	market := marketBodies[0]
	if market["instId"] != "BTC-USDT-SWAP" || market["tdMode"] != "cross" || market["side"] != "buy" || market["ordType"] != "market" || market["sz"] != "0.4" {
		t.Fatalf("bad market fallback order: %#v", market)
	}
	attach, ok := market["attachAlgoOrds"].([]any)
	if !ok || len(attach) != 1 {
		t.Fatalf("missing attach algo on market fallback: %#v", market)
	}
	first, ok := attach[0].(map[string]any)
	if !ok || first["tpTriggerRatio"] != "0.04" || first["slTriggerRatio"] != "-0.02" || first["tpOrdPx"] != "-1" || first["slOrdPx"] != "-1" {
		t.Fatalf("bad market fallback attach algo: %#v", attach)
	}
}

func TestTVBotPositionMarketCloseUsesDedicatedClosePositionEndpoint(t *testing.T) {
	srv := newTestServer(t)
	var sawClose bool
	okxServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v5/account/positions":
			if r.URL.Query().Get("instType") != "SWAP" {
				t.Fatalf("bad positions query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instType":"SWAP","instId":"BTC-USDT-SWAP","mgnMode":"cross","posId":"1","posSide":"net","pos":"2","availPos":"2","avgPx":"64000","markPx":"65000","upl":"500","uplRatio":"0.015","lever":"5","notionalUsd":"130000","margin":"26000","uTime":"1784880000000"}]}`))
		case "/api/v5/trade/close-position":
			sawClose = true
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["instId"] != "BTC-USDT-SWAP" || body["mgnMode"] != "cross" || body["autoCxl"] != true || body["clOrdId"] == "" {
				t.Fatalf("unexpected close-position request: %#v", body)
			}
			if _, ok := body["posSide"]; ok {
				t.Fatalf("net close request should not send posSide: %#v", body)
			}
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instId":"BTC-USDT-SWAP","posSide":"net","clOrdId":"close-1"}]}`))
		default:
			t.Fatalf("unexpected OKX path %s", r.URL.Path)
		}
	}))
	defer okxServer.Close()
	cfg := srv.ConfigStore.Get()
	cfg.Trading.BaseURL = okxServer.URL
	srv.ConfigStore = config.NewStore("", cfg)
	srv.OKXHTTPClient = okxServer.Client()
	if _, err := srv.OKXCredentials.UpdateAccount(okx.CredentialAccountUpdate{
		ID:     "default",
		Active: true,
		Credentials: okx.Credentials{
			APIKey:     "key",
			SecretKey:  "secret",
			Passphrase: "pass",
		},
	}); err != nil {
		t.Fatal(err)
	}

	reqBody := []byte(`{"api_id":"default","inst_id":"BTC-USDT-SWAP","pos_side":"net","mode":"market"}`)
	req := httptest.NewRequest(http.MethodPost, "/tvbot/positions/close", bytes.NewReader(reqBody))
	req.SetBasicAuth("admin", "Admin123")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("market close status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !sawClose {
		t.Fatal("expected OKX close-position request")
	}
	var resp positionCloseResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK || resp.Status != "submitted" || resp.Mode != "market" || resp.OrdID != "" || resp.ClOrdID != "close-1" || resp.Sz != "2" || resp.Px != "" {
		t.Fatalf("bad market close response: %#v", resp)
	}
}

func TestTVBotPositionMarketCloseCancelsExistingCloseOrdersWithDedicatedEndpoint(t *testing.T) {
	srv := newTestServer(t)
	var closeReq map[string]any
	okxServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v5/account/positions":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instType":"SWAP","instId":"H-USDT-SWAP","mgnMode":"isolated","posId":"1","posSide":"net","pos":"-4385.9","availPos":"4385.9"}]}`))
		case "/api/v5/trade/close-position":
			if err := json.NewDecoder(r.Body).Decode(&closeReq); err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instId":"H-USDT-SWAP","posSide":"net","clOrdId":"close-all-1"}]}`))
		default:
			t.Fatalf("unexpected OKX path %s", r.URL.Path)
		}
	}))
	defer okxServer.Close()
	cfg := srv.ConfigStore.Get()
	cfg.Trading.BaseURL = okxServer.URL
	srv.ConfigStore = config.NewStore("", cfg)
	srv.OKXHTTPClient = okxServer.Client()
	if _, err := srv.OKXCredentials.UpdateAccount(okx.CredentialAccountUpdate{ID: "default", Active: true, Credentials: okx.Credentials{APIKey: "key", SecretKey: "secret", Passphrase: "pass"}}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/tvbot/positions/close", bytes.NewReader([]byte(`{"api_id":"default","inst_id":"H-USDT-SWAP","pos_side":"net","mode":"market"}`)))
	req.SetBasicAuth("admin", "Admin123")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("market close status=%d body=%s", rr.Code, rr.Body.String())
	}
	if closeReq["instId"] != "H-USDT-SWAP" || closeReq["mgnMode"] != "isolated" || closeReq["autoCxl"] != true || closeReq["clOrdId"] == "" {
		t.Fatalf("bad close-position request: %#v", closeReq)
	}
	var resp positionCloseResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK || resp.Status != "submitted" || resp.Px != "" || resp.OrdID != "" || resp.ClOrdID != "close-all-1" || resp.Sz != "4385.9" {
		t.Fatalf("bad close-position response: %#v", resp)
	}
}

func TestTVBotPositionMarketPartialCloseUsesReduceOnlyOrder(t *testing.T) {
	oldMarketTimeout := marketClosePendingTimeout
	marketClosePendingTimeout = 0
	t.Cleanup(func() { marketClosePendingTimeout = oldMarketTimeout })
	srv := newTestServer(t)
	var orderReq map[string]any
	okxServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v5/account/positions":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instType":"SWAP","instId":"BTC-USDT-SWAP","mgnMode":"cross","posId":"1","posSide":"net","pos":"2","availPos":"2"}]}`))
		case "/api/v5/public/instruments":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instId":"BTC-USDT-SWAP","tickSz":"0.1","lotSz":"1","minSz":"1","ctVal":"1"}]}`))
		case "/api/v5/trade/order":
			if err := json.NewDecoder(r.Body).Decode(&orderReq); err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"ordId":"market-half-1","clOrdId":"close-half-1","sCode":"0","sMsg":""}]}`))
		default:
			t.Fatalf("unexpected OKX path %s", r.URL.Path)
		}
	}))
	defer okxServer.Close()
	cfg := srv.ConfigStore.Get()
	cfg.Trading.BaseURL = okxServer.URL
	srv.ConfigStore = config.NewStore("", cfg)
	srv.OKXHTTPClient = okxServer.Client()
	if _, err := srv.OKXCredentials.UpdateAccount(okx.CredentialAccountUpdate{ID: "default", Active: true, Credentials: okx.Credentials{APIKey: "key", SecretKey: "secret", Passphrase: "pass"}}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/tvbot/positions/close", bytes.NewReader([]byte(`{"api_id":"default","inst_id":"BTC-USDT-SWAP","pos_side":"net","mode":"market","ratio":0.5}`)))
	req.SetBasicAuth("admin", "Admin123")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("partial market close status=%d body=%s", rr.Code, rr.Body.String())
	}
	if orderReq["side"] != "sell" || orderReq["ordType"] != "market" || orderReq["sz"] != "1" || orderReq["reduceOnly"] != true {
		t.Fatalf("bad partial market close request: %#v", orderReq)
	}
	var resp positionCloseResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK || resp.OrdID != "market-half-1" || resp.ClOrdID != "close-half-1" || resp.Sz != "1" {
		t.Fatalf("bad partial market close response: %#v", resp)
	}
}

func TestTVBotPositionLimitCloseRetriesAfterOKX51006(t *testing.T) {
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

	srv := newTestServer(t)
	var orderReqs []map[string]any
	okxServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v5/account/positions":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instType":"SWAP","instId":"H-USDT-SWAP","mgnMode":"isolated","posId":"1","posSide":"net","pos":"-4385.9","availPos":"4385.9","avgPx":"0.0676927579398658","markPx":"0.0645","upl":"140.03117048457415","uplRatio":"0.4716542857807704","lever":"10","notionalUsd":"282.9","margin":"29.7"}]}`))
		case "/api/v5/public/instruments":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instId":"H-USDT-SWAP","ctVal":"100","tickSz":"0.0001","lotSz":"0.1","minSz":"0.1"}]}`))
		case "/api/v5/market/ticker":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instId":"H-USDT-SWAP","bidPx":"0.0643","askPx":"0.0644","last":"0.06435"}]}`))
		case "/api/v5/trade/order":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			orderReqs = append(orderReqs, body)
			if len(orderReqs) == 1 {
				if body["side"] != "buy" || body["ordType"] != "limit" || body["px"] != "0.0645" || body["sz"] != "4385.9" || body["reduceOnly"] != true {
					t.Fatalf("unexpected initial limit close: %#v", body)
				}
				_, _ = w.Write([]byte(`{"code":"1","msg":"All operations failed","data":[{"sCode":"51006","sMsg":"Order price is not within the price limit (max buy price: 0.0644, min sell price: 0.0585)"}]}`))
				return
			}
			if body["side"] != "buy" || body["ordType"] != "limit" || body["px"] != "0.0644" || body["sz"] != "4385.9" || body["reduceOnly"] != true {
				t.Fatalf("unexpected protected limit retry: %#v", body)
			}
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"ordId":"h-close-limit-2","clOrdId":"close-2","sCode":"0","sMsg":""}]}`))
		default:
			t.Fatalf("unexpected OKX path %s", r.URL.Path)
		}
	}))
	defer okxServer.Close()
	cfg := srv.ConfigStore.Get()
	cfg.Trading.BaseURL = okxServer.URL
	srv.ConfigStore = config.NewStore("", cfg)
	srv.OKXHTTPClient = okxServer.Client()
	if _, err := srv.OKXCredentials.UpdateAccount(okx.CredentialAccountUpdate{
		ID:     "default",
		Active: true,
		Credentials: okx.Credentials{
			APIKey:     "key",
			SecretKey:  "secret",
			Passphrase: "pass",
		},
	}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/tvbot/positions/close", bytes.NewReader([]byte(`{"api_id":"default","inst_id":"H-USDT-SWAP","pos_side":"net","mode":"limit"}`)))
	req.SetBasicAuth("admin", "Admin123")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("limit close status=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(orderReqs) != 2 {
		t.Fatalf("expected initial plus protected limit order, got %#v", orderReqs)
	}
	var resp positionCloseResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK || resp.Status != "running" || resp.Mode != "limit" || resp.Px != "0.0644" || resp.OrdID != "h-close-limit-2" {
		t.Fatalf("bad protected limit close response: %#v", resp)
	}
}

func TestTVBotPositionLimitCloseRatioRoundsToLotSize(t *testing.T) {
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

	srv := newTestServer(t)
	var sawOrder bool
	okxServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v5/account/positions":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instType":"SWAP","instId":"BTC-USDT-SWAP","mgnMode":"cross","posId":"1","posSide":"net","pos":"2","availPos":"2","avgPx":"64000","markPx":"65000","upl":"500","uplRatio":"0.015","lever":"5","notionalUsd":"130000","margin":"26000","uTime":"1784880000000"}]}`))
		case "/api/v5/public/instruments":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instType":"SWAP","instId":"BTC-USDT-SWAP","tickSz":"0.1","lotSz":"0.2","minSz":"0.2","ctVal":"0.01","state":"live"}]}`))
		case "/api/v5/market/ticker":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instType":"SWAP","instId":"BTC-USDT-SWAP","bidPx":"99.9","askPx":"100.1","last":"100","ts":"1784880000000"}]}`))
		case "/api/v5/trade/order":
			sawOrder = true
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["side"] != "sell" || body["ordType"] != "limit" || body["px"] != "99.9" || body["sz"] != "0.4" || body["reduceOnly"] != true {
				t.Fatalf("unexpected ratio close order: %#v", body)
			}
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"ordId":"ratio-1","clOrdId":"close-ratio","sCode":"0","sMsg":""}]}`))
		default:
			t.Fatalf("unexpected OKX path %s", r.URL.Path)
		}
	}))
	defer okxServer.Close()
	cfg := srv.ConfigStore.Get()
	cfg.Trading.BaseURL = okxServer.URL
	srv.ConfigStore = config.NewStore("", cfg)
	srv.OKXHTTPClient = okxServer.Client()
	if _, err := srv.OKXCredentials.UpdateAccount(okx.CredentialAccountUpdate{
		ID:     "default",
		Active: true,
		Credentials: okx.Credentials{
			APIKey:     "key",
			SecretKey:  "secret",
			Passphrase: "pass",
		},
	}); err != nil {
		t.Fatal(err)
	}

	reqBody := []byte(`{"api_id":"default","inst_id":"BTC-USDT-SWAP","pos_side":"net","mode":"limit","ratio":0.25}`)
	req := httptest.NewRequest(http.MethodPost, "/tvbot/positions/close", bytes.NewReader(reqBody))
	req.SetBasicAuth("admin", "Admin123")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("ratio limit close status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !sawOrder {
		t.Fatal("expected OKX ratio limit order")
	}
	var resp positionCloseResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK || resp.Status != "running" || resp.Mode != "limit" || resp.Px != "99.9" || resp.Sz != "0.4" {
		t.Fatalf("bad ratio limit close response: %#v", resp)
	}
}

func TestTVBotPositionLimitCloseReplacesAndFallsBackToMarket(t *testing.T) {
	oldPoll := positionClosePollInterval
	oldTimeout := positionCloseLimitTimeout
	oldJobs := positionCloseJobs
	positionClosePollInterval = 10 * time.Millisecond
	positionCloseLimitTimeout = 45 * time.Millisecond
	positionCloseJobs = newPositionCloseRegistry()
	t.Cleanup(func() {
		positionClosePollInterval = oldPoll
		positionCloseLimitTimeout = oldTimeout
		positionCloseJobs = oldJobs
	})

	srv := newTestServer(t)
	var mu sync.Mutex
	var orderBodies []map[string]any
	var closeBodies []map[string]any
	cancelCount := 0
	tickerCalls := 0
	okxServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v5/account/positions":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instType":"SWAP","instId":"BTC-USDT-SWAP","mgnMode":"isolated","posId":"1","posSide":"long","pos":"0.5","availPos":"0.5","avgPx":"64000","markPx":"65000","upl":"500","uplRatio":"0.015","lever":"5","notionalUsd":"32500","margin":"6500","uTime":"1784880000000"}]}`))
		case "/api/v5/public/instruments":
			if r.URL.Query().Get("instId") != "BTC-USDT-SWAP" {
				t.Fatalf("bad instrument query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instType":"SWAP","instId":"BTC-USDT-SWAP","tickSz":"0.1","lotSz":"0.1","minSz":"0.1","ctVal":"0.01","state":"live"}]}`))
		case "/api/v5/market/ticker":
			mu.Lock()
			tickerCalls++
			call := tickerCalls
			mu.Unlock()
			if r.URL.Query().Get("instId") != "BTC-USDT-SWAP" {
				t.Fatalf("bad ticker query: %s", r.URL.RawQuery)
			}
			if call == 1 {
				_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instType":"SWAP","instId":"BTC-USDT-SWAP","bidPx":"99.9","askPx":"100.1","last":"100","ts":"1784880000000"}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instType":"SWAP","instId":"BTC-USDT-SWAP","bidPx":"100.1","askPx":"100.3","last":"100.2","ts":"1784880005000"}]}`))
		case "/api/v5/trade/order":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			orderBodies = append(orderBodies, body)
			orderIndex := len(orderBodies)
			mu.Unlock()
			if body["instId"] != "BTC-USDT-SWAP" || body["tdMode"] != "isolated" || body["side"] != "sell" || body["sz"] != "0.5" || body["posSide"] != "long" {
				t.Fatalf("unexpected close order: %#v", body)
			}
			ordID := fmt.Sprintf("order-%d", orderIndex)
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"ordId":"` + ordID + `","clOrdId":"close","sCode":"0","sMsg":""}]}`))
		case "/api/v5/trade/close-position":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["instId"] != "BTC-USDT-SWAP" || body["mgnMode"] != "isolated" || body["posSide"] != "long" || body["autoCxl"] != true {
				t.Fatalf("unexpected fallback close-position request: %#v", body)
			}
			mu.Lock()
			closeBodies = append(closeBodies, body)
			mu.Unlock()
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instId":"BTC-USDT-SWAP","posSide":"long","clOrdId":"close-all"}]}`))
		case "/api/v5/trade/cancel-order":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["instId"] != "BTC-USDT-SWAP" || body["ordId"] == "" {
				t.Fatalf("unexpected cancel order: %#v", body)
			}
			mu.Lock()
			cancelCount++
			mu.Unlock()
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"ordId":"` + body["ordId"] + `","clOrdId":"close","sCode":"0","sMsg":""}]}`))
		default:
			t.Fatalf("unexpected OKX path %s", r.URL.Path)
		}
	}))
	defer okxServer.Close()
	cfg := srv.ConfigStore.Get()
	cfg.Trading.BaseURL = okxServer.URL
	srv.ConfigStore = config.NewStore("", cfg)
	srv.OKXHTTPClient = okxServer.Client()
	if _, err := srv.OKXCredentials.UpdateAccount(okx.CredentialAccountUpdate{
		ID:     "default",
		Active: true,
		Credentials: okx.Credentials{
			APIKey:     "key",
			SecretKey:  "secret",
			Passphrase: "pass",
		},
	}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/tvbot/positions/close", bytes.NewReader([]byte(`{"api_id":"default","inst_id":"BTC-USDT-SWAP","pos_side":"long","mode":"limit"}`)))
	req.SetBasicAuth("admin", "Admin123")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("limit close status=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp positionCloseResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK || resp.Status != "running" || resp.Px != "99.9" {
		t.Fatalf("bad limit close response: %#v", resp)
	}

	deadline := time.Now().Add(time.Second)
	var got []map[string]any
	var gotClose []map[string]any
	var gotCancels int
	for time.Now().Before(deadline) {
		mu.Lock()
		got = append([]map[string]any(nil), orderBodies...)
		gotClose = append([]map[string]any(nil), closeBodies...)
		gotCancels = cancelCount
		mu.Unlock()
		if len(gotClose) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(got) < 2 || got[0]["ordType"] != "limit" || got[0]["px"] != "99.9" || got[1]["ordType"] != "limit" || got[1]["px"] != "100.1" || len(gotClose) != 1 {
		t.Fatalf("expected initial limit, replacement limit, and dedicated fallback close: orders=%#v closes=%#v", got, gotClose)
	}
	if gotCancels < 2 {
		t.Fatalf("expected cancels before replacement and fallback, got %d", gotCancels)
	}
}

func TestLowMarginPositionMonitorStartsLimitClose(t *testing.T) {
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

	srv := newTestServer(t)
	var mu sync.Mutex
	var orders []map[string]any
	okxServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v5/account/positions":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[
				{"instType":"SWAP","instId":"BTC-USDT-SWAP","mgnMode":"isolated","posId":"1","posSide":"long","pos":"0.5","availPos":"0.5","avgPx":"64000","markPx":"65000","upl":"500","uplRatio":"0.015","lever":"5","notionalUsd":"32500","margin":"9.99","uTime":"1784880000000"},
				{"instType":"SWAP","instId":"ETH-USDT-SWAP","mgnMode":"isolated","posId":"2","posSide":"short","pos":"1","availPos":"1","avgPx":"2500","markPx":"2490","upl":"10","uplRatio":"0.01","lever":"5","notionalUsd":"2490","margin":"10","uTime":"1784880000000"},
				{"instType":"SWAP","instId":"DOGE-USDT-SWAP","mgnMode":"isolated","posId":"3","posSide":"long","pos":"100","availPos":"100","avgPx":"0.2","markPx":"0.21","upl":"1","uplRatio":"0.01","lever":"5","notionalUsd":"21","margin":"","uTime":"1784880000000"}
			]}`))
		case "/api/v5/public/instruments":
			if r.URL.Query().Get("instId") != "BTC-USDT-SWAP" {
				t.Fatalf("low margin monitor should only query BTC instrument, got %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instType":"SWAP","instId":"BTC-USDT-SWAP","tickSz":"0.1","lotSz":"0.1","minSz":"0.1","ctVal":"0.01","state":"live"}]}`))
		case "/api/v5/market/ticker":
			if r.URL.Query().Get("instId") != "BTC-USDT-SWAP" {
				t.Fatalf("bad ticker query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instType":"SWAP","instId":"BTC-USDT-SWAP","bidPx":"99.9","askPx":"100.1","last":"100","ts":"1784880000000"}]}`))
		case "/api/v5/trade/order":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			orders = append(orders, body)
			mu.Unlock()
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"ordId":"low-margin-1","clOrdId":"close","sCode":"0","sMsg":""}]}`))
		default:
			t.Fatalf("unexpected OKX path %s", r.URL.Path)
		}
	}))
	defer okxServer.Close()
	cfg := srv.ConfigStore.Get()
	cfg.Trading.BaseURL = okxServer.URL
	srv.ConfigStore = config.NewStore("", cfg)
	srv.OKXHTTPClient = okxServer.Client()
	if _, err := srv.OKXCredentials.UpdateAccount(okx.CredentialAccountUpdate{
		ID:     "default",
		Active: true,
		Credentials: okx.Credentials{
			APIKey:     "key",
			SecretKey:  "secret",
			Passphrase: "pass",
		},
	}); err != nil {
		t.Fatal(err)
	}

	srv.closeLowMarginPositions(context.Background())
	mu.Lock()
	defer mu.Unlock()
	if len(orders) != 1 {
		t.Fatalf("expected exactly one low margin close order, got %#v", orders)
	}
	got := orders[0]
	if got["instId"] != "BTC-USDT-SWAP" || got["ordType"] != "limit" || got["side"] != "sell" || got["px"] != "99.9" || got["sz"] != "0.5" || got["posSide"] != "long" {
		t.Fatalf("unexpected low margin close order: %#v", got)
	}
}

func TestAutoProfitPositionMonitorStartsLimitClose(t *testing.T) {
	oldPoll := autoProfitClosePollInterval
	oldTimeout := autoProfitCloseLimitTimeout
	oldJobs := positionCloseJobs
	autoProfitClosePollInterval = time.Hour
	autoProfitCloseLimitTimeout = time.Hour
	positionCloseJobs = newPositionCloseRegistry()
	t.Cleanup(func() {
		autoProfitClosePollInterval = oldPoll
		autoProfitCloseLimitTimeout = oldTimeout
		positionCloseJobs = oldJobs
	})

	srv := newTestServer(t)
	var orders []map[string]any
	okxServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v5/account/positions":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[
				{"instType":"SWAP","instId":"BTC-USDT-SWAP","mgnMode":"isolated","posSide":"long","pos":"0.5","avgPx":"100","markPx":"101","upl":"1","uplRatio":"0.051","lever":"5","margin":"100"},
				{"instType":"SWAP","instId":"ETH-USDT-SWAP","mgnMode":"isolated","posSide":"long","pos":"1","avgPx":"100","markPx":"101","upl":"10","uplRatio":"0.05","lever":"5","margin":"100"}
			]}`))
		case "/api/v5/public/instruments":
			if r.URL.Query().Get("instId") != "BTC-USDT-SWAP" {
				t.Fatalf("auto-profit monitor should only query BTC instrument, got %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instType":"SWAP","instId":"BTC-USDT-SWAP","tickSz":"0.1","lotSz":"0.1","minSz":"0.1","ctVal":"0.01","state":"live"}]}`))
		case "/api/v5/market/ticker":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instType":"SWAP","instId":"BTC-USDT-SWAP","bidPx":"99.9","askPx":"100.1","last":"100","ts":"1784880000000"}]}`))
		case "/api/v5/trade/order":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			orders = append(orders, body)
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"ordId":"profit-close-1","clOrdId":"close","sCode":"0","sMsg":""}]}`))
		default:
			t.Fatalf("unexpected OKX path %s", r.URL.Path)
		}
	}))
	defer okxServer.Close()
	cfg := srv.ConfigStore.Get()
	cfg.Trading.BaseURL = okxServer.URL
	srv.ConfigStore = config.NewStore("", cfg)
	srv.OKXHTTPClient = okxServer.Client()
	if _, err := srv.OKXCredentials.UpdateAccount(okx.CredentialAccountUpdate{
		ID:     "default",
		Active: true,
		Credentials: okx.Credentials{
			APIKey:     "key",
			SecretKey:  "secret",
			Passphrase: "pass",
		},
	}); err != nil {
		t.Fatal(err)
	}

	srv.closeProfitablePositions(context.Background())
	if len(orders) != 1 {
		t.Fatalf("expected one profitable position close order, got %#v", orders)
	}
	got := orders[0]
	if got["instId"] != "BTC-USDT-SWAP" || got["ordType"] != "limit" || got["side"] != "sell" || got["px"] != "99.9" || got["sz"] != "0.5" || got["posSide"] != "long" {
		t.Fatalf("unexpected profitable position close order: %#v", got)
	}
}

func TestPositionReturnRatioPrefersExchangeUPLRatio(t *testing.T) {
	if got, ok := positionReturnRatio(okx.Position{UplRatio: "0.06", Upl: "1", Margin: "100"}); !ok || got != 0.06 {
		t.Fatalf("exchange upl ratio = %v, %v; want 0.06, true", got, ok)
	}
	if got, ok := positionReturnRatio(okx.Position{UplRatio: "bad", Upl: "10", Margin: "200"}); !ok || got != 0.05 {
		t.Fatalf("fallback return ratio = %v, %v; want 0.05, true", got, ok)
	}
}

func TestCancelOKXPositionProtectionOrdersMatchesPositionSide(t *testing.T) {
	var canceled []okx.CancelAlgoOrderRequest
	okxServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v5/trade/orders-algo-pending":
			if r.URL.Query().Get("ordType") == "conditional" {
				_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[
					{"instId":"BTC-USDT-SWAP","algoId":"tp-long","posSide":"long","side":"sell","ordType":"conditional","tpTriggerPx":"110"},
					{"instId":"BTC-USDT-SWAP","algoId":"sl-long","posSide":"long","side":"sell","ordType":"conditional","slTriggerPx":"90"},
					{"instId":"BTC-USDT-SWAP","algoId":"tp-short","posSide":"short","side":"buy","ordType":"conditional","tpTriggerPx":"90"},
					{"instId":"ETH-USDT-SWAP","algoId":"tp-other","posSide":"long","side":"sell","ordType":"conditional","tpTriggerPx":"110"}
				]}`))
				return
			}
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[]}`))
		case "/api/v5/trade/cancel-algos":
			if err := json.NewDecoder(r.Body).Decode(&canceled); err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"algoId":"tp-long","sCode":"0"},{"algoId":"sl-long","sCode":"0"}]}`))
		default:
			t.Fatalf("unexpected OKX path %s", r.URL.Path)
		}
	}))
	defer okxServer.Close()
	client := okx.Client{BaseURL: okxServer.URL, Credentials: okx.Credentials{APIKey: "key", SecretKey: "secret", Passphrase: "pass"}, HTTPClient: okxServer.Client()}
	if err := cancelOKXPositionProtectionOrders(context.Background(), client, okx.Position{InstID: "BTC-USDT-SWAP", PosSide: "long", Pos: "1"}); err != nil {
		t.Fatal(err)
	}
	if len(canceled) != 2 || canceled[0].AlgoID != "tp-long" || canceled[1].AlgoID != "sl-long" {
		t.Fatalf("canceled protection orders = %#v", canceled)
	}
}

func TestCancelOKXPositionPendingOrdersCancelsOnlySameInstrument(t *testing.T) {
	var canceled []okx.CancelOrderRequest
	okxServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v5/trade/orders-pending":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[
				{"instId":"BTC-USDT-SWAP","ordId":"btc-entry","clOrdId":"btc-entry-client","side":"buy","ordType":"limit","sz":"1","accFillSz":"0","state":"live"},
				{"instId":"BTC-USDT-SWAP","ordId":"btc-reduce","clOrdId":"btc-reduce-client","side":"sell","ordType":"limit","sz":"1","accFillSz":"0","state":"live","reduceOnly":true},
				{"instId":"ETH-USDT-SWAP","ordId":"eth-entry","clOrdId":"eth-entry-client","side":"buy","ordType":"limit","sz":"1","accFillSz":"0","state":"live"}
			]}`))
		case "/api/v5/trade/cancel-order":
			var req okx.CancelOrderRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			canceled = append(canceled, req)
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"sCode":"0","sMsg":""}]}`))
		default:
			t.Fatalf("unexpected OKX path %s", r.URL.Path)
		}
	}))
	defer okxServer.Close()

	client := okx.Client{BaseURL: okxServer.URL, Credentials: okx.Credentials{APIKey: "key", SecretKey: "secret", Passphrase: "pass"}, HTTPClient: okxServer.Client()}
	if err := cancelOKXPositionPendingOrders(context.Background(), client, okx.Position{InstID: "BTC-USDT-SWAP", PosSide: "net", Pos: "0"}); err != nil {
		t.Fatal(err)
	}
	if len(canceled) != 2 || canceled[0].InstID != "BTC-USDT-SWAP" || canceled[0].OrdID != "btc-entry" || canceled[1].OrdID != "btc-reduce" {
		t.Fatalf("canceled pending orders = %#v", canceled)
	}
}

func TestTVBotAPIKeysTestUsesSelectedStoredAccount(t *testing.T) {
	var seenAPIKey string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v5/account/balance":
			seenAPIKey = r.Header.Get("OK-ACCESS-KEY")
			if r.URL.RawQuery != "" {
				t.Fatalf("balance should request all assets, got query: %s", r.URL.RawQuery)
			}
			if r.Header.Get("x-simulated-trading") != "1" {
				t.Fatal("missing demo header")
			}
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"totalEq":"1001.25","details":[{"ccy":"USDT","eq":"999.75","availEq":"888.5","availBal":"887.5","cashBal":"900","frozenBal":"11.25","uTime":"1784886000000"}]}]}`))
		case "/api/v5/public/instruments":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[]}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer ts.Close()

	srv := newTestServer(t)
	cfg := srv.ConfigStore.Get()
	cfg.Trading.BaseURL = ts.URL
	srv.ConfigStore = config.NewStore("", cfg)
	if _, err := srv.OKXCredentials.UpdateAccount(okx.CredentialAccountUpdate{
		ID: "main",
		Credentials: okx.Credentials{
			APIKey:     "main-key",
			SecretKey:  "main-secret",
			Passphrase: "main-pass",
		},
		Active: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.OKXCredentials.UpdateAccount(okx.CredentialAccountUpdate{
		ID: "backup",
		Credentials: okx.Credentials{
			APIKey:     "backup-key",
			SecretKey:  "backup-secret",
			Passphrase: "backup-pass",
		},
	}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/tvbot/api-keys/test", bytes.NewReader([]byte(`{"id":"backup"}`)))
	req.SetBasicAuth("admin", "Admin123")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if seenAPIKey != "backup-key" {
		t.Fatalf("used api key %q", seenAPIKey)
	}
	if !bytes.Contains(rr.Body.Bytes(), []byte(`"api_id":"backup"`)) {
		t.Fatalf("response missing api id: %s", rr.Body.String())
	}
	var resp struct {
		Found   bool `json:"usdt_balance_found"`
		Balance struct {
			Ccy             string `json:"ccy"`
			Equity          string `json:"eq"`
			AvailableEquity string `json:"avail_eq"`
			FrozenBalance   string `json:"frozen_bal"`
			UpdateTime      string `json:"u_time"`
		} `json:"usdt_balance"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Found || resp.Balance.Ccy != "USDT" || resp.Balance.AvailableEquity != "888.5" || resp.Balance.Equity != "999.75" || resp.Balance.FrozenBalance != "11.25" || resp.Balance.UpdateTime != "1784886000000" {
		t.Fatalf("bad usdt balance: %#v", resp)
	}
}

func TestUpgradeEndpointRequiresAdminAndStartsRunner(t *testing.T) {
	srv := newTestServer(t)
	runner := fakeUpgradeRunner{done: make(chan struct{}, 1)}
	srv.Upgrade = upgrade.NewManager(runner)

	unauth := httptest.NewRecorder()
	srv.ServeHTTP(unauth, httptest.NewRequest(http.MethodPost, "/upgrade", nil))
	if unauth.Code != http.StatusUnauthorized {
		t.Fatalf("upgrade without auth code=%d", unauth.Code)
	}

	req := httptest.NewRequest(http.MethodPost, "/upgrade", nil)
	req.SetBasicAuth("admin", "Admin123")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("upgrade start code=%d body=%s", rr.Code, rr.Body.String())
	}
	select {
	case <-runner.done:
	case <-time.After(time.Second):
		t.Fatal("upgrade runner was not called")
	}

	statusReq := httptest.NewRequest(http.MethodGet, "/upgrade", nil)
	statusReq.SetBasicAuth("admin", "Admin123")
	status := httptest.NewRecorder()
	srv.ServeHTTP(status, statusReq)
	if status.Code != http.StatusOK {
		t.Fatalf("upgrade status code=%d body=%s", status.Code, status.Body.String())
	}
	if !bytes.Contains(status.Body.Bytes(), []byte(`"status":"succeeded"`)) {
		t.Fatalf("expected succeeded status, got %s", status.Body.String())
	}
}

func newTestServer(t *testing.T) *Server {
	t.Helper()
	cfg := config.Default()
	dir := t.TempDir()
	cfg.DataFile = filepath.Join(dir, "orders.json")
	cfg.DatabaseFile = filepath.Join(dir, "tvbot.db")
	orderStore, err := storage.NewSQLiteOrderStore(cfg.DatabaseFile, cfg.DataFile)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = orderStore.Close() })
	credentialStore, err := okx.NewCredentialStore(filepath.Join(dir, "okx-credentials.json"), okx.Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	binanceCredentialStore, err := binance.NewCredentialStore(filepath.Join(dir, "binance-credentials.json"), binance.Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{
		ConfigStore:        config.NewStore("", cfg),
		Orders:             orderStore,
		Token:              security.NewTokenService("unit-test-secret"),
		Executor:           fakeExecutor{calls: make(chan trading.Signal, 2)},
		OKXCredentials:     credentialStore,
		BinanceCredentials: binanceCredentialStore,
		AdminToken:         "admin",
		AdminUser:          "admin",
		AdminPass:          "Admin123",
		Upgrade:            upgrade.NewManager(fakeUpgradeRunner{done: make(chan struct{}, 1)}),
		BuildInfo:          BuildInfo{CommitTime: "2026-07-24T03:00:00Z", CommitHash: "testhash", CommitBranch: "testbranch"},
		Now: func() time.Time {
			return time.Date(2026, 7, 24, 3, 0, 0, 0, time.UTC)
		},
	}
	seedTestMarketScopeCatalog(t, orderStore, srv.now())
	return srv
}

func seedTestMarketScopeCatalog(t *testing.T, store *storage.OrderStore, now time.Time) {
	t.Helper()
	bases := []string{"BTC", "ETH", "BNB", "SOL", "DOGE", "ESPORTS", "ETHFI", "SYRUP", "AAA", "BAD", "ZZZ", "PARTIAL", "SECOND", "EETH", "1000PEPE"}
	resp := emptySymbolsResponse(map[string]config.SymbolConfig{})
	for i, base := range bases {
		turnover := trading.NormalizeFloat(float64(len(bases) - i))
		okxView := symbolInstrument{
			Instrument:      okx.Instrument{InstType: "SWAP", InstID: base + "-USDT-SWAP", BaseCcy: base, QuoteCcy: "USDT", SettleCcy: "USDT", State: "live"},
			TurnoverUSDT24h: turnover,
		}
		binanceView := binanceSymbolInstrument{
			SymbolInfo:      binance.SymbolInfo{Symbol: base + "USDT", Pair: base + "USDT", ContractType: "PERPETUAL", Status: "TRADING", BaseAsset: base, QuoteAsset: "USDT", MarginAsset: "USDT"},
			TurnoverUSDT24h: turnover,
		}
		resp.OKX.Live.Instruments = append(resp.OKX.Live.Instruments, okxView)
		resp.OKX.Demo.Instruments = append(resp.OKX.Demo.Instruments, okxView)
		resp.Binance.Live.Instruments = append(resp.Binance.Live.Instruments, binanceView)
		resp.Binance.Demo.Instruments = append(resp.Binance.Demo.Instruments, binanceView)
	}
	resp.OKX.Live.Count = len(resp.OKX.Live.Instruments)
	resp.OKX.Demo.Count = len(resp.OKX.Demo.Instruments)
	resp.Binance.Live.Count = len(resp.Binance.Live.Instruments)
	resp.Binance.Demo.Count = len(resp.Binance.Demo.Instruments)
	applyMarketScopeRankings(&resp, config.DefaultMarketTurnoverScope)
	items, err := symbolCatalogCacheItems(resp, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertSymbolCatalogCaches(items); err != nil {
		t.Fatal(err)
	}
}

func installOKXRetryTicker(t *testing.T, srv *Server, instID, bidPx, askPx, lastPx string) {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v5/market/ticker" {
			t.Fatalf("unexpected OKX retry ticker path %s", r.URL.Path)
		}
		if r.URL.Query().Get("instId") != instID {
			t.Fatalf("bad OKX retry ticker query: %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instType":"SWAP","instId":"` + instID + `","bidPx":"` + bidPx + `","askPx":"` + askPx + `","last":"` + lastPx + `","ts":"1784880000000"}]}`))
	}))
	t.Cleanup(ts.Close)
	cfg := srv.ConfigStore.Get()
	cfg.Trading.BaseURL = ts.URL
	srv.ConfigStore = config.NewStore("", cfg)
	srv.OKXHTTPClient = ts.Client()
}

func waitOrderStatus(t *testing.T, store *storage.OrderStore, signalID string, want storage.OrderStatus) storage.OrderRecord {
	t.Helper()
	var last storage.OrderRecord
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		for _, rec := range store.List(50) {
			if rec.SignalID != signalID {
				continue
			}
			last = rec
			if rec.Status == want {
				return rec
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("order %s did not reach status %s, last=%#v", signalID, want, last)
	return storage.OrderRecord{}
}

func validSignal(t *testing.T, srv *Server) trading.Signal {
	t.Helper()
	tp := trading.NewFlexibleFloat(2)
	sl := trading.NewFlexibleFloat(1)
	signal := trading.Signal{
		Action:   trading.ActionLong,
		Coinpair: "BTC",
		Price:    trading.NewFlexibleFloat(50000),
		SentAt:   "2026-07-24T03:00:00Z",
		Ticker:   "BTCUSDT",
		Leverage: 5,
		Amount:   trading.NewFlexibleFloat(100),
		Risk:     trading.Risk{Type: trading.RiskTPSL, TPPct: &tp, SLPct: &sl},
	}
	signal.Normalize()
	signal.Token = srv.Token.Generate(signal.CanonicalTokenPayload())
	return signal
}

func cloneValues(values url.Values) url.Values {
	out := url.Values{}
	for key, list := range values {
		for _, value := range list {
			out.Add(key, value)
		}
	}
	return out
}
