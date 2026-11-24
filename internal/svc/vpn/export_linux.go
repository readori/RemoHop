//go:build linux

package vpn

import (
	"net/netip"
	"os"
	"path/filepath"
	"strings"
)

// ResolvedSharesNetns reports whether systemd-resolved runs in this
// process's network namespace, and if not, why per-link DNS must be left
// alone. resolvectl turns an interface name into an index in the caller's
// namespace and sends that number over D-Bus (a socket in the file system,
// shared across namespaces); resolved applies it to the link with that index
// in its own namespace. From `ip netns exec`, a container with the host's
// bus, or anything else in another network namespace, `resolvectl dns
// remohop0 ...` therefore reconfigures whatever host interface has the same
// index: a fresh namespace's first new interface is index 2, which on most
// machines is the uplink. Where it cannot tell (no permission to look at the
// other process), it says yes, as before.
func ResolvedSharesNetns() (bool, string) {
	self, err := os.Readlink("/proc/self/ns/net")
	if err != nil {
		return true, ""
	}
	other, what := "", ""
	if pid := resolvedPID(); pid != "" {
		other, err = os.Readlink("/proc/" + pid + "/ns/net")
		what = "systemd-resolved"
	} else {
		other, err = os.Readlink("/proc/1/ns/net")
		what = "the system (PID 1)"
	}
	if err != nil || other == self {
		return true, ""
	}
	return false, "this process runs in a network namespace other than " + what + "'s, so its per-interface DNS settings would land on a different interface"
}

// resolvedPID finds systemd-resolved among the processes this one can see.
func resolvedPID() string {
	comms, _ := filepath.Glob("/proc/[0-9]*/comm")
	for _, c := range comms {
		b, err := os.ReadFile(c)
		// The kernel truncates comm to 15 bytes.
		if err == nil && strings.TrimSpace(string(b)) == "systemd-resolve" {
			return filepath.Base(filepath.Dir(c))
		}
	}
	return ""
}

func forwardPrefix(st *state, dev string, src netip.Prefix) error {
	return linuxForward(st, dev, src.Masked().String())
}

// KeepOff pins dst to the way it is routed now, so that a later default
// route through a tunnel does not capture it. Linux only.
func KeepOff(c *Changes, dst netip.Prefix) error { return except(c.st, dst) }
