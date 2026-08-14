package config

import (
	"strings"
	"testing"
	"time"
)

func TestParseDefaults(t *testing.T) {
	cfg, err := Parse([]byte("auth:\n  userlist: users.txt\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PoolMode != ModeTransaction || cfg.Pool.DefaultSize != 20 || cfg.Pool.QueryWaitTimeout.Duration != 30*time.Second {
		t.Fatalf("defaults not applied: %+v", cfg)
	}
}

func TestPoolFor(t *testing.T) {
	cfg, err := Parse([]byte(`
pool_mode: transaction
auth: {method: trust}
pool: {default_size: 10, min_size: 2, query_wait_timeout: 5s}
databases:
  big: {pool_size: 50, pool_mode: session}
  alias: {dbname: real}
`))
	if err != nil {
		t.Fatal(err)
	}
	if s := cfg.PoolFor("other"); s.MaxSize != 10 || s.Mode != ModeTransaction || s.ServerDB != "other" || s.QueryWaitTimeout != 5*time.Second {
		t.Fatalf("defaults: %+v", s)
	}
	if s := cfg.PoolFor("big"); s.MaxSize != 50 || s.Mode != ModeSession || s.MinSize != 2 {
		t.Fatalf("override: %+v", s)
	}
	if s := cfg.PoolFor("alias"); s.ServerDB != "real" {
		t.Fatalf("alias: %+v", s)
	}
}

