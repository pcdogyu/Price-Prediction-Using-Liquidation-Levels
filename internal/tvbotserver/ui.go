package server

import (
	"html"
	"net/http"
	"strings"
)

type BuildInfo struct {
	CommitTime   string
	CommitHash   string
	CommitBranch string
}

func (b BuildInfo) FooterText() string {
	return "Code by Yuhao@jiansutech.com - " +
		buildInfoValue(b.CommitTime) + " - " +
		buildInfoValue(b.CommitHash) + " - " +
		buildInfoValue(b.CommitBranch)
}

func buildInfoValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	return value
}

func writeHTML(w http.ResponseWriter, status int, html string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(html))
}

func renderTVBotHTML(info BuildInfo, r *http.Request) string {
	prefix := strings.TrimRight(strings.TrimSpace(r.Header.Get("X-Forwarded-Prefix")), "/")
	if prefix == "" {
		prefix = ""
	}
	link := func(path string) string { return prefix + path }
	assets := `<script src="` + html.EscapeString(link("/assets/theme.js")) + `"></script><link rel="stylesheet" href="` + html.EscapeString(link("/assets/pages.css")) + `"><link rel="stylesheet" href="` + html.EscapeString(link("/assets/theme.css")) + `">`
	nav := `<div class="merged-liquidation-nav"><a class="merged-brand" href="` + html.EscapeString(link("/alerts")) + `">清算墙雷达</a><div class="merged-links"><a href="` + html.EscapeString(link("/alerts")) + `">报警</a><a href="` + html.EscapeString(link("/bubbles")) + `">气泡图</a><a href="` + html.EscapeString(link("/liquidations")) + `">清算历史</a><a href="` + html.EscapeString(link("/hedge-wall")) + `">对冲墙</a><a href="` + html.EscapeString(link("/market-info")) + `">市场信息</a><a href="` + html.EscapeString(link("/options")) + `">期权</a><a href="` + html.EscapeString(link("/tvbot#positions")) + `">持仓</a><a href="` + html.EscapeString(link("/admin")) + `">系统管理</a></div><button type="button" class="merged-theme-toggle" id="tvbot-theme" aria-label="切换主题"></button></div><style>.merged-liquidation-nav{position:sticky;top:0;z-index:20;display:flex;align-items:center;gap:18px;padding:12px 22px;background:var(--nav-bg);color:var(--text);border-bottom:1px solid var(--line)}.merged-brand{font-weight:800;color:var(--buy);text-decoration:none;white-space:nowrap}.merged-links{display:flex;flex-wrap:wrap;gap:8px}.merged-links a{color:var(--muted);text-decoration:none;padding:6px 8px;border-radius:7px}.merged-links a:hover{color:var(--text);background:var(--hover-bg)}.merged-theme-toggle{margin-left:auto;border:1px solid var(--strong-line);border-radius:7px;padding:6px 10px;background:var(--control-bg);color:var(--text);font:inherit;cursor:pointer;white-space:nowrap}.merged-theme-toggle:hover{background:var(--hover-bg)}@media(max-width:760px){.merged-liquidation-nav{align-items:flex-start;flex-wrap:wrap;gap:8px}.merged-links{gap:3px}.merged-theme-toggle{margin-left:0}}</style><script>(()=>{const button=document.getElementById('tvbot-theme');const update=()=>{const light=document.documentElement.dataset.theme==='light';button.textContent=light?'深色主题':'浅色主题';button.setAttribute('aria-label','当前'+(light?'浅色':'深色')+'主题，切换至'+(light?'深色':'浅色')+'主题')};button.addEventListener('click',()=>window.LiquidationTheme.toggle());window.addEventListener('themechange',update);update()})();</script>`
	page := strings.Replace(tvbotHTML, "</head>", assets+"</head>", 1)
	page = strings.Replace(page, "<body>", "<body class=\"tvbot-page\">"+nav, 1)
	return strings.Replace(page, "{{APP_FOOTER}}", html.EscapeString(info.FooterText()), 1)
}

func renderTVBotLoginHTML(message, next string, info BuildInfo) string {
	errorHTML := ""
	if strings.TrimSpace(message) != "" {
		errorHTML = `<p class="error">` + html.EscapeString(message) + `</p>`
	}
	page := strings.Replace(tvbotLoginHTML, "{{ERROR}}", errorHTML, 1)
	page = strings.Replace(page, "{{NEXT}}", html.EscapeString(sanitizeAdminNext(next)), 1)
	page = strings.Replace(page, "{{APP_FOOTER}}", html.EscapeString(info.FooterText()), 1)
	return page
}

const tvbotLoginHTML = `<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>TV OKX Bot 登录</title>
  <style>
    :root {
      color-scheme: light;
      --bg: #f4f7fb;
      --panel: #ffffff;
      --line: #d8dee9;
      --text: #172033;
      --muted: #647089;
      --blue: #1f6feb;
      --red: #c24135;
      --shadow: 0 18px 44px rgba(23, 32, 51, 0.12);
    }
    * { box-sizing: border-box; }
    body {
      margin: 0;
      min-height: 100vh;
      display: grid;
      place-items: center;
      padding: 28px;
      font: 14px/1.45 system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
      color: var(--text);
      background:
        linear-gradient(135deg, rgba(31, 111, 235, 0.08), rgba(19, 138, 85, 0.08)),
        var(--bg);
    }
    main {
      width: min(420px, 100%);
    }
    .brand {
      display: flex;
      align-items: center;
      gap: 10px;
      margin-bottom: 16px;
      font-weight: 800;
      font-size: 18px;
    }
    .logo {
      width: 32px;
      height: 32px;
      border-radius: 6px;
      display: inline-flex;
      align-items: center;
      justify-content: center;
      color: white;
      background: #172033;
      font-weight: 800;
      letter-spacing: 0;
    }
    form {
      border: 1px solid var(--line);
      border-radius: 8px;
      background: var(--panel);
      box-shadow: var(--shadow);
      padding: 24px;
    }
    h1 {
      margin: 0 0 18px;
      font-size: 22px;
      line-height: 1.2;
      letter-spacing: 0;
    }
    label {
      display: grid;
      gap: 6px;
      margin: 14px 0;
      color: var(--muted);
      font-weight: 650;
    }
    input {
      width: 100%;
      border: 1px solid var(--line);
      border-radius: 6px;
      padding: 11px 12px;
      color: var(--text);
      background: #fff;
      font: inherit;
      outline: none;
    }
    input:focus {
      border-color: var(--blue);
      box-shadow: 0 0 0 3px rgba(31, 111, 235, 0.14);
    }
    button {
      width: 100%;
      border: 1px solid var(--blue);
      border-radius: 6px;
      padding: 11px 12px;
      margin-top: 8px;
      color: white;
      background: var(--blue);
      font: inherit;
      font-weight: 750;
      cursor: pointer;
    }
    .error {
      margin: 0 0 12px;
      padding: 10px 12px;
      border: 1px solid rgba(194, 65, 53, 0.35);
      border-radius: 6px;
      color: var(--red);
      background: rgba(194, 65, 53, 0.08);
    }
    footer {
      margin-top: 16px;
      color: var(--muted);
      text-align: center;
      font-size: 12px;
    }
  </style>
</head>
<body>
  <main>
    <div class="brand"><span class="logo">TV</span><span>OKX Bot</span></div>
    <form method="post" action="/tvbot/login" autocomplete="on">
      <h1>管理员登录</h1>
      {{ERROR}}
      <input type="hidden" name="next" value="{{NEXT}}">
      <label>用户名<input name="username" autocomplete="username" required autofocus></label>
      <label>密码<input name="password" type="password" autocomplete="current-password" required></label>
      <button type="submit">登录</button>
    </form>
    <footer>{{APP_FOOTER}}</footer>
  </main>
</body>
</html>`

const tvbotHTML = `<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>TV OKX Bot</title>
  <style>
    :root {
      color-scheme: light dark;
      --blue: var(--buy);
      --green: var(--buy);
      --red: var(--sell);
      --amber: var(--warning);
      --shadow: 0 8px 28px rgba(23, 32, 51, 0.08);
    }
    * { box-sizing: border-box; }
    body {
      margin: 0;
      min-height: 100vh;
      display: flex;
      flex-direction: column;
      font: 14px/1.45 system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
      color: var(--text);
      background: var(--bg);
    }
    header {
      position: sticky;
      top: 0;
      z-index: 5;
      background: rgba(255, 255, 255, 0.96);
      border-bottom: 1px solid var(--line);
      box-shadow: 0 2px 12px rgba(23, 32, 51, 0.04);
    }
    .bar {
      width: 100%;
      margin: 0 auto;
      padding: 14px 18px;
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 18px;
    }
    .brand {
      display: flex;
      align-items: center;
      gap: 10px;
      min-width: 180px;
      font-weight: 700;
      font-size: 17px;
    }
    .mark {
      width: 28px;
      height: 28px;
      border-radius: 6px;
      background: #172033;
      color: #fff;
      display: inline-grid;
      place-items: center;
      font-size: 12px;
      letter-spacing: 0;
    }
    .header-controls {
      display: flex;
      align-items: center;
      justify-content: flex-end;
      gap: 8px;
      flex-wrap: wrap;
    }
    .global-exchange-switch {
      display: inline-flex;
      align-items: center;
      gap: 4px;
      border: 1px solid var(--line);
      border-radius: 8px;
      padding: 4px;
      background: #f8fafc;
    }
    .global-exchange-switch button {
      border: 1px solid transparent;
      border-radius: 6px;
      background: transparent;
      color: var(--text);
      min-height: 28px;
      padding: 4px 9px;
      font: inherit;
      font-size: 13px;
      cursor: pointer;
    }
    .global-exchange-switch button[aria-selected="true"] {
      background: var(--blue);
      border-color: var(--blue);
      color: #fff;
    }
    nav {
      display: flex;
      gap: 4px;
      flex-wrap: wrap;
      justify-content: flex-end;
    }
    nav button, .btn {
      border: 1px solid var(--line);
      background: #fff;
      color: var(--text);
      border-radius: 6px;
      padding: 8px 11px;
      font: inherit;
      cursor: pointer;
      min-height: 36px;
    }
    nav button {
      min-height: 34px;
      padding: 7px 10px;
      font-size: 13px;
    }
    nav button[aria-selected="true"], .btn.primary {
      background: var(--blue);
      color: #fff;
      border-color: var(--blue);
    }
    .btn.danger {
      color: var(--red);
      border-color: #e3aaa3;
    }
    .btn.success {
      color: #fff;
      background: var(--green);
      border-color: var(--green);
    }
    main {
      width: 100%;
      flex: 1;
      margin: 0 auto;
      padding: 18px;
    }
    .status {
      display: grid;
      grid-template-columns: repeat(4, minmax(0, 1fr));
      gap: 12px;
      margin-bottom: 16px;
    }
    .status[hidden] {
      display: none;
    }
    [hidden] {
      display: none !important;
    }
    .metric, section {
      background: var(--panel);
      border: 1px solid var(--line);
      border-radius: 8px;
      box-shadow: var(--shadow);
    }
    .metric {
      padding: 13px 14px;
      min-height: 78px;
    }
    .metric .label {
      color: var(--muted);
      font-size: 12px;
    }
    .metric .value {
      margin-top: 7px;
      font-size: 20px;
      font-weight: 700;
      overflow-wrap: anywhere;
    }
    section {
      display: none;
      padding: 16px;
    }
    section.active { display: block; }
    .section-head {
      display: flex;
      justify-content: space-between;
      align-items: center;
      gap: 12px;
      margin-bottom: 14px;
    }
    h1, h2, h3 {
      margin: 0;
      letter-spacing: 0;
    }
    h2 { font-size: 18px; }
    h3 { font-size: 14px; }
    .muted { color: var(--muted); }
    .grid {
      display: grid;
      grid-template-columns: repeat(3, minmax(0, 1fr));
      gap: 12px;
    }
    .grid.two { grid-template-columns: repeat(2, minmax(0, 1fr)); }
    label {
      display: grid;
      gap: 6px;
      color: var(--muted);
      font-size: 12px;
    }
    input, select, textarea {
      width: 100%;
      min-height: 36px;
      border: 1px solid var(--line);
      border-radius: 6px;
      padding: 8px 9px;
      color: var(--text);
      background: #fff;
      font: inherit;
    }
    textarea {
      min-height: 190px;
      resize: vertical;
      font-family: Consolas, "SFMono-Regular", monospace;
      font-size: 12px;
    }
    .check {
      display: flex;
      align-items: center;
      gap: 8px;
      color: var(--text);
      padding-top: 23px;
      min-height: 36px;
    }
    .check input {
      width: 16px;
      min-height: 16px;
    }
    .actions {
      display: flex;
      gap: 8px;
      flex-wrap: wrap;
      margin-top: 14px;
    }
    .template-title-row {
      display: grid;
      grid-template-columns: minmax(0, 1fr) auto;
      align-items: end;
      gap: 8px;
      margin-bottom: 10px;
    }
    .template-title-row .btn {
      min-height: 36px;
      white-space: nowrap;
    }
    table {
      width: 100%;
      border-collapse: collapse;
      table-layout: fixed;
      background: #fff;
      border: 1px solid var(--line);
      border-radius: 8px;
      overflow: hidden;
    }
    th, td {
      border-bottom: 1px solid var(--line);
      padding: 9px;
      text-align: left;
      vertical-align: top;
      overflow-wrap: anywhere;
    }
    td.time {
      white-space: nowrap;
      overflow-wrap: normal;
    }
    th.order-status,
    td.order-status {
      width: 7%;
    }
    th.order-okx,
    td.order-okx {
      width: 24%;
    }
    th.order-target,
    td.order-target {
      width: 9%;
    }
    th.order-signal-route,
    td.order-signal-route {
      width: 8%;
    }
    th.order-adx,
    td.order-adx {
      width: 5%;
    }
    th.order-market,
    td.order-market {
      width: 8%;
    }
    #orders .orders-table {
      min-width: 1500px;
    }
    th {
      color: var(--muted);
      font-size: 12px;
      background: #f8fafc;
      font-weight: 600;
    }
    tr:last-child td { border-bottom: 0; }
    .pill {
      display: inline-flex;
      align-items: center;
      min-height: 24px;
      border-radius: 999px;
      padding: 2px 8px;
      font-size: 12px;
      border: 1px solid var(--line);
      color: var(--muted);
      background: #fff;
    }
    .pill.ok { color: var(--green); border-color: #9fd8bd; }
    .pill.warn { color: var(--amber); border-color: #e9d08d; }
    .pill.bad { color: var(--red); border-color: #efb3ac; }
    .status-cell {
      display: flex;
      align-items: center;
      gap: 6px;
      flex-wrap: wrap;
    }
    .split {
      display: grid;
      grid-template-columns: 0.95fr 1.05fr;
      gap: 14px;
    }
    .api-key-layout {
      display: grid;
      grid-template-columns: 1.2fr 0.8fr;
      gap: 14px;
      align-items: start;
    }
    .api-key-form {
      display: grid;
      gap: 12px;
      align-content: start;
    }
    .api-key-inputs {
      align-items: start;
    }
    .api-key-inputs label {
      align-content: start;
    }
    .api-key-inputs input {
      min-height: 29px;
      height: 29px;
      padding-top: 5px;
      padding-bottom: 5px;
    }
    .exchange-tabs {
      display: inline-flex;
      gap: 6px;
      align-items: center;
      flex-wrap: wrap;
      margin-top: 8px;
    }
    .exchange-tabs button {
      border: 1px solid var(--line);
      border-radius: 6px;
      background: #fff;
      color: var(--text);
      min-height: 32px;
      padding: 6px 10px;
      font: inherit;
      cursor: pointer;
    }
    .exchange-tabs button[aria-selected="true"] {
      background: var(--blue);
      border-color: var(--blue);
      color: #fff;
    }
    .table-actions {
      display: flex;
      gap: 6px;
      flex-wrap: wrap;
    }
    .position-actions {
      display: flex;
      gap: 4px;
      flex-wrap: nowrap;
      white-space: nowrap;
    }
    .symbol-template-cell {
      display: grid;
      grid-template-columns: minmax(0, 1fr) auto;
      align-items: center;
      gap: 8px;
    }
    .symbol-template-text {
      min-width: 0;
      overflow-wrap: anywhere;
    }
    .symbol-template-btn {
      white-space: nowrap;
    }
    .btn.small {
      min-height: 28px;
      padding: 4px 8px;
      font-size: 12px;
    }
    .api-key-id-row {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 10px;
    }
    .api-key-active-id {
      min-width: 0;
      overflow-wrap: anywhere;
    }
    .api-key-id-row .btn {
      flex: 0 0 auto;
    }
    .btn.order-json-button {
      min-height: 24px;
      padding: 2px 7px;
    }
    .positions-table .position-actions {
      gap: 6px;
      flex-wrap: wrap;
    }
    .position-percent-close-btn {
      width: 36px;
      height: 36px;
      min-width: 36px;
      min-height: 36px;
      padding: 0;
      border-radius: 999px;
      font-size: 7px;
      line-height: 1;
      flex: 0 0 36px;
    }
    .position-monitor-panel {
      border-top: 1px solid var(--line);
      border-bottom: 1px solid var(--line);
      padding: 12px 0;
      margin: 14px 0 8px;
    }
    .position-monitor-grid {
      display: grid;
      grid-template-columns: repeat(5, minmax(140px, 1fr));
      gap: 10px;
      align-items: end;
      margin-top: 10px;
    }
    .position-monitor-grid label {
      margin: 0;
    }
    .position-monitor-status {
      font-size: 12px;
      color: var(--muted);
    }
    .btn:disabled, .btn.is-disabled {
      opacity: 0.58;
      cursor: not-allowed;
    }
    .okx-cell {
      display: flex;
      align-items: flex-start;
      justify-content: space-between;
      gap: 8px;
    }
    .okx-text {
      flex: 1;
      min-width: 0;
    }
    .analysis-controls {
      display: flex;
      gap: 10px;
      align-items: end;
      flex-wrap: wrap;
    }
    .analysis-controls label { min-width: 260px; }
    .analysis-period-row {
      display: flex;
      align-items: stretch;
      justify-content: space-between;
      gap: 12px;
      flex-wrap: wrap;
      margin-bottom: 14px;
    }
    .analysis-time-status {
      display: grid;
      align-content: center;
      gap: 3px;
      min-width: 260px;
      border: 1px solid var(--line);
      border-radius: 8px;
      padding: 8px 10px;
      background: #f8fafc;
    }
    .analysis-time-status .label {
      color: var(--muted);
      font-size: 12px;
      font-weight: 650;
    }
    .analysis-time-status .value {
      color: var(--text);
      font-weight: 700;
      overflow-wrap: anywhere;
    }
    .exchange-summary {
      display: inline-flex;
      align-items: center;
      min-height: 36px;
      border: 1px solid var(--line);
      border-radius: 6px;
      padding: 8px 10px;
      color: var(--muted);
      background: #f8fafc;
      font-weight: 650;
    }
    .symbol-controls {
      display: flex;
      gap: 10px;
      align-items: end;
      flex-wrap: wrap;
    }
    .symbol-controls label { min-width: 220px; }
    .symbol-search-label {
      flex: 1 1 280px;
      min-width: 260px;
    }
    .symbol-search-actions {
      display: flex;
      gap: 8px;
      align-items: end;
      flex-wrap: wrap;
    }
    #orders .section-head {
      align-items: flex-start;
      flex-wrap: wrap;
    }
    .order-history-actions {
      display: flex;
      justify-content: flex-end;
      align-items: center;
      gap: 8px;
      flex-wrap: wrap;
    }
    .order-search-input {
      width: min(270px, 55vw);
      min-width: 190px;
    }
    .order-status-filter {
      min-width: 130px;
    }
    .coinpair-filter-card {
      display: flex;
      justify-content: space-between;
      align-items: center;
      gap: 18px;
      flex-wrap: wrap;
      margin-top: 18px;
      padding: 14px;
      border: 1px solid var(--line);
      border-radius: 8px;
      background: #fff;
    }
    .coinpair-filter-card h3 {
      margin: 0 0 6px;
    }
    .coinpair-filter-actions {
      display: flex;
      justify-content: flex-end;
      align-items: center;
      gap: 8px;
      flex-wrap: wrap;
    }
    .coinpair-filter-input {
      width: min(260px, 60vw);
      min-width: 180px;
    }
    .coinpair-cooldown-settings {
      justify-content: flex-start;
    }
    .coinpair-cooldown-settings label {
      display: inline-flex;
      align-items: center;
      gap: 7px;
      color: var(--text);
      font-size: 13px;
      font-weight: 700;
    }
    .coinpair-cooldown-hours {
      width: 96px;
      min-width: 96px;
    }
    .coinpair-cooldown-status {
      margin-top: 8px;
    }
    .coinpair-filter-list {
      display: flex;
      align-items: stretch;
      gap: 10px;
      flex-wrap: wrap;
      flex: 1 0 100%;
      padding-top: 12px;
      border-top: 1px solid var(--line);
    }
    .coinpair-filter-group {
      flex: 1 0 100%;
    }
    .coinpair-filter-group-title {
      margin: 0 0 8px;
      font-size: 13px;
      color: var(--muted);
    }
    .coinpair-filter-group .coinpair-filter-list {
      border-top: 0;
      padding-top: 0;
    }
    .coinpair-filter-item {
      position: relative;
      min-width: 112px;
      padding: 13px 34px 13px 14px;
      border: 1px solid #b9c8df;
      border-radius: 8px;
      background: #f7faff;
      color: var(--text);
      font-weight: 700;
      overflow-wrap: anywhere;
    }
    .coinpair-filter-remove {
      position: absolute;
      top: 3px;
      right: 5px;
      width: 24px;
      min-height: 24px;
      padding: 0;
      border: 0;
      background: transparent;
      color: var(--muted);
      font-size: 20px;
      line-height: 1;
      cursor: pointer;
    }
    .coinpair-filter-remove:hover {
      color: var(--red);
    }
    .coinpair-filter-empty {
      padding: 6px 0;
    }
    .coinpair-cooldown-item {
      min-width: 190px;
      padding-right: 34px;
      border-color: #efb968;
      background: #fff8eb;
    }
    .coinpair-cooldown-title {
      display: flex;
      align-items: center;
      gap: 6px;
      margin-bottom: 5px;
    }
    .coinpair-cooldown-meta {
      display: block;
      color: var(--muted);
      font-size: 12px;
      font-weight: 500;
      line-height: 1.5;
    }
    .menu-hidden-check {
      display: flex;
      align-items: center;
      gap: 8px;
      color: var(--text);
      min-height: 28px;
    }
    .menu-hidden-check input {
      width: 16px;
      min-height: 16px;
    }
    .menu-sort-actions {
      display: flex;
      gap: 6px;
      flex-wrap: wrap;
    }
    .menu-label-input {
      max-width: 260px;
    }
    .analysis-metrics {
      display: grid;
      grid-template-columns: repeat(5, minmax(0, 1fr));
      gap: 10px;
      margin: 12px 0;
    }
    .asset-metrics {
      grid-template-columns: repeat(6, minmax(0, 1fr));
    }
    .symbol-metrics {
      grid-template-columns: repeat(4, minmax(0, 1fr));
    }
    .position-metrics {
      grid-template-columns: repeat(4, minmax(0, 1fr));
    }
    .analysis-card {
      border: 1px solid var(--line);
      border-radius: 8px;
      padding: 10px;
      background: #fff;
      min-height: 70px;
    }
    .analysis-card .label {
      color: var(--muted);
      font-size: 12px;
    }
    .analysis-card .value {
      margin-top: 5px;
      font-size: 18px;
      font-weight: 700;
      overflow-wrap: anywhere;
    }
    .signed-profit {
      color: var(--green);
      font-weight: 700;
    }
    .signed-loss {
      color: var(--red);
      font-weight: 700;
    }
    .chart-wrap {
      border: 1px solid var(--line);
      border-radius: 8px;
      background: #fff;
      padding: 10px;
      min-height: 460px;
    }
    .balance-window-toolbar {
      display: flex;
      align-items: center;
      gap: 7px;
      flex-wrap: wrap;
      flex: 1 1 560px;
      justify-content: flex-end;
      margin: 0;
    }
    .balance-window-toolbar .balance-window-btn {
      font-size: 16px;
    }
    .balance-window-btn[aria-selected="true"] {
      background: var(--blue);
      border-color: var(--blue);
      color: #fff;
    }
    .pending-order-switch {
      display: flex;
      align-items: center;
      gap: 6px;
      flex-wrap: wrap;
    }
    .pending-order-group-btn[aria-selected="true"] {
      background: var(--blue);
      border-color: var(--blue);
      color: #fff;
    }
    .dashboard-balance-grid {
      display: grid;
      grid-template-columns: minmax(0, 1fr);
      gap: 12px;
      margin-top: 14px;
    }
    .analysis-balance-grid {
      display: grid;
      grid-template-columns: minmax(0, 1fr);
      gap: 12px;
      margin-bottom: 14px;
      align-items: start;
    }
    .balance-chart-card {
      border: 1px solid var(--line);
      border-radius: 8px;
      background: #fff;
      padding: 10px;
      min-height: 360px;
    }
    .exchange-balance-card {
      display: grid;
      gap: 10px;
      align-content: start;
    }
    .analysis-usdt-block {
      display: grid;
      gap: 10px;
    }
    .exchange-balance-metrics {
      grid-template-columns: repeat(5, minmax(88px, 1fr));
      min-height: 0;
      align-content: start;
      margin: 0;
    }
    #usdt-chart {
      width: 100%;
      height: 420px;
      display: block;
    }
    .mini-usdt-chart {
      width: 100%;
      height: 250px;
      display: block;
    }
    #usdt-chart.mini-usdt-chart {
      height: 250px;
    }
    #analysis .mini-usdt-chart {
      height: 360px;
    }
    .chart-grid {
      stroke: #edf1f7;
      stroke-width: 1;
    }
    .chart-axis {
      stroke: #d8dee9;
      stroke-width: 1;
    }
    .chart-label {
      fill: #647089;
      font-size: 12px;
    }
    .symbol-table-wrap {
      overflow-x: auto;
    }
    .symbol-table {
      min-width: 980px;
    }
    .symbol-catalog-table {
      min-width: 1180px;
    }
    .symbol-catalog-table th,
    .symbol-catalog-table td {
      padding: 7px 6px;
      font-size: 13px;
      line-height: 1.32;
    }
    .symbol-catalog-table th {
      font-size: 11px;
    }
    .symbol-catalog-table .pill {
      min-height: 22px;
      padding: 1px 6px;
      font-size: 11px;
    }
    .symbol-catalog-table .symbol-template-cell {
      gap: 5px;
    }
    .symbol-catalog-table .symbol-template-btn {
      min-height: 26px;
      padding: 3px 6px;
      font-size: 11px;
    }
    .symbol-table th[data-symbol-sort] {
      cursor: pointer;
    }
    .symbol-table th[data-symbol-sort][draggable="true"] {
      cursor: grab;
    }
    .balance-pnl-block {
      border-top: 1px solid var(--line);
      padding-top: 10px;
    }
    .balance-pnl-block .analysis-metrics {
      grid-template-columns: repeat(5, minmax(88px, 1fr));
      gap: 8px;
      margin: 0;
    }
    .balance-pnl-block .analysis-card {
      min-height: 58px;
      padding: 8px;
    }
    .balance-pnl-block .analysis-card .value {
      font-size: 15px;
    }
    .analysis-usdt-chart-card {
      border-top: 1px solid var(--line);
      padding-top: 10px;
      min-height: 0;
    }
    .analysis-exchange-block + .analysis-exchange-block {
      margin-top: 14px;
    }
    .analysis-pagination {
      display: flex;
      align-items: center;
      justify-content: flex-end;
      gap: 8px;
      flex-wrap: wrap;
    }
    .analysis-trade-history-card {
      border-top: 1px solid var(--line);
      padding-top: 12px;
    }
    .analysis-trade-table {
      min-width: 1560px;
    }
    .analysis-trade-table th,
    .analysis-trade-table td {
      font-size: 12px;
      line-height: 1.35;
    }
    .analysis-trade-table td.time {
      overflow: hidden;
      text-overflow: clip;
    }
    .analysis-trade-table .analysis-exit-time-col { width: 11.4%; }
    .analysis-trade-table .analysis-inst-id-col { width: 6.4%; }
    .analysis-trade-table .analysis-side-col { width: 5.4%; }
    .analysis-trade-table .analysis-entry-px-col { width: 7%; }
    .analysis-trade-table .analysis-exit-px-col { width: 7%; }
    .analysis-trade-table .analysis-qty-col { width: 7%; }
    .analysis-trade-table .analysis-margin-col { width: 7.6%; }
    .analysis-trade-table .analysis-leverage-col { width: 4.6%; }
    .analysis-trade-table .analysis-realized-pnl-col { width: 7.4%; }
    .analysis-trade-table .analysis-pnl-ratio-col { width: 5.8%; }
    .analysis-trade-table .analysis-fee-col { width: 8.2%; }
    .analysis-trade-table .analysis-net-pnl-col { width: 7.6%; }
    .analysis-trade-table .analysis-turnover-col { width: 8.6%; }
    .analysis-trade-table .analysis-fill-count-col { width: 6%; }
    .analysis-realized-pnl-cell {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 6px;
      white-space: nowrap;
    }
    .analysis-cooldown-btn {
      flex: 0 0 auto;
      min-height: 24px !important;
      padding: 2px 6px !important;
    }
    .positions-table {
      min-width: 1400px;
    }
    .positions-table th,
    .positions-table td {
      font-size: 12px;
      line-height: 1.35;
    }
    .symbol-table th[draggable="true"] {
      cursor: grab;
      user-select: none;
    }
    .symbol-table th.is-dragging {
      opacity: 0.55;
    }
    .symbol-table th.is-drop-target {
      outline: 2px solid var(--accent);
      outline-offset: -2px;
    }
    .positions-table .pos-exchange-col { width: 4.8%; }
    .positions-table .pos-symbol-col { width: 7.8%; }
    .positions-table .pos-side-col { width: 6.8%; }
    .positions-table .pos-strategy-col { width: 7.2%; }
    .positions-table .pos-adx-col { width: 5.2%; }
    .positions-table .pos-size-col { width: 6.1%; }
    .positions-table .pos-price-col { width: 5.6%; }
    .positions-table .pos-margin-col { width: 5.9%; }
    .positions-table .pos-leverage-col { width: 3.9%; }
    .positions-table .pos-position-amount-col { width: 6.3%; }
    .positions-table .pos-pnl-col { width: 6.5%; }
    .positions-table .pos-rate-col { width: 5.2%; }
    .positions-table .pos-entry-time-col { width: 8.1%; }
    .positions-table .pos-holding-time-col { width: 6.6%; }
    .positions-table .pos-actions-col { width: 23.3%; }
    .pending-order-table {
      min-width: 1360px;
    }
    .pending-order-table .pending-exchange-col { width: 5.6%; }
    .pending-order-table .pending-time-col { width: 9.2%; }
    .pending-order-table .pending-symbol-col { width: 9.6%; }
    .pending-order-table .pending-side-col { width: 6.8%; }
    .pending-order-table .pending-pos-side-col { width: 5.4%; }
    .pending-order-table .pending-type-col { width: 6%; }
    .pending-order-table .pending-price-col { width: 7.8%; }
    .pending-order-table .pending-mid-col { width: 7.8%; }
    .pending-order-table .pending-size-col { width: 7.4%; }
    .pending-order-table .pending-margin-col { width: 7.4%; }
    .pending-order-table .pending-filled-col { width: 6.4%; }
    .pending-order-table .pending-age-col { width: 7%; }
    .pending-order-table .pending-state-col { width: 6%; }
    .pending-order-table .pending-actions-col { width: 7.6%; }
    pre {
      margin: 0;
      white-space: pre-wrap;
      overflow-wrap: anywhere;
      background: #111827;
      color: #e5e7eb;
      border-radius: 8px;
      padding: 12px;
      min-height: 140px;
      font-size: 12px;
    }
    .raw-json-dialog {
      width: min(780px, calc(100vw - 32px));
      border: 1px solid var(--line);
      border-radius: 8px;
      padding: 0;
      color: var(--text);
      background: #fff;
      box-shadow: var(--shadow);
    }
    .raw-json-dialog::backdrop {
      background: rgba(15, 23, 42, 0.45);
    }
    .raw-json-dialog:not([open]) {
      display: none;
    }
    .raw-json-head {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 10px;
      padding: 12px 14px;
      border-bottom: 1px solid var(--line);
    }
    .raw-json-dialog pre {
      border-radius: 0;
      min-height: 260px;
      max-height: min(62vh, 560px);
      overflow: auto;
    }
    .raw-json-actions {
      padding: 12px 14px;
      margin-top: 0;
      border-top: 1px solid var(--line);
      justify-content: flex-end;
    }
    .toast {
      position: fixed;
      right: 18px;
      bottom: 18px;
      max-width: 420px;
      background: #172033;
      color: #fff;
      border-radius: 8px;
      padding: 12px 14px;
      box-shadow: var(--shadow);
      display: none;
      z-index: 10;
    }
    .toast.show { display: block; }
    .build-footer {
      width: 100%;
      text-align: center;
      color: var(--muted);
      font-size: 12px;
      line-height: 1.4;
      padding: 8px 18px 18px;
      overflow-wrap: anywhere;
    }
    @media (max-width: 880px) {
      .bar { align-items: flex-start; flex-direction: column; }
      .header-controls { justify-content: flex-start; }
      nav { justify-content: flex-start; }
      .status, .grid, .grid.two, .split, .api-key-layout, .analysis-metrics, .asset-metrics, .symbol-metrics, .position-metrics, .dashboard-balance-grid, .analysis-balance-grid, .position-monitor-grid { grid-template-columns: 1fr; }
      .template-title-row { grid-template-columns: 1fr; }
      .analysis-period-row { flex-direction: column; }
      .analysis-time-status { min-width: 0; }
      .balance-window-toolbar { justify-content: flex-start; }
      main { padding: 12px; }
      section { padding: 12px; }
      #usdt-chart { height: 320px; }
      #usdt-chart.mini-usdt-chart { height: 240px; }
      .mini-usdt-chart { height: 240px; }
      #analysis .mini-usdt-chart { height: 346px; }
      .exchange-balance-metrics { min-height: 0; }
    }
  </style>
</head>
<body>
  <header>
    <div class="bar">
      <div class="brand"><span class="mark">TV</span><span>OKX Bot</span></div>
      <div class="header-controls">
        <div class="global-exchange-switch" role="group" aria-label="全局交易所">
          <button type="button" data-global-exchange="okx" aria-selected="true">OKX</button>
          <button type="button" data-global-exchange="binance">Binance</button>
        </div>
        <nav aria-label="sections">
          <button type="button" data-tab="dashboard" aria-selected="true">总览</button>
          <button type="button" data-tab="positions">持仓</button>
          <button type="button" data-tab="analysis">订单分析</button>
          <button type="button" data-tab="symbols">币对配置</button>
          <button type="button" data-tab="config">订单配置</button>
          <button type="button" data-tab="apiKeys">API Key</button>
          <button type="button" data-tab="orderSettings">下单设置</button>
          <button type="button" data-tab="template">告警模板</button>
          <button type="button" data-tab="orders">订单</button>
          <button type="button" data-tab="tradeMonitor">成交监听</button>
          <button type="button" data-tab="menuSettings">菜单设置</button>
          <button type="button" data-tab="upgrade">升级</button>
        </nav>
      </div>
    </div>
  </header>

  <main>
    <div class="status" id="order-info-status" hidden>
      <div class="metric"><div class="label">交易环境</div><div class="value" id="metric-env">-</div></div>
      <div class="metric"><div class="label">交易 API</div><div class="value" id="metric-api-keys">-</div></div>
      <div class="metric"><div class="label">下单金额</div><div class="value" id="metric-amount">-</div></div>
      <div class="metric"><div class="label">最近信号</div><div class="value" id="metric-orders">-</div></div>
    </div>

    <section id="dashboard" class="active">
      <div class="section-head">
        <h2>总览</h2>
        <div class="actions" style="margin-top:0">
          <button class="btn" type="button" id="refresh-all">刷新</button>
          <button class="btn success" type="button" id="check-okx">检查交易所</button>
        </div>
      </div>
      <div class="split">
        <div>
          <h3>运行配置</h3>
          <table style="margin-top:10px">
            <tbody id="dashboard-config"></tbody>
          </table>
        </div>
        <div>
          <h3>交易所检查</h3>
          <pre id="okx-output">-</pre>
        </div>
      </div>
      <div class="dashboard-balance-grid">
        <div class="balance-chart-card" data-exchange-view="okx">
          <div class="section-head" style="margin-bottom:8px">
            <h3>OKX USDT 余额</h3>
            <span class="muted" id="overview-okx-status">-</span>
          </div>
          <div class="analysis-metrics symbol-metrics">
            <div class="analysis-card"><div class="label">USDT估值</div><div class="value" id="overview-okx-eq">-</div></div>
            <div class="analysis-card"><div class="label">可用</div><div class="value" id="overview-okx-avail">-</div></div>
            <div class="analysis-card"><div class="label">交易 API</div><div class="value" id="overview-okx-api">-</div></div>
            <div class="analysis-card"><div class="label">更新时间</div><div class="value" id="overview-okx-updated">-</div></div>
          </div>
          <svg id="overview-okx-usdt-chart" class="mini-usdt-chart" role="img" aria-label="OKX USDT balance chart"></svg>
        </div>
        <div class="balance-chart-card" data-exchange-view="binance">
          <div class="section-head" style="margin-bottom:8px">
            <h3>Binance USDT 余额</h3>
            <span class="muted" id="overview-binance-status">-</span>
          </div>
          <div class="analysis-metrics symbol-metrics">
            <div class="analysis-card"><div class="label">USDT估值</div><div class="value" id="overview-binance-eq">-</div></div>
            <div class="analysis-card"><div class="label">可用</div><div class="value" id="overview-binance-avail">-</div></div>
            <div class="analysis-card"><div class="label">交易 API</div><div class="value" id="overview-binance-api">-</div></div>
            <div class="analysis-card"><div class="label">更新时间</div><div class="value" id="overview-binance-updated">-</div></div>
          </div>
          <svg id="overview-binance-usdt-chart" class="mini-usdt-chart" role="img" aria-label="Binance USDT balance chart"></svg>
        </div>
      </div>
    </section>

    <section id="positions">
      <div class="section-head">
        <h2>当前持仓</h2>
        <div class="analysis-controls">
          <span class="exchange-summary" id="position-exchange-summary">OKX / Binance USDⓈ-M</span>
          <button class="btn primary" type="button" id="refresh-positions">刷新持仓</button>
        </div>
      </div>
      <div class="analysis-metrics position-metrics">
        <div class="analysis-card"><div class="label">持仓数</div><div class="value" id="positions-count">-</div></div>
        <div class="analysis-card"><div class="label">未实现盈亏</div><div class="value" id="positions-upl">-</div></div>
        <div class="analysis-card"><div class="label">名义价值</div><div class="value" id="positions-notional">-</div></div>
        <div class="analysis-card"><div class="label">更新时间</div><div class="value" id="positions-updated">-</div></div>
      </div>
      <div class="symbol-table-wrap">
        <table class="symbol-table positions-table">
          <colgroup id="position-cols"></colgroup>
          <thead id="position-head"></thead>
          <tbody id="position-rows"></tbody>
        </table>
      </div>
      <div class="position-monitor-panel">
        <div class="section-head">
          <div>
            <h3>自动扫描持仓</h3>
            <div class="position-monitor-status" id="position-monitor-status">-</div>
          </div>
          <button class="btn primary" type="button" id="save-position-monitor">保存自动扫描</button>
        </div>
        <div class="position-monitor-grid">
          <label class="check"><input id="position-monitor-okx-enabled" type="checkbox">OKX 自动扫描/平仓</label>
          <label class="check"><input id="position-monitor-binance-enabled" type="checkbox">Binance 自动扫描/平仓</label>
          <label>扫描间隔秒<input id="position-monitor-interval" type="number" min="1" step="1"></label>
          <label>止盈 %<input id="position-monitor-tp" type="number" min="0.01" step="0.01"></label>
          <label>止损 %<input id="position-monitor-sl" type="number" min="0.01" step="0.01"></label>
        </div>
      </div>
      <div class="section-head" style="margin:18px 0 10px">
        <h3>当前挂单</h3>
        <div class="actions" style="margin-top:0">
          <div class="pending-order-switch" role="group" aria-label="挂单类型">
            <button class="btn small pending-order-group-btn" type="button" data-pending-order-group="normal" aria-selected="true">基础订单 (0)</button>
            <button class="btn small pending-order-group-btn" type="button" data-pending-order-group="algo">算法订单 (0)</button>
          </div>
          <span class="muted" id="pending-orders-updated">-</span>
        </div>
      </div>
      <div class="symbol-table-wrap">
        <table class="symbol-table pending-order-table">
          <colgroup id="pending-order-cols"></colgroup>
          <thead id="pending-order-head"></thead>
          <tbody id="pending-order-rows"></tbody>
        </table>
      </div>
    </section>

    <section id="analysis" aria-label="订单分析">
      <div class="analysis-period-row">
        <div class="analysis-time-status">
          <div class="label">订单时间</div>
          <div class="value" id="analysis-updated">-</div>
        </div>
        <div class="balance-window-toolbar" role="group" aria-label="USDT余额周期">
          <button class="btn small balance-window-btn" type="button" data-balance-minutes="0">当前</button>
          <button class="btn small balance-window-btn" type="button" data-balance-minutes="5">5m</button>
          <button class="btn small balance-window-btn" type="button" data-balance-minutes="10">10m</button>
          <button class="btn small balance-window-btn" type="button" data-balance-minutes="15">15m</button>
          <button class="btn small balance-window-btn" type="button" data-balance-minutes="30">30m</button>
          <button class="btn small balance-window-btn" type="button" data-balance-minutes="60">1h</button>
          <button class="btn small balance-window-btn" type="button" data-balance-minutes="240">4h</button>
          <button class="btn small balance-window-btn" type="button" data-balance-minutes="480">8h</button>
          <button class="btn small balance-window-btn" type="button" data-balance-minutes="720">12h</button>
          <button class="btn small balance-window-btn" type="button" data-balance-minutes="1440">24h</button>
          <button class="btn small balance-window-btn" type="button" data-balance-minutes="2880">48h</button>
          <button class="btn small balance-window-btn" type="button" data-balance-minutes="4320">3d</button>
          <button class="btn small balance-window-btn" type="button" data-balance-minutes="10080">7d</button>
          <button class="btn small balance-window-btn" type="button" data-balance-minutes="43200">30d</button>
          <button class="btn small balance-window-btn" type="button" data-balance-minutes="129600">90d</button>
          <button class="btn small" type="button" id="reset-balance-baseline">重置基准</button>
          <button class="btn small" type="button" id="sync-balance-history">同步历史</button>
        </div>
      </div>
      <div class="analysis-balance-grid">
        <div class="balance-chart-card exchange-balance-card" data-exchange-view="okx">
          <div class="section-head" style="margin-bottom:0">
            <h3>OKX 订单</h3>
            <span class="muted" id="analysis-okx-balance-status">-</span>
          </div>
          <div class="analysis-usdt-block">
            <div class="section-head" style="margin-bottom:0">
              <h3>USDT 估值表</h3>
            </div>
            <div class="analysis-metrics symbol-metrics exchange-balance-metrics">
              <div class="analysis-card"><div class="label">周期起始</div><div class="value" id="analysis-okx-window-start">-</div></div>
              <div class="analysis-card"><div class="label">当前USDT</div><div class="value" id="analysis-usdt-eq">-</div></div>
              <div class="analysis-card"><div class="label">周期变化</div><div class="value" id="analysis-okx-window-change">-</div></div>
              <div class="analysis-card"><div class="label">最大回撤</div><div class="value" id="analysis-okx-max-drawdown">-</div></div>
              <div class="analysis-card"><div class="label">更新时间</div><div class="value" id="analysis-balance-updated">-</div></div>
            </div>
          </div>
          <div class="analysis-exchange-block balance-pnl-block">
            <div class="analysis-metrics">
              <div class="analysis-card"><div class="label">净盈亏</div><div class="value" id="analysis-okx-net-pnl">-</div></div>
              <div class="analysis-card"><div class="label">胜率</div><div class="value" id="analysis-okx-win-rate">-</div></div>
              <div class="analysis-card"><div class="label">盈利因子</div><div class="value" id="analysis-okx-profit-factor">-</div></div>
              <div class="analysis-card"><div class="label">盈亏比</div><div class="value" id="analysis-okx-payoff-ratio">-</div></div>
              <div class="analysis-card"><div class="label">仓位数</div><div class="value" id="analysis-okx-trades">-</div></div>
              <div class="analysis-card"><div class="label">周期成交额</div><div class="value" id="analysis-okx-turnover">-</div></div>
              <div class="analysis-card"><div class="label">周期手续费</div><div class="value" id="analysis-okx-fees">-</div></div>
              <div class="analysis-card"><div class="label">当前持仓</div><div class="value" id="analysis-okx-position-count">-</div></div>
              <div class="analysis-card"><div class="label">持仓浮盈</div><div class="value" id="analysis-okx-position-upl">-</div></div>
            </div>
          </div>
          <div class="analysis-usdt-chart-card">
            <div class="section-head" style="margin:0 0 8px">
              <h3 id="analysis-okx-usdt-title">USDT 权益图</h3>
            </div>
            <svg id="usdt-chart" class="mini-usdt-chart" role="img" aria-label="OKX USDT equity chart"></svg>
          </div>
        </div>
        <div class="balance-chart-card exchange-balance-card" data-exchange-view="binance">
          <div class="section-head" style="margin-bottom:0">
            <h3>Binance 订单</h3>
            <span class="muted" id="analysis-binance-balance-status">-</span>
          </div>
          <div class="analysis-usdt-block">
            <div class="section-head" style="margin-bottom:0">
              <h3>USDT 估值表</h3>
            </div>
            <div class="analysis-metrics symbol-metrics exchange-balance-metrics">
              <div class="analysis-card"><div class="label">周期起始</div><div class="value" id="analysis-binance-window-start">-</div></div>
              <div class="analysis-card"><div class="label">当前USDT</div><div class="value" id="analysis-binance-usdt-eq">-</div></div>
              <div class="analysis-card"><div class="label">周期变化</div><div class="value" id="analysis-binance-window-change">-</div></div>
              <div class="analysis-card"><div class="label">最大回撤</div><div class="value" id="analysis-binance-max-drawdown">-</div></div>
              <div class="analysis-card"><div class="label">更新时间</div><div class="value" id="analysis-binance-balance-updated">-</div></div>
            </div>
          </div>
          <div class="analysis-exchange-block balance-pnl-block">
            <div class="analysis-metrics">
              <div class="analysis-card"><div class="label">净盈亏</div><div class="value" id="analysis-binance-net-pnl">-</div></div>
              <div class="analysis-card"><div class="label">胜率</div><div class="value" id="analysis-binance-win-rate">-</div></div>
              <div class="analysis-card"><div class="label">盈利因子</div><div class="value" id="analysis-binance-profit-factor">-</div></div>
              <div class="analysis-card"><div class="label">盈亏比</div><div class="value" id="analysis-binance-payoff-ratio">-</div></div>
              <div class="analysis-card"><div class="label">仓位数</div><div class="value" id="analysis-binance-trades">-</div></div>
              <div class="analysis-card"><div class="label">周期成交额</div><div class="value" id="analysis-binance-turnover">-</div></div>
              <div class="analysis-card"><div class="label">周期手续费</div><div class="value" id="analysis-binance-fees">-</div></div>
              <div class="analysis-card"><div class="label">当前持仓</div><div class="value" id="analysis-binance-position-count">-</div></div>
              <div class="analysis-card"><div class="label">持仓浮盈</div><div class="value" id="analysis-binance-position-upl">-</div></div>
            </div>
          </div>
          <div class="analysis-usdt-chart-card">
            <div class="section-head" style="margin:0 0 8px">
              <h3 id="analysis-binance-usdt-title">USDT 权益图</h3>
            </div>
            <svg id="analysis-binance-usdt-chart" class="mini-usdt-chart" role="img" aria-label="Binance USDT equity chart"></svg>
          </div>
        </div>
      </div>
      <div class="analysis-trade-history-card">
        <div class="section-head" style="margin-bottom:10px">
          <h3 id="analysis-trade-history-title">下单盈亏分析</h3>
          <span class="muted" id="analysis-trade-history-status">-</span>
        </div>
        <div class="symbol-table-wrap">
          <table class="symbol-table analysis-trade-table">
            <colgroup id="analysis-trade-cols"></colgroup>
            <thead id="analysis-trade-head"></thead>
            <tbody id="analysis-trade-rows"></tbody>
          </table>
        </div>
        <div class="analysis-pagination" style="margin-top:10px">
          <button class="btn small" type="button" id="analysis-trade-prev">上一页</button>
          <span class="muted" id="analysis-trade-page-info">-</span>
          <button class="btn small" type="button" id="analysis-trade-next">下一页</button>
        </div>
      </div>
    </section>

    <section id="apiKeys">
      <div class="section-head">
        <div>
          <h2>API Key</h2>
          <div class="exchange-tabs" id="api-key-exchange-tabs">
            <button type="button" data-api-key-exchange="okx" aria-selected="true">OKX</button>
            <button type="button" data-api-key-exchange="binance">Binance</button>
          </div>
        </div>
        <div class="actions" style="margin-top:0">
          <button class="btn primary" type="button" id="save-api-keys">保存 API Key</button>
          <button class="btn success" type="button" id="test-api-keys">测试 API</button>
        </div>
      </div>
      <div class="api-key-layout">
        <div class="api-key-form">
          <div class="grid two">
            <label>交易 API<select id="key-selected"></select></label>
            <label>账户名称<input id="key-name" autocomplete="off" spellcheck="false"></label>
            <label>API ID<input id="key-id" autocomplete="off" spellcheck="false" placeholder="default"></label>
            <label class="check"><input id="key-active" type="checkbox">设为交易 API</label>
          </div>
          <div class="grid api-key-inputs">
            <label id="key-api-label">OKX API Key<input id="key-api" autocomplete="off" spellcheck="false"></label>
            <label id="key-secret-label">OKX Secret Key<input id="key-secret" type="password" autocomplete="new-password" spellcheck="false"></label>
            <label id="key-passphrase-label">OKX Passphrase<input id="key-passphrase" type="password" autocomplete="new-password" spellcheck="false"></label>
          </div>
          <div class="actions">
            <button class="btn" type="button" id="add-api-key">新增 API</button>
            <button class="btn danger" type="button" id="delete-api-key">删除当前 API</button>
          </div>
        </div>
        <div>
          <h3>当前状态</h3>
          <table style="margin-top:10px">
            <tbody id="api-key-status"></tbody>
          </table>
          <h3 style="margin-top:14px">API 列表</h3>
          <table style="margin-top:10px">
            <thead><tr><th>API ID</th><th>名称</th><th>状态</th><th>API Key</th></tr></thead>
            <tbody id="api-key-accounts"></tbody>
          </table>
        </div>
      </div>
    </section>

    <section id="symbols">
      <div class="section-head">
        <h2>币对配置</h2>
        <div class="symbol-controls">
          <label>交易所<select id="symbol-exchange"><option value="okx">OKX</option><option value="binance">Binance</option></select></label>
          <label>环境<select id="symbol-env"><option value="live">实盘</option><option value="demo">模拟</option></select></label>
          <label>交易范围<select id="symbol-turnover-scope"><option value="top50">成交量前 50</option><option value="top100">成交量前 100</option><option value="top200">成交量前 200</option><option value="top500">成交量前 500</option><option value="all">全部</option></select></label>
          <label class="symbol-search-label"><span id="symbol-search-label-text">搜索成交量前 100 币对</span><input id="symbol-search" autocomplete="off" spellcheck="false" placeholder="例如 BTC"></label>
          <div class="symbol-search-actions">
            <button class="btn" type="button" id="clear-symbol-search" disabled>清除搜索</button>
            <button class="btn primary" type="button" id="refresh-symbols">刷新币对</button>
          </div>
        </div>
      </div>
      <div class="analysis-metrics symbol-metrics">
        <div class="analysis-card"><div class="label">实盘币对</div><div class="value" id="symbol-live-count">-</div></div>
        <div class="analysis-card"><div class="label">模拟币对</div><div class="value" id="symbol-demo-count">-</div></div>
        <div class="analysis-card"><div class="label">本地已配置</div><div class="value" id="symbol-configured-count">-</div></div>
        <div class="analysis-card"><div class="label">当前显示</div><div class="value" id="symbol-visible-count">-</div></div>
      </div>
      <div class="muted" id="symbol-errors" style="margin:0 0 10px"></div>
      <div class="symbol-table-wrap">
        <table class="symbol-table symbol-catalog-table">
          <colgroup id="symbol-cols"></colgroup>
          <thead id="symbol-head"></thead>
          <tbody id="symbol-rows"></tbody>
        </table>
      </div>
    </section>

    <section id="config">
      <div class="section-head">
        <h2>订单配置</h2>
        <button class="btn primary" type="button" id="save-config">保存订单配置</button>
      </div>
      <div class="grid">
        <label>监听地址<input id="cfg-addr" autocomplete="off"></label>
        <label>数据文件<input id="cfg-data-file" autocomplete="off"></label>
        <label>SQLite 数据库<input id="cfg-database-file" autocomplete="off"></label>
        <label>OKX Base URL<input id="cfg-base-url" autocomplete="off"></label>
        <label>Binance Base URL<input id="cfg-binance-base-url" autocomplete="off"></label>
        <label>Binance Demo Base URL<input id="cfg-binance-demo-base-url" autocomplete="off"></label>
        <label>交易环境<select id="cfg-env"><option value="demo">demo</option><option value="live">live</option></select></label>
        <label>保证金模式<select id="cfg-margin"><option value="isolated">isolated</option><option value="cross">cross</option></select></label>
        <label>持仓模式<select id="cfg-position"><option value="net">net</option><option value="long_short">long_short</option></select></label>
        <label>信号有效秒数<input id="cfg-ttl" type="number" min="1" step="1"></label>
        <label class="check"><input id="cfg-live" type="checkbox">允许实盘交易</label>
      </div>
    </section>

    <section id="orderSettings">
      <div class="section-head">
        <h2>下单设置</h2>
        <button class="btn primary" type="button" id="save-order-settings">保存下单设置</button>
      </div>
      <div class="grid">
        <label>USDT 下单金额<input id="order-amount" type="number" min="0" step="0.01"></label>
        <label>杠杆<input id="order-leverage" type="number" min="1" step="1"></label>
        <label>订单类型<select id="order-type"><option value="market">市价单</option><option value="limit">限价单</option></select></label>
        <label>风控模式<select id="order-risk-type"><option value="tp_sl">固定止盈止损</option><option value="trailing">移动止损</option><option value="none">不设置</option></select></label>
        <label>固定止盈 %<input id="order-tp" type="number" min="0" step="0.01"></label>
        <label>固定止损 %<input id="order-sl" type="number" min="0" step="0.01"></label>
        <label>移动止损 %<input id="order-trailing" type="number" min="0" step="0.01"></label>
        <label>多单限价倍率<input id="order-long-multiplier" type="number" min="0" step="0.000001"></label>
        <label>空单限价倍率<input id="order-short-multiplier" type="number" min="0" step="0.000001"></label>
        <label>24h 交易额排名限制<select id="order-turnover-scope"><option value="top50">前 50</option><option value="top100">前 100</option><option value="top200">前 200</option><option value="top500">前 500</option><option value="all">不限排名</option></select></label>
      </div>
      <div class="muted" style="margin-top:10px">按各交易所、各交易环境的 24 小时交易额排名过滤新开仓，适用于 OKX 和 Binance；平仓不受影响。点击“保存下单设置”后生效。</div>
      <table style="margin-top:14px">
        <tbody id="order-settings-preview"></tbody>
      </table>
    </section>

    <section id="template">
      <div class="section-head">
        <h2>告警模板</h2>
        <button class="btn primary" type="button" id="make-template">生成模板</button>
      </div>
      <div class="split">
        <div class="grid two">
          <label>Webhook URL<input id="template-webhook-url" readonly></label>
          <label>下单去向<select id="tpl-target-exchange"><option value="okx">OKX</option><option value="binance">Binance USDⓈ-M</option></select></label>
          <label>交易环境<select id="tpl-trade-env"><option value="demo">模拟</option><option value="live">实盘</option></select></label>
          <label>交易 API<select id="tpl-api-id"></select></label>
          <label>币对<input id="tpl-coinpair" list="tpl-coinpair-list" autocomplete="off" spellcheck="false" placeholder="跟随 TradingView ({{ticker}})"><datalist id="tpl-coinpair-list"></datalist></label>
          <label>方向<select id="tpl-direction"><option value="both">多空都做</option><option value="long">只做多</option><option value="short">只做空</option></select></label>
          <label>价格源<select id="tpl-price-source"><option value="close">close</option><option value="high">high</option><option value="low">low</option></select></label>
          <div class="actions" style="margin-top:0"><button class="btn" type="button" id="copy-webhook-url">复制 URL</button></div>
        </div>
        <div>
          <div class="template-title-row">
            <label>报警标题<input id="template-title" readonly></label>
            <button class="btn" type="button" id="copy-template-title">复制标题</button>
          </div>
          <textarea id="template-output" readonly></textarea>
          <div class="actions"><button class="btn" type="button" id="copy-template">复制 JSON</button></div>
        </div>
      </div>
    </section>

    <section id="orders">
      <div class="section-head">
        <div>
          <h2>订单 / 信号历史</h2>
          <span class="muted" id="order-history-status">-</span>
        </div>
        <div class="order-history-actions">
          <select class="order-status-filter" id="order-status-filter" aria-label="筛选订单状态">
            <option value="">全部状态</option>
            <option value="submitted">已下单</option>
            <option value="ignored">已忽略</option>
            <option value="failed">下单失败</option>
            <option value="rejected">已拒绝</option>
            <option value="duplicate">重复信号</option>
            <option value="accepted">处理中</option>
          </select>
          <input class="order-search-input" id="order-search" autocomplete="off" spellcheck="false" placeholder="币对 / 金额 / 订单号" aria-label="搜索订单历史">
          <button class="btn" type="button" id="search-orders">搜索</button>
          <button class="btn" type="button" id="clear-order-search" disabled>清除</button>
          <button class="btn" type="button" id="refresh-orders">刷新历史</button>
        </div>
      </div>
      <div class="symbol-table-wrap">
        <table class="orders-table">
          <thead>
            <tr><th>时间</th><th class="order-status">状态</th><th>信号来源</th><th class="order-target">下单去向</th><th>方向</th><th class="order-signal-route">信号执行</th><th class="order-adx">ADX</th><th class="order-market">市场类型</th><th>币对</th><th>价格</th><th>金额</th><th class="order-okx">交易所 / 返回</th></tr>
          </thead>
          <tbody id="order-rows"></tbody>
        </table>
      </div>
      <div class="analysis-pagination" style="margin-top:10px">
        <button class="btn small" type="button" id="order-prev">上一页</button>
        <span class="muted" id="order-page-info">-</span>
        <button class="btn small" type="button" id="order-next">下一页</button>
      </div>
      <div class="coinpair-filter-card">
        <div>
          <h3>忽略币对关键字</h3>
          <div class="muted" id="ignored-coinpair-status">当前未启用</div>
          <div class="muted">不区分大小写，采用包含匹配并忽略常见分隔符；只过滤开仓信号，平仓和系统持仓保护不受影响。</div>
        </div>
        <div class="coinpair-filter-actions">
          <input class="coinpair-filter-input" id="ignored-coinpair" autocomplete="off" spellcheck="false" placeholder="例如 ETH" aria-label="忽略币对关键字">
          <button class="btn primary" type="button" id="add-ignored-coinpair" disabled>添加过滤</button>
        </div>
        <div class="coinpair-filter-group">
          <h4 class="coinpair-filter-group-title">手动过滤</h4>
          <div class="coinpair-filter-list" id="ignored-coinpair-list"><span class="muted coinpair-filter-empty">暂无已添加的币对过滤</span></div>
        </div>
        <div class="coinpair-filter-group">
          <h4 class="coinpair-filter-group-title">亏损平仓后冷静期</h4>
          <div class="coinpair-filter-actions coinpair-cooldown-settings">
            <label><input id="loss-cooldown-enabled" type="checkbox">启用亏损平仓后同币对冷静期</label>
            <label>时长（小时）<input class="coinpair-cooldown-hours" id="loss-cooldown-hours" type="number" min="1" max="8760" step="1" inputmode="numeric"></label>
            <button class="btn primary" type="button" id="save-loss-cooldown">保存冷静期设置</button>
          </div>
          <div class="muted coinpair-cooldown-status" id="loss-cooldown-status">-</div>
        </div>
        <div class="coinpair-filter-group">
          <h4 class="coinpair-filter-group-title">动态冷静期（按来源生效）</h4>
          <div class="coinpair-filter-list" id="coinpair-cooldown-list"><span class="muted coinpair-filter-empty">暂无冷静期币对</span></div>
        </div>
      </div>
    </section>

    <section id="tradeMonitor">
      <div class="section-head">
        <div>
          <h2>成交监听</h2>
          <span class="muted" id="trade-monitor-status">-</span>
        </div>
        <button class="btn" type="button" id="refresh-trade-monitor">刷新状态</button>
      </div>
      <div class="analysis-metrics symbol-metrics">
        <div class="analysis-card"><div class="label">监听状态</div><div class="value" id="trade-monitor-running">-</div></div>
        <div class="analysis-card"><div class="label">自动补回</div><div class="value" id="trade-monitor-reentry">-</div></div>
        <div class="analysis-card"><div class="label">Lifecycle</div><div class="value" id="trade-monitor-lifecycle-count">-</div></div>
        <div class="analysis-card"><div class="label">最近事件</div><div class="value" id="trade-monitor-event-count">-</div></div>
      </div>
      <div class="section-head" style="margin:18px 0 10px">
        <h3>监听 Checkpoint</h3>
      </div>
      <div class="symbol-table-wrap">
        <table class="symbol-table">
          <thead><tr><th>交易所</th><th>API</th><th>Symbol</th><th>最近成交</th><th>最近轮询</th><th>错误</th></tr></thead>
          <tbody id="trade-monitor-checkpoints"></tbody>
        </table>
      </div>
      <div class="section-head" style="margin:18px 0 10px">
        <h3>Lifecycle</h3>
      </div>
      <div class="symbol-table-wrap">
        <table class="symbol-table">
          <thead><tr><th>最近更新</th><th>状态</th><th>API</th><th>Symbol</th><th>方向</th><th>入口订单</th><th>退出订单</th><th>补回</th></tr></thead>
          <tbody id="trade-monitor-lifecycles"></tbody>
        </table>
      </div>
      <div class="section-head" style="margin:18px 0 10px">
        <h3>最近事件</h3>
      </div>
      <div class="symbol-table-wrap">
        <table class="symbol-table">
          <thead><tr><th>时间</th><th>事件</th><th>API</th><th>Symbol</th><th>状态</th><th>说明</th></tr></thead>
          <tbody id="trade-monitor-events"></tbody>
        </table>
      </div>
    </section>

    <section id="menuSettings">
      <div class="section-head">
        <h2>菜单设置</h2>
        <button class="btn primary" type="button" id="save-menu-settings">保存菜单设置</button>
      </div>
      <table>
        <thead>
          <tr><th>首页</th><th>默认菜单</th><th>菜单名称</th><th>是否隐藏</th><th>排序</th></tr>
        </thead>
        <tbody id="menu-settings-rows"></tbody>
      </table>
    </section>

    <section id="upgrade">
      <div class="section-head">
        <h2>升级</h2>
        <div class="actions" style="margin-top:0">
          <button class="btn" type="button" id="refresh-upgrade">刷新状态</button>
          <button class="btn primary" type="button" id="start-upgrade">开始升级</button>
        </div>
      </div>
      <pre id="upgrade-output">-</pre>
    </section>
  </main>
  <footer class="build-footer">{{APP_FOOTER}}</footer>
  <div class="toast" id="toast"></div>
  <dialog class="raw-json-dialog" id="raw-json-dialog">
    <div class="raw-json-head">
      <h3>原始 JSON</h3>
      <button class="btn small" type="button" id="close-raw-json">关闭</button>
    </div>
    <pre id="raw-json-output">-</pre>
    <div class="actions raw-json-actions">
      <button class="btn" type="button" id="copy-raw-json">复制 JSON</button>
    </div>
  </dialog>

  <script>
    const state = {
      config: null,
      apiKeys: null,
      apiKeysByExchange: { okx: null, binance: null },
      apiKeyExchange: "okx",
      selectedAPIID: "",
      selectedAPIIDs: { okx: "", binance: "" },
      apiKeyTest: null,
      apiKeyTestID: "",
      apiKeyTestExchange: "okx",
      orders: [],
      ordersTotal: 0,
      ordersPage: 1,
      ordersSearch: "",
      ordersStatus: "",
      coinpairBlocks: null,
      coinpairCooldownSubmitting: {},
      coinpairCooldownRemoving: {},
      retrying: {},
      positionClosing: {},
      pendingOrderActions: {},
      analysis: null,
      analysisError: "",
      selectedExchange: "okx",
      analysisTradePage: 1,
      symbolPrecisions: {},
      balanceOverview: null,
      balanceOverviewError: "",
      balanceWindowMinutes: 720,
      analysisPositions: null,
      analysisPositionsError: "",
      positions: null,
      positionsError: "",
      pendingOrders: null,
      pendingOrderGroup: "normal",
      localPendingLimitCloses: [],
      pendingOrdersError: "",
      symbols: null,
      symbolsError: "",
      symbolSort: null,
      symbolMarketInitialized: false,
      tradeMonitor: null,
      tradeMonitorError: "",
      upgrade: null
    };
    let positionViewPollTimer = null;
    let positionViewPollBusy = false;
    let positionEntryTimeSyncTimer = null;
    let positionEntryTimeSyncBusy = false;
    let analysisBalanceRefreshTimer = null;
    let analysisBalanceRefreshBusy = false;
    let coinpairBlockPollTimer = null;
    let coinpairBlockPollBusy = false;
    let coinpairBlockCountdownTimer = null;
    let menuSettingsSynced = false;
    let tableColumnDrag = null;
    let tableColumnDropSuppressClick = false;
    let templateTitleGeneratedAt = new Date();
    const positionViewPollIntervalMs = 5000;
    const pendingLimitCloseOrderCacheMs = 90000;
    const missingPositionEntrySyncIntervalMs = 180000;
    const analysisBalanceRefreshIntervalMs = 60000;
    const coinpairBlockPollIntervalMs = 20000;
    const defaultMenuItems = [
      { tab: "dashboard", label: "总览" },
      { tab: "positions", label: "持仓" },
      { tab: "analysis", label: "订单分析" },
      { tab: "symbols", label: "币对配置" },
      { tab: "config", label: "订单配置" },
      { tab: "apiKeys", label: "API Key" },
      { tab: "orderSettings", label: "下单设置" },
      { tab: "template", label: "告警模板" },
      { tab: "orders", label: "订单" },
      { tab: "tradeMonitor", label: "成交监听" },
      { tab: "menuSettings", label: "菜单设置", locked: true },
      { tab: "upgrade", label: "升级" }
    ];
    const positionExchanges = ["okx", "binance"];
    const globalExchangeStorageKey = "tvbot.selectedExchange";
    const ordersPageSize = 20;
    const analysisTradePageSize = 20;
    const analysisTradeColumnStorageKey = "tvbot.analysisTradeColumns.v3";
    const analysisTradeColumnDefs = [
      { id: "exit_time", title: "平仓时间", colClass: "analysis-exit-time-col", tdClass: "time", render: (row) => shanghaiTime(row.exit_time) },
      { id: "inst_id", title: "币对", colClass: "analysis-inst-id-col", render: (row) => analysisInstIDText(row.inst_id) },
      { id: "side", title: "方向", colClass: "analysis-side-col", toneClass: (row) => analysisPositionSideToneClass(row.side), render: (row) => analysisPositionSideText(row.side) },
      { id: "entry_px", title: "开仓价", colClass: "analysis-entry-px-col", render: (row) => analysisAmountText(row.entry_px) },
      { id: "exit_px", title: "平仓价", colClass: "analysis-exit-px-col", render: (row) => analysisAmountText(row.exit_px) },
      { id: "qty", title: "数量", colClass: "analysis-qty-col", render: (row) => analysisAmountText(row.qty) },
      { id: "margin", title: "保证金", colClass: "analysis-margin-col", render: (row) => analysisAmountText(row.margin) },
      { id: "leverage", title: "杠杆", colClass: "analysis-leverage-col", render: (row) => row.leverage ? asText(row.leverage) + "x" : "-" },
      { id: "realized_pnl", title: "盈亏", colClass: "analysis-realized-pnl-col", signedField: "realized_pnl", renderHTML: (row) => analysisCooldownCellHTML(row) },
      { id: "pnl_ratio", title: "盈亏比", colClass: "analysis-pnl-ratio-col", toneClass: (row) => signedToneClass(analysisPnLRatioValue(row)), render: (row) => analysisPnLRatioText(row) },
      { id: "fee", title: "手续费", colClass: "analysis-fee-col", render: (row) => tradeFeeText(row) },
      { id: "net_pnl", title: "净盈亏", colClass: "analysis-net-pnl-col", signedField: "net_pnl", render: (row) => analysisAmountText(row.net_pnl) },
      { id: "turnover", title: "成交额", colClass: "analysis-turnover-col", render: (row) => analysisAmountText(row.turnover) },
      { id: "fill_count", title: "成交笔数", colClass: "analysis-fill-count-col", render: (row) => asText(row.fill_count || 1) }
    ];
    const balanceWindowOptions = [
      { minutes: 0, label: "当前" },
      { minutes: 5, label: "5m" },
      { minutes: 10, label: "10m" },
      { minutes: 15, label: "15m" },
      { minutes: 30, label: "30m" },
      { minutes: 60, label: "1h" },
      { minutes: 240, label: "4h" },
      { minutes: 480, label: "8h" },
      { minutes: 720, label: "12h" },
      { minutes: 1440, label: "24h" },
      { minutes: 2880, label: "48h" },
      { minutes: 4320, label: "3d" },
      { minutes: 10080, label: "7d" },
      { minutes: 43200, label: "30d" },
      { minutes: 129600, label: "90d" }
    ];
    const $ = (id) => document.getElementById(id);

    const positionTableColumnDefs = [
      { id: "exchange", title: "交易所", colClass: "pos-exchange-col", cell: (row) => textTableCell(exchangeLabel(normalizeExchange(row._exchange || "okx"))) },
      { id: "symbol", title: "币对", colClass: "pos-symbol-col", cell: (row) => textTableCell(displayInstID(row.instId)) },
      { id: "side", title: "方向", colClass: "pos-side-col", cell: (row) => positionSideCell(row) },
      { id: "strategy", title: "策略", colClass: "pos-strategy-col", cell: (row) => textTableCell(positionMarketStrategyText(row.market_strategy)) },
      { id: "entry_adx", title: "入场 ADX", colClass: "pos-adx-col", cell: (row) => positionEntryADXCell(row) },
      { id: "size", title: "持仓量", colClass: "pos-size-col", cell: (row) => textTableCell(formatQuantityAmount(row, row.pos)) },
      { id: "avg_price", title: "均价", colClass: "pos-price-col", cell: (row) => textTableCell(formatPriceAmount(row, row.avgPx)) },
      { id: "margin", title: "保证金", colClass: "pos-margin-col", cell: (row) => textTableCell(formatFixed2(row.margin)) },
      { id: "leverage", title: "杠杆", colClass: "pos-leverage-col", cell: (row) => textTableCell(asText(row.lever)) },
      { id: "position_amount", title: "仓位金额", colClass: "pos-position-amount-col", cell: (row) => textTableCell(positionAmount(row)) },
      { id: "mark_price", title: "标记价", colClass: "pos-price-col", cell: (row) => textTableCell(formatPriceAmount(row, row.markPx)) },
      { id: "upl", title: "未实现盈亏", colClass: "pos-pnl-col", cell: (row) => signedCell(row.upl, formatNumber(row.upl)) },
      { id: "return_rate", title: "收益率", colClass: "pos-rate-col", cell: (row) => signedCell(positionReturnRatio(row), positionReturnPercent(row)) },
      { id: "entry_time", title: "下单时间", colClass: "pos-entry-time-col", cell: (row) => positionEntryTimeCell(row) },
      { id: "holding_time", title: "持仓时间", colClass: "pos-holding-time-col", cell: (row) => positionHoldingTimeCell(row) },
      { id: "actions", title: "操作", colClass: "pos-actions-col", cell: (row) => positionActionCell(row) }
    ];
    const pendingOrderTableColumnDefs = [
      { id: "exchange", title: "交易所", colClass: "pending-exchange-col", cell: (row) => textTableCell(exchangeLabel(normalizeExchange(row._exchange || "okx"))) },
      { id: "time", title: "时间", colClass: "pending-time-col", cell: (row) => timeTableCell(shanghaiTimeFromOKX(row.cTime || row.uTime)) },
      { id: "symbol", title: "币对", colClass: "pending-symbol-col", cell: (row) => textTableCell(displayInstID(row.instId)) },
      { id: "side", title: "方向", colClass: "pending-side-col", cell: (row) => textTableCell(pendingOrderDirectionText(row)) },
      { id: "position_side", title: "持仓方向", colClass: "pending-pos-side-col", cell: (row) => textTableCell(positionSideText(row.posSide, "")) },
      { id: "type", title: "类型", colClass: "pending-type-col", cell: (row) => textTableCell(pendingOrderTypeText(row)) },
      { id: "price", title: "委托价格", colClass: "pending-price-col", cell: (row) => textTableCell(pendingOrderPriceText(row)) },
      { id: "mid_price", title: "中间价", colClass: "pending-mid-col", cell: (row) => textTableCell(row.price_error ? row.price_error : formatPriceAmount(row, row.mid_px)) },
      { id: "size", title: "委托量", colClass: "pending-size-col", cell: (row) => textTableCell(formatQuantityAmount(row, row.sz)) },
      { id: "margin", title: "保证金", colClass: "pending-margin-col", cell: (row) => textTableCell(formatFixed2(row.margin)) },
      { id: "filled", title: "已成交", colClass: "pending-filled-col", cell: (row) => textTableCell(formatQuantityAmount(row, row.accFillSz)) },
      { id: "pending_age", title: "挂单计时", colClass: "pending-age-col", cell: (row) => pendingOrderAgeCell(row) },
      { id: "state", title: "状态", colClass: "pending-state-col", cell: (row) => textTableCell(pendingOrderStateText(row.state)) },
      { id: "actions", title: "操作", colClass: "pending-actions-col", cell: (row) => pendingOrderActionCell(row) }
    ];
    const symbolTableColumnDefs = [
      { id: "env", title: "交易所 / 环境", colClass: "symbol-env-col", sortType: "text", cell: (row) => tableCell(pill(symbolExchangeEnvText(row), row.env === "live" ? "ok" : "warn")), sortValue: (row) => symbolExchangeEnvText(row) },
      { id: "symbol", title: "币对", colClass: "symbol-symbol-col", sortType: "text", cell: (row) => symbolTemplateButtonCell(row), sortValue: (row) => symbolInstID(row) },
      { id: "configured", title: "本地配置", colClass: "symbol-configured-col", sortType: "text", cell: (row) => symbolConfiguredCell(row), sortValue: (row) => symbolConfigured(row) ? "1" : "0" },
      { id: "state", title: "状态", colClass: "symbol-state-col", sortType: "text", cell: (row) => textTableCell(asText(symbolState(row))), sortValue: (row) => symbolState(row) },
      { id: "base_quote", title: "基础 / 计价", colClass: "symbol-base-quote-col", sortType: "text", cell: (row) => textTableCell(symbolBaseQuoteText(row)), sortValue: (row) => symbolBaseQuoteText(row) },
      { id: "settle", title: "结算币", colClass: "symbol-settle-col", sortType: "text", cell: (row) => textTableCell(asText(symbolSettle(row))), sortValue: (row) => symbolSettle(row) },
      { id: "ct_val", title: "合约面值", colClass: "symbol-ct-val-col", sortType: "number", cell: (row) => textTableCell(valueWithUnit(symbolCtVal(row), symbolCtValUnit(row))), sortValue: (row) => symbolCtVal(row) },
      { id: "min_size", title: "最小下单", colClass: "symbol-min-size-col", sortType: "number", cell: (row) => textTableCell(asText(symbolMinSize(row))), sortValue: (row) => symbolMinSize(row) },
      { id: "lot_size", title: "数量步长", colClass: "symbol-lot-size-col", sortType: "number", cell: (row) => textTableCell(asText(symbolLotSize(row))), sortValue: (row) => symbolLotSize(row) },
      { id: "leverage", title: "杠杆", colClass: "symbol-leverage-col", sortType: "number", cell: (row) => textTableCell(asText(symbolLeverage(row))), sortValue: (row) => symbolLeverage(row) },
      { id: "turnover", title: "今日累计成交金额", colClass: "symbol-turnover-col", sortType: "number", defaultSort: "desc", cell: (row) => textTableCell(formatSymbolTurnover(symbolTurnover(row))), sortValue: (row) => symbolTurnover(row) }
    ];

    function tableColumnDefs(tableID) {
      if (tableID === "pending_orders") return pendingOrderTableColumnDefs;
      if (tableID === "symbols") return symbolTableColumnDefs;
      return positionTableColumnDefs;
    }

    function tableColumnPartIDs(tableID) {
      if (tableID === "pending_orders") return { cols: "pending-order-cols", head: "pending-order-head" };
      if (tableID === "symbols") return { cols: "symbol-cols", head: "symbol-head" };
      return { cols: "position-cols", head: "position-head" };
    }

    function normalizeTableColumnOrder(tableID, order) {
      const defs = tableColumnDefs(tableID);
      const known = {};
      const seen = {};
      const out = [];
      const hadPendingAge = Array.isArray(order) && order.some((raw) => String(raw || "").trim() === "pending_age");
      defs.forEach((def) => { known[def.id] = true; });
      (Array.isArray(order) ? order : []).forEach((raw) => {
        const id = String(raw || "").trim();
        if (!known[id] || seen[id]) return;
        out.push(id);
        seen[id] = true;
      });
      defs.forEach((def) => {
        if (!seen[def.id]) out.push(def.id);
      });
      if (tableID === "pending_orders" && !hadPendingAge) {
        moveColumnBefore(out, "pending_age", "state");
      }
      return out;
    }

    function moveColumnBefore(order, columnID, beforeID) {
      const from = order.indexOf(columnID);
      const to = order.indexOf(beforeID);
      if (from < 0 || to < 0 || from === to - 1) return order;
      order.splice(from, 1);
      const nextTo = order.indexOf(beforeID);
      if (nextTo >= 0) order.splice(nextTo, 0, columnID);
      return order;
    }

    function currentTableColumnOrder(tableID) {
      const ui = state.config && state.config.ui ? state.config.ui : {};
      const columns = ui.table_columns || {};
      return normalizeTableColumnOrder(tableID, columns[tableID]);
    }

    function setCurrentTableColumnOrder(tableID, order) {
      if (!state.config) state.config = {};
      if (!state.config.ui) state.config.ui = {};
      if (!state.config.ui.table_columns) state.config.ui.table_columns = {};
      state.config.ui.table_columns[tableID] = normalizeTableColumnOrder(tableID, order);
    }

    function currentTableColumnDefs(tableID) {
      const byID = {};
      tableColumnDefs(tableID).forEach((def) => { byID[def.id] = def; });
      return currentTableColumnOrder(tableID).map((id) => byID[id]).filter(Boolean);
    }

    function tableColumnCount(tableID) {
      return currentTableColumnDefs(tableID).length;
    }

    function renderTableStructure(tableID) {
      const parts = tableColumnPartIDs(tableID);
      const cols = $(parts.cols);
      const head = $(parts.head);
      const columnDefs = currentTableColumnDefs(tableID);
      if (cols) {
        cols.innerHTML = columnDefs.map((col) => '<col class="' + escapeHTML(col.colClass) + '">').join("");
      }
      if (head) {
        head.innerHTML = "<tr>" + columnDefs.map((col) =>
          tableColumnHeaderHTML(tableID, col)
        ).join("") + "</tr>";
      }
    }

    function tableColumnHeaderHTML(tableID, col) {
      const attrs = [
        'draggable="true"',
        'data-table-columns="' + escapeHTML(tableID) + '"',
        'data-column-id="' + escapeHTML(col.id) + '"'
      ];
      let title = "拖动调整栏目顺序";
      let label = col.title;
      if (tableID === "symbols" && col.sortType) {
        attrs.push('data-symbol-sort="' + escapeHTML(col.id) + '"');
        title = "点击排序，拖动调整栏目顺序";
        const sort = state.symbolSort || {};
        if (sort.column === col.id) {
          label += sort.direction === "desc" ? " ▼" : " ▲";
          attrs.push('aria-sort="' + (sort.direction === "desc" ? "descending" : "ascending") + '"');
        }
      }
      attrs.push('title="' + escapeHTML(title) + '"');
      return "<th " + attrs.join(" ") + ">" + escapeHTML(label) + "</th>";
    }

    function currentTableColumnsPatch() {
      return {
        positions: currentTableColumnOrder("positions"),
        pending_orders: currentTableColumnOrder("pending_orders"),
        symbols: currentTableColumnOrder("symbols")
      };
    }

    function renderTableByID(tableID) {
      if (tableID === "pending_orders") {
        renderPendingOrders();
        return;
      }
      if (tableID === "symbols") {
        renderSymbols();
        return;
      }
      renderPositions();
    }

    async function saveTableColumnOrder(tableID, previousOrder) {
      try {
        const patch = { ui: { table_columns: currentTableColumnsPatch() } };
        state.config = await api("/tvbot/config", { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify(patch) });
        toast("栏目顺序已保存");
      } catch (err) {
        setCurrentTableColumnOrder(tableID, previousOrder);
        renderTableByID(tableID);
        toast(err.message);
      }
    }

    function clearTableColumnDropTargets() {
      document.querySelectorAll(".symbol-table th.is-drop-target, .symbol-table th.is-dragging").forEach((node) => {
        node.classList.remove("is-drop-target");
        node.classList.remove("is-dragging");
      });
    }

    function handleTableColumnDragStart(event) {
      const th = event.target.closest("th[data-table-columns][data-column-id]");
      if (!th) return;
      tableColumnDrag = { tableID: th.dataset.tableColumns, columnID: th.dataset.columnId };
      th.classList.add("is-dragging");
      if (event.dataTransfer) {
        event.dataTransfer.effectAllowed = "move";
        event.dataTransfer.setData("text/plain", tableColumnDrag.columnID);
      }
    }

    function handleTableColumnDragOver(event) {
      const th = event.target.closest("th[data-table-columns][data-column-id]");
      if (!th || !tableColumnDrag || th.dataset.tableColumns !== tableColumnDrag.tableID) return;
      event.preventDefault();
      th.classList.add("is-drop-target");
      if (event.dataTransfer) event.dataTransfer.dropEffect = "move";
    }

    function handleTableColumnDragLeave(event) {
      const th = event.target.closest("th[data-table-columns][data-column-id]");
      if (th) th.classList.remove("is-drop-target");
    }

    function handleTableColumnDragEnd() {
      tableColumnDrag = null;
      clearTableColumnDropTargets();
    }

    function handleTableColumnDrop(event) {
      const th = event.target.closest("th[data-table-columns][data-column-id]");
      if (!th || !tableColumnDrag || th.dataset.tableColumns !== tableColumnDrag.tableID) return;
      event.preventDefault();
      const tableID = tableColumnDrag.tableID;
      const fromID = tableColumnDrag.columnID;
      const toID = th.dataset.columnId;
      const previousOrder = currentTableColumnOrder(tableID);
      const fromIndex = previousOrder.indexOf(fromID);
      const toIndex = previousOrder.indexOf(toID);
      tableColumnDrag = null;
      clearTableColumnDropTargets();
      if (tableID === "symbols") {
        tableColumnDropSuppressClick = true;
        window.setTimeout(() => { tableColumnDropSuppressClick = false; }, 0);
      }
      if (fromIndex < 0 || toIndex < 0 || fromIndex === toIndex) return;
      const nextOrder = previousOrder.slice();
      const moved = nextOrder.splice(fromIndex, 1)[0];
      nextOrder.splice(toIndex, 0, moved);
      setCurrentTableColumnOrder(tableID, nextOrder);
      renderTableByID(tableID);
      saveTableColumnOrder(tableID, previousOrder).catch((err) => toast(err.message));
    }

    function initTableColumnDrag() {
      ["position-head", "pending-order-head", "symbol-head"].forEach((id) => {
        const head = $(id);
        if (!head) return;
        head.addEventListener("dragstart", handleTableColumnDragStart);
        head.addEventListener("dragover", handleTableColumnDragOver);
        head.addEventListener("dragleave", handleTableColumnDragLeave);
        head.addEventListener("dragend", handleTableColumnDragEnd);
        head.addEventListener("drop", handleTableColumnDrop);
      });
    }

    async function api(path, options) {
      const res = await fetch(path, Object.assign({ credentials: "same-origin" }, options || {}));
      const text = await res.text();
      let data = null;
      try { data = text ? JSON.parse(text) : null; } catch (_) { data = text; }
      if (!res.ok) {
        const msg = data && data.message ? data.message : res.status + " " + res.statusText;
        throw new Error(msg);
      }
      return data;
    }

    function toast(message) {
      const node = $("toast");
      node.textContent = message;
      node.classList.add("show");
      window.setTimeout(() => node.classList.remove("show"), 2600);
    }

    function asText(v) {
      if (v === null || v === undefined || v === "") return "-";
      return String(v);
    }

    function normalizeExchange(value) {
      const raw = String(value || "").trim().toLowerCase();
      if (raw === "binance" || raw === "binance_usdm" || raw === "binance-usdm" || raw === "usdm") return "binance";
      return "okx";
    }

    function exchangeLabel(value) {
      return normalizeExchange(value) === "binance" ? "Binance" : "OKX";
    }

    function storedSelectedExchange() {
      try {
        return normalizeExchange(window.localStorage.getItem(globalExchangeStorageKey) || "okx");
      } catch (_) {
        return "okx";
      }
    }

    function activeExchange() {
      return normalizeExchange(state.selectedExchange || "okx");
    }

    function sectionActive(tabID) {
      const section = $(tabID);
      return !!(section && section.classList.contains("active"));
    }

    function renderGlobalExchangeSwitch() {
      const selected = activeExchange();
      document.querySelectorAll("[data-global-exchange]").forEach((button) => {
        button.setAttribute("aria-selected", normalizeExchange(button.dataset.globalExchange) === selected ? "true" : "false");
      });
      document.querySelectorAll("[data-exchange-view]").forEach((panel) => {
        panel.hidden = normalizeExchange(panel.dataset.exchangeView) !== selected;
      });
      renderPositionAPIs();
    }

    function setSelectedExchange(exchange) {
      const selected = normalizeExchange(exchange);
      if (selected === activeExchange()) {
        if ($("symbol-exchange")) {
          $("symbol-exchange").value = selected;
          renderSymbols();
        }
        renderGlobalExchangeSwitch();
        return;
      }
      state.selectedExchange = selected;
      state.analysisTradePage = 1;
      state.orders = [];
      state.ordersTotal = 0;
      state.ordersPage = 1;
      state.analysisPositions = null;
      state.analysisPositionsError = "";
      state.positions = null;
      state.pendingOrders = null;
      state.pendingOrderGroup = "normal";
      try {
        window.localStorage.setItem(globalExchangeStorageKey, selected);
      } catch (_) {}
      if ($("symbol-exchange")) {
        $("symbol-exchange").value = selected;
        renderSymbols();
      }
      renderGlobalExchangeSwitch();
      renderDashboard();
      renderAnalysis();
      renderOrders();
      updateMetrics();
      loadOrders(true).catch((err) => toast(err.message));
      if (sectionActive("dashboard")) {
        loadBalanceOverview(true).then(() => renderDashboard()).catch((err) => toast(err.message));
      }
      if (sectionActive("analysis")) {
        loadAnalysis(false).catch((err) => toast(err.message));
      }
      if (sectionActive("positions")) {
        loadPositionView(true).catch((err) => toast(err.message));
      } else {
        renderPositions();
        renderPendingOrders();
      }
    }

    function formatNumber(v) {
      const n = Number(v);
      if (!Number.isFinite(n)) return "-";
      return n.toLocaleString("zh-CN", { minimumFractionDigits: 2, maximumFractionDigits: 6 });
    }

    function formatFixed2(v) {
      const n = Number(v);
      if (!Number.isFinite(n)) return "-";
      return n.toLocaleString("zh-CN", { minimumFractionDigits: 2, maximumFractionDigits: 2 });
    }

    function formatUSD(v) {
      const n = Number(v);
      if (!Number.isFinite(n)) return "-";
      return n.toLocaleString("zh-CN", { minimumFractionDigits: 2, maximumFractionDigits: 2 }) + " USD";
    }

    function formatSymbolTurnover(v) {
      if (v === null || v === undefined || String(v).trim() === "") return "-";
      const n = Number(v);
      if (!Number.isFinite(n)) return "-";
      return n.toLocaleString("zh-CN", { minimumFractionDigits: 2, maximumFractionDigits: 2 }) + " USDT";
    }

    function formatAssetAmount(v) {
      const n = Number(v);
      if (!Number.isFinite(n)) return "-";
      return n.toLocaleString("zh-CN", { minimumFractionDigits: 0, maximumFractionDigits: 8 });
    }

    function displayInstID(v) {
      const raw = asText(v);
      if (raw === "-") return raw;
      const upper = raw.toUpperCase();
      const okxMatch = upper.match(/^([A-Z0-9]+)-USDT-SWAP$/);
      if (okxMatch) return okxMatch[1];
      const binanceMatch = upper.match(/^([A-Z0-9]+)USDT$/);
      if (binanceMatch) return binanceMatch[1];
      return raw;
    }

    function normalizedPrecision(value) {
      const precision = Number(value);
      if (!Number.isFinite(precision) || precision < 0) return null;
      return Math.min(20, Math.floor(precision));
    }

    function formatFixedPrecision(v, precision, fallback) {
      if (v === null || v === undefined || String(v).trim() === "") return "-";
      const n = Number(v);
      if (!Number.isFinite(n)) return "-";
      const digits = normalizedPrecision(precision);
      if (digits === null) return fallback(v);
      return n.toLocaleString("zh-CN", { minimumFractionDigits: digits, maximumFractionDigits: digits });
    }

    function formatPriceAmount(row, value) {
      return formatFixedPrecision(value, row ? row.price_precision : null, formatNumber);
    }

    function formatQuantityAmount(row, value) {
      return formatFixedPrecision(value, row ? row.quantity_precision : null, formatAssetAmount);
    }

    function symbolPrecisionKey(exchange, instID) {
      const normalized = normalizeExchange(exchange);
      const key = normalizePrecisionInstID(normalized, instID);
      return key ? normalized + "|" + key : "";
    }

    function normalizePrecisionInstID(exchange, value) {
      let raw = String(value || "").trim().toUpperCase();
      if (!raw) return "";
      const colon = raw.lastIndexOf(":");
      if (colon >= 0) raw = raw.slice(colon + 1);
      raw = raw.replace(/\.P$/, "").replace(/\.PERP$/, "").replace(/PERP$/, "");
      raw = raw.replace(/\s+/g, "");
      if (normalizeExchange(exchange) === "binance") {
        raw = raw.replace(/[-_/]/g, "");
        if (raw && !raw.endsWith("USDT")) raw += "USDT";
        return raw;
      }
      raw = raw.replace(/_/g, "-").replace(/\//g, "-");
      if (raw.endsWith("-SWAP")) return raw;
      if (raw.includes("-")) {
        const parts = raw.split("-").filter(Boolean);
        if (parts.length >= 2) return parts[0] + "-" + parts[1] + "-SWAP";
      }
      if (raw.endsWith("USDT") && raw.length > 4) return raw.slice(0, -4) + "-USDT-SWAP";
      return raw + "-USDT-SWAP";
    }

    function rememberSymbolPrecision(exchange, row) {
      const key = symbolPrecisionKey(exchange, row && row.instId);
      if (!key) return;
      const pricePrecision = normalizedPrecision(row && row.price_precision);
      const quantityPrecision = normalizedPrecision(row && row.quantity_precision);
      if (pricePrecision === null && quantityPrecision === null) return;
      const current = state.symbolPrecisions[key] || {};
      state.symbolPrecisions[key] = Object.assign({}, current, {
        price_precision: pricePrecision === null ? current.price_precision : pricePrecision,
        quantity_precision: quantityPrecision === null ? current.quantity_precision : quantityPrecision
      });
    }

    function symbolPrecision(exchange, instID) {
      return state.symbolPrecisions[symbolPrecisionKey(exchange, instID)] || null;
    }

    function formatCachedSymbolPrice(exchange, instID, value) {
      const precision = symbolPrecision(exchange, instID);
      return formatFixedPrecision(value, precision ? precision.price_precision : null, formatNumber);
    }

    function formatUSDTBalance(v) {
      const n = Number(v);
      if (!Number.isFinite(n)) return "-";
      return Math.round(n).toLocaleString("zh-CN", { minimumFractionDigits: 0, maximumFractionDigits: 0 });
    }

    function formatPct(v) {
      const n = Number(v);
      if (!Number.isFinite(n)) return "-";
      return (n * 100).toLocaleString("zh-CN", { minimumFractionDigits: 2, maximumFractionDigits: 2 }) + "%";
    }

    function formatFactor(row) {
      if (row && row.profit_factor_text) return row.profit_factor_text;
      return formatNumber(row ? row.profit_factor : null);
    }

    function riskText(v) {
      if (v === "tp_sl") return "固定止盈止损";
      if (v === "trailing") return "移动止损";
      if (v === "none") return "不设置";
      return asText(v);
    }

    function orderTypeText(v) {
      if (v === "limit") return "限价单";
      if (v === "market") return "市价单";
      return asText(v);
    }

    function shanghaiTime(v) {
      if (!v) return "-";
      const date = new Date(v);
      if (Number.isNaN(date.getTime())) return asText(v);
      const parts = new Intl.DateTimeFormat("zh-CN", {
        timeZone: "Asia/Shanghai",
        year: "numeric",
        month: "2-digit",
        day: "2-digit",
        hour: "2-digit",
        minute: "2-digit",
        second: "2-digit",
        hour12: false
      }).formatToParts(date).reduce((acc, part) => {
        if (part.type !== "literal") acc[part.type] = part.value;
        return acc;
      }, {});
      return parts.year + "-" + parts.month + "-" + parts.day + " " + parts.hour + ":" + parts.minute + ":" + parts.second;
    }

    function shanghaiTimeFromOKX(v) {
      if (!v) return "-";
      const raw = String(v).trim();
      if (/^\d+$/.test(raw)) {
        const ms = Number(raw);
        if (Number.isFinite(ms)) return shanghaiTime(new Date(ms).toISOString());
      }
      return shanghaiTime(raw);
    }

    function formatHoldingSeconds(value) {
      const seconds = Number(value);
      if (!Number.isFinite(seconds) || seconds < 0) return "-";
      if (seconds < 60) return "<1分钟";
      const totalMinutes = Math.floor(seconds / 60);
      const days = Math.floor(totalMinutes / 1440);
      const hours = Math.floor((totalMinutes % 1440) / 60);
      const minutes = totalMinutes % 60;
      const hh = String(hours).padStart(2, "0");
      const mm = String(minutes).padStart(2, "0");
      return days > 0 ? (days + "天 " + hh + ":" + mm) : (hh + ":" + mm);
    }

    function exchangeTimestampMs(value) {
      if (!value) return null;
      const raw = String(value).trim();
      if (!raw) return null;
      if (/^\d+$/.test(raw)) {
        const ms = Number(raw);
        return Number.isFinite(ms) && ms > 0 ? ms : null;
      }
      const parsed = Date.parse(raw);
      return Number.isFinite(parsed) ? parsed : null;
    }

    function formatPendingOrderAgeSeconds(value) {
      const seconds = Number(value);
      if (!Number.isFinite(seconds) || seconds < 0) return "-";
      const wholeSeconds = Math.floor(seconds);
      const days = Math.floor(wholeSeconds / 86400);
      const hours = Math.floor((wholeSeconds % 86400) / 3600);
      const minutes = Math.floor((wholeSeconds % 3600) / 60);
      const secs = wholeSeconds % 60;
      const hh = String(hours).padStart(2, "0");
      const mm = String(minutes).padStart(2, "0");
      const ss = String(secs).padStart(2, "0");
      return days > 0 ? (days + "天 " + hh + ":" + mm + ":" + ss) : (hh + ":" + mm + ":" + ss);
    }

    function pendingOrderAgeCell(row) {
      const ms = exchangeTimestampMs(row && (row.cTime || row.uTime));
      if (ms === null) return textTableCell("-");
      const ageSeconds = Math.max(0, Math.floor((Date.now() - ms) / 1000));
      return textTableCell(formatPendingOrderAgeSeconds(ageSeconds));
    }

    function entryTimeSourceText(value) {
      const source = String(value || "").toLowerCase();
      if (source === "okx_fills_history") return "OKX 成交明细";
      if (source === "binance_user_trades") return "Binance 成交明细";
      if (source === "exchange_position_time") return "交易所持仓时间";
      return asText(value);
    }

    function balanceAmount(v) {
      if (v === null || v === undefined || v === "") return "-";
      const formatted = formatNumber(v);
      return (formatted === "-" ? asText(v) : formatted) + " USDT";
    }

    function balanceWindowLabel(minutes) {
      const normalized = Number(minutes || 0);
      const found = balanceWindowOptions.find((item) => item.minutes === normalized);
      if (found) return found.label;
      if (normalized <= 0) return "当前";
      if (normalized % 1440 === 0) return (normalized / 1440) + "d";
      if (normalized % 60 === 0) return (normalized / 60) + "h";
      return normalized + "m";
    }

    function analysisPNLWindowMinutes() {
      const minutes = Math.max(0, Number(state.balanceWindowMinutes || 0));
      if (!Number.isFinite(minutes) || minutes <= 0) return 5;
      return Math.min(minutes, 90 * 24 * 60);
    }

    function balanceOverviewPath(forceRefresh) {
      const qs = new URLSearchParams({ minutes: String(Math.max(0, Number(state.balanceWindowMinutes || 0))) });
      if (forceRefresh) qs.set("refresh", "true");
      return "/tvbot/balances/overview?" + qs.toString();
    }

    function updateBalanceWindowButtons() {
      document.querySelectorAll("[data-balance-minutes]").forEach((button) => {
        const selected = Number(button.dataset.balanceMinutes || "0") === Number(state.balanceWindowMinutes || 0);
        button.setAttribute("aria-selected", selected ? "true" : "false");
      });
    }

    function chartPointDate(point) {
      if (!point) return null;
      if (point.date instanceof Date) return point.date;
      if (point.date) {
        const date = new Date(point.date);
        if (!Number.isNaN(date.getTime())) return date;
      }
      if (point.ts !== null && point.ts !== undefined && point.ts !== "") {
        const ms = Number(point.ts);
        if (Number.isFinite(ms)) return new Date(ms);
      }
      if (!point.time) return null;
      const date = new Date(point.time);
      return Number.isNaN(date.getTime()) ? null : date;
    }

    function chartTimeLabel(date) {
      if (!date || Number.isNaN(date.getTime())) return "";
      const parts = new Intl.DateTimeFormat("zh-CN", {
        timeZone: "Asia/Shanghai",
        month: "2-digit",
        day: "2-digit",
        hour: "2-digit",
        minute: "2-digit",
        hour12: false
      }).formatToParts(date).reduce((acc, part) => {
        if (part.type !== "literal") acc[part.type] = part.value;
        return acc;
      }, {});
      return parts.month + "-" + parts.day + " " + parts.hour + ":" + parts.minute;
    }

    function chartTickIndexes(length, maxTicks) {
      if (length <= 0) return [];
      if (length === 1) return [0];
      const count = Math.min(maxTicks, length);
      const indexes = [];
      for (let i = 0; i < count; i++) {
        indexes.push(Math.round(i * (length - 1) / (count - 1)));
      }
      return Array.from(new Set(indexes));
    }

    function chartAxisValue(v) {
      const formatted = formatUSDTBalance(v);
      return formatted === "-" ? asText(v) : formatted;
    }

    function usdtBalanceDetail(balance) {
      const details = balance && Array.isArray(balance.details) ? balance.details : [];
      return details.find((row) => String(row.ccy || "").toUpperCase() === "USDT") || null;
    }

    function usdtBalanceRawValue(row) {
      const raw = row && row.eq;
      return raw !== undefined && raw !== null && String(raw).trim() !== "" ? raw : "";
    }

    function usdtBalancePoints(balancePoints, balance) {
      const stored = (Array.isArray(balancePoints) ? balancePoints : []).map((point, index) => {
        return {
          index: index,
          value: Number(usdtBalanceRawValue(point)),
          date: chartPointDate(point)
        };
      }).filter((point) => Number.isFinite(point.value));
      if (stored.length) return stored;
      const usdt = usdtBalanceDetail(balance);
      const currentValue = Number(usdtBalanceRawValue(usdt));
      if (!Number.isFinite(currentValue)) return [];
      return [{ index: 0, value: currentValue, date: balance && balance.updated_at ? new Date(balance.updated_at) : null }];
    }

    function pill(text, tone) {
      return '<span class="pill ' + (tone || "") + '">' + escapeHTML(asText(text)) + '</span>';
    }

    function orderRawJSONText(order) {
      const raw = order && order.raw_json ? order.raw_json : "";
      if (!raw) return "";
      if (typeof raw !== "string") return JSON.stringify(raw, null, 2);
      try {
        return JSON.stringify(JSON.parse(raw), null, 2);
      } catch (err) {
        return raw;
      }
    }

    function showOrderRawJSON(order) {
      const text = orderRawJSONText(order);
      if (!text) {
        toast("暂无原始 JSON");
        return;
      }
      $("raw-json-output").textContent = text;
      const dialog = $("raw-json-dialog");
      if (dialog.showModal) {
        dialog.showModal();
      } else {
        dialog.setAttribute("open", "open");
      }
    }

    function closeRawJSONDialog() {
      const dialog = $("raw-json-dialog");
      if (dialog.close) {
        dialog.close();
      } else {
        dialog.removeAttribute("open");
      }
    }

    function escapeHTML(v) {
      return String(v).replace(/[&<>"']/g, (ch) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[ch]));
    }

    function textTableCell(value) {
      return "<td>" + escapeHTML(value) + "</td>";
    }

    function tableCell(html) {
      return "<td>" + html + "</td>";
    }

    function timeTableCell(value) {
      return '<td class="time">' + escapeHTML(value) + "</td>";
    }

    function menuDefinition(tabID) {
      return defaultMenuItems.find((item) => item.tab === tabID) || null;
    }

    function defaultMenuTabByLabel(label) {
      const normalized = String(label || "").trim();
      const found = defaultMenuItems.find((item) => item.label === normalized);
      return found ? found.tab : "";
    }

    function menuLabel(item, def) {
      const label = item && item.label ? String(item.label).trim() : "";
      if (!label) return def ? def.label : "";
      const owner = defaultMenuTabByLabel(label);
      if (owner && def && owner !== def.tab) return def.label;
      return label;
    }

    function normalizeMenuItems(items) {
      const seen = {};
      const normalized = [];
      (Array.isArray(items) ? items : []).forEach((item) => {
        const tab = item && item.tab ? String(item.tab) : "";
        const def = menuDefinition(tab);
        if (!def || seen[tab]) return;
        normalized.push({ tab: tab, hidden: def.locked ? false : !!item.hidden, label: menuLabel(item, def) });
        seen[tab] = true;
      });
      defaultMenuItems.forEach((def) => {
        if (!seen[def.tab]) normalized.push({ tab: def.tab, hidden: false, label: def.label });
      });
      return normalized;
    }

    function currentMenuItems() {
      const ui = state.config && state.config.ui ? state.config.ui : {};
      return normalizeMenuItems(ui.menu_items);
    }

    function setCurrentMenuItems(items) {
      if (!state.config) state.config = {};
      if (!state.config.ui) state.config.ui = {};
      state.config.ui.menu_items = normalizeMenuItems(items);
    }

    function configuredDefaultTab() {
      const ui = state.config && state.config.ui ? state.config.ui : {};
      const tab = ui.default_tab ? String(ui.default_tab) : "dashboard";
      return menuDefinition(tab) && $(tab) ? tab : "dashboard";
    }

    function tabButton(tabID) {
      return Array.from(document.querySelectorAll("nav button")).find((button) => button.dataset.tab === tabID) || null;
    }

    function tabVisible(tabID) {
      const button = tabButton(tabID);
      return !!button && !button.hidden && !!$(tabID);
    }

    function firstVisibleTab() {
      const button = Array.from(document.querySelectorAll("nav button")).find((item) => !item.hidden && $(item.dataset.tab));
      return button ? button.dataset.tab : "menuSettings";
    }

    function effectiveDefaultTab() {
      const tab = configuredDefaultTab();
      return tabVisible(tab) ? tab : firstVisibleTab();
    }

    function activeTabID() {
      const active = document.querySelector('nav button[aria-selected="true"]');
      return active ? active.dataset.tab : "";
    }

    function syncActiveTabAfterMenuSettings() {
      const hash = location.hash ? location.hash.slice(1) : "";
      const active = activeTabID();
      if (!hash && !menuSettingsSynced) {
        menuSettingsSynced = true;
        activateTab(effectiveDefaultTab(), false);
        return;
      }
      if (hash && tabVisible(hash)) {
        menuSettingsSynced = true;
        activateTab(hash, false);
        return;
      }
      menuSettingsSynced = true;
      if (!active || !tabVisible(active)) {
        activateTab(effectiveDefaultTab(), false);
      }
    }

    function applyMenuSettings() {
      const nav = document.querySelector("nav");
      if (!nav) return;
      const known = {};
      currentMenuItems().forEach((item) => {
        const button = tabButton(item.tab);
        const def = menuDefinition(item.tab);
        if (!button || !def) return;
        button.textContent = menuLabel(item, def);
        button.hidden = !!item.hidden;
        nav.appendChild(button);
        known[item.tab] = true;
      });
      document.querySelectorAll("nav button").forEach((button) => {
        if (!known[button.dataset.tab]) button.hidden = true;
      });
      const activeButton = document.querySelector('nav button[aria-selected="true"]');
      if (!activeButton || activeButton.hidden) {
        activateTab(effectiveDefaultTab(), false);
      } else {
        syncOrderInfoVisibility(activeButton.dataset.tab || activeTabID());
      }
    }

    function renderMenuSettings() {
      const items = currentMenuItems();
      const defaultTab = configuredDefaultTab();
      $("menu-settings-rows").innerHTML = items.map((item, index) => {
        const def = menuDefinition(item.tab) || { label: item.tab };
        const hiddenCell = def.locked
          ? '<span class="muted">固定显示</span>'
          : '<label class="menu-hidden-check"><input type="checkbox" data-menu-hidden="' + escapeHTML(item.tab) + '"' + (item.hidden ? " checked" : "") + '>隐藏</label>';
        return "<tr>" +
          '<td><input type="radio" name="menu-default-tab" data-menu-home="' + escapeHTML(item.tab) + '"' + (item.tab === defaultTab ? " checked" : "") + ' aria-label="设为首页"></td>' +
          "<td>" + escapeHTML(def.label) + "</td>" +
          '<td><input class="menu-label-input" data-menu-label="' + escapeHTML(item.tab) + '" value="' + escapeHTML(menuLabel(item, def)) + '" maxlength="24" autocomplete="off" spellcheck="false"></td>' +
          "<td>" + hiddenCell + "</td>" +
          '<td><div class="menu-sort-actions">' +
            '<button class="btn small" type="button" data-menu-index="' + index + '" data-menu-move="-1"' + (index === 0 ? " disabled" : "") + ">上移</button>" +
            '<button class="btn small" type="button" data-menu-index="' + index + '" data-menu-move="1"' + (index === items.length - 1 ? " disabled" : "") + ">下移</button>" +
          "</div></td>" +
          "</tr>";
      }).join("");
    }

    function moveMenuItem(index, direction) {
      const items = currentMenuItems();
      const next = index + direction;
      if (index < 0 || next < 0 || index >= items.length || next >= items.length) return;
      const current = items[index];
      items[index] = items[next];
      items[next] = current;
      setCurrentMenuItems(items);
      renderMenuSettings();
      applyMenuSettings();
    }

    function positionsTabActive() {
      const section = $("positions");
      return !!(section && section.classList.contains("active"));
    }

    function positionEntryTimesMissing(row) {
      const entryTime = String(row && row.entry_fill_time ? row.entry_fill_time : "").trim();
      const holdingSeconds = Number(row && row.holding_seconds);
      return !entryTime || !Number.isFinite(holdingSeconds) || holdingSeconds < 0;
    }

    function missingPositionEntryTimesDetected() {
      const rows = state.positions && Array.isArray(state.positions.positions) ? state.positions.positions : [];
      return rows.some((row) => positionEntryTimesMissing(row));
    }

    function startPositionViewPolling() {
      if (!positionViewPollTimer) {
        positionViewPollTimer = window.setInterval(async () => {
          if (positionViewPollBusy) return;
          positionViewPollBusy = true;
          try {
            await loadPositionView();
          } catch (err) {
            toast(err.message);
          } finally {
            positionViewPollBusy = false;
          }
        }, positionViewPollIntervalMs);
      }
      startMissingPositionEntryTimeSync();
    }

    function stopPositionViewPolling() {
      if (positionViewPollTimer) {
        window.clearInterval(positionViewPollTimer);
        positionViewPollTimer = null;
      }
      stopMissingPositionEntryTimeSync();
      positionViewPollBusy = false;
    }

    function startMissingPositionEntryTimeSync() {
      if (positionEntryTimeSyncTimer) return;
      positionEntryTimeSyncTimer = window.setInterval(() => {
        syncMissingPositionEntryTimes().catch((err) => toast(err.message));
      }, missingPositionEntrySyncIntervalMs);
    }

    function stopMissingPositionEntryTimeSync() {
      if (!positionEntryTimeSyncTimer) return;
      window.clearInterval(positionEntryTimeSyncTimer);
      positionEntryTimeSyncTimer = null;
      positionEntryTimeSyncBusy = false;
    }

    async function syncMissingPositionEntryTimes() {
      if (positionEntryTimeSyncBusy || positionViewPollBusy || !positionsTabActive() || !missingPositionEntryTimesDetected()) return;
      positionEntryTimeSyncBusy = true;
      positionViewPollBusy = true;
      try {
        await loadPositions(true);
      } finally {
        positionEntryTimeSyncBusy = false;
        positionViewPollBusy = false;
      }
    }

    function analysisTabActive() {
      const section = $("analysis");
      return !!(section && section.classList.contains("active"));
    }

    function analysisBalanceAutoRefreshAllowed() {
      return analysisTabActive() && !(document.hidden);
    }

    function startAnalysisBalanceAutoRefresh() {
      if (analysisBalanceRefreshTimer) return;
      analysisBalanceRefreshTimer = window.setInterval(() => {
        refreshAnalysisBalanceOverviewAuto();
      }, analysisBalanceRefreshIntervalMs);
    }

    function stopAnalysisBalanceAutoRefresh() {
      if (!analysisBalanceRefreshTimer) return;
      window.clearInterval(analysisBalanceRefreshTimer);
      analysisBalanceRefreshTimer = null;
      analysisBalanceRefreshBusy = false;
    }

    async function refreshAnalysisBalanceOverviewAuto() {
      if (analysisBalanceRefreshBusy || !analysisBalanceAutoRefreshAllowed()) return;
      analysisBalanceRefreshBusy = true;
      try {
        await Promise.all([loadBalanceOverview(true), loadAnalysisPositions(true)]);
        renderAnalysis();
      } finally {
        analysisBalanceRefreshBusy = false;
      }
    }

    function syncOrderInfoVisibility(tabID) {
      const box = $("order-info-status");
      if (!box) return;
      box.hidden = tabID !== "orderSettings";
    }

    function parsedHashRoute() {
      const raw = location.hash ? location.hash.slice(1) : "";
      const splitAt = raw.indexOf("?");
      const tab = splitAt >= 0 ? raw.slice(0, splitAt) : raw;
      const query = splitAt >= 0 ? raw.slice(splitAt + 1) : "";
      return { tab: tab, params: new URLSearchParams(query) };
    }

    function activateTab(tabID, persist) {
      let target = tabID || "dashboard";
      let button = tabButton(target);
      let section = $(target);
      if (!button || button.hidden || !section) {
        target = firstVisibleTab();
        button = tabButton(target);
        section = $(target);
      }
      document.querySelectorAll("nav button").forEach((b) => b.setAttribute("aria-selected", "false"));
      document.querySelectorAll("main section").forEach((s) => s.classList.remove("active"));
      if (button && section) {
        button.setAttribute("aria-selected", "true");
        section.classList.add("active");
      }
      syncOrderInfoVisibility(target);
      if (persist) {
        if (window.history && location.hash !== "#" + target) {
          history.replaceState(null, "", "#" + target);
        }
      }
      if (target === "positions") {
        startPositionViewPolling();
      } else {
        stopPositionViewPolling();
      }
      if (target === "analysis") {
        startAnalysisBalanceAutoRefresh();
      } else {
        stopAnalysisBalanceAutoRefresh();
      }
      if (target === "orders" || target === "analysis") {
        startCoinpairBlockPolling();
      } else {
        stopCoinpairBlockPolling();
      }
      if (target === "analysis" && !state.analysis) {
        loadAnalysis(false).catch((err) => toast(err.message));
      }
      if (target === "positions" && (!state.positions || !state.pendingOrders)) {
        loadPositionView().catch((err) => toast(err.message));
      }
      if (target === "symbols" && !state.symbols) {
        loadSymbols(true).catch((err) => toast(err.message));
      }
      if (target === "template" && !state.symbols) {
        loadSymbols(false).catch((err) => toast(err.message));
      }
      if (target === "tradeMonitor" && !state.tradeMonitor) {
        loadTradeMonitor().catch((err) => toast(err.message));
      }
    }

    function initialTab() {
      const fromHash = parsedHashRoute().tab;
      const hashButton = tabButton(fromHash);
      if (fromHash && hashButton && !hashButton.hidden && $(fromHash)) return fromHash;
      return effectiveDefaultTab();
    }

    function activeTabID() {
      const section = document.querySelector("main section.active");
      return section ? section.id : initialTab();
    }

    async function loadAll() {
      await Promise.allSettled([loadConfig(), loadAPIKeys(), loadOrders(), loadCoinpairBlocks(), loadUpgrade()]);
      const target = activeTabID();
      const tabLoads = [];
      if (target === "dashboard") {
        tabLoads.push(loadBalanceOverview(false));
      } else if (target === "analysis") {
        tabLoads.push(loadAnalysis(false));
      } else if (target === "positions") {
        tabLoads.push(loadPositionView(false));
      } else if (target === "tradeMonitor") {
        tabLoads.push(loadTradeMonitor());
      }
      await Promise.allSettled(tabLoads);
      renderDashboard();
    }

    async function loadConfig() {
      state.config = await api("/tvbot/config");
      applyMenuSettings();
      syncActiveTabAfterMenuSettings();
      renderConfig();
      initializeSymbolMarket();
      renderOrderSettings();
      renderPositionMonitor();
      renderMenuSettings();
      renderTemplateCoinpairs();
      renderPositions();
      renderPendingOrders();
      updateMetrics();
    }

    async function loadAPIKeys() {
      const [okxResult, binanceResult] = await Promise.allSettled([
        api("/tvbot/api-keys?exchange=okx"),
        api("/tvbot/api-keys?exchange=binance")
      ]);
      state.apiKeysByExchange.okx = okxResult.status === "fulfilled" ? okxResult.value : { configured: false, credentials: [], error: okxResult.reason ? okxResult.reason.message : "OKX API Key 读取失败" };
      state.apiKeysByExchange.binance = binanceResult.status === "fulfilled" ? binanceResult.value : { configured: false, credentials: [], error: binanceResult.reason ? binanceResult.reason.message : "Binance API Key 读取失败" };
      state.apiKeys = apiKeyStatus(state.apiKeyExchange);
      renderAPIKeys();
      renderTemplateAPIs();
      renderPositionAPIs();
      renderOrders();
      updateMetrics();
    }

    async function loadBalanceOverview(forceRefresh) {
      try {
        state.balanceOverview = await api(balanceOverviewPath(!!forceRefresh));
        state.balanceOverviewError = "";
      } catch (err) {
        state.balanceOverview = null;
        state.balanceOverviewError = err.message;
      }
      renderBalanceOverview();
    }

    function ordersTotalPages() {
      return Math.max(1, Math.ceil(Number(state.ordersTotal || 0) / ordersPageSize));
    }

    async function loadOrders(resetPage) {
      if (resetPage) state.ordersPage = 1;
      state.ordersPage = Math.max(1, Number(state.ordersPage || 1));
      const offset = (state.ordersPage - 1) * ordersPageSize;
      const qs = new URLSearchParams({ limit: String(ordersPageSize), offset: String(offset), exchange: activeExchange() });
      state.ordersSearch = String(state.ordersSearch || "").trim();
      state.ordersStatus = String(state.ordersStatus || "").trim().toLowerCase();
      if (state.ordersSearch) qs.set("q", state.ordersSearch);
      if (state.ordersStatus) qs.set("status", state.ordersStatus);
      const data = await api("/tvbot/orders?" + qs.toString());
      state.orders = data.orders || [];
      state.ordersTotal = Number(data.total || 0);
      const totalPages = ordersTotalPages();
      if (state.ordersPage > totalPages) {
        state.ordersPage = totalPages;
        return loadOrders(false);
      }
      renderOrders();
      updateMetrics();
    }

    async function loadCoinpairBlocks() {
      const data = await api("/tvbot/coinpair-blocks");
      state.coinpairBlocks = Array.isArray(data.blocks) ? data.blocks : [];
      renderIgnoredCoinpairFilter();
      if (state.analysis) renderAnalysisTradeHistory("");
    }

    function startCoinpairBlockPolling() {
      if (!coinpairBlockPollTimer) {
        coinpairBlockPollTimer = window.setInterval(async () => {
          const activeTab = activeTabID();
          if (coinpairBlockPollBusy || (activeTab !== "orders" && activeTab !== "analysis")) return;
          coinpairBlockPollBusy = true;
          try {
            await loadCoinpairBlocks();
          } catch (err) {
            toast(err.message);
          } finally {
            coinpairBlockPollBusy = false;
          }
        }, coinpairBlockPollIntervalMs);
      }
      if (!coinpairBlockCountdownTimer) {
        coinpairBlockCountdownTimer = window.setInterval(() => {
          renderCoinpairCooldownList();
          syncAnalysisCooldownButtons();
        }, 1000);
      }
      if (state.coinpairBlocks === null && !coinpairBlockPollBusy) {
        coinpairBlockPollBusy = true;
        loadCoinpairBlocks().catch((err) => toast(err.message)).finally(() => { coinpairBlockPollBusy = false; });
      } else {
        renderCoinpairCooldownList();
      }
    }

    function stopCoinpairBlockPolling() {
      if (coinpairBlockPollTimer) window.clearInterval(coinpairBlockPollTimer);
      if (coinpairBlockCountdownTimer) window.clearInterval(coinpairBlockCountdownTimer);
      coinpairBlockPollTimer = null;
      coinpairBlockCountdownTimer = null;
    }

    async function loadUpgrade() {
      state.upgrade = await api("/upgrade");
      renderUpgrade();
      updateMetrics();
    }

    async function loadTradeMonitor() {
      try {
        state.tradeMonitor = await api("/tvbot/trade-monitor?exchange=binance");
        const errors = Array.isArray(state.tradeMonitor.errors) ? state.tradeMonitor.errors : [];
        state.tradeMonitorError = errors.join(" / ");
      } catch (err) {
        state.tradeMonitor = null;
        state.tradeMonitorError = err.message;
        renderTradeMonitor();
        throw err;
      }
      renderTradeMonitor();
      renderDashboard();
    }

    async function loadAnalysis(refresh) {
      const qs = new URLSearchParams({ price_days: "3", pnl_minutes: String(analysisPNLWindowMinutes()) });
      if (refresh) qs.set("refresh", "true");
      await Promise.all([loadBalanceOverview(!!refresh), loadAnalysisPositions(!!refresh)]);
      try {
        state.analysis = await api("/tvbot/analysis?" + qs.toString());
        state.analysisError = "";
        state.analysisTradePage = 1;
      } catch (err) {
        state.analysis = null;
        state.analysisError = err.message;
        state.analysisTradePage = 1;
      }
      renderAnalysis();
    }

    async function loadAnalysisPositions(forceRefresh) {
      const results = await Promise.all(positionExchanges.map((exchange) => loadPositionExchange(exchange, !!forceRefresh)));
      const rows = [];
      results.forEach((result) => {
        const exchange = normalizeExchange(result.exchange);
        const apiID = result.api_id || "";
        (Array.isArray(result.positions) ? result.positions : []).forEach((row) => {
          const view = Object.assign({}, row, { _exchange: exchange, _api_id: apiID });
          rememberSymbolPrecision(exchange, view);
          rows.push(view);
        });
      });
      state.analysisPositions = {
        ok: results.some((result) => result.ok),
        count: rows.length,
        refreshed_at: combinedRefreshedAt(results),
        exchanges: results,
        positions: rows
      };
      state.analysisPositionsError = combinedErrors(results);
    }

    async function loadPositions(forceRefresh) {
      const results = await Promise.all([loadPositionExchange(activeExchange(), !!forceRefresh)]);
      const rows = [];
      results.forEach((result) => {
        const exchange = normalizeExchange(result.exchange);
        const apiID = result.api_id || "";
        (Array.isArray(result.positions) ? result.positions : []).forEach((row) => {
          const view = Object.assign({}, row, { _exchange: exchange, _api_id: apiID });
          rememberSymbolPrecision(exchange, view);
          rows.push(view);
        });
      });
      state.positions = {
        ok: results.some((result) => result.ok),
        count: rows.length,
        refreshed_at: combinedRefreshedAt(results),
        exchanges: results,
        positions: rows
      };
      state.positionsError = combinedErrors(results);
      renderPositions();
    }

    async function loadPendingOrders() {
      const results = await Promise.all([loadPendingOrdersExchange(activeExchange())]);
      const rows = [];
      const algoRows = [];
      let normalCount = 0;
      let algoCount = 0;
      results.forEach((result) => {
        const exchange = normalizeExchange(result.exchange);
        const apiID = result.api_id || "";
        (Array.isArray(result.orders) ? result.orders : []).forEach((row) => {
          const view = Object.assign({}, row, { _exchange: exchange, _api_id: apiID });
          rememberSymbolPrecision(exchange, view);
          rows.push(view);
        });
        (Array.isArray(result.algo_orders) ? result.algo_orders : []).forEach((row) => {
          const view = Object.assign({}, row, { _exchange: exchange, _api_id: apiID });
          rememberSymbolPrecision(exchange, view);
          algoRows.push(view);
        });
        normalCount += pendingOrderNormalCount(result);
        algoCount += pendingOrderAlgoCount(result);
      });
      const mergedRows = mergeLocalPendingLimitCloseOrders(rows);
      const localCount = Math.max(0, mergedRows.length - rows.length);
      state.pendingOrders = {
        ok: results.some((result) => result.ok),
        count: normalCount + localCount,
        normal_count: normalCount + localCount,
        algo_count: algoCount,
        total_count: normalCount + algoCount + localCount,
        local_pending_count: localCount,
        refreshed_at: combinedRefreshedAt(results),
        exchanges: results,
        orders: mergedRows,
        algo_orders: algoRows
      };
      state.pendingOrdersError = combinedErrors(results);
      renderPendingOrders();
    }

    async function loadPositionExchange(exchange, forceRefresh) {
      const qs = new URLSearchParams({ inst_type: "SWAP", exchange });
      const apiID = activeAPIID(exchange);
      if (apiID) qs.set("api_id", apiID);
      if (forceRefresh) qs.set("refresh", "true");
      try {
        const result = await api("/tvbot/positions?" + qs.toString());
        result.exchange = normalizeExchange(result.exchange || exchange);
        return result;
      } catch (err) {
        return positionExchangeError(exchange, err);
      }
    }

    async function loadPendingOrdersExchange(exchange) {
      const qs = new URLSearchParams({ inst_type: "SWAP", exchange });
      const apiID = activeAPIID(exchange);
      if (apiID) qs.set("api_id", apiID);
      try {
        const result = await api("/tvbot/pending-orders?" + qs.toString());
        result.exchange = normalizeExchange(result.exchange || exchange);
        return result;
      } catch (err) {
        const result = positionExchangeError(exchange, err);
        result.orders = [];
        result.algo_orders = [];
        result.normal_count = 0;
        result.algo_count = 0;
        result.total_count = 0;
        return result;
      }
    }

    function positionExchangeError(exchange, err) {
      return {
        ok: false,
        exchange: normalizeExchange(exchange),
        api_id: "",
        count: 0,
        normal_count: 0,
        algo_count: 0,
        total_count: 0,
        refreshed_at: "",
        positions: [],
        error: err && err.message ? err.message : String(err || "读取失败")
      };
    }

    function combinedErrors(results) {
      return results
        .filter((result) => result && result.error)
        .map((result) => exchangeLabel(result.exchange) + ": " + result.error)
        .join(" / ");
    }

    function combinedRefreshedAt(results) {
      return results.reduce((latest, result) => {
        const value = result && result.refreshed_at ? new Date(result.refreshed_at).getTime() : 0;
        if (!Number.isFinite(value) || value <= 0) return latest;
        return value > latest ? value : latest;
      }, 0);
    }

    function combinedStatusText(response) {
      const exchanges = response && Array.isArray(response.exchanges) ? response.exchanges : [];
      if (exchanges.length === 0) return "-";
      return exchanges.map((result) => {
        const label = exchangeLabel(result.exchange);
        if (result && result.refreshed_at) return label + " " + shanghaiTime(result.refreshed_at);
        if (result && result.error) return label + " 失败";
        return label + " -";
      }).join(" / ");
    }

    function pendingOrderCountValue(result, key, fallback) {
      const value = Number(result && result[key]);
      if (Number.isFinite(value)) return Math.max(0, Math.trunc(value));
      return Math.max(0, Math.trunc(Number(fallback || 0)));
    }

    function pendingOrderNormalCount(result) {
      const orderCount = Array.isArray(result && result.orders) ? result.orders.length : 0;
      return pendingOrderCountValue(result, "normal_count", pendingOrderCountValue(result, "count", orderCount));
    }

    function pendingOrderAlgoCount(result) {
      const orderCount = Array.isArray(result && result.algo_orders) ? result.algo_orders.length : 0;
      return pendingOrderCountValue(result, "algo_count", orderCount);
    }

    function pendingOrdersSummaryText(response) {
      const exchanges = response && Array.isArray(response.exchanges) ? response.exchanges : [];
      if (exchanges.length === 0) return "-";
      return [activeExchange()].map((exchange) => {
        const result = exchanges.find((item) => normalizeExchange(item && item.exchange) === exchange);
        const localCount = exchange === activeExchange() ? pendingOrderCountValue(response, "local_pending_count", 0) : 0;
        const label = exchangeLabel(exchange);
        if (!result) return label + " -";
        if (!result.ok || result.error) return label + " 失败";
        if (exchange === "okx") return "OKX 普通单 " + (pendingOrderNormalCount(result) + localCount) + " / 算法订单 " + pendingOrderAlgoCount(result);
        if (exchange === "binance") return "Binance 普通单 " + (pendingOrderNormalCount(result) + localCount) + " / 算法单 " + pendingOrderAlgoCount(result);
        return label + " 普通单 " + (pendingOrderNormalCount(result) + localCount) + " / 算法单 " + pendingOrderAlgoCount(result);
      }).join(" . ");
    }

    function pendingOrderGroupCount(group) {
      const response = state.pendingOrders || {};
      if (group === "algo") return pendingOrderCountValue(response, "algo_count", Array.isArray(response.algo_orders) ? response.algo_orders.length : 0);
      return pendingOrderCountValue(response, "normal_count", Array.isArray(response.orders) ? response.orders.length : 0);
    }

    function updatePendingOrderGroupButtons() {
      document.querySelectorAll("[data-pending-order-group]").forEach((button) => {
        const group = button.dataset.pendingOrderGroup === "algo" ? "algo" : "normal";
        button.setAttribute("aria-selected", state.pendingOrderGroup === group ? "true" : "false");
        button.textContent = (group === "algo" ? "算法订单" : "基础订单") + " (" + pendingOrderGroupCount(group) + ")";
      });
    }

    function pendingOrderDisplayRows() {
      if (!state.pendingOrders) return [];
      if (state.pendingOrderGroup === "algo") {
        return Array.isArray(state.pendingOrders.algo_orders) ? state.pendingOrders.algo_orders : [];
      }
      return Array.isArray(state.pendingOrders.orders) ? state.pendingOrders.orders : [];
    }

    async function loadPositionView(forceRefresh) {
      await Promise.all([loadPositions(!!forceRefresh), loadPendingOrders()]);
    }

    async function loadSymbols(showLoading) {
      if (showLoading) {
        $("symbol-rows").innerHTML = '<tr><td colspan="' + tableColumnCount("symbols") + '" class="muted">载入中...</td></tr>';
      }
      try {
        state.symbols = await api("/tvbot/symbols");
        state.symbolsError = "";
      } catch (err) {
        state.symbols = null;
        state.symbolsError = err.message;
        renderSymbols();
        renderTemplateCoinpairs();
        throw err;
      }
      renderSymbols();
      renderTemplateCoinpairs();
    }

    async function syncSymbols(showLoading) {
      if (showLoading) {
        $("symbol-rows").innerHTML = '<tr><td colspan="' + tableColumnCount("symbols") + '" class="muted">同步中...</td></tr>';
      }
      try {
        state.symbols = await api("/tvbot/symbols", { method: "POST" });
        state.symbolsError = "";
      } catch (err) {
        state.symbolsError = err.message;
        renderSymbols();
        renderTemplateCoinpairs();
        throw err;
      }
      renderSymbols();
      renderTemplateCoinpairs();
    }

    function updateMetrics() {
      $("metric-env").textContent = state.config && state.config.trading ? state.config.trading.env : "-";
      $("metric-api-keys").textContent = "OKX " + apiMetricText("okx") + " / Binance " + apiMetricText("binance");
      $("metric-amount").textContent = state.config && state.config.trading ? asText(state.config.trading.order_amount_usdt) + " USDT" : "-";
      $("metric-orders").textContent = Number.isFinite(Number(state.ordersTotal)) ? String(Number(state.ordersTotal || 0)) : (state.orders ? state.orders.length : "-");
    }

    function positionMonitorConfig() {
      const trading = state.config && state.config.trading ? state.config.trading : {};
      const monitor = trading.position_monitor || {};
      return {
        okx_enabled: !!monitor.okx_enabled,
        binance_enabled: !!monitor.binance_enabled,
        poll_interval_seconds: Number(monitor.poll_interval_seconds || 300),
        take_profit_pct: Number(monitor.take_profit_pct || 5),
        stop_loss_pct: Number(monitor.stop_loss_pct || 8)
      };
    }

    function renderPositionMonitor() {
      if (!$("position-monitor-interval")) return;
      const monitor = positionMonitorConfig();
      $("position-monitor-okx-enabled").checked = !!monitor.okx_enabled;
      $("position-monitor-binance-enabled").checked = !!monitor.binance_enabled;
      $("position-monitor-interval").value = monitor.poll_interval_seconds || 300;
      $("position-monitor-tp").value = monitor.take_profit_pct || 5;
      $("position-monitor-sl").value = monitor.stop_loss_pct || 8;
      const active = [
        monitor.okx_enabled ? "OKX" : "",
        monitor.binance_enabled ? "Binance" : ""
      ].filter(Boolean).join(" + ");
      $("position-monitor-status").textContent = (active || "已关闭") +
        " / " + asText(monitor.poll_interval_seconds || 300) + "s" +
        " / +" + asText(monitor.take_profit_pct || 5) + "%" +
        " / -" + asText(monitor.stop_loss_pct || 8) + "%";
    }

    function renderDashboard() {
      if (!state.config) return;
      renderGlobalExchangeSwitch();
      const t = state.config.trading || {};
      const fillMonitor = t.fill_monitor || {};
      const fillMonitorExchanges = Array.isArray(fillMonitor.exchange) ? fillMonitor.exchange.join(", ") : "binance";
      const autoReentry = t.auto_reentry || {};
      const positionMonitor = positionMonitorConfig();
      const positionMonitorExchanges = [
        positionMonitor.okx_enabled ? "OKX" : "",
        positionMonitor.binance_enabled ? "Binance" : ""
      ].filter(Boolean).join(", ");
      const rows = [
        ["服务地址", state.config.server ? state.config.server.addr : "-"],
        ["数据文件", state.config.data_file],
        ["SQLite 数据库", state.config.database_file],
        ["OKX Base URL", t.base_url],
        ["Binance Base URL", t.binance_base_url],
        ["Binance Demo Base URL", t.binance_demo_base_url],
        ["交易环境", t.env],
        ["实盘开关", t.allow_live_trading ? "enabled" : "disabled"],
        ["保证金模式", t.default_margin_mode],
        ["持仓模式", t.position_mode],
        ["信号有效秒数", t.signal_ttl_seconds],
        ["忽略币对", configuredIgnoredCoinpairs().join(", ") || "未启用"],
        ["USDT 下单金额", t.order_amount_usdt],
        ["杠杆", t.leverage],
        ["订单类型", orderTypeText(t.order_type || "market")],
        ["风控模式", riskText(t.risk_type)],
        ["多单限价", "当前价格 x " + asText(t.long_limit_price_multiplier)],
        ["空单限价", "当前价格 x " + asText(t.short_limit_price_multiplier)],
        ["成交监听", fillMonitor.enabled ? ("enabled / " + fillMonitorExchanges + " / " + asText(fillMonitor.poll_interval_seconds) + "s") : "disabled"],
        ["自动补回", autoReentry.enabled ? ("enabled / " + asText(autoReentry.reentry_amount_pct) + "%") : "disabled"],
        ["持仓扫描", positionMonitorExchanges ? (positionMonitorExchanges + " / " + asText(positionMonitor.poll_interval_seconds) + "s / +" + asText(positionMonitor.take_profit_pct) + "% / -" + asText(positionMonitor.stop_loss_pct) + "%") : "disabled"]
      ];
      $("dashboard-config").innerHTML = rows.map((row) => "<tr><th>" + escapeHTML(row[0]) + "</th><td>" + escapeHTML(asText(row[1])) + "</td></tr>").join("");
      renderBalanceOverview();
    }

    function renderBalanceOverview() {
      renderGlobalExchangeSwitch();
      ["okx", "binance"].forEach((exchange) => {
        const item = balanceOverviewExchange(exchange);
        const prefix = "overview-" + exchange;
        const label = exchangeLabel(exchange);
        if (!item) {
          $(prefix + "-status").textContent = state.balanceOverviewError || "-";
          $(prefix + "-eq").textContent = "-";
          $(prefix + "-avail").textContent = "-";
          $(prefix + "-api").textContent = "-";
          $(prefix + "-updated").textContent = "-";
          drawUSDTChart([], prefix + "-usdt-chart", "暂无 " + label + " USDT 估值数据", exchange === "binance" ? "#138a55" : "#1f6feb");
          return;
        }
        const balance = item.balance || {};
        const usdt = usdtBalanceDetail(balance);
        const configured = !!item.configured;
        const statusText = configured ? (item.status === "ok" ? "已更新" : (item.error || item.status || "错误")) : "未配置";
        $(prefix + "-status").textContent = statusText;
        $(prefix + "-eq").textContent = usdt ? formatUSD(usdt.eq_usd || usdt.eq) : "-";
        $(prefix + "-avail").textContent = usdt ? formatUSDTBalance(usdt.avail_bal || usdt.avail_eq) + " USDT" : "-";
        $(prefix + "-api").textContent = item.api_id ? apiDisplayName(item.api_id, exchange) : "-";
        $(prefix + "-updated").textContent = balance.updated_at ? shanghaiTime(balance.updated_at) : (item.refreshed_at ? shanghaiTime(item.refreshed_at) : "-");
        const points = usdtBalancePoints(item.balance_points || [], balance);
        drawUSDTChart(points, prefix + "-usdt-chart", configured ? "暂无 " + label + " USDT 估值数据" : label + " 未配置", exchange === "binance" ? "#138a55" : "#1f6feb");
      });
      renderAnalysisExchangeBalances();
    }

    function balanceOverviewExchange(exchange) {
      const rows = state.balanceOverview && Array.isArray(state.balanceOverview.exchanges) ? state.balanceOverview.exchanges : [];
      return rows.find((row) => normalizeExchange(row.exchange) === normalizeExchange(exchange)) || null;
    }

    function renderConfig() {
      const cfg = state.config || {};
      const trading = cfg.trading || {};
      $("cfg-addr").value = cfg.server && cfg.server.addr ? cfg.server.addr : "";
      $("cfg-data-file").value = cfg.data_file || "";
      $("cfg-database-file").value = cfg.database_file || "";
      $("cfg-base-url").value = trading.base_url || "";
      $("cfg-binance-base-url").value = trading.binance_base_url || "";
      $("cfg-binance-demo-base-url").value = trading.binance_demo_base_url || "";
      $("cfg-env").value = trading.env || "demo";
      $("cfg-margin").value = trading.default_margin_mode || "isolated";
      $("cfg-position").value = trading.position_mode || "net";
      $("cfg-ttl").value = trading.signal_ttl_seconds || 120;
      $("cfg-live").checked = !!trading.allow_live_trading;
      renderMarketTurnoverScope();
      renderIgnoredCoinpairFilter();
    }

    function renderSymbols() {
      const data = state.symbols || {};
      const okxCatalog = data.okx || {};
      const binanceCatalog = data.binance || {};
      const okxLive = okxCatalog.live || {};
      const okxDemo = okxCatalog.demo || {};
      const binanceLive = binanceCatalog.live || {};
      const binanceDemo = binanceCatalog.demo || {};
      const configured = data.symbols || {};
      const keyword = currentSymbolSearchKeyword();
      const rows = sortedSymbolRows(filteredSymbolRows());
      $("symbol-live-count").textContent = asText(symbolSetCount(okxLive) + symbolSetCount(binanceLive));
      $("symbol-demo-count").textContent = asText(symbolSetCount(okxDemo) + symbolSetCount(binanceDemo));
      $("symbol-configured-count").textContent = asText(Object.keys(configured).length);
      $("symbol-visible-count").textContent = asText(rows.length);
      renderMarketTurnoverScope();
      updateSymbolSearchControls(keyword);
      renderTableStructure("symbols");
      const errors = [];
      if (state.symbolsError) errors.push(state.symbolsError);
      collectSymbolSetErrors(errors, "OKX 实盘", okxLive);
      collectSymbolSetErrors(errors, "OKX 模拟", okxDemo);
      collectSymbolSetErrors(errors, "Binance 实盘", binanceLive);
      collectSymbolSetErrors(errors, "Binance 模拟", binanceDemo);
      $("symbol-errors").textContent = errors.join(" / ");
      const columns = currentTableColumnDefs("symbols");
      $("symbol-rows").innerHTML = rows.map((row) => "<tr>" + columns.map((col) => col.cell(row)).join("") + "</tr>").join("") || '<tr><td colspan="' + tableColumnCount("symbols") + '" class="muted">' + escapeHTML(symbolEmptyMessage(keyword)) + '</td></tr>';
    }

    function marketTurnoverScope() {
      const trading = state.config && state.config.trading ? state.config.trading : {};
      const scope = String(trading.market_turnover_scope || "top100").trim().toLowerCase();
      return ["top50", "top100", "top200", "top500", "all"].includes(scope) ? scope : "top100";
    }

    function marketTurnoverScopeLabel(scope) {
      const labels = { top50: "成交量前 50", top100: "成交量前 100", top200: "成交量前 200", top500: "成交量前 500", all: "全部符合条件" };
      return labels[scope] || labels.top100;
    }

    function orderTurnoverScopeLabel(scope) {
      const labels = { top50: "前 50", top100: "前 100", top200: "前 200", top500: "前 500", all: "不限排名" };
      return labels[scope] || labels.top100;
    }

    function renderMarketTurnoverScope() {
      const scope = marketTurnoverScope();
      const select = $("symbol-turnover-scope");
      if (select && !select.disabled) select.value = scope;
      const label = $("symbol-search-label-text");
      if (label) label.textContent = scope === "all" ? "搜索全部符合条件币对" : "搜索" + marketTurnoverScopeLabel(scope) + " 币对";
    }

    async function saveMarketTurnoverScope() {
      const select = $("symbol-turnover-scope");
      if (!select) return;
      const previous = marketTurnoverScope();
      const next = String(select.value || "top100");
      if (next === previous) return;
      select.disabled = true;
      try {
        state.config = await api("/tvbot/config", { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ trading: { market_turnover_scope: next } }) });
      } catch (err) {
        select.value = previous;
        throw err;
      } finally {
        select.disabled = false;
      }
      renderMarketTurnoverScope();
      $("order-turnover-scope").value = marketTurnoverScope();
      renderOrderSettingsPreview();
      await loadSymbols(true);
      toast("交易范围已保存为" + marketTurnoverScopeLabel(next));
    }

    function filteredSymbolRows() {
      const data = state.symbols || {};
      const okxCatalog = data.okx || {};
      const binanceCatalog = data.binance || {};
      const exchangeFilter = $("symbol-exchange") ? normalizeExchange($("symbol-exchange").value) : activeExchange();
      const envFilter = $("symbol-env") && $("symbol-env").value === "live" ? "live" : "demo";
      const terms = symbolSearchTerms(currentSymbolSearchKeyword());
      const configuredLookup = configuredSymbolMap();
      const groups = [
        { exchange: "okx", exchangeLabel: "OKX", env: "live", envLabel: "实盘", set: okxCatalog.live || {} },
        { exchange: "okx", exchangeLabel: "OKX", env: "demo", envLabel: "模拟", set: okxCatalog.demo || {} },
        { exchange: "binance", exchangeLabel: "Binance", env: "live", envLabel: "实盘", set: binanceCatalog.live || {} },
        { exchange: "binance", exchangeLabel: "Binance", env: "demo", envLabel: "模拟", set: binanceCatalog.demo || {} }
      ];
      const rows = [];
      groups.forEach((group) => {
        if (exchangeFilter !== group.exchange || envFilter !== group.env) return;
        const instruments = Array.isArray(group.set.top_instruments) ? group.set.top_instruments : [];
        instruments.forEach((instrument) => {
          const configured = symbolConfiguredByLookup(instrument, configuredLookup);
          if (!symbolSearchMatches(symbolSearchFields(group, instrument, configured), terms)) return;
          rows.push({
            exchange: group.exchange,
            exchangeLabel: group.exchangeLabel,
            env: group.env,
            envLabel: group.envLabel,
            label: symbolExchangeEnvText(group),
            instrument: instrument,
            configured: configured,
            index: rows.length
          });
        });
      });
      return rows;
    }

    function initializeSymbolMarket() {
      if (state.symbolMarketInitialized) return;
      if ($("symbol-exchange")) $("symbol-exchange").value = activeExchange();
      const trading = state.config && state.config.trading ? state.config.trading : {};
      if ($("symbol-env")) $("symbol-env").value = trading.env === "live" ? "live" : "demo";
      state.symbolMarketInitialized = true;
      renderSymbols();
    }

    function currentSymbolSearchKeyword() {
      return $("symbol-search") ? $("symbol-search").value.trim() : "";
    }

    function updateSymbolSearchControls(keyword) {
      const clearButton = $("clear-symbol-search");
      if (clearButton) clearButton.disabled = !keyword;
    }

    function clearSymbolSearch() {
      const input = $("symbol-search");
      if (!input) return;
      input.value = "";
      renderSymbols();
      input.focus();
    }

    function symbolEmptyMessage(keyword) {
      if (keyword) return "没有匹配的币对";
      return state.symbolsError || "暂无币对数据";
    }

    function symbolSearchTerms(keyword) {
      return String(keyword || "").trim().toLowerCase().split(/\s+/).filter(Boolean);
    }

    function symbolSearchCompact(value) {
      return String(value || "").toLowerCase().replace(/[^a-z0-9]+/g, "");
    }

    function symbolSearchMatches(fields, terms) {
      if (!terms.length) return true;
      const raw = fields.map((value) => asText(value)).join(" ").toLowerCase();
      const compact = symbolSearchCompact(raw);
      return terms.every((term) => {
        const compactTerm = symbolSearchCompact(term);
        return raw.includes(term) || (compactTerm && compact.includes(compactTerm));
      });
    }

    function symbolSearchFields(group, instrument, configured) {
      return [
        group.exchange,
        group.exchangeLabel,
        group.env,
        group.envLabel,
        instrument.instId,
        instrument.symbol,
        instrument.baseCcy,
        instrument.baseAsset,
        instrument.quoteCcy,
        instrument.quoteAsset,
        instrument.settleCcy,
        instrument.marginAsset,
        instrument.instFamily,
        instrument.uly,
        instrument.pair,
        configured ? "本地已配置 configured" : "本地未配置 unconfigured"
      ];
    }

    function sortedSymbolRows(rows) {
      const sort = state.symbolSort || {};
      const col = symbolTableColumnDefs.find((def) => def.id === sort.column && def.sortType);
      if (!col) return rows;
      const direction = sort.direction === "desc" ? "desc" : "asc";
      return rows.slice().sort((a, b) => {
        const result = compareSymbolRows(col, a, b, direction);
        if (result !== 0) return result;
        return (a.index || 0) - (b.index || 0);
      });
    }

    function compareSymbolRows(col, a, b, direction) {
      const leftRaw = col.sortValue ? col.sortValue(a) : "";
      const rightRaw = col.sortValue ? col.sortValue(b) : "";
      if (col.sortType === "number") {
        const left = Number(leftRaw);
        const right = Number(rightRaw);
        const leftOK = Number.isFinite(left);
        const rightOK = Number.isFinite(right);
        if (leftOK && rightOK) {
          const result = left === right ? 0 : (left < right ? -1 : 1);
          return direction === "desc" ? -result : result;
        }
        if (leftOK !== rightOK) return leftOK ? -1 : 1;
        return 0;
      }
      const result = String(leftRaw || "").localeCompare(String(rightRaw || ""), "zh-CN", { numeric: true, sensitivity: "base" });
      return direction === "desc" ? -result : result;
    }

    function setSymbolSort(columnID) {
      const col = symbolTableColumnDefs.find((def) => def.id === columnID && def.sortType);
      if (!col) return;
      const current = state.symbolSort || {};
      if (current.column === columnID) {
        state.symbolSort = { column: columnID, direction: current.direction === "desc" ? "asc" : "desc" };
      } else {
        state.symbolSort = { column: columnID, direction: col.defaultSort === "desc" ? "desc" : "asc" };
      }
      renderSymbols();
    }

    function symbolSetCount(set) {
      const count = Number((set || {}).count);
      if (Number.isFinite(count)) return count;
      return Array.isArray((set || {}).instruments) ? set.instruments.length : 0;
    }

    function collectSymbolSetErrors(errors, label, set) {
      set = set || {};
      if (set.error) errors.push(label + ": " + set.error);
      if (set.ticker_error) errors.push(label + " ticker: " + set.ticker_error);
    }

    function symbolExchangeEnvText(row) {
      row = row || {};
      const exchange = row.exchangeLabel || (String(row.exchange || "").toLowerCase() === "binance" ? "Binance" : "OKX");
      const env = row.envLabel || (row.env === "demo" ? "模拟" : "实盘");
      return exchange + " " + env;
    }

    function symbolInstID(row) {
      const inst = row && row.instrument ? row.instrument : {};
      return inst.instId || inst.symbol || "";
    }

    function symbolTemplateButtonCell(row) {
      const symbol = asText(symbolInstID(row));
      const displaySymbol = asText(symbolBase(row && row.instrument ? row.instrument : {}));
      const exchange = normalizeExchange((row || {}).exchange || "okx");
      const env = (row || {}).env === "live" ? "live" : "demo";
      const disabled = symbol ? "" : " disabled";
      return tableCell(
        '<div class="symbol-template-cell"><span class="symbol-template-text">' + escapeHTML(displaySymbol) + '</span>' +
        '<button class="btn small symbol-template-btn" type="button" data-symbol-template="true" data-template-exchange="' + escapeHTML(exchange) + '" data-template-env="' + escapeHTML(env) + '" data-template-symbol="' + escapeHTML(symbol) + '"' + disabled + '>生成报警</button></div>'
      );
    }

    function symbolState(row) {
      const inst = row && row.instrument ? row.instrument : {};
      return inst.state || inst.status || "";
    }

    function symbolSettle(row) {
      const inst = row && row.instrument ? row.instrument : {};
      return inst.settleCcy || inst.marginAsset || "";
    }

    function symbolCtVal(row) {
      const inst = row && row.instrument ? row.instrument : {};
      return inst.ctVal || "";
    }

    function symbolCtValUnit(row) {
      const inst = row && row.instrument ? row.instrument : {};
      return inst.ctValCcy || "";
    }

    function symbolMinSize(row) {
      const inst = row && row.instrument ? row.instrument : {};
      return inst.minSz || inst.min_qty || "";
    }

    function symbolLotSize(row) {
      const inst = row && row.instrument ? row.instrument : {};
      return inst.lotSz || inst.step_size || "";
    }

    function symbolLeverage(row) {
      const inst = row && row.instrument ? row.instrument : {};
      return inst.lever || "";
    }

    function symbolTurnover(row) {
      const inst = row && row.instrument ? row.instrument : {};
      return inst.turnover_usdt_24h || "";
    }

    function symbolConfiguredByLookup(inst, configuredLookup) {
      const base = symbolBase(inst);
      const symbol = symbolInstID({ instrument: inst });
      return !!(configuredLookup[String(symbol || "").toUpperCase()] || configuredLookup[String(base || "").toUpperCase()]);
    }

    function symbolConfigured(row) {
      return !!(row && row.configured);
    }

    function symbolConfiguredCell(row) {
      const configured = symbolConfigured(row);
      return tableCell(pill(configured ? "已配置" : "未配置", configured ? "ok" : ""));
    }

    function symbolBase(inst) {
      inst = inst || {};
      return inst.baseCcy || inst.baseAsset || baseFromInstID(inst.instId || inst.symbol);
    }

    function symbolQuote(inst) {
      inst = inst || {};
      return inst.quoteCcy || inst.quoteAsset || quoteFromInstID(inst.instId || inst.symbol);
    }

    function symbolBaseQuoteText(row) {
      const inst = row && row.instrument ? row.instrument : {};
      return asText(symbolBase(inst)) + " / " + asText(symbolQuote(inst));
    }

    function configuredSymbolMap() {
      const configured = state.symbols && state.symbols.symbols ? state.symbols.symbols : {};
      const out = {};
      Object.keys(configured).forEach((key) => {
        const item = configured[key] || {};
        out[String(key).toUpperCase()] = true;
        if (item.coinpair) out[String(item.coinpair).toUpperCase()] = true;
        if (item.inst_id) out[String(item.inst_id).toUpperCase()] = true;
      });
      return out;
    }

    function baseFromInstID(instID) {
      const raw = String(instID || "");
      if (!raw.includes("-")) {
        if (raw.endsWith("USDT")) return raw.slice(0, -4);
        if (raw.endsWith("USDC")) return raw.slice(0, -4);
      }
      const parts = raw.split("-");
      return parts[0] || "";
    }

    function quoteFromInstID(instID) {
      const raw = String(instID || "");
      if (!raw.includes("-")) {
        if (raw.endsWith("USDT")) return "USDT";
        if (raw.endsWith("USDC")) return "USDC";
      }
      const parts = raw.split("-");
      return parts[1] || "";
    }

    function valueWithUnit(value, unit) {
      const text = asText(value);
      if (text === "-") return text;
      return unit ? text + " " + unit : text;
    }

    function renderOrderSettings() {
      const trading = state.config && state.config.trading ? state.config.trading : {};
      $("order-amount").value = trading.order_amount_usdt || 100;
      $("order-leverage").value = trading.leverage || 5;
      $("order-type").value = trading.order_type || "market";
      $("order-turnover-scope").value = marketTurnoverScope();
      $("order-risk-type").value = trading.risk_type || "tp_sl";
      $("order-tp").value = trading.take_profit_pct || 2;
      $("order-sl").value = trading.stop_loss_pct || 1;
      $("order-trailing").value = trading.trailing_pct || 1;
      $("order-long-multiplier").value = trading.long_limit_price_multiplier || 0.997;
      $("order-short-multiplier").value = trading.short_limit_price_multiplier || 1.003;
      renderOrderSettingsPreview();
    }

    function renderOrderSettingsPreview() {
      const orderType = $("order-type").value || "market";
      const rows = [
        ["订单类型", orderTypeText(orderType)],
        ["24h 交易额排名限制", orderTurnoverScopeLabel($("order-turnover-scope").value)],
        [orderType === "limit" ? "多单限价单价格" : "市价单估算价格", orderType === "limit" ? "TradingView 当前价格 x " + asText($("order-long-multiplier").value) : "TradingView 当前价格"],
        [orderType === "limit" ? "空单限价单价格" : "OKX 下单价格", orderType === "limit" ? "TradingView 当前价格 x " + asText($("order-short-multiplier").value) : "市价"],
        ["固定止盈止损", asText($("order-tp").value) + "% / " + asText($("order-sl").value) + "%"],
        ["移动止损", asText($("order-trailing").value) + "%"]
      ];
      $("order-settings-preview").innerHTML = rows.map((row) => "<tr><th>" + escapeHTML(row[0]) + "</th><td>" + escapeHTML(row[1]) + "</td></tr>").join("");
    }

    function renderAPIKeys() {
      const exchange = normalizeExchange(state.apiKeyExchange);
      const status = apiKeyStatus(exchange);
      state.apiKeys = status;
      const accounts = apiAccounts(exchange);
      const select = $("key-selected");
      const previous = state.selectedAPIIDs[exchange] || select.value || status.active_id || "";
      document.querySelectorAll("[data-api-key-exchange]").forEach((button) => {
        button.setAttribute("aria-selected", normalizeExchange(button.dataset.apiKeyExchange) === exchange ? "true" : "false");
      });
      $("key-api-label").childNodes[0].nodeValue = exchangeLabel(exchange) + " API Key";
      $("key-secret-label").childNodes[0].nodeValue = exchangeLabel(exchange) + " Secret Key";
      $("key-passphrase-label").style.display = exchange === "okx" ? "" : "none";
      select.innerHTML = accounts.map((account) => '<option value="' + escapeHTML(account.id) + '">' + escapeHTML(account.id + (account.name ? " - " + account.name : "") + (account.active ? " (交易)" : "")) + '</option>').join("");
      if (!accounts.length) {
        select.innerHTML = '<option value="default">default - 新 API</option>';
      }
      const selected = accounts.some((account) => account.id === previous) ? previous : (status.active_id || (accounts[0] && accounts[0].id) || "default");
      select.value = selected;
      state.selectedAPIID = selected;
      state.selectedAPIIDs[exchange] = selected;
      fillAPIForm(selected);
      renderAPIKeyStatus(selected);
      $("api-key-accounts").innerHTML = accounts.map((account) => {
        return "<tr>" +
          "<td>" + escapeHTML(account.id) + "</td>" +
          "<td>" + escapeHTML(account.name || "-") + "</td>" +
          "<td>" + pill(account.active ? "交易 API" : (account.configured ? "已配置" : "未配置"), account.active ? "ok" : (account.configured ? "warn" : "bad")) + "</td>" +
          "<td>" + escapeHTML(account.api_key_masked || "-") + "</td>" +
          "</tr>";
      }).join("") || '<tr><td colspan="4" class="muted">-</td></tr>';
    }

    function activeAPIIDStatusHTML(activeID) {
      const apiID = String(activeID || "").trim();
      if (!apiID) return "-";
      const escaped = escapeHTML(apiID);
      return '<div class="api-key-id-row"><span class="api-key-active-id">' + escaped + '</span><button class="btn small" type="button" data-rename-active-api-id="' + escaped + '" title="修改当前交易 API 的 API ID">改名</button></div>';
    }

    function renderAPIKeyStatus(selected) {
      const exchange = normalizeExchange(state.apiKeyExchange);
      const status = apiKeyStatus(exchange);
      const rows = [
        ["交易所", exchangeLabel(exchange)],
        ["配置状态", status.configured ? "已配置" : "未配置"],
        ["交易 API", activeAPIIDStatusHTML(status.active_id), true],
        ["API Key", status.api_key_masked || "-"],
        ["Secret Key", status.secret_key_set ? "已保存" : "未保存"]
      ];
      if (exchange === "okx") rows.push(["Passphrase", status.passphrase_set ? "已保存" : "未保存"]);
      rows.push(["来源", status.source || "-"]);
      rows.push(["更新时间", status.updated_at || "-"]);
      if (status.error) rows.push(["读取错误", status.error]);
      const test = state.apiKeyTest;
      const testID = state.apiKeyTestID || (test && test.api_id) || "";
      if (test && state.apiKeyTestExchange === exchange && (!selected || !testID || selected === testID || test.api_id === "input")) {
        const balance = test.usdt_balance || {};
        rows.push(["测试 API", test.api_id || testID || "-"]);
        if (test.usdt_balance_found && balance) {
          rows.push(["USDT 总权益", balanceAmount(balance.eq)]);
          rows.push(["USDT 可用", balanceAmount(balance.avail_eq || balance.avail_bal)]);
          rows.push(["USDT 冻结", balanceAmount(balance.frozen_bal)]);
          rows.push(["余额更新时间", shanghaiTimeFromOKX(balance.u_time)]);
        } else {
          rows.push(["USDT 余额", exchangeLabel(exchange) + " 未返回 USDT 明细"]);
        }
      }
      $("api-key-status").innerHTML = rows.map((row) => "<tr><th>" + escapeHTML(row[0]) + "</th><td>" + (row[2] ? row[1] : escapeHTML(row[1])) + "</td></tr>").join("");
    }

    function apiKeyStatus(exchange) {
      exchange = normalizeExchange(exchange);
      return state.apiKeysByExchange[exchange] || { configured: false, credentials: [] };
    }

    function apiMetricText(exchange) {
      const status = apiKeyStatus(exchange);
      return status && status.configured ? (status.active_id || "已配置") : "未配置";
    }

    function activeAPIID(exchange) {
      const status = apiKeyStatus(exchange);
      return status && status.configured && status.active_id ? status.active_id : "";
    }

    function apiAccounts(exchange) {
      const status = apiKeyStatus(exchange || state.apiKeyExchange);
      return status && Array.isArray(status.credentials) ? status.credentials : [];
    }

    function selectedAPIAccount(id, exchange) {
      return apiAccounts(exchange).find((account) => account.id === id) || null;
    }

    function apiDisplayName(id, exchange) {
      const apiID = String(id || "").trim();
      if (!apiID) return "-";
      const account = selectedAPIAccount(apiID, exchange) || selectedAPIAccount(apiID, "okx") || selectedAPIAccount(apiID, "binance");
      return account && account.name ? account.name : apiID;
    }

    function fillAPIForm(id) {
      const account = selectedAPIAccount(id, state.apiKeyExchange);
      $("key-id").value = account ? account.id : (id || "default");
      $("key-name").value = account ? (account.name || "") : "";
      $("key-active").checked = account ? !!account.active : true;
      $("key-api").value = "";
      $("key-secret").value = "";
      $("key-passphrase").value = "";
    }

    function renderTemplateAPIs() {
      const exchange = $("tpl-target-exchange") ? normalizeExchange($("tpl-target-exchange").value) : "okx";
      const status = apiKeyStatus(exchange);
      const options = apiAccounts(exchange).map((account) => '<option value="' + escapeHTML(account.id) + '">' + escapeHTML(account.id + (account.name ? " - " + account.name : "") + (account.active ? " (交易)" : "")) + '</option>');
      $("tpl-api-id").innerHTML = '<option value="">默认交易 API</option>' + options.join("");
      if (status && status.active_id) {
        $("tpl-api-id").value = status.active_id;
      }
    }

    function renderTemplateCoinpairs() {
      const input = $("tpl-coinpair");
      const list = $("tpl-coinpair-list");
      if (!input || !list) return;
      const previous = input.value;
      const pairs = templateCoinpairOptions();
      list.innerHTML = pairs.map((pair) => '<option value="' + escapeHTML(pair) + '"></option>').join("");
      if (previous && !pairs.includes(String(previous).trim().toUpperCase())) {
        input.value = previous;
      }
    }

    function templateCoinpairOptions() {
      const seen = {};
      const out = [];
      const add = (value) => {
        const normalized = String(value || "").trim().toUpperCase();
        if (!normalized || seen[normalized]) return;
        seen[normalized] = true;
        out.push(normalized);
      };
      const data = state.symbols || {};
      const exchange = $("tpl-target-exchange") ? normalizeExchange($("tpl-target-exchange").value) : activeExchange();
      const env = templateTradeEnv();
      const catalog = data[exchange] || {};
      const set = catalog[env] || {};
      const instruments = Array.isArray(set.top_instruments) ? set.top_instruments : [];
      instruments.forEach((instrument) => add(symbolInstID({ instrument: instrument })));
      return out.sort((a, b) => a.localeCompare(b));
    }

    function templateTradeEnv() {
      const raw = $("tpl-trade-env") ? String($("tpl-trade-env").value || "").trim().toLowerCase() : "demo";
      return raw === "live" ? "live" : "demo";
    }

    function templateWebhookURL() {
      return new URL("/tvorder", window.location.origin).toString();
    }

    function templatePageURLFromButton(button) {
      const params = new URLSearchParams();
      params.set("target_exchange", normalizeExchange(button.dataset.templateExchange || "okx"));
      params.set("trade_env", button.dataset.templateEnv === "live" ? "live" : "demo");
      params.set("coinpair", String(button.dataset.templateSymbol || "").trim());
      params.set("direction", "both");
      params.set("price_source", "close");
      const url = new URL("/tvbot/", window.location.origin);
      url.hash = "template?" + params.toString();
      return url.toString();
    }

    function renderTemplateWebhookURL() {
      if ($("template-webhook-url")) {
        $("template-webhook-url").value = templateWebhookURL();
      }
    }

    function templateTradeEnvLabel() {
      return templateTradeEnv() === "live" ? "实盘" : "模拟";
    }

    function templateCoinpairTitleText() {
      const raw = $("tpl-coinpair") ? String($("tpl-coinpair").value || "").trim() : "";
      return raw ? raw.toUpperCase() : "{{ticker}}";
    }

    function templateAlertTitle(generatedAt) {
      const exchange = $("tpl-target-exchange") ? normalizeExchange($("tpl-target-exchange").value) : activeExchange();
      return [
        exchangeLabel(exchange),
        templateTradeEnvLabel(),
        templateCoinpairTitleText(),
        shanghaiTime(generatedAt || new Date())
      ].join(" ");
    }

    function renderTemplateTitle(generatedAt) {
      if (generatedAt) templateTitleGeneratedAt = generatedAt;
      if (!$("template-title")) return "";
      const title = templateAlertTitle(templateTitleGeneratedAt);
      $("template-title").value = title;
      return title;
    }

    function renderPositionAPIs() {
      const summary = $("position-exchange-summary");
      if (!summary) return;
      const exchange = activeExchange();
      const status = apiKeyStatus(exchange);
      if (!status || !status.configured) {
        summary.textContent = exchangeLabel(exchange) + " 未配置";
        return;
      }
      const apiID = status && status.active_id ? status.active_id : "default";
      summary.textContent = exchangeLabel(exchange) + " " + apiDisplayName(apiID, exchange);
    }

    function positionSideText(posSide, pos) {
      const kind = positionSideKind(posSide, pos);
      if (kind === "long") return "多单";
      if (kind === "short") return "空单";
      if (kind === "net") return "持仓";
      return asText(posSide);
    }

    function positionEffectText(effect) {
      const value = String(effect || "").toLowerCase();
      if (value === "close" || value === "reduce") return "平仓";
      return "开仓";
    }

    function positionDirectionText(side) {
      const value = String(side || "").toLowerCase();
      if (value === "long") return "多单";
      if (value === "short") return "空单";
      if (value === "net") return "持仓";
      return asText(side);
    }

    function positionMarketStrategyText(strategy) {
      const value = String(strategy || "").toLowerCase();
      if (value === "trend") return "趋势行情";
      if (value === "scalp") return "震荡行情";
      return "-";
    }

    function positionEntryADXCell(row) {
      const raw = row ? row.entry_adx : null;
      if (raw === null || raw === undefined || raw === "") return textTableCell("-");
      const value = Number(raw);
      return textTableCell(Number.isFinite(value) ? value.toFixed(2) : "-");
    }

    function positionSideFromAction(action, effect) {
      const side = String(action || "").toLowerCase();
      const closing = String(effect || "").toLowerCase() === "close";
      if (side === "buy" || side === "long") return closing ? "short" : "long";
      if (side === "sell" || side === "short") return closing ? "long" : "short";
      return "";
    }

    function positionDirectionLabel(effect, side, fallbackAction) {
      const normalizedEffect = String(effect || "").toLowerCase() === "close" ? "close" : "open";
      let normalizedSide = String(side || "").toLowerCase();
      if (!normalizedSide) normalizedSide = positionSideFromAction(fallbackAction, normalizedEffect);
      const direction = positionDirectionText(normalizedSide);
      const effectText = positionEffectText(normalizedEffect);
      return direction === "-" ? effectText : (effectText + " " + direction);
    }

    function orderHistoryDirectionText(order) {
      const result = order && order.result ? order.result : {};
      const effect = order && order.position_effect ? order.position_effect : result.position_effect;
      const side = order && order.position_side ? order.position_side : result.position_side;
      return positionDirectionLabel(effect || "open", side, order ? order.action : "");
    }

    function normalizedOrderRouteAction(action) {
      const value = String(action || "").trim().toLowerCase();
      if (value === "buy" || value === "long") return "long";
      if (value === "sell" || value === "short") return "short";
      return "";
    }

    function orderHistorySignalRouteText(order) {
      const source = normalizedOrderRouteAction(order && order.source_action);
      const executed = normalizedOrderRouteAction(order && order.action);
      if (!source || !executed) return "-";
      return source === executed ? "同步信号" : "反转信号";
    }

    function orderHistoryADXText(order) {
      const raw = order ? order.adx : null;
      if (raw === null || raw === undefined || raw === "") return "-";
      const value = Number(raw);
      return Number.isFinite(value) ? value.toFixed(2) : "-";
    }

    function orderHistoryMarketText(order) {
      const strategy = String(order && order.market_strategy || "").trim().toLowerCase();
      if (strategy === "trend") return "单边市场";
      if (strategy === "scalp") return "震荡市场";
      if (strategy === "transition") return "过渡市场";
      const rawADX = order ? order.adx : null;
      if (rawADX === null || rawADX === undefined || rawADX === "") return "-";
      const adx = Number(rawADX);
      if (!Number.isFinite(adx)) return "-";
      if (adx >= 25) return "单边市场";
      return "震荡市场";
    }

    function isTrueValue(value) {
      if (value === true) return true;
      if (value === 1) return true;
      const text = String(value || "").toLowerCase();
      return text === "true" || text === "1" || text === "yes";
    }

    function pendingOrderIdentity(row) {
      const keys = pendingOrderIdentityKeys(row);
      return keys.length ? keys[0] : "";
    }

    function pendingOrderIdentityKeys(row) {
      if (!row) return [];
      const exchange = normalizeExchange(row._exchange || "okx");
      const apiID = row._api_id || "";
      const instID = String(row.instId || "").toUpperCase();
      const group = pendingOrderGroup(row);
      const ordID = String(row.ordId || "").trim();
      const clOrdID = String(row.clOrdId || "").trim();
      const algoID = String(row.algo_id || "").trim();
      const algoClOrdID = String(row.algo_cl_ord_id || "").trim();
      const keys = [];
      if (!instID) return keys;
      if (group === "algo") {
        if (algoID) keys.push([exchange, apiID, instID, group, "algo:" + algoID].join("|"));
        if (algoClOrdID) keys.push([exchange, apiID, instID, group, "algo-cl:" + algoClOrdID].join("|"));
        return keys;
      }
      if (ordID) keys.push([exchange, apiID, instID, group, "ord:" + ordID].join("|"));
      if (clOrdID) keys.push([exchange, apiID, instID, group, "cl:" + clOrdID].join("|"));
      return keys;
    }

    function markPendingOrderIdentity(row, seen) {
      pendingOrderIdentityKeys(row).forEach((key) => { seen[key] = true; });
    }

    function hasPendingOrderIdentity(row, seen) {
      return pendingOrderIdentityKeys(row).some((key) => !!seen[key]);
    }

    function mergeLocalPendingLimitCloseOrders(rows) {
      const now = Date.now();
      const baseRows = (Array.isArray(rows) ? rows : []).filter((row) => !(row && row._local_pending_limit_close));
      const seen = {};
      baseRows.forEach((row) => markPendingOrderIdentity(row, seen));
      const active = activeExchange();
      const kept = [];
      const merged = baseRows.slice();
      (Array.isArray(state.localPendingLimitCloses) ? state.localPendingLimitCloses : []).forEach((row) => {
        if (!row || Number(row._expires_at || 0) <= now) return;
        if (hasPendingOrderIdentity(row, seen)) return;
        kept.push(row);
        if (normalizeExchange(row._exchange || "okx") !== active) return;
        merged.unshift(row);
        markPendingOrderIdentity(row, seen);
      });
      state.localPendingLimitCloses = kept;
      return merged;
    }

    function positionCloseSideForPendingRow(posSide, pos) {
      const kind = positionSideKind(posSide, pos);
      if (kind === "short") return "buy";
      return "sell";
    }

    function localPendingLimitCloseRow(result, request) {
      const exchange = normalizeExchange(request && request.exchange);
      const precision = symbolPrecision(exchange, result && result.inst_id ? result.inst_id : request.inst_id);
      const now = Date.now();
      const status = result && result.status === "unknown" ? "syncing" : "live";
      return {
        _exchange: exchange,
        _api_id: (result && result.api_id) || (request && request.api_id) || "",
        _expires_at: now + pendingLimitCloseOrderCacheMs,
        _local_pending_limit_close: true,
        order_group: "normal",
        instType: exchange === "binance" ? "USDT-M" : "SWAP",
        instId: (result && result.inst_id) || (request && request.inst_id) || "",
        ordId: result && result.ord_id ? result.ord_id : "",
        clOrdId: result && result.cl_ord_id ? result.cl_ord_id : "",
        tdMode: "",
        side: positionCloseSideForPendingRow(request && request.pos_side, request && request.pos),
        posSide: request && request.pos_side ? request.pos_side : "",
        ordType: "limit",
        px: result && result.px ? result.px : "",
        sz: result && result.sz ? result.sz : "",
        accFillSz: "0",
        avgPx: "",
        state: status,
        reduceOnly: true,
        closePosition: false,
        cTime: String(now),
        uTime: String(now),
        price_precision: precision ? precision.price_precision : null,
        quantity_precision: precision ? precision.quantity_precision : null,
        chaseable: true,
        chasing: false
      };
    }

    function rememberLimitClosePendingOrder(result, request) {
      if (!result || !request || request.mode !== "limit") return;
      const row = localPendingLimitCloseRow(result, request);
      const key = pendingOrderIdentity(row);
      if (!key) return;
      const rows = Array.isArray(state.localPendingLimitCloses) ? state.localPendingLimitCloses : [];
      const nextSeen = {};
      markPendingOrderIdentity(row, nextSeen);
      state.localPendingLimitCloses = rows.filter((item) => !hasPendingOrderIdentity(item, nextSeen));
      state.localPendingLimitCloses.unshift(row);
      if (!state.pendingOrders) {
        state.pendingOrders = {
          ok: true,
          count: 0,
          normal_count: 0,
          algo_count: 0,
          total_count: 0,
          refreshed_at: new Date().toISOString(),
          exchanges: [{
            ok: true,
            exchange: row._exchange,
            api_id: row._api_id,
            count: 0,
            normal_count: 0,
            algo_count: 0,
            total_count: 0
          }],
          orders: [],
          algo_orders: []
        };
      }
      state.pendingOrders.orders = mergeLocalPendingLimitCloseOrders(state.pendingOrders.orders);
      const localCount = state.pendingOrders.orders.filter((item) => item && item._local_pending_limit_close).length;
      const actualCount = state.pendingOrders.orders.filter((item) => !(item && item._local_pending_limit_close)).length;
      state.pendingOrders.local_pending_count = localCount;
      state.pendingOrders.count = actualCount + localCount;
      state.pendingOrders.normal_count = actualCount + localCount;
      state.pendingOrders.total_count = Number(state.pendingOrders.normal_count || 0) + Number(state.pendingOrders.algo_count || 0);
      renderPendingOrders();
    }

    function pendingOrderPositionEffect(row) {
      if (!row) return "open";
      if (isTrueValue(row.reduceOnly) || isTrueValue(row.closePosition) || isTrueValue(row.close_position)) return "close";
      return "open";
    }

    function pendingOrderPositionSide(row) {
      if (!row) return "";
      const effect = pendingOrderPositionEffect(row);
      const fromPosSide = positionSideKind(row.posSide, "");
      if (fromPosSide && fromPosSide !== "net") return fromPosSide;
      return positionSideFromAction(row.side, effect);
    }

    function pendingOrderDirectionText(row) {
      return positionDirectionLabel(pendingOrderPositionEffect(row), pendingOrderPositionSide(row), row ? row.side : "");
    }

    function pendingOrderGroup(row) {
      return String(row && row.order_group ? row.order_group : "").toLowerCase() === "algo" ? "algo" : "normal";
    }

    function algoOrderTypeText(v) {
      const raw = String(v || "").trim();
      const value = raw.toLowerCase();
      if (value === "conditional") return "止盈止损";
      if (value === "trigger") return "触发单";
      if (value === "move_order_stop") return "移动止损";
      if (value === "iceberg") return "冰山委托";
      if (value === "twap") return "TWAP";
      if (value === "oco") return "OCO";
      if (value === "stop_market") return "止损市价";
      if (value === "take_profit_market") return "止盈市价";
      if (value === "stop") return "止损限价";
      if (value === "take_profit") return "止盈限价";
      if (value === "trailing_stop_market") return "移动止损";
      return asText(raw);
    }

    function pendingOrderTypeText(row) {
      if (pendingOrderGroup(row) !== "algo") return orderTypeText(row ? row.ordType : "");
      return "算法 " + algoOrderTypeText(row ? row.ordType : "");
    }

    function formatCallbackRatio(value) {
      if (value === null || value === undefined || String(value).trim() === "") return "-";
      const text = String(value).trim();
      return text.includes("%") ? text : text + "%";
    }

    function pendingOrderPriceText(row) {
      if (pendingOrderGroup(row) !== "algo") return formatPriceAmount(row, row ? row.px : null);
      const parts = [];
      if (row && row.trigger_px) parts.push("触发 " + formatPriceAmount(row, row.trigger_px));
      if (row && row.activation_px) parts.push("激活 " + formatPriceAmount(row, row.activation_px));
      if (row && row.callback_ratio) parts.push("回调 " + formatCallbackRatio(row.callback_ratio));
      if (row && row.px && !row.trigger_px && !row.activation_px) parts.push("委托 " + formatPriceAmount(row, row.px));
      return parts.length ? parts.join(" / ") : "-";
    }

    function pendingOrderChaseUnavailable(row) {
      if (!row) return "";
      if (!isTrueValue(row.chaseable)) {
        if (pendingOrderGroup(row) === "algo") return row.chase_unavailable_reason || row.price_error || "该算法订单不支持追单";
        return row.chase_unavailable_reason || row.price_error || "该挂单不支持追单";
      }
      return row.price_error || "";
    }

    function positionSideKind(posSide, pos) {
      const side = String(posSide || "").toLowerCase();
      if (side === "long") return "long";
      if (side === "short") return "short";
      if (side === "net") {
        const value = Number(pos);
        if (Number.isFinite(value) && value < 0) return "short";
        if (Number.isFinite(value) && value > 0) return "long";
        return "net";
      }
      return "";
    }

    function tradeSideText(side) {
      const value = String(side || "").toLowerCase();
      if (value === "buy") return "买入";
      if (value === "sell") return "卖出";
      return asText(side);
    }

    function pendingOrderStateText(value) {
      const stateText = String(value || "").toLowerCase();
      if (stateText === "syncing" || stateText === "unknown") return "等待同步";
      if (stateText === "new") return "等待成交";
      if (stateText === "live") return "等待成交";
      if (stateText === "partially_filled") return "部分成交";
      return asText(value);
    }

    function pendingOrderRowKey(row) {
      const apiID = row._api_id || "";
      const exchange = row._exchange || "okx";
      const group = pendingOrderGroup(row);
      const orderID = group === "algo"
        ? (row.algo_id || ("algo-cl:" + (row.algo_cl_ord_id || "")))
        : (row.ordId || ("cl:" + (row.clOrdId || "")));
      return [normalizeExchange(exchange), apiID, String(row.instId || "").toUpperCase(), group, orderID].join("|");
    }

    function pendingOrderActionCell(row) {
      if (row && row._local_pending_limit_close) return tableCell('<span class="muted">同步中</span>');
      const exchange = normalizeExchange(row._exchange || "okx");
      const group = pendingOrderGroup(row);
      const key = pendingOrderRowKey(row);
      const busy = !!state.pendingOrderActions[key];
      const chasing = !!row.chasing;
      const unavailableReason = !chasing ? pendingOrderChaseUnavailable(row) : "";
      const unavailable = !!unavailableReason;
      const disabled = busy;
      const label = busy ? "处理中" : (chasing ? "停止追单" : "追单");
      const mode = chasing ? "stop" : "start";
      const chaseButton = '<button class="btn small' + (unavailable ? " is-disabled" : "") + '" type="button" data-pending-chase="' + mode + '"' +
        ' data-exchange="' + exchange + '"' +
        ' data-order-group="' + group + '"' +
        ' data-api-id="' + escapeHTML(row._api_id || "") + '"' +
        ' data-inst-id="' + escapeHTML(asText(row.instId)) + '"' +
        ' data-ord-id="' + escapeHTML(row.ordId || "") + '"' +
        ' data-cl-ord-id="' + escapeHTML(row.clOrdId || "") + '"' +
        ' data-algo-id="' + escapeHTML(row.algo_id || "") + '"' +
        ' data-algo-cl-ord-id="' + escapeHTML(row.algo_cl_ord_id || "") + '"' +
        ' data-price-error="' + escapeHTML(unavailableReason) + '"' +
        (unavailable ? ' aria-disabled="true"' : "") +
        ' title="' + escapeHTML(unavailableReason || label) + '"' +
        (disabled || unavailable ? " disabled" : "") + ">" + label + "</button>";
      const cancelTitle = group === "algo" ? "取消算法订单" : "取消挂单";
      const cancelButton = '<button class="btn small danger" type="button" data-pending-cancel="true"' +
        ' data-exchange="' + exchange + '"' +
        ' data-order-group="' + group + '"' +
        ' data-api-id="' + escapeHTML(row._api_id || "") + '"' +
        ' data-inst-id="' + escapeHTML(asText(row.instId)) + '"' +
        ' data-ord-id="' + escapeHTML(row.ordId || "") + '"' +
        ' data-cl-ord-id="' + escapeHTML(row.clOrdId || "") + '"' +
        ' data-algo-id="' + escapeHTML(row.algo_id || "") + '"' +
        ' data-algo-cl-ord-id="' + escapeHTML(row.algo_cl_ord_id || "") + '"' +
        ' title="' + escapeHTML(cancelTitle) + '"' +
        (disabled ? " disabled" : "") + ">取消</button>";
      return '<td><div class="position-actions">' + chaseButton + cancelButton + "</div></td>";
    }

    function positionPercent(v) {
      if (v === null || v === undefined || v === "") return "-";
      const formatted = formatPct(v);
      return formatted === "-" ? asText(v) : formatted;
    }

    function positionNumber(value) {
      if (value === null || value === undefined) return null;
      const text = String(value).trim();
      if (!text || text === "-") return null;
      const number = Number(text.replace(/,/g, ""));
      return Number.isFinite(number) ? number : null;
    }

    function positionReturnRatio(row) {
      const uplRatio = positionNumber(row ? row.uplRatio : null);
      if (uplRatio !== null) return uplRatio;
      const upl = positionNumber(row ? row.upl : null);
      const margin = positionNumber(row ? row.margin : null);
      if (upl !== null && margin !== null && margin !== 0) return upl / Math.abs(margin);
      return null;
    }

    function positionReturnPercent(row) {
      const ratio = positionReturnRatio(row);
      return ratio === null ? positionPercent(row ? row.uplRatio : null) : formatPct(ratio);
    }

    function positionSum(rows, field) {
      return rows.reduce((sum, row) => {
        const value = Number(row[field]);
        return Number.isFinite(value) ? sum + value : sum;
      }, 0);
    }

    function positionAmount(row) {
      const notionalRaw = row ? row.notionalUsd : null;
      if (notionalRaw !== null && notionalRaw !== undefined) {
        const notionalText = String(notionalRaw).trim();
        if (notionalText && notionalText !== "-") {
          const notional = positionNumber(notionalText);
          if (Number.isFinite(notional) && notional !== 0) return formatFixed2(Math.abs(notional));
        }
      }
      const marginRaw = row ? row.margin : null;
      const leverRaw = row ? row.lever : null;
      if (marginRaw === null || marginRaw === undefined || leverRaw === null || leverRaw === undefined) return "-";
      const marginText = String(marginRaw).trim();
      const leverText = String(leverRaw).trim();
      if (!marginText || marginText === "-" || !leverText || leverText === "-") return "-";
      const margin = positionNumber(marginText);
      const lever = positionNumber(leverText);
      if (!Number.isFinite(margin) || !Number.isFinite(lever)) return "-";
      return formatFixed2(margin * lever);
    }

    function signedToneClass(v) {
      const value = Number(v);
      if (!Number.isFinite(value) || value === 0) return "";
      return value > 0 ? "signed-profit" : "signed-loss";
    }

    function signedCell(v, formatted) {
      const tone = signedToneClass(v);
      return '<td' + (tone ? ' class="' + tone + '"' : "") + ">" + escapeHTML(formatted) + "</td>";
    }

    function positionSideCell(row) {
      const kind = positionSideKind(row ? row.posSide : "", row ? row.pos : "");
      const tone = kind === "long" ? "signed-profit" : (kind === "short" ? "signed-loss" : "");
      const label = positionDirectionLabel("open", kind || positionSideKind(row ? row.posSide : "", row ? row.pos : ""), "");
      return '<td' + (tone ? ' class="' + tone + '"' : "") + ">" + escapeHTML(label) + "</td>";
    }

    function positionCloseRowKey(row) {
      return [normalizeExchange(row._exchange || "okx"), row._api_id || "", String(row.instId || "").toUpperCase(), String(row.posSide || "net").toLowerCase()].join("|");
    }

    function positionActionCell(row) {
      const exchange = normalizeExchange(row._exchange || "okx");
      const key = positionCloseRowKey(row);
      const closing = !!state.positionClosing[key];
      const instID = escapeHTML(asText(row.instId));
      const posSide = escapeHTML(String(row.posSide || ""));
      const pos = escapeHTML(String(row.pos || ""));
      const apiID = escapeHTML(row._api_id || "");
      const baseAttrs = ' data-exchange="' + exchange + '" data-api-id="' + apiID + '" data-inst-id="' + instID + '" data-pos-side="' + posSide + '" data-pos="' + pos + '"';
      const percentButtons = [
        { label: "10%", ratio: "0.1" },
        { label: "25%", ratio: "0.25" },
        { label: "50%", ratio: "0.5" },
        { label: "75%", ratio: "0.75" }
      ].map((item) => '<button class="btn small position-percent-close-btn" type="button" data-position-close="limit" data-position-ratio="' + item.ratio + '"' + baseAttrs + ' title="限价平仓 ' + item.label + '，60秒后市价兜底"' + (closing ? " disabled" : "") + '>' + item.label + '</button>').join("");
      return '<td><div class="position-actions">' +
        percentButtons +
        '<button class="btn small danger" type="button" data-position-close="market"' + baseAttrs + (closing ? " disabled" : "") + '>市价平仓</button>' +
        '<button class="btn small" type="button" data-position-close="limit"' + baseAttrs + (closing ? " disabled" : "") + '>限价平仓</button>' +
        '</div></td>';
    }

    function positionEntryTimeTitle(row) {
      const parts = [];
      if (row && row.entry_time_source) parts.push(entryTimeSourceText(row.entry_time_source));
      if (row && row.entry_time_error) parts.push(row.entry_time_error);
      return parts.join(" / ");
    }

    function positionEntryTimeCell(row) {
      const title = positionEntryTimeTitle(row);
      const attr = title ? ' title="' + escapeHTML(title) + '"' : "";
      const text = row && row.entry_fill_time ? shanghaiTime(row.entry_fill_time) : "-";
      return '<td class="time"' + attr + '>' + escapeHTML(text) + '</td>';
    }

    function positionHoldingTimeCell(row) {
      const title = positionEntryTimeTitle(row);
      const attr = title ? ' title="' + escapeHTML(title) + '"' : "";
      const text = row && row.entry_fill_time ? formatHoldingSeconds(row.holding_seconds) : "-";
      return '<td class="time"' + attr + '>' + escapeHTML(text) + '</td>';
    }

    function renderPositions() {
      renderTableStructure("positions");
      const columnDefs = currentTableColumnDefs("positions");
      const colspan = tableColumnCount("positions");
      const rows = state.positions && Array.isArray(state.positions.positions) ? state.positions.positions : [];
      const totalUpl = positionSum(rows, "upl");
      const positionsReady = state.positions && state.positions.ok;
      $("positions-count").textContent = positionsReady ? asText(state.positions.count || rows.length) : "-";
      $("positions-upl").textContent = positionsReady ? formatNumber(totalUpl) + " USDT" : "-";
      $("positions-upl").className = ["value", positionsReady ? signedToneClass(totalUpl) : ""].filter(Boolean).join(" ");
      $("positions-notional").textContent = positionsReady ? formatUSD(positionSum(rows, "notionalUsd")) : "-";
      $("positions-updated").textContent = state.positions ? combinedStatusText(state.positions) : "-";
      if (!state.positions) {
        $("position-rows").innerHTML = '<tr><td colspan="' + colspan + '" class="muted">' + escapeHTML(state.positionsError || "-") + '</td></tr>';
        return;
      }
      const positionRows = rows.map((row) => {
        return "<tr>" + columnDefs.map((col) => col.cell(row)).join("") + "</tr>";
      }).join("");
      const warningRow = state.positionsError ? '<tr><td colspan="' + colspan + '" class="muted">' + escapeHTML(state.positionsError) + '</td></tr>' : "";
      $("position-rows").innerHTML = positionRows + warningRow || '<tr><td colspan="' + colspan + '" class="muted">暂无当前持仓</td></tr>';
    }

    function renderPendingOrders() {
      renderTableStructure("pending_orders");
      updatePendingOrderGroupButtons();
      const columnDefs = currentTableColumnDefs("pending_orders");
      const colspan = tableColumnCount("pending_orders");
      const rows = pendingOrderDisplayRows();
      if (!state.pendingOrders) {
        $("pending-orders-updated").textContent = state.pendingOrdersError || "-";
        $("pending-order-rows").innerHTML = '<tr><td colspan="' + colspan + '" class="muted">' + escapeHTML(state.pendingOrdersError || "-") + '</td></tr>';
        return;
      }
      const pendingReady = state.pendingOrders && state.pendingOrders.ok;
      $("pending-orders-updated").textContent = pendingReady ? pendingOrdersSummaryText(state.pendingOrders) : state.pendingOrdersError || "-";
      const orderRows = rows.map((row) => {
        return "<tr>" + columnDefs.map((col) => col.cell(row)).join("") + "</tr>";
      }).join("");
      const warningRow = state.pendingOrdersError ? '<tr><td colspan="' + colspan + '" class="muted">' + escapeHTML(state.pendingOrdersError) + '</td></tr>' : "";
      const emptyText = state.pendingOrderGroup === "algo" ? "暂无当前算法订单" : "暂无当前挂单";
      $("pending-order-rows").innerHTML = orderRows + warningRow || '<tr><td colspan="' + colspan + '" class="muted">' + emptyText + '</td></tr>';
    }

    function renderAnalysis() {
      renderGlobalExchangeSwitch();
      if (!state.analysis) {
        $("analysis-updated").textContent = state.analysisError || "-";
        renderAnalysisExchangeBalances();
        renderAnalysisExchangeStats("okx", state.analysisError || "-");
        renderAnalysisExchangeStats("binance", state.analysisError || "-");
        renderAnalysisTradeHistory(state.analysisError || "-");
        return;
      }
      renderAnalysisExchangeBalances();
      const exchange = activeExchange();
      const apiID = exchange === "binance" ? state.analysis.binance_api_id : state.analysis.api_id;
      const staleLabel = state.analysis.cache && state.analysis.cache.stale ? " / 缓存数据（上游暂不可用）" : "";
      $("analysis-updated").textContent = shanghaiTime(state.analysis.refreshed_at) + " / API " + exchangeLabel(exchange) + " " + asText(apiID) + staleLabel;
      renderAnalysisExchangeStats("okx", "");
      renderAnalysisExchangeStats("binance", "");
      renderAnalysisTradeHistory("");
    }

    function renderAnalysisExchangeStats(exchange, errorText) {
      const normalized = normalizeExchange(exchange);
      const prefix = normalized === "binance" ? "analysis-binance" : "analysis-okx";
      if (errorText) {
        $(prefix + "-net-pnl").textContent = "-";
        $(prefix + "-win-rate").textContent = "-";
        $(prefix + "-profit-factor").textContent = "-";
        $(prefix + "-payoff-ratio").textContent = "-";
        $(prefix + "-trades").textContent = "-";
        $(prefix + "-turnover").textContent = "-";
        $(prefix + "-fees").textContent = "-";
        $(prefix + "-position-count").textContent = "-";
        $(prefix + "-position-upl").textContent = "-";
        $(prefix + "-position-upl").className = "value";
        return;
      }
      const summary = analysisExchangeSummary(normalized);
      const positionSummary = analysisPositionSummary(normalized);
      $(prefix + "-net-pnl").textContent = analysisAmountText(summary.net_pnl);
      $(prefix + "-win-rate").textContent = formatPct(summary.win_rate);
      $(prefix + "-profit-factor").textContent = formatFactor(summary);
      $(prefix + "-payoff-ratio").textContent = formatNumber(summary.payoff_ratio);
      $(prefix + "-trades").textContent = asText(summary.trade_count || 0);
      $(prefix + "-turnover").textContent = analysisAmountText(summary.turnover);
      $(prefix + "-fees").textContent = analysisAmountText(summary.fees);
      $(prefix + "-position-count").textContent = positionSummary.ready ? asText(positionSummary.count) : "-";
      $(prefix + "-position-upl").textContent = positionSummary.ready ? analysisAmountText(positionSummary.upl) : "-";
      $(prefix + "-position-upl").className = ["value", positionSummary.ready ? signedToneClass(positionSummary.upl) : ""].filter(Boolean).join(" ");
    }

    function analysisExchangeSummary(exchange) {
      const summaries = state.analysis && Array.isArray(state.analysis.exchange_summaries) ? state.analysis.exchange_summaries : [];
      const found = summaries.find((row) => normalizeExchange(row.exchange) === normalizeExchange(exchange));
      return found || { exchange: normalizeExchange(exchange), trade_count: 0, wins: 0, losses: 0, net_pnl: 0, fees: 0, turnover: 0, win_rate: 0, profit_factor: 0, payoff_ratio: 0 };
    }

    function analysisPositionRows(exchange) {
      const rows = state.analysisPositions && Array.isArray(state.analysisPositions.positions) ? state.analysisPositions.positions : [];
      return rows.filter((row) => normalizeExchange(row._exchange || "okx") === normalizeExchange(exchange));
    }

    function analysisPositionSummary(exchange) {
      const rows = analysisPositionRows(exchange);
      return {
        ready: !!state.analysisPositions,
        count: rows.length,
        upl: positionSum(rows, "upl")
      };
    }

    function analysisTradesForActiveExchange() {
      const exchange = activeExchange();
      const rows = state.analysis && Array.isArray(state.analysis.position_trades) ? state.analysis.position_trades : [];
      return rows.filter((row) => normalizeExchange(row.exchange) === exchange);
    }

    function analysisTradeColumnIDs() {
      return analysisTradeColumnDefs.map((col) => col.id);
    }

    function normalizeAnalysisTradeColumnOrder(order) {
      const valid = new Set(analysisTradeColumnIDs());
      const next = [];
      (Array.isArray(order) ? order : []).forEach((id) => {
        id = String(id || "").trim();
        if (!id || id === "order_id" || !valid.has(id) || next.includes(id)) return;
        next.push(id);
      });
      analysisTradeColumnIDs().forEach((id) => {
        if (!next.includes(id)) next.push(id);
      });
      return next;
    }

    function currentAnalysisTradeColumnOrder() {
      let raw = null;
      let parsed = null;
      try {
        raw = window.localStorage.getItem(analysisTradeColumnStorageKey);
        parsed = raw ? JSON.parse(raw) : null;
      } catch (_) {
        parsed = null;
      }
      const normalized = normalizeAnalysisTradeColumnOrder(parsed);
      try {
        if (JSON.stringify(parsed) !== JSON.stringify(normalized)) {
          window.localStorage.setItem(analysisTradeColumnStorageKey, JSON.stringify(normalized));
        }
      } catch (_) {}
      return normalized;
    }

    function setAnalysisTradeColumnOrder(order) {
      const normalized = normalizeAnalysisTradeColumnOrder(order);
      try {
        window.localStorage.setItem(analysisTradeColumnStorageKey, JSON.stringify(normalized));
      } catch (_) {}
      return normalized;
    }

    function currentAnalysisTradeColumns() {
      const byID = {};
      analysisTradeColumnDefs.forEach((col) => { byID[col.id] = col; });
      return currentAnalysisTradeColumnOrder().map((id) => byID[id]).filter(Boolean);
    }

    function analysisTradeColumnCount() {
      return currentAnalysisTradeColumnOrder().length;
    }

    function analysisAmountText(value) {
      if (value === null || value === undefined || String(value).trim() === "") return "-";
      const formatted = formatNumber(value);
      if (formatted === "-") return asText(value);
      return formatted;
    }

    function tradeFeeText(row) {
      const fee = analysisAmountText(row && row.fee);
      const ccy = row && row.fee_ccy ? String(row.fee_ccy).trim() : "";
      if (fee === "-") return "-";
      return ccy && ccy.toUpperCase() !== "USDT" ? fee + " " + ccy : fee;
    }

    function analysisInstIDText(instID) {
      const original = String(instID || "").trim();
      if (!original) return "-";
      let value = original.toUpperCase();
      const colon = value.lastIndexOf(":");
      if (colon >= 0) value = value.slice(colon + 1);
      value = value.replace(/\.P$/, "").replace(/\.PERP$/, "").replace(/PERP$/, "");
      if (value.endsWith("-USDT-SWAP") || value.endsWith("-USDC-SWAP")) {
        return value.split("-")[0] || original;
      }
      if (value.endsWith("USDT") && value.length > 4) return value.slice(0, -4);
      if (value.endsWith("USDC") && value.length > 4) return value.slice(0, -4);
      return original;
    }

    function analysisPositionSideText(side) {
      return positionDirectionText(side);
    }

    function analysisPositionSideToneClass(side) {
      const value = String(side || "").toLowerCase();
      if (value === "long") return "signed-profit";
      if (value === "short") return "signed-loss";
      return "";
    }

    function analysisPnLRatioValue(row) {
      const pnl = Number(row && row.net_pnl);
      const margin = Number(row && row.margin);
      if (!Number.isFinite(pnl) || !Number.isFinite(margin) || margin === 0) return null;
      return pnl / margin;
    }

    function analysisPnLRatioText(row) {
      const ratio = analysisPnLRatioValue(row);
      return ratio === null ? "-" : formatPct(ratio);
    }

    function renderAnalysisTradeHead() {
      const head = $("analysis-trade-head");
      const cols = $("analysis-trade-cols");
      const columns = currentAnalysisTradeColumns();
      if (cols) {
        cols.innerHTML = columns.map((col) => '<col class="' + escapeHTML(col.colClass || "") + '">').join("");
      }
      if (!head) return;
      head.innerHTML = "<tr>" + columns.map((col) =>
        '<th draggable="true" data-analysis-trade-column="' + escapeHTML(col.id) + '" title="拖动调整栏目顺序">' + escapeHTML(col.title) + "</th>"
      ).join("") + "</tr>";
    }

    function analysisTradeCellHTML(col, row) {
      const classes = [];
      if (col.tdClass) classes.push(col.tdClass);
      if (col.signedField) {
        const tone = signedToneClass(row && row[col.signedField]);
        if (tone) classes.push(tone);
      }
      if (col.toneClass) {
        const tone = col.toneClass(row || {});
        if (tone) classes.push(tone);
      }
      const classAttr = classes.length ? ' class="' + classes.map(escapeHTML).join(" ") + '"' : "";
      const content = col.renderHTML ? col.renderHTML(row || {}) : escapeHTML(col.render(row || {}));
      return "<td" + classAttr + ">" + content + "</td>";
    }

    function analysisCooldownKey(symbol) {
      return ignoredCoinpairKey(symbol);
    }

    function activeCoinpairBlockForSymbol(symbol) {
      const candidate = ignoredCoinpairKey(symbol);
      if (!candidate) return null;
      return activeCoinpairBlocks().find((block) => {
        const filter = ignoredCoinpairKey(block && block.keyword);
        return !!filter && candidate.includes(filter);
      }) || null;
    }

    function analysisCooldownCellHTML(row) {
      const symbol = String(row && row.inst_id || "").trim();
      const key = analysisCooldownKey(symbol);
      const block = activeCoinpairBlockForSymbol(symbol);
      const submitting = !!(key && state.coinpairCooldownSubmitting[key]);
      const disabled = block || submitting || !key;
      const label = submitting ? "提交中" : (block ? "冷静中" : "冷静期");
      const title = block
        ? ("冷静期至 " + shanghaiTime(block.expires_at))
        : (submitting ? "正在加入冷静期" : "将该币对加入 24 小时冷静期");
      return '<div class="analysis-realized-pnl-cell"><span>' + escapeHTML(analysisAmountText(row && row.realized_pnl)) + '</span>' +
        '<button class="btn small analysis-cooldown-btn" type="button" data-analysis-cooldown="true"' +
        ' data-symbol="' + escapeHTML(symbol) + '" data-trigger-price="' + escapeHTML(row && row.exit_px || "") + '"' +
        ' data-exchange="' + escapeHTML(row && row.exchange || activeExchange()) + '" data-api-id="' + escapeHTML(row && row.api_id || "") + '"' +
        ' title="' + escapeHTML(title) + '"' + (disabled ? " disabled" : "") + '>' + escapeHTML(label) + '</button></div>';
    }

    function syncAnalysisCooldownButtons() {
      document.querySelectorAll("button[data-analysis-cooldown]").forEach((button) => {
        const symbol = button.dataset.symbol || "";
        const key = analysisCooldownKey(symbol);
        const block = activeCoinpairBlockForSymbol(symbol);
        const submitting = !!(key && state.coinpairCooldownSubmitting[key]);
        button.disabled = !!block || submitting || !key;
        button.textContent = submitting ? "提交中" : (block ? "冷静中" : "冷静期");
        button.title = block
          ? ("冷静期至 " + shanghaiTime(block.expires_at))
          : (submitting ? "正在加入冷静期" : "将该币对加入 24 小时冷静期");
      });
    }

    async function addAnalysisCoinpairCooldown(button) {
      const symbol = String(button && button.dataset.symbol || "").trim();
      const key = analysisCooldownKey(symbol);
      if (!symbol || !key || state.coinpairCooldownSubmitting[key]) return;
      const existing = activeCoinpairBlockForSymbol(symbol);
      if (existing) {
        syncAnalysisCooldownButtons();
        return;
      }
      const estimatedExpiry = new Date(Date.now() + 24 * 60 * 60 * 1000).toISOString();
      const confirmed = window.confirm(
        "确认将 " + symbol + " 加入冷静期至 " + shanghaiTime(estimatedExpiry) + "？\n\n24 小时内将忽略该币对的新开仓、手动重试和自动补回，且不能提前解除；平仓不受影响。"
      );
      if (!confirmed) return;
      state.coinpairCooldownSubmitting[key] = true;
      syncAnalysisCooldownButtons();
      try {
        const result = await api("/tvbot/coinpair-blocks", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            symbol,
            trigger_price: button.dataset.triggerPrice || "",
            exchange: normalizeExchange(button.dataset.exchange || activeExchange()),
            api_id: button.dataset.apiId || ""
          })
        });
        await loadCoinpairBlocks();
        const block = result && result.block ? result.block : null;
        const expiryText = block ? ("，至 " + shanghaiTime(block.expires_at)) : "";
        toast(result && result.status === "active" ? (symbol + " 已在冷静期" + expiryText) : (symbol + " 已加入 24 小时冷静期" + expiryText));
      } finally {
        delete state.coinpairCooldownSubmitting[key];
        syncAnalysisCooldownButtons();
      }
    }

    function renderAnalysisTradeHistory(errorText) {
      const exchange = activeExchange();
      const label = exchangeLabel(exchange);
      const title = $("analysis-trade-history-title");
      const status = $("analysis-trade-history-status");
      const rowsEl = $("analysis-trade-rows");
      const pageInfo = $("analysis-trade-page-info");
      const prev = $("analysis-trade-prev");
      const next = $("analysis-trade-next");
      if (title) title.textContent = label + " 下单盈亏分析";
      renderAnalysisTradeHead();
      if (!rowsEl) return;
      if (errorText) {
        rowsEl.innerHTML = '<tr><td colspan="' + analysisTradeColumnCount() + '" class="muted">' + escapeHTML(errorText) + '</td></tr>';
        if (status) status.textContent = errorText;
        if (pageInfo) pageInfo.textContent = "-";
        if (prev) prev.disabled = true;
        if (next) next.disabled = true;
        return;
      }
      const trades = analysisTradesForActiveExchange();
      const totalPages = Math.max(1, Math.ceil(trades.length / analysisTradePageSize));
      state.analysisTradePage = Math.min(Math.max(1, Number(state.analysisTradePage || 1)), totalPages);
      const start = (state.analysisTradePage - 1) * analysisTradePageSize;
      const pageRows = trades.slice(start, start + analysisTradePageSize);
      const columns = currentAnalysisTradeColumns();
      rowsEl.innerHTML = pageRows.map((row) => {
        return "<tr>" + columns.map((col) => analysisTradeCellHTML(col, row)).join("") + "</tr>";
      }).join("") || '<tr><td colspan="' + analysisTradeColumnCount() + '" class="muted">暂无 ' + escapeHTML(label) + ' 下单盈亏记录</td></tr>';
      if (status) status.textContent = trades.length ? ("共 " + trades.length + " 条") : "-";
      if (pageInfo) pageInfo.textContent = trades.length ? ("第 " + state.analysisTradePage + " / " + totalPages + " 页") : "-";
      if (prev) prev.disabled = state.analysisTradePage <= 1;
      if (next) next.disabled = state.analysisTradePage >= totalPages;
    }

    function changeAnalysisTradePage(delta) {
      const trades = analysisTradesForActiveExchange();
      const totalPages = Math.max(1, Math.ceil(trades.length / analysisTradePageSize));
      state.analysisTradePage = Math.min(Math.max(1, Number(state.analysisTradePage || 1) + delta), totalPages);
      renderAnalysisTradeHistory("");
    }

    function handleAnalysisTradeColumnDragStart(event) {
      const th = event.target.closest("th[data-analysis-trade-column]");
      if (!th) return;
      tableColumnDrag = { tableID: "analysis_trades", columnID: th.dataset.analysisTradeColumn };
      th.classList.add("is-dragging");
      if (event.dataTransfer) {
        event.dataTransfer.effectAllowed = "move";
        event.dataTransfer.setData("text/plain", tableColumnDrag.columnID);
      }
    }

    function handleAnalysisTradeColumnDragOver(event) {
      const th = event.target.closest("th[data-analysis-trade-column]");
      if (!th || !tableColumnDrag || tableColumnDrag.tableID !== "analysis_trades") return;
      event.preventDefault();
      th.classList.add("is-drop-target");
      if (event.dataTransfer) event.dataTransfer.dropEffect = "move";
    }

    function handleAnalysisTradeColumnDragLeave(event) {
      const th = event.target.closest("th[data-analysis-trade-column]");
      if (th) th.classList.remove("is-drop-target");
    }

    function handleAnalysisTradeColumnDragEnd() {
      tableColumnDrag = null;
      clearTableColumnDropTargets();
    }

    function handleAnalysisTradeColumnDrop(event) {
      const th = event.target.closest("th[data-analysis-trade-column]");
      if (!th || !tableColumnDrag || tableColumnDrag.tableID !== "analysis_trades") return;
      event.preventDefault();
      const previousOrder = currentAnalysisTradeColumnOrder();
      const fromIndex = previousOrder.indexOf(tableColumnDrag.columnID);
      const toIndex = previousOrder.indexOf(th.dataset.analysisTradeColumn);
      tableColumnDrag = null;
      clearTableColumnDropTargets();
      if (fromIndex < 0 || toIndex < 0 || fromIndex === toIndex) return;
      const nextOrder = previousOrder.slice();
      const moved = nextOrder.splice(fromIndex, 1)[0];
      nextOrder.splice(toIndex, 0, moved);
      setAnalysisTradeColumnOrder(nextOrder);
      renderAnalysisTradeHistory("");
      toast("栏目顺序已保存");
    }

    function initAnalysisTradeColumnDrag() {
      const head = $("analysis-trade-head");
      if (!head) return;
      head.addEventListener("dragstart", handleAnalysisTradeColumnDragStart);
      head.addEventListener("dragover", handleAnalysisTradeColumnDragOver);
      head.addEventListener("dragleave", handleAnalysisTradeColumnDragLeave);
      head.addEventListener("dragend", handleAnalysisTradeColumnDragEnd);
      head.addEventListener("drop", handleAnalysisTradeColumnDrop);
    }

    function renderAnalysisExchangeBalances() {
      const okxItem = balanceOverviewExchange("okx");
      const okxBalance = okxItem && okxItem.balance ? okxItem.balance : (state.analysis ? state.analysis.balance : null);
      renderAnalysisOKXBalance(okxItem, okxBalance);
      renderAnalysisBinanceBalance(balanceOverviewExchange("binance"));
    }

    function renderAnalysisOKXBalance(item, balance) {
      const usdt = usdtBalanceDetail(balance);
      renderAnalysisBalanceWindow("analysis-okx", item);
      $("analysis-usdt-eq").textContent = usdt ? analysisAmountText(usdt.eq) : "-";
      $("analysis-balance-updated").textContent = balance && balance.updated_at ? shanghaiTime(balance.updated_at) : "-";
      $("analysis-okx-balance-status").textContent = exchangeBalanceStatusText(item, balance, "OKX");
      $("analysis-okx-usdt-title").textContent = "USDT 权益图 " + balanceWindowLabel(state.balanceWindowMinutes);
      const overviewPoints = item ? (item.balance_points || []) : [];
      const fallbackPoints = state.analysis ? (state.analysis.balance_points || []) : [];
      const points = usdtBalancePoints(overviewPoints.length ? overviewPoints : fallbackPoints, balance);
      drawUSDTChart(points, "usdt-chart", "暂无 OKX USDT 权益数据", "#1f6feb");
    }

    function renderAnalysisBinanceBalance(item) {
      const balance = item && item.balance ? item.balance : null;
      const usdt = usdtBalanceDetail(balance);
      renderAnalysisBalanceWindow("analysis-binance", item);
      $("analysis-binance-usdt-eq").textContent = usdt ? analysisAmountText(usdt.eq) : "-";
      $("analysis-binance-balance-updated").textContent = balance && balance.updated_at ? shanghaiTime(balance.updated_at) : (item && item.refreshed_at ? shanghaiTime(item.refreshed_at) : "-");
      $("analysis-binance-balance-status").textContent = exchangeBalanceStatusText(item, balance, "Binance");
      $("analysis-binance-usdt-title").textContent = "USDT 权益图 " + balanceWindowLabel(state.balanceWindowMinutes);
      const points = usdtBalancePoints(item ? (item.balance_points || []) : [], balance);
      drawUSDTChart(points, "analysis-binance-usdt-chart", item && item.configured ? "暂无 Binance USDT 权益数据" : "Binance 未配置", "#138a55");
    }

    function renderAnalysisBalanceWindow(prefix, item) {
      const windowStats = item && item.window ? item.window : null;
      const startEl = $(prefix + "-window-start");
      const changeEl = $(prefix + "-window-change");
      const drawdownEl = $(prefix + "-max-drawdown");
      if (!windowStats) {
        if (startEl) startEl.textContent = "-";
        if (changeEl) {
          changeEl.textContent = "-";
          changeEl.className = "value";
        }
        if (drawdownEl) {
          drawdownEl.textContent = "-";
          drawdownEl.className = "value";
        }
        return;
      }
      if (startEl) startEl.textContent = analysisAmountText(windowStats.start_value);
      if (changeEl) {
        changeEl.textContent = analysisAmountText(windowStats.change) + " / " + formatPct(windowStats.change_pct);
        changeEl.className = ["value", signedToneClass(windowStats.change)].filter(Boolean).join(" ");
      }
      if (drawdownEl) {
        const dd = Number(windowStats.max_drawdown);
        drawdownEl.textContent = analysisAmountText(dd) + " / " + formatPct(windowStats.max_drawdown_pct);
        drawdownEl.className = ["value", Number.isFinite(dd) && dd > 0 ? "signed-loss" : ""].filter(Boolean).join(" ");
      }
    }

    function exchangeBalanceStatusText(item, balance, label) {
      if (!item) {
        if (state.balanceOverviewError) return state.balanceOverviewError;
        return balance ? "已更新" : "-";
      }
      if (!item.configured) return label + " 未配置";
      if (item.status === "ok") return "已更新";
      if (item.status === "stale") return "缓存数据（上游暂不可用）";
      if (balance && usdtBalanceDetail(balance)) return "缓存数据（上游暂不可用）";
      return item.error || item.status || "读取失败";
    }

    function drawUSDTChart(points, svgID, emptyText, strokeColor) {
      const svg = $(svgID || "usdt-chart");
      if (!svg) return;
      const lineColor = strokeColor || "#1f6feb";
      const rect = svg.getBoundingClientRect();
      const parentWidth = svg.parentElement ? svg.parentElement.clientWidth : 0;
      const isMini = svg.classList.contains("mini-usdt-chart");
      const isAnalysisChart = !!(svg.closest && svg.closest("#analysis"));
      const miniMinHeight = isAnalysisChart ? 360 : 240;
      const miniFallbackHeight = isAnalysisChart ? 360 : 250;
      const width = Math.max(isMini ? 520 : 900, Math.floor(rect.width || parentWidth || (window.innerWidth - 72) || 900));
      const height = Math.max(isMini ? miniMinHeight : 320, Math.floor(rect.height || svg.clientHeight || (isMini ? miniFallbackHeight : 420)));
      const pad = { left: 64, right: 24, top: 18, bottom: 58 };
      const plotWidth = width - pad.left - pad.right;
      const plotHeight = height - pad.top - pad.bottom;
      const plotBottom = height - pad.bottom;
      svg.setAttribute("viewBox", "0 0 " + width + " " + height);
      svg.innerHTML = "";
      if (!points.length) {
        svg.innerHTML = '<text x="' + (width / 2) + '" y="' + (height / 2) + '" text-anchor="middle" fill="#647089">' + escapeHTML(emptyText || "暂无 USDT 权益数据") + '</text>';
        return;
      }
      const chartPoints = points.map((point, index) => {
        return { point: point, index: index, value: Number(point.value), date: chartPointDate(point) };
      }).filter((point) => Number.isFinite(point.value));
      if (!chartPoints.length) {
        svg.innerHTML = '<text x="' + (width / 2) + '" y="' + (height / 2) + '" text-anchor="middle" fill="#647089">' + escapeHTML(emptyText || "暂无 USDT 权益数据") + '</text>';
        return;
      }
      const values = chartPoints.map((point) => point.value);
      const min = Math.min.apply(null, values);
      const max = Math.max.apply(null, values);
      const span = max - min || 0.0001;
      const timed = chartPoints.filter((point) => point.date);
      const timeValues = timed.map((point) => point.date.getTime());
      const minTime = timeValues.length ? Math.min.apply(null, timeValues) : 0;
      const maxTime = timeValues.length ? Math.max.apply(null, timeValues) : 0;
      const timeSpan = maxTime - minTime;
      const x = (point) => {
        if (timeSpan > 0 && point.date) return pad.left + (point.date.getTime() - minTime) * plotWidth / timeSpan;
        if (chartPoints.length === 1) return pad.left + plotWidth / 2;
        return pad.left + point.index * plotWidth / Math.max(1, points.length - 1);
      };
      const y = (v) => pad.top + (max - v) * plotHeight / span;
      const path = chartPoints.map((point, i) => (i === 0 ? "M" : "L") + x(point).toFixed(2) + " " + y(point.value).toFixed(2)).join(" ");
      let grid = "";
      const yTickCount = 5;
      for (let i = 0; i < yTickCount; i++) {
        const ratio = yTickCount === 1 ? 0 : i / (yTickCount - 1);
        const lineY = pad.top + ratio * plotHeight;
        const labelValue = max - ratio * span;
        grid += '<line class="chart-grid" x1="' + pad.left + '" y1="' + lineY.toFixed(2) + '" x2="' + (width - pad.right) + '" y2="' + lineY.toFixed(2) + '"/>';
        grid += '<text class="chart-label" x="8" y="' + (lineY + 4).toFixed(2) + '">' + chartAxisValue(labelValue) + '</text>';
      }
      const xTicks = chartTickIndexes(chartPoints.length, 6);
      xTicks.forEach((tickIndex) => {
        const point = chartPoints[tickIndex];
        const lineX = x(point);
        const anchor = tickIndex === 0 ? "start" : (tickIndex === chartPoints.length - 1 ? "end" : "middle");
        grid += '<line class="chart-grid" x1="' + lineX.toFixed(2) + '" y1="' + pad.top + '" x2="' + lineX.toFixed(2) + '" y2="' + plotBottom + '"/>';
        grid += '<text class="chart-label" x="' + lineX.toFixed(2) + '" y="' + (height - 22) + '" text-anchor="' + anchor + '">' + escapeHTML(chartTimeLabel(point.date)) + '</text>';
      });
      const last = chartPoints[chartPoints.length - 1];
      svg.innerHTML =
        grid +
        '<line class="chart-axis" x1="' + pad.left + '" y1="' + pad.top + '" x2="' + pad.left + '" y2="' + plotBottom + '"/>' +
        '<line class="chart-axis" x1="' + pad.left + '" y1="' + plotBottom + '" x2="' + (width - pad.right) + '" y2="' + plotBottom + '"/>' +
        '<path d="' + path + '" fill="none" stroke="' + lineColor + '" stroke-width="2.4"/>' +
        '<circle cx="' + x(last).toFixed(2) + '" cy="' + y(last.value).toFixed(2) + '" r="4" fill="' + lineColor + '"/>';
    }

    function currentOrderSearchInput() {
      return $("order-search") ? $("order-search").value.trim() : "";
    }

    function currentOrderStatusFilter() {
      return $("order-status-filter") ? String($("order-status-filter").value || "").trim().toLowerCase() : "";
    }

    async function applyOrderStatusFilter() {
      state.ordersStatus = currentOrderStatusFilter();
      await loadOrders(true);
    }

    function syncOrderSearchControls() {
      const clearButton = $("clear-order-search");
      if (!clearButton) return;
      clearButton.disabled = !currentOrderSearchInput() && !state.ordersSearch;
    }

    async function applyOrderSearch() {
      state.ordersSearch = currentOrderSearchInput();
      syncOrderSearchControls();
      await loadOrders(true);
    }

    async function clearOrderSearch() {
      state.ordersSearch = "";
      const input = $("order-search");
      if (input) input.value = "";
      syncOrderSearchControls();
      await loadOrders(true);
    }

    function currentIgnoredCoinpairInput() {
      return $("ignored-coinpair") ? $("ignored-coinpair").value.trim() : "";
    }

    function configuredIgnoredCoinpairs() {
      const trading = state.config && state.config.trading ? state.config.trading : {};
      if (Array.isArray(trading.ignored_coinpairs)) {
        return trading.ignored_coinpairs.map((item) => String(item || "").trim()).filter(Boolean);
      }
      const legacy = String(trading.ignored_coinpair || "").trim();
      return legacy ? [legacy] : [];
    }

    function ignoredCoinpairKey(value) {
      return String(value || "").trim().toUpperCase().replace(/[^A-Z0-9]/g, "");
    }

    function lossCooldownConfig() {
      const trading = state.config && state.config.trading ? state.config.trading : {};
      const cooldown = trading.loss_cooldown || {};
      const hours = Number(cooldown.hours);
      return {
        enabled: cooldown.enabled !== false,
        hours: Number.isInteger(hours) && hours >= 1 && hours <= 8760 ? hours : 24
      };
    }

    function syncLossCooldownControls() {
      const enabled = $("loss-cooldown-enabled");
      const hours = $("loss-cooldown-hours");
      const status = $("loss-cooldown-status");
      if (!enabled || !hours || !status) return;
      hours.disabled = !enabled.checked;
      status.textContent = enabled.checked
        ? ("已开启：确认亏损平仓后阻止同币对新开仓 " + hours.value + " 小时")
        : "已关闭：不会创建或执行自动亏损冷静期；手动过滤和手动冷静期不受影响。";
    }

    function renderLossCooldownSettings() {
      const enabled = $("loss-cooldown-enabled");
      const hours = $("loss-cooldown-hours");
      if (!enabled || !hours) return;
      const cooldown = lossCooldownConfig();
      enabled.checked = cooldown.enabled;
      hours.value = cooldown.hours;
      syncLossCooldownControls();
    }

    async function saveLossCooldownSettings() {
      const enabled = $("loss-cooldown-enabled");
      const hours = $("loss-cooldown-hours");
      if (!enabled || !hours) return;
      const value = Number(hours.value);
      if (!Number.isInteger(value) || value < 1 || value > 8760) {
        throw new Error("冷静时长必须是 1 到 8760 的整数小时");
      }
      state.config = await api("/tvbot/config", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ trading: { loss_cooldown: { enabled: enabled.checked, hours: value } } })
      });
      renderConfig();
      renderDashboard();
      await loadCoinpairBlocks();
      toast(enabled.checked ? ("亏损冷静期已保存：" + value + " 小时") : "亏损冷静期已关闭，自动冷静期已解除");
    }

    function syncIgnoredCoinpairControls() {
      const addButton = $("add-ignored-coinpair");
      if (!addButton) return;
      addButton.disabled = !ignoredCoinpairKey(currentIgnoredCoinpairInput());
    }

    function renderIgnoredCoinpairFilter() {
      const status = $("ignored-coinpair-status");
      const list = $("ignored-coinpair-list");
      if (!status || !list) return;
      const keywords = configuredIgnoredCoinpairs();
      const cooldowns = activeCoinpairBlocks();
      status.textContent = (keywords.length || cooldowns.length)
        ? ("当前生效：" + keywords.length + " 个手动过滤，" + cooldowns.length + " 个动态冷静期")
        : "当前未启用";
      list.innerHTML = keywords.map((keyword, index) => {
        const removeLabel = "删除过滤 " + keyword + "，恢复后续下单";
        return '<div class="coinpair-filter-item"><span>' + escapeHTML(keyword) + '</span>' +
          '<button class="coinpair-filter-remove" type="button" data-ignored-coinpair-index="' + index + '" title="' + escapeHTML(removeLabel) + '" aria-label="' + escapeHTML(removeLabel) + '">×</button></div>';
      }).join("") || '<span class="muted coinpair-filter-empty">暂无已添加的币对过滤</span>';
      renderLossCooldownSettings();
      renderCoinpairCooldownList();
      syncIgnoredCoinpairControls();
    }

    function activeCoinpairBlocks() {
      const now = Date.now();
      return (Array.isArray(state.coinpairBlocks) ? state.coinpairBlocks : []).filter((block) => {
        const expiresAt = Date.parse(block && block.expires_at ? block.expires_at : "");
        return Number.isFinite(expiresAt) && expiresAt > now;
      });
    }

    function coinpairCooldownRemaining(expiresAt) {
      const remaining = Math.max(0, Math.ceil((Date.parse(expiresAt || "") - Date.now()) / 1000));
      const hours = Math.floor(remaining / 3600);
      const minutes = Math.floor((remaining % 3600) / 60);
      const seconds = remaining % 60;
      return String(hours).padStart(2, "0") + ":" + String(minutes).padStart(2, "0") + ":" + String(seconds).padStart(2, "0");
    }

    function coinpairCooldownSource(source) {
      if (source === "stop_loss_webhook") return "止损信号";
      if (source === "take_profit_webhook") return "历史止盈冷静期";
      if (source === "position_monitor") return "持仓监控止损";
      if (source === "exchange_fill") return "亏损平仓";
      if (source === "analysis_manual") return "手动冷静期";
      return source || "止损";
    }

    function coinpairCooldownPriceLabel(source) {
      if (source === "analysis_manual") return "平仓价";
      if (source === "exchange_fill") return "亏损平仓价";
      return "止损触发价";
    }

    function renderCoinpairCooldownList() {
      const list = $("coinpair-cooldown-list");
      if (!list) return;
      const blocks = activeCoinpairBlocks();
      list.innerHTML = blocks.map((block) => {
        const keyword = String(block.keyword || "-");
        const price = String(block.trigger_price || "").trim();
        const expiresAt = String(block.expires_at || "");
        const source = coinpairCooldownSource(block.source);
        const sourceExchange = block.exchange ? (" · " + exchangeLabel(block.exchange)) : "";
        const priceLabel = coinpairCooldownPriceLabel(block.source);
        const removing = !!state.coinpairCooldownRemoving[ignoredCoinpairKey(keyword)];
        const removeLabel = "提前解除 " + keyword + " 的冷静期";
        return '<div class="coinpair-filter-item coinpair-cooldown-item" title="冷静期内禁止新开仓">' +
          '<button class="coinpair-filter-remove" type="button" data-cooldown-keyword="' + escapeHTML(keyword) + '" title="' + escapeHTML(removeLabel) + '" aria-label="' + escapeHTML(removeLabel) + '"' + (removing ? " disabled" : "") + '>×</button>' +
          '<div class="coinpair-cooldown-title"><span aria-hidden="true">&#128274;</span><strong>' + escapeHTML(keyword) + '</strong></div>' +
          '<span class="coinpair-cooldown-meta">' + escapeHTML(priceLabel) + '：' + escapeHTML(price || "价格待确认") + '</span>' +
          '<span class="coinpair-cooldown-meta">来源：' + escapeHTML(source + sourceExchange) + '</span>' +
          '<span class="coinpair-cooldown-meta">剩余：' + escapeHTML(coinpairCooldownRemaining(expiresAt)) + '</span>' +
          '<span class="coinpair-cooldown-meta">到期：' + escapeHTML(shanghaiTime(expiresAt)) + '</span>' +
          '</div>';
      }).join("") || '<span class="muted coinpair-filter-empty">暂无冷静期币对</span>';
      const status = $("ignored-coinpair-status");
      if (status) {
        const manualCount = configuredIgnoredCoinpairs().length;
        status.textContent = (manualCount || blocks.length)
          ? ("当前生效：" + manualCount + " 个手动过滤，" + blocks.length + " 个动态冷静期")
          : "当前未启用";
      }
    }

    async function removeCoinpairCooldown(button) {
      const keyword = String(button && button.dataset.cooldownKeyword || "").trim().toUpperCase();
      const key = ignoredCoinpairKey(keyword);
      if (!keyword || !key || state.coinpairCooldownRemoving[key]) return;
      state.coinpairCooldownRemoving[key] = true;
      renderCoinpairCooldownList();
      try {
        await api("/tvbot/coinpair-blocks/" + encodeURIComponent(keyword), { method: "DELETE" });
        await loadCoinpairBlocks();
        toast(keyword + " 已移出冷静期");
      } finally {
        delete state.coinpairCooldownRemoving[key];
        renderCoinpairCooldownList();
      }
    }

    async function persistIgnoredCoinpairs(keywords) {
      state.config = await api("/tvbot/config", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ trading: { ignored_coinpairs: keywords } })
      });
      renderConfig();
      renderDashboard();
    }

    async function addIgnoredCoinpair() {
      const keyword = currentIgnoredCoinpairInput();
      const key = ignoredCoinpairKey(keyword);
      if (!key) return;
      const keywords = configuredIgnoredCoinpairs();
      if (keywords.some((item) => ignoredCoinpairKey(item) === key)) {
        toast("该币对过滤已存在");
        return;
      }
      await persistIgnoredCoinpairs(keywords.concat([keyword]));
      const input = $("ignored-coinpair");
      if (input) input.value = "";
      syncIgnoredCoinpairControls();
      toast("币对过滤已添加");
    }

    async function removeIgnoredCoinpair(index) {
      const keywords = configuredIgnoredCoinpairs();
      if (!Number.isInteger(index) || index < 0 || index >= keywords.length) return;
      const removed = keywords[index];
      keywords.splice(index, 1);
      await persistIgnoredCoinpairs(keywords);
      toast(removed + " 已恢复后续下单");
    }

    function orderHistoryStatusText(status) {
      const labels = {
        submitted: "已下单",
        ignored: "已忽略",
        failed: "下单失败",
        rejected: "已拒绝",
        duplicate: "重复信号",
        accepted: "处理中"
      };
      return labels[status] || status;
    }

    function orderCanRetry(order) {
      return !!(order && order.signal_id && (order.status === "failed" || order.status === "ignored") && !unsupportedOKXInstrumentMessage(order));
    }

    function unsupportedOKXInstrumentMessage(order) {
      if (!order) return "";
      const result = order.result || {};
      const targetExchange = normalizeExchange(order.target_exchange || result.target_exchange);
      if (targetExchange !== "okx") return "";
      const details = [order.error_code, order.error, result.okx_code, result.okx_msg].filter(Boolean).join(" ").toLowerCase();
      if (!details.includes("okx_instrument_unsupported") && !/(^|[^0-9])51001([^0-9]|$)/.test(details)) return "";
      const instID = normalizePrecisionInstID("okx", result.inst_id || order.coinpair || order.ticker) || "该币对";
      const tradeEnv = order.trade_env === "live" ? "实盘" : "模拟盘";
      return "OKX " + tradeEnv + "不支持 " + instID + "，无法重试";
    }

    function renderOrders() {
      const total = Number(state.ordersTotal || 0);
      const totalPages = ordersTotalPages();
      state.ordersPage = Math.min(Math.max(1, Number(state.ordersPage || 1)), totalPages);
      const search = String(state.ordersSearch || "").trim();
      const statusFilter = String(state.ordersStatus || "").trim();
      const filters = [];
      if (statusFilter) filters.push("状态: " + orderHistoryStatusText(statusFilter));
      if (search) filters.push("搜索: " + search);
      const rows = (state.orders || []).map((order, index) => {
        const targetExchange = normalizeExchange(order.target_exchange || (order.result && order.result.target_exchange));
        const precisionInstID = order.result && order.result.inst_id ? order.result.inst_id : order.coinpair;
        const okxResult = targetExchange === "okx" && order.result && (order.result.ord_id || order.result.okx_code) ? [order.result.ord_id, order.result.okx_code].filter(Boolean).join(" / ") : "";
        const binanceResult = targetExchange === "binance" && order.result && (order.result.ord_id || order.result.binance_code || order.result.binance_msg) ? [order.result.ord_id, order.result.binance_code, order.result.binance_msg].filter(Boolean).join(" / ") : "";
        const errorText = [order.error_code, order.error].filter(Boolean).join(": ");
        const unsupportedMessage = unsupportedOKXInstrumentMessage(order);
        const exchangeResult = unsupportedMessage || okxResult || binanceResult || errorText || "-";
        const apiID = order.api_id || (order.result && order.result.api_id);
        const sourceExchange = order.source_exchange || "-";
        const tradeEnvText = order.trade_env === "live" ? "实盘" : "模拟";
        const targetText = exchangeLabel(targetExchange) + " " + tradeEnvText + " / " + apiDisplayName(apiID, targetExchange);
        const tone = order.status === "submitted" ? "ok" : (order.status === "failed" || order.status === "rejected" ? "bad" : "warn");
        const canRetry = orderCanRetry(order);
        const retrying = canRetry && state.retrying[order.signal_id];
        const retryButton = canRetry ? '<button class="btn small" type="button" data-retry-id="' + escapeHTML(order.signal_id) + '"' + (retrying ? " disabled" : "") + ">" + (retrying ? "重试中" : "重试") + "</button>" : "";
        const rawJSONButton = orderRawJSONText(order) ? '<button class="btn small order-json-button" type="button" data-order-json-index="' + index + '">JSON</button>' : "";
        const statusCell = '<div class="status-cell">' + pill(orderHistoryStatusText(order.status), tone) + rawJSONButton + "</div>";
        return "<tr>" +
          '<td class="time">' + escapeHTML(shanghaiTime(order.accepted_at)) + "</td>" +
          '<td class="order-status">' + statusCell + "</td>" +
          "<td>" + escapeHTML(sourceExchange) + "</td>" +
          '<td class="order-target">' + escapeHTML(targetText) + "</td>" +
          "<td>" + escapeHTML(orderHistoryDirectionText(order)) + "</td>" +
          '<td class="order-signal-route">' + escapeHTML(orderHistorySignalRouteText(order)) + "</td>" +
          '<td class="order-adx">' + escapeHTML(orderHistoryADXText(order)) + "</td>" +
          '<td class="order-market">' + escapeHTML(orderHistoryMarketText(order)) + "</td>" +
          "<td>" + escapeHTML(asText(order.coinpair)) + "</td>" +
          "<td>" + escapeHTML(formatCachedSymbolPrice(targetExchange, precisionInstID, order.price)) + "</td>" +
          "<td>" + escapeHTML(asText(order.amount)) + "</td>" +
          '<td class="order-okx"><div class="okx-cell"><span class="okx-text">' + escapeHTML(exchangeResult) + "</span>" + retryButton + "</div></td>" +
          "</tr>";
      });
      $("order-rows").innerHTML = rows.join("") || '<tr><td colspan="12" class="muted">' + (filters.length ? "无匹配订单" : "-") + '</td></tr>';
      const status = $("order-history-status");
      const pageInfo = $("order-page-info");
      const prev = $("order-prev");
      const next = $("order-next");
      if (status) status.textContent = total ? ("共 " + total + " 条" + (filters.length ? " / " + filters.join(" / ") : "")) : (filters.length ? "无匹配 / " + filters.join(" / ") : "-");
      if (pageInfo) pageInfo.textContent = total ? ("第 " + state.ordersPage + " / " + totalPages + " 页") : "-";
      if (prev) prev.disabled = state.ordersPage <= 1;
      if (next) next.disabled = state.ordersPage >= totalPages;
      syncOrderSearchControls();
    }

    function changeOrdersPage(delta) {
      const totalPages = ordersTotalPages();
      const nextPage = Math.min(Math.max(1, Number(state.ordersPage || 1) + delta), totalPages);
      if (nextPage === state.ordersPage) return;
      state.ordersPage = nextPage;
      loadOrders(false).catch((err) => toast(err.message));
    }

    function tradeMonitorTime(value) {
      if (!value) return "-";
      const numeric = Number(value);
      if (Number.isFinite(numeric) && numeric > 0) return shanghaiTime(numeric);
      return shanghaiTime(value);
    }

    function tradeMonitorStatusText(value) {
      const status = String(value || "").toLowerCase();
      if (status === "entry_pending") return "等待入口";
      if (status === "open") return "持仓中";
      if (status === "exited") return "已退出";
      if (status === "sl_hit") return "止损";
      if (status === "tp_hit") return "止盈";
      if (status === "reentry_submitted") return "已补回";
      if (status === "cooldown") return "冷却";
      if (status === "blocked") return "阻止";
      return asText(value);
    }

    function tradeMonitorStatusTone(value) {
      const status = String(value || "").toLowerCase();
      if (status === "open" || status === "tp_hit" || status === "reentry_submitted") return "ok";
      if (status === "sl_hit" || status === "cooldown" || status === "blocked") return "warn";
      return "";
    }

    function tradeMonitorEventText(value) {
      const eventType = String(value || "").toLowerCase();
      if (eventType === "poll_error") return "轮询错误";
      if (eventType === "lifecycle_open") return "入口成交";
      if (eventType === "lifecycle_exit") return "退出成交";
      if (eventType === "auto_reentry_cooldown") return "补回冷却";
      if (eventType === "auto_reentry_blocked") return "补回阻止";
      if (eventType === "auto_reentry_failed") return "补回失败";
      if (eventType === "auto_reentry_submitted") return "补回提交";
      return asText(value);
    }

    function tradeMonitorActionText(action) {
      const value = String(action || "").toLowerCase();
      if (value === "buy") return "多";
      if (value === "sell") return "空";
      return asText(action);
    }

    function tradeMonitorListText(values) {
      if (!Array.isArray(values) || values.length === 0) return "-";
      return values.filter((item) => item !== null && item !== undefined && String(item).trim() !== "").join(", ") || "-";
    }

    function renderTradeMonitor() {
      const data = state.tradeMonitor || {};
      const checkpoints = Array.isArray(data.checkpoints) ? data.checkpoints : [];
      const lifecycles = Array.isArray(data.lifecycles) ? data.lifecycles : [];
      const events = Array.isArray(data.events) ? data.events : [];
      const fillMonitor = data.fill_monitor || {};
      const autoReentry = data.auto_reentry || {};
      const statusText = state.tradeMonitorError || (data.updated_at ? "更新于 " + shanghaiTime(data.updated_at) : "-");
      $("trade-monitor-status").textContent = statusText;
      $("trade-monitor-running").textContent = data.running ? "running" : (fillMonitor.enabled ? "waiting" : "disabled");
      $("trade-monitor-reentry").textContent = autoReentry.enabled ? ("enabled / " + asText(autoReentry.reentry_amount_pct) + "%") : "disabled";
      $("trade-monitor-lifecycle-count").textContent = asText(lifecycles.length);
      $("trade-monitor-event-count").textContent = asText(events.length);
      $("trade-monitor-checkpoints").innerHTML = checkpoints.map((row) => {
        return "<tr>" +
          "<td>" + escapeHTML(exchangeLabel(row.exchange)) + "</td>" +
          "<td>" + escapeHTML(asText(row.api_id)) + "</td>" +
          "<td>" + escapeHTML(asText(row.symbol)) + "</td>" +
          "<td>" + escapeHTML(tradeMonitorTime(row.last_fill_time)) + "</td>" +
          "<td>" + escapeHTML(tradeMonitorTime(row.last_polled_at)) + "</td>" +
          "<td>" + escapeHTML(asText(row.last_error)) + "</td>" +
          "</tr>";
      }).join("") || '<tr><td colspan="6" class="muted">暂无 checkpoint</td></tr>';
      $("trade-monitor-lifecycles").innerHTML = lifecycles.map((row) => {
        const entryIDs = tradeMonitorListText(row.entry_order_ids);
        const exitID = row.exit_price ? (asText(row.exit_price) + " / " + asText(row.realized_pnl) + " USDT") : "-";
        const reentry = row.reentry_signal_id ? asText(row.reentry_signal_id) : ("次数 " + asText(row.reentry_count || 0));
        return "<tr>" +
          '<td class="time">' + escapeHTML(tradeMonitorTime(row.updated_at)) + "</td>" +
          "<td>" + pill(tradeMonitorStatusText(row.status), tradeMonitorStatusTone(row.status)) + "</td>" +
          "<td>" + escapeHTML(asText(row.api_id)) + "</td>" +
          "<td>" + escapeHTML(asText(row.symbol)) + "</td>" +
          "<td>" + escapeHTML(tradeMonitorActionText(row.action)) + "</td>" +
          "<td>" + escapeHTML(entryIDs) + "</td>" +
          "<td>" + escapeHTML(exitID) + "</td>" +
          "<td>" + escapeHTML(reentry) + "</td>" +
          "</tr>";
      }).join("") || '<tr><td colspan="8" class="muted">暂无 lifecycle</td></tr>';
      $("trade-monitor-events").innerHTML = events.map((row) => {
        return "<tr>" +
          '<td class="time">' + escapeHTML(tradeMonitorTime(row.event_time)) + "</td>" +
          "<td>" + escapeHTML(tradeMonitorEventText(row.event_type)) + "</td>" +
          "<td>" + escapeHTML(asText(row.api_id)) + "</td>" +
          "<td>" + escapeHTML(asText(row.symbol)) + "</td>" +
          "<td>" + escapeHTML(tradeMonitorStatusText(row.status)) + "</td>" +
          "<td>" + escapeHTML(asText(row.message)) + "</td>" +
          "</tr>";
      }).join("") || '<tr><td colspan="6" class="muted">暂无事件</td></tr>';
    }

    function renderUpgrade() {
      $("upgrade-output").textContent = formatUpgradeLog(state.upgrade);
    }

    function formatUpgradeLog(upgrade) {
      if (!upgrade || !upgrade.status) return "-";
      const lines = [];
      const status = asText(upgrade.status);
      const runID = asText(upgrade.run_id || "-");
      lines.push("[" + upgradeLogTime(upgrade.started_at) + "] upgrade " + status + " run_id=" + runID);
      if (upgrade.finished_at && !zeroTime(upgrade.finished_at)) {
        lines.push("[" + upgradeLogTime(upgrade.finished_at) + "] finished status=" + status);
      }
      const steps = Array.isArray(upgrade.steps) ? upgrade.steps : [];
      steps.forEach((step, index) => {
        const label = asText(step.name || ("step_" + (index + 1)));
        const started = upgradeLogTime(step.started || upgrade.started_at);
        lines.push("[" + started + "] " + label + " started");
        if (step.command) lines.push("  $ " + asText(step.command));
        appendUpgradeBlock(lines, step.output, "  | ");
        if (step.error) appendUpgradeBlock(lines, step.error, "  ! ");
        if (step.finished && !zeroTime(step.finished)) {
          lines.push("[" + upgradeLogTime(step.finished) + "] " + label + " finished");
        }
      });
      if (upgrade.error) appendUpgradeBlock(lines, upgrade.error, "ERROR ");
      return lines.join("\n");
    }

    function appendUpgradeBlock(lines, text, prefix) {
      const raw = (text === null || text === undefined ? "" : String(text)).replace(/\r\n/g, "\n").replace(/\r/g, "\n").trimEnd();
      if (!raw) return;
      raw.split("\n").forEach((line) => lines.push(prefix + line));
    }

    function upgradeLogTime(value) {
      if (!value || zeroTime(value)) return "-";
      return shanghaiTime(value);
    }

    function zeroTime(value) {
      return !value || String(value).startsWith("0001-01-01");
    }

    async function saveConfig() {
      const patch = {
        server: { addr: $("cfg-addr").value.trim() },
        data_file: $("cfg-data-file").value.trim(),
        database_file: $("cfg-database-file").value.trim(),
        trading: {
          env: $("cfg-env").value,
          allow_live_trading: $("cfg-live").checked,
          base_url: $("cfg-base-url").value.trim(),
          binance_base_url: $("cfg-binance-base-url").value.trim(),
          binance_demo_base_url: $("cfg-binance-demo-base-url").value.trim(),
          default_margin_mode: $("cfg-margin").value,
          position_mode: $("cfg-position").value,
          signal_ttl_seconds: Number($("cfg-ttl").value)
        }
      };
      state.config = await api("/tvbot/config", { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify(patch) });
      applyMenuSettings();
      renderConfig();
      renderDashboard();
      renderMenuSettings();
      updateMetrics();
      toast("订单配置已保存");
    }

    async function saveOrderSettings() {
      const previousScope = marketTurnoverScope();
      const patch = {
        trading: {
          order_amount_usdt: Number($("order-amount").value),
          leverage: Number($("order-leverage").value),
          order_type: $("order-type").value,
          market_turnover_scope: $("order-turnover-scope").value,
          risk_type: $("order-risk-type").value,
          take_profit_pct: Number($("order-tp").value),
          stop_loss_pct: Number($("order-sl").value),
          trailing_pct: Number($("order-trailing").value),
          long_limit_price_multiplier: Number($("order-long-multiplier").value),
          short_limit_price_multiplier: Number($("order-short-multiplier").value)
        }
      };
      state.config = await api("/tvbot/config", { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify(patch) });
      applyMenuSettings();
      renderOrderSettings();
      renderMarketTurnoverScope();
      renderDashboard();
      renderMenuSettings();
      updateMetrics();
      toast("下单设置已保存");
      if (previousScope !== marketTurnoverScope()) {
        await loadSymbols(false).catch((err) => toast("下单设置已保存，币对列表刷新失败：" + err.message));
      }
    }

    async function savePositionMonitor() {
      const patch = {
        trading: {
          position_monitor: {
            okx_enabled: $("position-monitor-okx-enabled").checked,
            binance_enabled: $("position-monitor-binance-enabled").checked,
            poll_interval_seconds: Number($("position-monitor-interval").value),
            take_profit_pct: Number($("position-monitor-tp").value),
            stop_loss_pct: Number($("position-monitor-sl").value)
          }
        }
      };
      state.config = await api("/tvbot/config", { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify(patch) });
      renderPositionMonitor();
      renderDashboard();
      updateMetrics();
      toast("自动扫描持仓已保存");
    }

    async function saveMenuSettings() {
      const patch = { ui: { default_tab: configuredDefaultTab(), menu_items: currentMenuItems() } };
      state.config = await api("/tvbot/config", { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify(patch) });
      applyMenuSettings();
      syncActiveTabAfterMenuSettings();
      renderMenuSettings();
      toast("菜单设置已保存");
    }

    async function saveAPIKeys() {
      const exchange = normalizeExchange(state.apiKeyExchange);
      const body = {
        exchange: exchange,
        id: $("key-id").value.trim(),
        name: $("key-name").value.trim(),
        api_key: $("key-api").value.trim(),
        secret_key: $("key-secret").value.trim(),
        passphrase: exchange === "okx" ? $("key-passphrase").value.trim() : "",
        active: $("key-active").checked
      };
      state.apiKeysByExchange[exchange] = await api("/tvbot/api-keys?exchange=" + encodeURIComponent(exchange), { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
      state.apiKeys = apiKeyStatus(exchange);
      state.selectedAPIID = body.id || state.apiKeys.active_id || "default";
      state.selectedAPIIDs[exchange] = state.selectedAPIID;
      state.apiKeyTest = null;
      state.apiKeyTestID = "";
      state.apiKeyTestExchange = exchange;
      $("key-api").value = "";
      $("key-secret").value = "";
      $("key-passphrase").value = "";
      renderAPIKeys();
      renderTemplateAPIs();
      renderPositionAPIs();
      renderOrders();
      updateMetrics();
      toast("API Key 已保存");
    }

    async function testAPIKeys() {
      const exchange = normalizeExchange(state.apiKeyExchange);
      const body = {
        exchange: exchange,
        id: $("key-id").value.trim(),
        api_key: $("key-api").value.trim(),
        secret_key: $("key-secret").value.trim(),
        passphrase: exchange === "okx" ? $("key-passphrase").value.trim() : ""
      };
      $("okx-output").textContent = "checking...";
      const result = await api("/tvbot/api-keys/test", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
      state.apiKeyTest = result;
      state.apiKeyTestID = result.api_id || body.id || $("key-selected").value || "";
      state.apiKeyTestExchange = exchange;
      $("okx-output").textContent = JSON.stringify(result, null, 2);
      renderAPIKeyStatus(body.id || state.selectedAPIID || result.api_id || "");
      toast(exchangeLabel(exchange) + " API 可用");
    }

    async function deleteAPIKey() {
      const exchange = normalizeExchange(state.apiKeyExchange);
      const id = $("key-id").value.trim() || $("key-selected").value;
      if (!id) return;
      state.apiKeysByExchange[exchange] = await api("/tvbot/api-keys?exchange=" + encodeURIComponent(exchange) + "&id=" + encodeURIComponent(id), { method: "DELETE" });
      state.apiKeys = apiKeyStatus(exchange);
      state.selectedAPIID = state.apiKeys.active_id || "";
      state.selectedAPIIDs[exchange] = state.selectedAPIID;
      state.apiKeyTest = null;
      state.apiKeyTestID = "";
      state.apiKeyTestExchange = exchange;
      renderAPIKeys();
      renderTemplateAPIs();
      renderPositionAPIs();
      renderOrders();
      updateMetrics();
      toast("API Key 已删除");
    }

    async function renameActiveAPIID(button) {
      const exchange = normalizeExchange(state.apiKeyExchange);
      const status = apiKeyStatus(exchange);
      const currentID = (button && button.dataset.renameActiveApiId) || status.active_id || "";
      if (!currentID) return;
      const nextID = window.prompt("新的 app id", currentID);
      if (nextID === null) return;
      const trimmed = nextID.trim();
      if (!trimmed) {
        toast("app id 不能为空");
        return;
      }
      if (trimmed === currentID) return;
      if (button) button.disabled = true;
      try {
        state.apiKeysByExchange[exchange] = await api("/tvbot/api-keys?exchange=" + encodeURIComponent(exchange), {
          method: "PATCH",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ exchange: exchange, id: currentID, new_id: trimmed })
        });
      } finally {
        if (button) button.disabled = false;
      }
      state.apiKeys = apiKeyStatus(exchange);
      state.selectedAPIID = state.apiKeys.active_id || trimmed;
      state.selectedAPIIDs[exchange] = state.selectedAPIID;
      state.apiKeyTest = null;
      state.apiKeyTestID = "";
      state.apiKeyTestExchange = exchange;
      renderAPIKeys();
      renderTemplateAPIs();
      renderPositionAPIs();
      renderOrders();
      updateMetrics();
      toast("app id 已改名为 " + state.selectedAPIID);
    }

    async function makeTemplate() {
      const req = {
        target_exchange: normalizeExchange($("tpl-target-exchange").value),
        trade_env: templateTradeEnv(),
        api_id: $("tpl-api-id").value,
        coinpair: $("tpl-coinpair").value,
        direction: $("tpl-direction").value,
        price_source: $("tpl-price-source").value
      };
      const resp = await api("/tvbot/templates", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(req) });
      $("template-output").value = resp.json || "";
      renderTemplateTitle(new Date());
      toast("模板已生成");
    }

    function openTemplateFromSymbolButton(button) {
      const symbol = String(button.dataset.templateSymbol || "").trim();
      if (!symbol) return;
      const opened = window.open(templatePageURLFromButton(button), "_blank");
      if (opened) {
        try {
          opened.opener = null;
          opened.focus();
        } catch (err) {}
      } else {
        toast("浏览器拦截了弹出页面");
      }
    }

    async function applyTemplateHashParams() {
      const route = parsedHashRoute();
      if (route.tab !== "template") return;
      const params = route.params;
      const symbol = String(params.get("coinpair") || "").trim();
      if (!symbol) return;
      $("tpl-target-exchange").value = normalizeExchange(params.get("target_exchange") || "okx");
      $("tpl-trade-env").value = params.get("trade_env") === "live" ? "live" : "demo";
      $("tpl-direction").value = ["both", "long", "short"].includes(params.get("direction")) ? params.get("direction") : "both";
      $("tpl-price-source").value = ["close", "high", "low"].includes(params.get("price_source")) ? params.get("price_source") : "close";
      renderTemplateAPIs();
      const apiID = String(params.get("api_id") || "").trim();
      if (apiID) $("tpl-api-id").value = apiID;
      if (!state.symbols) {
        await loadSymbols(false);
      }
      renderTemplateCoinpairs();
      $("tpl-coinpair").value = symbol;
      await makeTemplate();
    }

    async function retryOrder(signalID) {
      if (!signalID || state.retrying[signalID]) return;
      state.retrying[signalID] = true;
      renderOrders();
      try {
        const result = await api("/tvbot/orders/" + encodeURIComponent(signalID) + "/retry", { method: "POST" });
        const retryPrice = result && result.price ? (" @ " + asText(result.price)) : "";
        if (result && result.status === "ignored") {
          toast("重试仍被忽略: " + asText(result.reason || "当前安全规则"));
        } else if (result && result.status === "duplicate") {
          toast("重试信号重复: " + asText(result.signal_id));
        } else {
          toast("按现价重试已触发 " + asText(result.signal_id) + retryPrice);
        }
        await loadOrders();
        window.setTimeout(() => loadOrders().catch((err) => toast(err.message)), 1600);
      } catch (err) {
        try { await loadOrders(); } catch (_) {}
        throw err;
      } finally {
        delete state.retrying[signalID];
        renderOrders();
      }
    }

    async function closePosition(button) {
      const exchange = normalizeExchange(button.dataset.exchange || "okx");
      const mode = button.dataset.positionClose;
      const instID = button.dataset.instId || "";
      const posSide = button.dataset.posSide || "";
      const pos = button.dataset.pos || "";
      const apiID = button.dataset.apiId || "";
      const ratio = Number(button.dataset.positionRatio || "0");
      const key = [exchange, apiID, instID.toUpperCase(), (posSide || "net").toLowerCase()].join("|");
      if (!instID || !mode || state.positionClosing[key]) return;
      state.positionClosing[key] = true;
      renderPositions();
      try {
        const body = {
          exchange,
          api_id: apiID,
          inst_id: instID,
          pos_side: posSide,
          mode
        };
        if (Number.isFinite(ratio) && ratio > 0 && ratio < 1) body.ratio = ratio;
        const result = await api("/tvbot/positions/close", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(body)
        });
        const limitCloseText = "限价平仓已启动 ";
        const ratioText = body.ratio ? " " + Math.round(body.ratio * 100) + "%" : "";
        if (result && result.status === "unknown") {
          const clientID = result.cl_ord_id ? " " + asText(result.cl_ord_id) : "";
          toast("Binance 平仓状态同步中" + clientID + "，请刷新确认后再重试");
        } else {
          const marketCloseText = result && result.px
            ? "市价平仓受价格保护，已转限价平仓 " + asText(result.px)
            : "市价平仓已提交";
          toast(mode === "market" ? marketCloseText + ratioText : limitCloseText + asText(result.px) + ratioText);
        }
        rememberLimitClosePendingOrder(result, Object.assign({}, body, { pos: pos }));
        await loadPositionView();
        window.setTimeout(() => loadPositionView().catch((err) => toast(err.message)), mode === "market" ? 1600 : 5200);
      } finally {
        delete state.positionClosing[key];
        renderPositions();
      }
    }

    function pendingOrderActionKeyFromBody(body) {
      const group = body && body.order_group === "algo" ? "algo" : "normal";
      const orderID = group === "algo"
        ? (body.algo_id || ("algo-cl:" + (body.algo_cl_ord_id || "")))
        : (body.ord_id || ("cl:" + (body.cl_ord_id || "")));
      return [normalizeExchange(body && body.exchange), body && body.api_id || "", String(body && body.inst_id || "").toUpperCase(), group, orderID].join("|");
    }

    function pendingOrderBodyHasID(body) {
      if (!body || !body.inst_id) return false;
      if (body.order_group === "algo") return !!(body.algo_id || body.algo_cl_ord_id);
      return !!(body.ord_id || body.cl_ord_id);
    }

    async function chasePendingOrder(button) {
      const exchange = normalizeExchange(button.dataset.exchange || "okx");
      const mode = button.dataset.pendingChase;
      const priceError = button.dataset.priceError || "";
      if (mode === "start" && priceError) {
        toast(priceError);
        return;
      }
      const apiID = button.dataset.apiId || "";
      const orderGroup = button.dataset.orderGroup === "algo" ? "algo" : "normal";
      const body = {
        exchange: exchange,
        api_id: apiID,
        order_group: orderGroup,
        inst_id: button.dataset.instId || "",
        ord_id: button.dataset.ordId || "",
        cl_ord_id: button.dataset.clOrdId || "",
        algo_id: button.dataset.algoId || "",
        algo_cl_ord_id: button.dataset.algoClOrdId || ""
      };
      const key = pendingOrderActionKeyFromBody(body);
      if (!pendingOrderBodyHasID(body) || state.pendingOrderActions[key]) return;
      state.pendingOrderActions[key] = true;
      renderPendingOrders();
      try {
        const path = mode === "stop" ? "/tvbot/pending-orders/chase/stop" : "/tvbot/pending-orders/chase";
        const result = await api(path, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
        const detail = result && result.px ? " " + asText(result.px) : "";
        if (orderGroup === "algo") {
          toast(mode === "stop" ? "算法订单追单已停止" : ("算法订单追单已启动" + detail));
        } else {
          toast(mode === "stop" ? "追单已停止" : ("追单已启动，60秒未成交将转市价 " + asText(result.px)));
        }
        await loadPendingOrders();
      } finally {
        delete state.pendingOrderActions[key];
        renderPendingOrders();
      }
    }

    async function cancelPendingOrder(button) {
      const exchange = normalizeExchange(button.dataset.exchange || "okx");
      const orderGroup = button.dataset.orderGroup === "algo" ? "algo" : "normal";
      const body = {
        exchange: exchange,
        order_group: orderGroup,
        api_id: button.dataset.apiId || "",
        inst_id: button.dataset.instId || "",
        ord_id: button.dataset.ordId || "",
        cl_ord_id: button.dataset.clOrdId || "",
        algo_id: button.dataset.algoId || "",
        algo_cl_ord_id: button.dataset.algoClOrdId || ""
      };
      const key = pendingOrderActionKeyFromBody(body);
      if (!pendingOrderBodyHasID(body) || state.pendingOrderActions[key]) return;
      state.pendingOrderActions[key] = true;
      renderPendingOrders();
      try {
        const result = await api("/tvbot/pending-orders/cancel", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
        const groupLabel = orderGroup === "algo" ? "算法订单" : "挂单";
        toast(result.status === "finished" ? groupLabel + "已不存在" : exchangeLabel(exchange) + " " + groupLabel + "已取消");
        await loadPendingOrders();
      } finally {
        delete state.pendingOrderActions[key];
        renderPendingOrders();
      }
    }

    async function checkOKX() {
      $("okx-output").textContent = "checking...";
      try {
        const exchange = activeExchange();
        const status = apiKeyStatus(exchange);
        const body = status && status.active_id ? { exchange: exchange, api_id: status.active_id } : { exchange: exchange };
        const result = await api("/tvbot/check-okx", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
        $("okx-output").textContent = JSON.stringify(result, null, 2);
      } catch (err) {
        $("okx-output").textContent = err.message;
      }
    }

    async function setBalanceWindow(minutes) {
      const parsed = Math.max(0, Number(minutes || 0));
      if (!Number.isFinite(parsed)) return;
      state.balanceWindowMinutes = parsed;
      updateBalanceWindowButtons();
      await loadAnalysis(false);
      toast("周期已切换为 " + balanceWindowLabel(parsed));
    }

    async function resetBalanceBaseline(button) {
      const original = button ? button.textContent : "";
      if (button) {
        button.disabled = true;
        button.textContent = "重置中";
      }
      try {
        state.balanceWindowMinutes = 0;
        updateBalanceWindowButtons();
        await loadAnalysis(false);
        toast("基准已重置为当前余额");
      } finally {
        if (button) {
          button.disabled = false;
          button.textContent = original || "重置基准";
        }
      }
    }

    async function syncBalanceHistory(button) {
      const original = button ? button.textContent : "";
      if (button) {
        button.disabled = true;
        button.textContent = "同步中";
      }
      try {
        await loadAnalysis(false);
        toast("余额历史已同步");
      } finally {
        if (button) {
          button.disabled = false;
          button.textContent = original || "同步历史";
        }
      }
    }

    async function startUpgrade() {
      $("upgrade-output").textContent = "[" + shanghaiTime(new Date().toISOString()) + "] starting upgrade...";
      state.upgrade = await api("/upgrade", { method: "POST" });
      renderUpgrade();
      updateMetrics();
      toast("升级已开始");
      pollUpgrade();
    }

    async function pollUpgrade() {
      for (let i = 0; i < 80; i++) {
        await new Promise((resolve) => setTimeout(resolve, 2000));
        try {
          await loadUpgrade();
          if (state.upgrade && state.upgrade.status && state.upgrade.status !== "running") break;
        } catch (_) {}
      }
    }

    document.querySelectorAll("nav button").forEach((button) => {
      button.addEventListener("click", () => {
        activateTab(button.dataset.tab, true);
      });
    });
    document.querySelectorAll("[data-global-exchange]").forEach((button) => {
      button.addEventListener("click", () => {
        setSelectedExchange(button.dataset.globalExchange);
      });
    });

    $("refresh-all").addEventListener("click", () => loadAll().then(() => toast("已刷新")).catch((err) => toast(err.message)));
    $("check-okx").addEventListener("click", () => checkOKX());
    $("save-config").addEventListener("click", () => saveConfig().catch((err) => toast(err.message)));
    $("refresh-symbols").addEventListener("click", () => syncSymbols(true).then(() => toast("币对已同步")).catch((err) => toast(err.message)));
    $("symbol-search").addEventListener("input", () => renderSymbols());
    $("clear-symbol-search").addEventListener("click", () => clearSymbolSearch());
    $("symbol-exchange").addEventListener("change", () => renderSymbols());
    $("symbol-env").addEventListener("change", () => renderSymbols());
    $("symbol-turnover-scope").addEventListener("change", () => saveMarketTurnoverScope().catch((err) => toast(err.message)));
    $("symbol-head").addEventListener("click", (event) => {
      if (tableColumnDropSuppressClick) return;
      const th = event.target.closest("th[data-symbol-sort]");
      if (!th) return;
      setSymbolSort(th.dataset.symbolSort);
    });
    $("save-order-settings").addEventListener("click", () => saveOrderSettings().catch((err) => toast(err.message)));
    ["order-amount", "order-leverage", "order-type", "order-turnover-scope", "order-risk-type", "order-tp", "order-sl", "order-trailing", "order-long-multiplier", "order-short-multiplier"].forEach((id) => {
      $(id).addEventListener("input", () => renderOrderSettingsPreview());
      $(id).addEventListener("change", () => renderOrderSettingsPreview());
    });
    $("save-position-monitor").addEventListener("click", () => savePositionMonitor().catch((err) => toast(err.message)));
    $("save-menu-settings").addEventListener("click", () => saveMenuSettings().catch((err) => toast(err.message)));
    $("menu-settings-rows").addEventListener("input", (event) => {
      const input = event.target.closest("input[data-menu-label]");
      if (!input) return;
      const items = currentMenuItems();
      const item = items.find((entry) => entry.tab === input.dataset.menuLabel);
      if (!item) return;
      item.label = input.value;
      setCurrentMenuItems(items);
      applyMenuSettings();
    });
    $("menu-settings-rows").addEventListener("change", (event) => {
      const homeInput = event.target.closest("input[data-menu-home]");
      if (homeInput) {
        if (!state.config) state.config = {};
        if (!state.config.ui) state.config.ui = {};
        if (menuDefinition(homeInput.dataset.menuHome)) state.config.ui.default_tab = homeInput.dataset.menuHome;
        renderMenuSettings();
        return;
      }
      const input = event.target.closest("input[data-menu-hidden]");
      if (!input) return;
      const items = currentMenuItems();
      const item = items.find((entry) => entry.tab === input.dataset.menuHidden);
      if (!item) return;
      item.hidden = input.checked;
      setCurrentMenuItems(items);
      renderMenuSettings();
      applyMenuSettings();
    });
    $("menu-settings-rows").addEventListener("click", (event) => {
      const button = event.target.closest("button[data-menu-move]");
      if (!button) return;
      moveMenuItem(Number(button.dataset.menuIndex), Number(button.dataset.menuMove));
    });
    $("save-api-keys").addEventListener("click", () => saveAPIKeys().catch((err) => toast(err.message)));
    $("test-api-keys").addEventListener("click", () => testAPIKeys().catch((err) => {
      $("okx-output").textContent = err.message;
      toast(err.message);
    }));
    $("delete-api-key").addEventListener("click", () => deleteAPIKey().catch((err) => toast(err.message)));
    $("api-key-status").addEventListener("click", (event) => {
      const button = event.target.closest("button[data-rename-active-api-id]");
      if (!button) return;
      renameActiveAPIID(button).catch((err) => toast(err.message));
    });
    document.querySelectorAll("[data-api-key-exchange]").forEach((button) => {
      button.addEventListener("click", () => {
        state.apiKeyExchange = normalizeExchange(button.dataset.apiKeyExchange);
        state.apiKeys = apiKeyStatus(state.apiKeyExchange);
        state.apiKeyTest = null;
        state.apiKeyTestID = "";
        state.apiKeyTestExchange = state.apiKeyExchange;
        renderAPIKeys();
        renderTemplateAPIs();
        renderPositionAPIs();
        updateMetrics();
      });
    });
    $("add-api-key").addEventListener("click", () => {
      const exchange = normalizeExchange(state.apiKeyExchange);
      state.selectedAPIID = "";
      state.selectedAPIIDs[exchange] = "";
      state.apiKeyTest = null;
      state.apiKeyTestID = "";
      state.apiKeyTestExchange = exchange;
      $("key-selected").value = "";
      $("key-id").value = "";
      $("key-name").value = "";
      const status = apiKeyStatus(exchange);
      $("key-active").checked = !status || !status.configured;
      $("key-api").value = "";
      $("key-secret").value = "";
      $("key-passphrase").value = "";
      renderAPIKeyStatus("");
      $("key-id").focus();
    });
    $("key-selected").addEventListener("change", () => {
      state.selectedAPIID = $("key-selected").value;
      state.selectedAPIIDs[normalizeExchange(state.apiKeyExchange)] = state.selectedAPIID;
      fillAPIForm(state.selectedAPIID);
      renderAPIKeyStatus(state.selectedAPIID);
    });
    document.querySelectorAll("[data-balance-minutes]").forEach((button) => {
      button.addEventListener("click", () => setBalanceWindow(button.dataset.balanceMinutes).catch((err) => toast(err.message)));
    });
    $("reset-balance-baseline").addEventListener("click", () => resetBalanceBaseline($("reset-balance-baseline")).catch((err) => toast(err.message)));
    $("sync-balance-history").addEventListener("click", () => syncBalanceHistory($("sync-balance-history")).catch((err) => toast(err.message)));
    $("analysis-trade-prev").addEventListener("click", () => changeAnalysisTradePage(-1));
    $("analysis-trade-next").addEventListener("click", () => changeAnalysisTradePage(1));
    $("analysis-trade-rows").addEventListener("click", (event) => {
      const button = event.target.closest("button[data-analysis-cooldown]");
      if (!button) return;
      addAnalysisCoinpairCooldown(button).catch((err) => toast(err.message));
    });
    $("refresh-positions").addEventListener("click", () => loadPositionView(true).then(() => toast("持仓和挂单已刷新")).catch((err) => toast(err.message)));
    document.querySelectorAll("[data-pending-order-group]").forEach((button) => {
      button.addEventListener("click", () => {
        state.pendingOrderGroup = button.dataset.pendingOrderGroup === "algo" ? "algo" : "normal";
        renderPendingOrders();
      });
    });
    $("tpl-target-exchange").addEventListener("change", () => {
      renderTemplateAPIs();
      renderTemplateCoinpairs();
      renderTemplateTitle(new Date());
    });
    $("tpl-trade-env").addEventListener("change", () => {
      renderTemplateCoinpairs();
      renderTemplateTitle(new Date());
    });
    $("tpl-coinpair").addEventListener("input", () => renderTemplateTitle(new Date()));
    $("make-template").addEventListener("click", () => makeTemplate().catch((err) => toast(err.message)));
    $("symbol-rows").addEventListener("click", (event) => {
      const button = event.target.closest("button[data-symbol-template]");
      if (!button) return;
      openTemplateFromSymbolButton(button);
    });
    $("copy-webhook-url").addEventListener("click", async () => {
      renderTemplateWebhookURL();
      await navigator.clipboard.writeText($("template-webhook-url").value);
      toast("Webhook URL 已复制");
    });
    $("copy-template-title").addEventListener("click", async () => {
      await navigator.clipboard.writeText(renderTemplateTitle());
      toast("报警标题已复制");
    });
    $("copy-template").addEventListener("click", async () => {
      await navigator.clipboard.writeText($("template-output").value);
      toast("已复制");
    });
    $("close-raw-json").addEventListener("click", () => closeRawJSONDialog());
    $("copy-raw-json").addEventListener("click", async () => {
      await navigator.clipboard.writeText($("raw-json-output").textContent || "");
      toast("原始 JSON 已复制");
    });
    $("order-rows").addEventListener("click", (event) => {
      const jsonButton = event.target.closest("button[data-order-json-index]");
      if (jsonButton) {
        const order = state.orders[Number(jsonButton.dataset.orderJsonIndex)];
        showOrderRawJSON(order);
        return;
      }
      const button = event.target.closest("button[data-retry-id]");
      if (!button) return;
      retryOrder(button.dataset.retryId).catch((err) => toast(err.message));
    });
    $("order-prev").addEventListener("click", () => changeOrdersPage(-1));
    $("order-next").addEventListener("click", () => changeOrdersPage(1));
    $("order-status-filter").addEventListener("change", () => applyOrderStatusFilter().catch((err) => toast(err.message)));
    $("order-search").addEventListener("input", () => syncOrderSearchControls());
    $("order-search").addEventListener("keydown", (event) => {
      if (event.key !== "Enter") return;
      event.preventDefault();
      applyOrderSearch().catch((err) => toast(err.message));
    });
    $("search-orders").addEventListener("click", () => applyOrderSearch().catch((err) => toast(err.message)));
    $("clear-order-search").addEventListener("click", () => clearOrderSearch().catch((err) => toast(err.message)));
    $("ignored-coinpair").addEventListener("input", () => syncIgnoredCoinpairControls());
    $("ignored-coinpair").addEventListener("keydown", (event) => {
      if (event.key !== "Enter") return;
      event.preventDefault();
      addIgnoredCoinpair().catch((err) => toast(err.message));
    });
    $("add-ignored-coinpair").addEventListener("click", () => addIgnoredCoinpair().catch((err) => toast(err.message)));
    $("loss-cooldown-enabled").addEventListener("change", () => syncLossCooldownControls());
    $("loss-cooldown-hours").addEventListener("input", () => syncLossCooldownControls());
    $("save-loss-cooldown").addEventListener("click", () => saveLossCooldownSettings().catch((err) => toast(err.message)));
    $("ignored-coinpair-list").addEventListener("click", (event) => {
      const button = event.target.closest("button[data-ignored-coinpair-index]");
      if (!button) return;
      removeIgnoredCoinpair(Number(button.dataset.ignoredCoinpairIndex)).catch((err) => toast(err.message));
    });
    $("coinpair-cooldown-list").addEventListener("click", (event) => {
      const button = event.target.closest("button[data-cooldown-keyword]");
      if (!button) return;
      removeCoinpairCooldown(button).catch((err) => toast(err.message));
    });
    $("refresh-trade-monitor").addEventListener("click", () => loadTradeMonitor().then(() => toast("成交监听已刷新")).catch((err) => toast(err.message)));
    $("position-rows").addEventListener("click", (event) => {
      const button = event.target.closest("button[data-position-close]");
      if (!button) return;
      closePosition(button).catch((err) => toast(err.message));
    });
    $("pending-order-rows").addEventListener("click", (event) => {
      const button = event.target.closest("button[data-pending-chase]");
      if (button) {
        chasePendingOrder(button).catch((err) => toast(err.message));
        return;
      }
      const cancelButton = event.target.closest("button[data-pending-cancel]");
      if (!cancelButton) return;
      cancelPendingOrder(cancelButton).catch((err) => toast(err.message));
    });
    $("refresh-orders").addEventListener("click", () => Promise.all([loadOrders(), loadCoinpairBlocks()]).then(() => toast("订单已刷新")).catch((err) => toast(err.message)));
    $("refresh-upgrade").addEventListener("click", () => loadUpgrade().then(() => toast("升级状态已刷新")).catch((err) => toast(err.message)));
    $("start-upgrade").addEventListener("click", () => startUpgrade().catch((err) => toast(err.message)));
    document.addEventListener("visibilitychange", () => {
      if (!document.hidden && analysisTabActive()) {
        refreshAnalysisBalanceOverviewAuto();
      }
    });

    state.selectedExchange = storedSelectedExchange();
    renderGlobalExchangeSwitch();
    renderTemplateWebhookURL();
    renderTemplateTitle(new Date());
    updateBalanceWindowButtons();
    initTableColumnDrag();
    initAnalysisTradeColumnDrag();
    renderPositions();
    renderPendingOrders();
    activateTab(initialTab(), false);
    loadAll().then(() => applyTemplateHashParams()).catch((err) => toast(err.message));
  </script>
</body>
</html>`
