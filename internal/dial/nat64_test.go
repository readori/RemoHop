package dial

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/hostd"
	"github.com/readori/RemoHop/internal/nat"
)

// The IPv6-only network of Apple's App Review, simulated on loopback: the
// phone's socket has no IPv4 route (sends to IPv4 addresses fail with
// "network unreachable"), and a NAT64 gateway with the well-known prefix
// 64:ff9b::/96 carries IPv6 to the IPv4 Internet: here, to the loopback
// rendezvous server and host. The resolver's answer for ipv4only.arpa is
// the DNS64 one.

var wkp = netip.MustParsePrefix("64:ff9b::/96")

// ipv6Only is the phone's socket on that network. Underneath it is a real
// IPv4 loopback socket: what NAT64 would send for it.
//
// It is a plain net.PacketConn on purpose: given a *net.UDPConn, quic-go
// would use its ReadMsgUDP and WriteMsgUDP and go around the simulation.
type ipv6Only struct {
	uc         *net.UDPConn
	mu         sync.Mutex
	translated int
}

func newIPv6Only(t *testing.T) *ipv6Only {
	uc, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	return &ipv6Only{uc: uc}
}

// LocalAddr is an IPv6 socket's.
func (c *ipv6Only) LocalAddr() net.Addr {
	return &net.UDPAddr{IP: net.IPv6unspecified, Port: c.uc.LocalAddr().(*net.UDPAddr).Port}
}

func (c *ipv6Only) WriteTo(p []byte, addr net.Addr) (int, error) {
	ap := addr.(*net.UDPAddr).AddrPort()
	ip := ap.Addr().Unmap()
	if ip.Is4() {
		return 0, &net.OpError{Op: "write", Net: "udp", Addr: addr, Err: syscall.ENETUNREACH}
	}
	v4, ok := nat.ExtractNAT64(wkp, ip)
	if !ok {
		return 0, &net.OpError{Op: "write", Net: "udp", Addr: addr, Err: syscall.EHOSTUNREACH}
	}
	c.mu.Lock()
	c.translated++
	c.mu.Unlock()
	return c.uc.WriteTo(p, net.UDPAddrFromAddrPort(netip.AddrPortFrom(v4, ap.Port())))
}

func (c *ipv6Only) ReadFrom(p []byte) (int, net.Addr, error) {
	n, addr, err := c.uc.ReadFrom(p)
	if ua, ok := addr.(*net.UDPAddr); ok {
		v6, _ := nat.SynthesizeNAT64(wkp, ua.AddrPort().Addr())
		addr = net.UDPAddrFromAddrPort(netip.AddrPortFrom(v6, ua.AddrPort().Port()))
	}
	return n, addr, err
}

func (c *ipv6Only) Close() error                       { return c.uc.Close() }
func (c *ipv6Only) SetDeadline(t time.Time) error      { return c.uc.SetDeadline(t) }
func (c *ipv6Only) SetReadDeadline(t time.Time) error  { return c.uc.SetReadDeadline(t) }
func (c *ipv6Only) SetWriteDeadline(t time.Time) error { return c.uc.SetWriteDeadline(t) }

// ipv6OnlyWorld returns Options.Listen for sockets on the simulated network
// (wrapped as nat.Listen wraps them on iOS, unless raw) and the sockets made.
func ipv6OnlyWorld(t *testing.T, raw bool) (func() (net.PacketConn, error), func() []*ipv6Only) {
	t.Helper()
	t.Cleanup(nat.SetNAT64LookupForTest(func(context.Context) ([]netip.Addr, error) {
		a, _ := nat.SynthesizeNAT64(wkp, netip.MustParseAddr("192.0.0.170"))
		b, _ := nat.SynthesizeNAT64(wkp, netip.MustParseAddr("192.0.0.171"))
		return []netip.Addr{a, b}, nil
	}))
	var mu sync.Mutex
	var socks []*ipv6Only
	listen := func() (net.PacketConn, error) {
		s := newIPv6Only(t)
		mu.Lock()
		socks = append(socks, s)
		mu.Unlock()
		if raw {
			return s, nil
		}
		return nat.WrapNAT64(s), nil
	}
	return listen, func() []*ipv6Only { mu.Lock(); defer mu.Unlock(); return append([]*ipv6Only(nil), socks...) }
}

func translatedSends(socks []*ipv6Only) int {
	n := 0
	for _, s := range socks {
		s.mu.Lock()
		n += s.translated
		s.mu.Unlock()
	}
	return n
}

// Connecting by an IPv4 address typed by the user (the owner's
// 146.235.209.71:21118), from an IPv6-only network.
func TestDialIPv4LiteralFromIPv6OnlyNetwork(t *testing.T) {
	host := startHost(t, hostd.Config{})
	addr := host.Addr().String() // 127.0.0.1:PORT
	pw := auth.FormatPassword(host.Password())

	// Without translation the simulation fails the way Go alone does.
	raw, _ := ipv6OnlyWorld(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if c, err := Dial(ctx, addr, Options{Password: pw, Dir: t.TempDir(), Locals: noLocals, Listen: raw}); err == nil {
		c.Close()
		t.Fatal("the simulated IPv6-only network let an IPv4 literal through")
	} else if !errors.Is(err, syscall.ENETUNREACH) {
		t.Logf("without NAT64 translation: %v", err)
	}

	listen, socks := ipv6OnlyWorld(t, false)
	conn, err := Dial(testCtx(t), addr, Options{Password: pw, Dir: t.TempDir(), Locals: noLocals, Listen: listen})
	if err != nil {
		t.Fatalf("dial %s from an IPv6-only network: %v", addr, err)
	}
	defer conn.Close()
	echo(t, conn)
	if info := InfoOf(conn); info == nil || info.Remote.String() != addr {
		t.Fatalf("the session should name the address the user typed, got %+v", info)
	}
	if translatedSends(socks()) == 0 {
		t.Fatal("nothing went through NAT64")
	}
}

// Connecting by ID: the rendezvous server is configured as an IPv4
// literal, and it hands out the host's IPv4 endpoints to punch.
func TestDialByIDFromIPv6OnlyNetwork(t *testing.T) {
	sig := loopbackSignal(t) // 127.0.0.1:PORT
	host := startHost(t, hostd.Config{Signal: sig})
	listen, socks := ipv6OnlyWorld(t, false)
	conn, err := Dial(testCtx(t), host.ID(), Options{Signal: sig, Password: auth.FormatPassword(host.Password()),
		Dir: t.TempDir(), Locals: noLocals, Listen: listen})
	if err != nil {
		t.Fatalf("dial by ID from an IPv6-only network: %v", err)
	}
	defer conn.Close()
	echo(t, conn)
	info := InfoOf(conn)
	if info == nil || !info.Remote.Addr().Is4() {
		t.Fatalf("session info %+v: the host's address should read as IPv4", info)
	}
	if translatedSends(socks()) == 0 {
		t.Fatal("nothing went through NAT64")
	}
	t.Logf("path %s to %s", info.Path, info.Remote)
}
