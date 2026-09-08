// Package session handles client connections: startup, authentication and the per-client state machine.
package session

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"

	"github.com/sanjeepan23/pgmux/internal/auth"
	"github.com/sanjeepan23/pgmux/internal/config"
	"github.com/sanjeepan23/pgmux/internal/metrics"
	"github.com/sanjeepan23/pgmux/internal/pool"
	"github.com/sanjeepan23/pgmux/internal/prepared"
	"github.com/sanjeepan23/pgmux/internal/protocol"
)

// loginTimeout limits the time from TCP accept to the end of authentication.
const loginTimeout = 15 * time.Second

type cancelKey struct {
	pid    uint32
	secret uint32
}

// Server accepts client connections and runs a Session for each one.
type Server struct {
	Manager *pool.Manager

	cfg   atomic.Pointer[config.Config]
	creds atomic.Pointer[map[string]*auth.Credential]
	tls   atomic.Pointer[tls.Config]

	authCache sync.Map // user -> authCacheEntry

	mu        sync.Mutex
	sessions  map[uint64]*Session
	byCancel  map[cancelKey]*Session
	userConns map[string]int
	nextID    uint64
}

type authCacheEntry struct {
	cred    *auth.Credential
	expires time.Time
}

// NewServer returns a Server. tlsCfg is nil when client TLS is off.
func NewServer(cfg *config.Config, creds map[string]*auth.Credential, tlsCfg *tls.Config, mgr *pool.Manager) *Server {
	s := &Server{
		Manager:   mgr,
		sessions:  map[uint64]*Session{},
		byCancel:  map[cancelKey]*Session{},
		userConns: map[string]int{},
	}
	s.Reload(cfg, creds, tlsCfg)
	return s
}

