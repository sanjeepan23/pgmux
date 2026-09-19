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

