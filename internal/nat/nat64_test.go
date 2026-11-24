package nat

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync"
	"syscall"
	"testing"
	"time"
)

// RFC 6052 section 2.4's examples, 192.0.2.33 under each prefix length.
var rfc6052 = []struct{ prefix, addr string }{
	{"2001:db8::/32", "2001:db8:c000:221::"},
	{"2001:db8:100::/40", "2001:db8:1c0:2:21::"},
	{"2001:db8:122::/48", "2001:db8:122:c000:2:2100::"},
	{"2001:db8:122:300::/56", "2001:db8:122:3c0:0:221::"},
	{"2001:db8:122:344::/64", "2001:db8:122:344:c0:2:2100:0"},
	{"2001:db8:122:344::/96", "2001:db8:122:344::192.0.2.33"},
	{"64:ff9b::/96", "64:ff9b::192.0.2.33"},
}

func TestSynthesizeAndExtractRFC6052(t *testing.T) {
	v4 := netip.MustParseAddr("192.0.2.33")
	for _, c := range rfc6052 {
		p := netip.MustParsePrefix(c.prefix)
		want := netip.MustParseAddr(c.addr)
		got, ok := SynthesizeNAT64(p, v4)
		if !ok || got != want {
			t.Errorf("Synthesize(%s, %s) = %s, %v; want %s", p, v4, got, ok, want)
		}
		back, ok := ExtractNAT64(p, want)
		if !ok || back != v4 {
			t.Errorf("Extract(%s, %s) = %s, %v; want %s", p, want, back, ok, v4)
		}
		// A v4-mapped input is the same IPv4 address.
		if got, _ := SynthesizeNAT64(p, netip.AddrFrom16(v4.As16())); got != want {
			t.Errorf("Synthesize with a mapped address = %s", got)
		}
	}
	if _, ok := SynthesizeNAT64(netip.MustParsePrefix("64:ff9b::/80"), v4); ok {
		t.Error("an invalid prefix length was accepted")
	}
	if _, ok := SynthesizeNAT64(netip.MustParsePrefix("64:ff9b::/96"), netip.MustParseAddr("2001:db8::1")); ok {
		t.Error("an IPv6 address was synthesized")
	}
	if _, ok := ExtractNAT64(netip.MustParsePrefix("64:ff9b::/96"), netip.MustParseAddr("2001:db8::1")); ok {
		t.Error("an address outside the prefix was extracted")
	}
	// Bits 64-71 must be zero for prefixes shorter than /96.
	if _, ok := ExtractNAT64(netip.MustParsePrefix("2001:db8::/32"), netip.MustParseAddr("2001:db8:c000:221:ff00::")); ok {
		t.Error("an address with a non-zero u-octet was extracted")
	}
}

func TestNAT64PrefixFrom(t *testing.T) {
	for _, c := range rfc6052 {
		p := netip.MustParsePrefix(c.prefix)
		for _, w := range []netip.Addr{wka1, wka2} {
			a, _ := SynthesizeNAT64(p, w)
			got, ok := NAT64PrefixFrom([]netip.Addr{netip.MustParseAddr("2001:db8:ffff::1"), a})
			if !ok || got != p.Masked() {
				t.Errorf("prefix from %s = %s, %v; want %s", a, got, ok, p)
			}
		}
	}
	none := [][]netip.Addr{
		nil,
		{netip.MustParseAddr("2001:db8::1")}, // a real AAAA record, not synthesized
		{netip.MustParseAddr("192.0.0.170")}, // IPv4 itself
		{netip.MustParseAddr("::ffff:192.0.0.170")},       // mapped, not synthesized
		{netip.MustParseAddr("64:ff9b::c000:ac")},         // 192.0.0.172: not well known
		{netip.MustParseAddr("2001:db8:c000:aa:ff00::")},  // /32 with a non-zero u-octet
		{netip.MustParseAddr("2001:db8:c000:aa:0:0:0:1")}, // /32 with a non-zero suffix
	}
	for i, addrs := range none {
		if p, ok := NAT64PrefixFrom(addrs); ok {
			t.Errorf("case %d (%v): found prefix %s", i, addrs, p)
		}
	}
	// 64:ff9b::1:c000:aa embeds 192.0.0.170 at /96 under 64:ff9b::1:0:0/96.
	if p, ok := NAT64PrefixFrom([]netip.Addr{netip.MustParseAddr("64:ff9b::1:c000:aa")}); !ok || p != netip.MustParsePrefix("64:ff9b::1:0:0/96") {
		t.Errorf("64:ff9b::1:c000:aa: %s %v", p, ok)
	}
}

// withLookup replaces the resolver for one test and forgets the cache.
func withLookup(t *testing.T, f func(context.Context) ([]netip.Addr, error)) *int {
	t.Helper()
	calls := new(int)
	var mu sync.Mutex
	old := nat64Lookup
	nat64Lookup = func(ctx context.Context) ([]netip.Addr, error) {
		mu.Lock()
		*calls++
		mu.Unlock()
		return f(ctx)
	}
	forgetNAT64()
	t.Cleanup(func() { nat64Lookup = old; forgetNAT64() })
	return calls
}

func dns64(prefix string) func(context.Context) ([]netip.Addr, error) {
	return func(context.Context) ([]netip.Addr, error) {
		p := netip.MustParsePrefix(prefix)
		a, _ := SynthesizeNAT64(p, wka1)
		b, _ := SynthesizeNAT64(p, wka2)
		return []netip.Addr{a, b}, nil
	}
}

// fakeNet is a packet connection on a pretend network: v4 says whether
// IPv4 sends work; IPv6 sends are recorded.
type fakeNet struct {
	mu    sync.Mutex
	v4    bool
	sent  []netip.AddrPort
	inbox chan fakePkt
}

type fakePkt struct {
	from netip.AddrPort
	p    []byte
}

func newFakeNet(v4 bool) *fakeNet { return &fakeNet{v4: v4, inbox: make(chan fakePkt, 8)} }

func (f *fakeNet) WriteTo(p []byte, addr net.Addr) (int, error) {
	ap := addr.(*net.UDPAddr).AddrPort()
	f.mu.Lock()
	defer f.mu.Unlock()
	if ap.Addr().Unmap().Is4() && !f.v4 {
		return 0, &net.OpError{Op: "write", Net: "udp", Err: &net.AddrError{}, Addr: addr, Source: nil}
	}
	f.sent = append(f.sent, normalize(ap))
	return len(p), nil
}

func (f *fakeNet) ReadFrom(p []byte) (int, net.Addr, error) {
	m := <-f.inbox
	return copy(p, m.p), net.UDPAddrFromAddrPort(m.from), nil
}

func (f *fakeNet) Close() error                       { return nil }
func (f *fakeNet) LocalAddr() net.Addr                { return &net.UDPAddr{IP: net.IPv6unspecified, Port: 4242} }
func (f *fakeNet) SetDeadline(t time.Time) error      { return nil }
func (f *fakeNet) SetReadDeadline(t time.Time) error  { return nil }
func (f *fakeNet) SetWriteDeadline(t time.Time) error { return nil }

// unreachableNet fails IPv4 sends the way an IPv6-only network does.
type unreachableNet struct{ *fakeNet }

func (u unreachableNet) WriteTo(p []byte, addr net.Addr) (int, error) {
	if addr.(*net.UDPAddr).AddrPort().Addr().Unmap().Is4() && !u.v4 {
		return 0, &net.OpError{Op: "write", Net: "udp", Addr: addr, Err: syscall.ENETUNREACH}
	}
	return u.fakeNet.WriteTo(p, addr)
}

func (f *fakeNet) last() netip.AddrPort {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) == 0 {
		return netip.AddrPort{}
	}
	return f.sent[len(f.sent)-1]
}

var hostV4 = netip.MustParseAddrPort("146.235.209.71:21118")

func TestNAT64NotUsedWhereIPv4Works(t *testing.T) {
	calls := withLookup(t, dns64("64:ff9b::/96"))
	f := newFakeNet(true)
	c := WrapNAT64(unreachableNet{f})
	if _, err := c.WriteTo([]byte("x"), net.UDPAddrFromAddrPort(hostV4)); err != nil {
		t.Fatal(err)
	}
	if f.last() != hostV4 {
		t.Fatalf("sent to %s, want %s", f.last(), hostV4)
	}
	if *calls != 0 {
		t.Fatal("looked up ipv4only.arpa although IPv4 works")
	}
	// IPv6 destinations are never touched.
	v6 := netip.MustParseAddrPort("[2001:db8::5]:443")
	c.WriteTo([]byte("x"), net.UDPAddrFromAddrPort(v6))
	if f.last() != v6 {
		t.Fatalf("IPv6 send went to %s", f.last())
	}
}

func TestNAT64TranslatesOnIPv6Only(t *testing.T) {
	calls := withLookup(t, dns64("64:ff9b::/96"))
	f := newFakeNet(false)
	c := WrapNAT64(unreachableNet{f})
	if _, err := c.WriteTo([]byte("x"), net.UDPAddrFromAddrPort(hostV4)); err != nil {
		t.Fatalf("send to an IPv4 literal on an IPv6-only network: %v", err)
	}
	want := netip.MustParseAddrPort("[64:ff9b::146.235.209.71]:21118")
	if f.last() != want {
		t.Fatalf("sent to %s, want %s", f.last(), want)
	}
	// Later sends go straight to the translated address, without asking
	// the resolver again.
	other := netip.MustParseAddrPort("203.0.113.9:21116")
	c.WriteTo([]byte("y"), net.UDPAddrFromAddrPort(other))
	if f.last() != netip.MustParseAddrPort("[64:ff9b::203.0.113.9]:21116") || *calls != 1 {
		t.Fatalf("second send to %s after %d lookups", f.last(), *calls)
	}
	// Answers from the translated address come up as the IPv4 address.
	f.inbox <- fakePkt{from: want, p: []byte("answer")}
	buf := make([]byte, 16)
	n, from, err := c.ReadFrom(buf)
	if err != nil || string(buf[:n]) != "answer" {
		t.Fatal(n, err)
	}
	if got := normalize(from.(*net.UDPAddr).AddrPort()); got != hostV4 {
		t.Fatalf("answer from %s, want %s", got, hostV4)
	}
	// An IPv6 address nobody sent to in translation (a DNS64 name that was
	// dialled as IPv6) stays IPv6.
	dialled := netip.MustParseAddrPort("[64:ff9b::198.51.100.7]:21116")
	f.inbox <- fakePkt{from: dialled, p: []byte("z")}
	_, from, _ = c.ReadFrom(buf)
	if got := from.(*net.UDPAddr).AddrPort(); got != dialled {
		t.Fatalf("a dialled IPv6 address came up as %s", got)
	}
}

func TestNAT64NoPrefixKeepsTheError(t *testing.T) {
	withLookup(t, func(context.Context) ([]netip.Addr, error) { return nil, errors.New("no such host") })
	c := WrapNAT64(unreachableNet{newFakeNet(false)})
	_, err := c.WriteTo([]byte("x"), net.UDPAddrFromAddrPort(hostV4))
	if !errors.Is(err, syscall.ENETUNREACH) {
		t.Fatalf("without NAT64 the IPv4 error must come through, got %v", err)
	}
}

// Other errors than "no route" are not a reason to translate.
func TestNAT64OnlyForUnreachable(t *testing.T) {
	calls := withLookup(t, dns64("64:ff9b::/96"))
	c := WrapNAT64(newFakeNet(false)) // fails IPv4 with an unrelated error
	if _, err := c.WriteTo([]byte("x"), net.UDPAddrFromAddrPort(hostV4)); err == nil {
		t.Fatal("expected the unrelated error")
	}
	if *calls != 0 {
		t.Fatal("an unrelated error started a NAT64 lookup")
	}
}

// A network-specific prefix (not 64:ff9b::/96), as an operator may use.
func TestNAT64NetworkSpecificPrefix(t *testing.T) {
	withLookup(t, dns64("2001:db8:122::/48"))
	f := newFakeNet(false)
	c := WrapNAT64(unreachableNet{f})
	if _, err := c.WriteTo([]byte("x"), net.UDPAddrFromAddrPort(netip.MustParseAddrPort("192.0.2.33:9"))); err != nil {
		t.Fatal(err)
	}
	if f.last() != netip.MustParseAddrPort("[2001:db8:122:c000:2:2100::]:9") {
		t.Fatalf("sent to %s", f.last())
	}
}

// Back on a network with IPv4 (the translated address has no route), the
// socket goes back to sending IPv4 directly.
func TestNAT64NetworkChangeBackToIPv4(t *testing.T) {
	withLookup(t, dns64("64:ff9b::/96"))
	f := newFakeNet(false)
	u := &switchNet{fakeNet: f}
	c := WrapNAT64(u)
	c.WriteTo([]byte("x"), net.UDPAddrFromAddrPort(hostV4))
	f.mu.Lock()
	f.v4 = true
	f.mu.Unlock()
	u.mu.Lock()
	u.no6 = true
	u.mu.Unlock()
	if _, err := c.WriteTo([]byte("y"), net.UDPAddrFromAddrPort(hostV4)); err != nil {
		t.Fatal(err)
	}
	if f.last() != hostV4 {
		t.Fatalf("after the change sent to %s", f.last())
	}
}

// switchNet: IPv4 as fakeNet says, and IPv6 unreachable once no6 is set.
type switchNet struct {
	*fakeNet
	mu  sync.Mutex
	no6 bool
}

func (s *switchNet) WriteTo(p []byte, addr net.Addr) (int, error) {
	ap := addr.(*net.UDPAddr).AddrPort()
	s.mu.Lock()
	no6 := s.no6
	s.mu.Unlock()
	s.fakeNet.mu.Lock()
	v4 := s.fakeNet.v4
	s.fakeNet.mu.Unlock()
	if ap.Addr().Unmap().Is4() && !v4 || !ap.Addr().Unmap().Is4() && no6 {
		return 0, &net.OpError{Op: "write", Net: "udp", Addr: addr, Err: syscall.ENETUNREACH}
	}
	return s.fakeNet.WriteTo(p, addr)
}
