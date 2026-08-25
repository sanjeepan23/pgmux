package pool

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"

	"github.com/sanjeepan23/pgmux/internal/auth"
	"github.com/sanjeepan23/pgmux/internal/protocol"
)

// Login holds what the pooler needs to log in to Postgres as one user.
type Login struct {
	User     string
	Database string
	Password string
	MD5      string
	Keys     *auth.ClientKeys
}

