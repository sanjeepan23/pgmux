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

