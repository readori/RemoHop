package vpn

import (
	"errors"
	"net"
	"net/netip"
	"strings"
)

// platform is everything the tunnel needs from the operating system. The
// real one is chosen per OS; tests substitute fakes.
type platform struct {
	// privileged returns nil if this process may create interfaces and
	// change routes, or an error that says exactly what is missing.
	privileged func() error
	// hostSupported returns nil if this OS can forward for a client.
	hostSupported func() error
	// open creates a tunnel interface. tunnel names the tunnel (its peer
	// and role) for systems that give interfaces stable identities.
	open func(tunnel string) (Device, error)
	// usedNets lists the networks already routed or assigned here.
	usedNets func() []netip.Prefix
	// configure gives the interface its addresses and MTU and brings it up.
	configure func(dev string, local, peer netip.Addr, mtu int) error
	// clientRoutes installs the client's routes; notes are things the user
	// should be told.
	clientRoutes func(st *state, dev string, o clientRouting) (notes []string, err error)
	// hostForward makes the host forward and masquerade for client.
	hostForward func(st *state, dev string, client netip.Addr) error
	// hostRelease undoes the part of hostForward shared between tunnels.
	hostRelease func()
	// resolvers lists the DNS servers this machine uses.
	resolvers func() []string
}

type clientRouting struct {
	routes []netip.Prefix
	exit   bool
	// keep are addresses that must stay off the tunnel when exit is set:
	// the far end of the session the tunnel itself runs on.
	keep   []netip.Addr
	bypass []netip.Prefix
	dns    []netip.Addr
}

var sys = newPlatform()

// errPrivilege is the full story for a process that lacks the privilege.
func errPrivilege(what string) error {
	return errors.New(what + " needs the right to create network interfaces and change routes (root, or the CAP_NET_ADMIN capability). Options: " +
		"run this command with sudo; or grant the binary the capability once with `sudo setcap cap_net_admin,cap_net_raw+ep $(command -v remohop)`; " +
		"or, for a host, run it as a system service with `sudo remohop service install --system`")
}

// connected is connectedNets, replaceable in tests.
var connected = connectedNets

// connectedNets lists the networks this machine is directly attached to,
// tunnels and loopback aside.
func connectedNets() []netip.Prefix {
	var out []netip.Prefix
	ifaces, _ := net.Interfaces()
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 || isTunnelName(ifc.Name) {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok {
				if p, err := netip.ParsePrefix(ipn.String()); err == nil && p.Addr().Is4() {
					out = append(out, p.Masked())
				}
			}
		}
	}
	return out
}

// interfaceNets lists every IPv4 network assigned to an interface.
func interfaceNets() []netip.Prefix {
	var out []netip.Prefix
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok {
			if p, err := netip.ParsePrefix(ipn.String()); err == nil && p.Addr().Is4() {
				out = append(out, p.Masked())
			}
		}
	}
	return out
}

func isTunnelName(name string) bool { return strings.HasPrefix(name, ifPrefix) }

// isLocalRoute reports whether r lies inside a network this machine is
// directly attached to.
func isLocalRoute(r netip.Prefix, connected []netip.Prefix) bool {
	for _, c := range connected {
		if c.Bits() <= r.Bits() && c.Contains(r.Addr()) {
			return true
		}
	}
	return false
}
