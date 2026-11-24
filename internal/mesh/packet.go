package mesh

import (
	"encoding/binary"
	"net/netip"
)

const (
	protoICMP   = 1
	protoTCP    = 6
	protoUDP    = 17
	protoICMPv6 = 58
	tcpSYN      = 0x02
)

// packet is what the mesh needs to know about an IP packet.
type packet struct {
	v6       bool
	src, dst netip.Addr
	proto    byte
	// first: not a later fragment, so the transport header is present
	// (if the packet is long enough; then l4 > 0).
	first bool
	l4    int // offset of the transport header, 0 if absent
	sport uint16
	dport uint16
	// ports: sport and dport are set (TCP or UDP with a full header).
	ports bool
	// icmpType is the ICMP (or ICMPv6) type, with icmp set.
	icmpType byte
	icmp     bool
}

// parsePacket validates an IPv4 or IPv6 packet: version, header length and
// a total length that matches the bytes there. Anything else is not
// carried.
func parsePacket(p []byte) (packet, bool) {
	var k packet
	if len(p) < 1 {
		return k, false
	}
	switch p[0] >> 4 {
	case 4:
		if len(p) < 20 {
			return k, false
		}
		ihl := int(p[0]&0x0f) * 4
		if ihl < 20 || ihl > len(p) || int(binary.BigEndian.Uint16(p[2:4])) != len(p) {
			return k, false
		}
		k.proto = p[9]
		k.first = binary.BigEndian.Uint16(p[6:8])&0x1fff == 0
		k.src = netip.AddrFrom4([4]byte(p[12:16]))
		k.dst = netip.AddrFrom4([4]byte(p[16:20]))
		if k.first {
			k.l4 = ihl
		}
	case 6:
		if len(p) < 40 || int(binary.BigEndian.Uint16(p[4:6]))+40 != len(p) {
			return k, false
		}
		k.v6 = true
		k.src = netip.AddrFrom16([16]byte(p[8:24]))
		k.dst = netip.AddrFrom16([16]byte(p[24:40]))
		next, off := p[6], 40
		k.first = true
		// Walk the extension headers a host may meet.
	walk:
		for i := 0; i < 8; i++ {
			switch next {
			case 0, 43, 60: // hop-by-hop, routing, destination options
				if off+8 > len(p) {
					return k, false
				}
				next, off = p[off], off+8+int(p[off+1])*8
			case 44: // fragment
				if off+8 > len(p) {
					return k, false
				}
				k.first = binary.BigEndian.Uint16(p[off+2:off+4])&0xfff8 == 0
				next, off = p[off], off+8
			default:
				break walk
			}
			if off > len(p) {
				return k, false
			}
		}
		k.proto = next
		if k.first {
			k.l4 = off
		}
	default:
		return k, false
	}
	if k.l4 > 0 {
		l4 := p[k.l4:]
		switch k.proto {
		case protoTCP, protoUDP:
			if len(l4) >= 4 {
				k.sport = binary.BigEndian.Uint16(l4[0:2])
				k.dport = binary.BigEndian.Uint16(l4[2:4])
				k.ports = true
			}
		case protoICMP, protoICMPv6:
			if len(l4) >= 1 {
				k.icmpType, k.icmp = l4[0], true
			}
		}
	}
	return k, true
}

// isEchoRequest reports whether k is a ping.
func (k packet) isEchoRequest() bool {
	return k.icmp && ((!k.v6 && k.proto == protoICMP && k.icmpType == 8) || (k.v6 && k.proto == protoICMPv6 && k.icmpType == 128))
}

// isICMPReply reports whether k is an ICMP message that only ever answers
// something: an echo reply, or an error (unreachable, too big, time
// exceeded, parameter problem). These pass a policy like replies do; they
// carry no new connection, and path MTU discovery depends on them.
func (k packet) isICMPReply() bool {
	if !k.icmp {
		return false
	}
	if k.v6 {
		return k.proto == protoICMPv6 && (k.icmpType == 129 || (k.icmpType >= 1 && k.icmpType <= 4))
	}
	return k.proto == protoICMP && (k.icmpType == 0 || k.icmpType == 3 || k.icmpType == 11 || k.icmpType == 12)
}

// clampMSS lowers the MSS option of a TCP SYN to mss if it is larger and
// patches the checksum (RFC 1624; the pseudo-header is unchanged, so this
// holds for IPv6 as for IPv4).
func clampMSS(p []byte, k packet, mss uint16) {
	if k.proto != protoTCP || k.l4 == 0 {
		return
	}
	if k.v6 && mss > 20 {
		mss -= 20 // the IPv6 header is 20 bytes longer
	}
	tcp := p[k.l4:]
	if len(tcp) < 20 || tcp[13]&tcpSYN == 0 {
		return
	}
	off := int(tcp[12]>>4) * 4
	if off < 20 || off > len(tcp) {
		return
	}
	for opts := tcp[20:off]; len(opts) > 0; {
		switch opts[0] {
		case 0:
			return
		case 1:
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
