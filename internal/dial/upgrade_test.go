package dial

import (
	"bytes"
	"io"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/hostd"
	"github.com/readori/RemoHop/internal/nat/vnet"
	"github.com/readori/RemoHop/internal/signal"
)

// gate is a firewall in front of the client that passes UDP to the
// rendezvous server (and its relay) always, and to anybody else only once
// opened: a network on which a direct path only becomes possible after the
// session has started.
type gate struct {
	net.PacketConn
	open *atomic.Bool
}

func (c gate) WriteTo(p []byte, addr net.Addr) (int, error) {
	if ua, ok := addr.(*net.UDPAddr); ok && !c.open.Load() && ua.AddrPort().Addr().Unmap() != netip.MustParseAddr("198.51.100.1") {
		return len(p), nil
	}
	return c.PacketConn.WriteTo(p, addr)
}

// fastUpgrades shortens the upgrade schedule for a test and counts attempts.
func fastUpgrades(t *testing.T) *atomic.Int32 {
	t.Helper()
	var n atomic.Int32
	oldSched, oldEvery, oldDrain := upgradeSchedule, upgradeEvery, relayDrain
	upgradeSchedule = []time.Duration{300 * time.Millisecond, 600 * time.Millisecond}
	upgradeEvery = 1500 * time.Millisecond
	relayDrain = 500 * time.Millisecond
	onUpgradeAttempt = func(int) { n.Add(1) }
	t.Cleanup(func() {
		upgradeSchedule, upgradeEvery, relayDrain = oldSched, oldEvery, oldDrain
		onUpgradeAttempt = nil
	})
	return &n
}

// relayedPair is a host and a client behind port-restricted cone NATs (a
// punchable pair) whose firewall starts closed, so the session is relayed.
func relayedPair(t *testing.T) (*vnetWorld, *hostd.Host, Options, *atomic.Bool) {
	t.Helper()
	w := relayWorld(t, &signal.RelayConfig{})
	hh := w.net.NAT("203.0.113.20", vnet.PortRestrictedCone).Host("10.0.0.20")
	host := startHost(t, hostd.Config{Signal: vnetSignal, PacketConn: hh.MustListen(0),
		Locals: func() []netip.Addr { return []netip.Addr{hh.Addr()} }})
	ch := w.net.NAT("203.0.113.10", vnet.PortRestrictedCone).Host("192.168.1.10")
	open := &atomic.Bool{}
	pc := ch.MustListen(0)
	opts := Options{
		Signal:     vnetSignal,
		Password:   host.Password(),
		Dir:        t.TempDir(),
		PacketConn: gate{pc, open},
		Locals:     func() []netip.Addr { return []netip.Addr{ch.Addr()} },
		PunchTime:  1500 * time.Millisecond,
	}
	return w, host, opts, open
}

// waitUntil polls cond for up to d.
func waitUntil(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
	return true
}

// TestRelayedSessionMovesToDirectPath: a session that had to be relayed
// moves to a direct path once one works, in the background, and a stream
// open across the move carries on as if nothing happened. Both ends say
// how the session travels now.
func TestRelayedSessionMovesToDirectPath(t *testing.T) {
	attempts := fastUpgrades(t)
	w, host, opts, open := relayedPair(t)
	// Packets through the relay: to or from the server's address, on a
	// port other than the rendezvous ports.
	var relayed atomic.Int64
	server := netip.MustParseAddr("198.51.100.1")
	w.net.SetTap(func(src, dst netip.AddrPort, _ []byte) {
		for _, a := range []netip.AddrPort{src, dst} {
			if a.Addr() == server && a.Port() != 21116 && a.Port() != 21117 {
				relayed.Add(1)
			}
		}
	})
	conn, err := Dial(testCtx(t), host.ID(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if p := InfoOf(conn).Path; p != PathRelayed {
		t.Fatalf("path %s, want relayed to begin with", p)
	}
	before := InfoOf(conn)

	// A stream opened over the relay, half used.
	s, err := conn.Open(testCtx(t), echoService, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var got bytes.Buffer
	var rmu sync.Mutex
	readDone := make(chan error, 1)
	go func() {
		buf := make([]byte, 32<<10)
		for {
			n, err := s.Read(buf)
			rmu.Lock()
			got.Write(buf[:n])
			rmu.Unlock()
			if err != nil {
				if err == io.EOF {
					err = nil
				}
				readDone <- err
				return
			}
		}
	}()
	first := bytes.Repeat([]byte("over the relay; "), 4096)
	if _, err := s.Write(first); err != nil {
		t.Fatal(err)
	}

	// The punch has failed at least once; now the network lets it through.
	if !waitUntil(5*time.Second, func() bool { return attempts.Load() >= 1 }) {
		t.Fatal("no attempt to find a direct path")
	}
	open.Store(true)
	if !waitUntil(10*time.Second, func() bool { return InfoOf(conn).Path != PathRelayed }) {
		t.Fatalf("still relayed after %d attempts", attempts.Load())
	}
	info := InfoOf(conn)
	if info.Path != PathPunched || info.Remote.Addr() != netip.MustParseAddr("203.0.113.20") {
		t.Fatalf("moved to %s at %s, want punched to the host's NAT", info.Path, info.Remote)
	}
	if before.Path != PathRelayed {
		t.Fatal("the Info handed out before the move was changed")
	}

	// The same stream goes on, now directly.
	second := bytes.Repeat([]byte("and now directly. "), 4096)
	if _, err := s.Write(second); err != nil {
		t.Fatalf("write after the move: %v", err)
	}
	s.CloseWrite()
	select {
	case err := <-readDone:
		if err != nil {
			t.Fatalf("read across the move: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("echo across the move did not finish")
	}
	rmu.Lock()
	ok := bytes.Equal(got.Bytes(), append(append([]byte{}, first...), second...))
	rmu.Unlock()
	if !ok {
		t.Fatalf("echo across the move: %d bytes back of %d", got.Len(), len(first)+len(second))
	}
	echo(t, conn) // and new streams

	// The host has followed and says so.
	if !waitUntil(5*time.Second, func() bool {
		st := host.Status()
		return len(st.Clients) == 1 && st.Clients[0].Path == "punched"
	}) {
		t.Fatalf("host status: %+v", host.Status().Clients)
	}
	// The relay is out of the picture once it has been released (after
	// relayDrain), and no more attempts are made.
	time.Sleep(relayDrain + 300*time.Millisecond)
	n, viaRelay := attempts.Load(), relayed.Load()
	echo(t, conn)
	time.Sleep(1500 * time.Millisecond)
	echo(t, conn)
	if attempts.Load() != n {
		t.Fatal("attempts go on after the move")
	}
	if d := relayed.Load() - viaRelay; d != 0 {
		t.Fatalf("%d packets through the relay after the move", d)
	}
}

// TestRelayedSessionStaysWhenNoDirectPath: between networks that can never
// be punched the session stays on the relay, undisturbed, and the attempts
// keep to their schedule.
func TestRelayedSessionStaysWhenNoDirectPath(t *testing.T) {
	attempts := fastUpgrades(t)
	_, host, opts, _ := relayedPair(t) // the gate never opens
	conn, err := Dial(testCtx(t), host.ID(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// A stream that stays open throughout, and new ones every so often.
	s, err := conn.Open(testCtx(t), echoService, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	start := time.Now()
	buf := make([]byte, 64)
	for time.Since(start) < 6*time.Second {
		msg := []byte(time.Now().Format(time.RFC3339Nano))
		if _, err := s.Write(msg); err != nil {
			t.Fatal(err)
		}
		if _, err := io.ReadFull(s, buf[:len(msg)]); err != nil || !bytes.Equal(buf[:len(msg)], msg) {
			t.Fatalf("long-lived stream: %q, %v", buf[:len(msg)], err)
		}
		echo(t, conn)
		time.Sleep(300 * time.Millisecond)
	}
	if p := InfoOf(conn).Path; p != PathRelayed {
		t.Fatalf("path %s", p)
	}
	// Waits of 0.3 s, 0.6 s, then 1.5 s, each attempt punching for
	// 1.5 s (Options.PunchTime): three attempts in 6 s, give or take one.
	if n := attempts.Load(); n < 2 || n > 4 {
		t.Fatalf("%d attempts in 6 s, want about 3", n)
	}
	if st := host.Status(); len(st.Clients) != 1 || st.Clients[0].Path != "relayed" {
		t.Fatalf("host status: %+v", st.Clients)
	}
}

// TestUpgradeNeedsTheSameHost: a path that leads to anybody but the host
// the session authenticated is never switched to, and the session goes on
// over the relay. (The rendezvous server is not trusted: it could hand out
// an impostor's address, and the nonce to punch it with.) The same step
// towards the real host succeeds.
func TestUpgradeNeedsTheSameHost(t *testing.T) {
	w := relayWorld(t, &signal.RelayConfig{})
	hh := w.net.Host("203.0.113.20") // public: no punch needed to reach it
	host := startHost(t, hostd.Config{Signal: vnetSignal, PacketConn: hh.MustListen(0),
		Locals: func() []netip.Addr { return []netip.Addr{hh.Addr()} }})
	ih := w.net.Host("203.0.113.66")
	impostor := startHost(t, hostd.Config{Signal: vnetSignal, PacketConn: ih.MustListen(0),
		Locals: func() []netip.Addr { return []netip.Addr{ih.Addr()} }})
	ch := w.net.NAT("203.0.113.10", vnet.PortRestrictedCone).Host("192.168.1.10")
	open := &atomic.Bool{}
	var u *upgrader
	testHookUpgrader = func(x *upgrader) { u = x }
	defer func() { testHookUpgrader = nil }()
	conn, err := Dial(testCtx(t), host.ID(), Options{
		Signal:     vnetSignal,
		Password:   host.Password(),
		Dir:        t.TempDir(),
		PacketConn: gate{ch.MustListen(0), open},
		Locals:     func() []netip.Addr { return []netip.Addr{ch.Addr()} },
		PunchTime:  1500 * time.Millisecond,
		NoUpgrade:  true, // driven by hand below
	})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if p := InfoOf(conn).Path; p != PathRelayed || u == nil {
		t.Fatalf("path %s", p)
	}
	open.Store(true)

	if err := u.migrate(testCtx(t), impostor.DirectAddrs()[0]); err == nil {
		t.Fatal("the session moved to a path that leads to another host")
	} else {
		t.Logf("path to the impostor refused: %v", err)
	}
	if st := host.Status(); len(st.Clients) != 1 || st.Clients[0].Path != "relayed" {
		t.Fatalf("host status: %+v", st.Clients)
	}
	if len(impostor.Sessions()) != 0 {
		t.Fatal("the impostor has a session")
	}
	echo(t, conn)

	if err := u.migrate(testCtx(t), host.DirectAddrs()[0]); err != nil {
		t.Fatalf("path to the real host: %v", err)
	}
	echo(t, conn)
	if !waitUntil(5*time.Second, func() bool {
		st := host.Status()
		return len(st.Clients) == 1 && st.Clients[0].Path == "direct"
	}) {
		t.Fatalf("host status: %+v", host.Status().Clients)
	}
}

// TestUpgradeOnNetworkChange: a change of the machine's addresses brings an
// attempt forward instead of waiting for the next one on the schedule.
func TestUpgradeOnNetworkChange(t *testing.T) {
	attempts := fastUpgrades(t)
	upgradeSchedule, upgradeEvery = []time.Duration{time.Hour}, time.Hour
	oldCheck := netCheckEvery
	netCheckEvery = 100 * time.Millisecond
	defer func() { netCheckEvery = oldCheck }()
	_, host, opts, _ := relayedPair(t)
	var extra atomic.Bool
	locals := opts.Locals
	opts.Locals = func() []netip.Addr {
		if extra.Load() {
			return append(locals(), netip.MustParseAddr("192.168.7.7"))
		}
		return locals()
	}
	conn, err := Dial(testCtx(t), host.ID(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	time.Sleep(500 * time.Millisecond)
	if n := attempts.Load(); n != 0 {
		t.Fatalf("%d attempts before the network changed", n)
	}
	extra.Store(true)
	if !waitUntil(2*time.Second, func() bool { return attempts.Load() == 1 }) {
		t.Fatalf("%d attempts after the network changed, want 1", attempts.Load())
	}
	echo(t, conn)
}

// TestUpgradeTurnedOff: REMOHOP_RELAY_UPGRADE=0 keeps relayed sessions on
// the relay without trying.
func TestUpgradeTurnedOff(t *testing.T) {
	attempts := fastUpgrades(t)
	t.Setenv("REMOHOP_RELAY_UPGRADE", "0")
	_, host, opts, open := relayedPair(t)
	conn, err := Dial(testCtx(t), host.ID(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	open.Store(true)
	time.Sleep(1500 * time.Millisecond)
	if n := attempts.Load(); n != 0 || InfoOf(conn).Path != PathRelayed {
		t.Fatalf("%d attempts, path %s", n, InfoOf(conn).Path)
	}
	echo(t, conn)
}
