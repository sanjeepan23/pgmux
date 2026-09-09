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

// Reload swaps in a new config, user list and TLS config for new connections.
func (srv *Server) Reload(cfg *config.Config, creds map[string]*auth.Credential, tlsCfg *tls.Config) {
	srv.cfg.Store(cfg)
	if creds == nil {
		creds = map[string]*auth.Credential{}
	}
	srv.creds.Store(&creds)
	srv.tls.Store(tlsCfg)
	srv.authCache.Clear()
	for user, cred := range creds {
		for _, p := range srv.Manager.Pools() {
			if p.Key.User == user {
				p.UpdateLogin(loginFor(cred))
			}
		}
	}
}

// Serve runs one client connection until it closes.
func (srv *Server) Serve(ctx context.Context, nc net.Conn) {
	metrics.ClientConnectionsTotal.Inc()
	s, err := srv.startup(ctx, nc)
	if err != nil {
		if s == nil {
			_ = nc.Close()
		}
		if !errors.Is(err, errCancelRequest) {
			slog.Debug("client login failed", "remote", nc.RemoteAddr(), "err", err)
		}
		return
	}
	defer srv.unregister(s)
	defer s.conn.Close()
	defer s.finish()
	err = s.run()
	if !errors.Is(err, errTerminate) && !errors.Is(err, net.ErrClosed) {
		s.log.Debug("client session ended", "err", err)
	}
}

var errCancelRequest = errors.New("cancel request")

func (srv *Server) startup(ctx context.Context, nc net.Conn) (*Session, error) {
	cfg := srv.cfg.Load()
	_ = nc.SetDeadline(time.Now().Add(loginTimeout))
	rd := protocol.NewReader(nc, 0)
	wr := protocol.NewWriter(nc)
	isTLS := false

	fail := func(code, msg string) error {
		_ = wr.WriteRaw(protocol.ErrorMessage("FATAL", code, msg))
		_ = wr.Flush()
		return errors.New(msg)
	}

	var params map[string]string
	var version uint32
	for params == nil {
		body, err := rd.ReadStartup()
		if err != nil {
			return nil, err
		}
		switch code := binary.BigEndian.Uint32(body); code {
		case protocol.SSLRequestCode:
			tlsCfg := srv.tls.Load()
			if tlsCfg == nil || isTLS {
				if _, err := nc.Write([]byte{'N'}); err != nil {
					return nil, err
				}
				continue
			}
			// Bytes sent before the TLS handshake would bypass TLS, so refuse them.
			if rd.Buffered() > 0 {
				return nil, errors.New("client sent data after SSLRequest")
			}
			if _, err := nc.Write([]byte{'S'}); err != nil {
				return nil, err
			}
			tc := tls.Server(nc, tlsCfg)
			if err := tc.HandshakeContext(ctx); err != nil {
				return nil, fmt.Errorf("tls handshake: %w", err)
			}
			nc, isTLS = tc, true
			rd, wr = protocol.NewReader(nc, 0), protocol.NewWriter(nc)
		case protocol.GSSEncRequestCode:
			if _, err := nc.Write([]byte{'N'}); err != nil {
				return nil, err
			}
		case protocol.CancelRequestCode:
			srv.handleCancel(body)
			return nil, errCancelRequest
		default:
			var sm pgproto3.StartupMessage
			if err := sm.Decode(body); err != nil {
				return nil, fail("08P01", err.Error())
			}
			params, version = sm.Parameters, sm.ProtocolVersion
		}
	}

	if cfg.ClientTLS.Required && !isTLS {
		return nil, fail("28000", "pgmux requires TLS")
	}
	if params["replication"] != "" && params["replication"] != "false" {
		return nil, fail("08P01", "pgmux does not support replication connections")
	}
	user := params["user"]
	if user == "" {
		return nil, fail("28000", "no user name in startup packet")
	}
	db := params["database"]
	if db == "" {
		db = user
	}

	// pgmux speaks protocol 3.0 and supports no protocol extensions.
	var unknownOpts []string
	for k := range params {
		if strings.HasPrefix(k, "_pq_.") {
			unknownOpts = append(unknownOpts, k)
		}
	}
	if version != protocol.ProtocolVersion30 || len(unknownOpts) > 0 {
		if err := wr.Write(&pgproto3.NegotiateProtocolVersion{NewestMinorProtocol: 0, UnrecognizedOptions: unknownOpts}); err != nil {
			return nil, err
		}
	}

	cred, err := srv.credential(ctx, cfg, user)
	if err != nil {
		slog.Warn("auth_query failed", "user", user, "err", err)
		return nil, fail("28000", "pgmux: cannot look up user")
	}
	keys, err := auth.AuthenticateClient(rd, wr, cfg.Auth.Method, cred)
	if err != nil {
		metrics.ErrorsTotal.WithLabelValues(metrics.ErrAuthFailed).Inc()
		if errors.Is(err, auth.ErrAuthFailed) {
			return nil, fail("28P01", fmt.Sprintf("password authentication failed for user %q", user))
		}
		return nil, fail("28000", "authentication failed: "+err.Error())
	}

	var login pool.Login
	if cred != nil {
		login = loginFor(cred)
	} else {
		login = pool.Login{User: user}
	}
	p := srv.Manager.Get(pool.Key{User: user, Database: db}, login)
	if keys != nil {
		p.SetClientKeys(keys.ClientKey, keys.ServerKey)
	}

	serverParams, err := srv.serverParams(ctx, p)
	if err != nil {
		return nil, fail("08006", "pgmux: cannot connect to server: "+err.Error())
	}

	s := &Session{
		srv:       srv,
		ctx:       ctx,
		conn:      nc,
		rd:        rd,
		wr:        wr,
		pool:      p,
		txMode:    p.Mode() == config.ModeTransaction,
		user:      user,
		db:        db,
		stmts:     map[string]*prepared.Statement{},
		warned:    map[string]bool{},
		txStatus:  'I',
		params:    clientParams(params),
		relayDone: closedChan(),
		poke:      make(chan struct{}, 1),
		lastUsed:  time.Now(),

		queries:      metrics.QueriesTotal.WithLabelValues(user, db),
		transactions: metrics.TransactionsTotal.WithLabelValues(user, db),
	}
	// A client expects the server defaults for tracked parameters that it did not set.
	// Without this, a value that another client SET on the server would leak to this client.
	for name, v := range serverParams {
		key := strings.ToLower(name)
		if _, ok := s.params[key]; !ok && trackedParams[key] {
			s.params[key] = v
		}
	}
	if err := srv.register(s, cfg); err != nil {
		metrics.ErrorsTotal.WithLabelValues(metrics.ErrClientLimit).Inc()
		return nil, fail("53300", err.Error())
	}
	s.log = slog.With("session", s.id, "user", user, "db", db, "remote", nc.RemoteAddr().String())

	_ = wr.Write(&pgproto3.AuthenticationOk{})
	for name, v := range serverParams {
		if cv, ok := s.params[strings.ToLower(name)]; ok {
			v = cv
		}
		_ = wr.Write(&pgproto3.ParameterStatus{Name: name, Value: v})
	}
	_ = wr.Write(&pgproto3.BackendKeyData{ProcessID: s.key.pid, SecretKey: binary.BigEndian.AppendUint32(nil, s.key.secret)})
	_ = wr.WriteRaw(protocol.ReadyForQuery('I'))
	if err := wr.Flush(); err != nil {
		srv.unregister(s)
		return nil, err
	}
	_ = nc.SetDeadline(time.Time{})
	return s, nil
}

// clientParams returns the startup parameters that pgmux applies to each server, keyed by lower-case name.
func clientParams(startup map[string]string) map[string]string {
	out := map[string]string{"application_name": ""}
	for k, v := range startup {
		switch k {
		case "user", "database", "replication", "options":
			continue
		}
		if strings.HasPrefix(k, "_pq_.") {
			continue
		}
		out[strings.ToLower(k)] = v
	}
	return out
}

func closedChan() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

// serverParams returns the pool's cached server parameters. It opens a server connection if the pool has none yet.
func (srv *Server) serverParams(ctx context.Context, p *pool.Pool) (map[string]string, error) {
	if params := p.ServerParams(); params != nil {
		return params, nil
	}
	c, err := p.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	p.Release(c)
	return p.ServerParams(), nil
}

func loginFor(cred *auth.Credential) pool.Login {
	return pool.Login{User: cred.User, Password: cred.Password, MD5: cred.MD5}
}

// credential finds the stored password for user in the userlist, or with auth_query.
// It returns nil, nil when the user is unknown.
func (srv *Server) credential(ctx context.Context, cfg *config.Config, user string) (*auth.Credential, error) {
	if c, ok := (*srv.creds.Load())[user]; ok {
		return c, nil
	}
	if cfg.Auth.AuthUser == "" || cfg.Auth.Method == auth.MethodTrust {
		return nil, nil
	}
	if e, ok := srv.authCache.Load(user); ok {
		if entry := e.(authCacheEntry); time.Now().Before(entry.expires) {
			return entry.cred, nil
		}
	}
	cred, err := srv.runAuthQuery(ctx, cfg, user)
	if err != nil {
		return nil, err
	}
	if cred != nil {
		srv.authCache.Store(user, authCacheEntry{cred: cred, expires: time.Now().Add(time.Minute)})
	}
	return cred, nil
}

// runAuthQuery fetches a user's password hash from Postgres as auth_user.
func (srv *Server) runAuthQuery(ctx context.Context, cfg *config.Config, user string) (*auth.Credential, error) {
	authCred, ok := (*srv.creds.Load())[cfg.Auth.AuthUser]
	if !ok {
		return nil, fmt.Errorf("auth_user %q is not in the userlist", cfg.Auth.AuthUser)
	}
	p := srv.Manager.Get(pool.Key{User: cfg.Auth.AuthUser, Database: cfg.Auth.AuthDBName}, loginFor(authCred))
	c, err := p.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	sql := strings.ReplaceAll(cfg.Auth.AuthQuery, "$1", quoteLiteral(user))
	row, err := queryRow(ctx, c, sql)
	if err != nil {
		if c.Broken() {
			p.Destroy(c)
		} else {
			p.Release(c)
		}
		return nil, err
	}
	p.Release(c)
	if len(row) < 2 || row[1] == nil {
		return nil, nil
	}
	return auth.ParseCredential(user, string(row[1]))
}

// queryRow runs a simple query and returns the first row, or nil when there are no rows.
func queryRow(ctx context.Context, c *pool.Conn, sql string) ([][]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := c.WriteRaw(protocol.MustEncode(&pgproto3.Query{String: sql})); err != nil {
		return nil, err
	}
	if err := c.Flush(); err != nil {
		return nil, err
	}
	var row [][]byte
	var qerr error
	for {
		select {
		case <-ctx.Done():
			c.MarkBroken()
			return nil, ctx.Err()
		case m, ok := <-c.Msgs():
			if !ok {
				return nil, c.ReadErr()
			}
			switch m.Type {
			case 'D':
				if row == nil {
					var dr pgproto3.DataRow
					if err := dr.Decode(m.Body); err != nil {
						return nil, err
					}
					row = make([][]byte, len(dr.Values))
					for i, v := range dr.Values {
						if v != nil {
							row[i] = append([]byte{}, v...)
						}
					}
				}
			case 'E':
				qerr = protocol.DecodeError(m.Body)
			case 'Z':
				return row, qerr
			}
		}
	}
}

func (srv *Server) register(s *Session, cfg *config.Config) error {
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if cfg.MaxClientConn > 0 && len(srv.sessions) >= cfg.MaxClientConn {
		return errors.New("too many client connections (max_client_conn)")
	}
	if u, ok := cfg.Users[s.user]; ok && u.MaxConnections > 0 && srv.userConns[s.user] >= u.MaxConnections {
		return fmt.Errorf("too many connections for user %q", s.user)
	}
	srv.nextID++
	s.id = srv.nextID
	for {
		var b [8]byte
		_, _ = rand.Read(b[:])
		k := cancelKey{pid: binary.BigEndian.Uint32(b[:4]) & 0x7fffffff, secret: binary.BigEndian.Uint32(b[4:])}
		if _, taken := srv.byCancel[k]; !taken {
			s.key = k
			break
		}
	}
	srv.sessions[s.id] = s
	srv.byCancel[s.key] = s
	srv.userConns[s.user]++
	s.pool.ClientConnected()
	return nil
}

func (srv *Server) unregister(s *Session) {
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if _, ok := srv.sessions[s.id]; !ok {
		return
	}
	delete(srv.sessions, s.id)
	delete(srv.byCancel, s.key)
	srv.userConns[s.user]--
	s.pool.ClientDisconnected()
}

func (srv *Server) handleCancel(body []byte) {
	if len(body) != 12 {
		return
	}
	k := cancelKey{pid: binary.BigEndian.Uint32(body[4:]), secret: binary.BigEndian.Uint32(body[8:])}
	srv.mu.Lock()
	s := srv.byCancel[k]
	srv.mu.Unlock()
	if s != nil {
		s.cancel()
	}
}

