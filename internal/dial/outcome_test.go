package dial

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/hostd"
	"github.com/readori/RemoHop/internal/nat"
	"github.com/readori/RemoHop/internal/nat/vnet"
	"github.com/readori/RemoHop/internal/signal"
)

// sent is one outcome report as the client sent it, and how the server
// answered.
type sent struct {
	o   signal.Outcome
	err error
}

// outcomeRecorder wraps the reports dialers made from now on until the
// test ends: send, if not nil, replaces the real request; started counts
// the reports begun, and each finished one is delivered on the channel.
type outcomeRecorder struct {
	ch      chan sent
	started atomic.Int32
}

func recordOutcomes(t *testing.T, send func(context.Context, *signal.Client, signal.Outcome) error) *outcomeRecorder {
	t.Helper()
	t.Setenv("REMOHOP_OUTCOMES", "")
	r := &outcomeRecorder{ch: make(chan sent, 16)}
	old := sendOutcome
	if send == nil {
		send = old
	}
	sendOutcome = func(ctx context.Context, sc *signal.Client, o signal.Outcome) error {
		r.started.Add(1)
		err := send(ctx, sc, o)
		r.ch <- sent{o, err}
		return err
	}
	t.Cleanup(func() { sendOutcome = old })
	return r
}

// next waits for the next finished report.
func (r *outcomeRecorder) next(t *testing.T) sent {
	t.Helper()
	select {
	case s := <-r.ch:
		return s
	case <-time.After(10 * time.Second):
		t.Fatal("no outcome reported")
		return sent{}
	}
}

// none checks that no report is started within a moment.
func (r *outcomeRecorder) none(t *testing.T) {
	t.Helper()
	time.Sleep(300 * time.Millisecond)
	if n := r.started.Load(); n != 0 {
		t.Fatalf("%d outcome reports, want none", n)
	}
}

// Each kind of path is reported as what it is, with both NAT types and the
// time it took, and the server takes the report.
func TestOutcomeClassification(t *testing.T) {
	type world struct {
		host *hostd.Host
		opts Options
	}
	symmetricPair := func(t *testing.T, w *vnetWorld) world {
		h := w.net.NAT("203.0.113.20", vnet.Symmetric).Host("10.0.0.20")
		host := startHost(t, hostd.Config{Signal: vnetSignal, PacketConn: h.MustListen(0),
			Locals: func() []netip.Addr { return []netip.Addr{h.Addr()} }})
		opts := w.options(w.net.NAT("203.0.113.10", vnet.Symmetric).Host("192.168.1.10"), host.Password())
		opts.PunchTime = 0
		return world{host, opts}
	}
	cone := func(w *vnetWorld) *vnet.Host {
		return w.net.NAT("203.0.113.10", vnet.PortRestrictedCone).Host("192.168.1.10")
	}
	cases := []struct {
		name          string
		setup         func(t *testing.T) world
		path          string
		nat, peerNAT  nat.Type
		wantDialError bool
	}{
		{"lan", func(t *testing.T) world {
			w := newVnetWorld(t)
			box := w.net.NAT("203.0.113.10", vnet.PortRestrictedCone)
			host := w.host(box.Host("192.168.1.20"))
			return world{host, w.options(box.Host("192.168.1.10"), host.Password())}
		}, signal.PathLAN, nat.TypeCone, nat.TypeCone, false},
		{"direct", func(t *testing.T) world {
			w := newVnetWorld(t)
			host := w.host(w.net.Host("192.0.2.20"))
			return world{host, w.options(cone(w), host.Password())}
		}, signal.PathDirect, nat.TypeCone, nat.TypeNone, false},
		{"punched", func(t *testing.T) world {
			w := newVnetWorld(t)
			host := w.host(w.net.NAT("203.0.113.20", vnet.PortRestrictedCone).Host("10.0.0.20"))
			return world{host, w.options(cone(w), host.Password())}
		}, signal.PathPunched, nat.TypeCone, nat.TypeCone, false},
		{"predicted", func(t *testing.T) world {
			w := newVnetWorld(t)
			host := w.host(w.net.NAT("203.0.113.20", vnet.Symmetric).Host("10.0.0.20"))
			waitFor(t, func() bool { return host.Status().NAT != string(nat.TypeUnknown) })
			return world{host, w.options(cone(w), host.Password())}
		}, signal.PathPredicted, nat.TypeCone, nat.TypeSymmetric, false},
		{"mapped", func(t *testing.T) world {
			// A host behind a symmetric NAT whose ports cannot be
			// predicted: only the port its router forwards reaches it.
			w := newVnetWorld(t)
			box := w.net.NAT("203.0.113.20", vnet.Symmetric)
			box.SetRandomPorts(true)
			hh := box.Host("10.0.0.20")
			pc := hh.MustListen(0)
			fwd := box.Forward(41641, pc.LocalAddr().(*net.UDPAddr).AddrPort())
			host := startHost(t, hostd.Config{Signal: vnetSignal, PacketConn: pc,
				Locals: func() []netip.Addr { return []netip.Addr{hh.Addr()} },
				Mapped: func() netip.AddrPort { return fwd }})
			waitFor(t, func() bool { return host.Status().NAT != string(nat.TypeUnknown) })
			return world{host, w.options(cone(w), host.Password())}
		}, signal.PathMapped, nat.TypeCone, nat.TypeSymmetric, false},
		{"relayed", func(t *testing.T) world {
			return symmetricPair(t, relayWorld(t, &signal.RelayConfig{}))
		}, signal.PathRelayed, nat.TypeSymmetric, nat.TypeSymmetric, false},
		{"failed", func(t *testing.T) world {
			return symmetricPair(t, newVnetWorld(t))
		}, signal.PathFailed, nat.TypeSymmetric, nat.TypeSymmetric, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := recordOutcomes(t, nil)
			w := tc.setup(t)
			start := time.Now()
			conn, err := Dial(testCtx(t), w.host.ID(), w.opts)
			took := time.Since(start)
			if tc.wantDialError {
				if !errors.Is(err, nat.ErrNoPath) {
					t.Fatalf("err = %v, want no path", err)
				}
			} else {
				if err != nil {
					t.Fatalf("dial: %v", err)
				}
				defer conn.Close()
				echo(t, conn)
			}
			s := rec.next(t)
			if s.err != nil {
				t.Errorf("report not taken: %v", s.err)
			}
			o := s.o
			if o.Path != tc.path || o.NAT != string(tc.nat) || o.PeerNAT != string(tc.peerNAT) {
				t.Errorf("reported %+v, want path %s, NATs %s/%s", o, tc.path, tc.nat, tc.peerNAT)
			}
			if o.MS <= 0 || o.MS > took.Milliseconds() {
				t.Errorf("reported %d ms for a dial of %s", o.MS, took)
			}
			rec.only(t)
		})
	}
}

// only checks that no further report follows the one received.
func (r *outcomeRecorder) only(t *testing.T) {
	t.Helper()
	time.Sleep(200 * time.Millisecond)
	if n := r.started.Load(); n != 1 {
		t.Fatalf("%d outcome reports, want one", n)
	}
}

// A dial through the websocket bridge is reported as bridged, on the
// bridge connection.
func TestOutcomeBridged(t *testing.T) {
	old := signal.FallbackAfter
	signal.FallbackAfter = 300 * time.Millisecond
	t.Cleanup(func() { signal.FallbackAfter = old })
	rec := recordOutcomes(t, nil)
	sig, url := wsSignal(t)
	h := startHost(t, hostd.Config{Signal: sig, SignalOptions: signal.Options{WS: url}, Password: "ws-test-password"})
	opts := Options{Signal: sig, SignalWS: url, Password: "ws-test-password", Dir: t.TempDir(), Locals: noLocals,
		Listen: func() (net.PacketConn, error) { return newBlockedUDP(), nil }}
	conn, err := Dial(testCtx(t), h.ID(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if s := rec.next(t); s.err != nil || s.o.Path != signal.PathBridged {
		t.Fatalf("reported %+v (%v), want bridged", s.o, s.err)
	}
}

// A relayed session that moves to a direct path reports that too, with the
// time since the session started.
func TestOutcomeUpgraded(t *testing.T) {
	fastUpgrades(t)
	rec := recordOutcomes(t, nil)
	_, host, opts, open := relayedPair(t)
	conn, err := Dial(testCtx(t), host.ID(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if s := rec.next(t); s.o.Path != signal.PathRelayed {
		t.Fatalf("first report %+v, want relayed", s.o)
	}
	start := time.Now()
	open.Store(true)
	s := rec.next(t)
	if s.err != nil || s.o.Path != signal.PathUpgraded || s.o.NAT != string(nat.TypeCone) || s.o.PeerNAT != string(nat.TypeCone) {
		t.Fatalf("second report %+v (%v), want upgraded between cones", s.o, s.err)
	}
	if s.o.MS <= 0 || s.o.MS > time.Since(start).Milliseconds()+10_000 {
		t.Errorf("upgraded after %d ms", s.o.MS)
	}
	if InfoOf(conn).Path == PathRelayed {
		t.Errorf("reported an upgrade but the session is still relayed")
	}
}

// A server that does not take reports is not asked again, by any dialer
// in the process; another server still is.
func TestOutcomeServerWithoutReports(t *testing.T) {
	rec := recordOutcomes(t, func(context.Context, *signal.Client, signal.Outcome) error { return signal.ErrNoOutcomes })
	w := newVnetWorld(t)
	host := w.host(w.net.NAT("203.0.113.20", vnet.PortRestrictedCone).Host("10.0.0.20"))
	for i := range 2 {
		opts := w.options(w.net.NAT(netip.AddrFrom4([4]byte{203, 0, 113, byte(10 + i)}).String(), vnet.PortRestrictedCone).Host("192.168.1.10"), host.Password())
		conn, err := Dial(testCtx(t), host.ID(), opts)
		if err != nil {
			t.Fatal(err)
		}
		conn.Close()
		if i == 0 {
			if s := rec.next(t); !errors.Is(s.err, signal.ErrNoOutcomes) {
				t.Fatalf("first report: %v", s.err)
			}
		}
	}
	time.Sleep(200 * time.Millisecond)
	if n := rec.started.Load(); n != 1 {
		t.Fatalf("%d reports to a server that takes none, want only the first", n)
	}

	w2 := newVnetWorld(t)
	host2 := w2.host(w2.net.NAT("203.0.113.20", vnet.PortRestrictedCone).Host("10.0.0.20"))
	conn, err := Dial(testCtx(t), host2.ID(), w2.options(w2.net.NAT("203.0.113.10", vnet.PortRestrictedCone).Host("192.168.1.10"), host2.Password()))
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	rec.next(t)
}

// REMOHOP_OUTCOMES=0 and the outcomes setting turn reports off.
func TestOutcomesOptOut(t *testing.T) {
	rec := recordOutcomes(t, nil)
	w := newVnetWorld(t)
	host := w.host(w.net.NAT("203.0.113.20", vnet.PortRestrictedCone).Host("10.0.0.20"))
	dial := func(n int, prepare func(o *Options)) {
		t.Helper()
		opts := w.options(w.net.NAT(netip.AddrFrom4([4]byte{203, 0, 113, byte(10 + n)}).String(), vnet.PortRestrictedCone).Host("192.168.1.10"), host.Password())
		prepare(&opts)
		conn, err := Dial(testCtx(t), host.ID(), opts)
		if err != nil {
			t.Fatal(err)
		}
		conn.Close()
	}

	for _, v := range []string{"0", "off", "no", "false"} {
		t.Setenv("REMOHOP_OUTCOMES", v)
		if OutcomesAllowed(t.TempDir()) {
			t.Errorf("REMOHOP_OUTCOMES=%s allows reports", v)
		}
	}
	t.Setenv("REMOHOP_OUTCOMES", "0")
	dial(0, func(*Options) {})
	rec.none(t)

	t.Setenv("REMOHOP_OUTCOMES", "")
	dial(1, func(o *Options) {
		if err := os.WriteFile(filepath.Join(o.Dir, "config.json"), []byte(`{"outcomes":"off"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if OutcomesAllowed(o.Dir) {
			t.Error("the outcomes setting does not turn reports off")
		}
	})
	rec.none(t)

	dial(2, func(o *Options) {
		if !OutcomesAllowed(o.Dir) {
			t.Error("reports are off by default")
		}
	})
	rec.next(t)
}

// A server that never answers a report holds up neither the session nor a
// failure: the report gives up on its own, in the background.
func TestOutcomeNeverDelaysTheDial(t *testing.T) {
	released := make(chan struct{}, 4)
	rec := recordOutcomes(t, func(ctx context.Context, _ *signal.Client, _ signal.Outcome) error {
		<-ctx.Done()
		released <- struct{}{}
		return ctx.Err()
	})
	w := newVnetWorld(t)
	host := w.host(w.net.NAT("203.0.113.20", vnet.PortRestrictedCone).Host("10.0.0.20"))
	conn, err := Dial(testCtx(t), host.ID(), w.options(w.net.NAT("203.0.113.10", vnet.PortRestrictedCone).Host("192.168.1.10"), host.Password()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	select {
	case <-released:
		t.Fatal("the dial waited for its report")
	default:
	}
	echo(t, conn)
	start := time.Now()
	if s := rec.next(t); !errors.Is(s.err, context.DeadlineExceeded) {
		t.Fatalf("report ended with %v", s.err)
	}
	if took := time.Since(start); took > outcomeTimeout+time.Second {
		t.Errorf("an unanswered report took %s more to give up", took)
	}
	<-released

	// The same for a dial that fails.
	sym := w.net.NAT("203.0.113.30", vnet.Symmetric)
	h2 := startHost(t, hostd.Config{Signal: vnetSignal, PacketConn: sym.Host("10.0.0.30").MustListen(0),
		Locals: noLocals})
	opts := w.options(w.net.NAT("203.0.113.11", vnet.Symmetric).Host("192.168.1.10"), h2.Password())
	opts.PunchTime = 0
	if _, err := Dial(testCtx(t), h2.ID(), opts); !errors.Is(err, nat.ErrNoPath) {
		t.Fatalf("err = %v", err)
	}
	select {
	case <-released:
		t.Fatal("the failed dial waited for its report")
	default:
	}
	if s := rec.next(t); s.o.Path != signal.PathFailed {
		t.Fatalf("reported %+v", s.o)
	}
}

// outcomePath and mappedCandidate on what a host can advertise.
func TestOutcomePath(t *testing.T) {
	ap := netip.MustParseAddrPort
	reflexive := ap("203.0.113.20:40001")
	behindNAT := signal.Peer{Reflexive: reflexive, NAT: nat.TypeCone,
		Local: []netip.AddrPort{ap("203.0.113.20:41641"), ap("10.0.0.20:5000")}}
	samePort := signal.Peer{Reflexive: reflexive, NAT: nat.TypeCone,
		Local: []netip.AddrPort{reflexive, ap("10.0.0.20:5000")}}
	public := signal.Peer{Reflexive: ap("192.0.2.20:5000"), NAT: nat.TypeNone, Local: []netip.AddrPort{ap("192.0.2.20:5000")}}
	unlisted := signal.Peer{Reflexive: ap("192.0.2.20:5000"), NAT: nat.TypeNone}
	unknown := signal.Peer{Reflexive: ap("192.0.2.20:5000"), NAT: nat.TypeUnknown, Local: []netip.AddrPort{ap("192.0.2.20:5000")}}
	for _, c := range []struct {
		name string
		path Path
		win  nat.Punched
		host signal.Peer
		want string
	}{
		{"lan", PathLAN, nat.Punched{Remote: ap("10.0.0.20:5000")}, behindNAT, signal.PathLAN},
		{"tailnet", PathTailnet, nat.Punched{Remote: ap("100.100.1.2:5000")}, behindNAT, signal.PathLAN},
		{"mapped", PathDirect, nat.Punched{Remote: ap("203.0.113.20:41641")}, behindNAT, signal.PathMapped},
		{"mapped on the NAT's own port", PathDirect, nat.Punched{Remote: reflexive}, samePort, signal.PathMapped},
		{"punched", PathPunched, nat.Punched{Remote: reflexive}, behindNAT, signal.PathPunched},
		{"predicted", PathPunched, nat.Punched{Remote: ap("203.0.113.20:40003"), Predicted: true}, behindNAT, signal.PathPredicted},
		{"public interface", PathDirect, nat.Punched{Remote: ap("192.0.2.20:5000")}, public, signal.PathDirect},
		{"public, not listed", PathPunched, nat.Punched{Remote: ap("192.0.2.20:5000")}, unlisted, signal.PathDirect},
		{"public, NAT not measured", PathDirect, nat.Punched{Remote: ap("192.0.2.20:5000")}, unknown, signal.PathDirect},
	} {
		if got := outcomePath(c.path, c.win, c.host); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}
