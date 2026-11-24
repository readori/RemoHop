package nat

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

// IPv6-only networks with NAT64 (RFC 6146) and DNS64 (RFC 6147).
//
// Apple reviews iOS apps on such a network, and some mobile carriers run
// them. There a host name works as usual: the system resolver returns an
// IPv6 address that DNS64 synthesized from the name's IPv4 address, and
// the NAT64 gateway translates. An IPv4 literal does not: Go hands it to
// the kernel as it is, and with no IPv4 route the send fails with "network
// is unreachable". RemoHop meets IPv4 literals all the time: the address a
// user types (146.235.209.71:21118), the host endpoints and relay ports the
// rendezvous server returns, punch candidates.
//
// So a socket can be wrapped (WrapNAT64; Listen does it on iOS, or where
// REMOHOP_NAT64=on) so that when a datagram to an IPv4 address cannot be
// sent, the network's NAT64 prefix is discovered (RFC 7050: the system
// resolver's AAAA answer for ipv4only.arpa, whose only A records are
// 192.0.0.170 and 192.0.0.171) and the datagram goes to the IPv4-embedded
// IPv6 address (RFC 6052) instead. Answers from such an address are handed
// up as coming from the IPv4 address, so QUIC, punching and relaying above
// never see the difference. Where IPv4 works nothing changes: no lookup is
// made and every datagram goes out as before.
//
// Limits: only datagrams are translated (TCP to IPv4 literals is not used
// on the paths a phone needs); a NAT64 that is not announced through DNS64
// (no synthesized ipv4only.arpa) is not found; and a translated path is a
// NAT like any other, which punching treats as such.

// ipv4only.arpa's well-known addresses (RFC 7050).
var (
	wka1 = netip.AddrFrom4([4]byte{192, 0, 0, 170})
	wka2 = netip.AddrFrom4([4]byte{192, 0, 0, 171})
)

// nat64Lengths are the prefix lengths RFC 6052 allows, most common first.
var nat64Lengths = []int{96, 64, 56, 48, 40, 32}

// v4Offsets says where the four bytes of the IPv4 address go in the IPv6
// address for each prefix length (byte 8, bits 64 to 71, is always zero).
func v4Offsets(bits int) []int {
	switch bits {
	case 32:
		return []int{4, 5, 6, 7}
	case 40:
		return []int{5, 6, 7, 9}
	case 48:
		return []int{6, 7, 9, 10}
	case 56:
		return []int{7, 9, 10, 11}
	case 64:
		return []int{9, 10, 11, 12}
	case 96:
		return []int{12, 13, 14, 15}
	}
	return nil
}

// SynthesizeNAT64 returns the IPv4-embedded IPv6 address of v4 under the
// NAT64 prefix (RFC 6052 section 2.2).
func SynthesizeNAT64(prefix netip.Prefix, v4 netip.Addr) (netip.Addr, bool) {
	off := v4Offsets(prefix.Bits())
	v4 = v4.Unmap()
	if off == nil || !prefix.Addr().Is6() || prefix.Addr().Is4In6() || !v4.Is4() {
		return netip.Addr{}, false
	}
	b := prefix.Masked().Addr().As16()
	for i := prefix.Bits() / 8; i < 16; i++ {
		b[i] = 0 // u-octet and suffix
	}
	f := v4.As4()
	for i, o := range off {
		b[o] = f[i]
	}
	return netip.AddrFrom16(b), true
}

// ExtractNAT64 returns the IPv4 address embedded in ip under prefix, if ip
// is in it.
func ExtractNAT64(prefix netip.Prefix, ip netip.Addr) (netip.Addr, bool) {
	off := v4Offsets(prefix.Bits())
	ip = ip.WithZone("")
	if off == nil || !ip.Is6() || ip.Is4In6() || !prefix.Contains(ip) {
		return netip.Addr{}, false
	}
	b := ip.As16()
	if prefix.Bits() < 96 && b[8] != 0 {
		return netip.Addr{}, false
	}
	return netip.AddrFrom4([4]byte{b[off[0]], b[off[1]], b[off[2]], b[off[3]]}), true
}

// NAT64PrefixFrom finds the NAT64 prefix in the AAAA answer for
// ipv4only.arpa (RFC 7050 section 3): an address that embeds one of the
// well-known IPv4 addresses at a valid position.
func NAT64PrefixFrom(addrs []netip.Addr) (netip.Prefix, bool) {
	for _, a := range addrs {
		a = a.WithZone("")
		if !a.Is6() || a.Is4In6() {
			continue
		}
		for _, bits := range nat64Lengths {
			p, err := a.Prefix(bits)
			if err != nil {
				continue
			}
			if v4, ok := ExtractNAT64(p, a); ok && (v4 == wka1 || v4 == wka2) {
				// The rest of the address (the suffix) must be zero.
				if s, _ := SynthesizeNAT64(p, v4); s == a {
					return p, true
				}
			}
		}
	}
	return netip.Prefix{}, false
}

// nat64Lookup asks the system resolver for ipv4only.arpa's AAAA records
// (on Apple platforms Go uses the system's resolver, which is the one that
// knows the network's DNS64). Tests replace it.
var nat64Lookup = func(ctx context.Context) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, "ip6", "ipv4only.arpa")
}

const (
	nat64LookupTimeout = 2 * time.Second
	nat64Keep          = 10 * time.Minute // a found prefix is used this long before asking again
	nat64KeepMiss      = 30 * time.Second // and an answer without one
	nat64V4Down        = time.Minute      // after an IPv4 send failed, translate at once for this long
	nat64MaxMapped     = 4096
)

// nat64Disc is the process's NAT64 prefix discovery (one network at a time).
var nat64Disc struct {
	mu     sync.Mutex
	prefix netip.Prefix
	ok     bool
	until  time.Time
}

// discoverNAT64 returns the network's NAT64 prefix, asking the resolver at
// most once per nat64Keep (nat64KeepMiss without one).
func discoverNAT64() (netip.Prefix, bool) {
	d := &nat64Disc
	d.mu.Lock()
	defer d.mu.Unlock()
	if time.Now().Before(d.until) {
		return d.prefix, d.ok
	}
	ctx, cancel := context.WithTimeout(context.Background(), nat64LookupTimeout)
	defer cancel()
	addrs, err := nat64Lookup(ctx)
	d.prefix, d.ok = netip.Prefix{}, false
	if err == nil {
		d.prefix, d.ok = NAT64PrefixFrom(addrs)
	}
	if d.ok {
		d.until = time.Now().Add(nat64Keep)
	} else {
		d.until = time.Now().Add(nat64KeepMiss)
	}
	return d.prefix, d.ok
}

// forgetNAT64 drops the cached prefix (the network changed).
func forgetNAT64() {
	nat64Disc.mu.Lock()
	nat64Disc.until = time.Time{}
	nat64Disc.mu.Unlock()
}

// NAT64Wanted reports whether Listen wraps its sockets with WrapNAT64: on
// iOS (where Apple's review network is IPv6-only and the system offers no
// translation of IPv4 literals to BSD sockets), or as REMOHOP_NAT64 says
// ("on"/"off"). Android translates in the system (464XLAT), desktops and
// hosts rarely need it, and the wrapper costs quic-go's batched I/O.
func NAT64Wanted() bool {
	switch strings.ToLower(os.Getenv("REMOHOP_NAT64")) {
	case "1", "on", "true", "yes":
		return true
	case "0", "off", "false", "no":
		return false
	}
	return runtime.GOOS == "ios"
}

// unreachable reports whether a send failed because there is no route of
// that address family (the IPv6-only network's answer to an IPv4 send).
func unreachable(err error) bool {
	return errors.Is(err, syscall.ENETUNREACH) || errors.Is(err, syscall.EHOSTUNREACH) ||
		errors.Is(err, syscall.EADDRNOTAVAIL) || errors.Is(err, syscall.EAFNOSUPPORT)
}

// nat64Conn is WrapNAT64's packet connection.
type nat64Conn struct {
	net.PacketConn

	mu     sync.Mutex
	v4Down time.Time                         // until when IPv4 sends go straight to NAT64
	mapped map[netip.AddrPort]netip.AddrPort // synthesized IPv6 -> the IPv4 address the caller used
}

// WrapNAT64 returns pc with IPv4 destinations translated through the
// network's NAT64 when IPv4 itself is unreachable (see the comment at the
// top of this file). pc should be a dual-stack (or IPv6) UDP socket.
func WrapNAT64(pc net.PacketConn) net.PacketConn {
	return &nat64Conn{PacketConn: pc, mapped: map[netip.AddrPort]netip.AddrPort{}}
}

func (c *nat64Conn) WriteTo(p []byte, addr net.Addr) (int, error) {
	ua, ok := addr.(*net.UDPAddr)
	if !ok {
		return c.PacketConn.WriteTo(p, addr)
	}
	dst := normalize(ua.AddrPort())
	if !dst.Addr().Is4() {
		return c.PacketConn.WriteTo(p, addr)
	}
	c.mu.Lock()
	down := time.Now().Before(c.v4Down)
	c.mu.Unlock()
	if down {
		n, err := c.writeVia64(p, dst)
		if err == nil || !unreachable(err) {
			return n, err
		}
		// No route for the translated address either: the network
		// changed. Back to IPv4, and look the prefix up again next time.
		forgetNAT64()
		c.mu.Lock()
		c.v4Down = time.Time{}
		c.mu.Unlock()
	}
	n, err := c.PacketConn.WriteTo(p, addr)
	if err == nil || !unreachable(err) {
		return n, err
	}
	n64, err64 := c.writeVia64(p, dst)
	if err64 != nil {
		return n, err // the IPv4 error is the one that explains it
	}
	c.mu.Lock()
	c.v4Down = time.Now().Add(nat64V4Down)
	c.mu.Unlock()
	return n64, nil
}

// writeVia64 sends p to dst's address under the network's NAT64 prefix.
func (c *nat64Conn) writeVia64(p []byte, dst netip.AddrPort) (int, error) {
	prefix, ok := discoverNAT64()
	if !ok {
		return 0, &net.OpError{Op: "write", Net: "udp", Addr: net.UDPAddrFromAddrPort(dst), Err: syscall.ENETUNREACH}
	}
	ip, ok := SynthesizeNAT64(prefix, dst.Addr())
	if !ok {
		return 0, &net.OpError{Op: "write", Net: "udp", Addr: net.UDPAddrFromAddrPort(dst), Err: syscall.ENETUNREACH}
	}
	to := netip.AddrPortFrom(ip, dst.Port())
	c.mu.Lock()
	if _, seen := c.mapped[to]; !seen {
		if len(c.mapped) >= nat64MaxMapped {
			clear(c.mapped)
		}
		c.mapped[to] = dst
	}
	c.mu.Unlock()
	return c.PacketConn.WriteTo(p, net.UDPAddrFromAddrPort(to))
}

func (c *nat64Conn) ReadFrom(p []byte) (int, net.Addr, error) {
	n, addr, err := c.PacketConn.ReadFrom(p)
	if ua, ok := addr.(*net.UDPAddr); ok && ua.IP.To4() == nil {
		// Only addresses this socket sent to in translation are handed
		// back as IPv4: a name that DNS64 resolved stays as it was dialled.
		from := netip.AddrPortFrom(ua.AddrPort().Addr().WithZone(""), ua.AddrPort().Port())
		c.mu.Lock()
		v4, ok := c.mapped[from]
		c.mu.Unlock()
		if ok {
			addr = net.UDPAddrFromAddrPort(v4)
		}
	}
	return n, addr, err
}

// SetReadBuffer and SetWriteBuffer reach the socket, for quic-go.
func (c *nat64Conn) SetReadBuffer(n int) error {
	if s, ok := c.PacketConn.(interface{ SetReadBuffer(int) error }); ok {
		return s.SetReadBuffer(n)
	}
	return errors.ErrUnsupported
}

func (c *nat64Conn) SetWriteBuffer(n int) error {
	if s, ok := c.PacketConn.(interface{ SetWriteBuffer(int) error }); ok {
		return s.SetWriteBuffer(n)
	}
	return errors.ErrUnsupported
}

// SetNAT64LookupForTest replaces the ipv4only.arpa lookup (tests of other
// packages simulating an IPv6-only network) and returns the function that
// restores it.
func SetNAT64LookupForTest(f func(context.Context) ([]netip.Addr, error)) (restore func()) {
	old := nat64Lookup
	nat64Lookup = f
	forgetNAT64()
	return func() { nat64Lookup = old; forgetNAT64() }
}
