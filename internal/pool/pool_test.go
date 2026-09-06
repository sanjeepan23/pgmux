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

func settings(size int) config.PoolSettings {
	return config.PoolSettings{ServerDB: "db", Mode: config.ModeTransaction, MaxSize: size, QueryWaitTimeout: 5 * time.Second}
}

func newTestPool(t *testing.T, s *fakeServer, st config.PoolSettings) *Pool {
	p := newPool(Key{User: "u", Database: "db"}, st, Login{User: "u"}, s.dialer(t))
	t.Cleanup(p.close)
	return p
}

func TestPoolRespectsMaxSize(t *testing.T) {
	srv := newFakeServer(t)
	p := newTestPool(t, srv, settings(4))
	var held, maxHeld atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				c, err := p.Acquire(context.Background())
				if err != nil {
					t.Error(err)
					return
				}
				n := held.Add(1)
				for {
					m := maxHeld.Load()
					if n <= m || maxHeld.CompareAndSwap(m, n) {
						break
					}
				}
				time.Sleep(100 * time.Microsecond)
				held.Add(-1)
				p.Release(c)
			}
		}()
	}
	wg.Wait()
	if maxHeld.Load() > 4 || srv.dials.Load() > 4 {
		t.Fatalf("held %d at once, dialed %d, max is 4", maxHeld.Load(), srv.dials.Load())
	}
	if s := p.Stats(); s.ServersActive != 0 || s.ServersIdle != s.ServersTotal {
		t.Fatalf("pool not balanced after test: %+v", s)
	}
}

func TestPoolWaitTimeout(t *testing.T) {
	srv := newFakeServer(t)
	st := settings(1)
	st.QueryWaitTimeout = 50 * time.Millisecond
	p := newTestPool(t, srv, st)
	c, err := p.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Acquire(context.Background()); !errors.Is(err, ErrWaitTimeout) {
		t.Fatalf("err = %v, want ErrWaitTimeout", err)
	}
	p.Release(c)
	c2, err := p.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if c2 != c {
		t.Fatal("idle connection was not reused")
	}
	p.Release(c2)
	if s := p.Stats(); s.ServersTotal != 1 || s.ClientsWaiting != 0 {
		t.Fatalf("stats = %+v", s)
	}
}

func TestPoolHandsOffInOrder(t *testing.T) {
	srv := newFakeServer(t)
	p := newTestPool(t, srv, settings(1))
	c, _ := p.Acquire(context.Background())
	order := make(chan int, 3)
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got, err := p.Acquire(context.Background())
			if err != nil {
				t.Error(err)
				return
			}
			order <- i
			p.Release(got)
		}(i)
		// Wait until the goroutine is in the queue, so the arrival order is fixed.
		for p.Stats().ClientsWaiting != i+1 {
			time.Sleep(time.Millisecond)
		}
	}
	p.Release(c)
	wg.Wait()
	close(order)
	want := 0
	for got := range order {
		if got != want {
			t.Fatalf("waiter %d served before waiter %d", got, want)
		}
		want++
	}
}

func TestPoolDestroyLetsWaiterDial(t *testing.T) {
	srv := newFakeServer(t)
	p := newTestPool(t, srv, settings(1))
	c, _ := p.Acquire(context.Background())
	got := make(chan *Conn)
	go func() {
		c2, err := p.Acquire(context.Background())
		if err != nil {
			t.Error(err)
		}
		got <- c2
	}()
	for p.Stats().ClientsWaiting != 1 {
		time.Sleep(time.Millisecond)
	}
	p.Destroy(c)
	c2 := <-got
	if c2 == nil || c2 == c {
		t.Fatal("waiter did not get a new connection")
	}
	p.Release(c2)
	if srv.dials.Load() != 2 {
		t.Fatalf("dials = %d, want 2", srv.dials.Load())
	}
}

func TestPoolPauseResume(t *testing.T) {
	srv := newFakeServer(t)
	p := newTestPool(t, srv, settings(2))
	c, _ := p.Acquire(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := p.Pause(ctx); err == nil {
		t.Fatal("Pause returned before the held connection came back")
	}
	p.Release(c)
	if err := p.Pause(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := make(chan error, 1)
	go func() {
		c, err := p.Acquire(context.Background())
		if err == nil {
			p.Release(c)
		}
		got <- err
	}()
	select {
	case <-got:
		t.Fatal("Acquire succeeded while paused")
	case <-time.After(30 * time.Millisecond):
	}
	p.Resume()
	if err := <-got; err != nil {
		t.Fatal(err)
	}
}

func TestPoolResizeAndMaintain(t *testing.T) {
	srv := newFakeServer(t)
	st := settings(3)
	p := newTestPool(t, srv, st)
	var cs []*Conn
	for i := 0; i < 3; i++ {
		c, _ := p.Acquire(context.Background())
		cs = append(cs, c)
	}
	for _, c := range cs {
		p.Release(c)
	}
	st.MaxSize = 1
	p.Resize(st)
	if s := p.Stats(); s.ServersTotal != 1 {
		t.Fatalf("after shrink: %+v", s)
	}

	st.MaxSize, st.MinSize, st.IdleTimeout = 3, 2, time.Millisecond
	p.Resize(st)
	p.maintain(context.Background())
	deadline := time.Now().Add(2 * time.Second)
	for p.Stats().ServersIdle != 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if s := p.Stats(); s.ServersTotal != 2 {
		t.Fatalf("min_size not kept: %+v", s)
	}
	time.Sleep(5 * time.Millisecond)
	p.maintain(context.Background())
	if s := p.Stats(); s.ServersTotal != 2 {
		t.Fatalf("idle_timeout closed connections below min_size: %+v", s)
	}
}

