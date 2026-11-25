package nat

import (
	"errors"
	"net"
	"sync/atomic"
	"syscall"
	"time"
)

// Brief losses of the route.
//
// quic-go ends a connection on any error a send returns, except "message too
// long". When a laptop roams between access points, renews its DHCP lease,
// or a VPN client rewrites the routing table, the kernel refuses sends for a
// second or so with "network is unreachable", "no route to host" or "no
// buffer space": the client's session died on the spot, although the same
// outage as plain packet loss would have been ridden out. So the socket
// Listen opens reports such errors as success: the packet is lost, which
// QUIC handles (it retransmits, and its idle timeout still ends a session
// whose network stays gone).
//
// Only while the socket has sent successfully within the last softGrace: a
// socket that never got a packet out (a dial on a machine with no network at
// all) still fails at once with the real reason.

// softGrace is how long after the last successful send transient errors are
// taken for packet loss. Longer than any idle timeout on the socket's
// connections, so it is QUIC that gives up first.
const softGrace = 60 * time.Second

// softClock is the time base for the last successful send (monotonic).
var softClock = time.Now()

type softSend struct{ lastOK atomic.Int64 }

// result turns a send's outcome into what the caller sees.
func (s *softSend) result(size, n int, err error) (int, error) {
	now := int64(time.Since(softClock))
	if err == nil {
		s.lastOK.Store(now)
		return n, nil
	}
	if last := s.lastOK.Load(); last != 0 && now-last < int64(softGrace) && transientSendError(err) {
		return size, nil
	}
	return n, err
}

// transientSendError reports whether a send failed for a reason that a
// moment later may be gone: no route, interface down, address being
// replaced, buffers full.
func transientSendError(err error) bool {
	var errno syscall.Errno
	return errors.As(err, &errno) && transientErrno(errno)
}

// softUDPConn is a *net.UDPConn whose transient send errors are packet loss.
// It embeds the connection, so quic-go still finds every method it uses for
// batching, GSO, ECN and DF (see quic.OOBCapablePacketConn).
type softUDPConn struct {
	*net.UDPConn
	soft softSend
}

func (c *softUDPConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	n, err := c.UDPConn.WriteTo(p, addr)
	return c.soft.result(len(p), n, err)
}

func (c *softUDPConn) WriteToUDP(p []byte, addr *net.UDPAddr) (int, error) {
	n, err := c.UDPConn.WriteToUDP(p, addr)
	return c.soft.result(len(p), n, err)
}

func (c *softUDPConn) WriteMsgUDP(b, oob []byte, addr *net.UDPAddr) (int, int, error) {
	n, oobn, err := c.UDPConn.WriteMsgUDP(b, oob, addr)
	n, err = c.soft.result(len(b), n, err)
	if err == nil {
		oobn = len(oob)
	}
	return n, oobn, err
}

// softPacketConn does the same for any other packet connection (the NAT64
// wrapper, which only has WriteTo).
type softPacketConn struct {
	net.PacketConn
	soft softSend
}

func (c *softPacketConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	n, err := c.PacketConn.WriteTo(p, addr)
	return c.soft.result(len(p), n, err)
}

// softErrors wraps pc so that brief route losses are packet loss.
func softErrors(pc net.PacketConn) net.PacketConn {
	if uc, ok := pc.(*net.UDPConn); ok {
		return &softUDPConn{UDPConn: uc}
	}
	return &softPacketConn{PacketConn: pc}
}
