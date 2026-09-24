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

func main() {
	configPath := flag.String("config", "pgmux.yaml", "path to the YAML config file")
	logLevel := flag.String("log-level", "info", "log level: debug, info, warn or error")
	logFormat := flag.String("log-format", "text", "log format: text or json")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("pgmux", version)
		return
	}
	setupLogging(*logLevel, *logFormat)
	if err := run(*configPath); err != nil {
		slog.Error("pgmux failed", "err", err)
		os.Exit(1)
	}
}

func setupLogging(level, format string) {
	var lv slog.Level
	if err := lv.UnmarshalText([]byte(level)); err != nil {
		lv = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: lv}
	var h slog.Handler = slog.NewTextHandler(os.Stderr, opts)
	if format == "json" {
		h = slog.NewJSONHandler(os.Stderr, opts)
	}
	slog.SetDefault(slog.New(h))
}

func run(configPath string) error {
	a, err := app.Start(configPath, version)
	if err != nil {
		return err
	}
	cfg := a.Config()
	slog.Info("pgmux listening", "addr", a.Addr(), "admin", a.AdminAddr(), "version", version, "pool_mode", cfg.PoolMode)

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	for {
		select {
		case err := <-a.Err():
			return err
		case sig := <-sigs:
			if sig == syscall.SIGHUP {
				_ = a.Reload()
				continue
			}
			slog.Info("shutting down", "signal", sig.String(), "timeout", cfg.ShutdownTimeout.String())
			a.Shutdown(cfg.ShutdownTimeout.Duration)
			return nil
		}
	}
}
