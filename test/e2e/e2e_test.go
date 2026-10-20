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
	"sync/atomic"
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

// TestFastReuse runs short queries with more servers than clients, so a server goes to a new client while
// the previous client can still be inside its write. pgbench -S found this race.
func TestFastReuse(t *testing.T) {
	a, _ := startPgmux(t, options{poolSize: 25})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	const clients = 10
	var wg sync.WaitGroup
	var total atomic.Int64
	errs := make(chan error, clients)
	for w := 0; w < clients; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := pgx.Connect(context.Background(), appDSN(a, "default_query_exec_mode", "simple_protocol"))
			if err != nil {
				errs <- err
				return
			}
			defer c.Close(context.Background())
			for ctx.Err() == nil {
				var n int
				if err := c.QueryRow(context.Background(), "SELECT 1").Scan(&n); err != nil {
					errs <- err
					return
				}
				total.Add(1)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	t.Logf("%d queries", total.Load())
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

func TestQueryWaitTimeout(t *testing.T) {
	a, _ := startPgmux(t, options{poolSize: 1, waitTimeout: "300ms"})
	ctx := context.Background()
	holder := connect(t, appDSN(a))
	tx, err := holder.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SELECT 1"); err != nil {
		t.Fatal(err)
	}
	waiter := connect(t, appDSN(a))
	_, err = waiter.Exec(ctx, "SELECT 1")
	if err == nil || !strings.Contains(err.Error(), "query_wait_timeout") {
		t.Fatalf("err = %v, want query_wait_timeout", err)
	}
}

func TestParameterSync(t *testing.T) {
	a, _ := startPgmux(t, options{poolSize: 2})
	ctx := context.Background()
	ny := connect(t, appDSN(a))
	plain := connect(t, appDSN(a, "application_name", "plain"))
	mustExec(t, ny, "SET TimeZone = 'America/New_York'")
	// Hold one server in a transaction, so the next queries must use the other server.
	tx, _ := plain.Begin(ctx)
	_, _ = tx.Exec(ctx, "SELECT 1")
	for i := 0; i < 5; i++ {
		if tz := queryString(t, ny, "SHOW TimeZone"); tz != "America/New_York" {
			t.Fatalf("TimeZone = %q on round %d", tz, i)
		}
	}
	_ = tx.Rollback(ctx)
	for i := 0; i < 5; i++ {
		if tz := queryString(t, plain, "SHOW TimeZone"); tz == "America/New_York" {
			t.Fatal("TimeZone leaked to another client")
		}
		if app := queryString(t, plain, "SHOW application_name"); app != "plain" {
			t.Fatalf("application_name = %q", app)
		}
		queryString(t, ny, "SHOW TimeZone")
	}
}

func TestSessionModeReset(t *testing.T) {
	a, _ := startPgmux(t, options{mode: "session", poolSize: 1})
	c1, err := pgx.Connect(context.Background(), appDSN(a))
	if err != nil {
		t.Fatal(err)
	}
	pid := queryInt(t, c1, "SELECT pg_backend_pid()")
	mustExec(t, c1, "SET search_path = foo")
	mustExec(t, c1, "PREPARE p AS SELECT 1")
	c1.Close(context.Background())

	c2 := connect(t, appDSN(a))
	if got := queryInt(t, c2, "SELECT pg_backend_pid()"); got != pid {
		t.Fatalf("second client got a new server (pid %d, want %d)", got, pid)
	}
	if sp := queryString(t, c2, "SHOW search_path"); sp == "foo" {
		t.Fatal("search_path leaked between session clients")
	}
	if _, err := c2.Exec(context.Background(), "EXECUTE p"); err == nil {
		t.Fatal("prepared statement leaked between session clients")
	}
}

func TestHotReload(t *testing.T) {
	a, path := startPgmux(t, options{poolSize: 3})
	c := connect(t, appDSN(a))
	queryInt(t, c, "SELECT 1")
	data, _ := os.ReadFile(path)
	updated := strings.Replace(string(data), "default_size: 3", "default_size: 7", 1)
	if err := os.WriteFile(path, []byte(updated), 0o600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if p := a.Manager.Pools(); len(p) > 0 && p[0].Stats().MaxSize == 7 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("pool size did not change after the config file changed")
}

func TestPauseResume(t *testing.T) {
	a, _ := startPgmux(t, options{})
	c := connect(t, appDSN(a))
	queryInt(t, c, "SELECT 1")
	base := "http://" + a.AdminAddr()
	post := func(path string) {
		resp, err := http.Post(base+path, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("%s: %d %s", path, resp.StatusCode, body)
		}
	}
	post("/pause?timeout=2s")
	done := make(chan error, 1)
	go func() {
		_, err := c.Exec(context.Background(), "SELECT 1")
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("query ran while paused (err %v)", err)
	case <-time.After(300 * time.Millisecond):
	}
	post("/resume")
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("query did not run after resume")
	}
	resp, err := http.Get(base + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	for _, m := range []string{"pgmux_queries_total", "pgmux_acquire_wait_seconds", "pgmux_servers_idle"} {
		if !bytes.Contains(body, []byte(m)) {
			t.Errorf("metrics output has no %s", m)
		}
	}
}

func TestGracefulShutdown(t *testing.T) {
	a, _ := startPgmux(t, options{})
	ctx := context.Background()
	c, err := pgx.Connect(ctx, appDSN(a))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	tx, err := c.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "SELECT 1"); err != nil {
		t.Fatal(err)
	}
	stopped := make(chan struct{})
	go func() {
		a.Shutdown(5 * time.Second)
		close(stopped)
	}()
	time.Sleep(200 * time.Millisecond)
	// The open transaction must be able to finish during the drain.
	if _, err := tx.Exec(ctx, "SELECT 2"); err != nil {
		t.Fatalf("query during drain: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit during drain: %v", err)
	}
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not finish after the transaction ended")
	}
	if _, err := c.Exec(ctx, "SELECT 3"); err == nil {
		t.Fatal("client still works after shutdown")
	}
}
