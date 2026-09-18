package session

import (
	"regexp"
	"strings"
)

// Session-level features that do not survive transaction pooling.
const (
	featSet          = "set"
	featListen       = "listen"
	featPrepare      = "prepare"
	featTempTable    = "temp_table"
	featAdvisoryLock = "advisory_lock"
	featHoldCursor   = "cursor_with_hold"
)

