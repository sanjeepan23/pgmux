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

