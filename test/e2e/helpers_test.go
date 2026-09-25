// Package e2e runs pgmux in-process against a real Postgres server.
// Set PGMUX_TEST_PG_HOST to run it, for example after `make compose-up`.
package e2e

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sanjeepan23/pgmux/internal/app"
)

type options struct {
	mode         string
	poolSize     int
	waitTimeout  string
	authMethod   string
	tlsRequired  bool
	extraYAML    string
	resetQuery   string
	healthPeriod string
}

