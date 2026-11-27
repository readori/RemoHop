// Package peertest gives service packages a real client<->host session to
// test against without rendezvous, hole punching or passwords: two QUIC
// endpoints on loopback wired straight into the session layer.
//
// It is test support only. Nothing outside _test.go files may import it,
// because the sessions it creates are unauthenticated.
package peertest

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"math/big"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/readori/RemoHop/internal/grant"
	"github.com/readori/RemoHop/internal/peer"
)

// Pair returns a connected client and host session. The host side serves
// peer.HostServices, so importing a service package (which registers itself
// from init) is all a test needs to do. Both ends are closed when the test
// finishes.
func Pair(t testing.TB) (client, host *peer.Conn) {
	t.Helper()
	return PairWith(t, peer.HostServices)
}

// PairWith is Pair with an explicit host-side registry.
func PairWith(t testing.TB, services *peer.Registry) (client, host *peer.Conn) {
	t.Helper()
	return PairOpts(t, services, Options{})
}

// Options tunes the host end of a test session.
type Options struct {
	// Authorize is passed to the host's peer.Options.
	Authorize func(service string) error
	// NoStart returns the host end unstarted (peer.New): it serves no
	// stream until the test calls Start.
	NoStart bool
	// Latency delays every packet either end sends by this much, so the
	// round trip is twice it: what timing tests use to see how an
	// operation behaves on a real network rather than on loopback.
	Latency time.Duration
}

// PairOpts is PairWith with host options.
func PairOpts(t testing.TB, services *peer.Registry, o Options) (client, host *peer.Conn) {
	t.Helper()

	// Transports are closed last, after the sessions on them (cleanups
	// run in reverse order).
	var transports []*quic.Transport
	t.Cleanup(func() {
		for _, tr := range transports {
			tr.Close()
		}
	})
	newTransport := func() *quic.Transport {
		uc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			t.Fatalf("peertest: listen: %v", err)
		}
		var pc net.PacketConn = uc
		if o.Latency > 0 {
			pc = newDelayConn(uc, o.Latency)
		}
		tr := &quic.Transport{Conn: pc}
		transports = append(transports, tr)
		return tr
	}

	ln, err := newTransport().Listen(serverTLS(t), peer.QUICConfig())
	if err != nil {
		t.Fatalf("peertest: listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	// The first loopback handshake of a process has been seen to stall on a
	// loaded macOS runner, so dial a few times rather than once for long.
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	accepted := make(chan *quic.Conn, 8)
	go func() {
		for {
			q, err := ln.Accept(ctx)
			if err != nil {
				return
			}
			accepted <- q
		}
	}()

	clientTLS := &tls.Config{InsecureSkipVerify: true, NextProtos: []string{peer.ALPN}}
	var cq *quic.Conn
	for attempt := 1; ; attempt++ {
		dctx, dcancel := context.WithTimeout(ctx, 10*time.Second)
		cq, err = newTransport().Dial(dctx, ln.Addr(), clientTLS, peer.QUICConfig())
		dcancel()
		if err == nil {
			break
		}
		if attempt == 3 {
			t.Fatalf("peertest: dial: %v", err)
		}
	}
	// Match the host side of this client connection; an abandoned earlier
	// attempt may have been accepted too.
	var hq *quic.Conn
	for hq == nil {
		select {
		case q := <-accepted:
			if samePort(q.RemoteAddr(), cq.LocalAddr()) {
				hq = q
			} else {
				q.CloseWithError(0, "superseded")
			}
		case <-ctx.Done():
			t.Fatalf("peertest: accept: %v", ctx.Err())
		}
	}

	host = peer.New(peer.QUICMux(hq), peer.Options{
		Role:      peer.RoleHost,
		RemoteID:  "test-client",
		Services:  services,
		Authorize: o.Authorize,
	})
	// Like a host daemon's session with full access; a test that wants a
	// granted session attaches its own guard.
	grant.AttachFull(host)
	if !o.NoStart {
		host.Start()
	}
	client = peer.NewConn(peer.QUICMux(cq), peer.Options{
		Role:     peer.RoleClient,
		RemoteID: "test-host",
	})
	t.Cleanup(func() {
		client.Close()
		host.Close()
		// Let the host's handlers finish before the next test starts: a
		// handler still running would hold shared state (a reserved
		// network, a swapped test seam) into it.
		host.WaitHandlers(10 * time.Second)
	})
	return client, host
}

func serverTLS(t testing.TB) *tls.Config {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: priv}},
		NextProtos:   []string{peer.ALPN},
	}
}

// delayConn sends every packet a fixed time after it was written, in the
// order written.
type delayConn struct {
	net.PacketConn
	delay time.Duration

	mu     sync.Mutex
	closed bool
	queue  chan delayed
}

type delayed struct {
	at   time.Time
	b    []byte
	addr net.Addr
}

func newDelayConn(pc *net.UDPConn, delay time.Duration) *delayConn {
	c := &delayConn{PacketConn: pc, delay: delay, queue: make(chan delayed, 1<<14)}
	go func() {
		for p := range c.queue {
			if w := time.Until(p.at); w > 0 {
				time.Sleep(w)
			}
			pc.WriteTo(p.b, p.addr)
		}
	}()
	return c
}

func (c *delayConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return 0, net.ErrClosed
	}
	select {
	case c.queue <- delayed{time.Now().Add(c.delay), append([]byte(nil), b...), addr}:
	default: // a full queue drops the packet, as a full router queue would
	}
	return len(b), nil
}

// SetReadBuffer and SetWriteBuffer let quic-go size the socket's buffers.
func (c *delayConn) SetReadBuffer(n int) error {
	return c.PacketConn.(*net.UDPConn).SetReadBuffer(n)
}

func (c *delayConn) SetWriteBuffer(n int) error {
	return c.PacketConn.(*net.UDPConn).SetWriteBuffer(n)
}

func (c *delayConn) Close() error {
	c.mu.Lock()
	if !c.closed {
		c.closed = true
		close(c.queue)
	}
	c.mu.Unlock()
	return c.PacketConn.Close()
}

// samePort matches the two ends of one loopback connection by port.
func samePort(a, b net.Addr) bool {
	ua, ok1 := a.(*net.UDPAddr)
	ub, ok2 := b.(*net.UDPAddr)
	return ok1 && ok2 && ua.Port == ub.Port
}
