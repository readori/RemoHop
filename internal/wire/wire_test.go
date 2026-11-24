package wire

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"runtime"
	"testing"
)

func TestJSONRoundTrip(t *testing.T) {
	var b bytes.Buffer
	in := map[string]any{"a": "b", "n": float64(3)}
	if err := WriteJSON(&b, in); err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := ReadJSON(&b, &out); err != nil {
		t.Fatal(err)
	}
	if out["a"] != "b" || out["n"] != float64(3) {
		t.Fatalf("got %v", out)
	}
	if err := ReadJSON(&b, &out); err != io.EOF {
		t.Fatalf("want EOF, got %v", err)
	}
}

func TestFrameRoundTrip(t *testing.T) {
	var b bytes.Buffer
	if err := WriteFrame(&b, 7, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := WriteFrame(&b, 9, nil); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 0, 64)
	typ, p, err := ReadFrame(&b, buf)
	if err != nil || typ != 7 || string(p) != "hello" {
		t.Fatalf("typ=%d p=%q err=%v", typ, p, err)
	}
	typ, p, err = ReadFrame(&b, buf)
	if err != nil || typ != 9 || len(p) != 0 {
		t.Fatalf("typ=%d p=%q err=%v", typ, p, err)
	}
}

func TestLimits(t *testing.T) {
	var hdr [5]byte
	binary.BigEndian.PutUint32(hdr[:], MaxFrame+1)
	if _, _, err := ReadFrame(bytes.NewReader(hdr[:]), nil); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("frame: want ErrTooLarge, got %v", err)
	}
	binary.BigEndian.PutUint32(hdr[:], MaxJSON+1)
	var v any
	if err := ReadJSON(bytes.NewReader(hdr[:4]), &v); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("json: want ErrTooLarge, got %v", err)
	}
	if err := ReadJSON(bytes.NewReader([]byte{0, 0, 0, 9, '{'}), &v); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("truncated: got %v", err)
	}
}

// A peer that announces a large message and then sends nothing must not make
// us allocate the announced size.
func TestReadJSONDoesNotPreallocate(t *testing.T) {
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], MaxJSON)
	msgs := make([]io.Reader, 200)
	for i := range msgs {
		msgs[i] = bytes.NewReader(hdr[:])
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for _, r := range msgs {
		var v any
		ReadJSON(r, &v)
	}
	runtime.ReadMemStats(&after)
	if grown := after.TotalAlloc - before.TotalAlloc; grown > 1<<20 {
		t.Fatalf("200 empty announcements allocated %d bytes", grown)
	}
}

func TestReadJSONLimit(t *testing.T) {
	var b bytes.Buffer
	if err := WriteJSON(&b, map[string]string{"k": "0123456789"}); err != nil {
		t.Fatal(err)
	}
	var v any
	if err := ReadJSONLimit(bytes.NewReader(b.Bytes()), &v, 8); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("want ErrTooLarge, got %v", err)
	}
	if err := ReadJSONLimit(bytes.NewReader(b.Bytes()), &v, 64); err != nil {
		t.Fatal(err)
	}
}
