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
		{Key: "overview", Title: "总览", Href: "/tvbot", Group: "trading"},
		{Key: "positions", Title: "持仓", Href: "/tvbot#positions", Group: "trading"},
		{Key: "orders", Title: "订单", Href: "/tvbot#orders", Group: "trading"},
		{Key: "analysis", Title: "订单分析", Href: "/tvbot#analysis", Group: "trading"},
		{Key: "trade-monitor", Title: "成交监听", Href: "/tvbot#tradeMonitor", Group: "trading"},
		{Key: "symbols", Title: "币对配置", Href: "/tvbot#symbols", Group: "trading"},
		{Key: "api-keys", Title: "API Key", Href: "/tvbot#apiKeys", Group: "trading"},
		{Key: "order-settings", Title: "下单设置", Href: "/tvbot#orderSettings", Group: "trading"},
		{Key: "template", Title: "告警模板", Href: "/tvbot#template", Group: "trading"},
		{Key: "config", Title: "订单配置", Href: "/tvbot#config", Group: "trading"},
		{Key: "menu", Title: "菜单管理", Href: "/admin#menu", Group: "admin"},
		{Key: "system", Title: "系统管理", Href: "/admin", Group: "admin"},
		{Key: "upgrade", Title: "升级", Href: "/upgrade", Group: "admin"},
	}
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
	_, _ = w.Write(s.renderPage([]byte(body), "admin", "系统管理"))
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
	if !s.requireAdminRequest(w, r) {
		return
	}
	s.menuMu.RLock()
	menu := append([]adminMenuItem(nil), s.menu...)
	s.menuMu.RUnlock()
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, map[string]any{"menu": menu})
		return
	}
	if r.Method != http.MethodPut {
		w.Header().Set("Allow", "GET, PUT")
		problem(w, http.StatusMethodNotAllowed, errors.New("only GET and PUT are allowed"))
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
	s.menuMu.Lock()
	s.menu = append([]adminMenuItem(nil), req.Menu...)
	s.menuMu.Unlock()
	payload, _ := json.Marshal(req.Menu)
	if err := s.userStore.SaveAdminMenu(r.Context(), payload); err != nil {
		problem(w, http.StatusInternalServerError, err)
		return
	}
	s.audit(r.Context(), r, "update_menu", "merged-navigation")
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
