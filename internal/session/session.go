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

