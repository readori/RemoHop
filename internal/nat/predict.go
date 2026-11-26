package nat

import (
	"context"
	"net/netip"
	"time"
)

// # Port prediction
//
// A symmetric NAT picks a new public port for every destination, so the
// peer cannot know which port our probes to it will come from, and a peer
// behind a port-restricted NAT drops them. Many symmetric NATs (a lot of
// home routers and carrier-grade NATs) hand those ports out in sequence,
// though: the next mapping gets the last port plus a small fixed stride.
// ProbeNAT sees two consecutive mappings (towards the rendezvous server's
// main and second port), and when they differ by a small stride the next
// ones can be guessed.
//
// The symmetric side then advertises the guess (Hint) and probes the other
// side's public address as usual, which opens its mapping at, hopefully,
// one of the predicted ports. The other side, if its NAT is a cone, probes
// a window of predicted ports on the address the rendezvous server saw the
// symmetric side at. Each of those probes opens the cone NAT's filter for
// one port, so the symmetric side's next probe gets in, from whichever of
// those ports its NAT chose; the probe that lands on the right port is
// let in by the symmetric NAT, whose new mapping expects exactly the cone
// side's address. Either way the normal punch takes over from there: the
// port that answered is adopted as a peer-reflexive candidate.
//
// The window is wider than the two ports the stride predicts because the
// allocator moves on with every other flow the LAN behind it starts between
// the measurement and the punch.

const (
	// maxPredictStep is the largest stride between consecutive ports that
	// is taken for a sequential allocator. Larger differences are more
	// likely random allocation, other traffic between the two
	// observations, or a NAT that reuses ports per destination.
	maxPredictStep = 16

	// maxPredicted bounds how many predicted ports one session probes.
	// Unlike maxRoutable, which keeps a peer and the rendezvous server
	// from naming third parties, these probes all go to one address: the
	// one the rendezvous server observed the peer at, which the peer's
	// own packets came from (see Punch: the prediction is ignored for any
	// other address, and signal only ever builds one for the observed
	// address). The bound limits what a peer can make us send to its own
	// address, and through it to whoever shares that address: at most
	// predictSweeps passes over maxPredicted ports, a few hundred 36-byte
	// packets in a few seconds. It is wide enough for an allocator that
	// moved on by a few dozen ports between the measurement and the punch.
	maxPredicted = 48

	// predictPace spaces the probes of one pass over the window, so that
	// the pass is not a burst a NAT or a rate limiter would drop. The
	// first, low-TTL pass of a host that primes is not paced: it reaches
	// only our own router, and has to be done before the client's first
	// probe can arrive (see PunchWith).
	predictPace = 2 * time.Millisecond

	// predictSweeps is how many passes over the window a session makes,
	// predictRepeat the pause between them. One pass suffices when its
	// probes leave our NAT (they open our filter whatever happens to them
	// further on); the others cover probes lost before that, and a peer
	// that starts probing late.
	predictSweeps = 3
	predictRepeat = time.Second

	// predictLead delays the first pass of a host that cannot prime its
	// NAT with low-TTL probes (see "Who sends first"): its probes would
	// reach the symmetric NAT before the client behind it has sent
	// anything, and a NAT that keeps state for unsolicited packets might
	// then skip exactly the ports we probed. The client starts probing
	// within a round trip of the host's answer, usually well before this.
	predictLead = 500 * time.Millisecond
)

// Prediction describes a NAT that allocates its public ports in sequence:
// Last is the most recent port observed, on public address IP, and Step the
// stride to the next one (negative for an allocator counting down).
type Prediction struct {
	IP   netip.Addr
	Last uint16
	Step int
}

// Valid reports whether p describes a usable prediction: an IPv4 address
// and a stride of at least one and at most maxPredictStep ports.
func (p Prediction) Valid() bool {
	return p.IP.Is4() && p.Step != 0 && p.Step >= -maxPredictStep && p.Step <= maxPredictStep
}

// Port returns the k-th port predicted after Last, wrapping around the
// port space; 0 means that guess falls on port 0 and is skipped.
func (p Prediction) Port(k int) uint16 {
	return uint16(int(p.Last) + p.Step*k)
}

// targets returns the first n predicted addresses.
func (p Prediction) targets(n int) []netip.AddrPort {
	out := make([]netip.AddrPort, 0, n)
	for k := 1; len(out) < n && k <= n+1; k++ {
		if port := p.Port(k); port != 0 {
			out = append(out, netip.AddrPortFrom(p.IP, port))
		}
	}
	return out
}

// predictionFrom derives a prediction from two observations of a symmetric
// NAT: first towards one destination, then second towards another, with
// between further mappings made in between whose ports were not observed.
func predictionFrom(first, second netip.AddrPort, between int) (Prediction, bool) {
	if first.Addr() != second.Addr() {
		return Prediction{}, false
	}
	// The difference as a signed 16-bit number, so that an allocator
	// wrapping from 65535 to 0 still shows a small stride.
	diff := int(int16(second.Port() - first.Port()))
	if diff%(between+1) != 0 {
		return Prediction{}, false
	}
	p := Prediction{IP: first.Addr(), Last: second.Port(), Step: diff / (between + 1)}
	return p, p.Valid()
}

// Hint encodes p for the peer as two self-reported candidates: the first
// two predicted ports on the public address. That address is the one the
// rendezvous server observes, so they pass its filter (it forwards
// self-reported IPv4 addresses only on the observed one), and the server
// needs no change. They go last in the list (see ReadHint). A peer that
// does not know about predictions simply probes them, which is harmless
// and can even work.
func (p Prediction) Hint() []netip.AddrPort {
	if !p.Valid() || p.Port(1) == 0 || p.Port(2) == 0 {
		return nil
	}
	return []netip.AddrPort{netip.AddrPortFrom(p.IP, p.Port(1)), netip.AddrPortFrom(p.IP, p.Port(2))}
}

// ReadHint looks for a Hint at the end of a peer's self-reported candidates
// local, and returns the prediction it encodes together with the remaining
// candidates; without one it returns local unchanged. reflexive is the
// peer's address as the rendezvous server observed it: a hint counts only
// on that address, and the prediction is always for that address. Honest
// peers without a hint never end their list with two addresses on it (the
// only public address they report, a router-mapped one, comes first).
func ReadHint(reflexive netip.AddrPort, local []netip.AddrPort) (Prediction, []netip.AddrPort) {
	n := len(local)
	if n < 2 || !reflexive.Addr().Is4() {
		return Prediction{}, local
	}
	a, b := local[n-2], local[n-1]
	if a.Addr() != reflexive.Addr() || b.Addr() != reflexive.Addr() || a == reflexive || b == reflexive || a.Port() == 0 {
		return Prediction{}, local
	}
	step := int(int16(b.Port() - a.Port()))
	p := Prediction{IP: reflexive.Addr(), Last: uint16(int(a.Port()) - step), Step: step}
	if !p.Valid() {
		return Prediction{}, local
	}
	return p, local[: n-2 : n-2]
}

// Prediction returns how the socket's NAT allocates ports, if ProbeNAT
// found it symmetric with ports handed out in sequence.
func (s *Socket) Prediction() (Prediction, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pred, s.pred.Valid()
}

// RefreshPrediction brings the last port of the prediction up to date, for
// a long-running host whose measurement is old while the allocator has
// moved on with the LAN's other traffic. It asks the rendezvous server's
// second port again: once the NAT has forgotten the mapping towards it
// from the measurement (an idle UDP mapping lasts from half a minute to a
// few minutes), the answer is a new mapping, at the allocator's current
// position. Until then the answer is the old mapping, and nothing changes.
// It costs one round trip to the server, bounded by ctx.
func (s *Socket) RefreshPrediction(ctx context.Context) {
	s.mu.Lock()
	p, alt := s.pred, s.predAlt
	s.mu.Unlock()
	if !p.Valid() || !alt.IsValid() {
		return
	}
	r, err := s.reflect(ctx, alt)
	if err != nil || r.addr.Addr() != p.IP {
		return
	}
	s.mu.Lock()
	if s.pred == p {
		s.pred.Last = r.addr.Port()
	}
	s.mu.Unlock()
}

// UsesPrediction reports whether a punch between two sides with these NAT
// types uses port prediction: one side is symmetric and has a prediction
// (localPred, remotePred), and the other, whose NAT is a cone or who has
// none, probes the predicted ports. Two symmetric NATs do not: probing a
// window from a symmetric NAT would take a new port per probe and run its
// own allocator past what the peer predicted.
func UsesPrediction(local, remote Type, localPred, remotePred bool) bool {
	probes := func(t Type) bool { return t == TypeCone || t == TypeNone }
	return (local == TypeSymmetric && localPred && probes(remote)) ||
		(remote == TypeSymmetric && remotePred && probes(local))
}
