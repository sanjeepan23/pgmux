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

