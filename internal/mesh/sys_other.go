//go:build !linux && !darwin && !windows

package mesh

import (
	"errors"
	"net/netip"
	"runtime"

	"github.com/readori/RemoHop/internal/svc/vpn"
)

// The mesh's data plane is implemented for Linux, macOS and Windows.
func sysSupported() error {
	return errors.New("remohop net runs on Linux, macOS and Windows (not on " + runtime.GOOS + "); see docs/net.md")
}

func processAlive(pid int) bool { return vpn.ProcessAlive(pid) }

func sysConfigure(string, netip.Prefix, netip.Prefix, int) (bool, []string, error) {
	return false, nil, sysSupported()
}

func sysRoute(string, netip.Prefix) error { return sysSupported() }

func checkPrefixFree(netip.Prefix) error { return nil }

func sysSplitDNS(*vpn.Changes, string, netip.Addr) (string, bool) { return "", false }
