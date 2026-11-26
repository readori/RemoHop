package nat_test

import (
	"context"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/nat"
	"github.com/readori/RemoHop/internal/nat/vnet"
)

// ProbeNAT sees how a symmetric NAT hands out its ports.
func TestProbeNATPrediction(t *testing.T) {
	for _, tc := range []struct {
		name  string
		kind  vnet.NATKind
		setup func(*vnet.NAT)
		step  int // 0: no prediction
	}{
		{"sequential", vnet.Symmetric, nil, 1},
		{"step 2", vnet.Symmetric, step2, 2},
		{"counting down", vnet.Symmetric, stepDown, -3},
		{"step too large", vnet.Symmetric, func(b *vnet.NAT) { b.SetPortStep(40) }, 0},
		{"random ports", vnet.Symmetric, randomPort, 0},
		{"port-restricted cone", vnet.PortRestrictedCone, nil, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := newWorld(t)
			box := w.net.NAT("203.0.113.10", tc.kind)
			if tc.setup != nil {
				tc.setup(box)
			}
			p := w.peer(box.Host("192.168.1.10"))
			pred, ok := p.sock.Prediction()
			if tc.step == 0 {
				if ok || p.predicted.Valid() {
					t.Fatalf("prediction %+v for a NAT without one", pred)
				}
				return
			}
			// The main port's mapping was made first, the second port's
			// next; that one is the last port seen.
			want := nat.Prediction{IP: p.reflexive.Addr(), Last: uint16(int(p.reflexive.Port()) + tc.step), Step: tc.step}
			if !ok || pred != want || p.predicted != want {
				t.Fatalf("prediction %+v (read back %+v), want %+v", pred, p.predicted, want)
			}
		})
	}
}

// A host's measurement is brought up to date once the NAT has forgotten
// the mapping it was made with.
func TestRefreshPrediction(t *testing.T) {
	w := newWorld(t)
	box := w.net.NAT("203.0.113.20", vnet.Symmetric)
	p := w.peer(box.Host("10.0.0.20"))
	before, ok := p.sock.Prediction()
	if !ok {
		t.Fatal("no prediction")
	}
	box.Burn(200)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	p.sock.RefreshPrediction(ctx)
	if now, _ := p.sock.Prediction(); now != before {
		t.Fatalf("prediction changed to %+v while the old mapping was still there", now)
	}
	box.Expire(reflectAlt)
	p.sock.RefreshPrediction(ctx)
	want := before
	want.Last += 201 // 200 ports burnt, the next one for the new mapping
	if now, _ := p.sock.Prediction(); now != want {
		t.Fatalf("refreshed prediction %+v, want %+v", now, want)
	}
}

// What a peer can make us probe through a prediction: at most MaxPredicted
// ports on the one address it was observed at, a few passes over them, and
// nothing at all for anything else.
func TestPredictedProbeBound(t *testing.T) {
	const peerIP = "203.0.113.20"
	observed := netip.MustParseAddrPort(peerIP + ":40000")
	cases := []struct {
		name   string
		remote []netip.AddrPort
		pred   nat.Prediction
		// symmetric puts the prober behind a symmetric NAT instead of
		// on a public address.
		symmetric bool
		sweep     bool
	}{
		{"honest", []netip.AddrPort{observed}, nat.Prediction{IP: observed.Addr(), Last: 40000, Step: 16}, false, true},
		{"another address", []netip.AddrPort{observed}, nat.Prediction{IP: netip.MustParseAddr("192.0.2.99"), Last: 40000, Step: 1}, false, false},
		{"private address", []netip.AddrPort{observed, netip.MustParseAddrPort("10.9.9.9:1")}, nat.Prediction{IP: netip.MustParseAddr("10.9.9.9"), Last: 1, Step: 1}, false, false},
		{"stride too large", []netip.AddrPort{observed}, nat.Prediction{IP: observed.Addr(), Last: 40000, Step: 17}, false, false},
		{"from a symmetric NAT", []netip.AddrPort{observed}, nat.Prediction{IP: observed.Addr(), Last: 40000, Step: 1}, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := newWorld(t)
			var prober *peer
			if tc.symmetric {
				prober = w.behind("192.0.2.50", "192.168.1.10", vnet.Symmetric)
			} else {
				prober = w.peer(w.net.Host("192.0.2.50"))
			}
			var mu sync.Mutex
			sent := map[netip.AddrPort]int{}
			w.net.SetTap(func(src, dst netip.AddrPort, _ []byte) {
				if src.Addr() == netip.MustParseAddr("192.0.2.50") && dst.Addr() != netip.MustParseAddr(serverIP) {
					mu.Lock()
					sent[dst]++
					mu.Unlock()
				}
			})
			// The client role: no hold, no lead, the sweep starts at once.
			sess, err := prober.sock.PunchWith(nat.PunchConfig{Nonce: newNonce(), Role: nat.RoleClient, Remote: tc.remote,
				Lifetime: 4 * time.Second, Predicted: tc.pred})
			if err != nil {
				t.Fatal(err)
			}
			defer sess.Close()
			wait := 1500 * time.Millisecond
			if tc.sweep {
				wait = 3500 * time.Millisecond // all passes
			}
			time.Sleep(wait)

			mu.Lock()
			defer mu.Unlock()
			allowed := map[netip.AddrPort]bool{}
			for _, r := range tc.remote {
				allowed[r] = true
			}
			predicted, packets := 0, 0
			for dst, n := range sent {
				if allowed[dst] {
					continue
				}
				k := (int(dst.Port()) - int(tc.pred.Last)) / tc.pred.Step
				if !tc.sweep || dst.Addr() != tc.pred.IP || k < 1 || k > nat.MaxPredicted || tc.pred.Port(k) != dst.Port() {
					t.Errorf("probed %s (%d times), which is neither a candidate nor in the predicted window", dst, n)
					continue
				}
				predicted++
				packets += n
			}
			if tc.sweep && predicted != nat.MaxPredicted {
				t.Errorf("%d predicted ports probed, want %d", predicted, nat.MaxPredicted)
			}
			if packets > nat.PredictSweeps*nat.MaxPredicted {
				t.Errorf("%d probes to predicted ports, want at most %d", packets, nat.PredictSweeps*nat.MaxPredicted)
			}
		})
	}
}
