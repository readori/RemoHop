package nat_test

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/nat"
	"github.com/readori/RemoHop/internal/nat/vnet"
)

const testALPN = "nat-test"

var (
	serverIP    = "198.51.100.1"
	reflectMain = netip.MustParseAddrPort("198.51.100.1:3478")
	reflectAlt  = netip.MustParseAddrPort("198.51.100.1:3479")
)

// world is a virtual internet with a public server answering reflect
// requests the way a rendezvous server does: on its main port (a QUIC
// transport's socket) and on a second plain UDP port.
type world struct {
	t   *testing.T
	net *vnet.Net
}

func newWorld(t *testing.T) *world {
	t.Helper()
	w := &world{t: t, net: vnet.New()}
	w.reflectServer(serverIP, 3478, 3479)
	return w
}

func (w *world) reflectServer(ip string, mainPort, altPort int) {
	w.t.Helper()
	srv := w.net.Host(ip)
	alt := srv.MustListen(altPort)
	go nat.ServeReflect(alt)
	tr := &quic.Transport{Conn: srv.MustListen(mainPort)}
	ctx, cancel := context.WithCancel(context.Background())
	go nat.ServeReflectQUIC(ctx, tr, uint16(altPort))
	w.t.Cleanup(func() {
		cancel()
		tr.Close()
		alt.Close()
	})
}

// peer is one endpoint: a socket plus what the rendezvous exchange would
// have told the other side about it.
type peer struct {
	sock       *nat.Socket
	reflexive  netip.AddrPort
	natType    nat.Type
	candidates []netip.AddrPort
}

func (w *world) peer(h *vnet.Host) *peer {
	return w.peerOn(h, h.MustListen(0))
}

func (w *world) peerOn(h *vnet.Host, pc net.PacketConn) *peer {
	w.t.Helper()
	sock, err := nat.NewSocket(pc)
	if err != nil {
		w.t.Fatal(err)
	}
	w.t.Cleanup(func() { sock.Close() })
	sock.Locals = func() []netip.Addr { return []netip.Addr{h.Addr()} }

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p := &peer{sock: sock}
	p.natType = sock.ProbeNAT(ctx, reflectMain)
	if p.reflexive, err = sock.Reflexive(ctx, reflectMain); err != nil {
		w.t.Fatalf("reflexive address: %v", err)
	}
	p.candidates = append([]netip.AddrPort{p.reflexive}, sock.Candidates()...)
	return p
}

// behind returns a peer on a fresh LAN behind a NAT of the given kind.
func (w *world) behind(publicIP, privateIP string, kind vnet.NATKind) *peer {
	return w.peer(w.net.NAT(publicIP, kind).Host(privateIP))
}

func newNonce() []byte {
	n := make([]byte, nat.NonceSize)
	rand.Read(n)
	return n
}

// serve makes host accept QUIC connections and echo on every stream.
func serve(t *testing.T, host *peer) {
	t.Helper()
	id, err := auth.NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	tlsConf, err := id.ServerTLS(testALPN, false)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := host.sock.Transport().Listen(tlsConf, quicConf())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			q, err := ln.Accept(context.Background())
			if err != nil {
				return
			}
			go func() {
				for {
					s, err := q.AcceptStream(context.Background())
					if err != nil {
						return
					}
					go func() { io.Copy(s, s); s.Close() }()
				}
			}()
		}
	}()
}

func quicConf() *quic.Config {
	return &quic.Config{HandshakeIdleTimeout: 3 * time.Second, MaxIdleTimeout: 10 * time.Second}
}

// connect runs the whole client side against host and checks that the
// resulting QUIC connection carries data.
func connect(t *testing.T, client, host *peer, punchTime time.Duration) (netip.AddrPort, error) {
	t.Helper()
	return connectAfter(t, client, host, punchTime, 0)
}

// connectAfter is connect with the client starting lead after the host, as
// it does in reality: the rendezvous server tells the host first.
func connectAfter(t *testing.T, client, host *peer, punchTime, lead time.Duration) (netip.AddrPort, error) {
	t.Helper()
	nonce := newNonce()
	hs, err := host.sock.Punch(nonce, nat.RoleHost, client.candidates, punchTime+time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer hs.Close()
	time.Sleep(lead)

	id, _ := auth.NewIdentity()
	tlsConf, err := id.ClientTLS(testALPN, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	q, addr, err := client.sock.Connect(ctx, nat.ConnectConfig{
		Nonce:     nonce,
		Remote:    host.candidates,
		TLS:       tlsConf,
		QUIC:      quicConf(),
		PunchTime: punchTime,
	})
	if err != nil {
		return netip.AddrPort{}, err
	}
	defer q.CloseWithError(0, "done")

	s, err := q.OpenStreamSync(ctx)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	msg := []byte("through the hole")
	if _, err := s.Write(msg); err != nil {
		t.Fatal(err)
	}
	s.Close()
	got, err := io.ReadAll(s)
	if err != nil || string(got) != string(msg) {
		t.Fatalf("echo = %q, %v", got, err)
	}
	if ra, _ := netip.ParseAddrPort(q.RemoteAddr().String()); ra != addr {
		t.Fatalf("Connect reported path %s but the connection runs to %s", addr, q.RemoteAddr())
	}
	return addr, nil
}

func TestPunchMatrix(t *testing.T) {
	cases := []struct {
		name         string
		client, host vnet.NATKind
		ok           bool
		// peerReflexive: the working path must be an address the host
		// never advertised.
		peerReflexive bool
	}{
		{"full-cone to full-cone", vnet.FullCone, vnet.FullCone, true, false},
		{"restricted to restricted", vnet.RestrictedCone, vnet.RestrictedCone, true, false},
		{"port-restricted to port-restricted", vnet.PortRestrictedCone, vnet.PortRestrictedCone, true, false},
		{"full-cone to port-restricted", vnet.FullCone, vnet.PortRestrictedCone, true, false},
		{"symmetric client to full-cone host", vnet.Symmetric, vnet.FullCone, true, false},
		{"full-cone client to symmetric host", vnet.FullCone, vnet.Symmetric, true, true},
		{"restricted client to symmetric host", vnet.RestrictedCone, vnet.Symmetric, true, true},
		{"symmetric client to restricted host", vnet.Symmetric, vnet.RestrictedCone, true, false},
		{"symmetric to symmetric", vnet.Symmetric, vnet.Symmetric, false, false},
		{"symmetric client to port-restricted host", vnet.Symmetric, vnet.PortRestrictedCone, false, false},
		{"port-restricted client to symmetric host", vnet.PortRestrictedCone, vnet.Symmetric, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := newWorld(t)
			client := w.behind("203.0.113.10", "192.168.1.10", tc.client)
			host := w.behind("203.0.113.20", "10.0.0.20", tc.host)
			serve(t, host)

			wantType := func(k vnet.NATKind) nat.Type {
				if k == vnet.Symmetric {
					return nat.TypeSymmetric
				}
				return nat.TypeCone
			}
			if client.natType != wantType(tc.client) || host.natType != wantType(tc.host) {
				t.Errorf("detected NAT types %s/%s, want %s/%s", client.natType, host.natType, wantType(tc.client), wantType(tc.host))
			}

			punchTime := 5 * time.Second
			if !tc.ok {
				punchTime = 1200 * time.Millisecond
			}
			start := time.Now()
			addr, err := connect(t, client, host, punchTime)
			took := time.Since(start)

			if !tc.ok {
				if !errors.Is(err, nat.ErrNoPath) {
					t.Fatalf("err = %v, want ErrNoPath", err)
				}
				if took > punchTime+time.Second {
					t.Errorf("failure took %s, want about %s", took, punchTime)
				}
				why := nat.Explain(client.natType, host.natType)
				if !strings.Contains(why, "symmetric") || !strings.Contains(why, "cannot be hole-punched") {
					t.Errorf("unhelpful diagnostic: %s", why)
				}
				return
			}
			if err != nil {
				t.Fatalf("connect: %v", err)
			}
			if took > 2*time.Second {
				t.Errorf("connect took %s", took)
			}
			advertised := false
			for _, c := range host.candidates {
				advertised = advertised || c == addr
			}
			if tc.peerReflexive == advertised {
				t.Errorf("path %s: advertised=%v, want peer-reflexive=%v (host candidates %v)", addr, advertised, tc.peerReflexive, host.candidates)
			}
			if addr.Addr() != netip.MustParseAddr("203.0.113.20") {
				t.Errorf("path %s does not lead to the host's NAT", addr)
			}
		})
	}
}

// Two machines behind the same NAT (which does not hairpin) find each other
// through their private addresses.
func TestPunchSameLAN(t *testing.T) {
	w := newWorld(t)
	box := w.net.NAT("203.0.113.10", vnet.PortRestrictedCone)
	client := w.peer(box.Host("192.168.1.10"))
	host := w.peer(box.Host("192.168.1.20"))
	serve(t, host)
	addr, err := connect(t, client, host, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if addr.Addr() != netip.MustParseAddr("192.168.1.20") {
		t.Fatalf("path %s, want the host's LAN address", addr)
	}
}

// With a hairpinning NAT both the LAN and the public path work; whichever
// wins, exactly one connection results.
func TestPunchSameLANHairpin(t *testing.T) {
	w := newWorld(t)
	box := w.net.NAT("203.0.113.10", vnet.FullCone)
	box.SetHairpin(true)
	client := w.peer(box.Host("192.168.1.10"))
	host := w.peer(box.Host("192.168.1.20"))
	serve(t, host)
	if _, err := connect(t, client, host, 5*time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestPunchPublicHost(t *testing.T) {
	w := newWorld(t)
	client := w.behind("203.0.113.10", "192.168.1.10", vnet.PortRestrictedCone)
	host := w.peer(w.net.Host("192.0.2.50"))
	serve(t, host)
	if host.natType != nat.TypeNone {
		t.Errorf("public host detected as %s, want none", host.natType)
	}
	addr, err := connect(t, client, host, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if addr.Addr() != netip.MustParseAddr("192.0.2.50") {
		t.Fatalf("path %s, want the host's public address", addr)
	}
}

// Probes are retransmitted, so punching survives a lossy, slow path.
func TestPunchLossyNetwork(t *testing.T) {
	w := newWorld(t)
	client := w.behind("203.0.113.10", "192.168.1.10", vnet.PortRestrictedCone)
	host := w.behind("203.0.113.20", "10.0.0.20", vnet.PortRestrictedCone)
	serve(t, host)
	w.net.SetLatency(15 * time.Millisecond)
	w.net.SetLoss(0.25)
	if _, err := connect(t, client, host, 6*time.Second); err != nil {
		t.Fatal(err)
	}
}

// A peer that does not know the nonce cannot make a session react.
func TestProbesNeedTheNonce(t *testing.T) {
	w := newWorld(t)
	host := w.peer(w.net.Host("192.0.2.50"))
	client := w.peer(w.net.Host("192.0.2.60"))
	stranger := w.peer(w.net.Host("192.0.2.66"))

	nonce := newNonce()
	hs, err := host.sock.Punch(nonce, nat.RoleHost, nil, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer hs.Close()

	// Wrong nonce, and the right nonce in the wrong role (a reflected
	// probe): neither may be acknowledged.
	for name, forge := range map[string]func() (*nat.Session, error){
		"wrong nonce": func() (*nat.Session, error) {
			return stranger.sock.Punch(newNonce(), nat.RoleClient, []netip.AddrPort{host.reflexive}, time.Second)
		},
		"same role": func() (*nat.Session, error) {
			return stranger.sock.Punch(nonce, nat.RoleHost, []netip.AddrPort{host.reflexive}, time.Second)
		},
	} {
		s, err := forge()
		if err != nil {
			t.Fatal(err)
		}
		select {
		case addr := <-s.Paths():
			t.Fatalf("%s: host acknowledged a forged probe (path %s)", name, addr)
		case <-time.After(400 * time.Millisecond):
		}
		s.Close()
	}

	// The real peer is acknowledged.
	cs, err := client.sock.Punch(nonce, nat.RoleClient, []netip.AddrPort{host.reflexive}, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	select {
	case addr := <-cs.Paths():
		if addr != host.reflexive {
			t.Fatalf("path %s, want %s", addr, host.reflexive)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no path to the host")
	}
}

// The reflect service must not amplify: nothing for undersized requests,
// and never more bytes out than in.
func TestReflectNoAmplification(t *testing.T) {
	w := newWorld(t)
	c := w.net.Host("192.0.2.70").MustListen(0)
	defer c.Close()
	server := net.UDPAddrFromAddrPort(reflectMain)

	read := func() (int, error) {
		c.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		n, _, err := c.ReadFrom(make([]byte, 1500))
		return n, err
	}
	for _, size := range []int{1, 4, 20, 39} {
		req := make([]byte, size)
		copy(req, []byte{0x03, 'S', 'D', 'K'})
		c.WriteTo(req, server)
		if n, err := read(); err == nil {
			t.Fatalf("%d-byte request got a %d-byte response", size, n)
		}
	}
	req := make([]byte, 40)
	copy(req, []byte{0x03, 'S', 'D', 'K'})
	c.WriteTo(req, server)
	n, err := read()
	if err != nil {
		t.Fatalf("valid request unanswered: %v", err)
	}
	if n >= len(req) {
		t.Fatalf("response (%d bytes) is not smaller than the request (%d bytes)", n, len(req))
	}
}

func TestExplain(t *testing.T) {
	for _, tc := range []struct {
		local, remote nat.Type
		want          string
	}{
		{nat.TypeSymmetric, nat.TypeSymmetric, "both sides are behind symmetric NATs"},
		{nat.TypeSymmetric, nat.TypeCone, "this side is behind a symmetric NAT"},
		{nat.TypeCone, nat.TypeSymmetric, "the peer is behind a symmetric NAT"},
		{nat.TypeCone, nat.TypeCone, "firewall"},
		{nat.TypeUnknown, nat.TypeCone, "firewall"},
	} {
		if got := nat.Explain(tc.local, tc.remote); !strings.Contains(got, tc.want) {
			t.Errorf("Explain(%s, %s) = %q, want it to mention %q", tc.local, tc.remote, got, tc.want)
		}
	}
	if nat.ParseType("cone") != nat.TypeCone || nat.ParseType("<script>") != nat.TypeUnknown {
		t.Error("ParseType does not validate")
	}
}

func TestCandidates(t *testing.T) {
	// A wildcard socket advertises its port on every local address.
	pc, err := net.ListenPacket("udp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	sock, err := nat.NewSocket(pc)
	if err != nil {
		t.Fatal(err)
	}
	defer sock.Close()
	sock.Locals = func() []netip.Addr {
		var out []netip.Addr
		for i := 1; i <= 20; i++ {
			out = append(out, netip.AddrFrom4([4]byte{10, 0, 0, byte(i)}))
		}
		return out
	}
	port := uint16(pc.LocalAddr().(*net.UDPAddr).Port)
	got := sock.Candidates()
	if len(got) != nat.MaxCandidates {
		t.Fatalf("%d candidates, want the cap of %d", len(got), nat.MaxCandidates)
	}
	for _, c := range got {
		if c.Port() != port {
			t.Fatalf("candidate %s does not carry the socket's port %d", c, port)
		}
	}

	// A socket bound to loopback has nothing to advertise.
	lo, err := nat.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer lo.Close()
	if c := lo.Candidates(); len(c) != 0 {
		t.Fatalf("loopback socket advertises %v", c)
	}

	for _, ip := range nat.InterfaceAddrs() {
		if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || ip.Is4In6() {
			t.Errorf("InterfaceAddrs returned unusable address %s", ip)
		}
	}
}

func TestProbeNAT(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	w := newWorld(t)
	// A second server whose NAT-probe port is not the one after its main
	// port, and one with no such port at all.
	w.reflectServer("198.51.100.2", 5000, 6000)
	plain := w.net.Host("198.51.100.3").MustListen(7000)
	defer plain.Close()
	go nat.ServeReflect(plain)

	for _, tc := range []struct {
		name string
		host func() *vnet.Host
		want nat.Type
	}{
		{"public", func() *vnet.Host { return w.net.Host("192.0.2.10") }, nat.TypeNone},
		{"full cone", func() *vnet.Host { return w.net.NAT("203.0.113.1", vnet.FullCone).Host("10.0.0.2") }, nat.TypeCone},
		{"port-restricted", func() *vnet.Host { return w.net.NAT("203.0.113.2", vnet.PortRestrictedCone).Host("10.0.0.2") }, nat.TypeCone},
		{"conntrack cone", func() *vnet.Host { return w.net.NAT("203.0.113.3", vnet.ConntrackCone).Host("10.0.0.2") }, nat.TypeCone},
		{"symmetric", func() *vnet.Host { return w.net.NAT("203.0.113.4", vnet.Symmetric).Host("10.0.0.2") }, nat.TypeSymmetric},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := tc.host()
			for _, server := range []string{"198.51.100.1:3478", "198.51.100.2:5000"} {
				sock, err := nat.NewSocket(h.MustListen(0))
				if err != nil {
					t.Fatal(err)
				}
				defer sock.Close()
				sock.Locals = func() []netip.Addr { return []netip.Addr{h.Addr()} }
				if got := sock.NATType(); got != nat.TypeUnknown {
					t.Fatalf("NATType before probing = %s", got)
				}
				start := time.Now()
				if got := sock.ProbeNAT(ctx, netip.MustParseAddrPort(server)); got != tc.want {
					t.Errorf("via %s: ProbeNAT = %s, want %s", server, got, tc.want)
				}
				if took := time.Since(start); took > 500*time.Millisecond {
					t.Errorf("via %s: probing took %s", server, took)
				}
				if got := sock.NATType(); got != tc.want {
					t.Errorf("via %s: cached type %s, want %s", server, got, tc.want)
				}
			}
		})
	}

	// No answer, or no second port to compare with: unknown, within the
	// caller's deadline.
	sock, err := nat.NewSocket(w.net.Host("192.0.2.11").MustListen(0))
	if err != nil {
		t.Fatal(err)
	}
	defer sock.Close()
	for _, server := range []string{"198.51.100.3:7000", "198.51.100.9:1"} {
		short, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
		start := time.Now()
		got := sock.ProbeNAT(short, netip.MustParseAddrPort(server))
		cancel()
		if got != nat.TypeUnknown || time.Since(start) > time.Second {
			t.Errorf("via %s: %s after %s, want unknown within the deadline", server, got, time.Since(start))
		}
	}
}

// plainConn hides everything but net.PacketConn, in particular the virtual
// socket's ability to send with a chosen TTL.
type plainConn struct{ net.PacketConn }

func (plainConn) SetReadBuffer(int) error  { return nil }
func (plainConn) SetWriteBuffer(int) error { return nil }

// NATs that remember unsolicited packets (Linux routers accepting input on
// the WAN side) defeat a punch in which one side's probe arrives before the
// other side has sent anything. The host hears about a connection first, so
// without countermeasure that is what happens every time. With low-TTL
// priming the host opens its own NAT without its probes reaching the
// client's.
func TestPunchThroughConntrackNATs(t *testing.T) {
	const lead = 40 * time.Millisecond // the client's head start deficit

	t.Run("without per-packet TTL the punch is lost", func(t *testing.T) {
		w := newWorld(t)
		hostHost := w.net.NAT("203.0.113.20", vnet.ConntrackCone).Host("10.0.0.20")
		host := w.peerOn(hostHost, plainConn{hostHost.MustListen(0)})
		client := w.behind("203.0.113.10", "192.168.1.10", vnet.ConntrackCone)
		serve(t, host)
		if _, err := connectAfter(t, client, host, 1500*time.Millisecond, lead); !errors.Is(err, nat.ErrNoPath) {
			t.Fatalf("err = %v, want ErrNoPath (if this connects, the simulated NAT no longer models the problem)", err)
		}
	})

	t.Run("with priming it connects", func(t *testing.T) {
		for _, kinds := range [][2]vnet.NATKind{
			{vnet.ConntrackCone, vnet.ConntrackCone},
			{vnet.ConntrackCone, vnet.PortRestrictedCone},
			{vnet.PortRestrictedCone, vnet.ConntrackCone},
		} {
			w := newWorld(t)
			client := w.behind("203.0.113.10", "192.168.1.10", kinds[0])
			host := w.behind("203.0.113.20", "10.0.0.20", kinds[1])
			serve(t, host)
			start := time.Now()
			addr, err := connectAfter(t, client, host, 5*time.Second, lead)
			if err != nil {
				t.Fatalf("%v client, %v host: %v", kinds[0], kinds[1], err)
			}
			if addr != host.reflexive {
				t.Errorf("path %s, want the host's reflexive address %s", addr, host.reflexive)
			}
			// The hold must not cost the common case anything: the
			// client's first probe goes straight through.
			if took := time.Since(start) - lead; took > 250*time.Millisecond {
				t.Errorf("%v client, %v host: connect took %s after the client started", kinds[0], kinds[1], took)
			}
		}
	})

	t.Run("a late client still connects after the hold", func(t *testing.T) {
		w := newWorld(t)
		client := w.behind("203.0.113.10", "192.168.1.10", vnet.PortRestrictedCone)
		host := w.behind("203.0.113.20", "10.0.0.20", vnet.ConntrackCone)
		serve(t, host)
		if _, err := connectAfter(t, client, host, 5*time.Second, 2300*time.Millisecond); err != nil {
			t.Fatal(err)
		}
	})

	// When the two NATs are so close that even a TTL of 2 reaches the
	// other one, priming cannot help and the outcome is the unprimed one.
	t.Run("adjacent NATs are beyond help", func(t *testing.T) {
		w := newWorld(t)
		w.net.SetHops(0)
		client := w.behind("203.0.113.10", "192.168.1.10", vnet.ConntrackCone)
		host := w.behind("203.0.113.20", "10.0.0.20", vnet.ConntrackCone)
		serve(t, host)
		_, err := connectAfter(t, client, host, 1500*time.Millisecond, lead)
		if !errors.Is(err, nat.ErrNoPath) {
			t.Fatalf("err = %v, want ErrNoPath", err)
		}
		if why := nat.Explain(client.natType, host.natType); !strings.Contains(why, "unsolicited inbound") {
			t.Errorf("diagnostic does not name the likely cause: %s", why)
		}
	})
}

// A host must not be usable to spray probes at addresses of somebody's
// choosing: of the globally routable candidates it is handed, it probes
// only a couple. Private candidates are not limited that way.
func TestPunchProbesFewRoutableAddresses(t *testing.T) {
	w := newWorld(t)
	host := w.peer(w.net.Host("192.0.2.50"))

	var victims []*vnet.Conn
	var candidates []netip.AddrPort
	for i := 0; i < 6; i++ {
		ip := netip.AddrFrom4([4]byte{192, 0, 2, byte(100 + i)})
		c := w.net.Host(ip.String()).MustListen(9)
		defer c.Close()
		victims = append(victims, c)
		candidates = append(candidates, netip.AddrPortFrom(ip, 9))
	}
	hs, err := host.sock.Punch(newNonce(), nat.RoleHost, candidates, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(700 * time.Millisecond)
	hs.Close()

	probed, packets := 0, 0
	for _, c := range victims {
		n := 0
		for {
			c.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
			if _, _, err := c.ReadFrom(make([]byte, 100)); err != nil {
				break
			}
			n++
		}
		if n > 0 {
			probed++
		}
		packets += n
	}
	if probed != 2 {
		t.Fatalf("%d of 6 routable candidates were probed, want 2", probed)
	}
	if packets == 0 || packets > 40 {
		t.Fatalf("%d probe packets sent in 0.7 s", packets)
	}
}

func TestPunchTime(t *testing.T) {
	if d := nat.PunchTime(nat.TypeSymmetric, nat.TypeSymmetric); d > 1500*time.Millisecond {
		t.Errorf("two symmetric NATs: %s, want a quick failure", d)
	}
	if d := nat.PunchTime(nat.TypeCone, nat.TypeSymmetric); d >= nat.DefaultPunchTime || d < 2*time.Second {
		t.Errorf("one symmetric NAT: %s", d)
	}
	for _, pair := range [][2]nat.Type{{nat.TypeCone, nat.TypeCone}, {nat.TypeUnknown, nat.TypeSymmetric}, {nat.TypeNone, nat.TypeUnknown}} {
		want := nat.DefaultPunchTime
		if pair[1] == nat.TypeSymmetric {
			want = nat.PunchTime(nat.TypeCone, nat.TypeSymmetric)
		}
		if d := nat.PunchTime(pair[0], pair[1]); d != want {
			t.Errorf("PunchTime(%s, %s) = %s, want %s", pair[0], pair[1], d, want)
		}
	}
}

// relayWorld starts a relay session on a public host and returns its
// address and a channel with its final statistics.
func relayWorld(t *testing.T, w *world, nonce []byte, limits nat.RelayLimits) (netip.AddrPort, <-chan nat.RelayStats) {
	t.Helper()
	pc := w.net.Host("198.51.100.9").MustListen(0)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	stats := make(chan nat.RelayStats, 1)
	go func() { stats <- nat.ServeRelay(ctx, pc, nonce, limits) }()
	return netip.MustParseAddrPort(pc.LocalAddr().String()), stats
}

// Two peers behind symmetric NATs, which cannot be punched, run QUIC
// through a relay session; nobody else can use that session.
func TestRelay(t *testing.T) {
	w := newWorld(t)
	client := w.behind("203.0.113.10", "192.168.1.10", vnet.Symmetric)
	host := w.behind("203.0.113.20", "10.0.0.20", vnet.Symmetric)
	serve(t, host)
	nonce := newNonce()
	relay, _ := relayWorld(t, w, nonce, nat.RelayLimits{Rate: 1 << 20})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// A stranger: no nonce, so no bind, no acknowledgement, nothing
	// forwarded, and nothing sent back to it at all.
	stranger := w.net.Host("192.0.2.66").MustListen(0)
	defer stranger.Close()
	strangerSock := w.peer(w.net.Host("192.0.2.67"))
	short, cancelShort := context.WithTimeout(ctx, 400*time.Millisecond)
	if err := strangerSock.sock.BindRelay(short, relay, newNonce(), nat.RoleClient); err == nil {
		t.Fatal("a bind with the wrong nonce was acknowledged")
	}
	cancelShort()
	if err := client.sock.BindRelay(ctx, relay, nonce, nat.RoleClient); err != nil {
		t.Fatalf("client bind: %v", err)
	}
	// With only one side attached nothing is forwarded yet.
	if err := host.sock.BindRelay(ctx, relay, nonce, nat.RoleHost); err != nil {
		t.Fatalf("host bind: %v", err)
	}
	for _, size := range []int{1, 40, 1200} {
		pkt := make([]byte, size)
		pkt[0] = 0x40
		stranger.WriteTo(pkt, net.UDPAddrFromAddrPort(relay))
	}
	stranger.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if n, _, err := stranger.ReadFrom(make([]byte, 2000)); err == nil {
		t.Fatalf("the relay answered a stranger with %d bytes", n)
	}

	id, _ := auth.NewIdentity()
	tlsConf, _ := id.ClientTLS(testALPN, nil)
	q, err := client.sock.Transport().Dial(ctx, net.UDPAddrFromAddrPort(relay), tlsConf, quicConf())
	if err != nil {
		t.Fatalf("QUIC through the relay: %v", err)
	}
	defer q.CloseWithError(0, "")
	s, err := q.OpenStreamSync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	msg := make([]byte, 100_000)
	rand.Read(msg)
	go func() { s.Write(msg); s.Close() }()
	got, err := io.ReadAll(s)
	if err != nil || string(got) != string(msg) {
		t.Fatalf("echo through the relay: %d bytes, %v", len(got), err)
	}
}

// The relay enforces its bandwidth budgets by dropping, and ends a session
// that has gone quiet.
func TestRelayCaps(t *testing.T) {
	w := newWorld(t)
	a := w.peer(w.net.Host("192.0.2.10"))
	b := w.net.Host("192.0.2.20").MustListen(0)
	defer b.Close()
	nonce := newNonce()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	for name, limits := range map[string]nat.RelayLimits{
		"per session": {Rate: 20_000, Idle: 400 * time.Millisecond},
		"global":      {Rate: 1 << 30, Global: nat.NewByteBudget(20_000), Idle: 400 * time.Millisecond},
	} {
		relay, stats := relayWorld(t, w, nonce, limits)
		if err := a.sock.BindRelay(ctx, relay, nonce, nat.RoleClient); err != nil {
			t.Fatal(err)
		}
		// The other side binds by hand: a raw socket playing the host.
		hostSock, err := nat.NewSocket(b)
		_ = hostSock
		if err != nil {
			t.Fatal(err)
		}
		if err := hostSock.BindRelay(ctx, relay, nonce, nat.RoleHost); err != nil {
			t.Fatal(err)
		}
		// 200 kB offered within a moment against a 20 kB/s budget.
		pkt := make([]byte, 1000)
		pkt[0] = 0x40
		for i := 0; i < 200; i++ {
			a.sock.Transport().WriteTo(pkt, net.UDPAddrFromAddrPort(relay))
		}
		start := time.Now()
		st := <-stats // returns when the session has idled out
		if st.Bytes > 25_000 || st.Bytes < 15_000 || st.Dropped < 170 {
			t.Errorf("%s cap: relayed %d bytes, dropped %d packets of 200", name, st.Bytes, st.Dropped)
		}
		if idle := time.Since(start); idle > 2*time.Second {
			t.Errorf("%s: idle session ended after %s", name, idle)
		}
		// The session's socket is gone: a new bind gets no answer.
		short, c := context.WithTimeout(ctx, 300*time.Millisecond)
		if err := a.sock.BindRelay(short, relay, nonce, nat.RoleClient); err == nil {
			t.Errorf("%s: relay still answering after the idle timeout", name)
		}
		c()
		hostSock.Close()
		b = w.net.Host("192.0.2.20").MustListen(0)
	}
}

// connectPrefer is connect with a preference, returning the time it took.
func connectPrefer(t *testing.T, client, host *peer, remote []netip.AddrPort, prefer func(netip.AddrPort) bool, wait time.Duration) (netip.AddrPort, time.Duration) {
	t.Helper()
	nonce := newNonce()
	hs, err := host.sock.Punch(nonce, nat.RoleHost, client.candidates, 6*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer hs.Close()
	id, _ := auth.NewIdentity()
	tlsConf, _ := id.ClientTLS(testALPN, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	q, addr, err := client.sock.Connect(ctx, nat.ConnectConfig{
		Nonce: nonce, Remote: remote, TLS: tlsConf, QUIC: quicConf(), PunchTime: 5 * time.Second,
		Prefer: prefer, PreferWait: wait,
	})
	took := time.Since(start)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	q.CloseWithError(0, "done")
	return addr, took
}

// A proven path that is not preferred waits for a preferred one, for at
// most PreferWait; a preferred path is taken at once. The punch itself is
// unchanged: every candidate is probed.
func TestPunchPreference(t *testing.T) {
	w := newWorld(t)
	client := w.behind("203.0.113.10", "192.168.1.10", vnet.PortRestrictedCone)
	host := w.peer(w.net.Host("192.0.2.50"))
	serve(t, host)
	public := netip.MustParseAddr("192.0.2.50")
	// A tailnet address of the host that never answers (its overlay is down).
	dead := netip.MustParseAddrPort("100.100.9.9:4242")
	remote := append([]netip.AddrPort{dead}, host.candidates...)
	const wait = 600 * time.Millisecond

	isDead := func(ap netip.AddrPort) bool { return ap == dead }
	addr, took := connectPrefer(t, client, host, remote, isDead, wait)
	if addr.Addr() != public {
		t.Fatalf("path %s", addr)
	}
	if took < wait {
		t.Errorf("a non-preferred path was taken after %s, before the preferred one had %s to answer", took, wait)
	}
	if took > wait+2*time.Second {
		t.Errorf("connect took %s", took)
	}

	isPublic := func(ap netip.AddrPort) bool { return ap.Addr() == public }
	addr, took = connectPrefer(t, client, host, remote, isPublic, 5*time.Second)
	if addr.Addr() != public || took > 2*time.Second {
		t.Errorf("preferred path %s took %s", addr, took)
	}

	// Without a preference nothing waits.
	addr, took = connectPrefer(t, client, host, remote, nil, 0)
	if addr.Addr() != public || took > wait {
		t.Errorf("no preference: path %s took %s", addr, took)
	}
}

// lossyStart drops the large packets (QUIC Initials are padded to at least
// 1200 bytes; punch probes are small) that arrive before until.
type lossyStart struct {
	net.PacketConn
	mu    sync.Mutex
	until time.Time
}

func (l *lossyStart) ReadFrom(p []byte) (int, net.Addr, error) {
	for {
		n, a, err := l.PacketConn.ReadFrom(p)
		l.mu.Lock()
		drop := err == nil && n >= 1200 && time.Now().Before(l.until)
		l.mu.Unlock()
		if !drop {
			return n, a, err
		}
	}
}

func (*lossyStart) SetReadBuffer(int) error  { return nil }
func (*lossyStart) SetWriteBuffer(int) error { return nil }

// A path that the punch has proven but that loses the first QUIC packets
// (a Wi-Fi station waking up, a firewall entry being set up) is not left to
// QUIC's exponential retransmission: further handshakes are started over
// it, and the first to finish wins.
func TestPunchHedgesLostHandshake(t *testing.T) {
	w := newWorld(t)
	box := w.net.NAT("203.0.113.10", vnet.PortRestrictedCone)
	client := w.peer(box.Host("192.168.1.10"))
	hostBox := box.Host("192.168.1.20")
	pc := &lossyStart{PacketConn: hostBox.MustListen(0)}
	host := w.peerOn(hostBox, pc)
	serve(t, host)
	const lossy = 2500 * time.Millisecond
	pc.mu.Lock()
	pc.until = time.Now().Add(lossy)
	pc.mu.Unlock()
	start := time.Now()
	addr, err := connect(t, client, host, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	took := time.Since(start)
	if addr.Addr() != netip.MustParseAddr("192.168.1.20") {
		t.Fatalf("path %s, want the host's LAN address", addr)
	}
	// Without hedging the handshake waits for QUIC's retransmission at
	// 4.5 s; the hedge started at 2.4 s retransmits at about 2.7 s.
	if took < lossy || took > 3800*time.Millisecond {
		t.Fatalf("connected after %s with the first %s of handshake packets lost", took, lossy)
	}
	t.Logf("connected after %s", took)
}

// A network the mesh tunnels (nat.ExcludeNetworks) never carries a punched
// path, whichever side knows about it: two devices of a mesh would
// otherwise prove the "LAN" path through an existing mesh link, and a link
// set up over it dies with the link it rides on. The LAN here stands for
// that network: it works, but must not be used.
func TestPunchAvoidsExcludedNetworks(t *testing.T) {
	tunnel := netip.MustParsePrefix("10.78.0.0/16")
	inTunnel := func(ip netip.Addr) bool { return tunnel.Contains(ip) }
	for _, tc := range []struct {
		name                   string
		clientKnows, hostKnows bool
		hairpin                bool
	}{
		{"both sides", true, true, false},
		{"host only (a client that does not filter)", false, true, false},
		{"client only (a host that does not filter)", true, false, false},
		{"both sides, hairpin NAT: the public path", true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			kind := vnet.PortRestrictedCone
			if tc.hairpin {
				kind = vnet.FullCone
			}
			box := w.net.NAT("203.0.113.10", kind)
			box.SetHairpin(tc.hairpin)
			client := w.peer(box.Host("10.78.1.10"))
			host := w.peer(box.Host("10.78.1.20"))
			for _, p := range []struct {
				peer  *peer
				knows bool
			}{{client, tc.clientKnows}, {host, tc.hostKnows}} {
				if !p.knows {
					continue
				}
				p.peer.sock.Excluded = inTunnel
				p.peer.candidates = append([]netip.AddrPort{p.peer.reflexive}, p.peer.sock.Candidates()...)
				for _, c := range p.peer.candidates {
					if inTunnel(c.Addr()) {
						t.Fatalf("advertises %s, an address in the excluded network", c)
					}
				}
			}
			serve(t, host)
			addr, err := connect(t, client, host, 1500*time.Millisecond)
			if tc.hairpin {
				if err != nil || addr != host.reflexive {
					t.Fatalf("path %s (err %v), want the public %s", addr, err, host.reflexive)
				}
				return
			}
			if err == nil {
				t.Fatalf("connected over %s, through the excluded network", addr)
			}
			if !errors.Is(err, nat.ErrNoPath) {
				t.Fatalf("err = %v, want ErrNoPath", err)
			}
		})
	}
}

func TestExcludeNetworks(t *testing.T) {
	a, b := netip.MustParseAddr("10.78.3.4"), netip.MustParseAddr("fd44:1::5")
	if nat.Excluded(a) {
		t.Fatalf("%s excluded before anything was registered", a)
	}
	r1 := nat.ExcludeNetworks(netip.MustParsePrefix("10.78.0.0/16"), netip.MustParsePrefix("fd44:1::/64"))
	r2 := nat.ExcludeNetworks(netip.MustParsePrefix("10.78.9.9/16")) // the same network, unmasked
	if !nat.Excluded(a) || !nat.Excluded(b) || !nat.Excluded(netip.AddrFrom16(a.As16())) {
		t.Fatal("registered networks are not excluded")
	}
	if nat.Excluded(netip.MustParseAddr("10.79.0.1")) {
		t.Fatal("an address outside the networks is excluded")
	}
	r1()
	r1() // a second release changes nothing
	if !nat.Excluded(a) || nat.Excluded(b) {
		t.Fatal("releasing one registration of a network registered twice must keep it")
	}
	r2()
	if nat.Excluded(a) {
		t.Fatal("still excluded after every release")
	}

	// InterfaceAddrs and Candidates leave them out.
	addrs := nat.InterfaceAddrs()
	if len(addrs) == 0 {
		t.Skip("no usable interface address here")
	}
	release := nat.ExcludeNetworks(netip.PrefixFrom(addrs[0], addrs[0].BitLen()))
	defer release()
	for _, ip := range nat.InterfaceAddrs() {
		if ip == addrs[0] {
			t.Fatalf("InterfaceAddrs still lists %s", ip)
		}
	}
}
