//go:build linux

package mesh

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/readori/RemoHop/internal/svc/vpn"
)

func sysSupported() error { return nil }

// processAlive reports whether pid runs; a zombie (killed, not yet reaped
// by its parent) does not.
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	if err != nil && !errors.Is(err, syscall.EPERM) {
		return false
	}
	if b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil {
		if i := strings.LastIndexByte(string(b), ')'); i > 0 && i+2 < len(b) && b[i+2] == 'Z' {
			return false
		}
	}
	return true
}

// sysConfigure gives the interface its addresses and brings it up. The
// addresses, and the routes they imply, vanish with the interface.
func sysConfigure(dev string, a4, a6 netip.Prefix, mtu int) (v6 bool, notes []string, err error) {
	if err := vpn.Command("ip", "addr", "add", a4.String(), "dev", dev); err != nil {
		return false, nil, err
	}
	if err := vpn.Command("ip", "link", "set", "dev", dev, "mtu", fmt.Sprint(mtu), "up"); err != nil {
		return false, nil, err
	}
	if a6.Addr().IsValid() {
		if err := vpn.Command("ip", "-6", "addr", "add", a6.String(), "dev", dev, "nodad"); err != nil {
			notes = append(notes, "IPv6 is not available on this machine; the mesh runs on IPv4 only here")
		} else {
			v6 = true
		}
	}
	return v6, notes, nil
}

// sysRoute routes p through the interface (gone with it).
func sysRoute(dev string, p netip.Prefix) error {
	return vpn.Command("ip", "route", "replace", p.String(), "dev", dev)
}

// checkPrefixFree refuses a mesh network that overlaps a network this
// machine already routes (a LAN that happens to use 10.78.0.0/16, say).
func checkPrefixFree(p netip.Prefix) error {
	out, err := vpn.CommandOutput("ip", "-4", "route", "show", "table", "all")
	if err != nil {
		return nil // cannot tell; the address assignment will complain
	}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		dst := f[0]
		if dst == "local" || dst == "broadcast" || dst == "unicast" {
			if len(f) < 2 {
				continue
			}
			dst = f[1]
		}
		if dst == "default" {
			continue
		}
		if !strings.Contains(dst, "/") {
			dst += "/32"
		}
		r, err := netip.ParsePrefix(dst)
		if err != nil || !r.Addr().Is4() || r.Bits() == 0 {
			continue
		}
		if r.Overlaps(p) {
			// Our own interface from an earlier run that is going away.
			if strings.Contains(line, " dev remohop") && strings.Contains(line, "proto kernel") {
				continue
			}
			return fmt.Errorf("the mesh network %s overlaps %s, which this machine already uses (%s); "+
				"the account's policy can move the mesh elsewhere (\"prefix4\", see `remohop help net`)", p, r, strings.TrimSpace(line))
		}
	}
	return nil
}

// sysSplitDNS sends queries for .remohop to the responder through
// systemd-resolved's per-link settings, which go away with the link. A
// note with false means split DNS was possible in principle but is unsafe
// here.
func sysSplitDNS(dev string, server netip.Addr) (string, bool) {
	if _, err := exec.LookPath("resolvectl"); err != nil {
		return "", false
	}
	// Never from another network namespace than resolved's: the setting
	// would go to the host interface with the same index (see
	// vpn.ResolvedSharesNetns), typically its uplink, and stay there.
	if ok, why := vpn.ResolvedSharesNetns(); !ok {
		return fmt.Sprintf("DNS: systemd-resolved left alone (%s); names like *.%s are answered at %s:53, "+
			"or use `remohop config set net-dns hosts`", why, Domain, server), false
	}
	if vpn.Command("resolvectl", "dns", dev, server.String()) != nil {
		return "", false
	}
	if vpn.Command("resolvectl", "domain", dev, "~"+Domain) != nil {
		return "", false
	}
	// Only .remohop goes here, never other names.
	vpn.Command("resolvectl", "default-route", dev, "false")
	return "DNS: .remohop names go to the mesh's responder through systemd-resolved (only those)", true
}
