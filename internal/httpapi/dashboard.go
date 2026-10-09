package httpapi

import (
	_ "embed"
	"fmt"
	"html"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/app"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/store"
)

//go:embed pages.html
var pagesHTML []byte

//go:embed pages.css
var pagesCSS []byte

//go:embed pages.js
var pagesJS []byte

//go:embed liquidation-analysis.js
var liquidationAnalysisJS []byte

//go:embed bubble-chart.js
var bubbleChartJS []byte

//go:embed shared.js
var sharedJS []byte

//go:embed theme.js
var themeJS []byte

//go:embed theme.css
var themeCSS []byte

func isDashboardPage(path string) bool {
	switch path {
	case "/", "/bubbles", "/liquidations", "/hedge-wall", "/market-info":
		return true
	}
	return false
}
func (s *Server) renderPage(body []byte, page, title string) []byte {
	base := html.EscapeString(s.auth.BasePath())
	nav := `<nav class="app-nav" aria-label="主菜单"><a class="app-brand" href="` + base + `bubbles">清算墙雷达</a><div class="app-links">`
	for _, item := range []struct{ path, title string }{{"bubbles", "气泡图"}, {"liquidations", "清算历史"}, {"hedge-wall", "对冲墙"}, {"market-info", "市场信息"}} {
		active := ""
		if item.path == page {
			active = ` aria-current="page" class="selected"`
		}
		nav += `<a href="` + base + item.path + `"` + active + `>` + item.title + `</a>`
	}
	nav += `</div><button id="nav-theme" type="button">浅色主题</button><button id="nav-logs" type="button">程序日志</button><form method="post" action="` + base + `auth/logout"><button type="submit">退出</button></form></nav><dialog id="shared-logs"><div class="dialog-head"><h2>程序日志</h2><button id="shared-logs-close" type="button">关闭</button></div><select id="shared-log-level"><option value="DEBUG">全部级别</option><option value="INFO">INFO+</option><option value="WARN">WARN+</option><option value="ERROR">ERROR</option></select><button id="shared-log-refresh" type="button">刷新</button><pre id="shared-log-content">等待加载</pre></dialog>`
	replacer := strings.NewReplacer("{{BASE}}", base, "{{NAV}}", nav, "{{PAGE}}", page, "{{TITLE}}", title)
	return []byte(replacer.Replace(string(body)))
}
func (s *Server) registerDashboard(mux *http.ServeMux) {
	mux.HandleFunc("/bubbles", getOnly(s.index))
	for path, title := range map[string]string{"/liquidations": "清算历史", "/hedge-wall": "对冲墙", "/market-info": "市场信息"} {
		p, t := path, title
		mux.HandleFunc(p, getOnly(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(s.renderPage(pagesHTML, strings.TrimPrefix(p, "/"), t))
		}))
	}
	for path, data := range map[string][]byte{"/assets/pages.css": pagesCSS, "/assets/pages.js": pagesJS, "/assets/liquidation-analysis.js": liquidationAnalysisJS, "/assets/bubble-chart.js": bubbleChartJS, "/assets/shared.js": sharedJS, "/assets/theme.js": themeJS, "/assets/theme.css": themeCSS} {
		p, b := path, data
		mux.HandleFunc(p, getOnly(func(w http.ResponseWriter, r *http.Request) {
			kind := "text/javascript"
			if strings.HasSuffix(p, ".css") {
				kind = "text/css"
			}
			w.Header().Set("Content-Type", kind+"; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			_, _ = w.Write(b)
		}))
	}
	mux.HandleFunc("/api/v1/liquidations", getOnly(s.liquidationHistory))
	mux.HandleFunc("/api/v1/hedge-wall", getOnly(s.hedgeWall))
	mux.HandleFunc("/api/v1/hedge-wall/history", getOnly(s.hedgeHistory))
	mux.HandleFunc("/api/v1/market-info", getOnly(s.marketInfo))
}
func queryLimit(r *http.Request) (int, error) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return 50, nil
	}
	n, e := strconv.Atoi(raw)
	if e != nil || n < 1 || n > 500 {
		return 0, fmt.Errorf("limit must be between 1 and 500")
	}
	return n, nil
}
func queryTimeRange(r *http.Request) (time.Time, time.Time, error) {
	from := time.UnixMilli(0).UTC()
	to := time.Now().UTC()
	q := r.URL.Query()
	for _, key := range []string{"from", "to"} {
		if v := q.Get(key); v != "" {
			t, e := time.Parse(time.RFC3339Nano, v)
			if e != nil {
				return from, to, fmt.Errorf("%s must be RFC3339", key)
			}
			if key == "from" {
				from = t
			} else {
				to = t
			}
		}
	}
	if !from.Before(to) {
		return from, to, fmt.Errorf("from must be before to")
	}
	if _, _, e := store.DecodeCursor(q.Get("cursor")); e != nil {
		return from, to, e
	}
	return from, to, nil
}
func (s *Server) liquidationHistory(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from, to, e := queryTimeRange(r)
	if e != nil {
		problem(w, 400, e)
		return
	}
	limit, e := queryLimit(r)
	if e != nil {
		problem(w, 400, e)
		return
	}
	f := domain.LiquidationFilter{Symbol: strings.ToUpper(strings.TrimSpace(q.Get("symbol"))), Side: q.Get("side"), Field: q.Get("field"), Cursor: q.Get("cursor"), From: from, To: to, Limit: limit}
	if f.Symbol == "" {
		f.Symbol = "ALL"
	}
	if f.Side != "" && f.Side != "all" && f.Side != "long" && f.Side != "short" {
		problem(w, 400, fmt.Errorf("side must be all, long, or short"))
		return
	}
	if f.Field != "" && f.Field != "notional_usd" && f.Field != "quantity" {
		problem(w, 400, fmt.Errorf("field must be notional_usd or quantity"))
		return
	}
	if value := q.Get("minimum"); value != "" {
		f.Minimum, e = strconv.ParseFloat(value, 64)
		if e != nil || math.IsNaN(f.Minimum) || math.IsInf(f.Minimum, 0) || f.Minimum < 0 {
			problem(w, 400, fmt.Errorf("minimum must be a nonnegative finite number"))
			return
		}
	}
	page, e := s.svc.LiquidationHistory(r.Context(), f)
	if e != nil {
		problem(w, 503, e)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"data": page, "sources": s.svc.Health()})
}
func (s *Server) hedgeWall(w http.ResponseWriter, r *http.Request) {
	sym, e := symbol(r)
	if e != nil {
		problem(w, 400, e)
		return
	}
	half, window := 120, 5
	if raw := r.URL.Query().Get("half_life"); raw != "" {
		half, e = strconv.Atoi(raw)
		if e != nil || (half != 60 && half != 120 && half != 180) {
			problem(w, 400, fmt.Errorf("half_life must be 60,120,180"))
			return
		}
	}
	if raw := r.URL.Query().Get("window"); raw != "" {
		window, e = strconv.Atoi(raw)
		if e != nil || (window != 3 && window != 5 && window != 10) {
			problem(w, 400, fmt.Errorf("window must be 3,5,10"))
			return
		}
	}
	v, e := s.svc.WallView(r.Context(), sym, half, window)
	if e != nil {
		problem(w, 503, e)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, v)
}
func (s *Server) hedgeHistory(w http.ResponseWriter, r *http.Request) {
	sym, e := symbol(r)
	if e != nil {
		problem(w, 400, e)
		return
	}
	kind := r.URL.Query().Get("kind")
	if kind == "" {
		kind = "events"
	}
	if !store.ValidateHistoryKind(kind) {
		problem(w, 400, fmt.Errorf("kind must be events or snapshots"))
		return
	}
	kind = strings.ToLower(kind)
	from, to, e := queryTimeRange(r)
	if e != nil {
		problem(w, 400, e)
		return
	}
	limit, e := queryLimit(r)
	if e != nil {
		problem(w, 400, e)
		return
	}
	v, e := s.svc.WallHistory(r.Context(), sym, kind, from, to, limit, r.URL.Query().Get("cursor"))
	if e != nil {
		problem(w, 503, e)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, v)
}
func (s *Server) marketInfo(w http.ResponseWriter, r *http.Request) {
	sym, e := symbol(r)
	if e != nil {
		problem(w, 400, e)
		return
	}
	period := r.URL.Query().Get("range")
	if period == "" {
		period = "1h"
	}
	if _, ok := app.MarketRanges[period]; !ok {
		problem(w, 400, fmt.Errorf("unsupported range"))
		return
	}
	v, e := s.svc.MarketInfo(r.Context(), sym, period)
	if e != nil {
		problem(w, 503, e)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, v)
}
