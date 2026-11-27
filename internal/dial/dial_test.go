package dial

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/config"
	"github.com/readori/RemoHop/internal/hostd"
	"github.com/readori/RemoHop/internal/nat"
	"github.com/readori/RemoHop/internal/nat/vnet"
	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/perm"
	"github.com/readori/RemoHop/internal/signal"
)

const echoService = "dialtest-echo"

func init() {
	perm.Register(echoService, perm.Shell)
	// A trivial service to prove sessions carry streams end to end.
	peer.Handle(echoService, func(_ context.Context, _ *peer.Conn, s peer.Stream, _ json.RawMessage) error {
		_, err := io.Copy(s, s)
		return err
	})
}

func noLocals() []netip.Addr { return nil }

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// startSignal runs a rendezvous server on the given sockets.
func startSignal(t *testing.T, main, alt net.PacketConn) *signal.Server {
	t.Helper()
	srv, err := signal.NewServer(signal.Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, main, alt) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("rendezvous server: %v", err)
		}
		srv.Close()
	})
	return srv
}

// loopbackSignal runs a rendezvous server on real loopback UDP sockets and
// returns its address.
func loopbackSignal(t *testing.T) string {
	t.Helper()
	main, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	alt, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	startSignal(t, main, alt)
	return main.LocalAddr().String()
}

// startHost runs a host daemon and waits until it is ready to be dialled.
func startHost(t *testing.T, cfg hostd.Config) *hostd.Host {
	t.Helper()
	if cfg.Dir == "" {
		cfg.Dir = t.TempDir()
	}
	if cfg.PacketConn == nil && cfg.Listen == "" {
		cfg.Listen = "127.0.0.1:0"
	}
	if cfg.Locals == nil {
		cfg.Locals = noLocals
	}
	h, err := hostd.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("host: %v", err)
		}
	})
	if cfg.Signal != "" {
		select {
		case <-h.Registered():
		case <-time.After(10 * time.Second):
			t.Fatal("host did not register")
		}
	}
	return h
}

// echo opens the test service on conn and checks a round trip.
func echo(t *testing.T, conn *peer.Conn) {
	t.Helper()
	ctx := testCtx(t)
	s, err := conn.Open(ctx, echoService, nil)
	if err != nil {
		t.Fatalf("open echo service: %v", err)
	}
	defer s.Close()
	msg := []byte("hello through the session")
	if _, err := s.Write(msg); err != nil {
		t.Fatal(err)
	}
	s.CloseWrite()
	got, err := io.ReadAll(s)
	if err != nil || !bytes.Equal(got, msg) {
		t.Fatalf("echo = %q, %v", got, err)
	}
}

// TestEndToEndLoopback is the whole product on loopback UDP: a real
// rendezvous server, a real host daemon and the dialler. Dial by ID with the
// temporary password, use a service, pair, reconnect with the token alone.
func TestEndToEndLoopback(t *testing.T) {
	sig := loopbackSignal(t)
	host := startHost(t, hostd.Config{Signal: sig})
	clientDir := t.TempDir()
	ctx := testCtx(t)

	id := host.ID()
	if !cli.IsID(id) {
		t.Fatalf("host ID %q", id)
	}
	opts := Options{Signal: sig, Dir: clientDir, Locals: noLocals}

	// 1. No credential at all: a clear error, and no prompt is invented.
	_, err := Dial(ctx, id, opts)
	if !errors.Is(err, ErrNoCredential) || !strings.Contains(err.Error(), "REMOHOP_PASSWORD") {
		t.Fatalf("dial without credentials: %v", err)
	}

	// 2. Password login, with the ID and password as a person would type them.
	withPW := opts
	withPW.Password = auth.FormatPassword(host.Password())
	conn, err := Dial(ctx, signal.FormatID(id), withPW)
	if err != nil {
		t.Fatalf("dial with password: %v", err)
	}
	info := InfoOf(conn)
	if info == nil || info.Target != id || info.Auth != auth.KindPassword || !info.NewHost {
		t.Fatalf("unexpected session info %+v", info)
	}
	if !info.HostKey.Equal(host.PublicKey()) {
		t.Fatal("session reports a different host key than the host's")
	}
	if info.Path != PathLAN || !info.Remote.Addr().IsLoopback() {
		t.Errorf("path %s to %s, want lan over loopback", info.Path, info.Remote)
	}
	if conn.RemoteID() != id || conn.Role() != peer.RoleClient {
		t.Errorf("conn RemoteID=%q role=%v", conn.RemoteID(), conn.Role())
	}
	echo(t, conn)

	res, err := hostd.Ping(ctx, conn, hostd.PingOptions{Count: 3, Interval: time.Millisecond})
	if err != nil || len(res.RTTs) != 3 {
		t.Fatalf("ping: %+v, %v", res, err)
	}
	if res.Info.Observed != info.Local.String() && !strings.HasSuffix(res.Info.Observed, ":"+portOf(info.Local)) {
		t.Errorf("host observed us as %s, we are %s", res.Info.Observed, info.Local)
	}

	// 3. Pair over the password session.
	pairing, err := hostd.Pair(ctx, conn, hostd.PairRequest{Name: "test-laptop"})
	if err != nil {
		t.Fatalf("pair: %v", err)
	}
	if err := SavePairing(info, pairing, "office", clientDir); err != nil {
		t.Fatal(err)
	}
	conn.Close()
	toks, _ := host.Tokens().List()
	if len(toks) != 1 || toks[0].Name != "test-laptop" {
		t.Fatalf("host tokens = %+v", toks)
	}
	if runtime.GOOS != "windows" {
		st, err := os.Stat(filepath.Join(clientDir, PeersFile))
		if err != nil || st.Mode().Perm() != 0o600 {
			t.Fatalf("peers file: %v, mode %v", err, st.Mode().Perm())
		}
	}

	// On every OS: only this user can read the client's secrets.
	for _, name := range []string{PeersFile, auth.IdentityFile} {
		if private, err := config.IsPrivate(filepath.Join(clientDir, name)); err != nil || !private {
			t.Fatalf("%s is not private to the user (%v)", name, err)
		}
	}

	// 4. Reconnect with no password, by alias: the token is used.
	conn, err = Dial(ctx, "office", opts)
	if err != nil {
		t.Fatalf("dial with saved pairing: %v", err)
	}
	if info := InfoOf(conn); info.Auth != auth.KindToken || info.NewHost || info.Target != id {
		t.Fatalf("unexpected session info %+v", info)
	}
	echo(t, conn)
	sessions := host.Sessions()
	if len(sessions) != 1 || sessions[0].Auth.TokenName != "test-laptop" {
		t.Fatalf("host sessions = %+v", sessions)
	}

	// 5. Revoking the pairing on the host ends the session and the token.
	if n, err := host.Tokens().Revoke("test-laptop"); err != nil || n != 1 {
		t.Fatalf("revoke: %d, %v", n, err)
	}
	select {
	case <-conn.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("session survived the revocation of its pairing")
	}
	_, err = Dial(ctx, id, opts)
	if !errors.Is(err, ErrNoCredential) || !strings.Contains(err.Error(), "no longer works") {
		t.Fatalf("dial with revoked pairing: %v", err)
	}
	// The dead token is gone from the client's store; the pin stays.
	if p, _, _ := mustStore(t, clientDir).Get(id); p.Paired() || p.HostKey == "" || p.Alias != "office" {
		t.Fatalf("saved peer after revocation: %+v", p)
	}
	// ... and with a prompt available, the user is asked instead.
	prompted := 0
	withPrompt := opts
	withPrompt.Prompt = func(_ context.Context, req PromptRequest) (string, error) {
		prompted++
		if req.Target != signal.FormatID(id) {
			t.Errorf("prompt target %q", req.Target)
		}
		if prompted == 1 {
			return "wrong-password", nil
		}
		if req.Reason != "wrong password" {
			t.Errorf("second prompt reason %q", req.Reason)
		}
		return host.Password(), nil
	}
	conn, err = Dial(ctx, id, withPrompt)
	if err != nil || prompted != 2 {
		t.Fatalf("dial with prompt: %v after %d prompts", err, prompted)
	}
	echo(t, conn)
	conn.Close()
}

func mustStore(t *testing.T, dir string) *Store {
	t.Helper()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func portOf(ap netip.AddrPort) string {
	_, port, _ := net.SplitHostPort(ap.String())
	return port
}

func TestDialErrors(t *testing.T) {
	sig := loopbackSignal(t)
	host := startHost(t, hostd.Config{Signal: sig, Password: "a fixed password"})
	ctx := testCtx(t)
	id := host.ID()
	base := Options{Signal: sig, Locals: noLocals}
	dialWith := func(target, pw string) error {
		o := base
		o.Dir, o.Password = t.TempDir(), pw
		c, err := Dial(ctx, target, o)
		if err == nil {
			c.Close()
		}
		return err
	}

	t.Run("unknown ID", func(t *testing.T) {
		other := "100000000"
		if other == id {
			other = "100000001"
		}
		err := dialWith(other, "whatever-pw")
		if signal.CodeOf(err) != signal.CodeUnknownID || !strings.Contains(err.Error(), "100 000 00") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("bad target", func(t *testing.T) {
		err := dialWith("no-such-alias", "whatever-pw")
		if err == nil || !strings.Contains(err.Error(), "9-digit ID") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("no rendezvous server configured", func(t *testing.T) {
		t.Setenv("REMOHOP_CONFIG_DIR", t.TempDir())
		o := Options{Dir: t.TempDir(), Password: "whatever-pw", Locals: noLocals}
		_, err := Dial(ctx, id, o)
		if !errors.Is(err, signal.ErrNotConfigured) || !strings.Contains(err.Error(), "remohop signal") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("rendezvous server unreachable", func(t *testing.T) {
		dead, _ := net.ListenPacket("udp", "127.0.0.1:0")
		addr := dead.LocalAddr().String()
		dead.Close()
		o := base
		o.Signal, o.Dir, o.Password = addr, t.TempDir(), "whatever-pw"
		start := time.Now()
		_, err := Dial(ctx, id, o)
		if err == nil || !strings.Contains(err.Error(), "unreachable") {
			t.Fatalf("err = %v", err)
		}
		if time.Since(start) > 12*time.Second {
			t.Fatalf("took %s", time.Since(start))
		}
	})
	t.Run("direct address with nothing there", func(t *testing.T) {
		dead, _ := net.ListenPacket("udp", "127.0.0.1:0")
		addr := dead.LocalAddr().String()
		dead.Close()
		defer func(d time.Duration) { directTimeout = d }(directTimeout)
		directTimeout = 1500 * time.Millisecond
		start := time.Now()
		err := dialWith(addr, "whatever-pw")
		if err == nil || !strings.Contains(err.Error(), "no remohop host answered") {
			t.Fatalf("err = %v", err)
		}
		if time.Since(start) > 5*time.Second {
			t.Fatalf("took %s", time.Since(start))
		}
	})
	t.Run("wrong password then lockout", func(t *testing.T) {
		for i := 0; i < 3; i++ {
			err := dialWith(id, "not the password")
			if auth.CodeOf(err) != auth.CodeAuthFailed || !strings.Contains(err.Error(), "wrong password") {
				t.Fatalf("attempt %d: %v", i+1, err)
			}
		}
		// Now even the right password is refused for a while.
		err := dialWith(id, "a fixed password")
		if auth.CodeOf(err) != auth.CodeLockedOut || !strings.Contains(err.Error(), "retry in") {
			t.Fatalf("after three failures: %v", err)
		}
		if n := len(host.Sessions()); n != 0 {
			t.Fatalf("%d sessions open after failed logins", n)
		}
	})
}

func TestDirectDialAndHostKeyPin(t *testing.T) {
	ctx := testCtx(t)
	// No rendezvous server anywhere in this test.
	hostDir := t.TempDir()
	hctx, stopHost := context.WithCancel(context.Background())
	h1, err := hostd.New(hostd.Config{Dir: hostDir, Listen: "127.0.0.1:0", Password: "direct-password", Locals: noLocals})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- h1.Run(hctx) }()
	addr := h1.Addr().String()

	clientDir := t.TempDir()
	opts := Options{Dir: clientDir, Password: "direct-password", Locals: noLocals}
	conn, err := Dial(ctx, addr, opts)
	if err != nil {
		t.Fatalf("direct dial: %v", err)
	}
	if info := InfoOf(conn); info.Path != PathDirect || info.Target != addr || !info.NewHost {
		t.Fatalf("unexpected session info %+v", info)
	}
	echo(t, conn)

	// Host shutdown ends the session cleanly.
	stopHost()
	if err := <-done; err != nil {
		t.Fatalf("host shutdown: %v", err)
	}
	select {
	case <-conn.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("session survived host shutdown")
	}

	// A different machine now answers at the same address, knowing the
	// same password. The pinned key gives it away before any login.
	pc, err := net.ListenPacket("udp", addr)
	if err != nil {
		t.Skipf("cannot rebind %s: %v", addr, err)
	}
	startHost(t, hostd.Config{PacketConn: pc, Password: "direct-password"})
	_, err = Dial(ctx, addr, opts)
	if !errors.Is(err, ErrHostKeyChanged) || !strings.Contains(err.Error(), "remohop peers forget") {
		t.Fatalf("dial to impostor: %v", err)
	}
	// After an explicit forget the new host is accepted.
	if n, err := mustStore(t, clientDir).Forget(addr); err != nil || n != 1 {
		t.Fatalf("forget: %d, %v", n, err)
	}
	conn, err = Dial(ctx, addr, opts)
	if err != nil {
		t.Fatalf("dial after forget: %v", err)
	}
	conn.Close()
}

// TestManInTheMiddle puts an active attacker between client and host: it
// terminates QUIC on both sides with its own identity and relays the login
// stream byte for byte. The client knows the right password, and the login
// must still fail on both ends, because each leg has its own TLS exporter.
func TestManInTheMiddle(t *testing.T) {
	ctx := testCtx(t)
	host := startHost(t, hostd.Config{Password: "the-real-password"})
	hostAddr := host.Addr().(*net.UDPAddr)

	attacker, err := auth.NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	serverTLS, _ := attacker.ServerTLS(peer.ALPN, true)
	clientTLS, _ := attacker.ClientTLS(peer.ALPN, nil)
	ln, err := quic.ListenAddr("127.0.0.1:0", serverTLS, peer.QUICConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	relayed := make(chan int64, 2)
	go func() {
		victim, err := ln.Accept(ctx)
		if err != nil {
			return
		}
		upstream, err := quic.DialAddr(ctx, hostAddr.String(), clientTLS, peer.QUICConfig())
		if err != nil {
			return
		}
		vs, err := victim.AcceptStream(ctx)
		if err != nil {
			return
		}
		us, err := upstream.OpenStreamSync(ctx)
		if err != nil {
			return
		}
		var wg sync.WaitGroup
		wg.Go(func() { n, _ := io.Copy(us, vs); us.Close(); relayed <- n })
		wg.Go(func() { n, _ := io.Copy(vs, us); vs.Close(); relayed <- n })
		wg.Wait()
		victim.CloseWithError(0, "")
		upstream.CloseWithError(0, "")
	}()

	_, err = Dial(ctx, ln.Addr().String(), Options{Dir: t.TempDir(), Password: "the-real-password", Locals: noLocals})
	if auth.CodeOf(err) != auth.CodeAuthFailed {
		t.Fatalf("dial through the attacker: %v, want auth_failed", err)
	}
	// The relay did carry the handshake in both directions: it was not a
	// connectivity failure.
	for i := 0; i < 2; i++ {
		select {
		case n := <-relayed:
			if n == 0 {
				t.Fatal("attacker relayed nothing in one direction")
			}
		case <-time.After(20 * time.Second):
			t.Fatal("relay did not finish")
		}
	}
	if n := len(host.Sessions()); n != 0 {
		t.Fatalf("host opened %d session(s) for the attacker", n)
	}
	// The host counted the attempt against the attacker's address: two
	// more and that address is locked out, although every attempt carried
	// messages made with the right password.
	hb := func() error {
		q, err := quic.DialAddr(ctx, hostAddr.String(), clientTLS, peer.QUICConfig())
		if err != nil {
			return err
		}
		defer q.CloseWithError(0, "")
		st, err := q.OpenStreamSync(ctx)
		if err != nil {
			return err
		}
		b, err := auth.ClientBinding(q.ConnectionState().TLS, attacker.Public())
		if err != nil {
			return err
		}
		err = auth.ClientHandshake(st, b, auth.PasswordCredential("a wrong guess"), "")
		// A refusal reaches the client on the stream or in the close.
		if e := hostd.LoginRefusal(err); e != nil {
			return e
		}
		return err
	}
	if err := hb(); auth.CodeOf(err) != auth.CodeAuthFailed {
		t.Fatalf("second attempt: %v", err)
	}
	if err := hb(); auth.CodeOf(err) != auth.CodeAuthFailed {
		t.Fatalf("third attempt: %v", err)
	}
	if err := hb(); auth.CodeOf(err) != auth.CodeLockedOut {
		t.Fatalf("fourth attempt: %v, want locked_out (the relayed login must have been counted)", err)
	}
}

// vnetWorld is a virtual internet with a rendezvous server.
type vnetWorld struct {
	t   *testing.T
	net *vnet.Net
}

const vnetSignal = "198.51.100.1:21116"

func newVnetWorld(t *testing.T) *vnetWorld {
	t.Helper()
	w := &vnetWorld{t: t, net: vnet.New()}
	srv := w.net.Host("198.51.100.1")
	startSignal(t, srv.MustListen(21116), srv.MustListen(21117))
	return w
}

func (w *vnetWorld) host(h *vnet.Host) *hostd.Host {
	return startHost(w.t, hostd.Config{
		Signal:     vnetSignal,
		PacketConn: h.MustListen(0),
		Locals:     func() []netip.Addr { return []netip.Addr{h.Addr()} },
	})
}

func (w *vnetWorld) options(h *vnet.Host, password string) Options {
	return Options{
		Signal:     vnetSignal,
		Password:   password,
		Dir:        w.t.TempDir(),
		PacketConn: h.MustListen(0),
		Locals:     func() []netip.Addr { return []netip.Addr{h.Addr()} },
		PunchTime:  1500 * time.Millisecond,
	}
}

// The full stack across simulated NATs: rendezvous, punch, QUIC, login.
func TestEndToEndThroughNATs(t *testing.T) {
	step2 := func(b *vnet.NAT) { b.SetPortStep(2) }
	random := func(b *vnet.NAT) { b.SetRandomPorts(true) }
	cases := []struct {
		name         string
		client, host vnet.NATKind
		wantErr      string
		// clientNAT and hostNAT configure the NATs; burn ports are taken
		// from the host's allocator after it registered (other traffic
		// on its LAN), and from the client's once it has measured.
		clientNAT, hostNAT func(*vnet.NAT)
		burn               int
		// auto leaves the punch time to the dialler (nat.PunchTime).
		auto bool
	}{
		{"port-restricted to port-restricted", vnet.PortRestrictedCone, vnet.PortRestrictedCone, "", nil, nil, 0, false},
		{"symmetric client to full-cone host", vnet.Symmetric, vnet.FullCone, "", nil, nil, 0, false},
		{"restricted client to symmetric host", vnet.RestrictedCone, vnet.Symmetric, "", nil, nil, 0, false},
		{"symmetric to symmetric", vnet.Symmetric, vnet.Symmetric, "both sides are behind symmetric NATs", nil, nil, 0, false},
		// Port prediction.
		{"port-restricted client to symmetric host", vnet.PortRestrictedCone, vnet.Symmetric, "", nil, nil, 0, false},
		{"symmetric client to port-restricted host", vnet.Symmetric, vnet.PortRestrictedCone, "", nil, nil, 0, false},
		{"port-restricted client to symmetric host (step 2), noisy LAN", vnet.PortRestrictedCone, vnet.Symmetric, "", nil, step2, 10, true},
		{"symmetric client (step 2) to port-restricted host, noisy LAN", vnet.Symmetric, vnet.PortRestrictedCone, "", step2, nil, 10, true},
		{"symmetric client to conntrack host", vnet.Symmetric, vnet.ConntrackCone, "", nil, nil, 0, true},
		// Ports that cannot be predicted fail as before, in the time
		// they always had.
		{"port-restricted client to symmetric host (random ports)", vnet.PortRestrictedCone, vnet.Symmetric,
			"the peer is behind a symmetric NAT whose ports cannot be predicted", nil, random, 0, true},
		{"symmetric client (random ports) to port-restricted host", vnet.Symmetric, vnet.PortRestrictedCone,
			"this side is behind a symmetric NAT whose ports cannot be predicted", random, nil, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.burn == 0 {
				t.Parallel() // the others set beforePunch
			}
			w := newVnetWorld(t)
			box := func(ip string, kind vnet.NATKind, setup func(*vnet.NAT)) *vnet.NAT {
				b := w.net.NAT(ip, kind)
				if setup != nil {
					setup(b)
				}
				return b
			}
			hostNAT := box("203.0.113.20", tc.host, tc.hostNAT)
			host := w.host(hostNAT.Host("10.0.0.20"))
			// Give the host a moment to measure its NAT, as a real one has.
			waitFor(t, func() bool { return host.Status().NAT != string(nat.TypeUnknown) })
			hostNAT.Burn(tc.burn)

			clientNAT := box("203.0.113.10", tc.client, tc.clientNAT)
			opts := w.options(clientNAT.Host("192.168.1.10"), host.Password())
			if tc.auto {
				opts.PunchTime = 0
			}
			if tc.burn > 0 {
				// Between the client's measurement (as its rendezvous
				// connection comes up) and its punch.
				beforePunch = func(int) { clientNAT.Burn(tc.burn) }
				defer func() { beforePunch = nil }()
			}
			start := time.Now()
			conn, err := Dial(testCtx(t), host.ID(), opts)
			if tc.wantErr != "" {
				if !errors.Is(err, nat.ErrNoPath) || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want a no-path error mentioning %q", err, tc.wantErr)
				}
				limit := 5 * time.Second
				if tc.auto {
					limit = nat.PunchTime(nat.TypeCone, nat.TypeSymmetric, false) + 1500*time.Millisecond
				}
				if took := time.Since(start); took > limit {
					t.Errorf("failure took %s", took)
				}
				return
			}
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			defer conn.Close()
			info := InfoOf(conn)
			if info.Path != PathPunched || info.Remote.Addr() != netip.MustParseAddr("203.0.113.20") {
				t.Errorf("path %s to %s, want punched to the host's NAT", info.Path, info.Remote)
			}
			echo(t, conn)
			// The host sees the client's NAT, not its private address.
			sessions := host.Sessions()
			if len(sessions) != 1 || sessions[0].Remote.Addr() != netip.MustParseAddr("203.0.113.10") {
				t.Errorf("host sessions: %+v", sessions)
			}
		})
	}
}

// A host behind a symmetric NAT that has been up for a while: the LAN's
// other traffic has moved the allocator on far past the window the client
// probes, and the NAT has forgotten the mapping the host measured it with.
// The host measures again before it answers, and the punch works.
func TestPredictionRefreshedForOffer(t *testing.T) {
	w := newVnetWorld(t)
	box := w.net.NAT("203.0.113.20", vnet.Symmetric)
	host := w.host(box.Host("10.0.0.20"))
	waitFor(t, func() bool { return host.Status().NAT != string(nat.TypeUnknown) })
	box.Burn(10 * nat.MaxCandidates * 16) // well past any window
	box.Expire(netip.MustParseAddrPort("198.51.100.1:21117"))

	opts := w.options(w.net.NAT("203.0.113.10", vnet.PortRestrictedCone).Host("192.168.1.10"), host.Password())
	opts.PunchTime = 0
	conn, err := Dial(testCtx(t), host.ID(), opts)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	if info := InfoOf(conn); info.Path != PathPunched {
		t.Errorf("path %s to %s, want punched", info.Path, info.Remote)
	}
	echo(t, conn)
}

// Two symmetric NATs cannot be punched. Both sides know their NAT type
// before the introduction, so the attempt is cut short instead of running
// its full course, and the host has been told what the client is behind.
func TestSymmetricNATsFailFast(t *testing.T) {
	w := newVnetWorld(t)
	var hostLog syncBuffer
	h := w.net.NAT("203.0.113.20", vnet.Symmetric).Host("10.0.0.20")
	host := startHost(t, hostd.Config{
		Signal:     vnetSignal,
		PacketConn: h.MustListen(0),
		Locals:     func() []netip.Addr { return []netip.Addr{h.Addr()} },
		Logger:     slog.New(slog.NewTextHandler(&hostLog, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	// The host knows its NAT type by the time it is registered.
	if got := host.Status().NAT; got != string(nat.TypeSymmetric) {
		t.Fatalf("host NAT type right after registration: %s", got)
	}

	opts := w.options(w.net.NAT("203.0.113.10", vnet.Symmetric).Host("192.168.1.10"), host.Password())
	opts.PunchTime = 0 // the dialler's own judgement
	start := time.Now()
	_, err := Dial(testCtx(t), host.ID(), opts)
	took := time.Since(start)
	if !errors.Is(err, nat.ErrNoPath) || !strings.Contains(err.Error(), "both sides are behind symmetric NATs") {
		t.Fatalf("err = %v", err)
	}
	if took > 2500*time.Millisecond {
		t.Fatalf("failure took %s, want about a second", took)
	}
	if !strings.Contains(hostLog.String(), "client_nat=symmetric") {
		t.Fatalf("host was not told the client's NAT type:\n%s", hostLog.String())
	}
}

// NATs that keep state for unsolicited packets (see internal/nat, "Who
// sends first"): the whole stack, with the host hearing about the
// connection first as it does in reality, gets through.
func TestEndToEndThroughConntrackNATs(t *testing.T) {
	w := newVnetWorld(t)
	// The introduction takes a while to travel on to the client.
	w.net.SetLatency(15 * time.Millisecond)
	host := w.host(w.net.NAT("203.0.113.20", vnet.ConntrackCone).Host("10.0.0.20"))
	opts := w.options(w.net.NAT("203.0.113.10", vnet.ConntrackCone).Host("192.168.1.10"), host.Password())
	opts.PunchTime = 0
	conn, err := Dial(testCtx(t), host.ID(), opts)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	if info := InfoOf(conn); info.Path != PathPunched {
		t.Errorf("path %s, want punched", info.Path)
	}
	echo(t, conn)
}

// syncBuffer is a log sink safe to read while it is written.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// The reviewer's case: a host (or whoever answers in its place) that puts
// terminal escape sequences into everything it can say before login. None
// of it may reach the error the user is shown.
func TestHostileHostTextIsNotPrinted(t *testing.T) {
	const esc = "\x1b]0;owned\x07\x1b[2J\x1b[31mremohop: login ok"
	id, err := auth.NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	serverTLS, _ := id.ServerTLS(peer.ALPN, true)
	for name, answer := range map[string]func(q *quic.Conn, st *quic.Stream){
		"unknown code with detail": func(q *quic.Conn, st *quic.Stream) {
			body, _ := json.Marshal(map[string]any{"v": 1, "ok": false, "code": "x" + esc, "detail": esc, "retry_after_ms": 3e12})
			st.Write([]byte{0, 0, byte(len(body) >> 8), byte(len(body))})
			st.Write(body)
			st.Close()
			<-q.Context().Done()
		},
		"close reason": func(q *quic.Conn, st *quic.Stream) {
			q.CloseWithError(0x7777, esc)
		},
		"refusal code in the close frame": func(q *quic.Conn, st *quic.Stream) {
			q.CloseWithError(0x100, esc)
		},
	} {
		t.Run(name, func(t *testing.T) {
			ln, err := quic.ListenAddr("127.0.0.1:0", serverTLS, peer.QUICConfig())
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			go func() {
				q, err := ln.Accept(context.Background())
				if err != nil {
					return
				}
				st, err := q.AcceptStream(context.Background())
				if err != nil {
					return
				}
				io.ReadFull(st, make([]byte, 4)) // the hello has started
				answer(q, st)
			}()
			_, err = Dial(testCtx(t), ln.Addr().String(), Options{Dir: t.TempDir(), Password: "whatever-pw", Locals: noLocals})
			if err == nil {
				t.Fatal("dial to the hostile host succeeded")
			}
			if strings.ContainsAny(err.Error(), "\x1b\x07") || strings.Contains(err.Error(), "\n") {
				t.Fatalf("escape sequence in the error: %q", err.Error())
			}
			if e, ok := errors.AsType[*auth.Error](err); ok && e.RetryAfter > auth.MaxRetryAfter {
				t.Fatalf("retry-after %s passed through", e.RetryAfter)
			}
		})
	}
}

// relayWorld is a virtual internet whose rendezvous server relays.
func relayWorld(t *testing.T, relay *signal.RelayConfig) *vnetWorld {
	t.Helper()
	w := &vnetWorld{t: t, net: vnet.New()}
	host := w.net.Host("198.51.100.1")
	if relay != nil {
		relay.Listen = func() (net.PacketConn, error) { return host.Listen(0) }
	}
	srv, err := signal.NewServer(signal.Config{DataDir: t.TempDir(), Relay: relay})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, host.MustListen(21116), host.MustListen(21117)) }()
	t.Cleanup(func() { cancel(); <-done; srv.Close() })
	return w
}

// Two symmetric NATs: no direct path. With a relaying rendezvous server the
// session is established through the relay, marked as such on both ends,
// and is the same authenticated session as any other. Either side can
// refuse relays; without the operator's opt-in there is none.
func TestRelayFallback(t *testing.T) {
	symmetricPair := func(w *vnetWorld, hostDir string) (*hostd.Host, Options) {
		h := w.net.NAT("203.0.113.20", vnet.Symmetric).Host("10.0.0.20")
		host := startHost(t, hostd.Config{
			Dir:        hostDir,
			Signal:     vnetSignal,
			PacketConn: h.MustListen(0),
			Locals:     func() []netip.Addr { return []netip.Addr{h.Addr()} },
		})
		opts := w.options(w.net.NAT("203.0.113.10", vnet.Symmetric).Host("192.168.1.10"), host.Password())
		opts.PunchTime = 0
		return host, opts
	}

	t.Run("relayed", func(t *testing.T) {
		w := relayWorld(t, &signal.RelayConfig{})
		host, opts := symmetricPair(w, "")
		var notices []string
		opts.Notify = func(msg string) { notices = append(notices, msg) }
		start := time.Now()
		conn, err := Dial(testCtx(t), host.ID(), opts)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer conn.Close()
		if took := time.Since(start); took > 4*time.Second {
			t.Errorf("relayed connect took %s", took)
		}
		info := InfoOf(conn)
		if info.Path != PathRelayed || info.Remote.Addr() != netip.MustParseAddr("198.51.100.1") {
			t.Fatalf("path %s to %s, want relayed through the server", info.Path, info.Remote)
		}
		if len(notices) != 1 || !strings.Contains(notices[0], "relay") {
			t.Fatalf("relay not announced: %v", notices)
		}
		echo(t, conn)
		// The host knows too, and it authenticated the client itself.
		st := host.Status()
		if len(st.Clients) != 1 || st.Clients[0].Path != "relayed" || st.Clients[0].Auth != auth.KindPassword {
			t.Fatalf("host status: %+v", st.Clients)
		}
		// A wrong password through the relay is as wrong as anywhere.
		bad := w.options(w.net.NAT("203.0.113.11", vnet.Symmetric).Host("192.168.1.10"), "not-the-password")
		bad.PunchTime = 0
		if _, err := Dial(testCtx(t), host.ID(), bad); auth.CodeOf(err) != auth.CodeAuthFailed {
			t.Fatalf("wrong password through the relay: %v", err)
		}
	})

	t.Run("direct paths are preferred", func(t *testing.T) {
		w := relayWorld(t, &signal.RelayConfig{})
		host := w.host(w.net.NAT("203.0.113.20", vnet.PortRestrictedCone).Host("10.0.0.20"))
		conn, err := Dial(testCtx(t), host.ID(), w.options(w.net.NAT("203.0.113.10", vnet.PortRestrictedCone).Host("192.168.1.10"), host.Password()))
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		if InfoOf(conn).Path != PathPunched {
			t.Fatalf("path %s although the NATs can be punched", InfoOf(conn).Path)
		}
	})

	t.Run("client refuses relays", func(t *testing.T) {
		w := relayWorld(t, &signal.RelayConfig{})
		host, opts := symmetricPair(w, "")
		opts.NoRelay = true
		_, err := Dial(testCtx(t), host.ID(), opts)
		if !errors.Is(err, nat.ErrNoPath) || !strings.Contains(err.Error(), "relaying is turned off on this machine") {
			t.Fatalf("err = %v", err)
		}
		t.Setenv("REMOHOP_RELAY", "0")
		opts.NoRelay = false
		opts.PacketConn = w.net.NAT("203.0.113.12", vnet.Symmetric).Host("192.168.1.10").MustListen(0)
		if _, err := Dial(testCtx(t), host.ID(), opts); !errors.Is(err, nat.ErrNoPath) || !strings.Contains(err.Error(), "turned off") {
			t.Fatalf("with REMOHOP_RELAY=0: %v", err)
		}
	})

	t.Run("host refuses relays", func(t *testing.T) {
		w := relayWorld(t, &signal.RelayConfig{})
		hostDir := t.TempDir()
		os.WriteFile(filepath.Join(hostDir, "config.json"), []byte(`{"relay":"off"}`), 0o600)
		host, opts := symmetricPair(w, hostDir)
		_, err := Dial(testCtx(t), host.ID(), opts)
		if !errors.Is(err, nat.ErrNoPath) || !strings.Contains(err.Error(), "host does not accept relayed connections") {
			t.Fatalf("err = %v", err)
		}
		if n := len(host.Sessions()); n != 0 {
			t.Fatalf("%d sessions on a host that refuses relays", n)
		}
	})

	t.Run("no relay unless the operator enabled it", func(t *testing.T) {
		w := relayWorld(t, nil)
		host, opts := symmetricPair(w, "")
		_, err := Dial(testCtx(t), host.ID(), opts)
		if !errors.Is(err, nat.ErrNoPath) || strings.Contains(err.Error(), "relay") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("all relay slots in use", func(t *testing.T) {
		w := relayWorld(t, &signal.RelayConfig{MaxSessions: 1})
		host, opts := symmetricPair(w, "")
		conn, err := Dial(testCtx(t), host.ID(), opts)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		second := w.options(w.net.NAT("203.0.113.13", vnet.Symmetric).Host("192.168.1.10"), host.Password())
		second.PunchTime = 0
		_, err = Dial(testCtx(t), host.ID(), second)
		if !errors.Is(err, nat.ErrNoPath) || !strings.Contains(err.Error(), "relay slots") {
			t.Fatalf("second relayed session with one slot: %v", err)
		}
		echo(t, conn)
	})
}

// A path that carries no IPv4 packet above 1280 bytes (a router whose
// uplink is a WireGuard or Tailscale tunnel, many VPNs) and filters the
// ICMP that would say so. QUIC's default first packets (1308 bytes on the
// wire) never arrive there; the whole stack has to start smaller.
func TestEndToEndSmallPathMTU(t *testing.T) {
	w := newVnetWorld(t)
	w.net.SetMTU(1280)
	host := w.host(w.net.NAT("203.0.113.20", vnet.PortRestrictedCone).Host("10.0.0.20"))
	conn, err := Dial(testCtx(t), host.ID(), w.options(w.net.NAT("203.0.113.10", vnet.PortRestrictedCone).Host("192.168.1.10"), host.Password()))
	if err != nil {
		t.Fatalf("dial over a 1280-byte path: %v", err)
	}
	defer conn.Close()
	ctx := testCtx(t)
	s, err := conn.Open(ctx, echoService, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	msg := bytes.Repeat([]byte("0123456789abcdef"), 16<<10) // 256 KiB: many full-size packets
	go func() { s.Write(msg); s.CloseWrite() }()
	got, err := io.ReadAll(s)
	if err != nil || !bytes.Equal(got, msg) {
		t.Fatalf("echo of %d bytes: got %d, %v", len(msg), len(got), err)
	}
}

// Two machines behind one router that keeps them apart (Wi-Fi client
// isolation, a guest network) and does not hairpin: no direct path, and a
// second punch could not change that. The relay is set up alongside the
// punch after a moment of silence, instead of after a 8 s punch and a 4 s
// retry.
func TestRelayRacesAHopelessPunch(t *testing.T) {
	w := relayWorld(t, &signal.RelayConfig{})
	box := w.net.NAT("203.0.113.10", vnet.PortRestrictedCone)
	box.SetIsolation(true)
	hh := box.Host("192.168.1.20")
	host := startHost(t, hostd.Config{Signal: vnetSignal, PacketConn: hh.MustListen(0), Locals: noLocals})
	ch := box.Host("192.168.1.10")
	opts := w.options(ch, host.Password())
	opts.PacketConn, opts.Listen, opts.PunchTime = nil, listenOn(ch), 0
	var attempts []int
	beforePunch = func(n int) { attempts = append(attempts, n) }
	defer func() { beforePunch = nil }()
	start := time.Now()
	conn, err := Dial(testCtx(t), host.ID(), opts)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	took := time.Since(start)
	if InfoOf(conn).Path != PathRelayed {
		t.Fatalf("path %s", InfoOf(conn).Path)
	}
	if took > relayAfter+2*time.Second || fmt.Sprint(attempts) != "[1]" {
		t.Fatalf("relayed after %s and punch attempts %v; want about %s and one attempt", took, attempts, relayAfter)
	}
	echo(t, conn)
}

func TestEndToEndSameLAN(t *testing.T) {
	w := newVnetWorld(t)
	box := w.net.NAT("203.0.113.10", vnet.PortRestrictedCone)
	host := w.host(box.Host("192.168.1.20"))
	conn, err := Dial(testCtx(t), host.ID(), w.options(box.Host("192.168.1.10"), host.Password()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	if info := InfoOf(conn); info.Path != PathLAN || info.Remote.Addr() != netip.MustParseAddr("192.168.1.20") {
		t.Errorf("path %s to %s, want lan to 192.168.1.20", info.Path, info.Remote)
	}
	echo(t, conn)
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Sessions survive the rendezvous server going away, and the host comes
// back online by itself when the server returns.
func TestRendezvousOutage(t *testing.T) {
	w := &vnetWorld{t: t, net: vnet.New()}
	srvHost := w.net.Host("198.51.100.1")
	dataDir := t.TempDir()
	run := func() (stop func()) {
		srv, err := signal.NewServer(signal.Config{DataDir: dataDir})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- srv.Serve(ctx, srvHost.MustListen(21116), srvHost.MustListen(21117)) }()
		return func() { cancel(); <-done; srv.Close() }
	}
	stop := run()

	host := w.host(w.net.Host("192.0.2.20"))
	id := host.ID()
	client := w.net.Host("192.0.2.10")
	conn, err := Dial(testCtx(t), id, w.options(client, host.Password()))
	if err != nil {
		stop()
		t.Fatal(err)
	}
	defer conn.Close()

	stop()
	waitFor(t, func() bool { return !host.Status().Online })
	echo(t, conn) // the session does not care

	stop = run()
	defer stop()
	deadline := time.Now().Add(20 * time.Second)
	for !host.Status().Online {
		if time.Now().After(deadline) {
			t.Fatal("host did not re-register after the rendezvous server came back")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if host.ID() != id {
		t.Fatalf("ID changed across the outage: %s -> %s", id, host.ID())
	}
	conn2, err := Dial(testCtx(t), id, w.options(client, host.Password()))
	if err != nil {
		t.Fatalf("dial after the outage: %v", err)
	}
	conn2.Close()
	echo(t, conn)
}

// Many commands started at once against one host (a script, an agent) all
// get their session, with the password as well as with a pairing: logins
// from one address queue on the host instead of tripping the lockout.
func TestParallelDials(t *testing.T) {
	sig := loopbackSignal(t)
	host := startHost(t, hostd.Config{Signal: sig})
	ctx := testCtx(t)
	clientDir := t.TempDir()
	opts := Options{Signal: sig, Dir: clientDir, Locals: noLocals}

	dialAll := func(o Options) {
		t.Helper()
		const n = 12
		errs := make(chan error, n)
		for i := 0; i < n; i++ {
			go func() {
				conn, err := Dial(ctx, host.ID(), o)
				if err == nil {
					_, err = hostd.Ping(ctx, conn, hostd.PingOptions{Count: 1})
					conn.Close()
				}
				errs <- err
			}()
		}
		for i := 0; i < n; i++ {
			if err := <-errs; err != nil {
				t.Fatalf("parallel dial: %v", err)
			}
		}
	}

	withPW := opts
	withPW.Password = host.Password()
	dialAll(withPW)

	conn, err := Dial(ctx, host.ID(), withPW)
	if err != nil {
		t.Fatal(err)
	}
	pairing, err := hostd.Pair(ctx, conn, hostd.PairRequest{Name: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	if err := SavePairing(InfoOf(conn), pairing, "", clientDir); err != nil {
		t.Fatal(err)
	}
	conn.Close()
	dialAll(opts)

	// Concurrent first contacts raced to pin the host key; exactly one
	// record resulted.
	peers, err := mustStore(t, clientDir).List()
	if err != nil || len(peers) != 1 || peers[0].HostKey != auth.EncodeKey(host.PublicKey()) {
		t.Fatalf("saved peers after parallel dials: %+v, %v", peers, err)
	}
}

func TestReuseHook(t *testing.T) {
	sig := loopbackSignal(t)
	host := startHost(t, hostd.Config{Signal: sig})
	ctx := testCtx(t)
	opts := Options{Signal: sig, Dir: t.TempDir(), Password: host.Password(), Locals: noLocals}
	shared, err := Dial(ctx, host.ID(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer shared.Close()

	var asked []string
	Reuse = func(_ context.Context, target string) (*peer.Conn, bool) {
		asked = append(asked, target)
		return shared, target == host.ID()
	}
	defer func() { Reuse = nil }()

	// The broker is consulted with the canonical target and its session
	// is returned as is, with no credentials needed.
	got, err := Dial(ctx, signal.FormatID(host.ID()), Options{Dir: t.TempDir()})
	if err != nil || got != shared {
		t.Fatalf("Dial with broker = %v, %v", got, err)
	}
	if len(asked) != 1 || asked[0] != host.ID() {
		t.Fatalf("broker asked for %v", asked)
	}
	if n := len(host.Sessions()); n != 1 {
		t.Fatalf("%d host sessions, want the one shared session", n)
	}
}

// cliEnv is a command environment that dials for real with test options.
func cliEnv(target, password string, opts Options) (*cli.Env, *bytes.Buffer, *bytes.Buffer) {
	var out, errb bytes.Buffer
	env := &cli.Env{
		Target: target,
		Stdin:  strings.NewReader(""),
		Stdout: &out,
		Stderr: &errb,
		DialFunc: func(ctx context.Context, env *cli.Env) (*peer.Conn, error) {
			o := opts
			o.Password = env.Globals.Password
			return Dial(ctx, env.Target, o)
		},
	}
	env.Globals.Password = password
	return env, &out, &errb
}

func TestCommands(t *testing.T) {
	sig := loopbackSignal(t)
	host := startHost(t, hostd.Config{Signal: sig})
	clientDir := t.TempDir()
	t.Setenv("REMOHOP_CONFIG_DIR", clientDir)
	ctx := testCtx(t)
	id := host.ID()
	opts := Options{Signal: sig, Locals: noLocals}

	run := func(name, target, password string, args ...string) (int, string, string) {
		t.Helper()
		env, out, errb := cliEnv(target, password, opts)
		defer env.Close()
		code := cli.Lookup(name).Run(ctx, env, args)
		return code, out.String(), errb.String()
	}

	// Without a terminal and without credentials: an error, never a prompt.
	code, _, errOut := run("ping", id, "")
	if code != cli.ExitConnect || !strings.Contains(errOut, "pass --password") {
		t.Fatalf("ping without credentials: code %d, stderr %q", code, errOut)
	}

	code, out, errOut := run("ping", id, host.Password(), "-c", "2", "-i", "0.01")
	if code != cli.ExitOK || !strings.Contains(out, "lan path") || !strings.Contains(out, "no relay") || strings.Count(out, "reply from") != 2 {
		t.Fatalf("ping: code %d\nstdout: %s\nstderr: %s", code, out, errOut)
	}

	code, out, errOut = run("pair", id, host.Password(), "--name", "office", "--label", "ci-box")
	if code != cli.ExitOK || !strings.Contains(out, "paired with "+signal.FormatID(id)) || !strings.Contains(out, "remohop office") {
		t.Fatalf("pair: code %d\nstdout: %s\nstderr: %s", code, out, errOut)
	}
	if code, _, errOut := run("pair", id, host.Password(), "--name", "ping"); code != cli.ExitUsage || !strings.Contains(errOut, "shadow") {
		t.Fatalf("pair with a command name as alias: code %d, stderr %q", code, errOut)
	}

	// From now on: by alias, no password, machine-readable.
	code, out, errOut = run("ping", "office", "", "-c", "3", "-i", "0", "--json")
	if code != cli.ExitOK {
		t.Fatalf("ping after pairing: code %d, stderr %q", code, errOut)
	}
	var ping struct {
		Target string    `json:"target"`
		Path   string    `json:"path"`
		P2P    bool      `json:"p2p"`
		Auth   string    `json:"auth"`
		RTT    []float64 `json:"rtt_ms"`
		Remote string    `json:"remote"`
	}
	if err := json.Unmarshal([]byte(out), &ping); err != nil {
		t.Fatalf("ping --json output %q: %v", out, err)
	}
	if ping.Target != id || ping.Path != "lan" || !ping.P2P || ping.Auth != "token" || len(ping.RTT) != 3 || ping.Remote == "" {
		t.Fatalf("ping --json = %+v", ping)
	}

	code, out, _ = run("peers", "", "")
	if code != cli.ExitOK || !strings.Contains(out, signal.FormatID(id)) || !strings.Contains(out, "office") || !strings.Contains(out, "yes") {
		t.Fatalf("peers: code %d\n%s", code, out)
	}
	code, out, _ = run("peers", "", "", "--json")
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil || code != cli.ExitOK || len(rows) != 1 || rows[0]["paired"] != true {
		t.Fatalf("peers --json: code %d, %q, %v", code, out, err)
	}
	if strings.Contains(out, "token") {
		t.Fatalf("peers --json leaks token material: %s", out)
	}
	if code, _, _ := run("peers", "", "", "alias", id, "lab"); code != cli.ExitOK {
		t.Fatal("peers alias failed")
	}
	if code, _, _ := run("ping", "lab", "", "-c", "1"); code != cli.ExitOK {
		t.Fatal("ping by new alias failed")
	}
	if code, _, _ := run("peers", "", "", "forget", "lab"); code != cli.ExitOK {
		t.Fatal("peers forget failed")
	}
	if code, _, errOut := run("peers", "", "", "forget", "lab"); code != cli.ExitError || !strings.Contains(errOut, "no saved host") {
		t.Fatalf("second forget: code %d, %q", code, errOut)
	}
	if code, _, _ := run("ping", id, "", "-c", "1"); code != cli.ExitConnect {
		t.Fatal("ping still works after the pairing was forgotten")
	}

	// key export / peers trust
	code, out, _ = run("key", "", "", "export", "--label", "ci box")
	key, label, err := auth.ParseKeyLine(strings.TrimSpace(out))
	if code != cli.ExitOK || err != nil || label != "ci box" {
		t.Fatalf("key export: %d %q %v", code, out, err)
	}
	if me, _ := auth.LoadIdentity(clientDir); !me.Public().Equal(key) {
		t.Fatal("key export printed another key than this machine's identity")
	}
	hostLine := auth.FormatKeyLine(host.PublicKey(), "host")
	if code, _, errOut := run("peers", "", "", "trust", id, hostLine); code != cli.ExitOK {
		t.Fatalf("peers trust: %d %q", code, errOut)
	}
	if p, _, _ := mustStore(t, clientDir).Get(id); !p.KeyLogin || p.HostKey != auth.EncodeKey(host.PublicKey()) {
		t.Fatalf("peer after trust: %+v", p)
	}
	other, _ := auth.NewIdentity()
	if code, _, errOut := run("peers", "", "", "trust", id, auth.FormatKeyLine(other.Public(), "")); code != cli.ExitError || !strings.Contains(errOut, "different key") {
		t.Fatalf("trust with a conflicting key: %d %q", code, errOut)
	}
	// The host's owner authorises this machine; from then on: no password.
	if _, err := host.Tokens().Authorize("ci", key); err != nil {
		t.Fatal(err)
	}
	code, out, errOut = run("ping", id, "", "-c", "1", "--json")
	if code != cli.ExitOK || !strings.Contains(out, `"auth": "key"`) {
		t.Fatalf("ping with key login: %d %s %s", code, out, errOut)
	}

	// config
	for _, args := range [][]string{{"feature", "input", "off"}, {"ask", "on"}, {"ask-paired", "on"}, {"otp", "on"}, {"relay", "off"}} {
		if code, _, errOut := run("config", "", "", args...); code != cli.ExitOK {
			t.Fatalf("config %v: %d %q", args, code, errOut)
		}
	}
	if code, out, _ := run("config", "", ""); code != cli.ExitOK || !regexp.MustCompile(`feature\.input\s+off`).MatchString(out) ||
		!regexp.MustCompile(`feature\.shell\s+on`).MatchString(out) || !regexp.MustCompile(`(?m)^ask\s+on`).MatchString(out) {
		t.Fatalf("config listing:\n%s", out)
	}
	if code, _, errOut := run("config", "", "", "feature", "telepathy", "on"); code != cli.ExitUsage || !strings.Contains(errOut, "shell, file") {
		t.Fatalf("config feature with an unknown name: %d %q", code, errOut)
	}
	// "files" is what people type for the "file" feature.
	if code, out, errOut := run("config", "", "", "feature", "files", "off"); code != cli.ExitOK || out != "feature.file: off\n" {
		t.Fatalf("config feature files: %d %q %q", code, out, errOut)
	}
	if code, out, _ := run("config", "", ""); code != cli.ExitOK || !regexp.MustCompile(`feature\.file\s+off`).MatchString(out) {
		t.Fatalf("config listing after feature files off:\n%s", out)
	}
	run("config", "", "", "feature", "file", "on")
	if code, _, _ := run("config", "", "", "ask", "maybe"); code != cli.ExitUsage {
		t.Fatal("config ask accepted a value that is neither on nor off")
	}
	for _, args := range [][]string{{"feature", "input", "on"}, {"ask", "off"}, {"ask-paired", "off"}, {"otp", "off"}, {"relay", "on"}} {
		run("config", "", "", args...)
	}
	if code, _, errOut := run("config", "", "", "set", "nonsense", "x"); code != cli.ExitUsage || !strings.Contains(errOut, "unknown key") {
		t.Fatalf("config set of unknown key: %d %q", code, errOut)
	}
	if code, _, _ := run("config", "", "", "get", "signal"); code != cli.ExitError {
		t.Fatal("config get of an unset key should exit non-zero")
	}
	if code, out, errOut := run("config", "", "", "set", "signal", "rendezvous.example.net:1234"); code != cli.ExitOK || out != "signal: rendezvous.example.net:1234\n" {
		t.Fatalf("config set: %d %q", code, errOut)
	}
	if code, out, _ := run("config", "", "", "get", "signal"); code != cli.ExitOK || out != "rendezvous.example.net:1234\n" {
		t.Fatalf("config get: %d %q", code, out)
	}
	if code, _, _ := run("config", "", "", "set", "host-password", "short"); code != cli.ExitUsage {
		t.Fatal("config accepted a too-short host password")
	}
	if _, out, _ := run("config", "", "", "set", "host-password", "long enough password"); out != "host-password: set\n" {
		t.Fatalf("config set of a secret printed %q", out)
	}
	if code, out, _ := run("config", "", ""); code != cli.ExitOK || !strings.Contains(out, "rendezvous.example.net:1234") || strings.Contains(out, "long enough") {
		t.Fatalf("config listing: %d\n%s", code, out)
	}
	if addr, _, err := signal.Resolve(""); err != nil || addr != "rendezvous.example.net:1234" {
		t.Fatalf("signal.Resolve after config set: %q, %v", addr, err)
	}
	if addr, _, _ := signal.Resolve("override:1"); addr != "override:1" {
		t.Fatal("flag does not override the config file")
	}
	run("config", "", "", "unset", "signal")
	if _, _, err := signal.Resolve(""); !errors.Is(err, signal.ErrNotConfigured) {
		t.Fatalf("after unset: %v", err)
	}
}

func TestDevices(t *testing.T) {
	sig := loopbackSignal(t)
	host := startHost(t, hostd.Config{Signal: sig})
	clientDir := t.TempDir()
	ctx := testCtx(t)
	opts := Options{Signal: sig, Dir: clientDir, Locals: noLocals}

	if list, err := Devices(ctx, DevicesOptions{Options: opts}); err != nil || len(list) != 0 {
		t.Fatalf("no saved peers yet: %v %v", list, err)
	}
	withPW := opts
	withPW.Password = host.Password()
	conn, err := Dial(ctx, host.ID(), withPW)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	store := mustStore(t, clientDir)
	if p, _, _ := store.Get(host.ID()); p.LastSeen == nil {
		t.Fatal("a successful login did not record when the host was last seen")
	}
	// A saved host that the server has never heard of, and one by address.
	store.Update("123456780", func(p *Peer) error { return nil })
	store.Update("192.0.2.1:21118", func(p *Peer) error { p.Alias = "lab"; return nil })

	list, err := Devices(ctx, DevicesOptions{Options: opts})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Device{}
	for _, d := range list {
		got[d.Target] = d
	}
	if d := got[host.ID()]; d.Presence != signal.PresenceOnline || d.Login != "password" || d.HostIdentity == "" || d.LastSeen == nil {
		t.Fatalf("online host: %+v", d)
	}
	if d := got["123456780"]; d.Presence != signal.PresenceUnknown {
		t.Fatalf("unknown ID: %+v", d)
	}
	if d := got["192.0.2.1:21118"]; d.Presence != "not_checked" || d.Alias != "lab" {
		t.Fatalf("address target: %+v", d)
	}
	// --offline does not ask; an unreachable server is reported, not fatal.
	list, err = Devices(ctx, DevicesOptions{Options: opts, Offline: true})
	if err != nil || list[0].Presence != "not_checked" {
		t.Fatalf("offline: %+v %v", list, err)
	}
	dead, _ := net.ListenPacket("udp", "127.0.0.1:0")
	deadAddr := dead.LocalAddr().String()
	dead.Close()
	bad := opts
	bad.Signal = deadAddr
	list, err = Devices(ctx, DevicesOptions{Options: bad})
	if err != nil || got[host.ID()].Target == "" {
		t.Fatalf("unreachable server: %v", err)
	}
	for _, d := range list {
		if cli.IsID(d.Target) && (d.Presence != "error" || d.Error == "") {
			t.Fatalf("unreachable server, %s: %+v", d.Target, d)
		}
	}

	// The command.
	t.Setenv("REMOHOP_CONFIG_DIR", clientDir)
	env, out, _ := cliEnv("", "", opts)
	env.Globals.Signal = sig
	if code := cli.Lookup("devices").Run(ctx, env, []string{"--json"}); code != cli.ExitOK {
		t.Fatalf("devices --json: %d", code)
	}
	var rows []Device
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil || len(rows) != 3 {
		t.Fatalf("devices --json output %q: %v", out.String(), err)
	}
	env, out, _ = cliEnv("", "", opts)
	env.Globals.Signal = sig
	if code := cli.Lookup("devices").Run(ctx, env, nil); code != cli.ExitOK || !strings.Contains(out.String(), "online") || !strings.Contains(out.String(), "not checked") {
		t.Fatalf("devices: %d\n%s", code, out.String())
	}
}

func TestKeyCommands(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("REMOHOP_CONFIG_DIR", dir)
	run := func(stdin string, args ...string) (int, string, string) {
		env, out, errb := cliEnv("", "", Options{})
		env.Stdin = strings.NewReader(stdin)
		code := cli.Lookup("key").Run(testCtx(t), env, args)
		return code, out.String(), errb.String()
	}
	_, line, _ := run("", "export")
	first, _, err := auth.ParseKeyLine(strings.TrimSpace(line))
	if err != nil {
		t.Fatal(err)
	}
	// show prints the fingerprint a host shows in its approval prompt.
	if code, out, _ := run("", "show"); code != cli.ExitOK || out != auth.Fingerprint(first)+"\n" {
		t.Fatalf("key show: %d %q, want %q", code, out, auth.Fingerprint(first))
	}
	// Destructive subcommands need --yes, and say what breaks.
	for _, args := range [][]string{{"gen"}, {"clear"}} {
		code, _, errOut := run("", args...)
		if code != cli.ExitUsage || !strings.Contains(errOut, "--yes") || !strings.Contains(errOut, "pairing") {
			t.Fatalf("key %v without --yes: %d %q", args, code, errOut)
		}
	}
	if _, again, _ := run("", "export"); again != line {
		t.Fatal("identity changed without --yes")
	}
	// Private export and import move an identity.
	keyFile := filepath.Join(t.TempDir(), "id.pem")
	if code, _, errOut := run("", "export", "--private", keyFile); code != cli.ExitOK {
		t.Fatalf("export --private: %d %q", code, errOut)
	}
	if private, err := config.IsPrivate(keyFile); err != nil || !private {
		t.Fatalf("private key file is not private (%v)", err)
	}
	if code, _, _ := run("", "export", "--private", keyFile); code == cli.ExitOK {
		t.Fatal("export --private overwrote an existing file")
	}
	if code, out, _ := run("", "gen", "--yes"); code != cli.ExitOK || !strings.Contains(out, "new identity") {
		t.Fatalf("gen: %d %q", code, out)
	}
	_, line2, _ := run("", "export")
	if second, _, _ := auth.ParseKeyLine(strings.TrimSpace(line2)); second.Equal(first) {
		t.Fatal("gen kept the old key")
	}
	blob, _ := os.ReadFile(keyFile)
	if code, _, errOut := run(string(blob), "import", "-"); code != cli.ExitUsage || !strings.Contains(errOut, "--yes") {
		t.Fatalf("import without --yes: %d %q", code, errOut)
	}
	if code, _, errOut := run("not a key", "import", "-", "--yes"); code != cli.ExitUsage || !strings.Contains(errOut, "export --private") {
		t.Fatalf("import of garbage: %d %q", code, errOut)
	}
	if code, _, errOut := run("", "import", keyFile, "--yes"); code != cli.ExitOK {
		t.Fatalf("import: %d %q", code, errOut)
	}
	if _, back, _ := run("", "export"); back != line {
		t.Fatal("import did not restore the exported identity")
	}
	if code, _, _ := run("", "clear", "--yes"); code != cli.ExitOK {
		t.Fatal("clear")
	}
	if _, err := os.Stat(filepath.Join(dir, auth.IdentityFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("clear left the identity file")
	}
	if code, _, _ := run("", "frobnicate"); code != cli.ExitUsage {
		t.Fatal("unknown key subcommand accepted")
	}
}

// listenOn returns an Options.Listen that opens a fresh socket on h each
// time, the way a real dial opens a fresh UDP port.
func listenOn(h *vnet.Host) func() (net.PacketConn, error) {
	return func() (net.PacketConn, error) { return h.Listen(0) }
}

// The punch between two NATs that keep state for unsolicited packets
// depends on the host's priming winning a race; when the introduction
// reaches the client late, it loses (the host's real probes reach the
// client's NAT first and leave a stray entry there). The dialler then
// retries once, on a fresh socket and a fresh introduction, and wins.
func TestPunchRetryAfterLostRace(t *testing.T) {
	var attempts []int
	var mu sync.Mutex
	beforePunch = func(n int) {
		mu.Lock()
		attempts = append(attempts, n)
		mu.Unlock()
		if n == 1 {
			// The introduction travels slowly this time: well past the
			// host's priming hold (2 s), so its full-TTL probes reach
			// this NAT first and the first attempt is lost for certain.
			time.Sleep(3 * time.Second)
		}
	}
	defer func() { beforePunch = nil }()

	w := newVnetWorld(t)
	host := w.host(w.net.NAT("203.0.113.20", vnet.ConntrackCone).Host("10.0.0.20"))
	ch := w.net.NAT("203.0.113.10", vnet.ConntrackCone).Host("192.168.1.10")
	opts := w.options(ch, host.Password())
	opts.PacketConn, opts.Listen = nil, listenOn(ch)
	start := time.Now()
	conn, err := Dial(testCtx(t), host.ID(), opts)
	if err != nil {
		t.Fatalf("dial with one lost race: %v (attempts %v)", err, attempts)
	}
	defer conn.Close()
	if fmt.Sprint(attempts) != "[1 2]" {
		t.Fatalf("punch attempts %v, want [1 2]", attempts)
	}
	// 3 s of forced delay, the lost first punch (1.5 s here), and the
	// second attempt, which wins within about a round trip.
	if took := time.Since(start); took > 9*time.Second {
		t.Fatalf("dial took %s", took)
	}
	if InfoOf(conn).Path != PathPunched {
		t.Fatalf("path %s", InfoOf(conn).Path)
	}
	echo(t, conn)

	// Without the retry the same race is lost: the first attempt alone
	// fails (this is what makes the test above meaningful).
	attempts = nil
	host2 := w.host(w.net.NAT("203.0.113.21", vnet.ConntrackCone).Host("10.0.0.21"))
	ch2 := w.net.NAT("203.0.113.11", vnet.ConntrackCone).Host("192.168.1.11")
	single := w.options(ch2, host2.Password()) // PacketConn: no fresh socket, no retry
	if _, err := Dial(testCtx(t), host2.ID(), single); !errors.Is(err, nat.ErrNoPath) || fmt.Sprint(attempts) != "[1]" {
		t.Fatalf("single attempt: %v (attempts %v), want ErrNoPath after one attempt", err, attempts)
	}
}

// A symmetric pair fails the same way every time; it is never retried.
func TestNoPunchRetryForSymmetric(t *testing.T) {
	var attempts []int
	beforePunch = func(n int) { attempts = append(attempts, n) }
	defer func() { beforePunch = nil }()
	w := newVnetWorld(t)
	h := w.net.NAT("203.0.113.20", vnet.Symmetric).Host("10.0.0.20")
	host := startHost(t, hostd.Config{Signal: vnetSignal, PacketConn: h.MustListen(0),
		Locals: func() []netip.Addr { return []netip.Addr{h.Addr()} }})
	ch := w.net.NAT("203.0.113.10", vnet.Symmetric).Host("192.168.1.10")
	opts := w.options(ch, host.Password())
	opts.PacketConn, opts.Listen, opts.PunchTime = nil, listenOn(ch), 0
	start := time.Now()
	_, err := Dial(testCtx(t), host.ID(), opts)
	if !errors.Is(err, nat.ErrNoPath) || !strings.Contains(err.Error(), "both sides are behind symmetric NATs") {
		t.Fatalf("err = %v", err)
	}
	if fmt.Sprint(attempts) != "[1]" || time.Since(start) > 2500*time.Millisecond {
		t.Fatalf("attempts %v in %s, want one quick attempt", attempts, time.Since(start))
	}
}

// A saved password (Peer.Password, which a gateway saves for a host that
// does not pair) logs in without one being given; once the host has
// another password it is dropped and the dial says so.
func TestSavedPassword(t *testing.T) {
	ctx := testCtx(t)
	h := startHost(t, hostd.Config{})
	addr := h.Addr().String()
	clientDir := t.TempDir()
	first := h.Password()

	conn, err := Dial(ctx, addr, Options{Dir: clientDir, Password: first, Locals: noLocals})
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	st := mustStore(t, clientDir)
	if err := st.SetPassword("nobody:1", "x"); err == nil {
		t.Fatal("a password was saved for a host that is not saved")
	}
	if err := st.SetPassword(addr, first); err != nil {
		t.Fatal(err)
	}
	conn, err = Dial(ctx, addr, Options{Dir: clientDir, Locals: noLocals})
	if err != nil {
		t.Fatalf("dial with the saved password: %v", err)
	}
	conn.Close()

	// The host gets a new password (quick support and the desktop app
	// make one at every start).
	if err := h.RotatePassword(); err != nil {
		t.Fatal(err)
	}
	_, err = Dial(ctx, addr, Options{Dir: clientDir, Locals: noLocals})
	if !errors.Is(err, ErrSavedPasswordRejected) || !errors.Is(err, ErrNoCredential) {
		t.Fatalf("dial with a stale saved password: %v", err)
	}
	if p, _, _ := st.Get(addr); p.Password != "" {
		t.Fatal("the rejected password was kept")
	}
	// A given password still works, and nothing is saved by the dial.
	conn, err = Dial(ctx, addr, Options{Dir: clientDir, Password: h.Password(), Locals: noLocals})
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if p, _, _ := st.Get(addr); p.Password != "" {
		t.Fatal("a dial saved a password by itself")
	}
}
