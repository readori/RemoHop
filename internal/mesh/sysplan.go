package mesh

import (
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"

	"github.com/readori/RemoHop/internal/svc/vpn"
)

// The system commands of the macOS and Windows data planes, built here
// without build tags so that the tests run on every platform; sys_darwin.go
// and sys_windows.go only run them.

// darwinResolverFile is where macOS looks for the resolver of one domain
// (resolver(5)). The file is ours alone: no other program writes a file for
// the .remohop domain.
const darwinResolverFile = "/etc/resolver/" + Domain

// darwinConfigureCmds gives a utun interface its mesh addresses. A utun is
// point to point: the address is set as both ends with a host mask, and the
// mesh network is routed to the interface explicitly. Both go with the
// interface. v6 holds the IPv6 commands, which may fail on their own.
func darwinConfigureCmds(dev string, a4, a6 netip.Prefix, mtu int) (v4, v6 [][]string) {
	v4 = [][]string{
		{"ifconfig", dev, "inet", a4.Addr().String(), a4.Addr().String(), "netmask", "255.255.255.255", "mtu", strconv.Itoa(mtu), "up"},
		darwinRouteCmd(dev, a4.Masked()),
	}
	if a6.Addr().IsValid() {
		v6 = [][]string{
			{"ifconfig", dev, "inet6", a6.Addr().String(), "prefixlen", strconv.Itoa(a6.Bits())},
			{"route", "-n", "add", "-inet6", "-net", a6.Masked().String(), "-interface", dev},
		}
	}
	return v4, v6
}

// darwinRouteCmd routes an IPv4 network to the interface.
func darwinRouteCmd(dev string, p netip.Prefix) []string {
	return []string{"route", "-n", "add", "-net", p.String(), "-interface", dev}
}

// darwinResolverContent is the resolver(5) file for the mesh's names.
func darwinResolverContent(server netip.Addr) string {
	return "# Written by remohop for `remohop net`; removed when the mesh stops.\n" +
		"nameserver " + server.String() + "\nport 53\ntimeout 2\n"
}

// windowsConfigureCmds gives the Wintun adapter (interface index idx) its
// addresses and MTU, for this boot only (store=active), and routes the
// mesh networks to it. The mask would make the network on-link by itself,
// but Windows adds that route only once it considers the address usable,
// which on a CI runner had not happened a second later; the explicit route
// does not wait ("already exists" when Windows was first is fine).
func windowsConfigureCmds(idx string, a4, a6 netip.Prefix, mtu int) (v4, v6 [][]string) {
	m := net.IP(net.CIDRMask(a4.Bits(), 32)).String()
	v4 = [][]string{
		{"netsh", "interface", "ipv4", "add", "address", "name=" + idx, "address=" + a4.Addr().String(), "mask=" + m, "store=active"},
		{"netsh", "interface", "ipv4", "set", "subinterface", idx, "mtu=" + strconv.Itoa(mtu), "store=active"},
		windowsRouteCmd(idx, a4.Masked()),
	}
	if a6.Addr().IsValid() {
		v6 = [][]string{
			{"netsh", "interface", "ipv6", "add", "address", "interface=" + idx, "address=" + a6.String(), "store=active"},
			{"netsh", "interface", "ipv6", "set", "subinterface", idx, "mtu=" + strconv.Itoa(mtu), "store=active"},
			{"netsh", "interface", "ipv6", "add", "route", "prefix=" + a6.Masked().String(), "interface=" + idx, "store=active"},
		}
	}
	return v4, v6
}

// windowsRouteCmd routes an IPv4 network to the adapter, on-link.
func windowsRouteCmd(idx string, p netip.Prefix) []string {
	return []string{"netsh", "interface", "ipv4", "add", "route", "prefix=" + p.String(), "interface=" + idx, "nexthop=0.0.0.0", "store=active"}
}

// nrptComment marks the Name Resolution Policy Table rule of the mesh, so
// that removing it never touches another program's rules.
const nrptComment = "remohop-net"

func powershell(script string) []string { return vpn.PowerShell(script) }

// nrptRemoveCmd removes the mesh's NRPT rules (any left by an earlier run
// too) and forgets cached answers for the domain.
func nrptRemoveCmd() []string { return vpn.NRPTRemoveCmd(nrptComment) }

// nrptAddCmd sends queries for .remohop, and only those, to the mesh's
// responder.
func nrptAddCmd(server netip.Addr) []string {
	return powershell(fmt.Sprintf("Add-DnsClientNrptRule -Namespace '.%s' -NameServers '%s' -Comment '%s' | Out-Null; Clear-DnsClientCache",
		Domain, server, nrptComment))
}

// overlapping returns the first IPv4 network in used that overlaps p;
// default routes do not count.
func overlapping(p netip.Prefix, used []netip.Prefix) (netip.Prefix, bool) {
	for _, r := range used {
		if !r.Addr().Is4() || r.Bits() == 0 {
			continue
		}
		if r.Overlaps(p) {
			return r, true
		}
	}
	return netip.Prefix{}, false
}

func errOverlap(p, r netip.Prefix) error {
	return fmt.Errorf("the mesh network %s overlaps %s, which this machine already uses; "+
		"the account's policy can move the mesh elsewhere (\"prefix4\", see `remohop help net`)", p, r)
}

// alreadyExists tells a failed "add" of something that is there already
// (a route a second link offers again, say) from a real failure.
func alreadyExists(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "file exists") || strings.Contains(s, "already exists") || strings.Contains(s, "object already exists")
}
