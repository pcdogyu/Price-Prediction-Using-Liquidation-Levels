package server

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRenderTVBotHTMLUsesOneUnifiedHeader(t *testing.T) {
	req := httptest.NewRequest("GET", "/tvbot/view/positions", nil)
	req.Header.Set("X-Forwarded-Prefix", "/liquidation")
	page := renderTVBotHTML(BuildInfo{CommitHash: "test"}, req)

	if strings.Count(page, "<header") != 1 {
		t.Fatalf("tvbot page should have one header, got %d", strings.Count(page, "<header"))
	}
	if strings.Contains(page, "{{TVBOT_TOP_MENU}}") || strings.Contains(page, "{{LIQUIDATION_LINKS}}") || strings.Contains(page, "merged-liquidation-nav") {
		t.Fatalf("tvbot page contains an unrendered or legacy second navigation")
	}
	brand := strings.Index(page, `class="app-brand"`)
	links := strings.Index(page, `class="app-links tvbot-sections tvbot-top-menu"`)
	controls := strings.Index(page, `class="header-controls"`)
	if brand < 0 || links < 0 || controls < 0 || !(brand < links && links < controls) {
		t.Fatalf("unified header order is wrong: brand=%d links=%d controls=%d", brand, links, controls)
	}
	main := strings.Index(page, "<main>")
	pageToolbar := strings.Index(page, `class="tvbot-page-toolbar"`)
	exchangeSwitch := strings.Index(page, `role="group" aria-label="全局交易所"`)
	if main < 0 || pageToolbar < main || exchangeSwitch < pageToolbar {
		t.Fatalf("exchange switch should be inside the page content: main=%d toolbar=%d switch=%d", main, pageToolbar, exchangeSwitch)
	}
	for _, want := range []string{
		`class="app-logo" aria-hidden="true"`,
		`class="app-links tvbot-sections tvbot-top-menu" data-unified-menu="true" aria-label="主菜单"`,
		`.app-nav.tvbot-nav .tvbot-sections a`,
		`href="/liquidation/alerts"`,
		`data-tab="dashboard" href="/liquidation/tvbot/view/dashboard"`,
		`data-tab="positions" href="/liquidation/tvbot/view/positions"`,
		`href="/liquidation/admin">用户管理`,
		`data-tab="apiKeys" href="/liquidation/tvbot/view/api-keys"`,
		`href="/liquidation/admin"`,
		`class="theme-toggle" id="tvbot-theme"`,
		`data-global-exchange="okx" aria-selected="true"`,
		`data-global-exchange="binance"`,
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("unified header missing %q", want)
		}
	}
	if strings.Contains(page, "清算墙雷达") || strings.Contains(page, `class="brand"`) || strings.Contains(page, `class="liquidation-links"`) {
		t.Fatal("tvbot header still contains the removed product name or legacy navigation classes")
	}
	for _, removed := range []string{"tvbot#", "location.hash", "history.replaceState", "url.hash ="} {
		if strings.Contains(page, removed) {
			t.Fatalf("tvbot page still contains hash navigation marker %q", removed)
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
		`data-tab="system" href="/liquidation/system">系统信息`,
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
		`function setDashboardBalanceWindow(minutes)`,
		`loadBalanceOverview(false, parsed)`,
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
