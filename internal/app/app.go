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

// Start loads the config, opens the listeners and starts serving.
func Start(configPath, version string) (*App, error) {
	l, err := load(configPath)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	a := &App{configPath: configPath, version: version, cfg: l.cfg, cancel: cancel, serveErr: make(chan error, 1)}

	a.Manager = pool.NewManager(l.cfg, l.dialer)
	metrics.SetPoolSource(a.Manager.MetricStats)
	go a.Manager.Run(ctx)

	a.Server = session.NewServer(l.cfg, l.creds, l.tls, a.Manager)
	if a.ln, err = listener.Listen(l.cfg.Listen, a.Server.Serve); err != nil {
		cancel()
		return nil, err
	}

	if l.cfg.AdminListen != "" {
		if a.adminLn, err = net.Listen("tcp", l.cfg.AdminListen); err != nil {
			_ = a.ln.Close()
			cancel()
			return nil, fmt.Errorf("admin listen: %w", err)
		}
		adm := &admin.Server{
			Manager:      a.Manager,
			Clients:      a.Server.ClientCount,
			Reload:       a.Reload,
			ReadOnly:     l.cfg.AdminReadOnly,
			DashboardDir: l.cfg.Resolve(l.cfg.DashboardDir),
			Version:      version,
		}
		go func() {
			if err := adm.Serve(ctx, a.adminLn); err != nil {
				slog.Error("admin API failed", "err", err)
			}
		}()
	}

	files := []string{configPath}
	if l.cfg.Auth.Userlist != "" {
		files = append(files, l.cfg.Resolve(l.cfg.Auth.Userlist))
	}
	if err := config.Watch(ctx, files, func() { _ = a.Reload() }); err != nil {
		slog.Warn("config hot reload is off", "err", err)
	}

	go func() { a.serveErr <- a.ln.Serve(ctx) }()
	return a, nil
}

// Addr returns the client listen address.
func (a *App) Addr() string { return a.ln.Addr().String() }

// AdminAddr returns the admin listen address, or "" when the admin API is off.
func (a *App) AdminAddr() string {
	if a.adminLn == nil {
		return ""
	}
	return a.adminLn.Addr().String()
}

