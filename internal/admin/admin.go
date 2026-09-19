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

