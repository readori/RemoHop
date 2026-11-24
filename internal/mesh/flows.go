package mesh

import (
	"net/netip"
	"sync"
	"time"
)

// flows remembers the connections this device opened over the mesh, so that
// their replies pass the policy: a policy restricts who may open a
// connection to a device, never the answers to the device's own.
type flows struct {
	mu    sync.Mutex
	m     map[flowKey]time.Time // expiry
	swept time.Time
	now   func() time.Time
}

type flowKey struct {
	proto  byte
	remote netip.AddrPort // the other end
	local  uint16         // our port
}

const (
	maxFlows   = 1 << 16
	tcpFlowTTL = 10 * time.Minute
	udpFlowTTL = 2 * time.Minute
)

func newFlows() *flows { return &flows{m: map[flowKey]time.Time{}, now: time.Now} }

// out records a packet this device sends.
func (f *flows) out(k packet) {
	if !k.ports || (k.proto != protoTCP && k.proto != protoUDP) {
		return
	}
	ttl := udpFlowTTL
	if k.proto == protoTCP {
		ttl = tcpFlowTTL
	}
	key := flowKey{k.proto, netip.AddrPortFrom(k.dst, k.dport), k.sport}
	f.mu.Lock()
	defer f.mu.Unlock()
	now := f.now()
	if len(f.m) >= maxFlows || now.Sub(f.swept) > time.Minute {
		f.sweep(now)
	}
	if len(f.m) < maxFlows {
		f.m[key] = now.Add(ttl)
	}
}

// reply reports whether a packet arriving for this device answers one it
// sent.
func (f *flows) reply(k packet) bool {
	if !k.ports || (k.proto != protoTCP && k.proto != protoUDP) {
		return false
	}
	key := flowKey{k.proto, netip.AddrPortFrom(k.src, k.sport), k.dport}
	f.mu.Lock()
	defer f.mu.Unlock()
	exp, ok := f.m[key]
	return ok && f.now().Before(exp)
}

func (f *flows) sweep(now time.Time) {
	f.swept = now
	for k, exp := range f.m {
		if !now.Before(exp) {
			delete(f.m, k)
		}
	}
}
