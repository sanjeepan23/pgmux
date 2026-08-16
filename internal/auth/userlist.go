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

