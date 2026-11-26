package portmap

import (
	"errors"
	"net/netip"
	"unsafe"

	"golang.org/x/sys/windows"
)

// defaultGateway returns the IPv4 gateway of the connected adapter with
// the lowest metric, from GetAdaptersAddresses.
func defaultGateway() (netip.Addr, error) {
	const flags = windows.GAA_FLAG_INCLUDE_GATEWAYS | windows.GAA_FLAG_SKIP_ANYCAST |
		windows.GAA_FLAG_SKIP_MULTICAST | windows.GAA_FLAG_SKIP_DNS_SERVER
	size := uint32(15 << 10)
	var buf []byte
	for range 3 {
		b := make([]byte, size)
		err := windows.GetAdaptersAddresses(windows.AF_INET, flags, 0, (*windows.IpAdapterAddresses)(unsafe.Pointer(&b[0])), &size)
		if err == nil {
			buf = b
			break
		}
		if err != windows.ERROR_BUFFER_OVERFLOW {
			return netip.Addr{}, err
		}
	}
	if buf == nil {
		return netip.Addr{}, errors.New("the adapter list keeps growing")
	}
	var best netip.Addr
	bestMetric := ^uint32(0)
	for a := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0])); a != nil; a = a.Next {
		if a.OperStatus != windows.IfOperStatusUp || a.IfType == windows.IF_TYPE_SOFTWARE_LOOPBACK {
			continue
		}
		for g := a.FirstGatewayAddress; g != nil; g = g.Next {
			ip, ok := netip.AddrFromSlice(g.Address.IP())
			if !ok {
				continue
			}
			ip = ip.Unmap()
			if ip.Is4() && !ip.IsUnspecified() && a.Ipv4Metric < bestMetric {
				best, bestMetric = ip, a.Ipv4Metric
			}
		}
	}
	if !best.IsValid() {
		return netip.Addr{}, errors.New("no IPv4 gateway")
	}
	return best, nil
}
