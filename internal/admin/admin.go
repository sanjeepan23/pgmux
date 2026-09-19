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

