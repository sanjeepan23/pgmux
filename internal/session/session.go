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

var paramNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]*$`)

// syncParamsLocked queues SET commands so that the server parameters match what the client expects.
func (s *Session) syncParamsLocked(c *pool.Conn) {
	var sets []string
	names := make([]string, 0, len(s.params))
	for name := range s.params {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		want := s.params[name]
		if have, ok := c.Param(name); ok && have == want {
			continue
		}
		if !paramNameRe.MatchString(name) {
			continue
		}
		sets = append(sets, fmt.Sprintf("SET %s = %s", name, quoteLiteral(want)))
		// Parameters that the server does not report keep this value until the next reset.
		c.SetParam(name, want)
	}
	if len(sets) == 0 {
		return
	}
	_ = c.WriteRaw(protocol.MustEncode(&pgproto3.Query{String: strings.Join(sets, "; ")}))
	s.queue = append(s.queue, pending{kind: kSync, swallow: true})
}

func quoteLiteral(v string) string {
	q := "'" + strings.ReplaceAll(v, "'", "''") + "'"
	if strings.Contains(v, `\`) {
		return "E" + strings.ReplaceAll(q, `\`, `\\`)
	}
	return q
}

// relay forwards server messages to the client until the server is released, lost or stopped.
func (s *Session) relay(c *pool.Conn, stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	msgs := c.Msgs()
	for {
		var out []protocol.Message
		var rfq bool
		select {
		case <-stop:
			return
		case <-s.poke:
			s.mu.Lock()
			out, rfq = s.takeSynthLocked(nil)
		case m, ok := <-msgs:
			if !ok {
				s.serverLost(c)
				return
			}
			s.mu.Lock()
			out, rfq = s.processLocked(c, m)
		}
		detach := rfq && s.canDetachLocked()
		if detach {
			s.backend = nil
			s.lastUsed = time.Now()
		}
		s.mu.Unlock()

		for _, m := range out {
			if err := s.wr.WriteRaw(m); err != nil {
				s.clientLost()
				return
			}
		}
		if detach || len(msgs) == 0 {
			if err := s.wr.Flush(); err != nil {
				s.clientLost()
				return
			}
		}
		if detach {
			s.pool.Release(c)
			return
		}
	}
}

func (s *Session) canDetachLocked() bool {
	return s.txMode && len(s.queue) == 0 && !s.inBatch && s.txStatus == 'I'
}

func (s *Session) popLocked() pending {
	p := s.queue[0]
	s.queue[0] = pending{}
	s.queue = s.queue[1:]
	return p
}

// takeSynthLocked appends the made-up responses at the head of the queue to out.
func (s *Session) takeSynthLocked(out []protocol.Message) ([]protocol.Message, bool) {
	rfq := false
	for len(s.queue) > 0 && s.queue[0].kind == kSynth {
		p := s.popLocked()
		out = append(out, p.msgs...)
		if p.rfq {
			out = append(out, protocol.ReadyForQuery(s.txStatus))
			rfq = true
		}
	}
	return out, rfq
}

// processLocked matches one server message to the request queue.
// It returns the messages to send to the client, and reports if a ReadyForQuery went out.
func (s *Session) processLocked(c *pool.Conn, m protocol.Message) ([]protocol.Message, bool) {
	out, rfq := s.takeSynthLocked(nil)
	var head *pending
	if len(s.queue) > 0 {
		head = &s.queue[0]
	}
	swallowing := head != nil && head.swallow && head.kind == kSync

	switch m.Type {
	case 'A': // NotificationResponse
		out = append(out, m)
	case 'Z': // ReadyForQuery
		if len(m.Body) == 1 {
			s.txStatus = m.Body[0]
		}
		fromClient := true
		for len(s.queue) > 0 {
			p := s.popLocked()
			if p.kind == kSync {
				fromClient = !p.swallow
				break
			}
		}
		if fromClient {
			out = append(out, m)
			if s.txStatus == 'I' {
				s.transactions.Inc()
			}
		}
		rfq = true
	case 'E': // ErrorResponse
		switch {
		case head != nil && head.kind != kSync && head.kind != kSynth:
			// After an error in an extended batch, the server skips all messages up to the next Sync.
			for len(s.queue) > 0 && s.queue[0].kind != kSync {
				if p := s.popLocked(); p.kind == kParse && p.stmt != "" {
					delete(c.Prepared, p.stmt)
				}
			}
			out = append(out, m)
		case swallowing:
			s.log.Warn("parameter sync failed", "err", protocol.DecodeError(m.Body))
		default:
			out = append(out, m)
		}
	case 'S': // ParameterStatus
		if !swallowing {
			s.trackParamLocked(m)
			out = append(out, m)
		}
	default:
		if swallowing {
			break
		}
		if head != nil && completes(head.kind, m.Type) {
			if p := s.popLocked(); p.swallow {
				break
			}
		}
		out = append(out, m)
	}
	out, synthRFQ := s.takeSynthLocked(out)
	return out, rfq || synthRFQ
}

// completes reports if a server message of type msgType is the last response to a request of kind k.
func completes(k kind, msgType byte) bool {
	switch k {
	case kParse:
		return msgType == '1'
	case kBind:
		return msgType == '2'
	case kClose:
		return msgType == '3'
	case kDescribe:
		return msgType == 'T' || msgType == 'n'
	case kExecute:
		return msgType == 'C' || msgType == 'I' || msgType == 's'
	}
	return false
}

// trackParamLocked records a parameter change, so that the next server gets the same value.
func (s *Session) trackParamLocked(m protocol.Message) {
	var ps pgproto3.ParameterStatus
	if ps.Decode(m.Body) != nil {
		return
	}
	name := strings.ToLower(ps.Name)
	if _, ok := s.params[name]; ok || trackedParams[name] {
		s.params[name] = ps.Value
	}
}

// serverLost ends the session after the server connection fails.
func (s *Session) serverLost(c *pool.Conn) {
	s.log.Warn("server connection lost", "err", c.ReadErr())
	metrics.ErrorsTotal.WithLabelValues(metrics.ErrServerLost).Inc()
	s.mu.Lock()
	s.backend = nil
	s.mu.Unlock()
	s.pool.Destroy(c)
	_ = s.wr.WriteRaw(protocol.ErrorMessage("FATAL", "08006", "pgmux: server connection lost"))
	_ = s.wr.Flush()
	_ = s.conn.Close()
}

// clientLost closes the client connection, so that the client goroutine stops and cleans up.
func (s *Session) clientLost() { _ = s.conn.Close() }

// stopRelay stops the relay and waits for it to exit.
func (s *Session) stopRelay() {
	s.mu.Lock()
	stop, done := s.relayStop, s.relayDone
	s.relayStop = nil
	s.mu.Unlock()
	if stop != nil {
		close(stop)
	}
	<-done
}

// finish returns or closes the attached server after the client leaves.
func (s *Session) finish() {
	s.stopRelay()
	s.mu.Lock()
	c := s.backend
	idle := len(s.queue) == 0 && !s.inBatch && s.txStatus == 'I'
	s.backend = nil
	s.mu.Unlock()
	if c == nil {
		return
	}
	if !idle {
		// The client left in the middle of a transaction or request. Closing the server rolls it back.
		s.pool.Destroy(c)
		return
	}
	if !s.txMode {
		if q := s.pool.Settings().ResetQuery; q != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			err := c.Exec(ctx, q)
			cancel()
			if err != nil {
				s.log.Warn("reset query failed", "err", err)
				s.pool.Destroy(c)
				return
			}
			c.ResetState()
		}
	}
	s.pool.Release(c)
}

// closeIfIdle closes the client if no server is attached. It reports if it closed the client.
func (s *Session) closeIfIdle() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.backend != nil || len(s.queue) > 0 || s.inBatch {
		return false
	}
	select {
	case <-s.relayDone:
	default:
		return false
	}
	_ = s.conn.SetWriteDeadline(time.Now().Add(time.Second))
	_ = s.wr.WriteRaw(protocol.ErrorMessage("FATAL", "57P01", "pgmux is shutting down"))
	_ = s.wr.Flush()
	_ = s.conn.Close()
	return true
}

// cancel forwards a cancel request to the server that serves this client now.
func (s *Session) cancel() {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Holding the lock keeps the server attached to this client while the cancel goes out.
	if s.backend == nil || len(s.queue) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.srv.Manager.Dialer().Cancel(ctx, s.backend.PID, s.backend.Secret); err != nil {
		metrics.ErrorsTotal.WithLabelValues(metrics.ErrCancel).Inc()
		s.log.Warn("cancel request failed", "err", err)
	}
}

func (s *Session) checkFeatures(sql string) {
	for _, f := range sessionFeatures(sql) {
		metrics.SessionFeatureWarnings.WithLabelValues(f).Inc()
		if !s.warned[f] {
			s.warned[f] = true
			s.log.Warn("session-level feature used in transaction pooling mode", "feature", f)
		}
	}
}

func cstr(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}
