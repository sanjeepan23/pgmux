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

