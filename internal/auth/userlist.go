package auth

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
)

// LoadUserlist reads a PgBouncer-style userlist file.
// Each line is `"user" "secret"`, and lines that start with ';' or '#' are comments.
func LoadUserlist(path string) (map[string]*Credential, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ParseUserlist(f)
}

// ParseUserlist parses userlist data from r.
func ParseUserlist(r io.Reader) (map[string]*Credential, error) {
	out := map[string]*Credential{}
	sc := bufio.NewScanner(r)
	for lineNo := 1; sc.Scan(); lineNo++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == ';' || line[0] == '#' {
			continue
		}
		user, rest, err := quotedToken(line)
		if err != nil {
			return nil, fmt.Errorf("userlist line %d: %w", lineNo, err)
		}
		secret, _, err := quotedToken(strings.TrimSpace(rest))
		if err != nil {
			return nil, fmt.Errorf("userlist line %d: %w", lineNo, err)
		}
		cred, err := ParseCredential(user, secret)
		if err != nil {
			return nil, fmt.Errorf("userlist line %d: %w", lineNo, err)
		}
		out[user] = cred
	}
	return out, sc.Err()
}

// quotedToken reads one double-quoted token. A doubled quote ("") inside the token is a literal quote.
func quotedToken(s string) (tok, rest string, err error) {
	if !strings.HasPrefix(s, `"`) {
		return "", "", fmt.Errorf("expected quoted value")
	}
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		if s[i] != '"' {
			b.WriteByte(s[i])
			continue
		}
		if i+1 < len(s) && s[i+1] == '"' {
			b.WriteByte('"')
			i++
			continue
		}
		return b.String(), s[i+1:], nil
	}
	return "", "", fmt.Errorf("unterminated quoted value")
}
