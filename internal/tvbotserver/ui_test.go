package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRenderTVBotHTMLUsesOneUnifiedHeader(t *testing.T) {
	req := httptest.NewRequest("GET", "/tvbot/view/positions", nil)
	req.Header.Set("X-Forwarded-Prefix", "/liquidation")
	page := renderTVBotHTML(BuildInfo{CommitHash: "test"}, req)

	if strings.Contains(page, "{{UNIFIED_NAV}}") || strings.Contains(page, "{{TVBOT_TOP_MENU}}") || strings.Contains(page, "{{LIQUIDATION_LINKS}}") || strings.Contains(page, "merged-liquidation-nav") {
		t.Fatalf("tvbot page contains an unrendered or legacy second navigation")
	}
	nav := strings.Index(page, `<nav class="app-nav tvbot-nav" aria-label="主菜单">`)
	links := strings.Index(page, `<div class="tvbot-menu-row"><div class="app-links tvbot-top-menu">`)
	controls := strings.Index(page, `<div class="header-controls">`)
	if nav < 0 || links < 0 || links < nav || controls < 0 || controls < links {
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
		`<nav class="app-nav tvbot-nav" aria-label="主菜单">`,
		`<div class="app-links tvbot-top-menu">`,
		`data-system-menu="true"`,
		`class="nav-system-trigger header-action"`,
		`交易所信息`,
		`href="/liquidation/alerts"`,
		`data-tab="dashboard" href="/liquidation/tvbot/view/dashboard"`,
		`data-tab="positions" href="/liquidation/tvbot/view/positions"`,
		`href="/liquidation/admin">用户管理`,
		`data-tab="apiKeys" href="/liquidation/tvbot/view/api-keys"`,
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
		`window.confirm("确认一键平掉当前 "`,
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("unified header missing %q", want)
		}
	}
	saveButton := strings.Index(page, `id="save-api-keys"`)
	testButton := strings.Index(page, `id="test-api-keys"`)
	addButton := strings.Index(page, `id="add-api-key"`)
	apiList := strings.Index(page, `id="api-key-accounts"`)
	operationHeader := strings.Index(page, `<th>操作</th>`)
	deleteButton := strings.Index(page, `data-delete-api-id=`)
	apiIDControl := strings.Index(page, `class="api-id-control"`)
	if saveButton < 0 || testButton < 0 || addButton < 0 || apiList < 0 || operationHeader < 0 || deleteButton < 0 || apiIDControl >= 0 || !(testButton < saveButton && saveButton < addButton) || !(operationHeader < apiList && apiList < deleteButton) {
		t.Fatalf("API key controls should put per-row delete in the rightmost list column and test/save before add: list=%d operation=%d delete=%d oldControl=%d save=%d test=%d add=%d", apiList, operationHeader, deleteButton, apiIDControl, saveButton, testButton, addButton)
	}
	for _, want := range []string{
		`function fillAPIForm(id, preserveCredentials)`,
		`renderAPIKeys(true);`,
		`const apiKey = preserveCredentials ? $("key-api").value : "";`,
		`const secretKey = preserveCredentials ? $("key-secret").value : "";`,
		`function testSavedAPIKey(exchange, env, id)`,
		`apiKeyTestEnv`,
		`apiKeyTestStale`,
		`apiKeyTestError`,
		`zeroTime(status.updated_at) ? "-" : status.updated_at`,
		`data-delete-api-id=`,
		`async function deleteAPIKey(apiID)`,
		`button.dataset.deleteApiId`,
		`colspan="5"`,
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("API key form should preserve entered credentials after saving; missing %q", want)
		}
	}
	saveStart := strings.Index(page, "async function saveAPIKeys()")
	testStart := strings.Index(page, "async function testAPIKeys()")
	if saveStart < 0 || testStart <= saveStart {
		t.Fatalf("API key save/test functions are missing or out of order: save=%d test=%d", saveStart, testStart)
	}
	saveFunction := page[saveStart:testStart]
	for _, want := range []string{
		`await testSavedAPIKey(exchange, env, state.selectedAPIID);`,
		`state.apiKeyTestStale = !!state.apiKeyTest;`,
		`余额已刷新`,
		`余额刷新失败`,
	} {
		if !strings.Contains(saveFunction, want) {
			t.Fatalf("API key save flow missing %q", want)
		}
	}
	if strings.Contains(saveFunction, `state.apiKeyTest = null`) {
		t.Fatal("API key save flow should preserve the previous balance result until saved credentials are retested")
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

func TestRenderTVBotHTMLShowsCurrentUserBeforeLogout(t *testing.T) {
	req := httptest.NewRequest("GET", "/tvbot/view/dashboard", nil)
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

func TestRenderTVBotHTMLAppliesThemeAssetsToEveryViewRoute(t *testing.T) {
	for _, item := range defaultTopMenu() {
		tab := topMenuTab(item)
		if tab == "" {
			continue
		}
		req := httptest.NewRequest("GET", tvbotViewPath(tab), nil)
		page := renderTVBotHTML(BuildInfo{CommitHash: "test"}, req)
		if !strings.Contains(page, `src="/assets/theme.js"`) || !strings.Contains(page, `href="/assets/theme.css"`) {
			t.Fatalf("view %s is missing shared theme assets", tab)
		}
		menuItem := strings.Index(page, `data-tab="`+tab+`"`)
		menuItemEnd := menuItem + 240
		if menuItemEnd > len(page) {
			menuItemEnd = len(page)
		}
		if menuItem < 0 || !strings.Contains(page[menuItem:menuItemEnd], `aria-current="page"`) {
			t.Fatalf("view %s is not marked active in the unified menu", tab)
		}
	}
}

func TestRenderTVBotHTMLUsesConfiguredTopMenu(t *testing.T) {
	req := httptest.NewRequest("GET", "/tvbot/view/dashboard", nil)
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
		`data-tab="dashboard" href="/liquidation/tvbot/view/dashboard" aria-current="page">资金`,
		`data-tab="system" href="/liquidation/system">交易所信息`,
		`href="/liquidation/menu">菜单管理`,
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("configured top menu missing %q", want)
		}
	}
	if strings.Contains(page, `data-tab="menuSettings"`) || strings.Contains(page, `href="/liquidation/tvbot/view/menu-settings"`) {
		t.Fatal("configured top menu should not expose the legacy tvbot menu settings entry")
	}
}

func TestRenderTVBotDashboardUsesFundingPeriods(t *testing.T) {
	page := renderTVBotHTML(BuildInfo{CommitHash: "test"}, httptest.NewRequest("GET", "/tvbot/view/dashboard", nil))
	for _, want := range []string{
		`<h2>资金</h2>`,
		`后台每 15 分钟采集一次三家交易所余额并生成数据点`,
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
		`const dashboardBalanceRefreshIntervalMs = 15 * 60 * 1000`,
		`function startDashboardBalanceAutoRefresh()`,
		`function refreshDashboardBalanceOverviewAuto()`,
		`function setDashboardBalanceWindow(minutes)`,
		`loadBalanceOverview(false, parsed)`,
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
