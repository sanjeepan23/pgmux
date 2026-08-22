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

func TestMD5HashMatchesPostgres(t *testing.T) {
	if got := MD5Hash("md5user", "md5pass"); got != "md57541d4e65fae11bd07f18dc6fe2b02bc" {
		t.Fatalf("MD5Hash = %s", got)
	}
}

func TestParseCredential(t *testing.T) {
	c, _ := ParseCredential("u", pgSecret)
	if c.SCRAM == nil || c.Password != "" {
		t.Fatal("SCRAM secret not detected")
	}
	c, _ = ParseCredential("md5user", "md57541d4e65fae11bd07f18dc6fe2b02bc")
	if c.MD5 == "" || c.Password != "" {
		t.Fatal("MD5 hash not detected")
	}
	c, _ = ParseCredential("u", "plain")
	if c.Password != "plain" || c.MD5 != MD5Hash("u", "plain") {
		t.Fatal("plaintext password not detected")
	}
	if _, err := ParseCredential("u", "SCRAM-SHA-256$bad"); err == nil {
		t.Fatal("malformed SCRAM secret accepted")
	}
}

// runExchange runs a SCRAM exchange between ScramClient and scramServer in memory.
func runExchange(t *testing.T, client *ScramClient, secret *ScramSecret) ([]byte, error) {
	t.Helper()
	srv := &scramServer{secret: secret}
	serverFirst, err := srv.firstMessage(string(client.First()))
	if err != nil {
		return nil, err
	}
	clientFinal, err := client.Final([]byte(serverFirst))
	if err != nil {
		return nil, err
	}
	serverFinal, clientKey, err := srv.finalMessage(string(clientFinal))
	if err != nil {
		return nil, err
	}
	return clientKey, client.Verify([]byte(serverFinal))
}

