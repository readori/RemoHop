//go:build windows

package vpn

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os/exec"
	"strconv"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/readori/RemoHop/internal/nowindow"
)

// ifPrefix starts the name of every tunnel adapter (RemoHop-1a2b).
const ifPrefix = "RemoHop"

// Windows support covers the client (and a host that is reached itself
// through the tunnel). Addresses, routes and DNS are set with netsh, the
// same tool and the same kind of recorded undo command as `ip` on Linux;
// everything set on the tunnel adapter goes away with it. Routes are read
// through the IP Helper API (x/sys/windows), which needs no extra module;
// golang.zx2c4.com/wireguard/windows/tunnel/winipcfg would do both, at the
// price of a large dependency tree for a handful of calls.
func newPlatform() platform {
	return platform{
		privileged: func() error {
			if windows.GetCurrentProcessToken().IsElevated() {
				return nil
			}
			return errors.New("a VPN tunnel needs Administrator rights on Windows: run the command from an elevated prompt " +
				"(right-click the terminal, Run as administrator), or, for a host, run it as a service with `remohop service install --system`")
		},
		hostSupported: func() error {
			// Forwarding with address translation needs WinNAT
			// (New-NetNat), which only some editions have (it comes with
			// Hyper-V and containers), or Internet Connection Sharing,
			// which takes over the whole machine's sharing setup and does
			// not undo cleanly. Neither is good enough to rely on.
			return errors.New("a Windows machine cannot be a VPN host for --route or --exit yet (it can only be reached itself through " +
				"the tunnel); use a Linux host to reach a LAN or the internet through it")
		},
		open:         openWintun,
		usedNets:     windowsUsedNets,
		configure:    windowsConfigure,
		clientRoutes: windowsClientRoutes,
		hostForward:  func(*state, string, netip.Addr) error { return errors.New("not supported on Windows") },
		hostRelease:  func() {},
		resolvers:    func() []string { return nil },
	}
}

func processAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == 259 // STILL_ACTIVE
}

// runPrivileged runs a system tool without flashing a console window. A
// variable so tests can record commands instead of running them.
var runPrivileged = func(cmd *exec.Cmd) ([]byte, error) {
	nowindow.Set(cmd)
	return cmd.CombinedOutput()
}

// route is one IPv4 route from the system table.
type route struct {
	dst     netip.Prefix
	ifIndex uint32
	nextHop netip.Addr // unspecified for on-link routes
	metric  uint32
}

// routeTable reads the IPv4 routing table. A variable for tests.
var routeTable = func() ([]route, error) {
	var table *windows.MibIpForwardTable2
	if err := windows.GetIpForwardTable2(windows.AF_INET, &table); err != nil {
		return nil, err
	}
	defer windows.FreeMibTable(unsafe.Pointer(table))
	var out []route
	for _, r := range table.Rows() {
		dst, ok1 := sockaddrIPv4(&r.DestinationPrefix.Prefix)
		hop, ok2 := sockaddrIPv4(&r.NextHop)
		if !ok1 || !ok2 || r.DestinationPrefix.PrefixLength > 32 {
			continue
		}
		out = append(out, route{dst: netip.PrefixFrom(dst, int(r.DestinationPrefix.PrefixLength)).Masked(),
			ifIndex: r.InterfaceIndex, nextHop: hop, metric: r.Metric})
	}
	return out, nil
}

func sockaddrIPv4(sa *windows.RawSockaddrInet) (netip.Addr, bool) {
	if sa.Family != windows.AF_INET {
		return netip.Addr{}, false
	}
	in4 := (*windows.RawSockaddrInet4)(unsafe.Pointer(sa))
	return netip.AddrFrom4(in4.Addr), true
}

func windowsUsedNets() []netip.Prefix {
	out := interfaceNets()
	routes, _ := routeTable()
	for _, r := range routes {
		if r.dst.Bits() > 0 && r.dst.Bits() < 32 {
			out = append(out, r.dst)
		}
	}
	return out
}

// lookupRoute finds how dst is reached now outside the tunnels: the
// longest matching route, lowest metric among equals.
func lookupRoute(dst netip.Addr, tunnels map[uint32]bool) (route, error) {
	routes, err := routeTable()
	if err != nil {
		return route{}, err
	}
	best, found := route{}, false
	for _, r := range routes {
		if tunnels[r.ifIndex] || !r.dst.Contains(dst) {
			continue
		}
		if !found || r.dst.Bits() > best.dst.Bits() || r.dst.Bits() == best.dst.Bits() && r.metric < best.metric {
			best, found = r, true
		}
	}
	if !found {
		return route{}, fmt.Errorf("no route to %s outside the tunnel", dst)
	}
	return best, nil
}

// tunnelIndexes lists the interface indexes of RemoHop adapters.
func tunnelIndexes() map[uint32]bool {
	out := map[uint32]bool{}
	ifaces, _ := net.Interfaces()
	for _, ifc := range ifaces {
		if isTunnelName(ifc.Name) {
			out[uint32(ifc.Index)] = true
		}
	}
	return out
}

// interfaceIndex is the index of a tunnel adapter by name. A variable for
// tests.
var interfaceIndex = func(dev string) (string, error) {
	if idx, ok := adapters.Load(dev); ok {
		return strconv.FormatUint(uint64(idx.(uint32)), 10), nil
	}
	ifc, err := net.InterfaceByName(dev)
	if err != nil {
		return "", fmt.Errorf("tunnel adapter %s: %w", dev, err)
	}
	return strconv.Itoa(ifc.Index), nil
}

func mask(bits int) string {
	m := net.CIDRMask(bits, 32)
	return net.IP(m).String()
}

// windowsConfigure gives the adapter its address and MTU. Both belong to
// the adapter and vanish with it. "active" means: not stored, not even
// until the next reboot.
func windowsConfigure(dev string, local, peer netip.Addr, mtu int) error {
	idx, err := interfaceIndex(dev)
	if err != nil {
		return err
	}
	// The tunnel's /30 holds both ends, so the peer is on-link.
	if err := run("netsh", "interface", "ipv4", "add", "address", "name="+idx, "address="+local.String(), "mask="+mask(30), "store=active"); err != nil {
		return err
	}
	return run("netsh", "interface", "ipv4", "set", "subinterface", idx, "mtu="+strconv.Itoa(mtu), "store=active")
}

func addRoute(prefix netip.Prefix, idx string, hop netip.Addr) []string {
	if !hop.IsValid() || hop.IsUnspecified() {
		hop = netip.IPv4Unspecified()
	}
	return []string{"netsh", "interface", "ipv4", "add", "route", "prefix=" + prefix.String(), "interface=" + idx, "nexthop=" + hop.String(), "store=active"}
}

func delRoute(prefix netip.Prefix, idx string, hop netip.Addr) []string {
	cmd := addRoute(prefix, idx, hop)
	cmd[3] = "delete"
	return cmd
}

func windowsClientRoutes(st *state, dev string, o clientRouting) (notes []string, err error) {
	idx, err := interfaceIndex(dev)
	if err != nil {
		return nil, err
	}
	// Routes on the tunnel adapter need no undo: they go with it.
	for _, r := range o.routes {
		if err := run(addRoute(r, idx, netip.Addr{})...); err != nil {
			return nil, err
		}
	}
	if !o.exit {
		return nil, nil
	}
	tunnels := tunnelIndexes()
	keep := append([]netip.Prefix(nil), o.bypass...)
	for _, a := range o.keep {
		keep = append(keep, netip.PrefixFrom(a, 32))
	}
	for _, k := range keep {
		r, err := lookupRoute(k.Addr(), tunnels)
		if err != nil {
			return nil, fmt.Errorf("keeping %s off the tunnel: %w", k, err)
		}
		via := strconv.Itoa(int(r.ifIndex))
		if err := st.record(undoStep{Cmd: delRoute(k, via, r.nextHop)}); err != nil {
			return nil, err
		}
		if err := run(addRoute(k, via, r.nextHop)...); err != nil {
			return nil, err
		}
	}
	// Two halves instead of replacing the default route, as on Linux.
	for _, half := range []string{"0.0.0.0/1", "128.0.0.0/1"} {
		if err := run(addRoute(netip.MustParsePrefix(half), idx, netip.Addr{})...); err != nil {
			return nil, err
		}
	}
	if len(o.dns) > 0 {
		for i, d := range o.dns {
			args := []string{"netsh", "interface", "ipv4", "add", "dnsservers", "name=" + idx, "address=" + d.String(), "index=" + strconv.Itoa(i+1), "validate=no"}
			if i == 0 {
				args = []string{"netsh", "interface", "ipv4", "set", "dnsservers", "name=" + idx, "source=static", "address=" + d.String(), "register=none", "validate=no"}
			}
			if err := run(args...); err != nil {
				return nil, err
			}
		}
		// The lowest interface metric makes Windows prefer this adapter's
		// resolvers; its "smart multi-homed name resolution" may still ask
		// the others' in parallel.
		if err := run("netsh", "interface", "ipv4", "set", "interface", idx, "metric=1", "store=active"); err != nil {
			return nil, err
		}
		notes = append(notes, "DNS goes to the host's resolvers through the tunnel (Windows may also ask the other interfaces' resolvers)")
	}
	return notes, nil
}
