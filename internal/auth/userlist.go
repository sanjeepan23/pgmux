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

