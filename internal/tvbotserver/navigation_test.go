package server

import (
	"strings"
	"testing"
)

func TestUnifiedNavigationUsesRequestedOrder(t *testing.T) {
	page := RenderTVBotNav(defaultTopMenu(), func(path string) string { return path }, func(TopMenuItem) bool { return false }, "wyx", "/auth/logout")
	if !strings.Contains(page, `<nav class="app-nav" aria-label="主菜单" data-unified-menu="true">`) {
		t.Fatal("TVBot navigation should use the alerts page navigation layout")
	}
	if strings.Contains(page, "tvbot-nav") || strings.Contains(page, "tvbot-menu-row") {
		t.Fatal("TVBot navigation should not use a page-specific layout")
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
		`href="/tvbot/view/orders"`,
		`href="/tvbot/view/positions"`,
		`href="/tvbot/view/analysis"`,
		`href="/tvbot/view/symbols"`,
		`href="/tvbot/view/config"`,
		`href="/tvbot/view/api-keys"`,
		`href="/tvbot/view/order-settings"`,
		`href="/tvbot/view/template"`,
	}
	last := -1
	for _, marker := range want {
		at := strings.Index(topMenu, marker)
		if at <= last {
			t.Fatalf("top navigation marker %q is out of order: index=%d last=%d page=%s", marker, at, last, page)
		}
		last = at
	}
	if strings.Contains(topMenu, `href="/tvbot/view/trade-monitor"`) || strings.Contains(topMenu, `href="/tvbot/view/upgrade"`) {
		t.Fatal("trade monitor and upgrade should not be direct top-navigation links")
	}
	if strings.Contains(page, `href="/tvbot/view/dashboard"`) {
		t.Fatal("funding dashboard should not be exposed in the top navigation")
	}
	systemPanelStart := strings.Index(page, `<div class="nav-system-panel"`)
	if systemPanelStart < 0 {
		t.Fatal("system menu panel is missing")
	}
	systemPanel := page[systemPanelStart:]
	for _, marker := range []string{
		`href="/tvbot/view/trade-monitor">成交监听</a>`,
		`href="/tvbot/view/upgrade">升级</a>`,
	} {
		if !strings.Contains(systemPanel, marker) {
			t.Fatalf("system submenu is missing %q: page=%s", marker, page)
		}
	}
	if !strings.Contains(page, `href="/tvbot/view/orders">信号</a>`) {
		t.Fatal("signal should be shown immediately before positions")
	}
	if !strings.Contains(page, `>系统信息<span class="nav-system-caret"`) {
		t.Fatal("system menu trigger is missing")
	}
}

func TestTVBotNavigationMatchesAlertsNavigation(t *testing.T) {
	link := func(path string) string { return path }
	active := func(item TopMenuItem) bool { return item.Key == "positions" }
	alertsPage := RenderUnifiedNav(defaultTopMenu(), link, active, "wyx", "/auth/logout")
	tvbotPage := RenderTVBotNav(defaultTopMenu(), link, active, "wyx", "/auth/logout")
	if tvbotPage != alertsPage {
		t.Fatalf("TVBot navigation differs from alerts navigation:\nalerts=%s\ntvbot=%s", alertsPage, tvbotPage)
	}
}
