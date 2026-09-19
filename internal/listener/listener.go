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

