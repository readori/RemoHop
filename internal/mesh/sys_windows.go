//go:build windows

package mesh

import (
	"net/netip"

	"github.com/readori/RemoHop/internal/svc/vpn"
)

// Windows: a Wintun adapter configured with netsh (see sysplan.go), split
// DNS through a Name Resolution Policy Table rule. Serving as an exit node
// or subnet router would need WinNAT and is refused by vpn.Forward; using
// an exit node works. Windows Firewall still applies to connections that
// other devices make over the mesh.

func sysSupported() error { return nil }

func processAlive(pid int) bool { return vpn.ProcessAlive(pid) }

func sysConfigure(dev string, a4, a6 netip.Prefix, mtu int) (v6 bool, notes []string, err error) {
	idx, err := vpn.IfIndex(dev)
	if err != nil {
		return false, nil, err
	}
	v4cmds, v6cmds := windowsConfigureCmds(idx, a4, a6, mtu)
	for _, c := range v4cmds {
		if err := vpn.Command(c...); err != nil && !alreadyExists(err) {
			return false, nil, err
		}
	}
	notes = append(notes, "Windows Firewall applies to connections from other devices of the mesh as to any network; "+
		"allow a program or port to reach it from them")
	if len(v6cmds) == 0 {
		return false, notes, nil
	}
	for _, c := range v6cmds {
		if err := vpn.Command(c...); err != nil && !alreadyExists(err) {
			return false, append(notes, "IPv6 could not be set up on "+dev+"; the mesh runs on IPv4 only here"), nil
		}
	}
	return true, notes, nil
}

func sysRoute(dev string, p netip.Prefix) error {
	idx, err := vpn.IfIndex(dev)
	if err != nil {
		return err
	}
	if err := vpn.Command(windowsRouteCmd(idx, p)...); err != nil && !alreadyExists(err) {
		return err
	}
	return nil
}

// checkPrefixFree refuses a mesh network that overlaps a network this
// machine's interfaces or routes already use. It runs before the mesh's
// own adapter exists.
func checkPrefixFree(p netip.Prefix) error {
	if r, ok := overlapping(p, vpn.UsedNets()); ok {
		return errOverlap(p, r)
	}
	return nil
}

// sysSplitDNS adds an NRPT rule that sends .remohop queries, and only
// those, to the mesh's responder. Its removal is recorded with the other
// changes, so a crash does not leave it behind; a rule left by an earlier
// run is removed first.
func sysSplitDNS(c *vpn.Changes, _ string, server netip.Addr) (string, bool) {
	vpn.Command(nrptRemoveCmd()...)
	if c != nil {
		if err := c.RecordNRPTRemoval(nrptComment); err != nil {
			return "", false
		}
	}
	if err := vpn.Command(nrptAddCmd(server)...); err != nil {
		return "DNS: could not add a name resolution policy rule for ." + Domain + " (" + err.Error() + "); names are answered at " +
			server.String() + ":53, or use `remohop config set net-dns hosts`", false
	}
	return "DNS: .remohop names go to the mesh's responder through a name resolution policy rule (only those)", true
}
