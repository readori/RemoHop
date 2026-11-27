//go:build darwin

package mesh

import (
	"fmt"
	"net/netip"
	"os"

	"github.com/readori/RemoHop/internal/svc/vpn"
)

// macOS: a utun interface configured with ifconfig and route (see
// sysplan.go), split DNS through /etc/resolver. Serving as an exit node or
// subnet router would need pf NAT rules and is refused by vpn.Forward;
// using an exit node works.

func sysSupported() error { return nil }

func processAlive(pid int) bool { return vpn.ProcessAlive(pid) }

func sysConfigure(dev string, a4, a6 netip.Prefix, mtu int) (v6 bool, notes []string, err error) {
	v4cmds, v6cmds := darwinConfigureCmds(dev, a4, a6, mtu)
	for _, c := range v4cmds {
		if err := vpn.Command(c...); err != nil && !alreadyExists(err) {
			return false, nil, err
		}
	}
	if len(v6cmds) == 0 {
		return false, nil, nil
	}
	for _, c := range v6cmds {
		if err := vpn.Command(c...); err != nil && !alreadyExists(err) {
			return false, []string{"IPv6 could not be set up on " + dev + "; the mesh runs on IPv4 only here"}, nil
		}
	}
	return true, nil, nil
}

func sysRoute(dev string, p netip.Prefix) error {
	if err := vpn.Command(darwinRouteCmd(dev, p)...); err != nil && !alreadyExists(err) {
		return err
	}
	return nil
}

// checkPrefixFree refuses a mesh network that overlaps a network of one
// of this machine's interfaces (another VPN's included). It runs before
// the mesh's own interface exists.
func checkPrefixFree(p netip.Prefix) error {
	if r, ok := overlapping(p, vpn.UsedNets()); ok {
		return errOverlap(p, r)
	}
	return nil
}

// sysSplitDNS writes /etc/resolver/remohop, which sends .remohop queries,
// and only those, to the mesh's responder. Removing the file is recorded
// with the other changes, so a crash does not leave it behind.
func sysSplitDNS(c *vpn.Changes, _ string, server netip.Addr) (string, bool) {
	if err := os.MkdirAll("/etc/resolver", 0o755); err != nil {
		return fmt.Sprintf("DNS: could not create /etc/resolver (%v); names like *.%s are answered at %s:53, "+
			"or use `remohop config set net-dns hosts`", err, Domain, server), false
	}
	if c != nil {
		if err := c.Record("rm", "-f", darwinResolverFile); err != nil {
			return "", false
		}
	}
	if err := os.WriteFile(darwinResolverFile, []byte(darwinResolverContent(server)), 0o644); err != nil {
		return fmt.Sprintf("DNS: could not write %s (%v); names like *.%s are answered at %s:53", darwinResolverFile, err, Domain, server), false
	}
	return "DNS: .remohop names go to the mesh's responder through " + darwinResolverFile + " (only those)", true
}
