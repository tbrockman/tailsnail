package proto

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// ErrFrameTooLarge is returned when a peer announces a frame above MaxFrame.
var ErrFrameTooLarge = errors.New("proto: frame exceeds maximum size")

// Conn is a message-oriented wrapper around a net.Conn. Writes are serialised
// internally so any goroutine may send; reads must be driven by a single
// goroutine, which is how every caller in tailsnail uses it.
type Conn struct {
	raw net.Conn
	br  *bufio.Reader

	wmu sync.Mutex
	// closed guards against the double-close that happens when a read loop and
	// a shutdown path both notice the same failure.
	closeOnce sync.Once
}

// NewConn wraps c. The caller retains responsibility for closing it exactly
// once via Conn.Close.
func NewConn(c net.Conn) *Conn {
	return &Conn{raw: c, br: bufio.NewReaderSize(c, 32<<10)}
}

// RemoteAddr reports the peer address of the underlying connection.
func (c *Conn) RemoteAddr() net.Addr { return c.raw.RemoteAddr() }

// Close shuts the underlying connection down. It is safe to call repeatedly
// and from multiple goroutines.
func (c *Conn) Close() error {
	var err error
	c.closeOnce.Do(func() { err = c.raw.Close() })
	return err
}

// NewEnvelope marshals body into an envelope of the given kind. Callers that
// fan one message out to several connections build it once and hand the result
// to SendEnvelope, rather than re-encoding per recipient.
func NewEnvelope(kind Kind, body any) (Envelope, error) {
	var raw json.RawMessage
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return Envelope{}, fmt.Errorf("proto: encoding %s body: %w", kind, err)
		}
		raw = b
	}
	return Envelope{V: Version, Kind: kind, Body: raw}, nil
}

// Send marshals body and writes it as a framed message of the given kind.
func (c *Conn) Send(kind Kind, body any) error {
	env, err := NewEnvelope(kind, body)
	if err != nil {
		return err
	}
	return c.SendEnvelope(env)
}

// SendEnvelope writes a pre-built envelope.
func (c *Conn) SendEnvelope(e Envelope) error {
	payload, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("proto: encoding envelope: %w", err)
	}
	if len(payload) > MaxFrame {
		return fmt.Errorf("%w: %d bytes", ErrFrameTooLarge, len(payload))
	}
	buf := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(buf[:4], uint32(len(payload)))
	copy(buf[4:], payload)

	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, err = c.raw.Write(buf)
	return err
}

// SetWriteDeadline bounds how long a blocked write may stall the sender.
func (c *Conn) SetWriteDeadline(t time.Time) error { return c.raw.SetWriteDeadline(t) }

// SetReadDeadline bounds how long Recv will block.
func (c *Conn) SetReadDeadline(t time.Time) error { return c.raw.SetReadDeadline(t) }

// Recv reads the next framed message. It returns io.EOF when the peer closes
// the connection cleanly.
func (c *Conn) Recv() (Envelope, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(c.br, hdr[:]); err != nil {
		return Envelope{}, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n == 0 {
		return Envelope{}, errors.New("proto: zero-length frame")
	}
	if n > MaxFrame {
		// The stream is no longer trustworthy once a bogus length arrives;
		// the caller is expected to close rather than resynchronise.
		return Envelope{}, fmt.Errorf("%w: peer announced %d bytes", ErrFrameTooLarge, n)
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(c.br, payload); err != nil {
		// A truncated body mid-frame is a broken peer, not a clean close.
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return Envelope{}, err
	}
	var e Envelope
	if err := json.Unmarshal(payload, &e); err != nil {
		return Envelope{}, fmt.Errorf("proto: decoding envelope: %w", err)
	}
	return e, nil
}

// RecvTimeout reads the next message, failing if it does not arrive in d.
func (c *Conn) RecvTimeout(d time.Duration) (Envelope, error) {
	if err := c.raw.SetReadDeadline(time.Now().Add(d)); err != nil {
		return Envelope{}, err
	}
	defer c.raw.SetReadDeadline(time.Time{})
	return c.Recv()
}

// SendTimeout writes a message, failing if it cannot be flushed within d.
func (c *Conn) SendTimeout(d time.Duration, kind Kind, body any) error {
	if err := c.raw.SetWriteDeadline(time.Now().Add(d)); err != nil {
		return err
	}
	defer c.raw.SetWriteDeadline(time.Time{})
	return c.Send(kind, body)
}
