package nat

import (
	"net/netip"
	"sync"
)

// # Networks a path must not run through
//
// A machine in a RemoHop mesh (`remohop net`) has an interface with an
// address in the mesh's network, and routes that network (and any subnet
// routes it accepted) into the mesh. Both ends of a punch would otherwise
// treat those addresses like any LAN address: advertise their own mesh
// address as a candidate, probe the other's, and answer probes that arrive
// from one. Once a mesh link between the two exists, such a probe travels
// through it and is answered, so the "LAN" path through the mesh is proven
// as fast as the real one and, being LAN-like, is dialled at once. A
// session set up over it runs inside the mesh, and a mesh link set up over
// it runs inside itself: as soon as the link it rode on is replaced or ends
// (two devices that open links to each other at the same moment keep only
// one), every packet it sends goes into its own tunnel and nothing comes
// back.
//
// So the mesh registers its networks here while it is up, and no path to a
// peer is ever found through them: they are left out of the candidates we
// advertise, never probed, and probes or acks that arrive from them are
// ignored (which also keeps a peer that does not filter, such as an older
// version, from proving such a path). A QUIC connection to such an address
// that the user asked for explicitly (`remohop 10.78.1.2 shell`, no
// rendezvous) is not affected: it is not a punch.

var excluded struct {
	sync.Mutex
	nets map[netip.Prefix]int // reference counts
}

// ExcludeNetworks marks networks this process tunnels itself, which must
// never carry a punched path, until release is called. Registering the
// same network twice needs two releases; release may be called more than
// once.
func ExcludeNetworks(nets ...netip.Prefix) (release func()) {
	var add []netip.Prefix
	for _, p := range nets {
		if p.IsValid() {
			add = append(add, p.Masked())
		}
	}
	excluded.Lock()
	if excluded.nets == nil {
		excluded.nets = map[netip.Prefix]int{}
	}
	for _, p := range add {
		excluded.nets[p]++
	}
	excluded.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			excluded.Lock()
			defer excluded.Unlock()
			for _, p := range add {
				if excluded.nets[p]--; excluded.nets[p] <= 0 {
					delete(excluded.nets, p)
				}
			}
		})
	}
}

// Excluded reports whether ip is in a network registered with
// ExcludeNetworks.
func Excluded(ip netip.Addr) bool {
	ip = ip.Unmap().WithZone("")
	excluded.Lock()
	defer excluded.Unlock()
	for p := range excluded.nets {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}
