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

// MD5Hash returns the Postgres MD5 password hash for a user.
func MD5Hash(user, password string) string {
	sum := md5.Sum([]byte(password + user))
	return "md5" + hex.EncodeToString(sum[:])
}

// MD5Response returns the response to an MD5 challenge, given a stored "md5..." hash.
func MD5Response(md5Hash string, salt []byte) string {
	sum := md5.Sum(append([]byte(strings.TrimPrefix(md5Hash, "md5")), salt...))
	return "md5" + hex.EncodeToString(sum[:])
}

// scramSecret returns the stored SCRAM secret, or derives one from the plaintext password.
func (c *Credential) scramSecret() (*ScramSecret, error) {
	if c.SCRAM != nil {
		return c.SCRAM, nil
	}
	c.once.Do(func() {
		if c.Password == "" {
			c.deriveErr = errors.New("auth: no SCRAM secret or plaintext password for user")
			return
		}
		salt := make([]byte, 16)
		_, _ = rand.Read(salt)
		c.derived, c.deriveErr = NewScramSecret(c.Password, salt, defaultIterations)
	})
	return c.derived, c.deriveErr
}

// ClientKeys holds the SCRAM keys that a client proved during authentication.
// The pooler reuses them to log in to Postgres when it has no plaintext password.
type ClientKeys struct {
	ClientKey []byte
	ServerKey []byte
}

// AuthenticateClient runs the server side of client authentication.
// It does not send AuthenticationOk. It returns the client's SCRAM keys when the SCRAM method ran.
func AuthenticateClient(rd *protocol.Reader, wr *protocol.Writer, method string, cred *Credential) (*ClientKeys, error) {
	if method == MethodTrust {
		return nil, nil
	}
	if cred == nil {
		// Run the exchange with a dummy secret so that a probe cannot tell if the user exists.
		cred = &Credential{Password: "-"}
	}
	if method == MethodMD5 && cred.MD5 != "" {
		return nil, authMD5(rd, wr, cred)
	}
	if method == MethodMD5 || method == MethodSCRAM {
		return authSCRAM(rd, wr, cred)
	}
	return nil, fmt.Errorf("auth: unknown method %q", method)
}

func authMD5(rd *protocol.Reader, wr *protocol.Writer, cred *Credential) error {
	var salt [4]byte
	_, _ = rand.Read(salt[:])
	if err := wr.Write(&pgproto3.AuthenticationMD5Password{Salt: salt}); err != nil {
		return err
	}
	if err := wr.Flush(); err != nil {
		return err
	}
	body, err := readPassword(rd)
	if err != nil {
		return err
	}
	var pm pgproto3.PasswordMessage
	if err := pm.Decode(body); err != nil {
		return err
	}
	want := MD5Response(cred.MD5, salt[:])
	if cred.User == "" || subtle.ConstantTimeCompare([]byte(pm.Password), []byte(want)) != 1 {
		return ErrAuthFailed
	}
	return nil
}

