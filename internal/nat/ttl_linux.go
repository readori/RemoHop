//go:build linux

package nat

import (
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
	"unsafe"

	"golang.org/x/sys/unix"
)

// udpTTLWriter returns a function that sends one IPv4 packet on pc with the
// given TTL, leaving the socket's own TTL (which the QUIC traffic sharing
// the socket uses) untouched. It does so with an IP_TTL control message on
// sendmsg, which Linux honours also on a dual-stack IPv6 socket sending to
// an IPv4 destination.
func udpTTLWriter(pc net.PacketConn) func(p []byte, to netip.AddrPort, ttl int) error {
	// A *net.UDPConn, or the wrapper Listen puts around one.
	uc, ok := pc.(interface {
		WriteMsgUDPAddrPort(b, oob []byte, addr netip.AddrPort) (n, oobn int, err error)
	})
	if !ok {
		return nil
	}
	return func(p []byte, to netip.AddrPort, ttl int) error {
		if !to.Addr().Is4() {
			return errors.ErrUnsupported
		}
		oob := make([]byte, unix.CmsgSpace(4))
		h := (*unix.Cmsghdr)(unsafe.Pointer(&oob[0]))
		h.Level = unix.IPPROTO_IP
		h.Type = unix.IP_TTL
		h.SetLen(unix.CmsgLen(4))
		binary.NativeEndian.PutUint32(oob[unix.CmsgLen(0):], uint32(ttl))
		_, _, err := uc.WriteMsgUDPAddrPort(p, oob, to)
		return err
	}
}
