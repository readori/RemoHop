//go:build !linux

package vpn

import (
	"errors"
	"net/netip"
	"runtime"
)

func forwardPrefix(*state, string, netip.Prefix) error {
	return errors.New("forwarding for other machines is implemented on Linux only, not on " + runtime.GOOS)
}

// KeepOff pins dst to the way it is routed now. Linux only.
func KeepOff(*Changes, netip.Prefix) error {
	return errors.New("not implemented on " + runtime.GOOS)
}
