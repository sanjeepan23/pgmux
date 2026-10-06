// Package compat runs the shared client scenarios against pgmux with pgx, pgx through database/sql, and lib/pq.
package compat

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/lib/pq"
)

func env(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

var (
	mode   = env("PGMUX_MODE", "transaction")
	dbName = map[string]string{"transaction": "appdb", "session": "sessiondb"}[mode]
	seq    atomic.Int64
)

func dsn(extra string) string {
	return fmt.Sprintf("host=%s port=%s user=app password=app dbname=%s sslmode=disable %s",
		env("PGMUX_HOST", "localhost"), env("PGMUX_PORT", "6432"), dbName, extra)
}

func table(prefix string) string {
	return fmt.Sprintf("go_%s_%d_%d", prefix, os.Getpid(), seq.Add(1))
}

var pgxModes = map[string]pgx.QueryExecMode{
	"pgx_cache_statement": pgx.QueryExecModeCacheStatement,
	"pgx_simple_protocol": pgx.QueryExecModeSimpleProtocol,
}

func pgxConnect(t testing.TB, m pgx.QueryExecMode) *pgx.Conn {
	t.Helper()
	cfg, err := pgx.ParseConfig(dsn(""))
	if err != nil {
		t.Fatal(err)
	}
	cfg.DefaultQueryExecMode = m
	c, err := pgx.ConnectConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close(context.Background()) })
	return c
}

