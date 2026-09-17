// Package prepared maps client prepared-statement names to shared server-side names for transaction mode.
package prepared

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"

	"github.com/jackc/pgx/v5/pgproto3"

	"github.com/sanjeepan23/pgmux/internal/protocol"
)

// Prefix starts every server-side statement name that pgmux creates.
const Prefix = "pgmux_"

