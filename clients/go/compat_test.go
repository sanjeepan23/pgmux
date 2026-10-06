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

// sqlDrivers are the database/sql drivers under test.
var sqlDrivers = map[string]string{"pq": "postgres", "pgx_stdlib": "pgx"}

func sqlOpen(t testing.TB, driver string) *sql.DB {
	t.Helper()
	db, err := sql.Open(driver, dsn(""))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestBasicCRUD(t *testing.T) {
	ctx := context.Background()
	for name, m := range pgxModes {
		t.Run(name, func(t *testing.T) {
			c := pgxConnect(t, m)
			tbl := table("crud")
			for _, q := range []string{
				"CREATE TABLE " + tbl + " (id int PRIMARY KEY, name text)",
				"INSERT INTO " + tbl + " VALUES (1, 'a'), (2, 'b')",
				"UPDATE " + tbl + " SET name = 'z' WHERE id = 2",
				"DELETE FROM " + tbl + " WHERE id = 1",
			} {
				if _, err := c.Exec(ctx, q); err != nil {
					t.Fatal(err)
				}
			}
			var name string
			if err := c.QueryRow(ctx, "SELECT name FROM "+tbl+" WHERE id = $1", 2).Scan(&name); err != nil || name != "z" {
				t.Fatalf("name = %q, err = %v", name, err)
			}
			_, _ = c.Exec(ctx, "DROP TABLE "+tbl)
		})
	}
	for name, driver := range sqlDrivers {
		t.Run(name, func(t *testing.T) {
			db := sqlOpen(t, driver)
			tbl := table("crud")
			mustSQL(t, db, "CREATE TABLE "+tbl+" (id int PRIMARY KEY, name text)")
			defer mustSQL(t, db, "DROP TABLE "+tbl)
			mustSQL(t, db, "INSERT INTO "+tbl+" VALUES ($1, $2)", 1, "a")
			mustSQL(t, db, "UPDATE "+tbl+" SET name = $1 WHERE id = $2", "z", 1)
			var name string
			if err := db.QueryRow("SELECT name FROM "+tbl+" WHERE id = $1", 1).Scan(&name); err != nil || name != "z" {
				t.Fatalf("name = %q, err = %v", name, err)
			}
			mustSQL(t, db, "DELETE FROM "+tbl)
		})
	}
}

