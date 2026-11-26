//go:build !linux && !darwin && !windows

package portmap

import (
	"errors"
	"net/netip"
)

// defaultGateway is not looked up here; the caller falls back to the
// address of the UPnP device that answered, if any.
func defaultGateway() (netip.Addr, error) {
	return netip.Addr{}, errors.New("default gateway lookup not supported on this system")
}
