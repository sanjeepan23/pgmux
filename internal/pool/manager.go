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

