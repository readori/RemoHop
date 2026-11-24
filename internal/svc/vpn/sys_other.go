//go:build !linux && !darwin && !windows

package vpn

import (
	"errors"
	"net/netip"
	"os/exec"
	"runtime"
)

const ifPrefix = "remohop"

// errUnsupported is what every entry point returns here.
func errUnsupported() error {
	return errors.New("VPN is not supported on " + runtime.GOOS)
}

func newPlatform() platform {
	return platform{
		privileged:    errUnsupported,
		hostSupported: errUnsupported,
		open:          func(string) (Device, error) { return nil, errUnsupported() },
		usedNets:      interfaceNets,
		configure:     func(string, netip.Addr, netip.Addr, int) error { return errUnsupported() },
		clientRoutes:  func(*state, string, clientRouting) ([]string, error) { return nil, errUnsupported() },
		hostForward:   func(*state, string, netip.Addr) error { return errUnsupported() },
		hostRelease:   func() {},
		resolvers:     func() []string { return nil },
	}
}

func processAlive(int) bool { return true }

func runPrivileged(cmd *exec.Cmd) ([]byte, error) { return cmd.CombinedOutput() }
