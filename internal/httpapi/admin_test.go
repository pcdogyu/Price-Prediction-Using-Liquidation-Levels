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
		{Key: "custom", Title: "自定义", Href: "/custom", Group: "custom"},
	}
	migrated, changed := migrateAdminMenu(legacy)
	if !changed || len(migrated) != len(legacy) {
		t.Fatalf("migration changed=%v menu=%#v", changed, migrated)
	}
	if migrated[0].Key != "menu" || migrated[0].Title != "菜单管理" || migrated[0].Href != "/menu" || migrated[0].Group != "admin" {
		t.Fatalf("migrated menu item=%#v", migrated[0])
	}
	if migrated[1].Href != "/tvbot/view/positions" || migrated[2].Title != "下单信号" || migrated[2].Href != "/tvbot/view/orders" || migrated[3].Href != "/tvbot/view/analysis" {
		t.Fatalf("migrated trading menu=%#v", migrated[1:4])
	}
	if migrated[4] != legacy[4] {
		t.Fatalf("custom menu item should remain unchanged: %#v", migrated[4])
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
	if positions.Code != http.StatusOK || !strings.Contains(positions.Body.String(), "positions") || !strings.Contains(positions.Body.String(), `class="app-logo"`) {
		t.Fatalf("unified tvbot page status=%d body prefix=%s", positions.Code, positions.Body.String()[:minInt(180, positions.Body.Len())])
	}
	tvbotPage := positions.Body.String()
	if strings.Count(tvbotPage, `aria-label="主菜单"`) != 1 || !strings.Contains(tvbotPage, `href="/menu">菜单管理`) || !strings.Contains(tvbotPage, `href="/tvbot/view/orders">下单信号`) || !strings.Contains(tvbotPage, `data-tab="system" href="/system">系统信息`) {
		t.Fatalf("tvbot should render the configured single top menu: %s", tvbotPage[:minInt(1200, len(tvbotPage))])
	}
	menuStart := strings.Index(tvbotPage, `data-unified-menu="true"`)
	personalTopMenu := tvbotPage[menuStart:]
	if menuStart < 0 || strings.Index(personalTopMenu, `href="/bubbles"`) > strings.Index(personalTopMenu, `href="/alerts"`) {
		t.Fatal("tvbot should render the ordinary user's personal menu order")
	}
	if strings.Contains(tvbotPage, `/liquidation/tvbot/view/menu-settings`) {
		t.Fatal("tvbot should not expose the legacy second menu settings entry")
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func nilContext() context.Context { return context.Background() }
