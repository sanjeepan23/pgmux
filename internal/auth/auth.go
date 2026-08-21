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

