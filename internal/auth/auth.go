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

