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

func mustSQL(t testing.TB, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func TestTransactionCommitRollback(t *testing.T) {
	ctx := context.Background()
	for name, m := range pgxModes {
		t.Run(name, func(t *testing.T) {
			c := pgxConnect(t, m)
			tbl := table("tx")
			_, _ = c.Exec(ctx, "CREATE TABLE "+tbl+" (v int)")
			defer c.Exec(ctx, "DROP TABLE "+tbl)
			_ = pgx.BeginFunc(ctx, c, func(tx pgx.Tx) error { _, err := tx.Exec(ctx, "INSERT INTO "+tbl+" VALUES (1)"); return err })
			tx, _ := c.Begin(ctx)
			_, _ = tx.Exec(ctx, "INSERT INTO "+tbl+" VALUES (2)")
			_ = tx.Rollback(ctx)
			tx, _ = c.Begin(ctx)
			if _, err := tx.Exec(ctx, "SELECT 1/0"); err == nil {
				t.Fatal("expected an error")
			}
			_ = tx.Rollback(ctx)
			var n int
			if err := c.QueryRow(ctx, "SELECT count(*) FROM "+tbl).Scan(&n); err != nil || n != 1 {
				t.Fatalf("count = %d, err = %v", n, err)
			}
		})
	}
	for name, driver := range sqlDrivers {
		t.Run(name, func(t *testing.T) {
			db := sqlOpen(t, driver)
			tbl := table("tx")
			mustSQL(t, db, "CREATE TABLE "+tbl+" (v int)")
			defer mustSQL(t, db, "DROP TABLE "+tbl)
			tx, _ := db.Begin()
			_, _ = tx.Exec("INSERT INTO " + tbl + " VALUES (1)")
			_ = tx.Commit()
			tx, _ = db.Begin()
			_, _ = tx.Exec("INSERT INTO " + tbl + " VALUES (2)")
			_ = tx.Rollback()
			var n int
			if err := db.QueryRow("SELECT count(*) FROM " + tbl).Scan(&n); err != nil || n != 1 {
				t.Fatalf("count = %d, err = %v", n, err)
			}
		})
	}
}

func TestPreparedStatements(t *testing.T) {
	ctx := context.Background()
	t.Run("pgx_named", func(t *testing.T) {
		for round := 0; round < 3; round++ {
			c := pgxConnect(t, pgx.QueryExecModeCacheStatement)
			if _, err := c.Prepare(ctx, "add", "SELECT $1::int + 1"); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 100; i++ {
				var got int
				if err := c.QueryRow(ctx, "add", i).Scan(&got); err != nil || got != i+1 {
					t.Fatalf("round %d iter %d: got %d, err %v", round, i, got, err)
				}
			}
			c.Close(ctx)
		}
	})
	for name, driver := range sqlDrivers {
		t.Run(name, func(t *testing.T) {
			for round := 0; round < 3; round++ {
				db := sqlOpen(t, driver)
				stmt, err := db.Prepare("SELECT $1::int + 1")
				if err != nil {
					t.Fatal(err)
				}
				for i := 0; i < 100; i++ {
					var got int
					if err := stmt.QueryRow(i).Scan(&got); err != nil || got != i+1 {
						t.Fatalf("round %d iter %d: got %d, err %v", round, i, got, err)
					}
				}
				stmt.Close()
				db.Close()
			}
		})
	}
}

func TestConcurrency(t *testing.T) {
	ctx := context.Background()
	setup := pgxConnect(t, pgx.QueryExecModeCacheStatement)
	tbl := table("conc")
	_, _ = setup.Exec(ctx, "CREATE TABLE "+tbl+" (id int PRIMARY KEY, n int)")
	_, _ = setup.Exec(ctx, "INSERT INTO "+tbl+" VALUES (1, 0)")
	defer setup.Exec(ctx, "DROP TABLE "+tbl)
	const workers, txs = 200, 50
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := pgx.Connect(ctx, dsn(""))
			if err != nil {
				errs <- err
				return
			}
			defer c.Close(ctx)
			for i := 0; i < txs; i++ {
				if err := pgx.BeginFunc(ctx, c, func(tx pgx.Tx) error {
					_, err := tx.Exec(ctx, "UPDATE "+tbl+" SET n = n + 1 WHERE id = 1")
					return err
				}); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var n int
	if err := setup.QueryRow(ctx, "SELECT n FROM "+tbl).Scan(&n); err != nil || n != workers*txs {
		t.Fatalf("n = %d, err = %v", n, err)
	}
}

func TestCancelQuery(t *testing.T) {
	t.Run("pgx", func(t *testing.T) {
		c := pgxConnect(t, pgx.QueryExecModeCacheStatement)
		go func() {
			time.Sleep(300 * time.Millisecond)
			_ = c.PgConn().CancelRequest(context.Background())
		}()
		_, err := c.Exec(context.Background(), "SELECT pg_sleep(30)")
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "57014" {
			t.Fatalf("err = %v", err)
		}
		var n int
		if err := c.QueryRow(context.Background(), "SELECT 1").Scan(&n); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("pq", func(t *testing.T) {
		db := sqlOpen(t, "postgres")
		db.SetMaxOpenConns(1)
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		start := time.Now()
		// lib/pq sends a CancelRequest when the context ends.
		_, err := db.ExecContext(ctx, "SELECT pg_sleep(30)")
		if err == nil || time.Since(start) > 5*time.Second {
			t.Fatalf("err = %v after %v", err, time.Since(start))
		}
		var n int
		if err := db.QueryRow("SELECT 1").Scan(&n); err != nil {
			t.Fatal(err)
		}
	})
}

func TestCopyInOut(t *testing.T) {
	ctx := context.Background()
	const rows = 100_000
	t.Run("pgx", func(t *testing.T) {
		c := pgxConnect(t, pgx.QueryExecModeCacheStatement)
		tbl := table("copy")
		_, _ = c.Exec(ctx, "CREATE TABLE "+tbl+" (id int, name text)")
		defer c.Exec(ctx, "DROP TABLE "+tbl)
		n, err := c.CopyFrom(ctx, pgx.Identifier{tbl}, []string{"id", "name"},
			pgx.CopyFromSlice(rows, func(i int) ([]any, error) { return []any{i, "n"}, nil }))
		if err != nil || n != rows {
			t.Fatalf("copy from: %d %v", n, err)
		}
		var buf bytes.Buffer
		if tag, err := c.PgConn().CopyTo(ctx, &buf, "COPY "+tbl+" TO STDOUT"); err != nil || tag.RowsAffected() != rows {
			t.Fatalf("copy to: %v %v", tag, err)
		}
	})
	t.Run("pq", func(t *testing.T) {
		db := sqlOpen(t, "postgres")
		tbl := table("copy")
		mustSQL(t, db, "CREATE TABLE "+tbl+" (id int)")
		defer mustSQL(t, db, "DROP TABLE "+tbl)
		tx, _ := db.Begin()
		stmt, err := tx.Prepare(pq.CopyIn(tbl, "id"))
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < rows; i++ {
			if _, err := stmt.Exec(i); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := stmt.Exec(); err != nil {
			t.Fatal(err)
		}
		_ = stmt.Close()
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		var n int
		_ = db.QueryRow("SELECT count(*) FROM " + tbl).Scan(&n)
		if n != rows {
			t.Fatalf("count = %d", n)
		}
	})
}

func TestLargeResult(t *testing.T) {
	c := pgxConnect(t, pgx.QueryExecModeCacheStatement)
	rows, err := c.Query(context.Background(), "SELECT g FROM generate_series(1, 1000000) g")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for rows.Next() {
		n++
	}
	if rows.Err() != nil || n != 1_000_000 {
		t.Fatalf("rows = %d, err = %v", n, rows.Err())
	}
}

