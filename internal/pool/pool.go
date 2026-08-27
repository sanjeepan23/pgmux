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

