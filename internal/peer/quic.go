package peer

import (
	"context"
	"net"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/readori/RemoHop/internal/nat"
)

// ALPN is the application protocol negotiated on every RemoHop P2P session.
const ALPN = "remohop/1"

// MaxStreams bounds how many streams the peer may have open towards us at
// once. It is enforced before login, and QUIC makes the receiver create state
// for every stream ID up to the highest one used, so it has to be small: a
// stranger costs at most this many stream objects per connection. A thousand
// is still far more concurrent forwarded connections than anyone needs.
const MaxStreams = 1024

// QUICConfig returns the QUIC parameters used for P2P sessions. Keep-alives
// hold the NAT mappings open; the generous flow-control windows let file
// transfer and video use the whole path.
func QUICConfig() *quic.Config {
	return &quic.Config{
		HandshakeIdleTimeout:           10 * time.Second,
		MaxIdleTimeout:                 45 * time.Second,
		KeepAlivePeriod:                10 * time.Second,
		MaxIncomingStreams:             MaxStreams,
		MaxIncomingUniStreams:          -1, // unused: refuse them
		InitialStreamReceiveWindow:     2 << 20,
		MaxStreamReceiveWindow:         32 << 20,
		InitialConnectionReceiveWindow: 4 << 20,
		MaxConnectionReceiveWindow:     128 << 20,
		EnableDatagrams:                true,
		InitialPacketSize:              nat.InitialPacketSize, // see there: tunnels at MTU 1280
	}
}

// QUICMux adapts an established QUIC connection to Mux.
func QUICMux(q *quic.Conn) Mux { return &quicMux{q: q} }

// QUICConn returns the QUIC connection underneath c, or nil if c does not run
// directly on QUIC. It is for features that need datagrams or path stats.
func QUICConn(c *Conn) *quic.Conn {
	if m, ok := c.mux.(*quicMux); ok {
		return m.q
	}
	return nil
}

type quicMux struct{ q *quic.Conn }

func (m *quicMux) OpenStream(ctx context.Context) (Stream, error) {
	s, err := m.q.OpenStreamSync(ctx)
	if err != nil {
		return nil, err
	}
	return quicStream{s}, nil
}

func (m *quicMux) AcceptStream(ctx context.Context) (Stream, error) {
	s, err := m.q.AcceptStream(ctx)
	if err != nil {
		return nil, err
	}
	return quicStream{s}, nil
}

func (m *quicMux) Close(reason string) error { return m.q.CloseWithError(0, reason) }
func (m *quicMux) Context() context.Context  { return m.q.Context() }
func (m *quicMux) LocalAddr() net.Addr       { return m.q.LocalAddr() }
func (m *quicMux) RemoteAddr() net.Addr      { return m.q.RemoteAddr() }

type quicStream struct{ *quic.Stream }

func (s quicStream) CloseWrite() error { return s.Stream.Close() }

func (s quicStream) Close() error {
	s.Stream.CancelRead(0)
	return s.Stream.Close()
}

func (s quicStream) Reset() {
	s.Stream.CancelRead(1)
	s.Stream.CancelWrite(1)
}
