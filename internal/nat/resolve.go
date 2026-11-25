package nat

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strconv"
)

// ResolveUDP resolves "host:port" for a socket that is bound as local says
// (an IPv4-only socket can only use IPv4 addresses), within ctx: a resolver
// that hangs (a dead first name server, a captive portal, the moment after
// a network switch) cannot hold a dial past its deadline or keep the user's
// Disconnect waiting.
//
// Of the name's addresses it returns the first this machine has a route to,
// IPv4 first: on an IPv6-only network without NAT64 a dual-stack name then
// still works through its AAAA record, where the system's first answer, the
// A record, would fail with "network is unreachable".
func ResolveUDP(ctx context.Context, local net.Addr, hostport string) (*net.UDPAddr, error) {
	host, portStr, err := net.SplitHostPort(hostport)
	if err != nil {
		return nil, err
	}
	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		if p, perr := net.DefaultResolver.LookupPort(ctx, "udp", portStr); perr == nil {
			port = uint64(p)
		} else {
			return nil, fmt.Errorf("bad port %q", portStr)
		}
	}
	v4only := false
	if la, ok := local.(*net.UDPAddr); ok && la.IP.To4() != nil {
		v4only = true
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return net.UDPAddrFromAddrPort(netip.AddrPortFrom(ip.Unmap(), uint16(port))), nil
	}
	network := "ip"
	if v4only {
		network = "ip4"
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, network, host)
	if err != nil {
		return nil, err
	}
	var cands []netip.AddrPort
	for _, ip := range orderFamilies(ips) {
		cands = append(cands, netip.AddrPortFrom(ip.Unmap(), uint16(port)))
	}
	if len(cands) == 0 {
		return nil, fmt.Errorf("no address for %s", host)
	}
	for _, ap := range cands {
		if routeTo(ap) {
			return net.UDPAddrFromAddrPort(ap), nil
		}
	}
	return net.UDPAddrFromAddrPort(cands[0]), nil
}

// orderFamilies puts IPv4 addresses before IPv6 ones, keeping the
// resolver's order within each: the rendezvous server and its filters know a
// peer by the address it reaches them from, and IPv4 is what has always
// been used.
func orderFamilies(ips []netip.Addr) []netip.Addr {
	var v4, v6 []netip.Addr
	for _, ip := range ips {
		if ip = ip.Unmap(); ip.Is4() {
			v4 = append(v4, ip)
		} else {
			v6 = append(v6, ip)
		}
	}
	return append(v4, v6...)
}

// routeTo reports whether the machine has a route to ap. Connecting a UDP
// socket only consults the routing table; nothing is sent.
func routeTo(ap netip.AddrPort) bool {
	c, err := net.DialUDP("udp", nil, net.UDPAddrFromAddrPort(ap))
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// routeSource returns the local address the machine sends from towards ap,
// or false when it has no route there.
func routeSource(ap netip.AddrPort) (netip.Addr, bool) {
	c, err := net.DialUDP("udp", nil, net.UDPAddrFromAddrPort(ap))
	if err != nil {
		return netip.Addr{}, false
	}
	defer c.Close()
	la, ok := c.LocalAddr().(*net.UDPAddr)
	if !ok {
		return netip.Addr{}, false
	}
	a, ok := netip.AddrFromSlice(la.IP)
	return a.Unmap(), ok && !a.IsUnspecified()
}
