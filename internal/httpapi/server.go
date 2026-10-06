package httpapi

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/app"
)

//go:embed index.html
var indexHTML []byte

type Server struct {
	http *http.Server
	svc  *app.Service
	log  *slog.Logger
}

func New(address string, svc *app.Service, log *slog.Logger) *Server {
	s := &Server{svc: svc, log: log}
	mux := http.NewServeMux()
	mux.HandleFunc("/", getOnly(s.index))
	mux.HandleFunc("/api/v1/signals/latest", getOnly(s.signal))
	mux.HandleFunc("/api/v1/map", getOnly(s.liquidationMap))
	mux.HandleFunc("/api/v1/market", getOnly(s.market))
	mux.HandleFunc("/api/v1/backtest", getOnly(s.backtest))
	mux.HandleFunc("/api/v1/stream", getOnly(s.stream))
	mux.HandleFunc("/healthz", getOnly(s.health))
	mux.HandleFunc("/readyz", getOnly(s.ready))
	mux.HandleFunc("/metrics", getOnly(s.metrics))
	s.http = &http.Server{Addr: address, Handler: securityHeaders(mux), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	return s
}
func (s *Server) ListenAndServe() error              { return s.http.ListenAndServe() }
func (s *Server) Shutdown(ctx context.Context) error { return s.http.Shutdown(ctx) }
func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(indexHTML)
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
	m, ok := s.svc.Map(sym)
	if !ok {
		problem(w, http.StatusServiceUnavailable, fmt.Errorf("map unavailable"))
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
	v, e := s.svc.Market(r.Context(), sym)
	if e != nil {
		problem(w, http.StatusServiceUnavailable, e)
		return
	}
	writeJSON(w, http.StatusOK, v)
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
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; connect-src 'self'")
		next.ServeHTTP(w, r)
	})
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
