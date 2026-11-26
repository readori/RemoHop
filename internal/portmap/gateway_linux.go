package portmap

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
	"net/netip"
	"os"
	"strconv"
	"strings"
)

// defaultGateway reads the IPv4 default route from /proc/net/route. On
// Android the file may not be readable to apps; then the caller falls back
// to the UPnP device's address.
func defaultGateway() (netip.Addr, error) {
	f, err := os.Open("/proc/net/route")
	if err != nil {
		return netip.Addr{}, err
	}
	defer f.Close()
	return parseProcRoute(io.LimitReader(f, 1<<20))
}

// parseProcRoute picks the default route with the lowest metric from
// /proc/net/route: a header, then "Iface Destination Gateway Flags RefCnt
// Use Metric Mask ..." with the addresses as the kernel's 32-bit words in
// hex.
func parseProcRoute(r io.Reader) (netip.Addr, error) {
	const rtfUp, rtfGateway = 0x1, 0x2
	var best netip.Addr
	bestMetric := uint64(1 << 63)
	s := bufio.NewScanner(r)
	for first := true; s.Scan(); first = false {
		f := strings.Fields(s.Text())
		if first || len(f) < 8 {
			continue
		}
		dst, err1 := strconv.ParseUint(f[1], 16, 32)
		gw, err2 := strconv.ParseUint(f[2], 16, 32)
		flags, err3 := strconv.ParseUint(f[3], 16, 32)
		metric, err4 := strconv.ParseUint(f[6], 10, 32)
		mask, err5 := strconv.ParseUint(f[7], 16, 32)
		if err1 != nil || err2 != nil || err3 != nil || err4 != nil || err5 != nil {
			continue
		}
		if dst != 0 || mask != 0 || flags&(rtfUp|rtfGateway) != rtfUp|rtfGateway || gw == 0 {
			continue
		}
		if metric < bestMetric {
			var b [4]byte
			binary.NativeEndian.PutUint32(b[:], uint32(gw)) // the word holds the address in network order
			best, bestMetric = netip.AddrFrom4(b), metric
		}
	}
	if !best.IsValid() {
		return netip.Addr{}, errors.New("no IPv4 default route")
	}
	return best, nil
}
