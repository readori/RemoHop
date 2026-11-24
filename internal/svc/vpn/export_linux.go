//go:build linux

package vpn

import "net/netip"

func forwardPrefix(st *state, dev string, src netip.Prefix) error {
	return linuxForward(st, dev, src.Masked().String())
}

// KeepOff pins dst to the way it is routed now, so that a later default
// route through a tunnel does not capture it. Linux only.
func KeepOff(c *Changes, dst netip.Prefix) error { return except(c.st, dst) }
