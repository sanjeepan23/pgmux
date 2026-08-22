package auth

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// pgSecret is the secret that Postgres 16 stored for the password "app".
const pgSecret = "SCRAM-SHA-256$4096:fvBIIOneAN5o0os6Etn67Q==$+MebYzA7q3sjIvzuZXfvbh8Qp1EAFSkSTKvoXgK+IWM=:wXnGhWmwbLGyXC/2OKFrQZtQXBNm6FOW8VABtlmM2Us="

func TestScramSecretMatchesPostgres(t *testing.T) {
	sec, err := ParseScramSecret(pgSecret)
	if err != nil {
		t.Fatal(err)
	}
	derived, err := NewScramSecret("app", sec.Salt, sec.Iterations)
	if err != nil {
		t.Fatal(err)
	}
	if derived.String() != pgSecret {
		t.Fatalf("derived secret\n%s\nwant\n%s", derived, pgSecret)
	}
}

