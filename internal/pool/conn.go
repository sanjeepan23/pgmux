package pool

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"

	"github.com/sanjeepan23/pgmux/internal/protocol"
)

var connIDs atomic.Uint64

// ErrConnClosed means the backend connection is gone.
var ErrConnClosed = errors.New("server connection closed")

// Conn is one connection to Postgres.
// A goroutine reads server messages into Msgs for the whole life of the connection.
type Conn struct {
	ID     uint64
	PID    uint32
	Secret []byte

	netConn net.Conn
	w       *protocol.Writer
	msgs    chan protocol.Message
	closed  chan struct{}
	once    sync.Once
	readErr error

	// params is keyed by lower-case name, because Postgres parameter names are not case-sensitive.
	mu            sync.Mutex
	params        map[string]string
	names         map[string]string // lower-case name to the name that the server reported
	initialParams map[string]string

	// Prepared holds the server-side names of statements prepared on this connection.
	// Only the current owner of the connection touches it.
	Prepared map[string]struct{}

	createdAt time.Time
	lastUsed  time.Time
	broken    atomic.Bool
}

