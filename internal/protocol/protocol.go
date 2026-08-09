// Package protocol reads and writes raw Postgres wire messages.
// It uses pgproto3 to decode and encode the messages that the proxy must inspect or change.
package protocol

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/jackc/pgx/v5/pgproto3"
)

// Request codes that a client can send in place of a protocol version in an untyped startup packet.
const (
	ProtocolVersion30 = 196608
	SSLRequestCode    = 80877103
	GSSEncRequestCode = 80877104
	CancelRequestCode = 80877102
)

const (
	// MaxStartupLen is the same limit that the Postgres server uses.
	MaxStartupLen = 10000
	// MaxMessageLen is the largest message body that Postgres accepts (1 GB).
	MaxMessageLen = 1 << 30
)

// Message is one typed wire message. Body excludes the type byte and the length field.
type Message struct {
	Type byte
	Body []byte
}

// Encode appends the complete wire form of m to dst.
func (m Message) Encode(dst []byte) []byte {
	dst = append(dst, m.Type)
	dst = binary.BigEndian.AppendUint32(dst, uint32(len(m.Body)+4))
	return append(dst, m.Body...)
}

// ErrMessageTooLong is returned when a peer announces a message larger than the limit.
var ErrMessageTooLong = errors.New("protocol: message too long")

// Reader reads messages from a buffered stream.
type Reader struct {
	r      *bufio.Reader
	maxLen int
	hdr    [5]byte
}

// NewReader returns a Reader that rejects message bodies larger than maxLen bytes.
func NewReader(r io.Reader, maxLen int) *Reader {
	if maxLen <= 0 || maxLen > MaxMessageLen {
		maxLen = MaxMessageLen
	}
	return &Reader{r: bufio.NewReaderSize(r, 16*1024), maxLen: maxLen}
}

// Buffered returns the number of bytes that can be read without a blocking read.
func (r *Reader) Buffered() int { return r.r.Buffered() }

// ReadMessage reads one typed message. The returned body is a new slice that the caller owns.
func (r *Reader) ReadMessage() (Message, error) {
	if _, err := io.ReadFull(r.r, r.hdr[:]); err != nil {
		return Message{}, err
	}
	n := int(binary.BigEndian.Uint32(r.hdr[1:])) - 4
	if n < 0 {
		return Message{}, fmt.Errorf("protocol: invalid message length %d", n+4)
	}
	if n > r.maxLen {
		return Message{}, ErrMessageTooLong
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r.r, body); err != nil {
		return Message{}, err
	}
	return Message{Type: r.hdr[0], Body: body}, nil
}

// ReadStartup reads an untyped startup packet. The returned body starts with the 4-byte request code.
func (r *Reader) ReadStartup() ([]byte, error) {
	var lenBuf [4]byte
	if _, err := io.ReadFull(r.r, lenBuf[:]); err != nil {
		return nil, err
	}
	n := int(binary.BigEndian.Uint32(lenBuf[:])) - 4
	if n < 4 || n > MaxStartupLen {
		return nil, fmt.Errorf("protocol: invalid startup packet length %d", n+4)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r.r, body); err != nil {
		return nil, err
	}
	return body, nil
}

// Writer buffers outgoing messages until Flush.
type Writer struct {
	w   *bufio.Writer
	buf []byte
}

// NewWriter returns a Writer on w.
func NewWriter(w io.Writer) *Writer {
	return &Writer{w: bufio.NewWriterSize(w, 16*1024)}
}

// WriteRaw writes a raw message.
func (w *Writer) WriteRaw(m Message) error {
	w.buf = m.Encode(w.buf[:0])
	_, err := w.w.Write(w.buf)
	return err
}

// Write encodes and writes a pgproto3 message.
func (w *Writer) Write(msg pgproto3.Message) error {
	var err error
	w.buf, err = msg.Encode(w.buf[:0])
	if err != nil {
		return err
	}
	_, err = w.w.Write(w.buf)
	return err
}

// WriteBytes writes already encoded bytes.
func (w *Writer) WriteBytes(b []byte) error {
	_, err := w.w.Write(b)
	return err
}

// Flush writes all buffered data to the stream.
func (w *Writer) Flush() error { return w.w.Flush() }

// Encode returns the wire form of msg as a Message.
func Encode(msg pgproto3.Message) (Message, error) {
	b, err := msg.Encode(nil)
	if err != nil {
		return Message{}, err
	}
	return Message{Type: b[0], Body: b[5:]}, nil
}

// MustEncode is Encode for messages that cannot fail to encode.
func MustEncode(msg pgproto3.Message) Message {
	m, err := Encode(msg)
	if err != nil {
		panic(err)
	}
	return m
}

// ErrorMessage builds an ErrorResponse with the given severity, SQLSTATE code and text.
func ErrorMessage(severity, code, text string) Message {
	return MustEncode(&pgproto3.ErrorResponse{
		Severity:            severity,
		SeverityUnlocalized: severity,
		Code:                code,
		Message:             text,
	})
}

