// Package app wires the pgmux components together. The main command and the end-to-end tests use it.
package app

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/sanjeepan23/pgmux/internal/admin"
	"github.com/sanjeepan23/pgmux/internal/auth"
	"github.com/sanjeepan23/pgmux/internal/config"
	"github.com/sanjeepan23/pgmux/internal/listener"
	"github.com/sanjeepan23/pgmux/internal/metrics"
	"github.com/sanjeepan23/pgmux/internal/pool"
	"github.com/sanjeepan23/pgmux/internal/session"
)

// App is a running pgmux instance.
type App struct {
	Manager *pool.Manager
	Server  *session.Server

	configPath string
	version    string
	cfg        *config.Config
	ln         *listener.Listener
	adminLn    net.Listener
	cancel     context.CancelFunc
	serveErr   chan error
	reloadMu   sync.Mutex
}

