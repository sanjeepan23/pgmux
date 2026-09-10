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

