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

