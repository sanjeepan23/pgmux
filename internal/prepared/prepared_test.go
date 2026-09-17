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

func TestNewStatementRenames(t *testing.T) {
	st, err := NewStatement(&pgproto3.Parse{Name: "client_name", Query: "SELECT 1", ParameterOIDs: []uint32{}})
	if err != nil {
		t.Fatal(err)
	}
	var p pgproto3.Parse
	if err := p.Decode(st.Parse.Body); err != nil {
		t.Fatal(err)
	}
	if p.Name != st.ServerName || p.Query != "SELECT 1" {
		t.Fatalf("renamed parse = %+v", p)
	}
}

func TestRenameBind(t *testing.T) {
	orig := &pgproto3.Bind{DestinationPortal: "p1", PreparedStatement: "s1", Parameters: [][]byte{[]byte("x"), nil}, ResultFormatCodes: []int16{1}}
	b, _ := orig.Encode(nil)
	body := b[5:]
	if name, err := BindStatement(body); err != nil || name != "s1" {
		t.Fatalf("BindStatement = %q, %v", name, err)
	}
	renamed, err := RenameBind(body, "pgmux_abc")
	if err != nil {
		t.Fatal(err)
	}
	var got pgproto3.Bind
	if err := got.Decode(renamed); err != nil {
		t.Fatal(err)
	}
	if got.PreparedStatement != "pgmux_abc" || got.DestinationPortal != "p1" || string(got.Parameters[0]) != "x" || got.Parameters[1] != nil {
		t.Fatalf("renamed bind = %+v", got)
	}
}

