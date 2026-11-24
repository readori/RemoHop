//go:build !linux

package nat

import (
	"net"
	"net/netip"
)

// udpTTLWriter reports that per-packet TTLs are not available: only Linux
// is known to honour an IP_TTL control message on sendmsg, and changing the
// socket-wide TTL instead would also cut short the QUIC packets of sessions
// sharing the socket.
func udpTTLWriter(net.PacketConn) func(p []byte, to netip.AddrPort, ttl int) error {
	return nil
}
