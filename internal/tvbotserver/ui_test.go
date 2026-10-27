package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRenderTVBotHTMLUsesOneUnifiedHeader(t *testing.T) {
	req := httptest.NewRequest("GET", "/tvbot", nil)
	req.Header.Set("X-Forwarded-Prefix", "/liquidation")
	page := renderTVBotHTML(BuildInfo{CommitHash: "test"}, req)

	if strings.Contains(page, "{{UNIFIED_NAV}}") || strings.Contains(page, "{{TVBOT_TOP_MENU}}") || strings.Contains(page, "{{LIQUIDATION_LINKS}}") || strings.Contains(page, "merged-liquidation-nav") {
		t.Fatalf("tvbot page contains an unrendered or legacy second navigation")
	}
	nav := strings.Index(page, `<nav class="app-nav" aria-label="主菜单" data-unified-menu="true">`)
	links := strings.Index(page, `<div class="app-links">`)
	systemMenu := strings.Index(page, `<div class="nav-system-menu"`)
	if nav < 0 || links < 0 || links < nav || systemMenu < 0 || systemMenu < links {
		t.Fatalf("unified navigation is missing or out of order: nav=%d links=%d", nav, links)
	}
	if strings.Contains(page, "tvbot-sections") || strings.Contains(page, "liquidation-links") {
		t.Fatal("tvbot page contains stale navigation layout selectors")
	}
	main := strings.Index(page, "<main>")
	pageToolbar := strings.Index(page, `class="tvbot-page-toolbar"`)
	exchangeSwitch := strings.Index(page, `role="group" aria-label="全局交易所"`)
	if main < 0 || pageToolbar < main || exchangeSwitch < pageToolbar {
		t.Fatalf("exchange switch should be inside the page content: main=%d toolbar=%d switch=%d", main, pageToolbar, exchangeSwitch)
	}
	for _, want := range []string{
		`<nav class="app-nav" aria-label="主菜单" data-unified-menu="true">`,
		`<div class="app-links">`,
		`data-system-menu="true"`,
		`class="nav-system-trigger header-action"`,
		`交易所信息`,
		`href="/liquidation/alerts"`,
		`href="/liquidation/admin">用户管理`,
		`href="/liquidation/admin"`,
		`id="nav-theme"`,
		`id="nav-logs" type="button">程序日志</button>`,
		`id="shared-logs"`,
		`method="post" action="/liquidation/auth/logout"><button type="submit">退出</button></form>`,
		`data-global-exchange="okx" aria-selected="true"`,
		`data-global-exchange="binance"`,
		`id="close-all-positions"`,
		`async function closeAllPositions()`,
		`await submitPositionClose(body);`,
		`throw new Error(localizeErrorMessage(msg));`,
		`平仓数量低于最小步长，请提高平仓比例或选择全部平仓`,
		`平仓数量 " + minimum[1] + " 低于最小数量 " + minimum[2]`,
		`id="position-monitor-gate-enabled"`,
		`gate_enabled: $("position-monitor-gate-enabled").checked`,
		`formatAveragePriceAmount(row, row.avgPx)`,
		`function rawDecimalPrecision(value)`,
		`const markPrecision = normalizedPrecision(row && row.price_precision)`,
		`const rawMarkPrecision = rawDecimalPrecision(row && row.markPx)`,
		`if (markPrecision !== null) return formatFixedPrecision(value, markPrecision, formatNumber)`,
		`window.confirm("确认一键平掉当前 "`,
		`return !!$(tabID) && (!button || !button.hidden);`,
		`if ((!button || button.hidden || !section) && routeTab !== target)`,
		`new URL("tvbot", new URL(appBase, window.location.origin))`,
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("unified header missing %q", want)
		}
	}
	if strings.Contains(page, `href="/liquidation/tvbot/view/dashboard"`) {
		t.Fatal("funding dashboard should not be exposed in the top navigation")
	}
	if strings.Contains(page, `/liquidation/tvbot/view/`) {
		t.Fatal("retired TVBot view routes should not be exposed in the top navigation")
	}
	saveButton := strings.Index(page, `id="save-api-keys"`)
	testButton := strings.Index(page, `id="test-api-keys"`)
	environmentSelect := strings.Index(page, `id="api-key-environment"`)
	accountName := strings.Index(page, `<label>账户名称<input id="key-name"`)
	if saveButton < 0 || testButton < 0 || strings.Contains(page, `id="add-api-key"`) || strings.Contains(page, `id="key-selected"`) || strings.Contains(page, `id="api-key-accounts"`) || strings.Contains(page, `data-delete-api-id=`) || strings.Contains(page, `data-rename-active-api-id=`) || strings.Contains(page, `id="api-key-environment-tabs"`) || strings.Contains(page, `data-api-key-env=`) || environmentSelect < 0 || accountName < 0 || !(environmentSelect < accountName) || !(testButton < saveButton) || !strings.Contains(page, `id="save-api-keys" disabled`) {
		t.Fatalf("API key page should expose one account form with environment select before account name: env=%d name=%d save=%d test=%d", environmentSelect, accountName, saveButton, testButton)
	}
	for _, want := range []string{
		`function fillAPIForm(id, preserveCredentials)`,
		`renderAPIKeys(true);`,
		`function apiKeySaveBody(exchange, env, active)`,
		`async function autoSaveAPIKeyDeactivation()`,
		`active: active`,
		`已取消交易 API，状态已自动保存`,
		`const apiKey = preserveCredentials ? $("key-api").value : "";`,
		`const secretKey = preserveCredentials ? $("key-secret").value : "";`,
		`apiKeyTestEnv`,
		`apiKeyTestStale`,
		`apiKeyTestError`,
		`apiKeyTestFingerprint`,
		`apiKeyTestHasNormalBalance`,
		`apiKeyTestMatchesCurrentForm`,
		`updateAPIKeySaveState`,
		`zeroTime(status.updated_at) ? "-" : status.updated_at`,
		`function allExchangesUnconfiguredForCurrentEnvironment()`,
		`const allowUnconfigured = allExchangesUnconfiguredForCurrentEnvironment();`,
		`const disabled = state.exchangeAvailabilityReady && !configured && !allowUnconfigured;`,
		`function exchangeSelectionAllowed(exchange)`,
		`if (!exchangeSelectionAllowed(selected))`,
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("API key form is missing single-account test gate marker %q", want)
		}
	}
	saveStart := strings.Index(page, "async function saveAPIKeys()")
	testStart := strings.Index(page, "async function testAPIKeys()")
	if saveStart < 0 || testStart <= saveStart {
		t.Fatalf("API key save/test functions are missing or out of order: save=%d test=%d", saveStart, testStart)
	}
	saveFunction := page[saveStart:testStart]
	for _, want := range []string{
		`if (!apiKeyTestMatchesCurrentForm())`,
		`请先测试 API，并确认返回有效的 USDT 余额`,
	} {
		if !strings.Contains(saveFunction, want) {
			t.Fatalf("API key save flow missing %q", want)
		}
	}
	if !strings.Contains(page, `if (id === "key-active" && !node.checked)`) || !strings.Contains(page, `autoSaveAPIKeyDeactivation().catch`) {
		t.Fatal("unchecking the trading API checkbox should trigger an automatic save")
	}
	if strings.Contains(page, "清算墙雷达") || strings.Contains(page, `class="brand"`) || strings.Contains(page, `class="liquidation-links"`) || strings.Contains(page, `class="app-logo"`) {
		t.Fatal("tvbot header still contains the removed product name or legacy navigation classes")
	}
	for _, removed := range []string{"tvbot#", "location.hash", "history.replaceState", "url.hash ="} {
		if strings.Contains(page, removed) {
			t.Fatalf("tvbot page still contains hash navigation marker %q", removed)
		}
	}
}

func TestRenderTVBotAnalysisAddsGateTradeChartEntryColumn(t *testing.T) {
	page := renderTVBotHTML(BuildInfo{CommitHash: "test"}, httptest.NewRequest("GET", "/tvbot", nil))
	entryColumn := strings.Index(page, `{ id: "entry_time", title: "开仓时间"`)
	exitColumn := strings.Index(page, `{ id: "exit_time", title: "平仓时间"`)
	if entryColumn < 0 || exitColumn < 0 || entryColumn >= exitColumn {
		t.Fatalf("analysis columns should put entry time before exit time: entry=%d exit=%d", entryColumn, exitColumn)
	}
	for _, want := range []string{
		`function analysisGateChartSymbol(instID)`,
		`function analysisTradeSymbolHTML(row)`,
		`exchange !== "gate" && exchange !== "binance"`,
		`查看 Binance 买卖点 K 线`,
		`data-analysis-chart-key=`,
		`tvbot.analysisChart.`,
		`url.searchParams.set("trade_ref", token)`,
		`url.searchParams.set("exchange", normalizedExchange)`,
		`function buildPositionChartURL(symbol, exchange, trades, selectedRow)`,
		`async function openStandalonePositionKline(options)`,
		`openStandalonePositionKline({`,
		`initialURL.searchParams.set("standalone", "1")`,
		`window.open(initialURL.toString(), "_blank")`,
		`function analysisTimeText(v)`,
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("Gate trade chart integration marker missing %q", want)
		}
	}
	analysisStart := strings.Index(page, "function openAnalysisTradeChart(row)")
	if analysisStart < 0 {
		t.Fatal("analysis trade chart handler is missing")
	}
	analysisHandler := page[analysisStart:]
	if strings.Contains(analysisHandler, "openSharedPositionKlineDialog({") || !strings.Contains(analysisHandler, "openStandalonePositionKline({") {
		t.Fatal("analysis symbol details must use the standalone position K-line page")
	}
}

func TestRenderTVBotPositionsAddsSymbolDetails(t *testing.T) {
	page := renderTVBotHTML(BuildInfo{CommitHash: "test"}, httptest.NewRequest("GET", "/tvbot", nil))
	for _, want := range []string{
		`cell: (row) => positionSymbolCell(row)`,
		`function positionSymbolCell(row)`,
		`data-position-symbol-details="true"`,
		`id="position-symbol-dialog"`,
		`id="position-symbol-chart-frame"`,
		`K线 / 开平仓信息`,
		`function positionSymbolTradeRows(row, includeAnalysis)`,
		`includeAnalysis !== false`,
		`state.analysis.trades`,
		`filter((item) => String(item && (item.trade_id || item.ord_id) || "").trim())`,
		`tradeChartPayloadRow(item)`,
		`operation_source: String(item && item.operation_source || "").trim().toLowerCase()`,
		`function buildPositionChartURL(symbol, exchange, trades, selectedRow)`,
		`function analysisPositionKlineURL()`,
		`base + "/alerts/chart"`,
		`url.searchParams.set("position", "1")`,
		`initialURL.searchParams.set("standalone", "1")`,
		`window.open(initialURL.toString(), "_blank")`,
		`chartWindow.location.href = chartURL.toString()`,
		`exchange: String(item && item.exchange || "").trim() ? normalizeExchange(item && item.exchange) : ""`,
		`async function loadPositionSymbolAnalysis()`,
		`async function openPositionSymbolDetails(row)`,
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("position symbol details marker missing %q", want)
		}
	}
	if strings.Contains(page, `trade_id: "position-entry"`) {
		t.Fatal("position chart must not synthesize a marker from the current position average price")
	}
	for _, removed := range []string{"position-symbol-history-panel", "position-symbol-history-rows", "历史下单信息"} {
		if strings.Contains(page, removed) {
			t.Fatalf("position symbol details should not render the right-side history panel marker %q", removed)
		}
	}
	positionsStart := strings.Index(page, "const positionTableColumnDefs = [")
	positionsEnd := strings.Index(page, "const pendingOrderTableColumnDefs = [")
	if positionsStart < 0 || positionsEnd <= positionsStart {
		t.Fatal("position table column definitions are missing")
	}
	if strings.Contains(page[positionsStart:positionsEnd], `cell: (row) => textTableCell(displayInstID(row.instId))`) {
		t.Fatal("position symbol column should no longer render as plain text")
	}
	positionOpen := strings.Index(page, "async function openPositionSymbolDetails(row)")
	if positionOpen < 0 {
		t.Fatal("position symbol detail handler is missing")
	}
	positionClose := strings.Index(page[positionOpen:], "\n    function closePositionSymbolDetails()")
	if positionClose < 0 {
		t.Fatal("position symbol detail handler end is missing")
	}
	positionHandler := page[positionOpen : positionOpen+positionClose]
	if strings.Contains(positionHandler, "openSharedPositionKlineDialog({") || !strings.Contains(positionHandler, `window.open(initialURL.toString(), "_blank")`) {
		t.Fatal("position symbol details must open a standalone K-line page")
	}
	if strings.Contains(page, `function buildTradeChartURL(`) || strings.Contains(page, `function buildTradeChartPayloadURL(`) || strings.Contains(page, `function analysisBubblesURL(`) {
		t.Fatal("analysis and positions must use one shared K-line builder instead of the legacy second chart path")
	}
}

func TestRenderTVBotPositionsFormatsFractionalEpochSeconds(t *testing.T) {
	page := renderTVBotHTML(BuildInfo{CommitHash: "test"}, httptest.NewRequest("GET", "/tvbot", nil))
	for _, want := range []string{
		`const epoch = Number(raw);`,
		`Math.abs(epoch) < 1e12 ? epoch * 1000 : epoch`,
		`timeZone: "Asia/Shanghai"`,
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("pending-order timestamps should format fractional epoch seconds, missing %q", want)
		}
	}
}

func TestRenderTVBotPositionsShowsOverallReturn(t *testing.T) {
	page := renderTVBotHTML(BuildInfo{CommitHash: "test"}, httptest.NewRequest("GET", "/tvbot", nil))
	metricsStart := strings.Index(page, `<div class="analysis-metrics position-metrics">`)
	if metricsStart < 0 {
		t.Fatalf("position summary metrics are missing: start=%d", metricsStart)
	}
	metricsEnd := strings.Index(page[metricsStart:], `</div>
      <div class="symbol-table-wrap">`)
	if metricsStart < 0 || metricsEnd < 0 {
		t.Fatalf("position summary metrics are missing: start=%d end=%d", metricsStart, metricsEnd)
	}
	metrics := page[metricsStart : metricsStart+metricsEnd]
	labels := []string{"持仓数", "名义价值", "未实现盈亏", "收益率", "更新时间"}
	previous := -1
	for _, label := range labels {
		index := strings.Index(metrics, `<div class="label">`+label+`</div>`)
		if index < 0 || index <= previous {
			t.Fatalf("position summary labels should be ordered %v: label=%q index=%d previous=%d", labels, label, index, previous)
		}
		previous = index
	}
	for _, want := range []string{
		".position-metrics {",
		"grid-template-columns: repeat(5, minmax(0, 1fr));",
		`id="positions-return"`,
		`function positionOverallReturnRatio(rows)`,
		`return totalUpl / totalNotional;`,
		`$("positions-return").className = ["value", signedToneClass(overallReturn)]`,
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("position overall return marker missing %q", want)
		}
	}
}

func TestRenderTVBotHTMLShowsCurrentUserBeforeLogout(t *testing.T) {
	req := httptest.NewRequest("GET", "/tvbot", nil)
	req.Header.Set("X-Forwarded-Prefix", "/liquidation")
	page := (&Server{
		BuildInfo: BuildInfo{CommitHash: "test"},
		CurrentUser: func(*http.Request) (string, bool) {
			return `operator<>`, true
		},
	}).renderTVBotHTML(req)

	want := `<span class="nav-user" aria-label="当前登录用户">operator&lt;&gt;</span>`
	if !strings.Contains(page, want) {
		t.Fatalf("tvbot page should show the escaped current user: %q", want)
	}
	userIndex := strings.Index(page, want)
	logoutIndex := strings.Index(page, `method="post" action="/liquidation/auth/logout"`)
	if userIndex < 0 || logoutIndex <= userIndex {
		t.Fatalf("current user should appear immediately before logout: user=%d logout=%d", userIndex, logoutIndex)
	}
}

func TestTVBotThemeStylesUseSemanticSurfaceColors(t *testing.T) {
	for _, forbidden := range []string{
		"background: #fff;",
		"background: #f8fafc;",
		"background: #f7faff;",
		"background: #fff8eb;",
		"background: #111827;",
	} {
		if strings.Contains(tvbotHTML, forbidden) {
			t.Fatalf("tvbot inline styles contain a fixed light/dark surface %q", forbidden)
		}
	}
	for _, want := range []string{
		"background: var(--control-bg);",
		"background: var(--panel);",
		"background: var(--chart-bg);",
		"background: var(--warning-bg);",
	} {
		if !strings.Contains(tvbotHTML, want) {
			t.Fatalf("tvbot inline styles missing theme-aware surface %q", want)
		}
	}
}

func TestRenderTVBotHTMLAppliesThemeAssetsToRemainingEntryRoutes(t *testing.T) {
	for _, path := range []string{"/tvbot", "/system"} {
		page := renderTVBotHTML(BuildInfo{CommitHash: "test"}, httptest.NewRequest("GET", path, nil))
		if !strings.Contains(page, `src="/assets/theme.js"`) || !strings.Contains(page, `href="/assets/theme.css"`) {
			t.Fatalf("route %s is missing shared theme assets", path)
		}
	}
}

func TestRenderTVBotHTMLUsesConfiguredTopMenu(t *testing.T) {
	req := httptest.NewRequest("GET", "/tvbot", nil)
	req.Header.Set("X-Forwarded-Prefix", "/liquidation")
	page := renderTVBotHTMLWithMenu(BuildInfo{CommitHash: "test"}, req, []TopMenuItem{
		{Key: "overview", Title: "资金", Href: "/tvbot/view/dashboard"},
		{Key: "system-info", Title: "系统信息", Href: "/system"},
		{Key: "menu", Title: "菜单管理", Href: "/menu"},
	})
	if strings.Count(page, `aria-label="主菜单"`) != 1 {
		t.Fatalf("configured tvbot page should have one top menu: %s", page)
	}
	for _, want := range []string{
		`data-tab="system" href="/liquidation/system">交易所信息`,
		`href="/liquidation/menu">菜单管理`,
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("configured top menu missing %q", want)
		}
	}
	if strings.Contains(page, `href="/liquidation/tvbot/view/dashboard"`) {
		t.Fatal("funding dashboard should not be exposed in the configured top navigation")
	}
	if strings.Contains(page, `data-tab="menuSettings"`) || strings.Contains(page, `href="/liquidation/tvbot/view/menu-settings"`) {
		t.Fatal("configured top menu should not expose the legacy tvbot menu settings entry")
	}
}

func TestRenderTVBotOrderHistoryLocalizesExchangeErrors(t *testing.T) {
	page := renderTVBotHTML(BuildInfo{CommitHash: "test"}, httptest.NewRequest("GET", "/tvbot", nil))
	for _, want := range []string{
		`function orderHistoryMarketScope(errorDetails)`,
		`function orderHistoryErrorText(order)`,
		`outside_market_(top5|top10|top20|top50|top100|top200|top500|all)`,
		`const scopeLabel = marketScope === "all" ? "全部符合条件" : "成交额" + orderTurnoverScopeLabel(marketScope)`,
		`范围内（outside_market_" + marketScope + "）`,
		`当前没有 " + positionText + " 持仓，无法执行平仓（position_not_open）`,
		`Gate 返回数据格式异常：订单创建时间字段类型不匹配`,
		`有效数字位数超过 12 位（INVALID_PARAM_VALUE）`,
		`const sourceExchange = order.source_exchange ? exchangeLabel(order.source_exchange) : "-";`,
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("order history localization marker missing %q", want)
		}
	}
}

func TestRenderTVBotDashboardUsesFundingPeriods(t *testing.T) {
	page := renderTVBotHTML(BuildInfo{CommitHash: "test"}, httptest.NewRequest("GET", "/tvbot", nil))
	for _, want := range []string{
		`<h2>资金</h2>`,
		`后台每 5 分钟采集一次三家交易所余额并生成权益数据点（Gate 含持仓浮盈浮亏）`,
		`历史订单统计`,
		`<div class="label">已平仓净盈亏</div>`,
		`id="overview-okx-net-pnl"`,
		`id="overview-binance-net-pnl"`,
		`id="overview-gate-net-pnl"`,
		`id="overview-gate-win-rate"`,
		`id="overview-gate-profit-factor"`,
		`id="overview-gate-payoff-ratio"`,
		`id="overview-gate-trades"`,
		`id="overview-gate-turnover"`,
		`id="overview-gate-fees"`,
		`id="overview-gate-position-count"`,
		`id="overview-gate-position-upl"`,
		`dashboardAnalysis: null`,
		`function loadDashboardOrderAnalysis(refresh)`,
		`function renderDashboardOrderStats()`,
		`const positionSummary = analysisPositionSummary(exchange);`,
		`positionSummary.ready ? analysisAmountText(positionSummary.upl) : "-"`,
		`const exchangeResult = response && Array.isArray(response.exchanges)`,
		`pnl_minutes: String(pnlMinutes)`,
		`id="dashboard-balance-window-label">24h</div>`,
		`aria-label="资金回看周期"`,
		`data-dashboard-balance-minutes="60">1h</button>`,
		`data-dashboard-balance-minutes="240">4h</button>`,
		`data-dashboard-balance-minutes="480">8h</button>`,
		`data-dashboard-balance-minutes="720">12h</button>`,
		`data-dashboard-balance-minutes="1440">24h</button>`,
		`data-dashboard-balance-minutes="2880">2d</button>`,
		`data-dashboard-balance-minutes="4320">3d</button>`,
		`data-dashboard-balance-minutes="10080">7d</button>`,
		`dashboardBalanceWindowMinutes: 1440`,
		`const dashboardBalanceRefreshIntervalMs = 5 * 60 * 1000`,
		`function startDashboardBalanceAutoRefresh()`,
		`function refreshDashboardBalanceOverviewAuto()`,
		`function setDashboardBalanceWindow(minutes)`,
		`loadBalanceOverview(false, parsed)`,
		`loadDashboardOrderAnalysis(false)`,
		`.dashboard-balance-grid .mini-usdt-chart`,
		`height: 358px`,
		`height: 343px`,
		`chart-axis-title`,
		`涨跌幅 (%)`,
		`估值 (USDT)`,
		`const pad = { left: 64, right: 72, top: 18, bottom: 58 }`,
		`chart-axis-value-right`,
		`function chartAxisPercentValue(value, baseline)`,
		`const baselineValue = chartPoints[0].value`,
		`chart-zero-line`,
		`0.00%`,
		`const positiveColor = "#138a55"`,
		`const negativeColor = "#d64545"`,
		`.dashboard-balance-grid .analysis-metrics.symbol-metrics`,
		`.dashboard-balance-period-row .balance-window-toolbar`,
		`flex: 0 0 auto`,
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("dashboard funding period marker missing %q", want)
		}
	}
}

func TestRenderTVBotSharesLivePositionSnapshotAcrossTabs(t *testing.T) {
	page := renderTVBotHTML(BuildInfo{CommitHash: "test"}, httptest.NewRequest("GET", "/tvbot", nil))
	for _, want := range []string{
		`function positionSnapshot(results)`,
		`const snapshot = positionSnapshot(results);`,
		`syncActivePositionSnapshot(snapshot);`,
		`syncAnalysisPositionSnapshot(snapshot);`,
		`state.positions = positionSnapshot([result]);`,
		`state.analysisPositions = positionSnapshot(exchanges);`,
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("positions and order analysis should share one live snapshot, missing %q", want)
		}
	}
}

func TestTVBotSystemInfoSeparatesDashboardBalance(t *testing.T) {
	page := renderTVBotHTML(BuildInfo{CommitHash: "test"}, httptest.NewRequest("GET", "/system", nil))
	dashboardStart := strings.Index(page, `<section id="dashboard"`)
	systemStart := strings.Index(page, `<section id="system"`)
	if dashboardStart < 0 || systemStart < 0 || dashboardStart >= systemStart {
		t.Fatalf("dashboard and system sections are missing or out of order: dashboard=%d system=%d", dashboardStart, systemStart)
	}
	dashboard := page[dashboardStart:systemStart]
	for _, removed := range []string{`id="dashboard-config"`, `id="system-config"`, `id="check-okx"`, `id="okx-output"`} {
		if strings.Contains(dashboard, removed) {
			t.Fatalf("dashboard should not contain system information marker %s", removed)
		}
	}
	for _, want := range []string{`id="overview-okx-eq"`, `id="overview-binance-eq"`, `id="system-config"`, `id="check-okx"`, `id="okx-output"`} {
		if !strings.Contains(page, want) {
			t.Fatalf("tvbot page missing %s", want)
		}
	}
}

func TestBuildInfoFooterUsesShanghaiTimeAndShortHash(t *testing.T) {
	footer := (BuildInfo{
		CommitTime:   "2026-10-19T05:25:53Z",
		CommitHash:   "13b939e48a0992717f7f82356692136b4c815650",
		CommitBranch: "binance",
	}).FooterText()
	const want = "Code by Yuhao@jiansutech.com - 2026-10-19 13:25:53 - 4c815650 - binance"
	if footer != want {
		t.Fatalf("unexpected footer: got %q, want %q", footer, want)
	}
}
