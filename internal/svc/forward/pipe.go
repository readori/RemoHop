package forward

import (
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/readori/RemoHop/internal/peer"
)

// bufSize is the copy buffer size. It is larger than the io.Copy default so
// a QUIC stream is fed in chunks that fill whole packets.
const bufSize = 128 << 10

var bufPool = sync.Pool{New: func() any { b := make([]byte, bufSize); return &b }}

// end is one side of a tunnelled connection.
type end interface {
	io.ReadWriteCloser
	// CloseWrite sends EOF to the peer and keeps the read side open.
	CloseWrite() error
	// Abort tears the connection down at once, signalling an error to the
	// peer where the transport can (RST for TCP, a reset for a stream).
	Abort()
}

// tcpEnd adapts a TCP connection.
type tcpEnd struct{ net.Conn }

func (e tcpEnd) CloseWrite() error {
	if cw, ok := e.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return e.Conn.Close()
}

func (e tcpEnd) Abort() {
	if tc, ok := e.Conn.(*net.TCPConn); ok {
		tc.SetLinger(0) // close with RST so the application sees a failure, not a clean EOF
	}
	e.Conn.Close()
}

// streamConn is a session stream presented as a net.Conn. It also implements
// CloseWrite, so callers can half-close the tunnelled connection.
type streamConn struct {
	s             peer.Stream
	local, remote net.Addr
}

func newStreamConn(s peer.Stream, network, addr string) *streamConn {
	return &streamConn{s: s, local: tunnelAddr{network, "remohop"}, remote: tunnelAddr{network, addr}}
}

func (c *streamConn) Read(p []byte) (int, error)         { return c.s.Read(p) }
func (c *streamConn) Write(p []byte) (int, error)        { return c.s.Write(p) }
func (c *streamConn) Close() error                       { return c.s.Close() }
func (c *streamConn) CloseWrite() error                  { return c.s.CloseWrite() }
func (c *streamConn) Abort()                             { c.s.Reset() }
func (c *streamConn) LocalAddr() net.Addr                { return c.local }
func (c *streamConn) RemoteAddr() net.Addr               { return c.remote }
func (c *streamConn) SetDeadline(t time.Time) error      { return c.s.SetDeadline(t) }
func (c *streamConn) SetReadDeadline(t time.Time) error  { return c.s.SetReadDeadline(t) }
func (c *streamConn) SetWriteDeadline(t time.Time) error { return c.s.SetWriteDeadline(t) }

type tunnelAddr struct{ network, addr string }

func (a tunnelAddr) Network() string { return a.network }
func (a tunnelAddr) String() string  { return a.addr }

// countWriter counts bytes as they are written so live stats stay current on
// long-lived connections. It deliberately has no ReadFrom, which keeps
// io.CopyBuffer on our pooled buffer.
type countWriter struct {
	w io.Writer
	n *atomic.Int64
}

func (c countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n.Add(int64(n))
	return n, err
}

// pipe copies between a and b until both directions finish, then closes
// both. A clean EOF in one direction half-closes the other end so protocols
// that rely on it (a client that sends its request and then shuts down
// writing) keep working. Any error aborts both ends, which unblocks the
// other copy. ab and ba count bytes moved a->b and b->a (may be nil).
func pipe(a, b end, ab, ba *atomic.Int64) {
	var dummy atomic.Int64
	if ab == nil {
		ab = &dummy
	}
	if ba == nil {
		ba = &dummy
	}
	var wg sync.WaitGroup
	var failed atomic.Bool
	cp := func(dst, src end, n *atomic.Int64) {
		defer wg.Done()
		bp := bufPool.Get().(*[]byte)
		defer bufPool.Put(bp)
		// struct{io.Reader} hides WriterTo so the pooled buffer is used.
		_, err := io.CopyBuffer(countWriter{dst, n}, struct{ io.Reader }{src}, *bp)
		if err != nil {
			if failed.CompareAndSwap(false, true) {
				a.Abort()
				b.Abort()
			}
			return
		}
		dst.CloseWrite()
	}
	wg.Add(2)
	go cp(b, a, ab)
	go cp(a, b, ba)
	wg.Wait()
	a.Close()
	b.Close()
}
