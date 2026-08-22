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

func newConn(nc net.Conn, rd *protocol.Reader, params map[string]string, pid uint32, secret []byte) *Conn {
	now := time.Now()
	c := &Conn{
		ID:        connIDs.Add(1),
		PID:       pid,
		Secret:    secret,
		netConn:   nc,
		w:         protocol.NewWriter(nc),
		msgs:      make(chan protocol.Message, 128),
		closed:    make(chan struct{}),
		params:    map[string]string{},
		names:     map[string]string{},
		Prepared:  map[string]struct{}{},
		createdAt: now,
		lastUsed:  now,
	}
	for k, v := range params {
		c.SetParam(k, v)
	}
	c.initialParams = copyMap(c.params)
	go c.readLoop(rd)
	return c
}

func (c *Conn) readLoop(rd *protocol.Reader) {
	defer close(c.msgs)
	for {
		m, err := rd.ReadMessage()
		if err != nil {
			c.readErr = err
			c.broken.Store(true)
			return
		}
		if m.Type == 'S' {
			var ps pgproto3.ParameterStatus
			if ps.Decode(m.Body) == nil {
				c.SetParam(ps.Name, ps.Value)
			}
		}
		select {
		case c.msgs <- m:
		case <-c.closed:
			return
		}
	}
}

