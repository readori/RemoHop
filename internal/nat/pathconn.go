package nat

import (
	"net"
	"net/netip"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// A relayed session can move to a direct path without being re-established:
// QUIC lets the client probe a new path for its connection and switch to it
// (connection migration, RFC 9000 section 9), and quic-go's client does so
// with Conn.AddPath, given another quic.Transport to send on. The server
// side accepts the move by itself once the new path has been validated.
//
// quic-go assumes a new path means a new local socket towards the same
// server address. Here it is the reverse: the same socket, but a new
// address for the host (its punched address instead of the relay). A
// PathConn bridges the two: a packet connection on this socket that sends
// whatever QUIC hands it to the address set with SetTarget, whatever
// address QUIC names (the relay's, which the connection keeps calling the
// host's).
//
// It only sends. What the host sends back over the new path arrives on the
// socket like any other packet and reaches the connection through the
// socket's own transport, which knows all of the connection's IDs. So
// ReadFrom returns nothing until the PathConn is closed or its read
// deadline passes.
//
// Nothing about the session's security changes: the new path carries the
// same QUIC connection, under the keys negotiated with the authenticated
// host. A path is validated by a PATH_CHALLENGE answered inside that
// connection, which only the host holding those keys can do; a path to
// anybody else (a third party that answered the punch, a stray address)
// is never validated and never used.
type PathConn struct {
	s      *Socket
	target atomic.Pointer[net.UDPAddr]

	mu       sync.Mutex
	deadline time.Time
	wake     chan struct{} // closed and replaced when the deadline changes
	closed   bool
}

// NewPathConn returns a PathConn on this socket with no target yet: sends
// are dropped until SetTarget names one.
func (s *Socket) NewPathConn() *PathConn {
	return &PathConn{s: s, wake: make(chan struct{})}
}

// SetTarget sets the address every packet is sent to.
func (c *PathConn) SetTarget(to netip.AddrPort) {
	c.target.Store(net.UDPAddrFromAddrPort(normalize(to)))
}

// Target is the address set with SetTarget (invalid before).
func (c *PathConn) Target() netip.AddrPort {
	if ua := c.target.Load(); ua != nil {
		return ua.AddrPort()
	}
	return netip.AddrPort{}
}

// WriteTo sends p to the target, whatever addr says. Without a target, or
// once closed, the packet is lost as it would be on any path that leads
// nowhere: an error here would end the whole QUIC connection.
func (c *PathConn) WriteTo(p []byte, _ net.Addr) (int, error) {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	to := c.target.Load()
	if closed || to == nil {
		return len(p), nil
	}
	return c.s.tr.WriteTo(p, to)
}

// ReadFrom blocks until the PathConn is closed or its read deadline passes;
// see the type's comment.
func (c *PathConn) ReadFrom([]byte) (int, net.Addr, error) {
	for {
		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			return 0, nil, net.ErrClosed
		}
		deadline, wake := c.deadline, c.wake
		c.mu.Unlock()
		var timer <-chan time.Time
		if !deadline.IsZero() {
			d := time.Until(deadline)
			if d <= 0 {
				return 0, nil, os.ErrDeadlineExceeded
			}
			t := time.NewTimer(d)
			timer = t.C
			select {
			case <-wake:
				t.Stop()
			case <-timer:
			}
			continue
		}
		<-wake
	}
}

// Close makes ReadFrom return and drops every later send. Close the
// quic.Transport on a PathConn only once the connection that added a path
// on it has ended: closing a transport ends every connection registered
// with it.
func (c *PathConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		c.closed = true
		close(c.wake)
	}
	return nil
}

// LocalAddr is the socket's address.
func (c *PathConn) LocalAddr() net.Addr { return c.s.pc.LocalAddr() }

// SetDeadline sets the read deadline; writes never block.
func (c *PathConn) SetDeadline(t time.Time) error { return c.SetReadDeadline(t) }

// SetReadDeadline implements net.PacketConn.
func (c *PathConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.deadline = t
	close(c.wake)
	c.wake = make(chan struct{})
	return nil
}

// SetWriteDeadline implements net.PacketConn; writes never block.
func (c *PathConn) SetWriteDeadline(time.Time) error { return nil }

// SetReadBuffer and SetWriteBuffer do nothing: the socket underneath has its
// buffers set already. They keep quic-go from warning that they are missing.
func (c *PathConn) SetReadBuffer(int) error  { return nil }
func (c *PathConn) SetWriteBuffer(int) error { return nil }
