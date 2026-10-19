package httpapi

import (
	_ "embed"
	"net/http"
)

//go:embed menu.html
var menuHTML []byte

func (s *Server) menuPage(w http.ResponseWriter, r *http.Request) {
	if !s.requireUnifiedUser(w, r) {
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(s.renderPage(menuHTML, "menu", "菜单管理", r))
}
