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

// ServerParams returns the startup parameters of the first server connection, or nil.
func (p *Pool) ServerParams() map[string]string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.params == nil {
		return nil
	}
	return copyMap(p.params)
}

// Acquire returns a server connection. It waits up to query_wait_timeout when the pool is full.
func (p *Pool) Acquire(ctx context.Context) (*Conn, error) {
	start := time.Now()
	if t := p.Settings().QueryWaitTimeout; t > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, t)
		defer cancel()
	}
	c, err := p.acquire(ctx)
	if err == nil {
		metrics.AcquireWait.WithLabelValues(p.Key.User, p.Key.Database).Observe(time.Since(start).Seconds())
	}
	return c, err
}

func (p *Pool) acquire(ctx context.Context) (*Conn, error) {
	for {
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return nil, ErrPoolClosed
		}
		// Skip the queue only when nobody is waiting, so that clients get connections in arrival order.
		if !p.paused && p.waiters.Len() == 0 {
			if n := len(p.idle); n > 0 {
				c := p.idle[n-1]
				p.idle = p.idle[:n-1]
				p.inUse++
				p.mu.Unlock()
				if c = p.check(ctx, c); c != nil {
					return c, nil
				}
				continue
			}
			if p.total < p.settings.MaxSize {
				p.total++
				p.inUse++
				p.mu.Unlock()
				return p.dialNew(ctx)
			}
		}
		ch := make(chan *Conn, 1)
		el := p.waiters.PushBack(ch)
		p.mu.Unlock()

		select {
		case c := <-ch:
			if c == nil {
				// A nil connection is a permit to dial a new one. The dispatcher already counted it.
				return p.dialNew(ctx)
			}
			if c = p.check(ctx, c); c != nil {
				return c, nil
			}
		case <-ctx.Done():
			p.mu.Lock()
			select {
			case c := <-ch:
				// The dispatcher gave us a connection or a permit just before the timeout.
				p.mu.Unlock()
				if c == nil {
					p.mu.Lock()
					p.total--
					p.inUse--
					p.dispatchLocked()
					p.mu.Unlock()
				} else {
					p.Release(c)
				}
			default:
				p.waiters.Remove(el)
				p.mu.Unlock()
			}
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return nil, ErrWaitTimeout
			}
			return nil, ctx.Err()
		}
	}
}

// check validates an idle connection before reuse. It destroys the connection and returns nil if it is bad.
func (p *Pool) check(ctx context.Context, c *Conn) *Conn {
	s := p.Settings()
	if !c.DrainAsync() || (s.MaxLifetime > 0 && time.Since(c.createdAt) > s.MaxLifetime) {
		p.Destroy(c)
		return nil
	}
	if s.HealthCheck > 0 && time.Since(c.lastUsed) > s.HealthCheck {
		hctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		// An empty query is the cheapest round trip that the server answers.
		if err := c.Exec(hctx, ";"); err != nil {
			slog.Debug("server health check failed", "user", p.Key.User, "db", p.Key.Database, "err", err)
			p.Destroy(c)
			return nil
		}
	}
	return c
}

func (p *Pool) dialNew(ctx context.Context) (*Conn, error) {
	p.mu.Lock()
	login := p.login
	login.Database = p.settings.ServerDB
	p.mu.Unlock()
	c, err := p.dialer().Dial(ctx, login)
	if err != nil {
		p.mu.Lock()
		p.total--
		p.inUse--
		p.dispatchLocked()
		p.mu.Unlock()
		metrics.ErrorsTotal.WithLabelValues(metrics.ErrServerConnect).Inc()
		return nil, err
	}
	metrics.ServerConnectionsTotal.Inc()
	p.mu.Lock()
	p.dialedOK = true
	if p.params == nil {
		p.params = c.Params()
	}
	p.mu.Unlock()
	return c, nil
}

// Release returns a connection to the pool. The caller must leave it idle, outside any transaction.
func (p *Pool) Release(c *Conn) {
	p.mu.Lock()
	p.inUse--
	s := p.settings
	if c.Broken() || p.closed || p.total > s.MaxSize || (s.MaxLifetime > 0 && time.Since(c.createdAt) > s.MaxLifetime) {
		p.total--
		p.dispatchLocked()
		p.mu.Unlock()
		c.Close()
		return
	}
	c.lastUsed = time.Now()
	p.idle = append(p.idle, c)
	p.dispatchLocked()
	p.mu.Unlock()
}

// Destroy closes a connection that the caller holds.
func (p *Pool) Destroy(c *Conn) {
	p.mu.Lock()
	p.inUse--
	p.total--
	p.dispatchLocked()
	p.mu.Unlock()
	c.Close()
}

// dispatchLocked gives idle connections or dial permits to waiting clients.
func (p *Pool) dispatchLocked() {
	if p.paused || p.closed {
		return
	}
	for p.waiters.Len() > 0 {
		var c *Conn
		if n := len(p.idle); n > 0 {
			c = p.idle[n-1]
			p.idle = p.idle[:n-1]
		} else if p.total < p.settings.MaxSize {
			p.total++
		} else {
			return
		}
		p.inUse++
		ch := p.waiters.Remove(p.waiters.Front()).(chan *Conn)
		ch <- c
	}
}

// Resize applies new settings. Extra connections close as clients release them.
func (p *Pool) Resize(s config.PoolSettings) {
	p.mu.Lock()
	p.settings = s
	var extra []*Conn
	for p.total > s.MaxSize && len(p.idle) > 0 {
		extra = append(extra, p.idle[0])
		p.idle = p.idle[1:]
		p.total--
	}
	p.dispatchLocked()
	p.mu.Unlock()
	for _, c := range extra {
		c.Close()
	}
}

// Pause stops new acquires and waits until clients release all connections or ctx ends.
func (p *Pool) Pause(ctx context.Context) error {
	p.mu.Lock()
	p.paused = true
	p.mu.Unlock()
	t := time.NewTicker(20 * time.Millisecond)
	defer t.Stop()
	for {
		p.mu.Lock()
		n := p.inUse
		p.mu.Unlock()
		if n == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

// Resume lets clients acquire connections again.
func (p *Pool) Resume() {
	p.mu.Lock()
	p.paused = false
	p.dispatchLocked()
	p.mu.Unlock()
}

// Paused reports if the pool is paused.
func (p *Pool) Paused() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.paused
}

// maintain closes idle connections past idle_timeout or max_lifetime, and opens connections up to min_size.
func (p *Pool) maintain(ctx context.Context) {
	now := time.Now()
	p.mu.Lock()
	s := p.settings
	var expired []*Conn
	kept := p.idle[:0]
	for _, c := range p.idle {
		idleTooLong := s.IdleTimeout > 0 && now.Sub(c.lastUsed) > s.IdleTimeout && p.total > s.MinSize
		tooOld := s.MaxLifetime > 0 && now.Sub(c.createdAt) > s.MaxLifetime
		if idleTooLong || tooOld || c.Broken() {
			expired = append(expired, c)
			p.total--
			continue
		}
		kept = append(kept, c)
	}
	clear(p.idle[len(kept):])
	p.idle = kept
	need := 0
	if !p.closed && p.canLogin() {
		need = s.MinSize - p.total
	}
	for i := 0; i < need; i++ {
		p.total++
		p.inUse++
	}
	p.dispatchLocked()
	p.mu.Unlock()

	for _, c := range expired {
		c.Close()
	}
	for i := 0; i < need; i++ {
		go func() {
			c, err := p.dialNew(ctx)
			if err != nil {
				slog.Warn("min_size connection failed", "user", p.Key.User, "db", p.Key.Database, "err", err)
				return
			}
			p.Release(c)
		}()
	}
}

func (p *Pool) close() {
	p.mu.Lock()
	p.closed = true
	idle := p.idle
	p.idle = nil
	p.total -= len(idle)
	p.mu.Unlock()
	for _, c := range idle {
		c.Close()
	}
}

// Stats is a snapshot of the pool state.
type Stats struct {
	User           string `json:"user"`
	Database       string `json:"database"`
	Mode           string `json:"mode"`
	Paused         bool   `json:"paused"`
	MaxSize        int    `json:"max_size"`
	MinSize        int    `json:"min_size"`
	Clients        int    `json:"clients"`
	ClientsWaiting int    `json:"clients_waiting"`
	ServersTotal   int    `json:"servers_total"`
	ServersActive  int    `json:"servers_active"`
	ServersIdle    int    `json:"servers_idle"`
}

// Stats returns a snapshot of the pool state.
func (p *Pool) Stats() Stats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return Stats{
		User:           p.Key.User,
		Database:       p.Key.Database,
		Mode:           p.settings.Mode,
		Paused:         p.paused,
		MaxSize:        p.settings.MaxSize,
		MinSize:        p.settings.MinSize,
		Clients:        int(p.clients.Load()),
		ClientsWaiting: p.waiters.Len(),
		ServersTotal:   p.total,
		ServersActive:  p.inUse,
		ServersIdle:    len(p.idle),
	}
}
