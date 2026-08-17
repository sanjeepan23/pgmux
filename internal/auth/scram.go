package auth

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const (
	scramMechanism = "SCRAM-SHA-256"
	// defaultIterations matches the Postgres default for scram_iterations.
	defaultIterations = 4096
)

// ScramSecret holds the values that Postgres stores for a SCRAM-SHA-256 password.
type ScramSecret struct {
	Iterations int
	Salt       []byte
	StoredKey  []byte
	ServerKey  []byte
}

// ParseScramSecret parses the format "SCRAM-SHA-256$<iter>:<salt>$<StoredKey>:<ServerKey>".
func ParseScramSecret(s string) (*ScramSecret, error) {
	rest, ok := strings.CutPrefix(s, scramMechanism+"$")
	if !ok {
		return nil, errors.New("auth: not a SCRAM-SHA-256 secret")
	}
	iterSalt, keys, ok := strings.Cut(rest, "$")
	if !ok {
		return nil, errors.New("auth: malformed SCRAM secret")
	}
	iterStr, saltStr, ok1 := strings.Cut(iterSalt, ":")
	storedStr, serverStr, ok2 := strings.Cut(keys, ":")
	if !ok1 || !ok2 {
		return nil, errors.New("auth: malformed SCRAM secret")
	}
	iter, err := strconv.Atoi(iterStr)
	if err != nil || iter <= 0 {
		return nil, errors.New("auth: bad SCRAM iteration count")
	}
	sec := &ScramSecret{Iterations: iter}
	for _, f := range []struct {
		dst *[]byte
		src string
	}{{&sec.Salt, saltStr}, {&sec.StoredKey, storedStr}, {&sec.ServerKey, serverStr}} {
		if *f.dst, err = base64.StdEncoding.DecodeString(f.src); err != nil {
			return nil, fmt.Errorf("auth: bad SCRAM secret encoding: %w", err)
		}
	}
	if len(sec.StoredKey) != sha256.Size || len(sec.ServerKey) != sha256.Size {
		return nil, errors.New("auth: bad SCRAM key length")
	}
	return sec, nil
}

