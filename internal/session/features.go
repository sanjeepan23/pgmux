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

var (
	advisoryLockRe = regexp.MustCompile(`(?i)\bpg_(try_)?advisory_lock(_shared)?\s*\(`)
	holdCursorRe   = regexp.MustCompile(`(?i)^\s*declare\b.*\bwith\s+hold\b`)
	tempTableRe    = regexp.MustCompile(`(?i)^\s*create\s+(global\s+|local\s+)?(temp|temporary)\s+(table|view|sequence)\b`)
)

// sessionFeatures returns the session-level features that sql uses.
// It splits on ';' without a full parser, so it can miss cases or report false matches.
func sessionFeatures(sql string) []string {
	var out []string
	if advisoryLockRe.MatchString(sql) {
		out = append(out, featAdvisoryLock)
	}
	for _, stmt := range strings.Split(sql, ";") {
		words := strings.Fields(strings.ToLower(stmt))
		if len(words) == 0 {
			continue
		}
		switch words[0] {
		case "set":
			if isSessionSet(words[1:]) {
				out = append(out, featSet)
			}
		case "listen":
			out = append(out, featListen)
		case "prepare":
			out = append(out, featPrepare)
		case "create":
			if tempTableRe.MatchString(stmt) {
				out = append(out, featTempTable)
			}
		case "declare":
			if holdCursorRe.MatchString(stmt) {
				out = append(out, featHoldCursor)
			}
		}
	}
	return out
}

