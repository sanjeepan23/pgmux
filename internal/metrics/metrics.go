// Package metrics defines the Prometheus metrics for pgmux.
package metrics

import (
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// Error types for the ErrorsTotal counter.
const (
	ErrAuthFailed       = "auth_failed"
	ErrQueryWaitTimeout = "query_wait_timeout"
	ErrServerConnect    = "server_connect"
	ErrServerLost       = "server_lost"
	ErrClientProtocol   = "client_protocol"
	ErrClientLimit      = "client_limit"
	ErrCancel           = "cancel"
)

// Registry holds all pgmux metrics.
var Registry = prometheus.NewRegistry()

var (
	// AcquireWait measures how long a client waited for a server connection.
	AcquireWait = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "pgmux",
		Name:      "acquire_wait_seconds",
		Help:      "Time a client waited to get a server connection.",
		Buckets:   []float64{.0001, .0005, .001, .005, .01, .05, .1, .5, 1, 5, 30},
	}, []string{"user", "database"})

	QueriesTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "pgmux",
		Name:      "queries_total",
		Help:      "Simple queries and Execute messages sent to servers.",
	}, []string{"user", "database"})

	TransactionsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "pgmux",
		Name:      "transactions_total",
		Help:      "Transactions completed, counted when a server returns to idle.",
	}, []string{"user", "database"})

	ErrorsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "pgmux",
		Name:      "errors_total",
		Help:      "Pooler errors by type.",
	}, []string{"type"})

	ClientConnectionsTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "pgmux",
		Name:      "client_connections_total",
		Help:      "Client connections accepted.",
	})

	ServerConnectionsTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "pgmux",
		Name:      "server_connections_total",
		Help:      "Server connections opened.",
	})

	SessionFeatureWarnings = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "pgmux",
		Name:      "session_feature_warnings_total",
		Help:      "Statements that use session state in transaction mode.",
	}, []string{"feature"})
)

// PoolStat is a snapshot of one pool for the pool gauges.
type PoolStat struct {
	User, Database, Mode string
	ClientsActive        int
	ClientsWaiting       int
	ServersActive        int
	ServersIdle          int
	MaxSize              int
}

type poolCollector struct {
	descs map[string]*prometheus.Desc
}

var poolSource atomic.Pointer[func() []PoolStat]

var poolGauges = []struct{ name, help string }{
	{"clients_active", "Clients connected to the pool."},
	{"clients_waiting", "Clients waiting for a server connection."},
	{"servers_active", "Server connections in use by a client."},
	{"servers_idle", "Idle server connections."},
	{"pool_max_size", "Maximum server connections for the pool."},
}

func (c *poolCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range c.descs {
		ch <- d
	}
}

func (c *poolCollector) Collect(ch chan<- prometheus.Metric) {
	src := poolSource.Load()
	if src == nil {
		return
	}
	for _, s := range (*src)() {
		vals := map[string]int{
			"clients_active":  s.ClientsActive,
			"clients_waiting": s.ClientsWaiting,
			"servers_active":  s.ServersActive,
			"servers_idle":    s.ServersIdle,
			"pool_max_size":   s.MaxSize,
		}
		for name, v := range vals {
			ch <- prometheus.MustNewConstMetric(c.descs[name], prometheus.GaugeValue, float64(v), s.User, s.Database, s.Mode)
		}
	}
}

// SetPoolSource sets the function that the pool gauges call at scrape time.
func SetPoolSource(source func() []PoolStat) { poolSource.Store(&source) }

func init() {
	pc := &poolCollector{descs: map[string]*prometheus.Desc{}}
	for _, g := range poolGauges {
		pc.descs[g.name] = prometheus.NewDesc("pgmux_"+g.name, g.help, []string{"user", "database", "mode"}, nil)
	}
	Registry.MustRegister(pc,
		AcquireWait, QueriesTotal, TransactionsTotal, ErrorsTotal,
		ClientConnectionsTotal, ServerConnectionsTotal, SessionFeatureWarnings,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
}
