package nat

import (
	"net"
	"net/netip"

	"github.com/readori/RemoHop/internal/tailnet"
)

// MaxCandidates bounds how many local addresses a peer advertises. The
// rendezvous server enforces the same bound; it also caps how many
// addresses a stranger can make us send probes to.
const MaxCandidates = 6

// InterfaceAddrs returns the addresses of this machine's active network
// interfaces that a peer might reach: every IPv4 address except loopback
// and link-local (private ones matter most: they are how two machines on
// the same LAN find each other), and global or unique-local IPv6 addresses.
// IPv4 comes first, except that the addresses of a Tailscale interface come
// before everything: they work through any NAT when both peers are on the
// same tailnet (see internal/tailnet), so they must not fall victim to the
// MaxCandidates cap on a machine with many interfaces.
func InterfaceAddrs() []netip.Addr {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var overlay, v4, v6 []netip.Addr
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		var ips []netip.Addr
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip, ok := netip.AddrFromSlice(ipn.IP)
			if !ok {
				continue
			}
			ip = ip.Unmap()
			if !usableLocal(ip) {
				continue
			}
			ips = append(ips, ip)
		}
		tailnetIf := !tailnet.Disabled() && tailnet.IsInterface(ifc.Name, ips)
		for _, ip := range ips {
			switch {
			case tailnetIf && tailnet.InRange(ip):
				overlay = append(overlay, ip)
			case ip.Is4():
				v4 = append(v4, ip)
			default:
				v6 = append(v6, ip)
			}
		}
	}
	return append(overlay, append(v4, v6...)...)
}

func usableLocal(ip netip.Addr) bool {
	return ip.IsValid() && !ip.IsLoopback() && !ip.IsUnspecified() &&
		!ip.IsLinkLocalUnicast() && !ip.IsMulticast()
}

// normalize strips the IPv4-in-IPv6 mapping a dual-stack socket reports, so
// one peer address has one representation.
func normalize(ap netip.AddrPort) netip.AddrPort {
	return netip.AddrPortFrom(ap.Addr().Unmap().WithZone(""), ap.Port())
}

func addrPortOf(a net.Addr) (netip.AddrPort, bool) {
	ua, ok := a.(*net.UDPAddr)
	if !ok {
		return netip.AddrPort{}, false
	}
	ap := ua.AddrPort()
	if !ap.IsValid() {
		return netip.AddrPort{}, false
	}
	return normalize(ap), true
}

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// lanLike reports whether ip is an address that only means something on a
// local network (RFC 1918, unique-local IPv6, link-local, loopback), as
// opposed to an address a NAT might be listening on.
func lanLike(ip netip.Addr) bool {
	return ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast()
}

// Routable reports whether ip is a globally routable address: one that
// packets sent blindly could bother a stranger at. Carrier-grade NAT space
// is not; like RFC 1918 space it never leaves the network it is used in.
func Routable(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsValid() && ip.IsGlobalUnicast() && !lanLike(ip) && !cgnat.Contains(ip)
}
