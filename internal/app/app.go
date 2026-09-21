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

// loaded is everything that one config load produces.
type loaded struct {
	cfg    *config.Config
	creds  map[string]*auth.Credential
	tls    *tls.Config
	dialer *pool.Dialer
}

func load(path string) (*loaded, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	l := &loaded{cfg: cfg, creds: map[string]*auth.Credential{}}
	if cfg.Auth.Userlist != "" {
		if l.creds, err = auth.LoadUserlist(cfg.Resolve(cfg.Auth.Userlist)); err != nil {
			return nil, fmt.Errorf("load userlist: %w", err)
		}
	}
	if cfg.ClientTLS.Enabled() {
		cert, err := tls.LoadX509KeyPair(cfg.Resolve(cfg.ClientTLS.CertFile), cfg.Resolve(cfg.ClientTLS.KeyFile))
		if err != nil {
			return nil, fmt.Errorf("load client TLS key pair: %w", err)
		}
		l.tls = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	}
	s := cfg.Server
	l.dialer, err = pool.NewDialer(s.Host, s.Port, s.SSLMode, cfg.Resolve(s.SSLRootCert), s.ConnectTimeout.Duration)
	if err != nil {
		return nil, err
	}
	return l, nil
}

