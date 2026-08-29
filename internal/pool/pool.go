// Package pool keeps one pool of Postgres connections for each (user, database) pair.
package pool

import (
	"container/list"
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sanjeepan23/pgmux/internal/auth"
	"github.com/sanjeepan23/pgmux/internal/config"
	"github.com/sanjeepan23/pgmux/internal/metrics"
)

// Key identifies a pool.
type Key struct {
	User     string
	Database string
}

var (
	// ErrWaitTimeout means no server connection became free within query_wait_timeout.
	ErrWaitTimeout = errors.New("query_wait_timeout: no server connection available")
	// ErrPoolClosed means the pooler is shutting down.
	ErrPoolClosed = errors.New("pool is closed")
)

// Pool is a set of server connections for one (user, database) pair.
type Pool struct {
	Key    Key
	dialer func() *Dialer

	mu       sync.Mutex
	settings config.PoolSettings
	login    Login
	idle     []*Conn // a stack, so the most recently used connection is reused first
	total    int     // open connections plus connections that are being dialed
	inUse    int     // connections that a client holds, or that a client is dialing
	waiters  list.List
	paused   bool
	closed   bool
	params   map[string]string
	dialedOK bool // a login has worked, so warm-up dials can work without stored credentials

	clients atomic.Int64
}

