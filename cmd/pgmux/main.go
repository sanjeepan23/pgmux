// Command pgmux is a Postgres connection pooler.
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/sanjeepan23/pgmux/internal/app"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

