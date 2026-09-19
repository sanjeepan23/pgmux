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

