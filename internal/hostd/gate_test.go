package hostd

import (
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/signal"
)

// enterN enters n connections from addr and returns them with a record of
// which were closed to make room.
func enterN(g *gate, addr netip.Addr, n int, evicted map[*pending]bool) []*pending {
	var out []*pending
	for i := 0; i < n; i++ {
		var p *pending
		p = g.enter(addr, func() { evicted[p] = true })
		out = append(out, p)
	}
	return out
}

func TestGateEvictsOldestWaiter(t *testing.T) {
	v4 := func(i int) netip.Addr { return netip.AddrFrom4([4]byte{192, 0, 2, byte(i)}) }
	v6 := func(net, sub, host int) netip.Addr {
		return netip.AddrFrom16([16]byte{0x20, 0x01, 0x0d, 0xb8, 0, byte(net), 0, byte(sub), 15: byte(host)})
	}

	t.Run("per source", func(t *testing.T) {
		g, evicted := newGate(), map[*pending]bool{}
		mine := enterN(g, v4(1), maxWaitingPerSource, evicted)
		other := enterN(g, v4(2), 1, evicted)
		if len(evicted) != 0 {
			t.Fatal("evictions within bounds")
		}
		// One more from the busy source costs that source its oldest.
		extra := enterN(g, v4(1), 3, evicted)
		if len(evicted) != 3 || !evicted[mine[0]] || !evicted[mine[2]] || evicted[mine[3]] || evicted[other[0]] {
			t.Fatalf("wrong connections evicted (%d)", len(evicted))
		}
		// An evicted connection cannot go on to log in; a fresh one can.
		if g.promote(mine[0], auth.KindPassword) {
			t.Fatal("evicted connection promoted")
		}
		if !g.promote(extra[2], auth.KindPassword) {
			t.Fatal("newest connection not promoted")
		}
	})

	t.Run("IPv6 /64 is one source, /48 is bounded too", func(t *testing.T) {
		g, evicted := newGate(), map[*pending]bool{}
		// Rotating the host part does not get around the per-source bound.
		var first *pending
		for i := 0; i <= maxWaitingPerSource; i++ {
			p := enterN(g, v6(1, 1, i+1), 1, evicted)[0]
			if i == 0 {
				first = p
			}
		}
		if len(evicted) != 1 || !evicted[first] {
			t.Fatalf("rotating addresses within a /64: %d evictions", len(evicted))
		}
		// Rotating /64s within one /48 runs into the network bound.
		g, evicted = newGate(), map[*pending]bool{}
		for sub := 0; sub < maxWaitingPerNet+10; sub++ {
			enterN(g, v6(2, sub, 1), 1, evicted)
		}
		if len(evicted) != 10 || len(g.waiting) != maxWaitingPerNet {
			t.Fatalf("one /48 holds %d waiting slots after %d evictions, want %d", len(g.waiting), len(evicted), maxWaitingPerNet)
		}
		// Another network is not affected by that.
		before := len(evicted)
		enterN(g, v6(3, 0, 1), 1, evicted)
		if len(evicted) != before {
			t.Fatal("a different /48 paid for the busy one")
		}
	})

	t.Run("global bound never refuses a newcomer", func(t *testing.T) {
		g, evicted := newGate(), map[*pending]bool{}
		var all []*pending
		for i := 0; i < maxWaiting; i++ {
			all = append(all, enterN(g, v4(i%200+1), 1, evicted)...)
		}
		if len(evicted) != 0 {
			t.Fatalf("%d evictions below the bound", len(evicted))
		}
		newcomer := enterN(g, netip.MustParseAddr("198.51.100.1"), 1, evicted)[0]
		if len(evicted) != 1 || !evicted[all[0]] || len(g.waiting) != maxWaiting {
			t.Fatal("the globally oldest waiter was not the one evicted")
		}
		if !g.promote(newcomer, auth.KindToken) {
			t.Fatal("newcomer could not log in")
		}
	})
}

func TestGateSharesByCredentialKind(t *testing.T) {
	g, evicted := newGate(), map[*pending]bool{}
	src := netip.MustParseAddr("192.0.2.1")
	// One source fills its share of password logins.
	var pw []*pending
	for i := 0; i < maxActivePasswordsPerSource; i++ {
		p := enterN(g, src, 1, evicted)[0]
		if !g.promote(p, auth.KindPassword) {
			t.Fatalf("password login %d refused", i)
		}
		pw = append(pw, p)
	}
	over := enterN(g, src, 1, evicted)[0]
	if g.promote(over, auth.KindPassword) {
		t.Fatal("password logins beyond the per-source share admitted")
	}
	// Token logins have their own room.
	tok := enterN(g, src, 1, evicted)[0]
	if !g.promote(tok, auth.KindToken) {
		t.Fatal("token login refused while password logins are in progress")
	}
	// Leaving frees the share.
	g.leave(pw[0])
	g.leave(pw[0]) // twice is harmless
	again := enterN(g, src, 1, evicted)[0]
	if !g.promote(again, auth.KindPassword) {
		t.Fatal("share not freed by leave")
	}
	if g.passwords != maxActivePasswordsPerSource || g.tokens != 1 {
		t.Fatalf("accounting off: %d passwords, %d tokens", g.passwords, g.tokens)
	}
	// All sources together are bounded as well.
	g = newGate()
	admitted := 0
	for i := 0; i < 2*maxActivePasswords; i++ {
		p := enterN(g, netip.AddrFrom4([4]byte{10, 0, byte(i >> 8), byte(i)}), 1, evicted)[0]
		if g.promote(p, auth.KindPassword) {
			admitted++
		}
	}
	if admitted != maxActivePasswords {
		t.Fatalf("%d password logins in progress, want at most %d", admitted, maxActivePasswords)
	}
}

// A rendezvous server cannot park a host: whatever wait it names, the host
// is back within signal.MaxRetryAfter (plus jitter).
func TestRetryWaitIsBounded(t *testing.T) {
	limit := signal.MaxRetryAfter + signal.MaxRetryAfter/4 + time.Second
	for _, err := range []error{
		&signal.Error{Code: signal.CodeRateLimited, RetryAfter: 100 * 365 * 24 * time.Hour},
		&signal.Error{Code: signal.CodeRateLimited, RetryAfter: 1<<63 - 1},
		errors.New("connection refused"),
	} {
		for _, backoff := range []time.Duration{time.Second, time.Minute} {
			w := retryWait(backoff, err)
			if w < backoff || w > limit {
				t.Errorf("retryWait(%s, %v) = %s", backoff, err, w)
			}
		}
	}
	// A reasonable request is honoured.
	if w := retryWait(time.Second, &signal.Error{Code: signal.CodeRateLimited, RetryAfter: 30 * time.Second}); w < 30*time.Second || w > 38*time.Second {
		t.Errorf("30 s retry-after gave %s", w)
	}
}

func TestLoginRefusal(t *testing.T) {
	remote := func(code quic.ApplicationErrorCode, msg string) error {
		return &quic.ApplicationError{Remote: true, ErrorCode: code, ErrorMessage: msg}
	}
	for _, tc := range []struct {
		err  error
		code auth.Code
		wait time.Duration
	}{
		{remote(closeAuth, "auth_failed"), auth.CodeAuthFailed, 0},
		{remote(closeAuth, "unknown_token"), auth.CodeUnknownToken, 0},
		{remote(closeAuth, "locked_out:12000"), auth.CodeLockedOut, 12 * time.Second},
		{remote(closeAuth, "locked_out:3000000000000"), auth.CodeLockedOut, auth.MaxRetryAfter},
		{remote(closeAuth, "locked_out:-7"), auth.CodeLockedOut, 0},
		{remote(closeAuth, "\x1b[2Jowned"), auth.CodeProtocol, 0},
		{remote(closeBusy, "anything"), auth.CodeBusy, 0},
	} {
		e := LoginRefusal(tc.err)
		if e == nil || e.Code != tc.code || e.RetryAfter != tc.wait {
			t.Errorf("LoginRefusal(%v) = %+v, want %s/%s", tc.err, e, tc.code, tc.wait)
			continue
		}
		if strings.ContainsRune(e.Error(), 0x1b) {
			t.Errorf("escape sequence in %q", e.Error())
		}
	}
	// Not refusals: our own close, another application code, other errors.
	for _, err := range []error{
		&quic.ApplicationError{Remote: false, ErrorCode: closeAuth, ErrorMessage: "auth_failed"},
		remote(0, "host shutting down"),
		errors.New("timeout"),
		nil,
	} {
		if e := LoginRefusal(err); e != nil {
			t.Errorf("LoginRefusal(%v) = %+v, want nil", err, e)
		}
	}
	// The host's own encoding round-trips.
	e := LoginRefusal(remote(closeAuth, closeReason(&auth.Error{Code: auth.CodeLockedOut, RetryAfter: 90 * time.Second})))
	if e == nil || e.Code != auth.CodeLockedOut || e.RetryAfter != 90*time.Second {
		t.Errorf("round trip gave %+v", e)
	}
}
