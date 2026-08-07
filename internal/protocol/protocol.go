// Package protocol reads and writes raw Postgres wire messages.
// It uses pgproto3 to decode and encode the messages that the proxy must inspect or change.
package protocol

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/jackc/pgx/v5/pgproto3"
)

// Request codes that a client can send in place of a protocol version in an untyped startup packet.
const (
	ProtocolVersion30 = 196608
	SSLRequestCode    = 80877103
	GSSEncRequestCode = 80877104
	CancelRequestCode = 80877102
)

