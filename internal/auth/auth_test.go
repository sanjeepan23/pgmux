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

func TestScramExchange(t *testing.T) {
	sec, _ := ParseScramSecret(pgSecret)
	clientKey, err := runExchange(t, NewScramClient("app"), sec)
	if err != nil {
		t.Fatalf("good password: %v", err)
	}
	if _, err := runExchange(t, NewScramClient("wrong"), sec); !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("wrong password: err = %v", err)
	}
	// The recovered ClientKey must log in again without the password. auth_query depends on this.
	if _, err := runExchange(t, NewScramClientWithKeys(clientKey, sec.ServerKey), sec); err != nil {
		t.Fatalf("passthrough keys: %v", err)
	}
}

func TestScramServerRejectsBadMessages(t *testing.T) {
	sec, _ := ParseScramSecret(pgSecret)
	for _, first := range []string{"", "p=tls-server-end-point,,n=,r=abc", "n,,n=", "x,,n=,r=a"} {
		srv := &scramServer{secret: sec}
		if _, err := srv.firstMessage(first); err == nil {
			t.Errorf("firstMessage(%q) accepted", first)
		}
	}
	srv := &scramServer{secret: sec}
	if _, err := srv.firstMessage("n,,n=,r=abc"); err != nil {
		t.Fatal(err)
	}
	for _, final := range []string{"", "c=biws,r=abc", "c=eSws,r=" + srv.nonce + ",p=AAAA", "c=biws,r=other,p=AAAA"} {
		if _, _, err := srv.finalMessage(final); err == nil {
			t.Errorf("finalMessage(%q) accepted", final)
		}
	}
}

func TestScramClientRejectsBadServer(t *testing.T) {
	c := NewScramClient("x")
	c.First()
	if _, err := c.Final([]byte("r=othernonce,s=AAAA,i=4096")); err == nil {
		t.Fatal("foreign nonce accepted")
	}
	c = NewScramClient("x")
	c.First()
	if _, err := c.Final([]byte("r=" + c.cnonce + "srv,s=AAAA,i=0")); err == nil {
		t.Fatal("zero iterations accepted")
	}
}

func TestParseUserlist(t *testing.T) {
	data := `; comment
"alice" "secret"
# another comment
"bob" "md57541d4e65fae11bd07f18dc6fe2b02bc"
"quo""te" "pa""ss"
`
	creds, err := ParseUserlist(strings.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(creds) != 3 || creds["alice"].Password != "secret" || creds["bob"].MD5 == "" {
		t.Fatalf("unexpected result: %+v", creds)
	}
	if creds[`quo"te`].Password != `pa"ss` {
		t.Fatal("escaped quotes not handled")
	}
	for _, bad := range []string{`alice secret`, `"alice" secret`, `"alice`} {
		if _, err := ParseUserlist(bytes.NewBufferString(bad)); err == nil {
			t.Errorf("ParseUserlist(%q) accepted", bad)
		}
	}
}

