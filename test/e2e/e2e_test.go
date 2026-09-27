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

