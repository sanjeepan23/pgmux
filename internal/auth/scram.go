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

// String returns the secret in the Postgres storage format.
func (s *ScramSecret) String() string {
	b64 := base64.StdEncoding.EncodeToString
	return fmt.Sprintf("%s$%d:%s$%s:%s", scramMechanism, s.Iterations, b64(s.Salt), b64(s.StoredKey), b64(s.ServerKey))
}

// NewScramSecret derives a secret from a plaintext password.
func NewScramSecret(password string, salt []byte, iterations int) (*ScramSecret, error) {
	clientKey, serverKey, err := scramKeys(password, salt, iterations)
	if err != nil {
		return nil, err
	}
	stored := sha256.Sum256(clientKey)
	return &ScramSecret{Iterations: iterations, Salt: salt, StoredKey: stored[:], ServerKey: serverKey}, nil
}

func scramKeys(password string, salt []byte, iterations int) (clientKey, serverKey []byte, err error) {
	salted, err := pbkdf2.Key(sha256.New, password, salt, iterations, sha256.Size)
	if err != nil {
		return nil, nil, err
	}
	return hmacSHA256(salted, "Client Key"), hmacSHA256(salted, "Server Key"), nil
}

func hmacSHA256(key []byte, msg string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(msg))
	return h.Sum(nil)
}

func xorBytes(a, b []byte) []byte {
	out := make([]byte, len(a))
	for i := range a {
		out[i] = a[i] ^ b[i]
	}
	return out
}

func newNonce() string {
	b := make([]byte, 18)
	_, _ = rand.Read(b)
	return base64.RawStdEncoding.EncodeToString(b)
}

// parseAttrs splits "k=v,k=v" into a map. Values can contain '='.
func parseAttrs(s string) map[byte]string {
	out := map[byte]string{}
	for _, part := range strings.Split(s, ",") {
		if len(part) >= 2 && part[1] == '=' {
			out[part[0]] = part[2:]
		}
	}
	return out
}

