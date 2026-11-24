package vpn

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/config"
	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/peer/peertest"
	"github.com/readori/RemoHop/internal/wire"
)

func ipChecksum(b []byte) uint16 {
	var sum uint32
	for i := 0; i+1 < len(b); i += 2 {
		sum += uint32(binary.BigEndian.Uint16(b[i:]))
	}
	if len(b)%2 == 1 {
		sum += uint32(b[len(b)-1]) << 8
	}
	for sum>>16 != 0 {
		sum = sum&0xffff + sum>>16
	}
	return ^uint16(sum)
}

// packet builds an IPv4 packet with the given payload size.
func packet(src, dst string, proto byte, payload []byte) []byte {
	p := make([]byte, 20+len(payload))
	p[0] = 0x45
	binary.BigEndian.PutUint16(p[2:], uint16(len(p)))
	p[8] = 64
	p[9] = proto
	s, d := netip.MustParseAddr(src).As4(), netip.MustParseAddr(dst).As4()
	copy(p[12:], s[:])
	copy(p[16:], d[:])
	binary.BigEndian.PutUint16(p[10:], ipChecksum(p[:20]))
	copy(p[20:], payload)
	return p
}

// tcpSyn builds a SYN segment with an MSS option, checksummed.
func tcpSyn(src, dst string, mss uint16) []byte {
	tcp := make([]byte, 24)
	binary.BigEndian.PutUint16(tcp[0:], 40000)
	binary.BigEndian.PutUint16(tcp[2:], 80)
	tcp[12] = 6 << 4
	tcp[13] = tcpSYN
	tcp[20], tcp[21] = 2, 4
	binary.BigEndian.PutUint16(tcp[22:], mss)
	p := packet(src, dst, protoTCP, tcp)
	binary.BigEndian.PutUint16(p[20+16:], tcpChecksum(p))
	return p
}

func tcpChecksum(p []byte) uint16 {
	seg := append([]byte(nil), p[20:]...)
	seg[16], seg[17] = 0, 0
	pseudo := append(append([]byte{}, p[12:20]...), 0, protoTCP, byte(len(seg)>>8), byte(len(seg)))
	return ipChecksum(append(pseudo, seg...))
}

func TestParse(t *testing.T) {
	good := packet("10.77.0.2", "10.77.0.1", 1, make([]byte, 8))
	h, ok := parse(good)
	if !ok || h.src.String() != "10.77.0.2" || h.dst.String() != "10.77.0.1" || h.proto != 1 || !h.first {
		t.Fatalf("parse: %+v %v", h, ok)
	}
	v6 := append([]byte{0x60}, make([]byte, 39)...)
	short := good[:10]
	padded := append(append([]byte{}, good...), 0, 0)
	badIHL := append([]byte{}, good...)
	badIHL[0] = 0x4f
	for name, p := range map[string][]byte{"ipv6": v6, "truncated": short, "length mismatch": padded, "header longer than packet": badIHL, "empty": nil} {
		if _, ok := parse(p); ok {
			t.Errorf("%s packet accepted", name)
		}
	}
}

func TestClampMSS(t *testing.T) {
	syn := tcpSyn("10.77.0.2", "192.168.1.50", 1460)
	h, _ := parse(syn)
	clampMSS(syn, h, 1160)
	if got := binary.BigEndian.Uint16(syn[20+22:]); got != 1160 {
		t.Fatalf("MSS = %d, want 1160", got)
	}
	if got, want := binary.BigEndian.Uint16(syn[20+16:]), tcpChecksum(syn); got != want {
		t.Fatalf("TCP checksum %#x after clamping, a fresh computation gives %#x", got, want)
	}
	// A smaller MSS, a non-SYN and non-TCP are left alone.
	small := tcpSyn("10.77.0.2", "192.168.1.50", 536)
	before := append([]byte{}, small...)
	h, _ = parse(small)
	clampMSS(small, h, 1160)
	if string(small) != string(before) {
		t.Fatal("a SYN with a small MSS was modified")
	}
	ack := tcpSyn("10.77.0.2", "192.168.1.50", 1460)
	ack[20+13] = 0x10
	before = append([]byte{}, ack...)
	h, _ = parse(ack)
	clampMSS(ack, h, 1160)
	if string(ack) != string(before) {
		t.Fatal("a non-SYN segment was modified")
	}
	// Malformed options must not panic or loop.
	for _, opts := range [][]byte{{2, 0, 0, 0}, {2, 9, 0, 0}, {3}, {1, 1, 1, 2}} {
		bad := tcpSyn("10.77.0.2", "192.168.1.50", 1460)
		copy(bad[40:], opts)
		h, _ = parse(bad)
		clampMSS(bad, h, 1160)
	}
}

func TestNetsRoutesAndMTU(t *testing.T) {
	nets := freeNets(nil, 3)
	if len(nets) != 3 || nets[0].String() != "10.77.0.0/30" || nets[1].String() != "10.77.0.4/30" {
		t.Fatalf("freeNets: %v", nets)
	}
	used := []netip.Prefix{netip.MustParsePrefix("10.77.0.0/24"), netip.MustParsePrefix("10.77.1.2/32")}
	if nets = freeNets(used, 2); nets[0].String() != "10.77.1.4/30" {
		t.Fatalf("freeNets around used networks: %v", nets)
	}
	if nets = freeNets([]netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}, 2); len(nets) != 0 {
		t.Fatalf("freeNets with the pool taken: %v", nets)
	}
	h, c := endpoints(netip.MustParsePrefix("10.77.0.4/30"))
	if h.String() != "10.77.0.5" || c.String() != "10.77.0.6" {
		t.Fatalf("endpoints: %s %s", h, c)
	}
	for _, bad := range []string{"10.77.0.0/29", "10.77.0.1/30", "10.78.0.0/30", "192.168.0.0/30", "x"} {
		if _, ok := parseNet(bad); ok {
			t.Errorf("parseNet(%q) accepted", bad)
		}
	}
	for _, bad := range [][]string{{"0.0.0.0/0"}, {"192.168.1.5/24"}, {"10.77.3.0/24"}, {"10.0.0.0/8"}, {"127.0.0.0/8"}, {"224.0.0.0/4"}, {"fd00::/8"}, {"junk"}} {
		if _, err := parseRoutes(bad); err == nil {
			t.Errorf("parseRoutes(%v) accepted", bad)
		}
	}
	if _, err := parseRoutes(make([]string, maxRoutes+1)); err == nil {
		t.Error("too many routes accepted")
	}
	if r, err := parseRoutes([]string{"192.168.1.0/24", "172.16.0.0/12"}); err != nil || len(r) != 2 {
		t.Errorf("parseRoutes: %v %v", r, err)
	}
	for _, tc := range []struct{ a, b, want int }{{1241, 1300, 1201}, {0, 1200, maxMSS}, {1200, 0, maxMSS}, {9000, 9000, maxMSS}, {300, 1200, minMSS}} {
		if got := pickMSS(tc.a, tc.b); got != tc.want {
			t.Errorf("pickMSS(%d, %d) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
	if !isLocalRoute(netip.MustParsePrefix("192.168.1.0/24"), []netip.Prefix{netip.MustParsePrefix("192.168.0.0/16")}) ||
		isLocalRoute(netip.MustParsePrefix("192.168.0.0/16"), []netip.Prefix{netip.MustParsePrefix("192.168.1.0/24")}) {
		t.Error("isLocalRoute")
	}
}

// fakeDev is a tunnel interface whose kernel side is the test.
type fakeDev struct {
	name      string
	fromOS    chan []byte // packets the "kernel" routes into the tunnel
	toOS      chan []byte // packets the tunnel hands to the "kernel"
	closeOnce sync.Once
	closed    chan struct{}
}

func newFakeDev(name string) *fakeDev {
	return &fakeDev{name: name, fromOS: make(chan []byte, 64), toOS: make(chan []byte, 64), closed: make(chan struct{})}
}

func (d *fakeDev) Read(p []byte) (int, error) {
	select {
	case pkt := <-d.fromOS:
		return copy(p, pkt), nil
	case <-d.closed:
		return 0, os.ErrClosed
	}
}

func (d *fakeDev) Write(p []byte) (int, error) {
	select {
	case d.toOS <- append([]byte(nil), p...):
	default:
	}
	return len(p), nil
}

func (d *fakeDev) Close() error { d.closeOnce.Do(func() { close(d.closed) }); return nil }
func (d *fakeDev) Name() string { return d.name }

func (d *fakeDev) expect(t *testing.T, what string) []byte {
	t.Helper()
	select {
	case p := <-d.toOS:
		return p
	case <-time.After(3 * time.Second):
		t.Fatalf("%s: no packet arrived", what)
		return nil
	}
}

func (d *fakeDev) expectNone(t *testing.T, what string) {
	t.Helper()
	select {
	case p := <-d.toOS:
		t.Fatalf("%s: a packet got through (%d bytes)", what, len(p))
	case <-time.After(200 * time.Millisecond):
	}
}

// fakeSystem replaces the operating system with recorders.
type fakeSystem struct {
	mu         sync.Mutex
	devs       []*fakeDev
	used       []netip.Prefix
	configured []string
	routing    []clientRouting
	forwarded  []string
	released   int
	privErr    error
}

func (f *fakeSystem) install(t *testing.T) {
	t.Helper()
	old := sys
	t.Cleanup(func() { sys = old })
	sys = platform{
		privileged:    func() error { return f.privErr },
		hostSupported: func() error { return nil },
		open: func(string) (Device, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			d := newFakeDev("fake" + string(rune('0'+len(f.devs))))
			f.devs = append(f.devs, d)
			return d, nil
		},
		usedNets: func() []netip.Prefix { return f.used },
		configure: func(dev string, local, peer netip.Addr, mtu int) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.configured = append(f.configured, dev+" "+local.String()+"->"+peer.String())
			return nil
		},
		clientRoutes: func(st *state, dev string, o clientRouting) ([]string, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.routing = append(f.routing, o)
			return nil, nil
		},
		hostForward: func(st *state, dev string, client netip.Addr) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.forwarded = append(f.forwarded, dev+" "+client.String())
			return nil
		},
		hostRelease: func() { f.mu.Lock(); f.released++; f.mu.Unlock() },
		resolvers:   func() []string { return []string{"9.9.9.9", "not-an-address"} },
	}
}

func (f *fakeSystem) dev(t *testing.T, i int) *fakeDev {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		f.mu.Lock()
		if len(f.devs) > i {
			d := f.devs[i]
			f.mu.Unlock()
			return d
		}
		f.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatalf("tunnel device %d was never opened", i)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func testCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// The whole thing over a real session, with the kernel replaced: request,
// grant, packets both ways, and what each side refuses to carry.
func TestTunnelOverSession(t *testing.T) {
	t.Setenv("REMOHOP_CONFIG_DIR", t.TempDir())
	fs := &fakeSystem{}
	fs.install(t)
	client, _ := peertest.Pair(t)
	// The host is attached to 192.168.1.0/24 for the purposes of "local".
	lan := netip.MustParsePrefix("192.168.1.0/24")
	oldConnected := connected
	connected = func() []netip.Prefix { return []netip.Prefix{lan} }
	defer func() { connected = oldConnected }()

	ctx, cancel := context.WithCancel(testCtx(t))
	tun, err := Up(ctx, client, Options{Routes: []netip.Prefix{lan}})
	if err != nil {
		t.Fatalf("Up: %v", err)
	}
	if tun.Local.String() != "10.77.0.2" || tun.Peer.String() != "10.77.0.1" || tun.MTU != tunnelMTU || tun.MSS < 1100 || tun.MSS > 1300 || len(tun.Notes) != 0 {
		t.Fatalf("tunnel %+v", tun)
	}
	done := make(chan error, 1)
	go func() { done <- tun.Run(ctx) }()
	hostDev, clientDev := fs.dev(t, 0), fs.dev(t, 1)
	if got := strings.Join(fs.configured, "; "); got != "fake0 10.77.0.1->10.77.0.2; fake1 10.77.0.2->10.77.0.1" {
		t.Fatalf("interfaces configured: %s", got)
	}
	if len(fs.forwarded) != 1 || fs.forwarded[0] != "fake0 10.77.0.2" {
		t.Fatalf("host forwarding: %v", fs.forwarded)
	}
	if len(fs.routing) != 1 || len(fs.routing[0].routes) != 1 || fs.routing[0].exit {
		t.Fatalf("client routing: %+v", fs.routing)
	}

	// Client to host, to the LAN behind it, and answers back.
	ping := packet("10.77.0.2", "10.77.0.1", 1, []byte("echo request"))
	clientDev.fromOS <- ping
	if got := hostDev.expect(t, "client → host"); string(got) != string(ping) {
		t.Fatal("packet changed in transit")
	}
	hostDev.fromOS <- packet("10.77.0.1", "10.77.0.2", 1, []byte("echo reply"))
	clientDev.expect(t, "host → client")
	clientDev.fromOS <- packet("10.77.0.2", "192.168.1.50", 17, []byte("to the LAN"))
	hostDev.expect(t, "client → host LAN")
	hostDev.fromOS <- packet("192.168.1.50", "10.77.0.2", 17, []byte("from the LAN"))
	clientDev.expect(t, "host LAN → client")

	// A full-size TCP segment fits a datagram: that is what the clamp is
	// for. A larger packet of another protocol still arrives, the slow way.
	clientDev.fromOS <- packet("10.77.0.2", "10.77.0.1", protoTCP, make([]byte, tun.MSS+20))
	hostDev.expect(t, "MSS-sized segment")
	if tun.Stats.ViaStream.Load() != 0 {
		t.Fatal("a full TCP segment did not fit a datagram")
	}
	clientDev.fromOS <- packet("10.77.0.2", "10.77.0.1", 17, make([]byte, tun.MTU-20))
	if got := hostDev.expect(t, "MTU-sized packet"); len(got) != tun.MTU {
		t.Fatalf("got %d bytes", len(got))
	}
	// Whether it needed the stream depends on the datagram size QUIC has
	// discovered for the path (large on some loopback interfaces, e.g.
	// Windows); either way it must arrive intact, as checked above.
	if n := tun.Stats.ViaStream.Load(); n > 1 {
		t.Fatalf("%d packets took the stream, want at most 1", n)
	}
	// TCP SYNs are clamped on the way through.
	clientDev.fromOS <- tcpSyn("10.77.0.2", "192.168.1.50", 1460)
	if got := binary.BigEndian.Uint16(hostDev.expect(t, "SYN")[20+22:]); int(got) != tun.MSS {
		t.Fatalf("MSS %d through a tunnel that clamps to %d", got, tun.MSS)
	}

	// What the client will not send: other sources (it does not forward
	// for networks behind it), destinations that were not asked for,
	// oversized and non-IPv4 packets.
	for what, p := range map[string][]byte{
		"forwarded source":       packet("192.168.2.9", "10.77.0.1", 1, nil),
		"destination not routed": packet("10.77.0.2", "8.8.8.8", 1, nil),
		"another tunnel":         packet("10.77.0.2", "10.77.0.5", 1, nil),
		"over the MTU":           packet("10.77.0.2", "10.77.0.1", 1, make([]byte, tun.MTU)),
		"not IPv4":               append([]byte{0x60}, make([]byte, 60)...),
	} {
		clientDev.fromOS <- p
		hostDev.expectNone(t, "client sending "+what)
	}
	// What the client will not accept from the host: anything not for its
	// tunnel address (the host cannot reach the client's other networks
	// even if the client's kernel would forward), or from somewhere the
	// user did not ask to reach.
	for what, p := range map[string][]byte{
		"to the client's LAN":  packet("10.77.0.1", "192.168.2.1", 1, nil),
		"from an unasked net":  packet("8.8.8.8", "10.77.0.2", 1, nil),
		"to another tunnel ip": packet("10.77.0.1", "10.77.0.6", 1, nil),
	} {
		hostDev.fromOS <- p
		clientDev.expectNone(t, "host sending "+what)
	}
	if tun.Stats.DroppedOut.Load() != 5 {
		t.Errorf("client refused %d of its own packets, want 5", tun.Stats.DroppedOut.Load())
	}

	// One tunnel per session.
	if _, err := Up(ctx, client, Options{}); err == nil || !strings.Contains(err.Error(), "already has a VPN tunnel") {
		t.Fatalf("second tunnel on one session: %v", err)
	}

	// Ending the client ends both sides and undoes everything.
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run after cancel: %v", err)
	}
	select {
	case <-hostDev.closed:
	case <-time.After(3 * time.Second):
		t.Fatal("host device still open after the client left")
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		dir, _ := stateDir()
		ents, _ := os.ReadDir(dir)
		fs.mu.Lock()
		released := fs.released
		fs.mu.Unlock()
		if len(ents) == 0 && released == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d state files left, host released %d times", len(ents), released)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The host enforces its side whatever the client sends: a client that
// does not filter (a modified one) gets nowhere it was not granted.
func TestHostRefusesSpoofedAndUngranted(t *testing.T) {
	t.Setenv("REMOHOP_CONFIG_DIR", t.TempDir())
	fs := &fakeSystem{}
	fs.install(t)
	lan := netip.MustParsePrefix("192.168.1.0/24")
	oldConnected := connected
	connected = func() []netip.Prefix { return []netip.Prefix{lan} }
	defer func() { connected = oldConnected }()
	client, _ := peertest.Pair(t)
	ctx := testCtx(t)

	// A hand-rolled client: the request, then raw packets on the stream.
	s, err := client.Open(ctx, Service, Request{V: protoVersion, Nets: []string{"10.77.0.0/30"}, Routes: []string{lan.String()},
		MaxDatagram: 1200})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Reset()
	var rep Reply
	if err := wire.ReadJSON(s, &rep); err != nil || rep.Error != "" || rep.Net != "10.77.0.0/30" {
		t.Fatalf("reply %+v, %v", rep, err)
	}
	hostDev := fs.dev(t, 0)
	l := &link{s: s}
	l.d, _ = datagrams(client)
	send := func(p []byte, datagram bool) {
		t.Helper()
		if _, err := l.send(p, datagram); err != nil {
			t.Fatal(err)
		}
	}
	for _, datagram := range []bool{true, false} {
		send(packet("10.77.0.2", "192.168.1.50", 1, []byte("fine")), datagram)
		hostDev.expect(t, "granted packet")
		for what, p := range map[string][]byte{
			"spoofed source (host's own address)": packet("10.77.0.1", "192.168.1.50", 1, nil),
			"spoofed source (LAN machine)":        packet("192.168.1.7", "192.168.1.50", 1, nil),
			"ungranted destination":               packet("10.77.0.2", "8.8.8.8", 1, nil),
			"host loopback":                       packet("10.77.0.2", "127.0.0.1", 1, nil),
			"broadcast":                           packet("10.77.0.2", "255.255.255.255", 17, nil),
			"another tunnel":                      packet("10.77.0.2", "10.77.0.6", 1, nil),
			"truncated":                           packet("10.77.0.2", "192.168.1.50", 1, make([]byte, 30))[:30],
			"oversized":                           packet("10.77.0.2", "192.168.1.50", 1, make([]byte, 4000)),
		} {
			send(p, datagram && len(p) < 1000)
			hostDev.expectNone(t, what)
		}
	}
	// The host only ever sends the client packets addressed to it.
	hostDev.fromOS <- packet("192.168.1.50", "192.168.2.2", 1, nil)
	hostDev.fromOS <- packet("192.168.1.50", "10.77.0.2", 1, []byte("ok"))
	got := make(chan []byte, 1)
	go func() {
		if d, err := l.d.Receive(ctx); err == nil {
			got <- d
		}
	}()
	select {
	case p := <-got:
		if h, _ := parse(p); h.dst.String() != "10.77.0.2" {
			t.Fatalf("host sent the client a packet for %s", h.dst)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("nothing from the host")
	}
}

// Packets that do not fit a datagram, and sessions without datagrams, go
// over the stream.
func TestStreamFallback(t *testing.T) {
	reg := peer.NewRegistry()
	hostDev, clientDev := newFakeDev("h"), newFakeDev("c")
	open := policy{src: anyAddr, dst: anyAddr}
	var hostStats Stats
	reg.Handle("t", func(ctx context.Context, c *peer.Conn, s peer.Stream, _ json.RawMessage) error {
		d, _ := datagrams(c)
		return pump(ctx, hostDev, &link{d: d, s: s}, 9000, 1200, true, open, open, &hostStats)
	})
	client, _ := peertest.PairWith(t, reg)
	ctx := testCtx(t)
	s, err := client.Open(ctx, "t", nil)
	if err != nil {
		t.Fatal(err)
	}
	d, limit := datagrams(client)
	if limit < 1000 || limit > 1500 {
		t.Fatalf("datagram limit %d", limit)
	}
	var stats Stats
	go pump(ctx, clientDev, &link{d: d, s: s}, 9000, 1200, true, open, open, &stats)

	small := packet("10.77.0.2", "10.77.0.1", 17, make([]byte, 500))
	big := packet("10.77.0.2", "10.77.0.1", 17, make([]byte, 5000))
	for i := range big[20:] {
		big[20+i] = byte(i)
	}
	clientDev.fromOS <- small
	hostDev.expect(t, "small packet")
	if stats.ViaStream.Load() != 0 {
		t.Fatal("a small packet took the stream")
	}
	clientDev.fromOS <- big
	if got := hostDev.expect(t, "big packet"); string(got) != string(big) {
		t.Fatal("big packet damaged on the stream path")
	}
	if stats.ViaStream.Load() != 1 {
		t.Fatalf("%d packets took the stream, want 1", stats.ViaStream.Load())
	}
	// And the other way.
	hostDev.fromOS <- big
	clientDev.expect(t, "big packet from the host")
	// Order on the stream is kept, sizes are bounded by the framing.
	for i := 0; i < 20; i++ {
		p := packet("10.77.0.2", "10.77.0.1", 17, make([]byte, 3000))
		p[20] = byte(i)
		clientDev.fromOS <- p
	}
	for i := 0; i < 20; i++ {
		if got := hostDev.expect(t, "stream packet"); got[20] != byte(i) {
			t.Fatalf("stream packet %d arrived out of order", i)
		}
	}
}

func TestHostPolicy(t *testing.T) {
	t.Setenv("REMOHOP_CONFIG_DIR", t.TempDir())
	fs := &fakeSystem{}
	fs.install(t)
	lan := netip.MustParsePrefix("192.168.1.0/24")
	oldConnected := connected
	connected = func() []netip.Prefix { return []netip.Prefix{lan} }
	defer func() { connected = oldConnected }()
	far := netip.MustParsePrefix("172.20.0.0/16")
	ctx := testCtx(t)
	up := func(o Options) (*Tunnel, error) {
		client, _ := peertest.Pair(t)
		return Up(ctx, client, o)
	}

	// Beyond the host's own networks, and exit: only with vpn-exit on.
	if _, err := up(Options{Routes: []netip.Prefix{far}}); err == nil || !strings.Contains(err.Error(), "remohop config vpn-exit on") {
		t.Fatalf("non-local route with vpn-exit off: %v", err)
	}
	if _, err := up(Options{Exit: true}); err == nil || !strings.Contains(err.Error(), "exit for all traffic") {
		t.Fatalf("exit with vpn-exit off: %v", err)
	}
	if len(fs.devs) != 0 {
		t.Fatal("a refused request created an interface")
	}
	if err := config.Set(SettingExit, "on"); err != nil {
		t.Fatal(err)
	}
	tun, err := up(Options{Exit: true, Bypass: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}})
	if err != nil {
		t.Fatalf("exit with vpn-exit on: %v", err)
	}
	r := fs.routing[len(fs.routing)-1]
	if !r.exit || len(r.keep) != 1 || !r.keep[0].IsLoopback() || len(r.bypass) != 1 {
		t.Fatalf("exit routing %+v: the session's own peer address must be kept off the tunnel", r)
	}
	// Resolvers offered by the host are taken only if they are addresses.
	if len(r.dns) != 1 || r.dns[0].String() != "9.9.9.9" {
		t.Fatalf("dns %v", r.dns)
	}
	go tun.Run(ctx)

	// A second client at the same time gets the next network; a network
	// in use on either side is skipped, and the user is told.
	tun2, err := up(Options{})
	if err != nil {
		t.Fatal(err)
	}
	if tun2.Net.String() != "10.77.0.4/30" || len(tun2.Notes) != 1 || !strings.Contains(tun2.Notes[0], "10.77.0.4/30") {
		t.Fatalf("second tunnel: %s, notes %v", tun2.Net, tun2.Notes)
	}
	go tun2.Run(ctx)
	fs.used = []netip.Prefix{netip.MustParsePrefix("10.77.0.0/20")}
	tun3, err := up(Options{})
	if err != nil || tun3.Net.String() != "10.77.16.0/30" {
		t.Fatalf("with 10.77.0.0/20 in use: %v %v", tun3, err)
	}
	go tun3.Run(ctx)

	// No privilege: an exact, actionable refusal, on either side.
	fs.privErr = errPrivilege("a VPN tunnel")
	if _, err := up(Options{}); err == nil || !strings.Contains(err.Error(), "setcap cap_net_admin") || !strings.Contains(err.Error(), "sudo") {
		t.Fatalf("without privilege: %v", err)
	}
}

// A host that answers with something the client did not propose is not
// obeyed.
func TestClientRejectsUnofferedNetwork(t *testing.T) {
	t.Setenv("REMOHOP_CONFIG_DIR", t.TempDir())
	fs := &fakeSystem{}
	fs.install(t)
	for name, rep := range map[string]Reply{
		"network outside the pool": {Net: "192.168.2.0/30", MTU: tunnelMTU, MSS: 1200},
		"network not proposed":     {Net: "10.77.200.0/30", MTU: tunnelMTU, MSS: 1200},
		"absurd MTU":               {Net: "10.77.0.0/30", MTU: 65000, MSS: 1200},
		"absurd MSS":               {Net: "10.77.0.0/30", MTU: tunnelMTU, MSS: 9000},
		"escape in the error":      {Error: "no\x1b[2J way"},
	} {
		reg := peer.NewRegistry()
		reg.Handle(Service, func(_ context.Context, _ *peer.Conn, s peer.Stream, _ json.RawMessage) error {
			return wire.WriteJSON(s, rep)
		})
		client, _ := peertest.PairWith(t, reg)
		_, err := Up(testCtx(t), client, Options{})
		if err == nil || strings.ContainsRune(err.Error(), 0x1b) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if len(fs.devs) != 0 || len(fs.routing) != 0 {
		t.Fatal("the client touched the system on a bad reply")
	}
}

func TestStateUndoAndRecover(t *testing.T) {
	t.Setenv("REMOHOP_CONFIG_DIR", t.TempDir())
	(&fakeSystem{}).install(t)
	marker := filepath.Join(t.TempDir(), "marker")
	st, err := newState("client", "x0")
	if err != nil {
		t.Fatal(err)
	}
	// Steps a state file must never be able to trigger: other tools,
	// files outside /proc/sys/net.
	st.record(undoStep{Cmd: []string{"touch", marker}})
	st.record(undoStep{Sysctl: marker, Value: "1"})
	st.record(undoStep{Sysctl: "/proc/sys/net/../../../" + marker, Value: "1"})
	if private, err := config.IsPrivate(st.path); err != nil || !private {
		t.Fatalf("state file is not private to the user (%v)", err)
	}
	st.undo()
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("undo ran a step outside its allowlist")
	}
	if _, err := os.Stat(st.path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("state file left after undo")
	}

	// A file of a dead process is cleaned up; one of a live process, and
	// garbage, are handled without drama. Only where tunnels exist: elsewhere
	// no state is ever written and every process is treated as alive.
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return
	}
	dir, _ := stateDir()
	dead, _ := json.Marshal(state{PID: 1 << 30, Role: "host", Dev: "remohop9"})
	os.WriteFile(filepath.Join(dir, "host-dead.json"), dead, 0o600)
	live, _ := json.Marshal(state{PID: os.Getppid(), Role: "host", Dev: "remohop8"})
	os.WriteFile(filepath.Join(dir, "host-live.json"), live, 0o600)
	os.WriteFile(filepath.Join(dir, "junk.json"), []byte("{"), 0o600)
	cleaned, stale := Recover()
	if cleaned != 1 || stale != 0 {
		t.Fatalf("Recover: cleaned %d, stale %d", cleaned, stale)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 || ents[0].Name() != "host-live.json" {
		t.Fatalf("after Recover: %v", ents)
	}
}

// A peer that goes silent (killed outright, so it never closes anything)
// is noticed by the keep-alive, and the surviving end tears down; a peer
// that only sends keep-alives keeps the tunnel up.
func TestKeepAlive(t *testing.T) {
	oldKA, oldDead := keepAlive, deadAfter
	keepAlive, deadAfter = 50*time.Millisecond, 300*time.Millisecond
	defer func() { keepAlive, deadAfter = oldKA, oldDead }()

	silent := make(chan struct{})
	reg := peer.NewRegistry()
	reg.Handle("t", func(ctx context.Context, _ *peer.Conn, s peer.Stream, _ json.RawMessage) error {
		<-silent // reads nothing, writes nothing, closes nothing
		return nil
	})
	client, _ := peertest.PairWith(t, reg)
	ctx := testCtx(t)
	s, err := client.Open(ctx, "t", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer close(silent)
	open := policy{src: anyAddr, dst: anyAddr}
	dev := newFakeDev("c")
	var st Stats
	start := time.Now()
	err = pump(ctx, dev, &link{s: s}, tunnelMTU, 1200, false, open, open, &st)
	if err == nil || !strings.Contains(err.Error(), "stopped answering") {
		t.Fatalf("pump against a silent peer: %v", err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("took %s to notice", took)
	}
	select {
	case <-dev.closed:
	default:
		t.Fatal("device left open")
	}

	// Two live ends with no traffic at all stay up well past deadAfter.
	reg2 := peer.NewRegistry()
	hostDev := newFakeDev("h")
	hostDone := make(chan struct{})
	reg2.Handle("t", func(ctx context.Context, _ *peer.Conn, s peer.Stream, _ json.RawMessage) error {
		defer close(hostDone)
		return pump(ctx, hostDev, &link{s: s}, tunnelMTU, 1200, false, open, open, &Stats{})
	})
	client2, _ := peertest.PairWith(t, reg2)
	s2, err := client2.Open(ctx, "t", nil)
	if err != nil {
		t.Fatal(err)
	}
	pctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err := pump(pctx, newFakeDev("c2"), &link{s: s2}, tunnelMTU, 1200, false, open, open, &Stats{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("idle but live tunnel ended early: %v", err)
	}
	// The other end notices that this one has gone.
	select {
	case <-hostDone:
	case <-time.After(3 * time.Second):
		t.Fatal("the host end outlived the client end")
	}
}
