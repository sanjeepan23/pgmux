// Package admin serves the HTTP admin API, Prometheus metrics and the live stats stream.
package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/sanjeepan23/pgmux/internal/metrics"
	"github.com/sanjeepan23/pgmux/internal/pool"
)

// Server is the admin HTTP API.
type Server struct {
	Manager      *pool.Manager
	Clients      func() int
	Reload       func() error
	ReadOnly     bool
	DashboardDir string
	Version      string

	started time.Time
}

// Stats is the response of GET /stats.
type Stats struct {
	Version        string  `json:"version"`
	UptimeSeconds  float64 `json:"uptime_seconds"`
	Clients        int     `json:"clients"`
	ClientsWaiting int     `json:"clients_waiting"`
	ServersActive  int     `json:"servers_active"`
	ServersIdle    int     `json:"servers_idle"`
	Pools          int     `json:"pools"`
	QueriesTotal   float64 `json:"queries_total"`
	TxTotal        float64 `json:"transactions_total"`
	ErrorsTotal    float64 `json:"errors_total"`
}

// Handler returns the HTTP handler for the admin API.
func (a *Server) Handler() http.Handler {
	a.started = time.Now()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /stats", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, a.stats()) })
	mux.HandleFunc("GET /pools", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, a.pools()) })
	mux.HandleFunc("GET /events", a.events)
	mux.HandleFunc("POST /pause", a.write(a.pause))
	mux.HandleFunc("POST /resume", a.write(a.resume))
	mux.HandleFunc("POST /reload", a.write(a.reload))
	mux.Handle("GET /metrics", promhttp.HandlerFor(metrics.Registry, promhttp.HandlerOpts{}))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	if !a.ReadOnly {
		mux.HandleFunc("GET /debug/pprof/", pprof.Index)
		mux.HandleFunc("GET /debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("GET /debug/pprof/trace", pprof.Trace)
	}
	if a.DashboardDir != "" {
		mux.Handle("GET /", http.FileServer(http.Dir(a.DashboardDir)))
	}
	return cors(mux)
}

// cors lets a dashboard on another origin, such as the Vite dev server, read the API.
func cors(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Access-Control-Allow-Origin", "*")
		}
		h.ServeHTTP(w, r)
	})
}

func (a *Server) write(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a.ReadOnly {
			http.Error(w, "admin API is read-only", http.StatusForbidden)
			return
		}
		h(w, r)
	}
}

func (a *Server) stats() Stats {
	s := Stats{Version: a.Version, UptimeSeconds: time.Since(a.started).Seconds(), Clients: a.Clients()}
	for _, p := range a.Manager.Pools() {
		ps := p.Stats()
		s.Pools++
		s.ClientsWaiting += ps.ClientsWaiting
		s.ServersActive += ps.ServersActive
		s.ServersIdle += ps.ServersIdle
	}
	s.QueriesTotal = sumCounter("pgmux_queries_total")
	s.TxTotal = sumCounter("pgmux_transactions_total")
	s.ErrorsTotal = sumCounter("pgmux_errors_total")
	return s
}

func sumCounter(name string) float64 {
	families, err := metrics.Registry.Gather()
	if err != nil {
		return 0
	}
	var total float64
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
		for _, m := range f.GetMetric() {
			total += m.GetCounter().GetValue()
		}
	}
	return total
}

func (a *Server) pools() []pool.Stats {
	pools := a.Manager.Pools()
	out := make([]pool.Stats, 0, len(pools))
	for _, p := range pools {
		out = append(out, p.Stats())
	}
	return out
}

// matching returns the pools that match the optional user and db query parameters.
func (a *Server) matching(r *http.Request) []*pool.Pool {
	user, db := r.URL.Query().Get("user"), r.URL.Query().Get("db")
	var out []*pool.Pool
	for _, p := range a.Manager.Pools() {
		if (user == "" || p.Key.User == user) && (db == "" || p.Key.Database == db) {
			out = append(out, p)
		}
	}
	return out
}

func (a *Server) pause(w http.ResponseWriter, r *http.Request) {
	timeout := 30 * time.Second
	if t := r.URL.Query().Get("timeout"); t != "" {
		d, err := time.ParseDuration(t)
		if err != nil {
			http.Error(w, "bad timeout: "+err.Error(), http.StatusBadRequest)
			return
		}
		timeout = d
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	pools := a.matching(r)
	for _, p := range pools {
		_ = p.Pause(ctx)
	}
	drained := ctx.Err() == nil
	writeJSON(w, map[string]any{"paused": len(pools), "drained": drained})
}

func (a *Server) resume(w http.ResponseWriter, r *http.Request) {
	pools := a.matching(r)
	for _, p := range pools {
		p.Resume()
	}
	writeJSON(w, map[string]any{"resumed": len(pools)})
}

func (a *Server) reload(w http.ResponseWriter, r *http.Request) {
	if err := a.Reload(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"reloaded": true})
}

// events streams stats and pools once a second as Server-Sent Events.
func (a *Server) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		b, _ := json.Marshal(map[string]any{"time": time.Now().UnixMilli(), "stats": a.stats(), "pools": a.pools()})
		if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
			return
		}
		flusher.Flush()
		select {
		case <-r.Context().Done():
			return
		case <-t.C:
		}
	}
}

