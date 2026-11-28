package server

import (
	"strings"
	"testing"
)

func TestUnifiedNavigationUsesRequestedOrder(t *testing.T) {
	page := RenderUnifiedNav(defaultTopMenu(), func(path string) string { return path }, func(TopMenuItem) bool { return false }, "wyx", "/auth/logout")
	if !strings.Contains(page, `<nav class="app-nav" aria-label="主菜单" data-unified-menu="true">`) {
		t.Fatal("TVBot navigation should use the alerts page navigation layout")
	}
	for _, retired := range []string{"/tvbot/view/", "tvbot-nav", "tvbot-menu-row"} {
		if strings.Contains(page, retired) {
			t.Fatalf("retired TVBot navigation marker leaked: %s", retired)
		}
	}
	topMenuStart := strings.Index(page, `<div class="app-links">`)
	if topMenuStart < 0 {
		t.Fatalf("top navigation links container is missing: page=%s", page)
	}
	topMenuEnd := strings.Index(page[topMenuStart:], `</div>`)
	if topMenuEnd < 0 {
		t.Fatalf("top navigation links container is missing: page=%s", page)
	}
	topMenu := page[topMenuStart : topMenuStart+topMenuEnd]
	want := []string{
		`href="/alerts"`,
		`href="/bubbles"`,
		`href="/liquidations"`,
		`href="/hedge-wall"`,
		`href="/market-info"`,
		`href="/options"`,
		`data-tab="orders" href="/orders"`,
		`data-tab="positions" href="/positions"`,
		`data-tab="analysis" href="/analysis"`,
		`data-tab="historyOrders" href="/historyOrders">历史订单`,
		`data-tab="stopLossAnalysis" href="/stopLossAnalysis">止盈止损分析`,
		`data-tab="symbols" href="/symbols"`,
		`data-tab="config" href="/oderConfig"`,
		`data-tab="apiKeys" href="/apiKeys"`,
		`data-tab="orderSettings" href="/orderSettings"`,
		`data-tab="template" href="/template"`,
	}
	last := -1
	for _, marker := range want {
		at := strings.Index(topMenu, marker)
		if at <= last {
			t.Fatalf("top navigation marker %q is out of order: index=%d last=%d page=%s", marker, at, last, page)
		}
		last = at
	}
	systemPanelStart := strings.Index(page, `<div class="nav-system-panel"`)
	if systemPanelStart < 0 {
		t.Fatal("system menu panel is missing")
	}
	systemPanel := page[systemPanelStart:]
	if strings.Contains(systemPanel, "/tvbot/view/") {
		t.Fatalf("system submenu contains a retired TVBot route: %s", systemPanel)
	}
	systemWant := []string{
		`href="/admin">用户管理`,
		`href="/menu">菜单管理`,
		`data-tab="tradeMonitor" href="/tradeMonitor">成交监听`,
		`href="/system">交易所信息`,
		`data-tab="upgrade" href="/upgradePage">升级`,
	}
	last = -1
	for _, marker := range systemWant {
		at := strings.Index(systemPanel, marker)
		if at <= last {
			t.Fatalf("system submenu marker %q is out of order: index=%d last=%d page=%s", marker, at, last, page)
		}
		last = at
	}
	if !strings.Contains(page, `>系统信息<span class="nav-system-caret"`) {
		t.Fatal("system menu trigger is missing")
	}
	if !strings.Contains(page, `id="nav-theme" data-theme-toggle="true"`) {
		t.Fatal("theme toggle should be marked for delegated mobile handling")
	}
}

func TestTVBotNavigationMatchesAlertsNavigation(t *testing.T) {
	link := func(path string) string { return path }
	active := func(item TopMenuItem) bool { return item.Key == "positions" }
	alertsPage := RenderUnifiedNav(defaultTopMenu(), link, active, "wyx", "/auth/logout")
	tvbotPage := RenderUnifiedNav(defaultTopMenu(), link, active, "wyx", "/auth/logout")
	if tvbotPage != alertsPage {
		t.Fatalf("TVBot navigation differs from alerts navigation:\nalerts=%s\ntvbot=%s", alertsPage, tvbotPage)
	}
}
