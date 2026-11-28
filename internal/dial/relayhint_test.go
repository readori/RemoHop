package dial

import (
	"fmt"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/hostd"
	"github.com/readori/RemoHop/internal/nat/vnet"
	"github.com/readori/RemoHop/internal/signal"
)

// serverOnly passes datagrams to the rendezvous server only: a firewall
// that blocks UDP between the peers but not to the server.
type serverOnly struct{ net.PacketConn }

func (c serverOnly) WriteTo(p []byte, addr net.Addr) (int, error) {
	if ua, ok := addr.(*net.UDPAddr); ok && ua.AddrPort().Addr().Unmap() != netip.MustParseAddr("198.51.100.1") {
		return len(p), nil
	}
	return c.PacketConn.WriteTo(p, addr)
}

// TestRelayHint: once punching to a host has failed and the relay was
// used, the next dials from the same networks go to the relay at once; a
// change of network punches again.
func TestRelayHint(t *testing.T) {
	w := relayWorld(t, &signal.RelayConfig{})
	hh := w.net.NAT("203.0.113.20", vnet.PortRestrictedCone).Host("10.0.0.20")
	host := startHost(t, hostd.Config{Signal: vnetSignal, PacketConn: hh.MustListen(0),
		Locals: func() []netip.Addr { return []netip.Addr{hh.Addr()} }})
	dir := t.TempDir()
	client := func(publicIP string) (Options, *[]string) {
		ch := w.net.NAT(publicIP, vnet.PortRestrictedCone).Host("192.168.1.10")
		var notes []string
		return Options{
			Signal:    vnetSignal,
			Password:  host.Password(),
			Dir:       dir,
			Listen:    func() (net.PacketConn, error) { pc, err := ch.Listen(0); return serverOnly{pc}, err },
			Locals:    func() []netip.Addr { return []netip.Addr{ch.Addr()} },
			PunchTime: 2 * relayAfter,
			Notify:    func(m string) { notes = append(notes, m) },
		}, &notes
	}
	var attempts []int
	beforePunch = func(n int) { attempts = append(attempts, n) }
	defer func() { beforePunch = nil }()
	dialOnce := func(opts Options) time.Duration {
		t.Helper()
		attempts = nil
		start := time.Now()
		conn, err := Dial(testCtx(t), host.ID(), opts)
		if err != nil {
			t.Fatal(err)
		}
		took := time.Since(start)
		if InfoOf(conn).Path != PathRelayed || fmt.Sprint(attempts) != "[1]" {
			t.Fatalf("path %s after punch attempts %v; want the relay, raced in the first attempt", InfoOf(conn).Path, attempts)
		}
		echo(t, conn)
		conn.Close()
		return took
	}

	opts, notes := client("203.0.113.10")
	first := dialOnce(opts)
	second := dialOnce(opts)
	t.Logf("relayed dial without a hint %s, with one %s", first.Round(time.Millisecond), second.Round(time.Millisecond))
	// Without a hint the punch runs alone for relayAfter, then the relay
	// joins it (and wins), in the same attempt.
	if first < relayAfter {
		t.Errorf("first dial took only %s: did it punch at all?", first)
	}
	if second > time.Second || !strings.Contains((*notes)[len(*notes)-1], "minutes ago") {
		t.Errorf("hinted dial took %s, notices %q", second, *notes)
	}
	// Another network of ours: the hint does not apply.
	opts2, _ := client("203.0.113.11")
	if third := dialOnce(opts2); third < relayAfter {
		t.Errorf("dial from another network took %s: it used a hint for a different address", third)
	}
	// Relays turned off: the hint is not used.
	t.Setenv("REMOHOP_RELAY", "0")
	if _, err := Dial(testCtx(t), host.ID(), opts); err == nil {
		t.Fatal("dial with relays off succeeded")
	}
}

func TestRelayHintStore(t *testing.T) {
	dir := t.TempDir()
	a, b := netip.MustParseAddrPort("203.0.113.1:1"), netip.MustParseAddrPort("198.51.100.2:2")
	if relayHinted(dir, "123456789", a, b) {
		t.Fatal("hint without a record")
	}
	setRelayHint(dir, "123456789", a, b, true)
	if !relayHinted(dir, "123456789", netip.MustParseAddrPort("203.0.113.1:999"), b) {
		t.Fatal("hint not found (ports must not matter)")
	}
	if relayHinted(dir, "123456789", b, a) || relayHinted(dir, "987654321", a, b) || relayHinted(dir, "123456789", netip.AddrPort{}, b) {
		t.Fatal("hint applied to other addresses or hosts")
	}
	for i := range maxRelayHints + 10 {
		setRelayHint(dir, string(rune('a'+i%26))+strings.Repeat("x", i), a, b, true)
	}
	if n := len(readRelayHints(dir)); n > maxRelayHints {
		t.Fatalf("%d hints kept", n)
	}
	setRelayHint(dir, "123456789", a, b, false)
	if relayHinted(dir, "123456789", a, b) {
		t.Fatal("hint not forgotten")
	}
}

// TestRelayHintStillPunches: a hint sets the relay up at once, but the
// punch runs alongside it, so when the direct path works now (the network
// that blocked it is gone) the session is direct and the hint is dropped,
// rather than the hint pinning the host to the relay.
func TestRelayHintStillPunches(t *testing.T) {
	w := relayWorld(t, &signal.RelayConfig{})
	hh := w.net.NAT("203.0.113.20", vnet.PortRestrictedCone).Host("10.0.0.20")
	host := startHost(t, hostd.Config{Signal: vnetSignal, PacketConn: hh.MustListen(0),
		Locals: func() []netip.Addr { return []netip.Addr{hh.Addr()} }})
	dir := t.TempDir()
	ch := w.net.NAT("203.0.113.10", vnet.PortRestrictedCone).Host("192.168.1.10")
	opts := func(block bool) Options {
		return Options{
			Signal:   vnetSignal,
			Password: host.Password(),
			Dir:      dir,
			Listen: func() (net.PacketConn, error) {
				pc, err := ch.Listen(0)
				if block {
					return serverOnly{pc}, err
				}
				return pc, err
			},
			Locals:    func() []netip.Addr { return []netip.Addr{ch.Addr()} },
			PunchTime: 1500 * time.Millisecond,
		}
	}
	dial := func(o Options) Path {
		t.Helper()
		conn, err := Dial(testCtx(t), host.ID(), o)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		echo(t, conn)
		return InfoOf(conn).Path
	}
	if p := dial(opts(true)); p != PathRelayed {
		t.Fatalf("blocked dial: path %s", p)
	}
	if len(readRelayHints(dir)) != 1 {
		t.Fatalf("no hint after a relayed dial: %+v", readRelayHints(dir))
	}
	if p := dial(opts(false)); p == PathRelayed {
		t.Fatal("the hint kept the session on the relay although the direct path works now")
	}
	if n := len(readRelayHints(dir)); n != 0 {
		t.Fatalf("hint kept after a direct dial: %d", n)
	}
}
