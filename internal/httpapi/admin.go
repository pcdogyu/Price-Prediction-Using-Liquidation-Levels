package httpapi

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/authn"
)

//go:embed admin.html
var adminHTML []byte

type adminUserStore interface {
	ListAuthUsers(context.Context) ([]authn.UserRecord, error)
	CreateAuthUser(context.Context, string, string, string) error
	UpsertAuthUser(context.Context, string, string) error
	SetAuthUserState(context.Context, string, string, bool) error
	DeleteAuthUser(context.Context, string) error
	RecordAuthAudit(context.Context, string, string, string) error
	LoadAdminMenu(context.Context) ([]byte, bool, error)
	SaveAdminMenu(context.Context, []byte) error
	LoadUserMenu(context.Context, string) ([]byte, bool, error)
	SaveUserMenu(context.Context, string, []byte) error
	LoadUserTheme(context.Context, string) (string, bool, error)
	SaveUserTheme(context.Context, string, string) error
}

type adminMenuItem struct {
	Key   string `json:"key"`
	Title string `json:"title"`
	Href  string `json:"href"`
	Group string `json:"group"`
}

func defaultAdminMenu() []adminMenuItem {
	return []adminMenuItem{
		{Key: "alerts", Title: "报警", Href: "/alerts", Group: "liquidation"},
		{Key: "bubbles", Title: "气泡图", Href: "/bubbles", Group: "liquidation"},
		{Key: "liquidations", Title: "清算历史", Href: "/liquidations", Group: "liquidation"},
		{Key: "hedge-wall", Title: "对冲墙", Href: "/hedge-wall", Group: "liquidation"},
		{Key: "market-info", Title: "市场信息", Href: "/market-info", Group: "liquidation"},
		{Key: "options", Title: "期权", Href: "/options", Group: "liquidation"},
		{Key: "overview", Title: "资金", Href: "/tvbot/view/dashboard", Group: "trading"},
		{Key: "positions", Title: "持仓", Href: "/tvbot/view/positions", Group: "trading"},
		{Key: "analysis", Title: "订单分析", Href: "/tvbot/view/analysis", Group: "trading"},
		{Key: "symbols", Title: "币对配置", Href: "/tvbot/view/symbols", Group: "trading"},
		{Key: "config", Title: "订单配置", Href: "/tvbot/view/config", Group: "trading"},
		{Key: "api-keys", Title: "API Key", Href: "/tvbot/view/api-keys", Group: "trading"},
		{Key: "order-settings", Title: "下单设置", Href: "/tvbot/view/order-settings", Group: "trading"},
		{Key: "template", Title: "告警模板", Href: "/tvbot/view/template", Group: "trading"},
		{Key: "orders", Title: "订单", Href: "/tvbot/view/orders", Group: "trading"},
		{Key: "trade-monitor", Title: "成交监听", Href: "/tvbot/view/trade-monitor", Group: "trading"},
		{Key: "upgrade", Title: "升级", Href: "/tvbot/view/upgrade", Group: "admin"},
		{Key: "system-info", Title: "系统信息", Href: "/system", Group: "admin"},
		{Key: "menu", Title: "菜单管理", Href: "/menu", Group: "admin"},
		{Key: "system", Title: "用户管理", Href: "/admin", Group: "admin"},
	}
}

var legacyDefaultAdminMenuKeys = []string{
	"alerts", "bubbles", "liquidations", "hedge-wall", "market-info", "options",
	"overview", "positions", "orders", "analysis", "trade-monitor", "symbols",
	"api-keys", "order-settings", "template", "config", "system-info", "menu", "system", "upgrade",
}

var canonicalDefaultAdminMenuKeys = []string{
	"alerts", "bubbles", "liquidations", "hedge-wall", "market-info", "options",
	"overview", "positions", "analysis", "symbols", "config", "api-keys",
	"order-settings", "template", "orders", "trade-monitor", "upgrade", "system-info", "menu", "system",
}

var legacyPersonalMenuKeys = []string{
	"alerts", "bubbles", "liquidations", "hedge-wall", "market-info", "options",
	"overview", "orders", "positions", "analysis", "trade-monitor", "symbols",
	"api-keys", "order-settings", "template", "config", "upgrade", "system-info",
}

var legacyOrdersFirstAdminMenuKeys = []string{
	"alerts", "bubbles", "liquidations", "hedge-wall", "market-info", "options",
	"overview", "orders", "positions", "analysis", "trade-monitor", "symbols",
	"api-keys", "order-settings", "template", "config", "system-info", "menu", "system", "upgrade",
}

var canonicalPersonalMenuKeys = []string{
	"alerts", "bubbles", "liquidations", "hedge-wall", "market-info", "options",
	"overview", "positions", "analysis", "symbols", "config", "api-keys",
	"order-settings", "template", "orders", "trade-monitor", "upgrade", "system-info",
}

func reorderLegacyDefaultAdminMenu(menu []adminMenuItem) ([]adminMenuItem, bool) {
	if len(menu) != len(legacyDefaultAdminMenuKeys) {
		return menu, false
	}
	byKey := make(map[string]adminMenuItem, len(menu))
	for i, item := range menu {
		if item.Key != legacyDefaultAdminMenuKeys[i] {
			return menu, false
		}
		byKey[item.Key] = item
	}
	ordered := make([]adminMenuItem, 0, len(canonicalDefaultAdminMenuKeys))
	for _, key := range canonicalDefaultAdminMenuKeys {
		item, ok := byKey[key]
		if !ok {
			return menu, false
		}
		ordered = append(ordered, item)
	}
	return ordered, true
}

func reorderLegacyPersonalMenu(menu []adminMenuItem) ([]adminMenuItem, bool) {
	if len(menu) != len(legacyPersonalMenuKeys) {
		return menu, false
	}
	byKey := make(map[string]adminMenuItem, len(menu))
	for i, item := range menu {
		if item.Key != legacyPersonalMenuKeys[i] {
			return menu, false
		}
		byKey[item.Key] = item
	}
	ordered := make([]adminMenuItem, 0, len(canonicalPersonalMenuKeys))
	for _, key := range canonicalPersonalMenuKeys {
		item, ok := byKey[key]
		if !ok {
			return menu, false
		}
		ordered = append(ordered, item)
	}
	return ordered, true
}

func reorderLegacyOrdersFirstAdminMenu(menu []adminMenuItem) ([]adminMenuItem, bool) {
	if len(menu) != len(legacyOrdersFirstAdminMenuKeys) {
		return menu, false
	}
	byKey := make(map[string]adminMenuItem, len(menu))
	for i, item := range menu {
		if item.Key != legacyOrdersFirstAdminMenuKeys[i] {
			return menu, false
		}
		byKey[item.Key] = item
	}
	ordered := make([]adminMenuItem, 0, len(canonicalDefaultAdminMenuKeys))
	for _, key := range canonicalDefaultAdminMenuKeys {
		item, ok := byKey[key]
		if !ok {
			return menu, false
		}
		ordered = append(ordered, item)
	}
	return ordered, true
}

func migrateAdminMenu(menu []adminMenuItem) ([]adminMenuItem, bool) {
	migrated := append([]adminMenuItem(nil), menu...)
	changed := false
	tradingRoutes := map[string]string{
		"overview":       "/tvbot/view/dashboard",
		"positions":      "/tvbot/view/positions",
		"orders":         "/tvbot/view/orders",
		"analysis":       "/tvbot/view/analysis",
		"trade-monitor":  "/tvbot/view/trade-monitor",
		"symbols":        "/tvbot/view/symbols",
		"api-keys":       "/tvbot/view/api-keys",
		"order-settings": "/tvbot/view/order-settings",
		"template":       "/tvbot/view/template",
		"config":         "/tvbot/view/config",
	}
	for i := range migrated {
		if migrated[i].Key == "system" || migrated[i].Href == "/admin" || migrated[i].Title == "系统管理" {
			updated := adminMenuItem{Key: "system", Title: "用户管理", Href: "/admin", Group: "admin"}
			if migrated[i] != updated {
				migrated[i] = updated
				changed = true
			}
			continue
		}
		if migrated[i].Key == "menu" || migrated[i].Title == "菜单管理" || migrated[i].Href == "/admin#menu" {
			updated := adminMenuItem{Key: "menu", Title: "菜单管理", Href: "/menu", Group: "admin"}
			if migrated[i] != updated {
				migrated[i] = updated
				changed = true
			}
			continue
		}
		if migrated[i].Key == "orders" || migrated[i].Href == "/tvbot/view/orders" {
			if migrated[i].Title != "订单" {
				migrated[i].Title = "订单"
				changed = true
			}
		}
		if migrated[i].Key == "overview" || migrated[i].Href == "/tvbot/view/dashboard" {
			if migrated[i].Title != "资金" {
				migrated[i].Title = "资金"
				changed = true
			}
		}
		if migrated[i].Key == "upgrade" && migrated[i].Href == "/upgrade" {
			migrated[i].Href = "/tvbot/view/upgrade"
			changed = true
		}
		if href, ok := tradingRoutes[migrated[i].Key]; ok && (migrated[i].Href == "/tvbot" || migrated[i].Href == "/tvbot/" || strings.HasPrefix(migrated[i].Href, "/tvbot#")) {
			migrated[i].Href = href
			changed = true
		}
	}
	if reordered, ok := reorderLegacyDefaultAdminMenu(migrated); ok {
		return reordered, true
	}
	if reordered, ok := reorderLegacyPersonalMenu(migrated); ok {
		return reordered, true
	}
	if reordered, ok := reorderLegacyOrdersFirstAdminMenu(migrated); ok {
		return reordered, true
	}
	return migrated, changed
}

func ensureAdminMenuItems(menu []adminMenuItem) ([]adminMenuItem, bool) {
	for _, item := range menu {
		if item.Key == "system-info" || item.Href == "/system" {
			return menu, false
		}
	}
	item := adminMenuItem{Key: "system-info", Title: "系统信息", Href: "/system", Group: "admin"}
	insertAt := len(menu)
	for i, candidate := range menu {
		if candidate.Group == "admin" {
			insertAt = i
			break
		}
	}
	menu = append(menu, adminMenuItem{})
	copy(menu[insertAt+1:], menu[insertAt:])
	menu[insertAt] = item
	return menu, true
}

func (s *Server) menuUsername(r *http.Request) (string, bool) {
	if r == nil || !s.auth.Enabled() {
		return "", false
	}
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		return "", false
	}
	username, _, ok := s.auth.Identity(cookie.Value)
	return username, ok && strings.TrimSpace(username) != ""
}

func (s *Server) defaultMenu() []adminMenuItem {
	s.menuMu.RLock()
	defer s.menuMu.RUnlock()
	return append([]adminMenuItem(nil), s.menu...)
}

func (s *Server) menuForRequest(r *http.Request) []adminMenuItem {
	menu := s.defaultMenu()
	username, ok := s.menuUsername(r)
	if !ok || s.userStore == nil {
		return menu
	}
	payload, found, err := s.userStore.LoadUserMenu(r.Context(), username)
	if err != nil || !found {
		return menu
	}
	var userMenu []adminMenuItem
	if json.Unmarshal(payload, &userMenu) != nil {
		return menu
	}
	userMenu, migrated := migrateAdminMenu(userMenu)
	userMenu, normalized := ensureAdminMenuItems(userMenu)
	if validateMenu(userMenu) != nil {
		return menu
	}
	if migrated || normalized {
		if normalizedPayload, marshalErr := json.Marshal(userMenu); marshalErr == nil {
			_ = s.userStore.SaveUserMenu(r.Context(), username, normalizedPayload)
		}
	}
	return userMenu
}

func (s *Server) requireAdminRequest(w http.ResponseWriter, r *http.Request) bool {
	if !s.auth.Enabled() {
		return true
	}
	cookie, err := r.Cookie(sessionCookie)
	if err != nil || !s.auth.IsAdmin(cookie.Value) {
		problem(w, http.StatusForbidden, errors.New("administrator role required"))
		return false
	}
	return true
}

func (s *Server) adminPage(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminRequest(w, r) {
		return
	}
	/*
		const body = `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="app-base" content="{{BASE}}"><script src="{{BASE}}assets/theme.js"></script><title>系统管理 · 清算墙雷达</title><link rel="stylesheet" href="{{BASE}}assets/pages.css"><link rel="stylesheet" href="{{BASE}}assets/theme.css"><script src="{{BASE}}assets/shared.js" defer></script><style>.admin-grid{display:grid;gap:18px;grid-template-columns:repeat(auto-fit,minmax(320px,1fr))}.admin-card{border:1px solid var(--line);border-radius:14px;padding:18px;background:var(--panel)}.admin-card h2{margin-top:0}.admin-table{width:100%;border-collapse:collapse}.admin-table th,.admin-table td{padding:9px 7px;border-bottom:1px solid var(--line);text-align:left}.admin-form{display:grid;gap:10px}.admin-form input,.admin-form select{padding:9px;border:1px solid var(--line);border-radius:8px;background:var(--panel);color:var(--text)}.admin-actions{display:flex;flex-wrap:wrap;gap:8px}.admin-actions button{padding:7px 10px}.admin-note{color:var(--muted)}.admin-error{color:#c24135;min-height:1.2em}</style></head><body><main class="page-shell"><header class="page-head"><p class="eyebrow">ADMINISTRATION</p><h1>系统管理</h1><p>统一账号、菜单和升级状态。</p></header><div class="admin-grid"><section class="admin-card"><h2>用户管理</h2><p class="admin-note">密码只在提交时使用，页面和接口不会返回密码哈希。</p><form id="new-user" class="admin-form"><input name="username" placeholder="用户名" maxlength="128" required><input name="password" type="password" placeholder="初始密码" minlength="8" required><select name="role"><option value="user">普通用户</option><option value="admin">管理员</option></select><button type="submit">新增用户</button></form><p id="user-error" class="admin-error" role="alert"></p><table class="admin-table"><thead><tr><th>用户</th><th>角色</th><th>状态</th><th>操作</th></tr></thead><tbody id="users"><tr><td colspan="4">加载中</td></tr></tbody></table></section><section class="admin-card" id="menu"><h2>菜单管理</h2><p class="admin-note">默认顺序以报警页风格为主；调整后仅影响统一导航配置。</p><ol id="menu-list"></ol><div class="admin-actions"><button id="save-menu" type="button">保存当前顺序</button><button id="reload-menu" type="button">恢复读取</button></div></section><section class="admin-card"><h2>系统状态</h2><pre id="system">加载中</pre></section></div></main><script>(async()=>{const j=async(u,o)=>{const r=await fetch(u,Object.assign({credentials:'same-origin'},o||{}));const b=await r.json().catch(()=>({}));if(!r.ok)throw new Error(b.detail||b.error||('HTTP '+r.status));return b};const err=document.querySelector('#user-error');const users=document.querySelector('#users');const draw=rows=>{users.innerHTML=rows.map(u=>`<tr><td>${u.username}</td><td>${u.role}</td><td>${u.enabled?'启用':'停用'}</td><td><div class="admin-actions"><button data-toggle="${u.username}" type="button">${u.enabled?'停用':'启用'}</button><button data-role="${u.username}" type="button">切换角色</button><button data-reset="${u.username}" type="button">重置密码</button></div></td></tr>`).join('')||'<tr><td colspan="4">暂无用户</td></tr>';users.querySelectorAll('button').forEach(b=>b.onclick=async()=>{try{const username=b.dataset.toggle||b.dataset.role||b.dataset.reset;const row=rows.find(x=>x.username===username);const body={};if(b.dataset.toggle)body.disabled=row.enabled;if(b.dataset.role)body.role=row.role==='admin'?'user':'admin';if(b.dataset.reset){const password=prompt('输入新密码（至少 8 个字符）');if(!password)return;body.password=password}await j('/api/v1/admin/users/'+encodeURIComponent(username),{method:'PATCH',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});await load()}catch(e){err.textContent=e.message}})};const load=async()=>{try{const b=await j('/api/v1/admin/users');draw(b.users);err.textContent=''}catch(e){err.textContent=e.message}};document.querySelector('#new-user').onsubmit=async e=>{e.preventDefault();const f=new FormData(e.target);try{await j('/api/v1/admin/users',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(Object.fromEntries(f))});e.target.reset();await load()}catch(x){err.textContent=x.message}};const menuList=document.querySelector('#menu-list');let menu=[];const drawMenu=()=>{menuList.innerHTML=menu.map((x,i)=>`<li>${x.title} <button type="button" data-up="${i}" ${i?'':'disabled'}>上移</button><button type="button" data-down="${i}" ${i===menu.length-1?'disabled':''}>下移</button></li>`).join('');menuList.querySelectorAll('button').forEach(b=>b.onclick=()=>{const i=+b.dataset.up||+b.dataset.down;const d=b.dataset.up?-1:1;[menu[i],menu[i+d]]=[menu[i+d],menu[i]];drawMenu()})};const loadMenu=async()=>{const b=await j('/api/v1/admin/menu');menu=b.menu;drawMenu()};document.querySelector('#save-menu').onclick=async()=>{try{await j('/api/v1/admin/menu',{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify({menu})});await loadMenu()}catch(e){err.textContent=e.message}};document.querySelector('#reload-menu').onclick=loadMenu;try{const b=await j('/api/v1/admin/system');document.querySelector('#system').textContent=JSON.stringify(b,null,2)}catch(e){document.querySelector('#system').textContent=e.message}await load();await loadMenu()})()</script></body></html>`
	*/
	body := string(adminHTML)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(s.renderPage([]byte(body), "admin", "系统管理", r))
}

func (s *Server) adminUsers(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminRequest(w, r) {
		return
	}
	if s.userStore == nil {
		problem(w, http.StatusServiceUnavailable, errors.New("user store is not configured"))
		return
	}
	ctx := r.Context()
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/admin/users")
	username := strings.Trim(strings.TrimSpace(path), "/")
	if username != "" {
		if decoded, err := url.PathUnescape(username); err == nil {
			username = decoded
		}
	}
	switch r.Method {
	case http.MethodGet:
		if username != "" {
			problem(w, http.StatusNotFound, errors.New("user endpoint not found"))
			return
		}
		rows, err := s.userStore.ListAuthUsers(ctx)
		if err != nil {
			problem(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"users": userViews(rows)})
	case http.MethodPost:
		if username != "" {
			problem(w, http.StatusMethodNotAllowed, errors.New("use PATCH for an existing user"))
			return
		}
		var req struct {
			Username string `json:"username"`
			Password string `json:"password"`
			Role     string `json:"role"`
		}
		if err := decodeAdminJSON(w, r, &req); err != nil {
			return
		}
		req.Username = strings.TrimSpace(req.Username)
		req.Role = strings.TrimSpace(req.Role)
		if !validAdminUsername(req.Username) || len(req.Password) < 8 || len(req.Password) > 4096 || (req.Role != "admin" && req.Role != "user") {
			problem(w, http.StatusBadRequest, errors.New("invalid username, password or role"))
			return
		}
		hash, err := authn.HashPassword(req.Password)
		if err != nil {
			problem(w, http.StatusInternalServerError, err)
			return
		}
		if err = s.userStore.CreateAuthUser(ctx, req.Username, hash, req.Role); err != nil {
			problem(w, http.StatusConflict, errors.New("user already exists or could not be created"))
			return
		}
		_ = s.auth.ReloadUsers(ctx)
		s.audit(ctx, r, "create_user", req.Username)
		w.WriteHeader(http.StatusCreated)
	case http.MethodPatch:
		if username == "" {
			problem(w, http.StatusBadRequest, errors.New("username is required"))
			return
		}
		rows, err := s.userStore.ListAuthUsers(ctx)
		if err != nil {
			problem(w, http.StatusInternalServerError, err)
			return
		}
		current, found := findUser(rows, username)
		if !found {
			problem(w, http.StatusNotFound, errors.New("user not found"))
			return
		}
		var req struct {
			Password string `json:"password"`
			Role     string `json:"role"`
			Disabled *bool  `json:"disabled"`
		}
		if err := decodeAdminJSON(w, r, &req); err != nil {
			return
		}
		role := current.Role
		if req.Role != "" {
			role = strings.TrimSpace(req.Role)
		}
		disabled := current.Disabled
		if req.Disabled != nil {
			disabled = *req.Disabled
		}
		if role != "admin" && role != "user" || (req.Password != "" && (len(req.Password) < 8 || len(req.Password) > 4096)) {
			problem(w, http.StatusBadRequest, errors.New("invalid role or password"))
			return
		}
		if current.Role == "admin" && (!current.Disabled && (disabled || role != "admin")) && activeAdminCount(rows) <= 1 {
			problem(w, http.StatusConflict, errors.New("cannot disable or demote the last administrator"))
			return
		}
		if req.Password != "" {
			hash, err := authn.HashPassword(req.Password)
			if err != nil {
				problem(w, http.StatusInternalServerError, err)
				return
			}
			if err = s.userStore.UpsertAuthUser(ctx, username, hash); err != nil {
				problem(w, http.StatusInternalServerError, err)
				return
			}
		}
		if err = s.userStore.SetAuthUserState(ctx, username, role, disabled); err != nil {
			problem(w, http.StatusInternalServerError, err)
			return
		}
		if err = s.auth.ReloadUsers(ctx); err != nil {
			problem(w, http.StatusInternalServerError, err)
			return
		}
		s.audit(ctx, r, "update_user", username)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case http.MethodDelete:
		if username == "" {
			problem(w, http.StatusBadRequest, errors.New("username is required"))
			return
		}
		if username == s.authPrimaryUsername() {
			problem(w, http.StatusConflict, errors.New("configured primary administrator cannot be deleted"))
			return
		}
		rows, err := s.userStore.ListAuthUsers(ctx)
		if err != nil {
			problem(w, http.StatusInternalServerError, err)
			return
		}
		current, found := findUser(rows, username)
		if !found {
			problem(w, http.StatusNotFound, errors.New("user not found"))
			return
		}
		if current.Role == "admin" && !current.Disabled && activeAdminCount(rows) <= 1 {
			problem(w, http.StatusConflict, errors.New("cannot delete the last administrator"))
			return
		}
		if err = s.userStore.DeleteAuthUser(ctx, username); err != nil {
			problem(w, http.StatusInternalServerError, err)
			return
		}
		_ = s.auth.ReloadUsers(ctx)
		s.audit(ctx, r, "delete_user", username)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "GET, POST, PATCH, DELETE")
		problem(w, http.StatusMethodNotAllowed, errors.New("unsupported admin user method"))
	}
}

func (s *Server) authPrimaryUsername() string {
	return s.auth.PrimaryUsername()
}

func (s *Server) adminMenu(w http.ResponseWriter, r *http.Request) {
	if !s.requireUnifiedUser(w, r) {
		return
	}
	menu := s.menuForRequest(r)
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, map[string]any{"menu": menu})
		return
	}
	if r.Method != http.MethodPut {
		w.Header().Set("Allow", "GET, PUT")
		problem(w, http.StatusMethodNotAllowed, errors.New("only GET and PUT are allowed"))
		return
	}
	if s.userStore == nil {
		problem(w, http.StatusServiceUnavailable, errors.New("menu store is not configured"))
		return
	}
	var req struct {
		Menu []adminMenuItem `json:"menu"`
	}
	if err := decodeAdminJSON(w, r, &req); err != nil {
		return
	}
	if err := validateMenu(req.Menu); err != nil {
		problem(w, http.StatusBadRequest, err)
		return
	}
	payload, _ := json.Marshal(req.Menu)
	if username, ok := s.menuUsername(r); ok {
		if err := s.userStore.SaveUserMenu(r.Context(), username, payload); err != nil {
			problem(w, http.StatusInternalServerError, err)
			return
		}
	} else {
		if err := s.userStore.SaveAdminMenu(r.Context(), payload); err != nil {
			problem(w, http.StatusInternalServerError, err)
			return
		}
		s.menuMu.Lock()
		s.menu = append([]adminMenuItem(nil), req.Menu...)
		s.menuMu.Unlock()
	}
	s.audit(r.Context(), r, "update_menu", "personal-navigation")
	writeJSON(w, http.StatusOK, map[string]any{"menu": req.Menu})
}

func (s *Server) adminSystem(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminRequest(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "time": time.Now().UTC(), "auth_enabled": s.auth.Enabled(),
		"build":  map[string]string{"time": BuildTime, "revision": BuildRevision, "branch": BuildBranch},
		"health": s.svc.Health(),
	})
}

func (s *Server) audit(ctx context.Context, r *http.Request, action, target string) {
	if s.userStore == nil {
		return
	}
	actor := "system"
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		if username, _, ok := s.auth.Identity(cookie.Value); ok {
			actor = username
		}
	}
	_ = s.userStore.RecordAuthAudit(ctx, actor, action, target)
	if s.log != nil {
		s.log.Info("admin action", "actor", actor, "action", action, "target", target)
	}
}

func decodeAdminJSON(w http.ResponseWriter, r *http.Request, out any) error {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(out); err != nil {
		problem(w, http.StatusBadRequest, errors.New("invalid JSON body"))
		return err
	}
	return nil
}

func validAdminUsername(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if r < 0x21 || r == '/' || r == '\\' || r == ':' {
			return false
		}
	}
	return true
}

func findUser(rows []authn.UserRecord, username string) (authn.UserRecord, bool) {
	for _, row := range rows {
		if row.Username == username {
			return row, true
		}
	}
	return authn.UserRecord{}, false
}

func activeAdminCount(rows []authn.UserRecord) int {
	count := 0
	for _, row := range rows {
		if row.Role == "admin" && !row.Disabled {
			count++
		}
	}
	return count
}

func userViews(rows []authn.UserRecord) []map[string]any {
	sort.Slice(rows, func(i, j int) bool { return rows[i].Username < rows[j].Username })
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		view := map[string]any{"username": row.Username, "role": row.Role, "enabled": !row.Disabled, "created_at": row.CreatedAt, "updated_at": row.UpdatedAt}
		if row.LastLoginAt != nil {
			view["last_login_at"] = row.LastLoginAt
		}
		out = append(out, view)
	}
	return out
}

func validateMenu(menu []adminMenuItem) error {
	if len(menu) == 0 || len(menu) > 64 {
		return errors.New("menu must contain 1 to 64 items")
	}
	seen := make(map[string]struct{}, len(menu))
	for _, item := range menu {
		if item.Key == "" || item.Title == "" || item.Href == "" || strings.HasPrefix(item.Href, "//") || !strings.HasPrefix(item.Href, "/") {
			return errors.New("menu item is invalid")
		}
		if _, ok := seen[item.Key]; ok {
			return errors.New("menu item keys must be unique")
		}
		seen[item.Key] = struct{}{}
	}
	return nil
}
