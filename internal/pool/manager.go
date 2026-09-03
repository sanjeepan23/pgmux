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

