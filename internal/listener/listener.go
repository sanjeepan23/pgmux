// Package listener runs the TCP accept loop and graceful shutdown.
package listener

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"sync"
	"time"
)

// Handler serves one client connection. It must close the connection before it returns.
type Handler func(ctx context.Context, conn net.Conn)

// Listener accepts client connections and runs a goroutine for each one.
type Listener struct {
	ln      net.Listener
	handler Handler
	wg      sync.WaitGroup
}

// Listen opens a TCP listener on addr.
func Listen(addr string, h Handler) (*Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	return &Listener{ln: ln, handler: h}, nil
}

// Addr returns the listen address.
func (l *Listener) Addr() net.Addr { return l.ln.Addr() }

// Serve accepts connections until Close. It returns nil after Close.
func (l *Listener) Serve(ctx context.Context) error {
	var backoff time.Duration
	for {
		conn, err := l.ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			// Back off on errors such as "too many open files", so the loop does not spin.
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() || isTemporary(err) {
				backoff = min(max(backoff*2, 5*time.Millisecond), time.Second)
				slog.Warn("accept failed", "err", err, "retry_in", backoff)
				time.Sleep(backoff)
				continue
			}
			return err
		}
		backoff = 0
		if tc, ok := conn.(*net.TCPConn); ok {
			_ = tc.SetNoDelay(true)
			_ = tc.SetKeepAlive(true)
			_ = tc.SetKeepAlivePeriod(30 * time.Second)
		}
		l.wg.Add(1)
		go func() {
			defer l.wg.Done()
			l.handler(ctx, conn)
		}()
	}
}

