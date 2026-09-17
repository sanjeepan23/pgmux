// Package prepared maps client prepared-statement names to shared server-side names for transaction mode.
package prepared

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"

	"github.com/jackc/pgx/v5/pgproto3"

	"github.com/sanjeepan23/pgmux/internal/protocol"
)

// Prefix starts every server-side statement name that pgmux creates.
const Prefix = "pgmux_"

// Statement is one named statement that a client prepared.
type Statement struct {
	ServerName string
	// Parse is the client's Parse message, renamed to ServerName. pgmux sends it again on other servers.
	Parse protocol.Message
}

// ServerName returns a name derived from the query text and parameter types.
// Clients that prepare the same query share one statement on each server.
func ServerName(query string, paramOIDs []uint32) string {
	h := sha256.New()
	h.Write([]byte(query))
	var b [4]byte
	for _, oid := range paramOIDs {
		binary.BigEndian.PutUint32(b[:], oid)
		h.Write(b[:])
	}
	return Prefix + hex.EncodeToString(h.Sum(nil)[:12])
}

// NewStatement builds a Statement from a decoded client Parse message.
func NewStatement(p *pgproto3.Parse) (*Statement, error) {
	name := ServerName(p.Query, p.ParameterOIDs)
	renamed := *p
	renamed.Name = name
	msg, err := protocol.Encode(&renamed)
	if err != nil {
		return nil, err
	}
	return &Statement{ServerName: name, Parse: msg}, nil
}

var errMalformed = errors.New("prepared: malformed message")

// cstring returns the NUL-terminated string at the start of b and the rest after the NUL.
func cstring(b []byte) (string, []byte, error) {
	i := bytes.IndexByte(b, 0)
	if i < 0 {
		return "", nil, errMalformed
	}
	return string(b[:i]), b[i+1:], nil
}

// BindStatement returns the statement name in a Bind body.
func BindStatement(body []byte) (string, error) {
	_, rest, err := cstring(body)
	if err != nil {
		return "", err
	}
	name, _, err := cstring(rest)
	return name, err
}

// RenameBind returns a copy of a Bind body with a new statement name.
func RenameBind(body []byte, stmt string) ([]byte, error) {
	portal, rest, err := cstring(body)
	if err != nil {
		return nil, err
	}
	_, rest, err = cstring(rest)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(portal)+len(stmt)+2+len(rest))
	out = append(out, portal...)
	out = append(out, 0)
	out = append(out, stmt...)
	out = append(out, 0)
	return append(out, rest...), nil
}

// Target returns the object type ('S' or 'P') and name in a Describe or Close body.
func Target(body []byte) (byte, string, error) {
	if len(body) < 2 {
		return 0, "", errMalformed
	}
	name, _, err := cstring(body[1:])
	return body[0], name, err
}

// RenameTarget returns a Describe or Close body for the given object type and name.
func RenameTarget(objType byte, name string) []byte {
	out := make([]byte, 0, len(name)+2)
	out = append(out, objType)
	out = append(out, name...)
	return append(out, 0)
}
