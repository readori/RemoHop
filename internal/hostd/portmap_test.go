package hostd

import (
	"net/netip"
	"testing"

	"github.com/readori/RemoHop/internal/nat"
)

// The router's mapping is offered unless the rendezvous server sees the
// host at another public IPv4 address: then another NAT sits in front of
// the router and its "public" address reaches nobody.
func TestOfferMapped(t *testing.T) {
	ext := netip.MustParseAddrPort("203.0.113.5:41641")
	ap := func(s string) *netip.AddrPort { a := netip.MustParseAddrPort(s); return &a }
	for _, c := range []struct {
		reflexive *netip.AddrPort
		want      netip.AddrPort
	}{
		{nil, ext},                     // not registered yet
		{ap("203.0.113.5:61000"), ext}, // the same address, another port: the NAT's own mapping
		{ap("198.51.100.7:61000"), netip.AddrPort{}}, // carrier-grade NAT
		{ap("[2001:db8::1]:61000"), ext},             // registered over IPv6: nothing to compare
	} {
		if got := offerMapped(ext, c.reflexive); got != c.want {
			t.Errorf("reflexive %v: offered %v, want %v", c.reflexive, got, c.want)
		}
	}
}

// A host without a rendezvous server, told not to, or on a socket bound
// to loopback (as every test host is) never asks the router for anything.
func TestNoPortmap(t *testing.T) {
	t.Setenv("REMOHOP_PORTMAP", "")
	lo, err := nat.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer lo.Close()
	for _, cfg := range []Config{
		{Signal: "example.org:21116", NoPortMap: true},
		{Signal: ""},
		{Signal: "example.org:21116"},
	} {
		h := &Host{cfg: cfg, dir: t.TempDir(), sock: lo}
		if pm := h.startPortmap(); pm != nil {
			pm.Close()
			t.Errorf("config %+v started a port mapping", cfg)
		}
	}
}
