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

// scramServer runs the server side of one SCRAM-SHA-256 exchange.
type scramServer struct {
	secret          *ScramSecret
	gs2Header       string
	clientFirstBare string
	serverFirst     string
	nonce           string
	authMessage     string
}

// firstMessage handles the client-first message and returns the server-first message.
func (s *scramServer) firstMessage(clientFirst string) (string, error) {
	switch {
	case strings.HasPrefix(clientFirst, "p="):
		return "", errors.New("SCRAM channel binding is not supported")
	case strings.HasPrefix(clientFirst, "n,"), strings.HasPrefix(clientFirst, "y,"):
	default:
		return "", errors.New("malformed SCRAM client-first message")
	}
	// The GS2 header is "n," or "y," followed by an optional authzid and a comma.
	idx := strings.Index(clientFirst[2:], ",")
	if idx < 0 {
		return "", errors.New("malformed SCRAM client-first message")
	}
	s.gs2Header = clientFirst[:2+idx+1]
	s.clientFirstBare = clientFirst[len(s.gs2Header):]
	attrs := parseAttrs(s.clientFirstBare)
	cnonce := attrs['r']
	if cnonce == "" {
		return "", errors.New("SCRAM client nonce missing")
	}
	s.nonce = cnonce + newNonce()
	s.serverFirst = fmt.Sprintf("r=%s,s=%s,i=%d", s.nonce, base64.StdEncoding.EncodeToString(s.secret.Salt), s.secret.Iterations)
	return s.serverFirst, nil
}

// finalMessage verifies the client proof. It returns the server-final message and the recovered ClientKey.
func (s *scramServer) finalMessage(clientFinal string) (string, []byte, error) {
	idx := strings.LastIndex(clientFinal, ",p=")
	if idx < 0 {
		return "", nil, errors.New("SCRAM client proof missing")
	}
	withoutProof := clientFinal[:idx]
	attrs := parseAttrs(withoutProof)
	if attrs['c'] != base64.StdEncoding.EncodeToString([]byte(s.gs2Header)) {
		return "", nil, errors.New("SCRAM channel binding mismatch")
	}
	if attrs['r'] != s.nonce {
		return "", nil, errors.New("SCRAM nonce mismatch")
	}
	proof, err := base64.StdEncoding.DecodeString(clientFinal[idx+3:])
	if err != nil || len(proof) != sha256.Size {
		return "", nil, errors.New("malformed SCRAM client proof")
	}
	s.authMessage = s.clientFirstBare + "," + s.serverFirst + "," + withoutProof
	clientSig := hmacSHA256(s.secret.StoredKey, s.authMessage)
	clientKey := xorBytes(proof, clientSig)
	stored := sha256.Sum256(clientKey)
	if subtle.ConstantTimeCompare(stored[:], s.secret.StoredKey) != 1 {
		return "", nil, ErrAuthFailed
	}
	serverSig := hmacSHA256(s.secret.ServerKey, s.authMessage)
	return "v=" + base64.StdEncoding.EncodeToString(serverSig), clientKey, nil
}

// ScramClient runs the client side of one SCRAM-SHA-256 exchange against a Postgres server.
// It authenticates with a password, or with a ClientKey and ServerKey that a client proved earlier.
type ScramClient struct {
	password        string
	clientKey       []byte
	serverKey       []byte
	cnonce          string
	clientFirstBare string
	authMessage     string
}

// NewScramClient returns a client that uses a plaintext password.
func NewScramClient(password string) *ScramClient {
	return &ScramClient{password: password, cnonce: newNonce()}
}

// NewScramClientWithKeys returns a client that uses the given SCRAM keys in place of a password.
func NewScramClientWithKeys(clientKey, serverKey []byte) *ScramClient {
	return &ScramClient{clientKey: clientKey, serverKey: serverKey, cnonce: newNonce()}
}

// Mechanism returns the SASL mechanism name.
func (c *ScramClient) Mechanism() string { return scramMechanism }

// First returns the client-first message.
func (c *ScramClient) First() []byte {
	c.clientFirstBare = "n=,r=" + c.cnonce
	return []byte("n,," + c.clientFirstBare)
}

// Final handles the server-first message and returns the client-final message.
func (c *ScramClient) Final(serverFirst []byte) ([]byte, error) {
	attrs := parseAttrs(string(serverFirst))
	nonce, saltB64, iterStr := attrs['r'], attrs['s'], attrs['i']
	if !strings.HasPrefix(nonce, c.cnonce) || len(nonce) == len(c.cnonce) {
		return nil, errors.New("auth: invalid SCRAM server nonce")
	}
	salt, err := base64.StdEncoding.DecodeString(saltB64)
	if err != nil {
		return nil, errors.New("auth: invalid SCRAM salt")
	}
	iter, err := strconv.Atoi(iterStr)
	if err != nil || iter <= 0 {
		return nil, errors.New("auth: invalid SCRAM iteration count")
	}
	if c.clientKey == nil {
		if c.clientKey, c.serverKey, err = scramKeys(c.password, salt, iter); err != nil {
			return nil, err
		}
	}
	withoutProof := "c=biws,r=" + nonce
	c.authMessage = c.clientFirstBare + "," + string(serverFirst) + "," + withoutProof
	stored := sha256.Sum256(c.clientKey)
	proof := xorBytes(c.clientKey, hmacSHA256(stored[:], c.authMessage))
	return []byte(withoutProof + ",p=" + base64.StdEncoding.EncodeToString(proof)), nil
}

