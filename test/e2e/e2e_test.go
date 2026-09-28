package e2e

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestBasicCRUD(t *testing.T) {
	bothModes(t, func(t *testing.T, mode string) {
		a, _ := startPgmux(t, options{mode: mode})
		c := connect(t, appDSN(a))
		tbl := uniqueName("crud")
		mustExec(t, c, "CREATE TABLE "+tbl+" (id int PRIMARY KEY, name text)")
		defer mustExec(t, c, "DROP TABLE "+tbl)
		mustExec(t, c, "INSERT INTO "+tbl+" VALUES ($1, $2), ($3, $4)", 1, "a", 2, "b")
		mustExec(t, c, "UPDATE "+tbl+" SET name = $1 WHERE id = $2", "z", 2)
		if got := queryString(t, c, "SELECT name FROM "+tbl+" WHERE id = 2"); got != "z" {
			t.Fatalf("name = %q, want z", got)
		}
		mustExec(t, c, "DELETE FROM "+tbl+" WHERE id = 1")
		if n := queryInt(t, c, "SELECT count(*) FROM "+tbl); n != 1 {
			t.Fatalf("count = %d, want 1", n)
		}
	})
}

func TestTransactionCommitRollback(t *testing.T) {
	bothModes(t, func(t *testing.T, mode string) {
		a, _ := startPgmux(t, options{mode: mode, poolSize: 2})
		ctx := context.Background()
		c := connect(t, appDSN(a))
		other := connect(t, appDSN(a))
		tbl := uniqueName("tx")
		mustExec(t, c, "CREATE TABLE "+tbl+" (v int)")
		defer mustExec(t, c, "DROP TABLE "+tbl)

		tx, err := c.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, "INSERT INTO "+tbl+" VALUES (1)"); err != nil {
			t.Fatal(err)
		}
		if n := queryInt(t, other, "SELECT count(*) FROM "+tbl); n != 0 {
			t.Fatalf("uncommitted row is visible to another client")
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}

		tx, _ = c.Begin(ctx)
		_, _ = tx.Exec(ctx, "INSERT INTO "+tbl+" VALUES (2)")
		_ = tx.Rollback(ctx)

		// An error inside a transaction must leave the client usable after rollback.
		tx, _ = c.Begin(ctx)
		if _, err := tx.Exec(ctx, "SELECT 1/0"); err == nil {
			t.Fatal("expected division by zero")
		}
		_ = tx.Rollback(ctx)

		if n := queryInt(t, other, "SELECT count(*) FROM "+tbl); n != 1 {
			t.Fatalf("count = %d, want 1", n)
		}
	})
}

// TestPreparedStatementsAcrossServers makes many clients share few servers, so statements must move between servers.
func TestPreparedStatementsAcrossServers(t *testing.T) {
	a, _ := startPgmux(t, options{poolSize: 3})
	modes := []pgx.QueryExecMode{
		pgx.QueryExecModeCacheStatement, pgx.QueryExecModeCacheDescribe,
		pgx.QueryExecModeDescribeExec, pgx.QueryExecModeExec, pgx.QueryExecModeSimpleProtocol,
	}
	for _, mode := range modes {
		t.Run(mode.String(), func(t *testing.T) {
			var wg sync.WaitGroup
			errs := make(chan error, 20)
			for w := 0; w < 10; w++ {
				wg.Add(1)
				go func(w int) {
					defer wg.Done()
					ctx := context.Background()
					cfg, _ := pgx.ParseConfig(appDSN(a))
					cfg.DefaultQueryExecMode = mode
					// Reconnect a few times, so that each new connection prepares again.
					for round := 0; round < 3; round++ {
						c, err := pgx.ConnectConfig(ctx, cfg)
						if err != nil {
							errs <- err
							return
						}
						for i := 0; i < 100; i++ {
							var got int
							if err := c.QueryRow(ctx, "SELECT $1::int + $2::int", i, w).Scan(&got); err != nil {
								errs <- fmt.Errorf("worker %d iter %d: %w", w, i, err)
								c.Close(ctx)
								return
							}
							if got != i+w {
								errs <- fmt.Errorf("got %d, want %d", got, i+w)
							}
						}
						c.Close(ctx)
					}
				}(w)
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				t.Error(err)
			}
		})
	}
}

func TestConcurrency(t *testing.T) {
	a, _ := startPgmux(t, options{poolSize: 10})
	ctx := context.Background()
	setup := connect(t, appDSN(a))
	tbl := uniqueName("conc")
	mustExec(t, setup, "CREATE TABLE "+tbl+" (id int PRIMARY KEY, n int)")
	mustExec(t, setup, "INSERT INTO "+tbl+" VALUES (1, 0)")
	defer mustExec(t, setup, "DROP TABLE "+tbl)

	const workers, txPerWorker = 200, 50
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := pgx.Connect(ctx, appDSN(a))
			if err != nil {
				errs <- err
				return
			}
			defer c.Close(ctx)
			for i := 0; i < txPerWorker; i++ {
				err := pgx.BeginFunc(ctx, c, func(tx pgx.Tx) error {
					_, err := tx.Exec(ctx, "UPDATE "+tbl+" SET n = n + 1 WHERE id = 1")
					return err
				})
				if err != nil {
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
	if n := queryInt(t, setup, "SELECT n FROM "+tbl+" WHERE id = 1"); n != workers*txPerWorker {
		t.Fatalf("n = %d, want %d", n, workers*txPerWorker)
	}
	for _, p := range a.Manager.Pools() {
		if s := p.Stats(); s.ServersTotal > 10 {
			t.Fatalf("pool opened %d servers, max is 10", s.ServersTotal)
		}
	}
}

func TestCancelQuery(t *testing.T) {
	bothModes(t, func(t *testing.T, mode string) {
		a, _ := startPgmux(t, options{mode: mode})
		c := connect(t, appDSN(a))
		// Send the cancel the way psql does on Ctrl+C: a CancelRequest on a new connection.
		go func() {
			time.Sleep(300 * time.Millisecond)
			_ = c.PgConn().CancelRequest(context.Background())
		}()
		start := time.Now()
		_, err := c.Exec(context.Background(), "SELECT pg_sleep(30)")
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "57014" {
			t.Fatalf("err = %v, want query_canceled (57014)", err)
		}
		if d := time.Since(start); d > 5*time.Second {
			t.Fatalf("cancel took %v", d)
		}
		if n := queryInt(t, c, "SELECT 42"); n != 42 {
			t.Fatal("connection is not usable after cancel")
		}
	})
}

func TestCopyInOut(t *testing.T) {
	bothModes(t, func(t *testing.T, mode string) {
		a, _ := startPgmux(t, options{mode: mode})
		ctx := context.Background()
		c := connect(t, appDSN(a))
		tbl := uniqueName("copy")
		mustExec(t, c, "CREATE TABLE "+tbl+" (id int, name text)")
		defer mustExec(t, c, "DROP TABLE "+tbl)

		const rows = 100_000
		n, err := c.CopyFrom(ctx, pgx.Identifier{tbl}, []string{"id", "name"},
			pgx.CopyFromSlice(rows, func(i int) ([]any, error) { return []any{i, fmt.Sprintf("row %d", i)}, nil }))
		if err != nil || n != rows {
			t.Fatalf("copy from: n=%d err=%v", n, err)
		}
		var buf bytes.Buffer
		tag, err := c.PgConn().CopyTo(ctx, &buf, "COPY "+tbl+" TO STDOUT")
		if err != nil || tag.RowsAffected() != rows {
			t.Fatalf("copy to: tag=%v err=%v", tag, err)
		}
		if lines := bytes.Count(buf.Bytes(), []byte("\n")); lines != rows {
			t.Fatalf("copy to returned %d lines", lines)
		}
		// A failed COPY must leave the connection usable.
		_, err = c.PgConn().CopyFrom(ctx, strings.NewReader("not-a-number\tx\n"), "COPY "+tbl+" FROM STDIN")
		if err == nil {
			t.Fatal("expected copy error")
		}
		if got := queryInt(t, c, "SELECT count(*) FROM "+tbl); got != rows {
			t.Fatalf("count = %d", got)
		}
	})
}

func TestLargeResult(t *testing.T) {
	a, _ := startPgmux(t, options{})
	c := connect(t, appDSN(a))
	rows, err := c.Query(context.Background(), "SELECT g, repeat('x', 100) FROM generate_series(1, 1000000) g")
	if err != nil {
		t.Fatal(err)
	}
	var count int
	for rows.Next() {
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if count != 1_000_000 {
		t.Fatalf("got %d rows", count)
	}
}

func TestListenNotify(t *testing.T) {
	t.Run("session", func(t *testing.T) {
		a, _ := startPgmux(t, options{mode: "session"})
		listener := connect(t, appDSN(a))
		notifier := connect(t, appDSN(a))
		mustExec(t, listener, "LISTEN pgmux_test")
		mustExec(t, notifier, "SELECT pg_notify('pgmux_test', 'hello')")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		n, err := listener.WaitForNotification(ctx)
		if err != nil || n.Payload != "hello" {
			t.Fatalf("notification = %v, err = %v", n, err)
		}
	})
	t.Run("transaction", func(t *testing.T) {
		a, _ := startPgmux(t, options{})
		c := connect(t, appDSN(a, "default_query_exec_mode", "simple_protocol"))
		_, err := c.Exec(context.Background(), "LISTEN pgmux_test")
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || !strings.Contains(pgErr.Message, "transaction pooling") {
			t.Fatalf("err = %v, want a clear rejection", err)
		}
		if n := queryInt(t, c, "SELECT 1"); n != 1 {
			t.Fatal("connection is not usable after rejection")
		}
	})
}

func TestTLS(t *testing.T) {
	a, _ := startPgmux(t, options{tlsRequired: true})
	ca := repoRoot() + "/deploy/certs/ca.crt"
	host := strings.Replace(appDSN(a, "sslmode", "verify-full", "sslrootcert", ca), "127.0.0.1", "localhost", 1)
	c := connect(t, host)
	if n := queryInt(t, c, "SELECT 1"); n != 1 {
		t.Fatal("query over TLS failed")
	}
	if _, err := pgx.Connect(context.Background(), appDSN(a)); err == nil || !strings.Contains(err.Error(), "requires TLS") {
		t.Fatalf("plaintext connect err = %v, want TLS required", err)
	}
}

func TestAuth(t *testing.T) {
	ctx := context.Background()
	t.Run("scram", func(t *testing.T) {
		a, _ := startPgmux(t, options{})
		if _, err := pgx.Connect(ctx, dsn(a, "app", "wrong", "appdb")); err == nil || !strings.Contains(err.Error(), "28P01") {
			t.Fatalf("wrong password err = %v", err)
		}
		if _, err := pgx.Connect(ctx, dsn(a, "nobody", "x", "appdb")); err == nil {
			t.Fatal("unknown user connected")
		}
		c := connect(t, dsn(a, "md5user", "md5pass", "appdb"))
		if got := queryString(t, c, "SELECT current_user"); got != "md5user" {
			t.Fatalf("current_user = %q", got)
		}
	})
	t.Run("md5", func(t *testing.T) {
		a, _ := startPgmux(t, options{authMethod: "md5"})
		c := connect(t, appDSN(a))
		queryInt(t, c, "SELECT 1")
		if _, err := pgx.Connect(ctx, dsn(a, "app", "wrong", "appdb")); err == nil {
			t.Fatal("wrong password accepted")
		}
	})
	t.Run("auth_query", func(t *testing.T) {
		a, _ := startPgmux(t, options{})
		// bob is not in the userlist. pgmux reads his SCRAM secret from Postgres and reuses his proof to log in.
		c := connect(t, dsn(a, "bob", "bob", "appdb"))
		if got := queryString(t, c, "SELECT current_user"); got != "bob" {
			t.Fatalf("current_user = %q", got)
		}
		if _, err := pgx.Connect(ctx, dsn(a, "bob", "wrong", "appdb")); err == nil {
			t.Fatal("wrong password accepted for auth_query user")
		}
	})
}

