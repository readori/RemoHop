// Package vpn tunnels IP packets through a RemoHop session: `remohop <ID> vpn up`
// gives this machine and the host a virtual network interface each, joined
// by the session, and optionally routes subnets on the host's side (or all
// traffic) through it.
//
// # Wire protocol (service "vpn")
//
// The client opens a stream with a Request; the host answers with one
// Reply (a JSON message). If the reply carries no error, the tunnel is up
// and IP packets flow in both directions, each either
//
//   - in a datagram on the session's VPN channel (peer.DatagramVPN), when
//     the session has datagrams and the packet fits, or
//   - on the stream: a 2-byte big-endian length followed by the packet, for
//     packets too large for a datagram right now and for sessions without
//     datagrams. A length of zero is a keep-alive, sent every few seconds;
//     a side that hears nothing for longer than that takes the tunnel down.
//
// Closing the stream (either side) ends the tunnel. One tunnel per session.
//
// # Who decides what
//
// The client proposes the /30 networks that are free on its side and the
// host picks one that is free on its side too; the host's address is the
// first host address in it, the client's the second. The client asks for
// routes; the host grants all of them or refuses the request. Nothing the
// host says adds a route on the client: the client installs exactly the
// routes its user named, and accepts only packets addressed to its own
// tunnel address. The host accepts from the tunnel only packets that come
// from the client's tunnel address and go where the request was granted to
// go. IPv4 only.
package vpn

import (
	"errors"
	"fmt"
	"net/netip"
	"time"
)

// Service is the service name; it belongs to the "vpn" permission feature.
const Service = "vpn"

const (
	protoVersion = 1

	// maxRoutes bounds the routes of one request.
	maxRoutes = 32
	// maxCandidates bounds the networks a client may propose.
	maxCandidates = 16

	// tunnelMTU is the MTU of every tunnel interface. It is deliberately
	// larger than what fits a datagram on a fresh session (about 1240
	// bytes): TCP, which is nearly all traffic, is held to datagram size
	// by clamping its MSS, while the occasional larger packet of another
	// protocol still gets through, on the stream. A smaller interface MTU
	// would save those detours but break protocols that insist on a
	// 1280-byte path, QUIC (and so HTTP/3, and remohop itself) among them.
	tunnelMTU = 1400
	// minMSS and maxMSS bound the TCP segment size a tunnel clamps to.
	minMSS = 536
	maxMSS = tunnelMTU - 40
)

// keepAlive is how often a tunnel end tells the other it is alive, and
// deadAfter how long silence lasts before the other end gives up and
// removes its interface and rules. A peer that is killed outright cannot
// say goodbye, and the session underneath takes longer to notice.
// Variables so tests need not wait for them.
var (
	keepAlive = 4 * time.Second
	deadAfter = 14 * time.Second
)

// pool is where tunnel networks come from: 10.77.0.0/16, cut into /30s.
var pool = netip.MustParsePrefix("10.77.0.0/16")

// Request asks the host for a tunnel.
type Request struct {
	V int `json:"v"`
	// Nets are /30 networks from the pool that are unused on the client,
	// in order of preference.
	Nets []string `json:"nets"`
	// Routes are the host-side subnets the client wants to reach through
	// the tunnel. The host forwards and masquerades for them.
	Routes []string `json:"routes,omitempty"`
	// Exit asks the host to forward traffic to any destination.
	Exit bool `json:"exit,omitempty"`
	// MaxDatagram is the largest packet the client can send in a datagram
	// right now, 0 if it has no datagrams.
	MaxDatagram int `json:"max_datagram,omitempty"`
}

// Reply is the host's answer.
type Reply struct {
	Error string `json:"error,omitempty"`
	// Net is the network the host picked from Request.Nets.
	Net string `json:"net,omitempty"`
	// MTU is the tunnel interface MTU both ends use, and MSS the TCP
	// segment size both clamp connections through the tunnel to, chosen
	// so that TCP packets fit a datagram.
	MTU int `json:"mtu,omitempty"`
	MSS int `json:"mss,omitempty"`
	// DNS lists resolvers the host itself uses, offered to a client that
	// sends all its traffic through the host.
	DNS []string `json:"dns,omitempty"`
}

// parseRoutes validates route prefixes: IPv4, canonical, and not something
// that would capture the tunnel network itself.
func parseRoutes(in []string) ([]netip.Prefix, error) {
	if len(in) > maxRoutes {
		return nil, fmt.Errorf("too many routes (at most %d)", maxRoutes)
	}
	out := make([]netip.Prefix, 0, len(in))
	for _, s := range in {
		if len(s) > 32 {
			return nil, errors.New("malformed route")
		}
		p, err := netip.ParsePrefix(s)
		if err != nil || !p.Addr().Is4() {
			return nil, fmt.Errorf("route %q is not an IPv4 network (like 192.168.1.0/24)", s)
		}
		if p != p.Masked() {
			return nil, fmt.Errorf("route %s has host bits set; did you mean %s?", p, p.Masked())
		}
		if p.Bits() == 0 {
			return nil, errors.New("0.0.0.0/0 is not a route; use --exit to send all traffic through the host")
		}
		if p.Overlaps(pool) {
			return nil, fmt.Errorf("route %s overlaps %s, which is reserved for the tunnels themselves", p, pool)
		}
		if a := p.Addr(); a.IsLoopback() || a.IsMulticast() || a.IsLinkLocalUnicast() || a.IsUnspecified() {
			return nil, fmt.Errorf("route %s is not a routable network", p)
		}
		out = append(out, p)
	}
	return out, nil
}

// parseNet validates a tunnel network: a /30 inside the pool.
func parseNet(s string) (netip.Prefix, bool) {
	p, err := netip.ParsePrefix(s)
	if err != nil || p.Bits() != 30 || p != p.Masked() || !pool.Contains(p.Addr()) {
		return netip.Prefix{}, false
	}
	return p, true
}

// endpoints returns the host's and the client's address in a tunnel network.
func endpoints(n netip.Prefix) (host, client netip.Addr) {
	host = n.Addr().Next()
	return host, host.Next()
}

// pickMSS derives the TCP segment size from the largest packet each end
// can put in a datagram (0: no datagrams, everything goes on the stream).
func pickMSS(a, b int) int {
	if a <= 0 || b <= 0 {
		return maxMSS
	}
	return max(minMSS, min(min(a, b)-40, maxMSS))
}

// freeNets lists up to n /30s of the pool that overlap none of used.
func freeNets(used []netip.Prefix, n int) []netip.Prefix {
	var out []netip.Prefix
	for p := pool.Masked(); pool.Contains(p.Addr()) && len(out) < n; {
		p = netip.PrefixFrom(p.Addr(), 30)
		free := true
		for _, u := range used {
			if u.Overlaps(p) {
				free = false
				break
			}
		}
		if free {
			out = append(out, p)
		}
		next := p.Addr()
		for i := 0; i < 4; i++ {
			next = next.Next()
		}
		p = netip.PrefixFrom(next, 30)
	}
	return out
}
