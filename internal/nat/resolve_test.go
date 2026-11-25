package nat

import (
	"context"
	"net"
	"net/netip"
	"testing"
	"time"
)

func TestResolveUDP(t *testing.T) {
	ctx := context.Background()
	ua, err := ResolveUDP(ctx, nil, "192.0.2.7:21116")
	if err != nil || ua.AddrPort() != netip.MustParseAddrPort("192.0.2.7:21116") {
		t.Fatalf("literal: %v, %v", ua, err)
	}
	ua, err = ResolveUDP(ctx, nil, "[2001:db8::7]:1")
	if err != nil || ua.AddrPort() != netip.MustParseAddrPort("[2001:db8::7]:1") {
		t.Fatalf("IPv6 literal: %v, %v", ua, err)
	}
	if ua, err := ResolveUDP(ctx, &net.UDPAddr{IP: net.IPv4zero}, "localhost:5"); err != nil || !ua.IP.IsLoopback() || ua.Port != 5 {
		t.Fatalf("localhost: %v, %v", ua, err)
	}
	// The lookup is bounded by the context: a dial's deadline, or the
	// user's Disconnect, ends it.
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	start := time.Now()
	if _, err := ResolveUDP(cctx, nil, "rendezvous.invalid:21116"); err == nil {
		t.Fatal("resolved with a cancelled context")
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("a cancelled lookup took %s", took)
	}
}

func TestOrderFamilies(t *testing.T) {
	in := []netip.Addr{netip.MustParseAddr("2001:db8::1"), netip.MustParseAddr("192.0.2.1"),
		netip.MustParseAddr("2001:db8::2"), netip.MustParseAddr("::ffff:192.0.2.2")}
	got := orderFamilies(in)
	want := []string{"192.0.2.1", "192.0.2.2", "2001:db8::1", "2001:db8::2"}
	for i, w := range want {
		if got[i].String() != w {
			t.Fatalf("orderFamilies = %v, want %v", got, want)
		}
	}
}
