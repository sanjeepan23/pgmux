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

