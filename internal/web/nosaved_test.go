package web

import (
	"context"
	"testing"

	"github.com/readori/RemoHop/internal/peer"
)

// A shared gateway (--no-saved) refuses to pair even when the API asks,
// although its page hides the option (security audit finding 5).
func TestNoSavedRefusesPairing(t *testing.T) {
	testEnv(t)
	d := &dialer{t: t}
	paired := false
	s := startServer(t, Config{Dial: d.dial, NoSaved: true, Pair: func(context.Context, *peer.Conn, string) error { paired = true; return nil }})
	c := newLoopbackClient(t, s)
	c.newSession()
	var e struct{ Error apiError }
	code := c.jsonCall("POST", "api/v1/connect", map[string]any{"target": "123456789", "password": "hostpw", "pair": true}, &e)
	if code != 403 || e.Error.Code != "pairing_disabled" {
		t.Fatalf("pair on a --no-saved gateway: %d %+v", code, e)
	}
	if paired || len(d.calls) != 0 {
		t.Fatalf("dialled (%d) or paired (%v) anyway", len(d.calls), paired)
	}
}
