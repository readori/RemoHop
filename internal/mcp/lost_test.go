package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/peer/peertest"
	"github.com/readori/RemoHop/internal/wire"
)

// A closing QUIC connection reports itself through the failing stream call
// and through the connection's context in no fixed order. These cases pin
// the order "error first, session still looks alive", which is the one a
// check of conn.Done() alone gets wrong.
func TestSessionLostClassification(t *testing.T) {
	live, _ := peertest.Pair(t)
	lost := []error{
		&quic.ApplicationError{Remote: true, ErrorMessage: "closed"},
		fmt.Errorf("sysinfo: %w", &quic.ApplicationError{Remote: true, ErrorMessage: "closed"}),
		&quic.TransportError{ErrorMessage: "boom"},
		&quic.IdleTimeoutError{},
		&quic.StatelessResetError{},
		fmt.Errorf("read: %w", net.ErrClosed),
	}
	for _, err := range lost {
		start := time.Now()
		if !sessionLost(live, err) {
			t.Errorf("%T (%v) on a session not yet reported closed: not classified as lost", err, err)
		}
		if time.Since(start) > lostGrace/2 {
			t.Errorf("%T: a typed close error should not wait for the grace period", err)
		}
	}
	answers := []error{
		errors.New("remote: no such job"),
		&peer.RemoteError{Service: "exec", Message: "the host does not allow exec"},
		fmt.Errorf("list /x: %w", &peer.RemoteError{Service: "fs", Message: "Application error 0x0 mentioned in a refusal"}),
	}
	for _, err := range answers {
		if sessionLost(live, err) {
			t.Errorf("%v: an answer from the host was classified as a lost session", err)
		}
	}
	// Transport-looking but untyped, on a session that stays up: an
	// ordinary failure, decided after the grace period.
	if sessionLost(live, fmt.Errorf("reading reply: %w", io.ErrUnexpectedEOF)) {
		t.Error("EOF on a healthy session classified as lost")
	}

	// The same untyped errors when the session is reported closed a moment
	// later: lost.
	for _, err := range []error{
		errors.New("sysinfo: Application error 0x0 (remote): closed"),
		fmt.Errorf("reading reply: %w", io.EOF),
	} {
		c, _ := peertest.Pair(t)
		time.AfterFunc(20*time.Millisecond, func() { c.Close() })
		if !sessionLost(c, err) {
			t.Errorf("%v followed by the session closing: not classified as lost", err)
		}
	}
}

// displaysHost is a host whose screenshot service, on the first session,
// closes the whole session after accepting the stream and before replying,
// so the client's call fails with the connection's close error.
func displaysHost(calls *atomic.Int32) *peer.Registry {
	reg := peer.NewRegistry()
	reg.Handle("screenshot", func(_ context.Context, c *peer.Conn, s peer.Stream, _ json.RawMessage) error {
		if calls.Add(1) == 1 {
			c.Close()
			return nil
		}
		return wire.WriteJSON(s, map[string]any{"displays": []map[string]any{{"index": 0, "width": 800, "height": 600, "primary": true}}})
	})
	return reg
}

func TestRetryWhenSessionClosesMidCall(t *testing.T) {
	// Repeated so that both orders of "stream error" and "session done" on
	// the client are exercised.
	for i := range 20 {
		var calls atomic.Int32
		h := newHarnessWith(t, displaysHost(&calls))
		var out displaysOut
		h.ok("remohop_displays", nil, &out)
		if len(out.Displays) != 1 || out.Displays[0].Width != 800 {
			t.Fatalf("round %d: %+v", i, out)
		}
		if n := h.dials.Load(); n != 2 {
			t.Fatalf("round %d: dials = %d, want 2 (one retry on a fresh session)", i, n)
		}
		h.cs.Close()
		h.srv.Close()
	}
}

// A tool that must not be repeated reports the loss instead, whichever way
// the close surfaced.
func TestNoRetryForInputWhenSessionClosesMidCall(t *testing.T) {
	var calls atomic.Int32
	reg := peer.NewRegistry()
	reg.Handle("input", func(_ context.Context, c *peer.Conn, _ peer.Stream, _ json.RawMessage) error {
		calls.Add(1)
		c.Close()
		return nil
	})
	h := newHarnessWith(t, reg)
	msg := h.fail("remohop_key", map[string]any{"keys": []string{"Return"}})
	if calls.Load() != 1 || h.dials.Load() != 1 {
		t.Fatalf("input action repeated: %d calls, %d dials", calls.Load(), h.dials.Load())
	}
	if !strings.Contains(msg, "lost") || !strings.Contains(msg, "may or may not") {
		t.Errorf("message should say the session was lost: %s", msg)
	}
}
