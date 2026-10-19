package httpapi

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/app"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/authn"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/coinglass"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/config"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/observability"
	tvbotserver "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotserver"
)

//go:embed index.html
var indexHTML []byte

//go:embed login.html
var loginHTML []byte

//go:embed dashboard.js
var dashboardJS []byte

const sessionCookie = "liquidation_session"

type Server struct {
	http      *http.Server
	svc       *app.Service
	log       *slog.Logger
	auth      *authn.Manager
	logs      *observability.Store
	cg        *coinglass.Capturer
	tvbot     http.Handler
	userStore adminUserStore
	menuMu    sync.RWMutex
	menu      []adminMenuItem

	cgScheduleMu sync.RWMutex
	cgInterval   time.Duration
	cgNext       time.Time
}

func New(cfg config.Config, svc *app.Service, log *slog.Logger, logs *observability.Store, sessionStores ...authn.SessionStore) (*Server, error) {
	auth, err := authn.New(cfg.AuthUsername, cfg.AuthPasswordHash, cfg.BasePath, sessionStores...)
	if err != nil {
		return nil, err
	}
	s := &Server{svc: svc, log: log, auth: auth, logs: logs, cg: coinglass.New(cfg.CoinGlassDebugURL, cfg.CoinGlassCaptureDir), menu: defaultAdminMenu()}
	for _, candidate := range sessionStores {
		if users, ok := candidate.(adminUserStore); ok {
			s.userStore = users
			if payload, found, loadErr := users.LoadAdminMenu(context.Background()); loadErr == nil && found {
				var menu []adminMenuItem
				if json.Unmarshal(payload, &menu) == nil {
					menu, migrated := migrateAdminMenu(menu)
					menu, normalized := ensureAdminMenuItems(menu)
					migrated = migrated || normalized
					if validateMenu(menu) == nil {
						s.menu = menu
						if migrated {
							if normalized, marshalErr := json.Marshal(menu); marshalErr == nil {
								_ = users.SaveAdminMenu(context.Background(), normalized)
							}
						}
					}
				}
			}
			break
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", getOnly(s.index))
	s.registerDashboard(mux)
	s.registerAlerts(mux)
	mux.HandleFunc("/menu", getOnly(s.menuPage))
	mux.HandleFunc("/assets/dashboard.js", getOnly(s.dashboardScript))
	mux.HandleFunc("/auth/login", postOnly(s.login))
	mux.HandleFunc("/auth/logout", postOnly(s.logout))
	mux.Handle("/tvbot", http.HandlerFunc(s.tvbotRoute))
	mux.Handle("/tvbot/", http.HandlerFunc(s.tvbotRoute))
	mux.Handle("/tvorder", http.HandlerFunc(s.tvbotRoute))
	mux.Handle("/upgrade", http.HandlerFunc(s.tvbotRoute))
	mux.HandleFunc("/api/v1/auth/check", getOnly(s.authCheck))
	mux.HandleFunc("/admin", getOnly(s.adminPage))
	mux.Handle("/system", http.HandlerFunc(s.systemPage))
	mux.HandleFunc("/api/v1/admin/users", s.adminUsers)
	mux.HandleFunc("/api/v1/admin/users/", s.adminUsers)
	mux.HandleFunc("/api/v1/admin/menu", s.adminMenu)
	mux.HandleFunc("/api/v1/admin/system", getOnly(s.adminSystem))
	mux.HandleFunc("/api/v1/coinglass/capture", postOnly(s.coinGlassCapture))
	mux.HandleFunc("/api/v1/coinglass/latest", getOnly(s.coinGlassLatest))
	mux.HandleFunc("/api/v1/coinglass/schedule", getOnly(s.coinGlassSchedule))
	mux.HandleFunc("/api/v1/signals/latest", getOnly(s.signal))
	mux.HandleFunc("/api/v1/map", getOnly(s.liquidationMap))
	mux.HandleFunc("/api/v1/market", getOnly(s.market))
	mux.HandleFunc("/api/v1/volume-profile", getOnly(s.volumeProfile))
	mux.HandleFunc("/api/v1/backtest", getOnly(s.backtest))
	mux.HandleFunc("/api/v1/logs", getOnly(s.applicationLogs))
	mux.HandleFunc("/api/v1/stream", getOnly(s.stream))
	mux.HandleFunc("/healthz", getOnly(s.health))
	mux.HandleFunc("/readyz", getOnly(s.ready))
	mux.HandleFunc("/metrics", getOnly(s.metrics))
	s.http = &http.Server{Addr: cfg.Address, Handler: securityHeaders(s.authorize(mux)), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	return s, nil
}

// SetTVBot attaches the merged TradingView dashboard after the core server has
// been constructed. The route is registered during New so authentication and
// security headers cover it exactly like the liquidation pages.
func (s *Server) SetTVBot(handler http.Handler) {
	s.tvbot = handler
	if setter, ok := handler.(interface {
		SetUnifiedAuth(func(http.ResponseWriter, *http.Request) bool, func(http.ResponseWriter, *http.Request) bool, func(*http.Request) (string, bool), func(http.ResponseWriter, *http.Request), func(http.ResponseWriter, *http.Request))
	}); ok {
		setter.SetUnifiedAuth(s.requireUnifiedUser, s.requireUnifiedAdmin, s.unifiedUser, s.redirectUnifiedLogin, s.logout)
	}
	if setter, ok := handler.(interface {
		SetTopMenuProvider(func(*http.Request) []tvbotserver.TopMenuItem)
	}); ok {
		setter.SetTopMenuProvider(func(r *http.Request) []tvbotserver.TopMenuItem {
			menu := s.menuForRequest(r)
			items := make([]tvbotserver.TopMenuItem, 0, len(menu))
			for _, item := range menu {
				items = append(items, tvbotserver.TopMenuItem{Key: item.Key, Title: item.Title, Href: item.Href})
			}
			return items
		})
	}
}

func (s *Server) requireUnifiedUser(w http.ResponseWriter, r *http.Request) bool {
	if !s.auth.Enabled() {
		return true
	}
	if _, ok := s.unifiedUser(r); ok {
		return true
	}
	if wantsHTML(r) {
		s.redirectUnifiedLogin(w, r)
	} else {
		problem(w, http.StatusUnauthorized, errors.New("authentication required"))
	}
	return false
}

func (s *Server) unifiedUser(r *http.Request) (string, bool) {
	if !s.auth.Enabled() {
		return "", false
	}
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		return "", false
	}
	username, _, ok := s.auth.Identity(cookie.Value)
	return username, ok
}

func wantsHTML(r *http.Request) bool {
	return r.Method == http.MethodGet && (strings.Contains(r.Header.Get("Accept"), "text/html") || r.Header.Get("Sec-Fetch-Dest") == "document" || r.URL.Path == "/tvbot" || r.URL.Path == "/tvbot/")
}

func (s *Server) requireUnifiedAdmin(w http.ResponseWriter, r *http.Request) bool {
	if !s.requireUnifiedUser(w, r) {
		return false
	}
	cookie, err := r.Cookie(sessionCookie)
	if err == nil && s.auth.IsAdmin(cookie.Value) {
		return true
	}
	problem(w, http.StatusForbidden, errors.New("administrator role required"))
	return false
}

func (s *Server) redirectUnifiedLogin(w http.ResponseWriter, r *http.Request) {
	next := r.URL.RequestURI()
	if r.URL.Path == "/tvbot/login" || r.URL.Path == "/tvbot/login/" {
		next = s.auth.BasePath() + "alerts"
	}
	if next == "" || strings.HasPrefix(next, "//") || !strings.HasPrefix(next, "/") {
		next = s.auth.BasePath() + "alerts"
	}
	http.Redirect(w, r, s.auth.BasePath()+"?next="+url.QueryEscape(next), http.StatusFound)
}

func (s *Server) tvbotRoute(w http.ResponseWriter, r *http.Request) {
	if s.tvbot == nil {
		problem(w, http.StatusServiceUnavailable, errors.New("trading dashboard is not configured"))
		return
	}
	s.tvbot.ServeHTTP(w, r)
}

func (s *Server) systemPage(w http.ResponseWriter, r *http.Request) {
	if s.tvbot == nil {
		problem(w, http.StatusServiceUnavailable, errors.New("trading dashboard is not configured"))
		return
	}
	request := r.Clone(r.Context())
	request.URL.Path = "/tvbot/view/system"
	request.URL.RawPath = ""
	request.RequestURI = request.URL.RequestURI()
	s.tvbot.ServeHTTP(w, request)
}
func (s *Server) ListenAndServe() error              { return s.http.ListenAndServe() }
func (s *Server) Shutdown(ctx context.Context) error { return s.http.Shutdown(ctx) }
func (s *Server) StartCoinGlassScheduler(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		return
	}
	startedAt := time.Now().UTC()
	s.setCoinGlassSchedule(interval, startedAt.Add(interval))
	s.log.Info("CoinGlass capture scheduler started", "interval", interval, "first_run", startedAt)
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		run := func() {
			result, err := s.cg.Capture(ctx)
			if err != nil {
				s.log.Warn("scheduled CoinGlass capture failed", "error", err)
				return
			}
			s.log.Info("scheduled CoinGlass capture saved", "file", result.File, "responses", len(result.Responses), "parsed", len(result.Parsed))
		}
		run()
		for {
			select {
			case <-ctx.Done():
				return
			case tickedAt := <-ticker.C:
				s.setCoinGlassSchedule(interval, tickedAt.UTC().Add(interval))
				run()
			}
		}
	}()
}

func (s *Server) setCoinGlassSchedule(interval time.Duration, next time.Time) {
	s.cgScheduleMu.Lock()
	s.cgInterval = interval
	s.cgNext = next
	s.cgScheduleMu.Unlock()
}

func (s *Server) coinGlassSchedule(w http.ResponseWriter, _ *http.Request) {
	s.cgScheduleMu.RLock()
	interval, next := s.cgInterval, s.cgNext
	s.cgScheduleMu.RUnlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"interval_seconds": int64(interval / time.Second),
		"next_capture_at":  next,
		"server_time":      time.Now().UTC(),
	})
}
func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" {
		http.Redirect(w, r, s.auth.BasePath()+"alerts", http.StatusFound)
		return
	}
	if r.URL.Path != "/bubbles" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(s.renderPage(indexHTML, "bubbles", "气泡图", r))
}
func (s *Server) dashboardScript(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(dashboardJS)
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil {
		s.renderLogin(w, http.StatusBadRequest, "用户名或密码错误")
		return
	}
	ip := clientIP(r)
	token, expires, err := s.auth.Login(ip, r.FormValue("username"), r.FormValue("password"))
	if err != nil {
		locked := errors.Is(err, authn.ErrLocked)
		s.log.Warn("login failed", "client_ip", ip, "locked", locked)
		status := http.StatusUnauthorized
		if locked {
			status = http.StatusTooManyRequests
			w.Header().Set("Retry-After", "900")
		}
		s.renderLogin(w, status, "用户名或密码错误")
		return
	}
	s.setSessionCookies(w, token, expires)
	s.log.Info("login succeeded", "client_ip", ip)
	next := sanitizeLoginNext(r.FormValue("next"))
	if next == "" {
		next = s.auth.BasePath() + "alerts"
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
}
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		s.auth.Logout(cookie.Value)
	}
	s.clearSessionCookies(w)
	s.log.Info("logout", "client_ip", clientIP(r))
	http.Redirect(w, r, s.auth.BasePath(), http.StatusSeeOther)
}
func (s *Server) authCheck(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) coinGlassCapture(w http.ResponseWriter, r *http.Request) {
	result, err := s.cg.Capture(r.Context())
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, coinglass.ErrBusy) {
			status = http.StatusConflict
		} else if errors.Is(err, context.DeadlineExceeded) {
			status = http.StatusGatewayTimeout
		}
		s.log.Warn("CoinGlass capture failed", "error", err)
		problem(w, status, err)
		return
	}
	s.log.Info("CoinGlass capture saved", "file", result.File, "responses", len(result.Responses))
	responses := make([]map[string]any, 0, len(result.Responses))
	for _, response := range result.Responses {
		responses = append(responses, map[string]any{"url": response.URL, "status": response.Status, "bytes": response.Bytes})
	}
	parsed := make([]map[string]any, 0, len(result.Parsed))
	for _, item := range result.Parsed {
		parsed = append(parsed, map[string]any{"symbol": item.Symbol, "scope": item.Scope, "code": item.Code, "success": item.Success, "bytes": item.Bytes})
	}
	writeJSON(w, http.StatusOK, map[string]any{"captured_at": result.CapturedAt, "page_url": result.PageURL, "file": result.File, "responses": responses, "parsed": parsed})
}
func (s *Server) coinGlassLatest(w http.ResponseWriter, _ *http.Request) {
	result, err := s.cg.Latest()
	if err != nil {
		problem(w, http.StatusNotFound, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, result)
}
func (s *Server) renderLogin(w http.ResponseWriter, status int, message string) {
	html := strings.ReplaceAll(string(loginHTML), "{{LOGIN_ACTION}}", s.auth.BasePath()+"auth/login")
	html = strings.ReplaceAll(html, "{{ERROR}}", message)
	html = strings.ReplaceAll(html, "{{NEXT}}", "")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(html))
}

func (s *Server) renderLoginForRequest(w http.ResponseWriter, r *http.Request, status int, message string) {
	html := strings.ReplaceAll(string(loginHTML), "{{LOGIN_ACTION}}", s.auth.BasePath()+"auth/login")
	html = strings.ReplaceAll(html, "{{ERROR}}", message)
	next := ""
	if r.URL.Path == "/system" || r.URL.Path == "/tvbot" || strings.HasPrefix(r.URL.Path, "/tvbot/") || r.URL.Path == "/upgrade" {
		next = htmlEscapeAttribute(sanitizeLoginNext(r.URL.RequestURI()))
	}
	html = strings.ReplaceAll(html, "{{NEXT}}", next)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(html))
}

func htmlEscapeAttribute(value string) string {
	return strings.NewReplacer("&", "&amp;", "\"", "&#34;", "<", "&lt;", ">", "&gt;").Replace(value)
}

func sanitizeLoginNext(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") {
		return ""
	}
	return raw
}

func (s *Server) setSessionCookies(w http.ResponseWriter, token string, expires time.Time) {
	cookie := &http.Cookie{Name: sessionCookie, Value: token, Path: "/", Expires: expires, MaxAge: int(time.Until(expires).Seconds()), HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode}
	http.SetCookie(w, cookie)
	// Keep a second scoped cookie for old clients and the existing liquidation
	// contract. Both values identify the same server-side session; the root
	// cookie is what enables the /tvbot compatibility namespace.
	if s.auth.BasePath() != "/" {
		compat := *cookie
		compat.Path = s.auth.BasePath()
		http.SetCookie(w, &compat)
	}
}

func (s *Server) clearSessionCookies(w http.ResponseWriter) {
	for _, path := range []string{"/", s.auth.BasePath()} {
		http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: path, MaxAge: -1, Expires: time.Unix(1, 0), HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
	}
}
func symbol(r *http.Request) (string, error) {
	v := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("symbol")))
	if v != "BTCUSDT" && v != "ETHUSDT" {
		return "", fmt.Errorf("symbol must be BTCUSDT or ETHUSDT")
	}
	return v, nil
}
func (s *Server) signal(w http.ResponseWriter, r *http.Request) {
	sym, e := symbol(r)
	if e != nil {
		problem(w, http.StatusBadRequest, e)
		return
	}
	p, e := s.svc.Latest(r.Context(), sym)
	if e != nil {
		problem(w, http.StatusServiceUnavailable, e)
		return
	}
	writeJSON(w, http.StatusOK, p)
}
func (s *Server) liquidationMap(w http.ResponseWriter, r *http.Request) {
	sym, e := symbol(r)
	if e != nil {
		problem(w, http.StatusBadRequest, e)
		return
	}
	m, err := s.cg.LiquidationMap(sym, 2*time.Hour)
	if err != nil {
		s.log.Warn("CoinGlass liquidation map unavailable", "symbol", sym, "error", err)
		problem(w, http.StatusServiceUnavailable, fmt.Errorf("CoinGlass liquidation map unavailable: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, m)
}
func (s *Server) market(w http.ResponseWriter, r *http.Request) {
	sym, e := symbol(r)
	if e != nil {
		problem(w, http.StatusBadRequest, e)
		return
	}
	interval := strings.TrimSpace(r.URL.Query().Get("interval"))
	if interval == "" {
		interval = "15m"
	}
	allowed := map[string]bool{"1m": true, "2m": true, "3m": true, "5m": true, "10m": true, "15m": true, "30m": true, "1h": true, "4h": true, "8h": true, "12h": true, "24h": true}
	if !allowed[interval] {
		problem(w, http.StatusBadRequest, errors.New("interval must be one of 1m,2m,3m,5m,10m,15m,30m,1h,4h,8h,12h,24h"))
		return
	}
	limit := 120
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		limit, e = strconv.Atoi(raw)
		if e != nil || limit < 1 || limit > 500 {
			problem(w, http.StatusBadRequest, errors.New("limit must be between 1 and 500"))
			return
		}
	}
	var before time.Time
	if raw := strings.TrimSpace(r.URL.Query().Get("before")); raw != "" {
		before, e = time.Parse(time.RFC3339Nano, raw)
		if e != nil {
			problem(w, http.StatusBadRequest, errors.New("before must be RFC3339Nano"))
			return
		}
	}
	v, e := s.svc.Market(r.Context(), sym, interval, before, limit)
	if e != nil {
		problem(w, http.StatusServiceUnavailable, e)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) volumeProfile(w http.ResponseWriter, r *http.Request) {
	sym, err := symbol(r)
	if err != nil {
		problem(w, http.StatusBadRequest, err)
		return
	}
	profile, err := s.svc.VolumeProfile(r.Context(), sym)
	if err != nil {
		problem(w, http.StatusServiceUnavailable, err)
		return
	}
	writeJSON(w, http.StatusOK, profile)
}
func (s *Server) backtest(w http.ResponseWriter, r *http.Request) {
	sym, e := symbol(r)
	if e != nil {
		problem(w, http.StatusBadRequest, e)
		return
	}
	v, e := s.svc.Backtest(r.Context(), sym)
	if e != nil {
		problem(w, http.StatusServiceUnavailable, e)
		return
	}
	writeJSON(w, http.StatusOK, v)
}
func (s *Server) applicationLogs(w http.ResponseWriter, r *http.Request) {
	if s.logs == nil {
		problem(w, http.StatusServiceUnavailable, errors.New("application log file is not configured"))
		return
	}
	limit := 200
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 500 {
			problem(w, http.StatusBadRequest, errors.New("limit must be between 1 and 500"))
			return
		}
		limit = value
	}
	var before time.Time
	if raw := strings.TrimSpace(r.URL.Query().Get("before")); raw != "" {
		value, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			problem(w, http.StatusBadRequest, errors.New("before must be RFC3339Nano"))
			return
		}
		before = value
	}
	level := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("level")))
	if level != "" && level != "DEBUG" && level != "INFO" && level != "WARN" && level != "ERROR" {
		problem(w, http.StatusBadRequest, errors.New("level must be DEBUG, INFO, WARN, or ERROR"))
		return
	}
	page, err := s.logs.Query(limit, before, level)
	if err != nil {
		problem(w, http.StatusServiceUnavailable, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}
func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "alive", "time": time.Now().UTC(), "sources": s.svc.Health()})
}
func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	if !s.svc.Ready(r.Context()) {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not_ready"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}
func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	for name, h := range s.svc.Health() {
		connected := 0
		if h.Connected {
			connected = 1
		}
		fmt.Fprintf(w, "liquidation_source_connected{source=%q} %d\nliquidation_source_coverage{source=%q} %.4f\nliquidation_source_last_message_seconds{source=%q} %.3f\n", name, connected, name, h.Coverage, name, time.Since(h.LastMessage).Seconds())
	}
}
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		problem(w, 500, fmt.Errorf("stream unsupported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	writeEvent := func(name string, value any) bool {
		b, err := json.Marshal(value)
		if err != nil {
			s.log.Error("encode SSE event", "event", name, "error", err)
			return false
		}
		if _, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, b); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	writeHealth := func() bool {
		return writeEvent("health", map[string]any{
			"status":  "alive",
			"time":    time.Now().UTC(),
			"sources": s.svc.Health(),
		})
	}
	// Write immediately so browsers and reverse proxies establish the stream
	// without waiting for the next five-minute prediction update.
	if !writeHealth() {
		return
	}
	ch, cancel := s.svc.Subscribe()
	defer cancel()
	liquidations, cancelLiquidations := s.svc.SubscribeLiquidations()
	defer cancelLiquidations()
	prices, cancelPrices := s.svc.SubscribePrices()
	defer cancelPrices()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case p, open := <-ch:
			if !open || !writeEvent("prediction", p) {
				return
			}
		case event, open := <-liquidations:
			if !open || !writeEvent("liquidation", event) {
				return
			}
		case tick, open := <-prices:
			if !open || !writeEvent("price", tick) {
				return
			}
		case <-ping.C:
			if !writeHealth() {
				return
			}
		}
	}
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func problem(w http.ResponseWriter, status int, e error) {
	writeJSON(w, status, map[string]any{"error": http.StatusText(status), "detail": e.Error()})
}
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; connect-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) authorize(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.auth.Enabled() || (r.URL.Path == "/auth/login" && r.Method == http.MethodPost) || r.URL.Path == "/tvorder" || r.URL.Path == "/tvbot/login" || localProbe(r) {
			next.ServeHTTP(w, r)
			return
		}
		cookie, err := r.Cookie(sessionCookie)
		if err == nil && s.auth.Authenticated(cookie.Value) {
			next.ServeHTTP(w, r)
			return
		}
		if isDashboardPage(r.URL.Path) && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
			s.renderLoginForRequest(w, r, http.StatusOK, "")
			return
		}
		problem(w, http.StatusUnauthorized, errors.New("authentication required"))
	})
}

func localProbe(r *http.Request) bool {
	if r.URL.Path != "/healthz" && r.URL.Path != "/readyz" {
		return false
	}
	if r.Header.Get("X-Forwarded-Prefix") != "" || r.Header.Get("X-Real-IP") != "" {
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	return err == nil && net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()
}

func clientIP(r *http.Request) string {
	if ip := strings.TrimSpace(r.Header.Get("X-Real-IP")); net.ParseIP(ip) != nil {
		return ip
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && net.ParseIP(host) != nil {
		return host
	}
	return "unknown"
}

func getOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", http.MethodGet)
			problem(w, http.StatusMethodNotAllowed, fmt.Errorf("method must be GET"))
			return
		}
		next(w, r)
	}
}

func postOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			problem(w, http.StatusMethodNotAllowed, fmt.Errorf("method must be POST"))
			return
		}
		next(w, r)
	}
}
