package hostd

import (
	"context"
	"net/netip"
	"testing"

	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/signal"
)

// Every relayed (and websocket-bridged) client reaches the host from the
// rendezvous server's relay. Counted by that address they would share one
// login-limiter source and one gate source, so a stranger's three failed
// guesses through the relay would lock every relayed password login out.
// They count as the client the relay was bound for.
func TestRelayedLoginsHaveTheirOwnSource(t *testing.T) {
	h, err := New(Config{Dir: t.TempDir(), Listen: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	defer h.sock.Close()
	relayIP := netip.MustParseAddr("203.0.113.10")
	attacker, victim := netip.MustParseAddr("198.51.100.66"), netip.MustParseAddr("192.0.2.7")
	introduce := func(nonce byte, client netip.Addr, port uint16) netip.AddrPort {
		n := make([]byte, 16)
		n[0] = nonce
		// Both ends bridged: nothing is punched, the client comes through
		// the relay.
		h.onOffer(signal.Offer{Nonce: n, Peer: signal.Peer{Reflexive: netip.AddrPortFrom(client, 40000), Bridged: true}}, true)
		h.mu.Lock()
		in := h.offers[string(n)]
		h.mu.Unlock()
		relay := netip.AddrPortFrom(relayIP, port)
		h.noteRelay(relay, in.client)
		return relay
	}
	viaA := introduce(1, attacker, 50001)
	viaV := introduce(2, victim, 50002)
	if got := h.loginSource(viaA).Addr(); got != attacker {
		t.Fatalf("source of the attacker's relayed connection: %v", got)
	}
	if got := h.loginSource(viaV).Addr(); got != victim {
		t.Fatalf("source of the victim's relayed connection: %v", got)
	}
	// A connection from a relay address nobody was introduced for, or a
	// direct one, counts as where it comes from.
	other := netip.MustParseAddrPort("203.0.113.10:50003")
	if got := h.loginSource(other); got != other {
		t.Fatalf("unknown relay address: %v", got)
	}

	// What that means for the limiter: the attacker locks out only itself.
	l := auth.NewLimiter(auth.LimiterConfig{})
	for i := range 3 {
		a, err := l.Begin(context.Background(), h.loginSource(viaA).Addr())
		if err != nil {
			t.Fatalf("attacker attempt %d: %v", i+1, err)
		}
		a.Fail()
	}
	if _, err := l.Begin(context.Background(), h.loginSource(viaA).Addr()); auth.CodeOf(err) != auth.CodeLockedOut {
		t.Fatalf("the attacker after 3 failures: %v", err)
	}
	if _, err := l.Begin(context.Background(), h.loginSource(viaV).Addr()); err != nil {
		t.Fatalf("a relayed client after a stranger's failures through the same relay: %v", err)
	}
	// And for the login gate.
	if p, q := h.gate.enter(h.loginSource(viaA).Addr(), func() {}), h.gate.enter(h.loginSource(viaV).Addr(), func() {}); p.source == q.source {
		t.Fatal("two relayed clients share a gate source")
	}
}
