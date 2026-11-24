package vpn

import (
	"encoding/binary"
	"net/netip"
)

const (
	protoTCP = 6
	tcpSYN   = 0x02
)

// header is what the tunnel needs to know about an IPv4 packet.
type header struct {
	src, dst netip.Addr
	proto    byte
	ihl      int  // header length in bytes
	first    bool // not a later fragment: the transport header is present
}

// parse validates an IPv4 packet: version, header length, and a total
// length that matches the bytes actually there. Anything else (IPv6, ARP
// noise, truncated or padded packets) is not carried.
func parse(p []byte) (header, bool) {
	var h header
	if len(p) < 20 || p[0]>>4 != 4 {
		return h, false
	}
	h.ihl = int(p[0]&0x0f) * 4
	if h.ihl < 20 || h.ihl > len(p) || int(binary.BigEndian.Uint16(p[2:4])) != len(p) {
		return h, false
	}
	h.proto = p[9]
	h.first = binary.BigEndian.Uint16(p[6:8])&0x1fff == 0
	h.src = netip.AddrFrom4([4]byte(p[12:16]))
	h.dst = netip.AddrFrom4([4]byte(p[16:20]))
	return h, true
}

// clampMSS lowers the MSS option of a TCP SYN to mss if it is larger, and
// patches the TCP checksum. A TCP connection through the tunnel then never
// sends segments that would have to be fragmented to fit the tunnel MTU,
// whatever the MTUs of the networks at either end are.
func clampMSS(p []byte, h header, mss uint16) {
	if h.proto != protoTCP || !h.first {
		return
	}
	tcp := p[h.ihl:]
	if len(tcp) < 20 || tcp[13]&tcpSYN == 0 {
		return
	}
	off := int(tcp[12]>>4) * 4
	if off < 20 || off > len(tcp) {
		return
	}
	for opts := tcp[20:off]; len(opts) > 0; {
		switch opts[0] {
		case 0: // end of options
			return
		case 1: // no-op
			opts = opts[1:]
			continue
		}
		if len(opts) < 2 || int(opts[1]) < 2 || int(opts[1]) > len(opts) {
			return
		}
		if opts[0] == 2 && opts[1] == 4 {
			old := binary.BigEndian.Uint16(opts[2:4])
			if old > mss {
				binary.BigEndian.PutUint16(opts[2:4], mss)
				// RFC 1624: HC' = ~(~HC + ~m + m')
				sum := uint32(^binary.BigEndian.Uint16(tcp[16:18])) + uint32(^old) + uint32(mss)
				sum = sum&0xffff + sum>>16
				sum = sum&0xffff + sum>>16
				binary.BigEndian.PutUint16(tcp[16:18], ^uint16(sum))
			}
			return
		}
		opts = opts[opts[1]:]
	}
}

// policy decides which packets may pass in one direction.
type policy struct {
	src func(netip.Addr) bool
	dst func(netip.Addr) bool
}

func (f policy) allows(h header) bool { return f.src(h.src) && f.dst(h.dst) }

func only(a netip.Addr) func(netip.Addr) bool {
	return func(x netip.Addr) bool { return x == a }
}

func anyAddr(netip.Addr) bool { return true }

// forwardable reports whether a is an address a tunnel peer may talk to
// through us at all: never loopback, multicast, broadcast, link-local, the
// unspecified address or another tunnel.
func forwardable(a netip.Addr) bool {
	return a.Is4() && !a.IsLoopback() && !a.IsMulticast() && !a.IsLinkLocalUnicast() && !a.IsUnspecified() &&
		a != netip.AddrFrom4([4]byte{255, 255, 255, 255}) && !pool.Contains(a)
}

// within returns a predicate for "inside one of the routes" (and
// forwardable); with all set, any forwardable address.
func within(routes []netip.Prefix, all bool, also ...netip.Addr) func(netip.Addr) bool {
	return func(a netip.Addr) bool {
		for _, x := range also {
			if a == x {
				return true
			}
		}
		if !forwardable(a) {
			return false
		}
		if all {
			return true
		}
		for _, r := range routes {
			if r.Contains(a) {
				return true
			}
		}
		return false
	}
}
