package server

import (
	"strings"
	"testing"
)

func TestUnifiedNavigationUsesRequestedOrder(t *testing.T) {
	page := RenderTVBotNav(defaultTopMenu(), func(path string) string { return path }, func(TopMenuItem) bool { return false }, "wyx", "/auth/logout")
	if !strings.Contains(page, `<nav class="app-nav tvbot-nav"`) {
		t.Fatal("TVBot navigation should use the dedicated single-row layout")
	}
	want := []string{
		`href="/alerts"`,
		`href="/bubbles"`,
		`href="/liquidations"`,
		`href="/hedge-wall"`,
		`href="/market-info"`,
		`href="/options"`,
		`href="/tvbot/view/dashboard"`,
		`href="/tvbot/view/positions"`,
		`href="/tvbot/view/analysis"`,
		`href="/tvbot/view/symbols"`,
		`href="/tvbot/view/config"`,
		`href="/tvbot/view/api-keys"`,
		`href="/tvbot/view/order-settings"`,
		`href="/tvbot/view/template"`,
		`href="/tvbot/view/orders"`,
		`href="/tvbot/view/trade-monitor"`,
		`href="/tvbot/view/upgrade"`,
		`href="/system"`,
	}
	last := -1
	for _, marker := range want {
		at := strings.Index(page, marker)
		if at <= last {
			t.Fatalf("navigation marker %q is out of order: index=%d last=%d page=%s", marker, at, last, page)
		}
		last = at
	}
	if !strings.Contains(page, `>升级</a>`) {
		t.Fatal("upgrade should remain a direct top-navigation link")
	}
	if !strings.Contains(page, `>系统信息<span class="nav-system-caret"`) {
		t.Fatal("system menu trigger is missing")
	}
}
