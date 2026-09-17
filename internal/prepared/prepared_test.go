package prepared

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgproto3"
)

func TestServerName(t *testing.T) {
	a := ServerName("SELECT $1", []uint32{23})
	if a != ServerName("SELECT $1", []uint32{23}) {
		t.Fatal("name is not stable")
	}
	if a == ServerName("SELECT $1", []uint32{25}) || a == ServerName("SELECT $2", []uint32{23}) {
		t.Fatal("different statements share a name")
	}
	if !strings.HasPrefix(a, Prefix) || len(a) > 63 {
		t.Fatalf("bad name %q", a)
	}
}

