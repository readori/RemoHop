package vpn

import (
	"net/netip"
)

// What other packages (the mesh network, internal/mesh) reuse of this one:
// the tunnel device of each OS, the record of system changes that is undone
// when a tunnel ends or after a crash, the client's routes (--route and
// --exit) and the host's forwarding.

// Open creates a tunnel interface (TUN on Linux, utun on macOS, Wintun on
// Windows). tunnel names it for systems that give interfaces stable
// identities.
func Open(tunnel string) (Device, error) { return sys.open(tunnel) }

// Privileged returns nil if this process may create interfaces and change
// routes, or an error that says what is missing.
func Privileged() error { return sys.privileged() }

// Changes records changes to the system as they are made, each with the
// command that undoes it, so that Undo (or the next remohop start, after a
// crash) reverts them.
type Changes struct{ st *state }

// NewChanges starts a record for a tunnel interface. role names the user
// ("mesh", say) in the state file's name.
func NewChanges(role string, dev Device) (*Changes, error) {
	st, err := newState(role, dev.Name())
	if err != nil {
		return nil, err
	}
	if u, ok := dev.(interface{ undoSteps() []undoStep }); ok {
		for _, step := range u.undoSteps() {
			st.record(step)
		}
	}
	return &Changes{st: st}, nil
}

// Record notes the command that undoes a change about to be made. Only the
// tools this package runs itself are accepted (ip, iptables, nft, route,
// resolvectl, netsh, pnputil).
func (c *Changes) Record(undo ...string) error { return c.st.record(undoStep{Cmd: undo}) }

// Undo reverts every recorded change, newest first. Safe to call twice.
func (c *Changes) Undo() { c.st.undo() }

// Command runs a system tool (with the capabilities this process holds),
// returning its output in the error on failure.
func Command(argv ...string) error { return run(argv...) }

// CommandOutput is Command returning the tool's output.
func CommandOutput(argv ...string) (string, error) { return output(argv...) }

// ClientRoutes installs routes through dev: routes, and with exit all
// traffic except the local networks, keep (addresses that must stay on
// their current path, such as the far end of the sessions the tunnel runs
// on) and bypass. dns are resolvers to use while exit is on, where the
// system allows that per interface. It returns notes for the user.
func ClientRoutes(c *Changes, dev string, routes []netip.Prefix, exit bool, keep []netip.Addr, bypass []netip.Prefix, dns []netip.Addr) ([]string, error) {
	return sys.clientRoutes(c.st, dev, clientRouting{routes: routes, exit: exit, keep: keep, bypass: bypass, dns: dns})
}

// Forward makes this machine forward packets from src arriving on dev to
// its other networks, hiding them behind its own address, and lets only
// replies back in. release undoes the part shared with other tunnels.
func Forward(c *Changes, dev string, src netip.Prefix) (release func(), err error) {
	if err := sys.hostSupported(); err != nil {
		return nil, err
	}
	if err := forwardPrefix(c.st, dev, src); err != nil {
		return nil, err
	}
	return sys.hostRelease, nil
}

// ProcessAlive reports whether the process pid runs (for the status of a
// daemon that left a state file).
func ProcessAlive(pid int) bool { return processAlive(pid) }

// UsedNets lists the IPv4 networks this machine already uses: those of its
// interfaces and, where the platform reads them, of its routes.
func UsedNets() []netip.Prefix { return sys.usedNets() }

// IsTunnelName reports whether an interface name is one of RemoHop's
// tunnel interfaces (on macOS any utun, which may also be another program's).
func IsTunnelName(name string) bool { return isTunnelName(name) }
