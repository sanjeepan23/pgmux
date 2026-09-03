package pool

import (
	"context"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sanjeepan23/pgmux/internal/config"
	"github.com/sanjeepan23/pgmux/internal/metrics"
)

// Manager owns all pools.
type Manager struct {
	dialer atomic.Pointer[Dialer]
	cfg    atomic.Pointer[config.Config]

	mu    sync.Mutex
	pools map[Key]*Pool
}

// NewManager returns a Manager that dials servers with d.
func NewManager(cfg *config.Config, d *Dialer) *Manager {
	m := &Manager{pools: map[Key]*Pool{}}
	m.dialer.Store(d)
	m.cfg.Store(cfg)
	return m
}

// Dialer returns the current dialer.
func (m *Manager) Dialer() *Dialer { return m.dialer.Load() }

// Get returns the pool for key and creates it if needed. The login is used only when the pool is new.
func (m *Manager) Get(key Key, login Login) *Pool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.pools[key]; ok {
		return p
	}
	p := newPool(key, m.cfg.Load().PoolFor(key.Database), login, m.Dialer)
	m.pools[key] = p
	return p
}

// Pools returns all pools sorted by user and database.
func (m *Manager) Pools() []*Pool {
	m.mu.Lock()
	out := make([]*Pool, 0, len(m.pools))
	for _, p := range m.pools {
		out = append(out, p)
	}
	m.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Key.User != out[j].Key.User {
			return out[i].Key.User < out[j].Key.User
		}
		return out[i].Key.Database < out[j].Key.Database
	})
	return out
}

// Reload applies a new config and dialer to all pools.
func (m *Manager) Reload(cfg *config.Config, d *Dialer) {
	m.cfg.Store(cfg)
	if d != nil {
		m.dialer.Store(d)
	}
	for _, p := range m.Pools() {
		p.Resize(cfg.PoolFor(p.Key.Database))
	}
}

// Run does pool maintenance once a second until ctx ends.
func (m *Manager) Run(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			for _, p := range m.Pools() {
				p.maintain(ctx)
			}
		}
	}
}

