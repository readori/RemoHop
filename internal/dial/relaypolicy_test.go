package dial

import (
	"context"
	"io"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/hostd"
	"github.com/readori/RemoHop/internal/nat/vnet"
	"github.com/readori/RemoHop/internal/signal"
)

// TestPublicRelayLimits: on a server with a per-address cap and an
// anonymous rate, an anonymous client gets that rate, a third concurrent
// session from its address is refused with an explanation, and closing a
// session frees its slot at once (the client releases it).
func TestPublicRelayLimits(t *testing.T) {
	w := relayWorld(t, &signal.RelayConfig{Rate: 100_000, PerSource: 2})
	hh := w.net.NAT("203.0.113.20", vnet.Symmetric).Host("10.0.0.20")
	host := startHost(t, hostd.Config{Signal: vnetSignal, PacketConn: hh.MustListen(0),
		Locals: func() []netip.Addr { return []netip.Addr{hh.Addr()} }})
	ch := w.net.NAT("203.0.113.10", vnet.Symmetric).Host("192.168.1.10")
	opts := Options{Signal: vnetSignal, Password: host.Password(), Dir: t.TempDir(),
		Listen: func() (net.PacketConn, error) { return ch.Listen(0) },
		Locals: func() []netip.Addr { return []netip.Addr{ch.Addr()} }}

	a, err := Dial(testCtx(t), host.ID(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if InfoOf(a).Path != PathRelayed {
		t.Fatalf("path %s", InfoOf(a).Path)
	}
	// 2 × 150 kB through a 100 kB/s session take well over a second.
	s, err := a.Open(testCtx(t), echoService, nil)
	if err != nil {
		t.Fatal(err)
	}
	msg := make([]byte, 150_000)
	start := time.Now()
	go func() { s.Write(msg); s.CloseWrite() }()
	n, _ := io.Copy(io.Discard, s)
	took := time.Since(start)
	t.Logf("150 kB echoed through the anonymous tier in %s", took.Round(time.Millisecond))
	if n != int64(len(msg)) || took < 1500*time.Millisecond {
		t.Fatalf("echoed %d bytes in %s: not capped at the anonymous rate", n, took)
	}

	b, err := Dial(testCtx(t), host.ID(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	_, err = Dial(testCtx(t), host.ID(), opts)
	if err == nil || !strings.Contains(err.Error(), "already has 2 relayed sessions") {
		t.Fatalf("third session: %v", err)
	}

	// Closing a frees its slot at once, long before the relay's idle
	// timeout.
	a.Close()
	time.Sleep(300 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := Dial(ctx, host.ID(), opts)
	if err != nil {
		t.Fatalf("after closing a session: %v", err)
	}
	c.Close()
}
