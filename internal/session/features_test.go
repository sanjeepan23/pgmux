package session

import (
	"slices"
	"testing"
)

func TestSessionFeatures(t *testing.T) {
	cases := []struct {
		sql  string
		want []string
	}{
		{"SELECT 1", nil},
		{"SET LOCAL statement_timeout = 5", nil},
		{"SET TRANSACTION ISOLATION LEVEL SERIALIZABLE", nil},
		{"SET TimeZone = 'UTC'", nil},
		{"SET TIME ZONE 'UTC'", nil},
		{"SET NAMES 'UTF8'", nil},
		{"set application_name to x", nil},
		{"SET statement_timeout = 5", []string{featSet}},
		{"SET SESSION search_path TO foo", []string{featSet}},
		{"SET search_path = foo", []string{featSet}},
		{"LISTEN ch", []string{featListen}},
		{"select 1; listen ch", []string{featListen}},
		{"PREPARE p AS SELECT 1", []string{featPrepare}},
		{"CREATE TEMP TABLE t (a int)", []string{featTempTable}},
		{"create temporary table t (a int)", []string{featTempTable}},
		{"CREATE TABLE t (a int)", nil},
		{"SELECT pg_advisory_lock(1)", []string{featAdvisoryLock}},
		{"SELECT pg_try_advisory_lock_shared (1)", []string{featAdvisoryLock}},
		{"SELECT pg_advisory_xact_lock(1)", nil},
		{"DECLARE c CURSOR WITH HOLD FOR SELECT 1", []string{featHoldCursor}},
		{"DECLARE c CURSOR FOR SELECT 1", nil},
	}
	for _, c := range cases {
		if got := sessionFeatures(c.sql); !slices.Equal(got, c.want) {
			t.Errorf("sessionFeatures(%q) = %v, want %v", c.sql, got, c.want)
		}
	}
}

