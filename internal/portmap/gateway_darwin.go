package portmap

import (
	"errors"
	"net/netip"
	"syscall"

	"golang.org/x/net/route"
)

// defaultGateway reads the IPv4 default route from the routing table
// (sysctl NET_RT_DUMP, through x/net/route). Interface-scoped default
// routes, which macOS keeps for every interface besides the primary one,
// are skipped.
func defaultGateway() (netip.Addr, error) {
	rib, err := route.FetchRIB(syscall.AF_INET, route.RIBTypeRoute, 0)
	if err != nil {
		return netip.Addr{}, err
	}
	msgs, err := route.ParseRIB(route.RIBTypeRoute, rib)
	if err != nil {
		return netip.Addr{}, err
	}
	const rtfIfScope = 0x1000000 // RTF_IFSCOPE
	for _, msg := range msgs {
		rm, ok := msg.(*route.RouteMessage)
		if !ok || rm.Flags&syscall.RTF_GATEWAY == 0 || rm.Flags&syscall.RTF_UP == 0 || rm.Flags&rtfIfScope != 0 {
			continue
		}
		if len(rm.Addrs) <= syscall.RTAX_GATEWAY {
			continue
		}
		dst, ok1 := rm.Addrs[syscall.RTAX_DST].(*route.Inet4Addr)
		gw, ok2 := rm.Addrs[syscall.RTAX_GATEWAY].(*route.Inet4Addr)
		if !ok1 || !ok2 || dst.IP != [4]byte{} {
			continue
		}
		if ip := netip.AddrFrom4(gw.IP); !ip.IsUnspecified() {
			return ip, nil
		}
	}
	return netip.Addr{}, errors.New("no IPv4 default route")
}
