//go:build !linux

package mesh

import (
	"errors"
	"net/netip"
	"os"
	"runtime"
)

// The mesh's data plane is implemented for Linux only so far. The tunnel
// device of the other systems exists (internal/svc/vpn); what is missing is
// their interface, route and DNS configuration for a whole network instead
// of one point-to-point link.
func sysSupported() error {
	return errors.New("remohop net runs on Linux only so far (not on " + runtime.GOOS + "); see docs/net.md")
}

func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	_ = p
	return true
}

func sysConfigure(string, netip.Prefix, netip.Prefix, int) (bool, []string, error) {
	return false, nil, sysSupported()
}

func sysRoute(string, netip.Prefix) error { return sysSupported() }

func checkPrefixFree(netip.Prefix) error { return nil }

func sysSplitDNS(string, netip.Addr) (string, bool) { return "", false }
