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

func (s *fakeServer) serve(c net.Conn) {
	defer s.wg.Done()
	defer s.open.Add(-1)
	defer c.Close()
	rd := protocol.NewReader(c, 0)
	wr := protocol.NewWriter(c)
	if _, err := rd.ReadStartup(); err != nil {
		return
	}
	_ = wr.Write(&pgproto3.AuthenticationOk{})
	_ = wr.Write(&pgproto3.ParameterStatus{Name: "server_version", Value: "16.0"})
	_ = wr.Write(&pgproto3.BackendKeyData{ProcessID: 1, SecretKey: []byte{0, 0, 0, 2}})
	_ = wr.WriteRaw(protocol.ReadyForQuery('I'))
	_ = wr.Flush()
	for {
		m, err := rd.ReadMessage()
		if err != nil || m.Type == 'X' {
			return
		}
		if m.Type == 'Q' {
			_ = wr.Write(&pgproto3.EmptyQueryResponse{})
			_ = wr.WriteRaw(protocol.ReadyForQuery('I'))
			_ = wr.Flush()
		}
	}
}

func (s *fakeServer) close() {
	s.mu.Lock()
	s.closed = true
	for _, c := range s.conns {
		c.Close()
	}
	s.mu.Unlock()
	s.ln.Close()
	s.wg.Wait()
}

func (s *fakeServer) dialer(t *testing.T) func() *Dialer {
	host, port, _ := net.SplitHostPort(s.ln.Addr().String())
	var p int
	for _, ch := range port {
		p = p*10 + int(ch-'0')
	}
	d, err := NewDialer(host, p, "disable", "", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return func() *Dialer { return d }
}

