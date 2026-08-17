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

