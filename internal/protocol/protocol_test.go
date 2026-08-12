package protocol

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/jackc/pgx/v5/pgproto3"
)

func TestReadWriteRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	msgs := []Message{{Type: 'Q', Body: []byte("SELECT 1\x00")}, {Type: 'S'}, {Type: 'X', Body: []byte{}}}
	for _, m := range msgs {
		if err := w.WriteRaw(m); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Write(&pgproto3.Sync{}); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	r := NewReader(&buf, 0)
	for _, want := range append(msgs, Message{Type: 'S'}) {
		got, err := r.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		if got.Type != want.Type || !bytes.Equal(got.Body, want.Body) {
			t.Fatalf("got %q %q, want %q %q", got.Type, got.Body, want.Type, want.Body)
		}
	}
	if _, err := r.ReadMessage(); !errors.Is(err, io.EOF) {
		t.Fatalf("err = %v, want EOF", err)
	}
}

func TestReadMessageLimits(t *testing.T) {
	big := Message{Type: 'Q', Body: make([]byte, 100)}.Encode(nil)
	if _, err := NewReader(bytes.NewReader(big), 50).ReadMessage(); !errors.Is(err, ErrMessageTooLong) {
		t.Fatalf("err = %v, want ErrMessageTooLong", err)
	}
	// A length below 4 is invalid because the length counts itself.
	if _, err := NewReader(bytes.NewReader([]byte{'Q', 0, 0, 0, 2}), 0).ReadMessage(); err == nil {
		t.Fatal("invalid length accepted")
	}
}

