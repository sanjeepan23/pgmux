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

// trackedParams are parameters that the server reports and that pgmux copies to each server a client lands on.
// A client SET of one of these is safe in transaction mode.
var trackedParams = map[string]bool{
	"application_name":            true,
	"client_encoding":             true,
	"datestyle":                   true,
	"intervalstyle":               true,
	"timezone":                    true,
	"standard_conforming_strings": true,
}

