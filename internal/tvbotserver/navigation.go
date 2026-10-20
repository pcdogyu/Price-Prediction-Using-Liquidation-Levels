package server

import (
	"html"
	"strings"
)

// RenderTopMenuLinks renders the non-administrative links for the shared top
// navigation. The administrative links are intentionally rendered by
// RenderSystemMenu as one compact dropdown.
func RenderTopMenuLinks(items []TopMenuItem, link func(string) string, active func(TopMenuItem) bool) string {
	var out strings.Builder
	for _, item := range items {
		if isSystemMenuItem(item) || !validTopMenuItem(item) {
			continue
		}
		renderTopMenuLink(&out, item, link, active(item))
	}
	return out.String()
}

// RenderSystemMenu renders the compact system dropdown shared by the market
// and trading pages. The system-info route is shown as 交易所信息 because that
// page contains the exchange connection and account information.
func RenderSystemMenu(items []TopMenuItem, link func(string) string, active func(TopMenuItem) bool) string {
	systemItems := make([]TopMenuItem, 0, 4)
	for _, item := range items {
		if isSystemMenuItem(item) && validTopMenuItem(item) {
			systemItems = append(systemItems, item)
		}
	}
	if len(systemItems) == 0 {
		return ""
	}
	var out strings.Builder
	activeGroup := false
	for _, item := range systemItems {
		if active(item) {
			activeGroup = true
			break
		}
	}
	out.WriteString(`<div class="nav-system-menu" data-system-menu="true">`)
	out.WriteString(`<button type="button" class="nav-system-trigger" aria-haspopup="menu" aria-expanded="false"`)
	if activeGroup {
		out.WriteString(` aria-current="page"`)
	}
	out.WriteString(`>系统信息<span class="nav-system-caret" aria-hidden="true">⌄</span></button>`)
	out.WriteString(`<div class="nav-system-panel" role="menu" hidden>`)
	for _, item := range systemItems {
		label := item.Title
		if item.Key == "system-info" || item.Href == "/system" {
			label = "交易所信息"
		}
		out.WriteString(`<a role="menuitem"`)
		if tab := topMenuTab(item); tab != "" {
			out.WriteString(` data-tab="` + html.EscapeString(tab) + `"`)
		}
		out.WriteString(` href="` + html.EscapeString(link(item.Href)) + `"`)
		if active(item) {
			out.WriteString(` aria-current="page"`)
		}
		out.WriteString(`>` + html.EscapeString(label) + `</a>`)
	}
	out.WriteString(`</div></div>`)
	return out.String()
}

func isSystemMenuItem(item TopMenuItem) bool {
	switch item.Key {
	case "system-info", "menu", "system", "upgrade":
		return true
	}
	switch item.Href {
	case "/system", "/menu", "/admin", "/upgrade", "/tvbot/view/upgrade":
		return true
	default:
		return false
	}
}

func validTopMenuItem(item TopMenuItem) bool {
	return strings.TrimSpace(item.Href) != "" && strings.HasPrefix(item.Href, "/")
}

func renderTopMenuLink(out *strings.Builder, item TopMenuItem, link func(string) string, active bool) {
	out.WriteString(`<a`)
	if tab := topMenuTab(item); tab != "" {
		out.WriteString(` data-tab="` + html.EscapeString(tab) + `"`)
	}
	out.WriteString(` href="` + html.EscapeString(link(item.Href)) + `"`)
	if active {
		out.WriteString(` aria-current="page"`)
	}
	out.WriteString(`>` + html.EscapeString(item.Title) + `</a>`)
}
