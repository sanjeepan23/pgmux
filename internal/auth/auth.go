// Package auth authenticates clients to the pooler and the pooler to Postgres.
package auth

import (
	"crypto/md5"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5/pgproto3"

	"github.com/sanjeepan23/pgmux/internal/protocol"
)

// Client authentication methods.
const (
	MethodTrust = "trust"
	MethodMD5   = "md5"
	MethodSCRAM = "scram-sha-256"
)

// ErrAuthFailed means the client gave a wrong password.
var ErrAuthFailed = errors.New("password authentication failed")

// Credential is what the pooler knows about one user's password.
// Any of the three fields can be empty, but at least one must be set.
type Credential struct {
	User     string
	Password string
	MD5      string // "md5" + hex(md5(password + user))
	SCRAM    *ScramSecret

	once      sync.Once
	derived   *ScramSecret
	deriveErr error
}

// ParseCredential classifies a stored secret as plaintext, MD5 hash or SCRAM secret.
func ParseCredential(user, secret string) (*Credential, error) {
	c := &Credential{User: user}
	switch {
	case strings.HasPrefix(secret, scramMechanism+"$"):
		s, err := ParseScramSecret(secret)
		if err != nil {
			return nil, err
		}
		c.SCRAM = s
	case len(secret) == 35 && strings.HasPrefix(secret, "md5"):
		c.MD5 = secret
	default:
		c.Password = secret
		c.MD5 = MD5Hash(user, secret)
	}
	return c, nil
}

