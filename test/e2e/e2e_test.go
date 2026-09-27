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

