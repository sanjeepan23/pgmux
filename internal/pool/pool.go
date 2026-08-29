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

func newPool(key Key, settings config.PoolSettings, login Login, dialer func() *Dialer) *Pool {
	return &Pool{Key: key, settings: settings, login: login, dialer: dialer}
}

// Settings returns the current pool settings.
func (p *Pool) Settings() config.PoolSettings {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.settings
}

// Mode returns the pool mode.
func (p *Pool) Mode() string { return p.Settings().Mode }

// ClientConnected and ClientDisconnected count the clients that use the pool.
func (p *Pool) ClientConnected()    { p.clients.Add(1) }
func (p *Pool) ClientDisconnected() { p.clients.Add(-1) }

// UpdateLogin sets new credentials for future server connections.
// SCRAM keys from a client are kept when the new login has no password.
func (p *Pool) UpdateLogin(l Login) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if l.Keys == nil && l.Password == "" {
		l.Keys = p.login.Keys
	}
	p.login = l
}

// SetClientKeys stores SCRAM keys that a client proved, if the pool has no other way to log in.
func (p *Pool) SetClientKeys(clientKey, serverKey []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.login.Password == "" && p.login.Keys == nil {
		p.login.Keys = &auth.ClientKeys{ClientKey: clientKey, ServerKey: serverKey}
	}
}

func (p *Pool) canLogin() bool {
	return p.dialedOK || p.login.Password != "" || p.login.MD5 != "" || p.login.Keys != nil
}

