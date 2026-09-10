package session

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/sanjeepan23/pgmux/internal/metrics"
	"github.com/sanjeepan23/pgmux/internal/pool"
	"github.com/sanjeepan23/pgmux/internal/prepared"
	"github.com/sanjeepan23/pgmux/internal/protocol"
)

// kind is the type of a request that waits for a server response.
type kind uint8

const (
	kParse kind = iota
	kBind
	kDescribe
	kExecute
	kClose
	// kSync covers Sync, simple Query and FunctionCall. Each one ends with ReadyForQuery.
	kSync
	// kSynth holds responses that pgmux makes itself. They go to the client in queue order.
	kSynth
)

// pending is one request that is waiting for its response.
type pending struct {
	kind kind
	// swallow means pgmux sent the request itself, so the client must not see the response.
	swallow bool
	stmt    string // server statement name, for kParse
	msgs    []protocol.Message
	rfq     bool // for kSynth: also send ReadyForQuery with the current status
}

var errTerminate = errors.New("client terminated")

// Session is one client connection after login.
//
// The client goroutine reads client messages and forwards them to the attached server.
// A relay goroutine forwards server messages to the client while a server is attached.
type Session struct {
	srv    *Server
	id     uint64
	ctx    context.Context
	conn   net.Conn
	rd     *protocol.Reader
	wr     *protocol.Writer
	pool   *pool.Pool
	txMode bool
	key    cancelKey
	user   string
	db     string
	log    *slog.Logger

	queries      prometheus.Counter
	transactions prometheus.Counter

	// Only the client goroutine uses these fields.
	stmts  map[string]*prepared.Statement
	warned map[string]bool
	// deferred means pgmux answered a Parse or Close without a server, and still owes the client a ReadyForQuery at Sync.
	deferred bool

	mu        sync.Mutex
	backend   *pool.Conn
	queue     []pending
	inBatch   bool // the client sent extended-protocol messages after its last Sync
	txStatus  byte
	params    map[string]string // parameter values that the client expects, keyed by lower-case name
	relayStop chan struct{}
	relayDone chan struct{}
	poke      chan struct{}
	lastUsed  time.Time
}

func (s *Session) run() error {
	for {
		m, err := s.rd.ReadMessage()
		if err != nil {
			if errors.Is(err, protocol.ErrMessageTooLong) {
				metrics.ErrorsTotal.WithLabelValues(metrics.ErrClientProtocol).Inc()
			}
			return err
		}
		if err := s.handle(m); err != nil {
			return err
		}
	}
}

func (s *Session) handle(m protocol.Message) error {
	switch m.Type {
	case 'Q':
		return s.handleQuery(m)
	case 'P':
		return s.handleParse(m)
	case 'B':
		return s.handleBind(m)
	case 'D':
		return s.handleDescribe(m)
	case 'C':
		return s.handleClose(m)
	case 'E':
		s.queries.Inc()
		return s.forward(m, pending{kind: kExecute}, true)
	case 'S':
		if s.deferred && s.detached() {
			s.deferred = false
			return s.writeClient(protocol.ReadyForQuery('I'))
		}
		c, err := s.lockBackend()
		if err != nil {
			return err
		}
		s.queue = append(s.queue, pending{kind: kSync})
		s.inBatch = false
		s.mu.Unlock()
		return s.send(c, m)
	case 'F':
		return s.forward(m, pending{kind: kSync}, false)
	case 'H', 'd', 'c', 'f':
		// Flush and COPY data only make sense while a request is in flight.
		// The in-flight request keeps the server attached, so the write is safe.
		s.mu.Lock()
		c := s.backend
		busy := len(s.queue) > 0 || s.inBatch
		s.mu.Unlock()
		if c == nil || !busy {
			return nil
		}
		return s.send(c, m)
	case 'X':
		return errTerminate
	default:
		metrics.ErrorsTotal.WithLabelValues(metrics.ErrClientProtocol).Inc()
		return s.fatal("08P01", fmt.Sprintf("unsupported frontend message type %q", m.Type))
	}
}

// forward sends m to the server and queues the expected response.
func (s *Session) forward(m protocol.Message, p pending, extended bool) error {
	c, err := s.lockBackend()
	if err != nil {
		return err
	}
	s.queue = append(s.queue, p)
	if extended {
		s.inBatch = true
	}
	s.mu.Unlock()
	return s.send(c, m)
}

func (s *Session) handleQuery(m protocol.Message) error {
	sql := cstr(m.Body)
	if s.txMode {
		s.checkFeatures(sql)
		if isListen(sql) {
			return s.reject("0A000", "LISTEN is not supported in transaction pooling mode, use a session-mode pool")
		}
	}
	s.queries.Inc()
	c, err := s.lockBackend()
	if err != nil {
		return err
	}
	s.queue = append(s.queue, pending{kind: kSync})
	if s.txMode && resetsState(sql) {
		c.ResetState()
	}
	s.mu.Unlock()
	return s.send(c, m)
}

func (s *Session) handleParse(m protocol.Message) error {
	if !s.txMode {
		return s.forward(m, pending{kind: kParse}, true)
	}
	var p pgproto3.Parse
	if err := p.Decode(m.Body); err != nil {
		return s.fatal("08P01", "invalid Parse message")
	}
	s.checkFeatures(p.Query)
	if p.Name == "" {
		return s.forward(m, pending{kind: kParse}, true)
	}
	st, err := prepared.NewStatement(&p)
	if err != nil {
		return err
	}
	s.stmts[p.Name] = st
	if s.detached() {
		// Do not take a server just to prepare. pgmux sends the Parse before the first Bind or Describe.
		// A client that prepares synchronously while it holds other connections can then not deadlock the pool.
		s.deferred = true
		return s.writeClient(protocol.Message{Type: '1'})
	}
	c, err := s.lockBackend()
	if err != nil {
		return err
	}
	s.inBatch = true
	if _, ok := c.Prepared[st.ServerName]; ok {
		// The server already has this statement, so answer the Parse ourselves.
		s.queue = append(s.queue, pending{kind: kSynth, msgs: []protocol.Message{{Type: '1'}}})
		s.mu.Unlock()
		s.wake()
		return s.send(c)
	}
	c.Prepared[st.ServerName] = struct{}{}
	s.queue = append(s.queue, pending{kind: kParse, stmt: st.ServerName})
	s.mu.Unlock()
	return s.send(c, st.Parse)
}

// ensurePreparedLocked returns a Parse to send first if the attached server lacks the statement.
func (s *Session) ensurePreparedLocked(c *pool.Conn, st *prepared.Statement) []protocol.Message {
	if _, ok := c.Prepared[st.ServerName]; ok {
		return nil
	}
	c.Prepared[st.ServerName] = struct{}{}
	s.queue = append(s.queue, pending{kind: kParse, stmt: st.ServerName, swallow: true})
	return []protocol.Message{st.Parse}
}

func (s *Session) handleBind(m protocol.Message) error {
	if !s.txMode {
		return s.forward(m, pending{kind: kBind}, true)
	}
	name, err := prepared.BindStatement(m.Body)
	if err != nil {
		return s.fatal("08P01", "invalid Bind message")
	}
	st := s.stmts[name]
	if name == "" || st == nil {
		return s.forward(m, pending{kind: kBind}, true)
	}
	body, err := prepared.RenameBind(m.Body, st.ServerName)
	if err != nil {
		return s.fatal("08P01", "invalid Bind message")
	}
	c, err := s.lockBackend()
	if err != nil {
		return err
	}
	out := s.ensurePreparedLocked(c, st)
	s.queue = append(s.queue, pending{kind: kBind})
	s.inBatch = true
	s.mu.Unlock()
	return s.send(c, append(out, protocol.Message{Type: 'B', Body: body})...)
}

func (s *Session) handleDescribe(m protocol.Message) error {
	objType, name, err := prepared.Target(m.Body)
	if err != nil {
		return s.fatal("08P01", "invalid Describe message")
	}
	st := s.stmts[name]
	if !s.txMode || objType != 'S' || name == "" || st == nil {
		return s.forward(m, pending{kind: kDescribe}, true)
	}
	c, err := s.lockBackend()
	if err != nil {
		return err
	}
	out := s.ensurePreparedLocked(c, st)
	s.queue = append(s.queue, pending{kind: kDescribe})
	s.inBatch = true
	s.mu.Unlock()
	return s.send(c, append(out, protocol.Message{Type: 'D', Body: prepared.RenameTarget('S', st.ServerName)})...)
}

func (s *Session) handleClose(m protocol.Message) error {
	objType, name, err := prepared.Target(m.Body)
	if err != nil {
		return s.fatal("08P01", "invalid Close message")
	}
	if !s.txMode || objType != 'S' || name == "" {
		return s.forward(m, pending{kind: kClose}, true)
	}
	// Other clients can share the server statement, so only forget the client name.
	delete(s.stmts, name)
	if s.detached() {
		s.deferred = true
		return s.writeClient(protocol.Message{Type: '3'})
	}
	c, err := s.lockBackend()
	if err != nil {
		return err
	}
	s.queue = append(s.queue, pending{kind: kSynth, msgs: []protocol.Message{{Type: '3'}}})
	s.inBatch = true
	s.mu.Unlock()
	s.wake()
	return s.send(c)
}

// reject answers a simple query with an error, without sending it to a server.
func (s *Session) reject(code, text string) error {
	resp := protocol.ErrorMessage("ERROR", code, text)
	s.mu.Lock()
	if s.backend != nil {
		s.queue = append(s.queue, pending{kind: kSynth, msgs: []protocol.Message{resp}, rfq: true})
		s.mu.Unlock()
		s.wake()
		return nil
	}
	done := s.relayDone
	s.mu.Unlock()
	<-done
	// No relay runs now, so this goroutine can write to the client.
	s.mu.Lock()
	status := s.txStatus
	s.mu.Unlock()
	if err := s.wr.WriteRaw(resp); err != nil {
		return err
	}
	if err := s.wr.WriteRaw(protocol.ReadyForQuery(status)); err != nil {
		return err
	}
	return s.wr.Flush()
}

// detached reports if no server is attached and the client is outside a transaction.
// When it returns true, no relay runs, so the client goroutine can write to the client.
func (s *Session) detached() bool {
	s.mu.Lock()
	ok := s.backend == nil && s.txStatus == 'I'
	done := s.relayDone
	s.mu.Unlock()
	if ok {
		<-done
	}
	return ok
}

// writeClient writes a response from the client goroutine. The caller must make sure that no relay runs.
func (s *Session) writeClient(m protocol.Message) error {
	if err := s.wr.WriteRaw(m); err != nil {
		return err
	}
	if s.rd.Buffered() > 0 {
		return nil
	}
	return s.wr.Flush()
}

// fatal sends a FATAL error to the client and ends the session.
func (s *Session) fatal(code, text string) error {
	s.mu.Lock()
	stop := s.backend != nil
	s.mu.Unlock()
	if stop {
		s.stopRelay()
	}
	_ = s.wr.WriteRaw(protocol.ErrorMessage("FATAL", code, text))
	_ = s.wr.Flush()
	return errors.New(text)
}

func (s *Session) wake() {
	select {
	case s.poke <- struct{}{}:
	default:
	}
}

// send writes messages to the server. It flushes when no more client input is ready, so a pipeline goes out in one write.
func (s *Session) send(c *pool.Conn, msgs ...protocol.Message) error {
	for _, m := range msgs {
		if err := c.WriteRaw(m); err != nil {
			return fmt.Errorf("write to server: %w", err)
		}
	}
	if s.rd.Buffered() > 0 {
		return nil
	}
	if err := c.Flush(); err != nil {
		return fmt.Errorf("write to server: %w", err)
	}
	return nil
}

// lockBackend returns the attached server and holds s.mu. It acquires a server first if none is attached.
// The caller must queue a request or set inBatch before it unlocks, so that the relay cannot release the server.
func (s *Session) lockBackend() (*pool.Conn, error) {
	s.mu.Lock()
	if s.backend != nil {
		return s.backend, nil
	}
	done := s.relayDone
	s.mu.Unlock()
	// The previous relay can still be writing to the client. Wait for it, to keep responses in order.
	<-done
	c, err := s.pool.Acquire(s.ctx)
	if err != nil {
		if errors.Is(err, pool.ErrWaitTimeout) {
			metrics.ErrorsTotal.WithLabelValues(metrics.ErrQueryWaitTimeout).Inc()
			return nil, s.fatal("08P01", "query_wait_timeout")
		}
		return nil, s.fatal("08006", "pgmux: cannot connect to server: "+err.Error())
	}
	s.deferred = false
	s.mu.Lock()
	s.backend = c
	s.queue = s.queue[:0]
	s.syncParamsLocked(c)
	s.relayStop = make(chan struct{})
	s.relayDone = make(chan struct{})
	go s.relay(c, s.relayStop, s.relayDone)
	return c, nil
}

