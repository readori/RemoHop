// Package wire implements the two framings every RemoHop stream protocol is
// built from:
//
//   - JSON messages: a 4-byte big-endian length followed by a JSON document.
//     Used for stream headers, requests, replies and other control messages.
//   - Binary frames: a 4-byte big-endian length, a 1-byte type and a payload.
//     Used wherever raw bytes dominate (terminal I/O, file data, video).
//
// Neither function is safe for concurrent writers on the same stream; callers
// that write from several goroutines must serialise with their own mutex.
package wire

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	// MaxJSON bounds a single JSON message.
	MaxJSON = 1 << 20
	// MaxFrame bounds the payload of a single binary frame.
	MaxFrame = 16 << 20
)

// ErrTooLarge is returned when a peer announces a message above the limit.
var ErrTooLarge = errors.New("wire: message too large")

// WriteJSON marshals v and writes it as one length-prefixed message using a
// single Write call.
func WriteJSON(w io.Writer, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(body) > MaxJSON {
		return ErrTooLarge
	}
	buf := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(buf, uint32(len(body)))
	copy(buf[4:], body)
	_, err = w.Write(buf)
	return err
}

// ReadJSON reads one length-prefixed JSON message of at most MaxJSON bytes
// into v.
func ReadJSON(r io.Reader, v any) error { return ReadJSONLimit(r, v, MaxJSON) }

// ReadJSONLimit is ReadJSON with a caller-chosen size limit, for places that
// read from a peer they have less reason to trust.
//
// The body buffer grows only as bytes actually arrive. Allocating the
// announced length up front would let a peer make us reserve the whole limit
// for the price of a 4-byte header.
func ReadJSONLimit(r io.Reader, v any, limit int) error {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return err
	}
	n := int64(binary.BigEndian.Uint32(hdr[:]))
	if n > int64(limit) {
		return ErrTooLarge
	}
	var body bytes.Buffer
	if _, err := io.CopyN(&body, r, n); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return err
	}
	if err := json.Unmarshal(body.Bytes(), v); err != nil {
		return fmt.Errorf("wire: bad json message: %w", err)
	}
	return nil
}

// WriteFrame writes one binary frame using a single Write call.
func WriteFrame(w io.Writer, typ byte, payload []byte) error {
	if len(payload) > MaxFrame {
		return ErrTooLarge
	}
	buf := make([]byte, 5+len(payload))
	binary.BigEndian.PutUint32(buf, uint32(len(payload)))
	buf[4] = typ
	copy(buf[5:], payload)
	_, err := w.Write(buf)
	return err
}

// ReadFrame reads one binary frame. If buf has enough capacity it is reused
// for the payload, so the returned slice is only valid until the next call
// that is handed the same buf.
func ReadFrame(r io.Reader, buf []byte) (typ byte, payload []byte, err error) {
	var hdr [5]byte
	if _, err = io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	n := int(binary.BigEndian.Uint32(hdr[:4]))
	if n > MaxFrame {
		return 0, nil, ErrTooLarge
	}
	if cap(buf) >= n {
		payload = buf[:n]
	} else {
		payload = make([]byte, n)
	}
	if _, err = io.ReadFull(r, payload); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return 0, nil, err
	}
	return hdr[4], payload, nil
}
