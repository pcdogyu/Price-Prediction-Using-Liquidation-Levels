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
		`data-tab="orders" href="/tvbot?tab=orders"`,
		`data-tab="positions" href="/tvbot?tab=positions"`,
		`data-tab="analysis" href="/tvbot?tab=analysis"`,
		`data-tab="symbols" href="/tvbot?tab=symbols"`,
		`data-tab="config" href="/tvbot?tab=config"`,
		`data-tab="apiKeys" href="/tvbot?tab=apiKeys"`,
		`data-tab="orderSettings" href="/tvbot?tab=orderSettings"`,
		`data-tab="template" href="/tvbot?tab=template"`,
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
	for _, marker := range []string{
		`data-tab="tradeMonitor" href="/tvbot?tab=tradeMonitor">成交监听`,
		`data-tab="upgrade" href="/tvbot?tab=upgrade">升级`,
	} {
		if !strings.Contains(systemPanel, marker) {
			t.Fatalf("system submenu is missing restored TVBot item %q: %s", marker, systemPanel)
		}
	}
	if !strings.Contains(page, `>系统信息<span class="nav-system-caret"`) {
		t.Fatal("system menu trigger is missing")
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
