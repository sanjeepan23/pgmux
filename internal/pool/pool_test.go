package pool

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"
	"go.uber.org/goleak"

	"github.com/sanjeepan23/pgmux/internal/config"
	"github.com/sanjeepan23/pgmux/internal/protocol"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// fakeServer speaks just enough of the protocol for the pool: trust login and empty queries.
type fakeServer struct {
	ln     net.Listener
	open   atomic.Int64
	dials  atomic.Int64
	wg     sync.WaitGroup
	mu     sync.Mutex
	conns  []net.Conn
	closed bool
}

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &fakeServer{ln: ln}
	s.wg.Add(1)
	go s.accept()
	t.Cleanup(s.close)
	return s
}

func (s *fakeServer) accept() {
	defer s.wg.Done()
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			c.Close()
			return
		}
		s.conns = append(s.conns, c)
		s.mu.Unlock()
		s.dials.Add(1)
		s.open.Add(1)
		s.wg.Add(1)
		go s.serve(c)
	}
}

