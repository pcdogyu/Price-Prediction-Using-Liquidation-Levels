package server

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRenderTVBotHTMLUsesOneUnifiedHeader(t *testing.T) {
	req := httptest.NewRequest("GET", "/tvbot#positions", nil)
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
	for _, want := range []string{
		`class="app-logo" aria-hidden="true"`,
		`href="/liquidation/alerts"`,
		`href="/liquidation/tvbot#positions"`,
		`href="/liquidation/admin"`,
		`class="theme-toggle" id="tvbot-theme"`,
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("unified header missing %q", want)
		}
	}
	if strings.Contains(page, "清算墙雷达") || strings.Contains(page, `class="brand"`) || strings.Contains(page, `class="liquidation-links"`) {
		t.Fatal("tvbot header still contains the removed product name or legacy navigation classes")
	}
}
