// Package e2e runs pgmux in-process against a real Postgres server.
// Set PGMUX_TEST_PG_HOST to run it, for example after `make compose-up`.
package e2e

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sanjeepan23/pgmux/internal/app"
)

type options struct {
	mode         string
	poolSize     int
	waitTimeout  string
	authMethod   string
	tlsRequired  bool
	extraYAML    string
	resetQuery   string
	healthPeriod string
}

func repoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

func env(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

var tableSeq atomic.Int64

// uniqueName returns a table name that no other test uses.
func uniqueName(prefix string) string {
	return fmt.Sprintf("%s_%d_%d", prefix, os.Getpid(), tableSeq.Add(1))
}

// startPgmux writes a config file, starts pgmux and stops it when the test ends.
func startPgmux(t *testing.T, o options) (*app.App, string) {
	t.Helper()
	host := os.Getenv("PGMUX_TEST_PG_HOST")
	if host == "" {
		t.Skip("PGMUX_TEST_PG_HOST is not set")
	}
	if o.mode == "" {
		o.mode = "transaction"
	}
	if o.poolSize == 0 {
		o.poolSize = 5
	}
	if o.waitTimeout == "" {
		o.waitTimeout = "10s"
	}
	if o.authMethod == "" {
		o.authMethod = "scram-sha-256"
	}
	if o.resetQuery == "" {
		o.resetQuery = "DISCARD ALL"
	}
	if o.healthPeriod == "" {
		o.healthPeriod = "30s"
	}
	root := repoRoot()
	dir := t.TempDir()
	cfg := fmt.Sprintf(`
listen: 127.0.0.1:0
admin_listen: 127.0.0.1:0
pool_mode: %s
shutdown_timeout: 5s
server:
  host: %s
  port: %s
  sslmode: %s
  sslrootcert: %s
client_tls:
  cert_file: %s
  key_file: %s
  required: %t
auth:
  method: %s
  userlist: %s
  auth_user: pgmux_auth
  auth_query: SELECT usename, passwd FROM public.pgmux_lookup($1)
  auth_dbname: postgres
pool:
  default_size: %d
  query_wait_timeout: %s
  health_check_period: %s
  reset_query: %s
%s
`, o.mode, host, env("PGMUX_TEST_PG_PORT", "5432"), env("PGMUX_TEST_PG_SSLMODE", "disable"),
		filepath.Join(root, "deploy/certs/ca.crt"),
		filepath.Join(root, "deploy/certs/server.crt"), filepath.Join(root, "deploy/certs/server.key"), o.tlsRequired,
		o.authMethod, filepath.Join(root, "deploy/userlist.txt"), o.poolSize, o.waitTimeout, o.healthPeriod, o.resetQuery, o.extraYAML)
	path := filepath.Join(dir, "pgmux.yaml")
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := app.Start(path, "test")
	if err != nil {
		t.Fatalf("start pgmux: %v", err)
	}
	t.Cleanup(func() { a.Shutdown(2 * time.Second) })
	return a, path
}

// dsn returns a connection string for pgmux with the given user and database.
func dsn(a *app.App, user, password, db string, extra ...string) string {
	host, port, _ := strings.Cut(a.Addr(), ":")
	u := url.URL{Scheme: "postgres", User: url.UserPassword(user, password), Host: host + ":" + port, Path: db}
	q := url.Values{}
	q.Set("sslmode", "disable")
	for i := 0; i+1 < len(extra); i += 2 {
		q.Set(extra[i], extra[i+1])
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func appDSN(a *app.App, extra ...string) string { return dsn(a, "app", "app", "appdb", extra...) }

func connect(t *testing.T, connString string) *pgx.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := pgx.Connect(ctx, connString)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { c.Close(context.Background()) })
	return c
}

func mustExec(t *testing.T, c *pgx.Conn, sql string, args ...any) {
	t.Helper()
	if _, err := c.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

func queryInt(t *testing.T, c *pgx.Conn, sql string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := c.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	return n
}

func queryString(t *testing.T, c *pgx.Conn, sql string, args ...any) string {
	t.Helper()
	var s string
	if err := c.QueryRow(context.Background(), sql, args...).Scan(&s); err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	return s
}

func bothModes(t *testing.T, fn func(t *testing.T, mode string)) {
	for _, mode := range []string{"session", "transaction"} {
		t.Run(mode, func(t *testing.T) { fn(t, mode) })
	}
}
