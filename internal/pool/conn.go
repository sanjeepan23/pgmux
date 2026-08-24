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

// Msgs returns the channel of server messages. The channel closes when the connection fails.
func (c *Conn) Msgs() <-chan protocol.Message { return c.msgs }

// ReadErr returns the error that stopped the read loop, after Msgs is closed.
func (c *Conn) ReadErr() error {
	if c.readErr == nil {
		return ErrConnClosed
	}
	return c.readErr
}

// WriteRaw buffers a raw message. Call Flush to send it.
func (c *Conn) WriteRaw(m protocol.Message) error {
	if err := c.w.WriteRaw(m); err != nil {
		c.broken.Store(true)
		return err
	}
	return nil
}

// Flush sends all buffered messages.
func (c *Conn) Flush() error {
	if err := c.w.Flush(); err != nil {
		c.broken.Store(true)
		return err
	}
	return nil
}

// SetWriteDeadline sets the deadline for writes to the server.
func (c *Conn) SetWriteDeadline(t time.Time) error { return c.netConn.SetWriteDeadline(t) }

// MarkBroken stops the pool from reusing the connection.
func (c *Conn) MarkBroken() { c.broken.Store(true) }

// Broken reports if the connection must not be reused.
func (c *Conn) Broken() bool { return c.broken.Load() }

// Close closes the connection and stops the read loop.
func (c *Conn) Close() {
	c.once.Do(func() {
		c.broken.Store(true)
		close(c.closed)
		// Send Terminate so that the server does not log an unexpected EOF.
		_ = c.netConn.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
		_ = c.w.WriteRaw(protocol.Message{Type: 'X'})
		_ = c.w.Flush()
		_ = c.netConn.Close()
	})
}

// Param returns the current value of a server parameter.
func (c *Conn) Param(name string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.params[strings.ToLower(name)]
	return v, ok
}

// SetParam records the current value of a server parameter.
func (c *Conn) SetParam(name, value string) {
	key := strings.ToLower(name)
	c.mu.Lock()
	c.params[key] = value
	if _, ok := c.names[key]; !ok {
		c.names[key] = name
	}
	c.mu.Unlock()
}

// Params returns a copy of the current server parameters, with the names that the server reported.
func (c *Conn) Params() map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]string, len(c.params))
	for k, v := range c.params {
		out[c.names[k]] = v
	}
	return out
}

// ResetState records that a reset query returned the session to its startup state.
func (c *Conn) ResetState() {
	c.mu.Lock()
	c.params = copyMap(c.initialParams)
	c.mu.Unlock()
	clear(c.Prepared)
}

// DrainAsync discards queued asynchronous messages. It returns false if the connection is gone.
func (c *Conn) DrainAsync() bool {
	for {
		select {
		case _, ok := <-c.msgs:
			if !ok {
				return false
			}
		default:
			return !c.Broken()
		}
	}
}

