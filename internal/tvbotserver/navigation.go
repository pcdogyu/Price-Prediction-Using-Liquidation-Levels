package server

import (
	"html"
	"strings"
)

// RenderTopMenuLinks renders the main links for the shared top navigation.
// Account-management links are intentionally rendered by RenderSystemMenu as
// one compact dropdown; the upgrade page remains directly accessible.
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
	out.WriteString(`<button type="button" class="nav-system-trigger header-action" aria-haspopup="menu" aria-expanded="false"`)
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

// RenderUnifiedNav renders the complete authenticated navigation used by all
// dashboard and TVBot pages. Keeping the controls here prevents individual
// page templates from drifting apart.
func RenderUnifiedNav(items []TopMenuItem, link func(string) string, active func(TopMenuItem) bool, username, logoutPath string) string {
	return renderUnifiedNav("", items, link, active, username, logoutPath)
}

// RenderTVBotNav is kept as a compatibility wrapper for callers that render
// TVBot pages. Every authenticated page now uses the same navigation markup
// and responsive behavior as the alerts page.
func RenderTVBotNav(items []TopMenuItem, link func(string) string, active func(TopMenuItem) bool, username, logoutPath string) string {
	return RenderUnifiedNav(items, link, active, username, logoutPath)
}

func renderUnifiedNav(classSuffix string, items []TopMenuItem, link func(string) string, active func(TopMenuItem) bool, username, logoutPath string) string {
	var out strings.Builder
	tvbotLayout := classSuffix == " tvbot-nav"
	out.WriteString(`<nav class="app-nav` + classSuffix + `" aria-label="主菜单">`)
	if tvbotLayout {
		out.WriteString(`<div class="tvbot-menu-row"><div class="app-links tvbot-top-menu">`)
	} else {
		out.WriteString(`<div class="app-links">`)
	}
	out.WriteString(RenderTopMenuLinks(items, link, active))
	if tvbotLayout {
		out.WriteString(`</div></div><div class="header-controls">`)
	} else {
		out.WriteString(`</div>`)
	}
	out.WriteString(RenderSystemMenu(items, link, active))
	out.WriteString(`<button id="nav-theme" type="button">浅色主题</button><button id="nav-logs" type="button">程序日志</button>`)
	if username = strings.TrimSpace(username); username != "" {
		out.WriteString(`<span class="nav-user" aria-label="当前登录用户">` + html.EscapeString(username) + `</span>`)
	}
	out.WriteString(`<form method="post" action="` + html.EscapeString(logoutPath) + `"><button type="submit">退出</button></form>`)
	if tvbotLayout {
		out.WriteString(`</div>`)
	}
	out.WriteString(`</nav>`)
	return out.String()
}

// RenderSharedLogsDialog renders the log dialog used with shared.js.
func RenderSharedLogsDialog() string {
	return `<dialog id="shared-logs"><div class="dialog-head"><h2>程序日志</h2><button id="shared-logs-close" type="button">关闭</button></div><select id="shared-log-level"><option value="DEBUG">全部级别</option><option value="INFO">INFO+</option><option value="WARN">WARN+</option><option value="ERROR">ERROR</option></select><button id="shared-log-refresh" type="button">刷新</button><pre id="shared-log-content">等待加载</pre></dialog>`
}

func isSystemMenuItem(item TopMenuItem) bool {
	switch item.Key {
	case "system-info", "menu", "system":
		return true
	}
	switch item.Href {
	case "/system", "/menu", "/admin":
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
