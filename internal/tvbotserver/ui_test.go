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
	if strings.Contains(page, "{{LIQUIDATION_LINKS}}") || strings.Contains(page, "merged-liquidation-nav") {
		t.Fatalf("tvbot page contains an unrendered or legacy second navigation")
	}
	brand := strings.Index(page, `class="app-brand"`)
	links := strings.Index(page, `class="app-links"`)
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
		`class="tvbot-sections" aria-label="sections"`,
		`.app-nav.tvbot-nav .tvbot-sections a`,
		`href="/liquidation/alerts"`,
		`href="/liquidation/tvbot/view/dashboard">总览`,
		`href="/liquidation/tvbot/view/positions">持仓`,
		`href="/liquidation/admin">用户管理`,
		`href="/liquidation/tvbot/view/api-keys">API Key`,
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
