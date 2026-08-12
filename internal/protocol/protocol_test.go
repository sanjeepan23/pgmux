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

func TestReadStartup(t *testing.T) {
	b, _ := (&pgproto3.StartupMessage{ProtocolVersion: ProtocolVersion30, Parameters: map[string]string{"user": "u"}}).Encode(nil)
	body, err := NewReader(bytes.NewReader(b), 0).ReadStartup()
	if err != nil {
		t.Fatal(err)
	}
	var sm pgproto3.StartupMessage
	if err := sm.Decode(body); err != nil || sm.Parameters["user"] != "u" {
		t.Fatalf("decode: %v %v", sm, err)
	}
	if _, err := NewReader(bytes.NewReader([]byte{0, 1, 0, 0}), 0).ReadStartup(); err == nil {
		t.Fatal("oversized startup packet accepted")
	}
}

func FuzzReadMessage(f *testing.F) {
	f.Add(Message{Type: 'Q', Body: []byte("SELECT 1\x00")}.Encode(nil))
	f.Add([]byte{'P', 0, 0, 0, 4})
	f.Add([]byte{0, 0, 0, 8, 4, 210, 22, 47})
	f.Fuzz(func(t *testing.T, data []byte) {
		r := NewReader(bytes.NewReader(data), 1<<16)
		for {
			if _, err := r.ReadMessage(); err != nil {
				break
			}
		}
		r = NewReader(bytes.NewReader(data), 1<<16)
		if body, err := r.ReadStartup(); err == nil {
			var sm pgproto3.StartupMessage
			_ = sm.Decode(body)
		}
	})
}
