package nat

import (
	"net"
	"net/netip"
	"slices"
	"strings"

	"github.com/readori/RemoHop/internal/tailnet"
)

// InitialPacketSize is the UDP payload size every QUIC connection on a
// RemoHop socket starts with: the smallest QUIC allows. quic-go's default
// (1280 bytes, 1308 on the wire over IPv4) is more than paths through a
// WireGuard, Tailscale or other tunnel at MTU 1280, many corporate VPNs and
// some IPv6 tunnels carry, and the DF bit it sets turns such a path into a
// black hole: the handshake never completes while the small punch probes
// pass. Path MTU discovery raises the size within a few round trips where
// the path allows. Every quic.Config RemoHop uses sets it.
const InitialPacketSize = 1200

// MaxCandidates bounds how many local addresses a peer advertises. The
// rendezvous server enforces the same bound; it also caps how many
// addresses a stranger can make us send probes to.
const MaxCandidates = 6

// InterfaceAddrs returns the addresses of this machine's active network
// interfaces that a peer might reach: every IPv4 address except loopback
// and link-local (private ones matter most: they are how two machines on
// the same LAN find each other), and global or unique-local IPv6 addresses;
// not those in a network the mesh registered (see ExcludeNetworks).
//
// Only the first MaxCandidates are advertised, so the order matters:
//
//   - the addresses of a Tailscale interface: they work through any NAT
//     when both peers are on the same tailnet (see internal/tailnet);
//   - the addresses the machine sends from on its default routes, IPv4 then
//     IPv6: the real LAN address, and the IPv6 address a punch's packets
//     actually leave from (a stateful firewall opens only for that one);
//   - other IPv4, then IPv6, addresses of interfaces that are up and do not
//     look virtual;
//   - last, those of virtual adapters (Hyper-V and WSL switches, Docker
//     and libvirt bridges, VMware, VirtualBox) and of interfaces without a
//     carrier, which on a developer's machine could otherwise crowd the
//     real LAN address out of the list.
func InterfaceAddrs() []netip.Addr {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	// The documentation prefixes route like any address beyond the LAN;
	// connecting a UDP socket to them sends nothing.
	src4, _ := routeSource(netip.MustParseAddrPort("192.0.2.1:9"))
	src6, _ := routeSource(netip.MustParseAddrPort("[2001:db8::1]:9"))
	var overlay, primary4, primary6, v4, v6, late4, late6 []netip.Addr
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
			if !usableLocal(ip) || Excluded(ip) {
				continue
			}
			ips = append(ips, ip)
		}
		tailnetIf := !tailnet.Disabled() && tailnet.IsInterface(ifc.Name, ips)
		late := virtualInterface(ifc.Name) || ifc.Flags&net.FlagRunning == 0
		for _, ip := range ips {
			switch {
			case tailnetIf && tailnet.InRange(ip):
				overlay = append(overlay, ip)
			case ip == src4:
				primary4 = append(primary4, ip)
			case ip == src6:
				primary6 = append(primary6, ip)
			case late && ip.Is4():
				late4 = append(late4, ip)
			case late:
				late6 = append(late6, ip)
			case ip.Is4():
				v4 = append(v4, ip)
			default:
				v6 = append(v6, ip)
			}
		}
	}
	return slices.Concat(overlay, primary4, primary6, v4, v6, late4, late6)
}

// virtualInterface reports whether an interface name is one that
// hypervisors, container engines and the like create on the host.
func virtualInterface(name string) bool {
	n := strings.ToLower(name)
	for _, p := range []string{"docker", "br-", "veth", "virbr", "vmnet", "vboxnet", "lxcbr", "lxdbr", "cni", "podman",
		"flannel", "vethernet", "vmware", "bridge", "vmenet"} {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	return strings.Contains(n, "virtual") // VirtualBox, "Hyper-V Virtual Ethernet Adapter"
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
