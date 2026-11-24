package signal

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/nat"
	"github.com/readori/RemoHop/internal/wsdgram"
)

// wsWorld is a rendezvous server on loopback UDP with its websocket bridge
// on an httptest server.
type wsWorld struct {
	t    *testing.T
	srv  *Server
	main string // UDP address
	url  string // ws:// URL of the bridge
}

func newWSWorld(t *testing.T, relay *RelayConfig, cfg WSConfig) *wsWorld {
	t.Helper()
	srv, err := NewServer(Config{DataDir: t.TempDir(), Relay: relay})
	if err != nil {
		t.Fatal(err)
	}
	main, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	alt, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, main, alt) }()
	for srv.mainPort.Load() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	ts := httptest.NewServer(srv.WSHandler(cfg))
	t.Cleanup(func() {
		ts.Close()
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Serve: %v", err)
		}
		srv.Close()
	})
	return &wsWorld{t: t, srv: srv, main: main.LocalAddr().String(), url: "ws" + strings.TrimPrefix(ts.URL, "http") + wsdgram.DefaultPath}
}

func (w *wsWorld) bridge(header http.Header) (*wsdgram.Conn, *nat.Socket) {
	w.t.Helper()
	wc, err := wsdgram.New(wsdgram.Options{URL: w.url, Header: header, Proxy: func(*url.URL) (*url.URL, error) { return nil, nil }})
	if err != nil {
		w.t.Fatal(err)
	}
	sock, err := nat.NewSocket(wc)
	if err != nil {
		w.t.Fatal(err)
	}
	w.t.Cleanup(func() { sock.Close() })
	return wc, sock
}

func (w *wsWorld) udpSocket() *nat.Socket {
	w.t.Helper()
	sock, err := nat.Listen("127.0.0.1:0")
	if err != nil {
		w.t.Fatal(err)
	}
	sock.Locals = func() []netip.Addr { return nil }
	w.t.Cleanup(func() { sock.Close() })
	return sock
}

func TestWSBridgeRendezvous(t *testing.T) {
	ctx := testCtx(t)
	w := newWSWorld(t, &RelayConfig{}, WSConfig{})

	// A host on the bridge registers like any other.
	wc, hsock := w.bridge(nil)
	hc, err := DialBridge(ctx, hsock, wc, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer hc.Close()
	if !hc.Bridged() || hc.NATType() != nat.TypeUnknown {
		t.Fatalf("bridged = %v, NAT = %v", hc.Bridged(), hc.NATType())
	}
	if !hc.ServerKey().Equal(w.srv.PublicKey()) {
		t.Fatal("server key differs over the bridge")
	}
	hostID := newIdentity(t)
	reg, err := hc.Register(ctx, hostID)
	if err != nil {
		t.Fatal(err)
	}
	if reg.Reflexive != bridgedPeerAddr {
		t.Fatalf("bridged host told it is at %v", reg.Reflexive)
	}
	hc.OnRelay = func([]byte, netip.AddrPort) error { return nil }
	offers := make(chan Offer, 1)
	go hc.ServeOffers(ctx, func(o Offer) Answer {
		offers <- o
		return Answer{Local: []netip.AddrPort{netip.MustParseAddrPort("192.168.1.5:4000")}}
	})

	// A UDP client is introduced and learns the host only via the relay.
	usock := w.udpSocket()
	uc, err := Dial(ctx, usock, w.main, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer uc.Close()
	if uc.Bridged() {
		t.Fatal("UDP client reported as bridged")
	}
	in, err := uc.Connect(ctx, reg.ID, []netip.AddrPort{netip.MustParseAddrPort("192.168.7.7:5000")}, nat.TypeCone)
	if err != nil {
		t.Fatal(err)
	}
	if !in.Peer.Bridged || len(in.Peer.Candidates()) != 0 || !in.RelayOffered {
		t.Fatalf("introduction to a bridged host: %+v", in)
	}
	if o := <-offers; o.Peer.Bridged {
		t.Fatal("UDP client offered as bridged")
	}

	// And the other way round: a bridged client to a UDP host.
	usock2 := w.udpSocket()
	uh, err := Dial(ctx, usock2, w.main, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer uh.Close()
	reg2, err := uh.Register(ctx, newIdentity(t))
	if err != nil {
		t.Fatal(err)
	}
	offers2 := make(chan Offer, 1)
	go uh.ServeOffers(ctx, func(o Offer) Answer { offers2 <- o; return Answer{} })
	wc3, csock := w.bridge(nil)
	cc, err := DialBridge(ctx, csock, wc3, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer cc.Close()
	in2, err := cc.Connect(ctx, reg2.ID, []netip.AddrPort{netip.MustParseAddrPort("127.0.0.1:22")}, nat.TypeUnknown)
	if err != nil {
		t.Fatal(err)
	}
	if in2.Peer.Bridged {
		t.Fatal("UDP host reported as bridged")
	}
	o := <-offers2
	// The bridged client's self-reported (loopback!) candidate is not
	// passed on: the host would probe its own machine.
	if !o.Peer.Bridged || len(o.Peer.Candidates()) != 0 || len(o.Peer.Local) != 0 || o.Peer.Reflexive != bridgedPeerAddr {
		t.Fatalf("offer from a bridged client: %+v", o.Peer)
	}
	// Presence works over the bridge.
	p, err := cc.Presence(ctx, []string{reg.ID, reg2.ID})
	if err != nil || p[reg.ID] != PresenceOnline || p[reg2.ID] != PresenceOnline {
		t.Fatalf("presence = %v, %v", p, err)
	}
}

func TestWSBridgeNeedsRelay(t *testing.T) {
	ctx := testCtx(t)
	w := newWSWorld(t, nil, WSConfig{})
	usock := w.udpSocket()
	uh, err := Dial(ctx, usock, w.main, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer uh.Close()
	reg, err := uh.Register(ctx, newIdentity(t))
	if err != nil {
		t.Fatal(err)
	}
	go uh.ServeOffers(ctx, func(Offer) Answer { return Answer{} })
	wc, csock := w.bridge(nil)
	cc, err := DialBridge(ctx, csock, wc, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer cc.Close()
	if _, err := cc.Connect(ctx, reg.ID, nil, nat.TypeUnknown); CodeOf(err) != CodeNoRelay {
		t.Fatalf("connect from a bridged client without relay: %v", err)
	}
}

// TestWSBridgeIsNoOpenProxy checks that datagrams go only to the server's
// own ports, and only the server's own ports reach a bridged peer.
func TestWSBridgeIsNoOpenProxy(t *testing.T) {
	ctx := testCtx(t)
	w := newWSWorld(t, &RelayConfig{}, WSConfig{})
	victim, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer victim.Close()
	vport := victim.LocalAddr().(*net.UDPAddr).Port

	wc, _ := w.bridge(nil)
	if err := wc.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		wc.WriteTo([]byte("attack"), &net.UDPAddr{IP: wsdgram.ServerIP.AsSlice(), Port: vport})
	}
	victim.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if n, from, err := victim.ReadFrom(make([]byte, 100)); err == nil {
		t.Fatalf("the bridge forwarded %d bytes to an unrelated port, from %v", n, from)
	}

	// A stranger writing to the bridge socket is not passed on.
	var bridgeAddr netip.AddrPort
	w.srv.ws.bridges.Range(func(k, _ any) bool { bridgeAddr = k.(netip.AddrPort); return false })
	if !bridgeAddr.IsValid() {
		t.Fatal("no bridge registered")
	}
	victim.WriteToUDPAddrPort([]byte("spoof"), bridgeAddr)
	wc.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if _, from, err := wc.ReadFrom(make([]byte, 100)); err == nil {
		t.Fatalf("the bridge passed on a datagram from %v", from)
	}

	// Relay ports are allowed only while their session exists.
	if w.srv.allowedPort(40000) {
		t.Fatal("unopened relay port allowed")
	}
	w.srv.ws.relayPorts.Store(uint16(40000), struct{}{})
	if !w.srv.allowedPort(40000) {
		t.Fatal("open relay port refused")
	}
	w.srv.ws.relayPorts.Delete(uint16(40000))
}

func TestWSBridgeAdmission(t *testing.T) {
	ctx := testCtx(t)
	w := newWSWorld(t, &RelayConfig{}, WSConfig{MaxConnsPerSource: 1, RealIPHeader: "CF-Connecting-IP"})
	httpURL := "http" + strings.TrimPrefix(w.url, "ws")

	// Plain requests and browsers are turned away.
	resp, err := http.Get(httpURL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("plain GET: %d", resp.StatusCode)
	}
	resp, _ = http.Get(strings.TrimSuffix(httpURL, wsdgram.DefaultPath) + "/other")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("other path: %d", resp.StatusCode)
	}
	req, _ := http.NewRequest("GET", httpURL, nil)
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("CF-Connecting-IP", "203.0.113.9")
	req.Header.Set("Origin", "https://evil.example")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("browser origin: %d", resp.StatusCode)
	}

	// Without the configured client-address header: refused.
	wc, _ := w.bridge(nil)
	if err := wc.Connect(ctx); err == nil {
		t.Fatal("connection without CF-Connecting-IP accepted")
	}

	// With it: the bridge counts the client as that address, one
	// connection per address here.
	wc1, sock1 := w.bridge(http.Header{"Cf-Connecting-Ip": {"203.0.113.7"}})
	c1, err := DialBridge(ctx, sock1, wc1, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer c1.Close()
	var client netip.Addr
	w.srv.ws.bridges.Range(func(_, v any) bool { client = v.(netip.Addr); return false })
	if client != netip.MustParseAddr("203.0.113.7") {
		t.Fatalf("bridge client = %v", client)
	}
	wc2, _ := w.bridge(http.Header{"Cf-Connecting-Ip": {"203.0.113.7"}})
	err = wc2.Connect(ctx)
	var se *wsdgram.StatusError
	if !errors.As(err, &se) || se.Code != http.StatusServiceUnavailable {
		t.Fatalf("second connection from one address: %v", err)
	}
	wc3, _ := w.bridge(http.Header{"Cf-Connecting-Ip": {"203.0.113.8"}})
	if err := wc3.Connect(ctx); err != nil {
		t.Fatalf("connection from another address: %v", err)
	}
}

func TestWSBridgeIdle(t *testing.T) {
	ctx := testCtx(t)
	w := newWSWorld(t, nil, WSConfig{Idle: 300 * time.Millisecond})
	wc, _ := w.bridge(nil)
	if err := wc.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	for start := time.Now(); wc.Connected(); time.Sleep(20 * time.Millisecond) {
		if time.Since(start) > 5*time.Second {
			t.Fatal("idle bridge not closed")
		}
	}
}

func TestClientAddr(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "[::ffff:192.0.2.10]:5555"
	if a, err := clientAddr(r, ""); err != nil || a != netip.MustParseAddr("192.0.2.10") {
		t.Fatalf("clientAddr = %v, %v", a, err)
	}
	r.Header.Set("X-Forwarded-For", "10.0.0.1, 198.51.100.4")
	if a, err := clientAddr(r, "X-Forwarded-For"); err != nil || a != netip.MustParseAddr("198.51.100.4") {
		t.Fatalf("clientAddr(XFF) = %v, %v", a, err)
	}
	r.Header.Set("X-Forwarded-For", "garbage")
	if _, err := clientAddr(r, "X-Forwarded-For"); err == nil {
		t.Fatal("malformed header accepted")
	}
}

func TestWSFlagsAndTarget(t *testing.T) {
	for _, bad := range [][]string{
		{"", "/p", "", "", ""}, {"nohostport", "", "", "", ""}, {"127.0.0.1:1", "rel", "", "", ""},
		{"127.0.0.1:1", "", "cert", "", ""}, {"127.0.0.1:1", "", "", "", "Bad Header"},
	} {
		if err := checkWSFlags(bad[0], bad[1], bad[2], bad[3], bad[4], 0); err == nil {
			t.Errorf("checkWSFlags(%q) accepted", bad)
		}
	}
	if err := checkWSFlags("127.0.0.1:8080", "/remohop/v1/udp", "", "", "CF-Connecting-IP", 10); err != nil {
		t.Fatal(err)
	}
	if got := bridgeTarget(&net.UDPAddr{IP: net.IPv6zero, Port: 1}); got != netip.MustParseAddr("127.0.0.1") {
		t.Fatalf("target for [::] = %v", got)
	}
	if got := bridgeTarget(&net.UDPAddr{IP: net.IPv4(10, 0, 0, 5), Port: 1}); got != netip.MustParseAddr("10.0.0.5") {
		t.Fatalf("target for 10.0.0.5 = %v", got)
	}
}

func TestResolveBridgeSettings(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("REMOHOP_CONFIG_DIR", dir)
	t.Setenv("REMOHOP_TRANSPORT", "")
	t.Setenv("REMOHOP_SIGNAL_WS", "")
	t.Setenv("REMOHOP_SIGNAL_WS_ACCESS_ID", "")
	t.Setenv("REMOHOP_SIGNAL_WS_ACCESS_SECRET", "")
	write := func(s string) {
		t.Helper()
		if err := writeTestConfig(dir, s); err != nil {
			t.Fatal(err)
		}
	}

	write(`{"signal":"sig.example.net","signal-ws":"wss://relay.example.net"}`)
	addr, o, err := Resolve("")
	if err != nil || addr != "sig.example.net" || o.WS != "wss://relay.example.net" || o.Transport != TransportAuto || !o.UseUDP() || !o.UseWS() {
		t.Fatalf("Resolve = %q %+v %v", addr, o, err)
	}
	if o.UDPWait(10*time.Second) != FallbackAfter {
		t.Fatal("no fallback wait")
	}
	// Another server named on the command line does not get this one's
	// bridge.
	if _, o, _ := Resolve("other:1"); o.WS != "" {
		t.Fatalf("bridge carried over to --signal: %+v", o)
	}
	t.Setenv("REMOHOP_TRANSPORT", "ws")
	if _, o, _ := Resolve(""); o.UseUDP() || !o.UseWS() {
		t.Fatalf("REMOHOP_TRANSPORT=ws: %+v", o)
	}
	t.Setenv("REMOHOP_TRANSPORT", "udp")
	if _, o, _ := Resolve(""); !o.UseUDP() || o.UseWS() {
		t.Fatalf("REMOHOP_TRANSPORT=udp: %+v", o)
	}
	t.Setenv("REMOHOP_TRANSPORT", "carrier-pigeon")
	if _, _, err := Resolve(""); err == nil {
		t.Fatal("bad REMOHOP_TRANSPORT accepted")
	}
	t.Setenv("REMOHOP_TRANSPORT", "")

	// A bridge alone is enough.
	write(`{"signal-ws":"wss://relay.example.net/x"}`)
	addr, o, err = Resolve("")
	if err != nil || addr != "" || o.Transport != TransportWS || Display(addr, o) != "wss://relay.example.net/x" {
		t.Fatalf("bridge-only Resolve = %q %+v %v", addr, o, err)
	}

	// Access tokens: the secret from a file or the environment.
	secret := dir + "/secret"
	if err := writeFile(secret, "s3cr3t\n"); err != nil {
		t.Fatal(err)
	}
	write(`{"signal":"sig.example.net","signal-ws":"wss://relay.example.net","signal-ws-access-id":"abc.access","signal-ws-access-secret-file":` + strconv.Quote(secret) + `}`)
	if _, o, err := Resolve(""); err != nil || http.Header(o.WSHeader).Get("Cf-Access-Client-Secret") != "s3cr3t" || http.Header(o.WSHeader).Get("Cf-Access-Client-Id") != "abc.access" {
		t.Fatalf("access headers: %+v %v", o.WSHeader, err)
	}
	t.Setenv("REMOHOP_SIGNAL_WS_ACCESS_SECRET", "from-env")
	if _, o, _ := Resolve(""); http.Header(o.WSHeader).Get("Cf-Access-Client-Secret") != "from-env" {
		t.Fatal("environment secret not preferred")
	}
	t.Setenv("REMOHOP_SIGNAL_WS_ACCESS_SECRET", "")
	write(`{"signal":"sig.example.net","signal-ws":"wss://relay.example.net","signal-ws-access-id":"abc.access"}`)
	if _, _, err := Resolve(""); err == nil || !strings.Contains(err.Error(), "secret") {
		t.Fatalf("ID without secret: %v", err)
	}
	write(`{"signal":"sig.example.net"}`)
	t.Setenv("REMOHOP_TRANSPORT", "ws")
	if _, _, err := Resolve(""); err == nil || !strings.Contains(err.Error(), "signal-ws") {
		t.Fatalf("ws without a bridge: %v", err)
	}
}

func TestConnectFallsBackToBridge(t *testing.T) {
	ctx := testCtx(t)
	w := newWSWorld(t, &RelayConfig{}, WSConfig{})
	old := FallbackAfter
	FallbackAfter = 500 * time.Millisecond
	defer func() { FallbackAfter = old }()

	blocked := func() (*nat.Socket, error) { return nat.NewSocket(newBlackhole()) }
	start := time.Now()
	c, sock, err := Connect(ctx, w.main, Options{WS: w.url}, blocked, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sock.Close()
	defer c.Close()
	if !c.Bridged() || time.Since(start) > 5*time.Second {
		t.Fatalf("bridged = %v after %v", c.Bridged(), time.Since(start))
	}
	// Forced UDP does not fall back.
	if _, _, err := Connect(ctx, w.main, Options{WS: w.url, Transport: TransportUDP}, func() (*nat.Socket, error) {
		return nat.NewSocket(newBlackhole())
	}, nil); err == nil {
		t.Fatal("UDP-only connect through a black hole succeeded")
	}
}

// blackhole is a UDP socket in a network that drops all UDP.
type blackhole struct {
	once   sync.Once
	closed chan struct{}
	wake   chan struct{}
}

func newBlackhole() *blackhole {
	return &blackhole{closed: make(chan struct{}), wake: make(chan struct{}, 1)}
}

func (b *blackhole) ReadFrom([]byte) (int, net.Addr, error) {
	select {
	case <-b.closed:
		return 0, nil, net.ErrClosed
	case <-b.wake:
		return 0, nil, os.ErrDeadlineExceeded
	}
}
func (b *blackhole) WriteTo(p []byte, _ net.Addr) (int, error) { return len(p), nil }
func (b *blackhole) Close() error {
	b.once.Do(func() { close(b.closed) })
	return nil
}
func (b *blackhole) LocalAddr() net.Addr           { return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9} }
func (b *blackhole) SetDeadline(t time.Time) error { return b.SetReadDeadline(t) }
func (b *blackhole) SetReadDeadline(t time.Time) error {
	if !t.IsZero() && !t.After(time.Now()) {
		select {
		case b.wake <- struct{}{}:
		default:
		}
	}
	return nil
}
func (b *blackhole) SetWriteDeadline(time.Time) error { return nil }

func writeTestConfig(dir, json string) error {
	return writeFile(filepath.Join(dir, "config.json"), json)
}

func writeFile(path, content string) error { return os.WriteFile(path, []byte(content), 0o600) }

func TestFirewallWithBridge(t *testing.T) {
	plan, err := firewallPlanFor(":21116", "", true, PortRange{40000, 40099}, "0.0.0.0:8443")
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	printFirewall(&b, plan, "debian")
	if !strings.Contains(b.String(), "sudo iptables -I INPUT -p tcp --dport 8443 -j ACCEPT") || !strings.Contains(b.String(), "8443/tcp") {
		t.Fatalf("no TCP rule for a public bridge:\n%s", b.String())
	}
	b.Reset()
	printFirewall(&b, plan, "rhel")
	if !strings.Contains(b.String(), "--add-port=8443/tcp") {
		t.Fatalf("no firewalld TCP rule:\n%s", b.String())
	}
	tunnel, _ := firewallPlanFor(":21116", "", true, PortRange{40000, 40099}, "127.0.0.1:8080")
	b.Reset()
	printFirewall(&b, tunnel, "debian")
	if strings.Contains(b.String(), "tcp") && !strings.Contains(b.String(), "no port to open") {
		t.Fatalf("a loopback bridge got a TCP rule:\n%s", b.String())
	}
	if _, err := firewallPlanFor(":21116", "", true, PortRange{}, "127.0.0.1:0"); err == nil {
		t.Fatal("ephemeral --ws-listen accepted")
	}
}

// TestSignalCommandWS starts `remohop signal --ws-listen` for real and talks to
// the bridge it prints.
func TestSignalCommandWS(t *testing.T) {
	t.Setenv("REMOHOP_CONFIG_DIR", t.TempDir())
	ctx := context.Background()
	if code, _, errOut := runSignalCmd(t, ctx, "--ws-path", "/x"); code != cli.ExitUsage || !strings.Contains(errOut, "--ws-listen") {
		t.Errorf("--ws-path without --ws-listen: %d %q", code, errOut)
	}
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	out := &syncBuffer{}
	done := make(chan int, 1)
	go func() {
		env := &cli.Env{Stdout: out, Stderr: out, Stdin: strings.NewReader("")}
		done <- runSignal(sctx, env, []string{"--listen", "127.0.0.1:0", "--alt", "off", "--data", t.TempDir(), "--relay", "--ws-listen", "127.0.0.1:0"})
	}()
	var bridge string
	for start := time.Now(); bridge == ""; time.Sleep(20 * time.Millisecond) {
		if time.Since(start) > 10*time.Second {
			t.Fatalf("no bridge line:\n%s", out.String())
		}
		for _, line := range strings.Split(out.String(), "\n") {
			if rest, ok := strings.CutPrefix(line, "websocket bridge: http://"); ok {
				bridge = "ws://" + strings.Fields(rest)[0]
			}
		}
	}
	if !strings.Contains(out.String(), "--ws-real-ip-header") {
		t.Errorf("no warning about the client address behind a proxy:\n%s", out.String())
	}
	wc, err := wsdgram.New(wsdgram.Options{URL: bridge, Proxy: func(*url.URL) (*url.URL, error) { return nil, nil }})
	if err != nil {
		t.Fatal(err)
	}
	sock, err := nat.NewSocket(wc)
	if err != nil {
		t.Fatal(err)
	}
	defer sock.Close()
	c, err := DialBridge(testCtx(t), sock, wc, Options{})
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	cancel()
	if code := <-done; code != cli.ExitOK {
		t.Fatalf("exit %d:\n%s", code, out.String())
	}
}

func TestAccessTokenNeedsTLS(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("REMOHOP_CONFIG_DIR", dir)
	t.Setenv("REMOHOP_TRANSPORT", "")
	t.Setenv("REMOHOP_SIGNAL_WS", "")
	t.Setenv("REMOHOP_SIGNAL_WS_ACCESS_ID", "")
	t.Setenv("REMOHOP_SIGNAL_WS_ACCESS_SECRET", "s")
	if err := writeTestConfig(dir, `{"signal":"x.example","signal-ws":"ws://relay.example.net","signal-ws-access-id":"a.access"}`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Resolve(""); err == nil || !strings.Contains(err.Error(), "wss://") {
		t.Fatalf("token over ws://: %v", err)
	}
}
