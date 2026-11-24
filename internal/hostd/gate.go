package hostd

import (
	"net/netip"
	"sync"

	"github.com/readori/RemoHop/internal/auth"
)

// Bounds on connections that have not logged in yet. Anyone who can reach
// the host's UDP port can open such connections, so they must be cheap to
// hold and impossible to hoard.
const (
	// A connection that has completed the QUIC handshake but not yet sent
	// its login hello is "waiting". At most maxWaiting exist at a time, at
	// most maxWaitingPerSource from one source (an IPv4 address or an
	// IPv6 /64) and maxWaitingPerNet from one IPv6 /48.
	maxWaiting          = 128
	maxWaitingPerSource = 32
	maxWaitingPerNet    = 64
	// Once the hello is in, the login is "active" until it succeeds or
	// fails. Password and token logins are counted apart, so neither kind
	// can use up the other's room.
	maxActivePasswords          = 96
	maxActivePasswordsPerSource = 32
	maxActiveTokens             = 64
)

// gate accounts for unauthenticated connections.
//
// The waiting stage never turns a newcomer away. When a bound is reached,
// the oldest waiting connection within that bound is closed instead: a real
// client sends its hello within a round trip of connecting, so the oldest
// waiter is the one most likely to be idling on purpose. Holding slots
// therefore keeps nobody out; to shut out a real client an attacker would
// have to cycle through the whole table inside that client's one round
// trip, from many addresses at once.
//
// The active stage is where the client has committed to a credential kind.
// Password logins are bounded here and paced by auth.Limiter. A token login
// stays active only if the token exists and belongs to the identity the
// client proved in TLS (anything else is refused at once), so strangers
// cannot occupy the token share, and paired clients get through no matter
// what is happening to password logins.
type gate struct {
	mu        sync.Mutex
	waiting   []*pending // oldest first
	passwords int
	tokens    int
	pwBy      map[netip.Addr]int
}

type pendingState int

const (
	stateWaiting pendingState = iota
	stateActive
	stateGone
)

type pending struct {
	evict  func()     // closes the connection to make room
	source netip.Addr // IPv4 address or IPv6 /64
	net    netip.Addr // IPv6 /48; equal to source for IPv4
	state  pendingState
	kind   auth.Kind
}

func newGate() *gate { return &gate{pwBy: map[netip.Addr]int{}} }

// sourceOf reduces a client address to the units the bounds apply to, the
// same grouping auth.Limiter uses for sources.
func sourceOf(a netip.Addr) (source, network netip.Addr) {
	a = a.Unmap().WithZone("")
	if !a.Is6() {
		return a, a
	}
	source, network = a, a
	if p, err := a.Prefix(64); err == nil {
		source = p.Addr()
	}
	if p, err := a.Prefix(48); err == nil {
		network = p.Addr()
	}
	return source, network
}

// enter registers a new connection from addr as waiting, closing an older
// waiter (through the evict function it was entered with) if that is what
// it takes to stay within bounds.
func (g *gate) enter(addr netip.Addr, evict func()) *pending {
	p := &pending{evict: evict}
	p.source, p.net = sourceOf(addr)

	g.mu.Lock()
	fromSource, fromNet := 0, 0
	for _, w := range g.waiting {
		if w.source == p.source {
			fromSource++
		}
		if w.net == p.net {
			fromNet++
		}
	}
	var victim *pending
	switch {
	case fromSource >= maxWaitingPerSource:
		victim = g.oldest(func(w *pending) bool { return w.source == p.source })
	case fromNet >= maxWaitingPerNet:
		victim = g.oldest(func(w *pending) bool { return w.net == p.net })
	case len(g.waiting) >= maxWaiting:
		victim = g.oldest(func(*pending) bool { return true })
	}
	if victim != nil {
		g.remove(victim)
	}
	g.waiting = append(g.waiting, p)
	g.mu.Unlock()

	if victim != nil {
		victim.evict()
	}
	return p
}

func (g *gate) oldest(match func(*pending) bool) *pending {
	for _, w := range g.waiting {
		if match(w) {
			return w
		}
	}
	return nil
}

// remove takes p out of whichever stage it is in. Callers hold g.mu.
func (g *gate) remove(p *pending) {
	switch p.state {
	case stateWaiting:
		for i, w := range g.waiting {
			if w == p {
				g.waiting = append(g.waiting[:i], g.waiting[i+1:]...)
				break
			}
		}
	case stateActive:
		if p.kind == auth.KindToken {
			g.tokens--
		} else {
			g.passwords--
			if g.pwBy[p.source]--; g.pwBy[p.source] <= 0 {
				delete(g.pwBy, p.source)
			}
		}
	}
	p.state = stateGone
}

// promote moves a waiting connection to the active stage for a login of the
// given kind. It reports false when the connection was closed to make room
// while it waited, or when that kind's share is full.
func (g *gate) promote(p *pending, kind auth.Kind) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if p.state != stateWaiting {
		return false
	}
	if kind == auth.KindToken {
		if g.tokens >= maxActiveTokens {
			return false
		}
	} else if g.passwords >= maxActivePasswords || g.pwBy[p.source] >= maxActivePasswordsPerSource {
		return false
	}
	g.remove(p)
	p.state, p.kind = stateActive, kind
	if kind == auth.KindToken {
		g.tokens++
	} else {
		g.passwords++
		g.pwBy[p.source]++
	}
	return true
}

// leave ends the accounting for p. The caller closes the connection (or
// hands it to the session layer) before calling it, so a slot is never
// free while its connection still holds memory.
func (g *gate) leave(p *pending) {
	g.mu.Lock()
	g.remove(p)
	g.mu.Unlock()
}
