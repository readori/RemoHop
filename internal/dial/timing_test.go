package dial

import (
	"net"
	"net/netip"
	"os"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/hostd"
	"github.com/readori/RemoHop/internal/nat"
	"github.com/readori/RemoHop/internal/nat/vnet"
	"github.com/readori/RemoHop/internal/signal"
)

// TestTimingRelayed prints how long dials that end on the relay take at a
// 40 ms round trip, next to what they took before the relay was raced
// against the first punch attempt (two cone NATs whose punch cannot work)
// and started at once between two symmetric NATs. It asserts nothing
// about time and runs only on request:
//
//	REMOHOP_BENCH_REPORT=1 go test ./internal/dial -run TestTimingRelayed -v -count=1
func TestTimingRelayed(t *testing.T) {
	if os.Getenv("REMOHOP_BENCH_REPORT") == "" {
		t.Skip("set REMOHOP_BENCH_REPORT=1 to print timings")
	}
	for _, tc := range []struct {
		name         string
		client, host vnet.NATKind
		block        bool // no UDP between the peers, only to the server
		before       string
	}{
		{"two cone NATs, peer-to-peer UDP blocked", vnet.PortRestrictedCone, vnet.PortRestrictedCone, true, "6.2 s"},
		{"two symmetric NATs", vnet.Symmetric, vnet.Symmetric, false, "1.74 s"},
	} {
		w := relayWorld(t, &signal.RelayConfig{})
		hb := w.net.NAT("203.0.113.20", tc.host).Host("10.0.0.20")
		host := startHost(t, hostd.Config{Signal: vnetSignal, PacketConn: hb.MustListen(0),
			Locals: func() []netip.Addr { return []netip.Addr{hb.Addr()} }})
		waitFor(t, func() bool { return host.Status().NAT != string(nat.TypeUnknown) })
		w.net.SetLatency(20 * time.Millisecond)
		ch := w.net.NAT("203.0.113.10", tc.client).Host("192.168.1.10")
		opts := w.options(ch, host.Password())
		opts.PacketConn, opts.PunchTime = nil, 0
		opts.Listen = func() (net.PacketConn, error) {
			pc, err := ch.Listen(0)
			if tc.block {
				return serverOnly{pc}, err
			}
			return pc, err
		}
		start := time.Now()
		conn, err := Dial(testCtx(t), host.ID(), opts)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s, 40 ms round trip: %s after %s (before: %s)", tc.name, InfoOf(conn).Path,
			time.Since(start).Round(10*time.Millisecond), tc.before)
		conn.Close()
	}
}
