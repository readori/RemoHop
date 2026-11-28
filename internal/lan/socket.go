package lan

import (
	"errors"
	"net"
	"net/netip"
	"sync"
	"time"

	"golang.org/x/net/ipv4"
)

// udpConn is a packetConn on a UDP socket.
type udpConn struct {
	c  *net.UDPConn
	p  *ipv4.PacketConn
	mu sync.Mutex // serialises writes to the group, which change the socket's multicast interface
}

// listenMDNS opens the responder's socket: port 5353 shared with the
// system's mDNS responder (the standard library sets SO_REUSEADDR, and
// SO_REUSEPORT where it exists, for a multicast address), in the mDNS
// group on every interface that does multicast. Where the system can say
// (Linux, macOS, the BSDs), each datagram comes with the address it was
// sent to, so a unicast question to port 5353 is told apart and ignored.
func listenMDNS() (packetConn, error) {
	c, err := net.ListenMulticastUDP("udp4", nil, net.UDPAddrFromAddrPort(group))
	if err != nil {
		return nil, err
	}
	u := &udpConn{c: c, p: ipv4.NewPacketConn(c)}
	_ = u.p.SetControlMessage(ipv4.FlagDst, true) // not on Windows: then every question counts as sent to the group
	u.join()
	return u, nil
}

// join adds the socket to the mDNS group on every interface that does
// multicast; an interface it is in already refuses, which is fine.
func (u *udpConn) join() {
	ga := &net.UDPAddr{IP: group.Addr().AsSlice()}
	for _, ifi := range multicastInterfaces() {
		_ = u.p.JoinGroup(&ifi, ga)
	}
}

// listenBrowser opens the browser's socket on an ephemeral port.
func listenBrowser() (packetConn, error) {
	c, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		return nil, err
	}
	u := &udpConn{c: c, p: ipv4.NewPacketConn(c)}
	_ = u.p.SetMulticastTTL(255) // RFC 6762, section 11
	_ = u.p.SetMulticastLoopback(true)
	return u, nil
}

func (u *udpConn) readFrom(b []byte) (int, netip.AddrPort, bool, error) {
	n, cm, src, err := u.p.ReadFrom(b)
	if err != nil {
		return 0, netip.AddrPort{}, false, err
	}
	ua, ok := src.(*net.UDPAddr)
	if !ok {
		return 0, netip.AddrPort{}, false, errors.New("not a UDP address")
	}
	toGroup := true
	if cm != nil && cm.Dst != nil {
		toGroup = cm.Dst.Equal(group.Addr().AsSlice())
	}
	ap := ua.AddrPort()
	return n, netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port()), toGroup, nil
}

func (u *udpConn) writeTo(b []byte, dst netip.AddrPort) error {
	if dst != group {
		_, err := u.c.WriteToUDPAddrPort(b, dst)
		return err
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	ua := net.UDPAddrFromAddrPort(dst)
	ifs := multicastInterfaces()
	if len(ifs) == 0 {
		_, err := u.p.WriteTo(b, nil, ua) // the system's default
		return err
	}
	var errs []error
	sent := false
	for _, ifi := range ifs {
		if err := u.p.SetMulticastInterface(&ifi); err != nil {
			errs = append(errs, err)
			continue
		}
		if _, err := u.p.WriteTo(b, nil, ua); err != nil {
			errs = append(errs, err)
			continue
		}
		sent = true
	}
	if sent {
		return nil
	}
	return errors.Join(errs...)
}

func (u *udpConn) setReadDeadline(t time.Time) error { return u.c.SetReadDeadline(t) }
func (u *udpConn) close() error                      { return u.c.Close() }

// multicastInterfaces lists the interfaces discovery runs on: up, with
// multicast, and neither loopback nor point to point (VPN tunnels: their
// other end is not on the local network).
func multicastInterfaces() []net.Interface {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []net.Interface
	for _, ifi := range ifs {
		f := ifi.Flags
		if f&net.FlagUp == 0 || f&net.FlagMulticast == 0 || f&net.FlagLoopback != 0 || f&net.FlagPointToPoint != 0 {
			continue
		}
		out = append(out, ifi)
	}
	return out
}

// localPrefixes lists the machine's IPv4 addresses with their subnets, on
// the interfaces that are up.
func localPrefixes() []netip.Prefix {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []netip.Prefix
	for _, ifi := range ifs {
		if ifi.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := ifi.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip, ok := netip.AddrFromSlice(ipn.IP)
			ones, bits := ipn.Mask.Size()
			if !ok || !ip.Unmap().Is4() || bits == 0 {
				continue
			}
			if bits == 128 {
				ones -= 96
			}
			out = append(out, netip.PrefixFrom(ip.Unmap(), ones))
		}
	}
	return out
}
