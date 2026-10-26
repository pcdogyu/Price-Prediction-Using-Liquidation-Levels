package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/authn"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/config"
	tvbotserver "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotserver"
)

func TestMigrateAdminMenuMovesLegacyMenuItem(t *testing.T) {
	legacy := []adminMenuItem{
		{Key: "menu", Title: "菜单管理", Href: "/admin#menu", Group: "admin"},
		{Key: "positions", Title: "持仓", Href: "/tvbot#positions", Group: "trading"},
		{Key: "orders", Title: "订单", Href: "/tvbot/view/orders", Group: "trading"},
		{Key: "analysis", Title: "分析", Href: "/tvbot#analysis", Group: "trading"},
		{Key: "overview", Title: "总览", Href: "/tvbot/view/dashboard", Group: "trading"},
		{Key: "custom", Title: "自定义", Href: "/custom", Group: "custom"},
	}
	migrated, changed := migrateAdminMenu(legacy)
	if !changed || len(migrated) != 5 {
		t.Fatalf("migration changed=%v menu=%#v", changed, migrated)
	}
	if migrated[0].Key != "menu" || migrated[0].Title != "菜单管理" || migrated[0].Href != "/menu" || migrated[0].Group != "admin" {
		t.Fatalf("migrated menu item=%#v", migrated[0])
	}
	wantHrefs := map[string]string{
		"positions": "/tvbot?tab=positions",
		"orders":    "/tvbot?tab=orders",
		"analysis":  "/tvbot?tab=analysis",
	}
	for _, item := range migrated {
		if want, ok := wantHrefs[item.Key]; ok && item.Href != want {
			t.Fatalf("TVBot menu item %q href=%q, want=%q", item.Key, item.Href, want)
		}
	}
	if migrated[len(migrated)-1] != legacy[5] {
		t.Fatalf("custom menu item should remain unchanged: %#v", migrated[len(migrated)-1])
	}
	for _, item := range migrated {
		if isRetiredTVBotMenuItem(item) {
			t.Fatalf("legacy TVBot view route should not remain in migrated menu: %#v", migrated)
		}
	}
}

func TestDefaultAdminMenuUsesRequestedTopOrder(t *testing.T) {
	menu := defaultAdminMenu()
	want := []string{
		"alerts", "bubbles", "liquidations", "hedge-wall", "market-info", "options",
		"orders", "positions", "analysis", "symbols", "config", "api-keys",
		"order-settings", "template", "trade-monitor", "upgrade",
		"system-info", "menu", "system",
	}
	if len(menu) != len(want) {
		t.Fatalf("default menu length=%d, want=%d", len(menu), len(want))
	}
	for i, key := range want {
		if menu[i].Key != key {
			t.Fatalf("default menu[%d]=%q, want=%q", i, menu[i].Key, key)
		}
	}
}

func TestMigrateAdminMenuReordersLegacyDefaultOnly(t *testing.T) {
	legacy := make([]adminMenuItem, 0, len(legacyDefaultAdminMenuKeys))
	for _, key := range legacyDefaultAdminMenuKeys {
		legacy = append(legacy, adminMenuItem{Key: key, Title: key, Href: "/" + key, Group: "trading"})
	}
	migrated, changed := migrateAdminMenu(legacy)
	if !changed {
		t.Fatal("legacy default menu should be migrated")
	}
	for i, key := range canonicalDefaultAdminMenuKeys {
		if migrated[i].Key != key {
			t.Fatalf("migrated menu[%d]=%q, want=%q", i, migrated[i].Key, key)
		}
	}

	custom := append([]adminMenuItem(nil), legacy...)
	custom[0], custom[1] = custom[1], custom[0]
	kept, changedCustom := migrateAdminMenu(custom)
	if kept[0].Key != custom[0].Key || kept[1].Key != custom[1].Key {
		t.Fatalf("custom menu order should remain unchanged: changed=%v menu=%#v", changedCustom, kept[:2])
	}
}

func TestMigrateAdminMenuReordersLegacyPersonalMenu(t *testing.T) {
	legacy := make([]adminMenuItem, 0, len(legacyPersonalMenuKeys))
	for _, key := range legacyPersonalMenuKeys {
		legacy = append(legacy, adminMenuItem{Key: key, Title: key, Href: "/" + key, Group: "trading"})
	}
	migrated, changed := migrateAdminMenu(legacy)
	if !changed {
		t.Fatal("legacy personal menu should be migrated")
	}
	for i, key := range canonicalPersonalMenuKeys {
		if migrated[i].Key != key {
			t.Fatalf("migrated personal menu[%d]=%q, want=%q", i, migrated[i].Key, key)
		}
	}
}

func TestMigrateAdminMenuReordersLegacyOrdersFirstAdminMenu(t *testing.T) {
	legacy := make([]adminMenuItem, 0, len(legacyOrdersFirstAdminMenuKeys))
	for _, key := range legacyOrdersFirstAdminMenuKeys {
		legacy = append(legacy, adminMenuItem{Key: key, Title: key, Href: "/" + key, Group: "trading"})
	}
	migrated, changed := migrateAdminMenu(legacy)
	if !changed {
		t.Fatal("legacy orders-first admin menu should be migrated")
	}
	for i, key := range canonicalDefaultAdminMenuKeys {
		if migrated[i].Key != key {
			t.Fatalf("migrated orders-first menu[%d]=%q, want=%q", i, migrated[i].Key, key)
		}
	}
}

func TestMigrateAdminMenuReordersCurrentMenuToRestoreSignal(t *testing.T) {
	legacy := make([]adminMenuItem, 0, len(legacyCanonicalDefaultAdminMenuKeys))
	for _, key := range legacyCanonicalDefaultAdminMenuKeys {
		legacy = append(legacy, adminMenuItem{Key: key, Title: key, Href: "/" + key, Group: "trading"})
	}
	migrated, changed := migrateAdminMenu(legacy)
	if !changed {
		t.Fatal("current menu should be migrated to restore the signal item position")
	}
	for i, key := range canonicalDefaultAdminMenuKeys {
		if migrated[i].Key != key {
			t.Fatalf("migrated menu[%d]=%q, want=%q", i, migrated[i].Key, key)
		}
	}
}

func TestMigrateAdminMenuRenamesSystemManagement(t *testing.T) {
	legacy := []adminMenuItem{{Key: "system", Title: "系统管理", Href: "/admin", Group: "admin"}}
	migrated, changed := migrateAdminMenu(legacy)
	if !changed || len(migrated) != 1 {
		t.Fatalf("migration changed=%v menu=%#v", changed, migrated)
	}
	want := adminMenuItem{Key: "system", Title: "用户管理", Href: "/admin", Group: "admin"}
	if migrated[0] != want {
		t.Fatalf("renamed system menu item=%#v, want %#v", migrated[0], want)
	}
}

func TestMigrateAdminMenuRemovesRetiredUpgradePage(t *testing.T) {
	legacy := []adminMenuItem{{Key: "upgrade", Title: "升级", Href: "/upgrade", Group: "admin"}}
	migrated, changed := migrateAdminMenu(legacy)
	if !changed || len(migrated) != 1 || migrated[0].Href != "/tvbot?tab=upgrade" {
		t.Fatalf("upgrade menu should migrate to the TVBot route: changed=%v menu=%#v", changed, migrated)
	}
}

func TestUnifiedAdminAndTVBotAuthorization(t *testing.T) {
	hash, err := authn.HashPassword("admin-password")
	if err != nil {
		t.Fatal(err)
	}
	srv, st := testService(t, config.Config{BasePath: "/liquidation/", AuthUsername: "admin", AuthPasswordHash: hash}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	defer st.Close()
	if err := st.UpsertAuthUser(nilContext(), "admin", hash); err != nil {
		t.Fatal(err)
	}
	if err := st.SetAuthUserState(nilContext(), "admin", "admin", false); err != nil {
		t.Fatal(err)
	}
	if err := srv.auth.ReloadUsers(nilContext()); err != nil {
		t.Fatal(err)
	}

	form := url.Values{"username": {"admin"}, "password": {"admin-password"}}
	login := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	srv.http.Handler.ServeHTTP(login, req)
	if login.Code != http.StatusSeeOther {
		t.Fatalf("login status=%d", login.Code)
	}
	var rootCookie, compatibilityCookie *http.Cookie
	for _, cookie := range login.Result().Cookies() {
		if cookie.Name != sessionCookie {
			continue
		}
		if cookie.Path == "/" {
			rootCookie = cookie
		} else if cookie.Path == "/liquidation/" {
			compatibilityCookie = cookie
		}
	}
	if rootCookie == nil || compatibilityCookie == nil {
		t.Fatalf("unified session cookies missing: %+v", login.Result().Cookies())
	}
	tvbot := &tvbotserver.Server{}
	srv.SetTVBot(tvbot)

	serve := func(method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if cookie != nil {
			r.AddCookie(cookie)
		}
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		srv.http.Handler.ServeHTTP(w, r)
		return w
	}
	users := serve(http.MethodGet, "/api/v1/admin/users", "", rootCookie)
	if users.Code != http.StatusOK || strings.Contains(users.Body.String(), "argon2") {
		t.Fatalf("admin user response leaked secret or failed: %d %s", users.Code, users.Body.String())
	}
	menuPage := serve(http.MethodGet, "/menu", "", rootCookie)
	if menuPage.Code != http.StatusOK || !strings.Contains(menuPage.Body.String(), "<h1>菜单管理</h1>") || strings.Contains(menuPage.Body.String(), "id=\"new-user\"") {
		t.Fatalf("menu page status=%d body=%s", menuPage.Code, menuPage.Body.String())
	}
	for _, marker := range []string{`id="save-menu"`, `is-saving`, `保存中...`, `保存成功 ✓`, `保存失败，重试`, `menu-save-spin`} {
		if !strings.Contains(menuPage.Body.String(), marker) {
			t.Fatalf("menu page must include save feedback marker %q", marker)
		}
	}
	adminPage := serve(http.MethodGet, "/admin", "", rootCookie)
	if adminPage.Code != http.StatusOK || !strings.Contains(adminPage.Body.String(), `id="system-health"`) || !strings.Contains(adminPage.Body.String(), `status-ok`) || strings.Contains(adminPage.Body.String(), `<pre id="system">`) {
		t.Fatalf("system health table missing or legacy JSON remains: status=%d body=%s", adminPage.Code, adminPage.Body.String())
	}
	systemPage := serve(http.MethodGet, "/system", "", rootCookie)
	if systemPage.Code != http.StatusOK || !strings.Contains(systemPage.Body.String(), `<section id="system"`) || !strings.Contains(systemPage.Body.String(), `id="system-config"`) || !strings.Contains(systemPage.Body.String(), `id="check-okx"`) {
		t.Fatalf("system info route status=%d body=%s", systemPage.Code, systemPage.Body.String())
	}
	created := serve(http.MethodPost, "/api/v1/admin/users", `{"username":"ordinary","password":"ordinary-password","role":"user"}`, rootCookie)
	if created.Code != http.StatusCreated {
		t.Fatalf("create user status=%d body=%s", created.Code, created.Body.String())
	}
	ordinaryToken, _, err := srv.auth.Login("127.0.0.1", "ordinary", "ordinary-password")
	if err != nil {
		t.Fatal(err)
	}
	ordinaryCookie := &http.Cookie{Name: sessionCookie, Value: ordinaryToken, Path: "/"}
	ordinarySystem := serve(http.MethodGet, "/system", "", ordinaryCookie)
	if ordinarySystem.Code != http.StatusOK {
		t.Fatalf("ordinary user should be able to view system info: status=%d body=%s", ordinarySystem.Code, ordinarySystem.Body.String())
	}
	for _, path := range []string{"/admin", "/api/v1/admin/users", "/upgrade"} {
		response := serve(http.MethodGet, path, "", ordinaryCookie)
		if response.Code != http.StatusForbidden {
			t.Fatalf("ordinary user path=%s status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
	ordinaryMenuPage := serve(http.MethodGet, "/menu", "", ordinaryCookie)
	if ordinaryMenuPage.Code != http.StatusOK || !strings.Contains(ordinaryMenuPage.Body.String(), "我的导航菜单") {
		t.Fatalf("ordinary user menu page status=%d body=%s", ordinaryMenuPage.Code, ordinaryMenuPage.Body.String())
	}
	ordinaryMenuResponse := serve(http.MethodGet, "/api/v1/admin/menu", "", ordinaryCookie)
	if ordinaryMenuResponse.Code != http.StatusOK {
		t.Fatalf("ordinary user menu read status=%d body=%s", ordinaryMenuResponse.Code, ordinaryMenuResponse.Body.String())
	}
	var ordinaryMenu struct {
		Menu []adminMenuItem `json:"menu"`
	}
	if err := json.Unmarshal(ordinaryMenuResponse.Body.Bytes(), &ordinaryMenu); err != nil || len(ordinaryMenu.Menu) < 2 {
		t.Fatalf("ordinary user menu payload=%s err=%v", ordinaryMenuResponse.Body.String(), err)
	}
	ordinaryMenu.Menu[0], ordinaryMenu.Menu[1] = ordinaryMenu.Menu[1], ordinaryMenu.Menu[0]
	personalPayload, err := json.Marshal(map[string]any{"menu": ordinaryMenu.Menu})
	if err != nil {
		t.Fatal(err)
	}
	ordinaryMenuSaved := serve(http.MethodPut, "/api/v1/admin/menu", string(personalPayload), ordinaryCookie)
	if ordinaryMenuSaved.Code != http.StatusOK {
		t.Fatalf("ordinary user menu save status=%d body=%s", ordinaryMenuSaved.Code, ordinaryMenuSaved.Body.String())
	}
	rootMenuResponse := serve(http.MethodGet, "/api/v1/admin/menu", "", rootCookie)
	var rootMenu struct {
		Menu []adminMenuItem `json:"menu"`
	}
	if rootMenuResponse.Code != http.StatusOK || json.Unmarshal(rootMenuResponse.Body.Bytes(), &rootMenu) != nil || len(rootMenu.Menu) < 2 || rootMenu.Menu[0].Key == ordinaryMenu.Menu[0].Key {
		t.Fatalf("personal menu should not change admin menu: %s", rootMenuResponse.Body.String())
	}
	lastAdmin := serve(http.MethodPatch, "/api/v1/admin/users/admin", `{"disabled":true}`, rootCookie)
	if lastAdmin.Code != http.StatusConflict {
		t.Fatalf("last admin guard status=%d body=%s", lastAdmin.Code, lastAdmin.Body.String())
	}

	var decoded map[string]any
	if err := json.Unmarshal(users.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(login.Body.String(), "admin-password") || strings.Contains(login.Body.String(), "argon2") {
		t.Fatal("login response leaked a password or hash")
	}

	positions := serve(http.MethodGet, "/tvbot", "", ordinaryCookie)
	if positions.Code != http.StatusOK || !strings.Contains(positions.Body.String(), "positions") || strings.Contains(positions.Body.String(), `class="app-logo"`) {
		t.Fatalf("unified tvbot page status=%d body prefix=%s", positions.Code, positions.Body.String()[:minInt(180, positions.Body.Len())])
	}
	tvbotPage := positions.Body.String()
	menuChecks := map[string]bool{
		"main-menu":       strings.Count(tvbotPage, `aria-label="主菜单"`) == 1,
		"menu-link":       strings.Contains(tvbotPage, `href="/menu">菜单管理`),
		"exchange-link":   strings.Contains(tvbotPage, `data-tab="system" href="/system">交易所信息`),
		"orders-link":     strings.Contains(tvbotPage, `data-tab="orders" href="/tvbot?tab=orders">信号`),
		"positions-link":  strings.Contains(tvbotPage, `data-tab="positions" href="/tvbot?tab=positions">持仓`),
		"system-dropdown": strings.Contains(tvbotPage, `data-system-menu="true"`),
	}
	for name, ok := range menuChecks {
		if !ok {
			start := strings.Index(tvbotPage, "菜单管理") - 180
			if start < 0 {
				start = 0
			}
			t.Fatalf("tvbot should render the configured single top menu, missing %s: %s", name, tvbotPage[start:minInt(start+500, len(tvbotPage))])
		}
	}
	menuStart := strings.Index(tvbotPage, `<nav class="app-nav" aria-label="主菜单" data-unified-menu="true">`)
	if menuStart < 0 {
		t.Fatal("tvbot should render the alerts page navigation layout")
	}
	personalTopMenu := tvbotPage[menuStart:]
	if strings.Index(personalTopMenu, `href="/bubbles"`) > strings.Index(personalTopMenu, `href="/alerts"`) {
		t.Fatal("tvbot should render the ordinary user's personal menu order")
	}
	if strings.Contains(tvbotPage, `/liquidation/tvbot/view/menu-settings`) {
		t.Fatal("tvbot should not expose the legacy second menu settings entry")
	}
	if strings.Contains(tvbotPage, `/liquidation/tvbot/view/`) {
		t.Fatal("tvbot should not expose retired TVBot view routes")
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func nilContext() context.Context { return context.Background() }
