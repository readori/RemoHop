package nat

import (
	"net/netip"
	"slices"
	"testing"
)

func TestPredictionFrom(t *testing.T) {
	ip := "203.0.113.7"
	at := func(port uint16) netip.AddrPort { return netip.AddrPortFrom(netip.MustParseAddr(ip), port) }
	for _, tc := range []struct {
		name          string
		first, second netip.AddrPort
		between       int
		ok            bool
		last          uint16
		step          int
	}{
		{"step 1", at(40000), at(40001), 0, true, 40001, 1},
		{"step 2", at(40000), at(40002), 0, true, 40002, 2},
		{"counting down", at(40000), at(39997), 0, true, 39997, -3},
		{"largest step", at(1000), at(1016), 0, true, 1016, 16},
		{"largest step down", at(1016), at(1000), 0, true, 1000, -16},
		{"wrapping up past 65535", at(65535), at(1), 0, true, 1, 2},
		{"wrapping down past 0", at(2), at(65534), 0, true, 65534, -4},
		{"same port is no stride (a cone)", at(40000), at(40000), 0, false, 0, 0},
		{"step too large", at(40000), at(40017), 0, false, 0, 0},
		{"step too large down", at(40017), at(40000), 0, false, 0, 0},
		{"far apart (random)", at(40000), at(12345), 0, false, 0, 0},
		{"two addresses", at(40000), netip.MustParseAddrPort("203.0.113.8:40001"), 0, false, 0, 0},
		{"one unseen mapping in between", at(40000), at(40004), 1, true, 40004, 2},
		{"unseen mapping, uneven", at(40000), at(40003), 1, false, 0, 0},
	} {
		p, ok := predictionFrom(tc.first, tc.second, tc.between)
		if ok != tc.ok || (ok && (p.Last != tc.last || p.Step != tc.step || p.IP != tc.first.Addr())) {
			t.Errorf("%s: predictionFrom(%s, %s, %d) = %+v, %v; want last %d step %d ok %v",
				tc.name, tc.first, tc.second, tc.between, p, ok, tc.last, tc.step, tc.ok)
		}
	}
	if (Prediction{IP: netip.MustParseAddr("2001:db8::1"), Last: 1, Step: 1}).Valid() {
		t.Error("an IPv6 prediction is valid")
	}
}

func TestPredictionTargets(t *testing.T) {
	p := Prediction{IP: netip.MustParseAddr("203.0.113.7"), Last: 65533, Step: 1}
	got := p.targets(4)
	var ports []uint16
	for _, ap := range got {
		ports = append(ports, ap.Port())
	}
	// Port 0 is skipped, the window still has four ports.
	if !slices.Equal(ports, []uint16{65534, 65535, 1, 2}) {
		t.Errorf("targets = %v", ports)
	}
	down := Prediction{IP: p.IP, Last: 100, Step: -16}
	for i, ap := range down.targets(maxPredicted) {
		if want := uint16(100 - 16*(i+1)); ap.Port() != want { // counting down past 0 wraps
			t.Fatalf("target %d = %s", i, ap)
		}
	}
	if n := len(down.targets(maxPredicted)); n != maxPredicted {
		t.Errorf("%d targets, want %d", n, maxPredicted)
	}
}

func TestHint(t *testing.T) {
	refl := netip.MustParseAddrPort("203.0.113.7:40000")
	lan := netip.MustParseAddrPort("192.168.1.10:5000")
	v6 := netip.MustParseAddrPort("[2001:db8::7]:5000")
	for _, p := range []Prediction{
		{IP: refl.Addr(), Last: 40001, Step: 1},
		{IP: refl.Addr(), Last: 40002, Step: 2},
		{IP: refl.Addr(), Last: 39990, Step: -5},
		{IP: refl.Addr(), Last: 65530, Step: 16},
	} {
		hint := p.Hint()
		if len(hint) != 2 {
			t.Fatalf("hint of %+v: %v", p, hint)
		}
		local := append([]netip.AddrPort{lan, v6}, hint...)
		got, rest := ReadHint(refl, local)
		if got != p || !slices.Equal(rest, []netip.AddrPort{lan, v6}) {
			t.Errorf("ReadHint(%v) = %+v, %v; want %+v", local, got, rest, p)
		}
	}
	// A guess that would fall on port 0 is not advertised.
	if h := (Prediction{IP: refl.Addr(), Last: 65534, Step: 1}).Hint(); h != nil {
		t.Errorf("hint across port 0: %v", h)
	}

	at := func(s string) netip.AddrPort { return netip.MustParseAddrPort(s) }
	for name, local := range map[string][]netip.AddrPort{
		"none":                   {lan},
		"empty":                  nil,
		"another address":        {at("198.51.100.9:40002"), at("198.51.100.9:40003")},
		"one on another address": {at("203.0.113.7:40002"), at("198.51.100.9:40003")},
		"private address":        {at("192.168.1.10:40002"), at("192.168.1.10:40003")},
		"stride too large":       {at("203.0.113.7:40002"), at("203.0.113.7:40019")},
		"same port twice":        {at("203.0.113.7:40002"), at("203.0.113.7:40002")},
		"the observed address":   {at("203.0.113.7:39999"), refl},
		"not at the end":         {at("203.0.113.7:40002"), at("203.0.113.7:40003"), lan},
	} {
		got, rest := ReadHint(refl, local)
		if got.Valid() || !slices.Equal(rest, local) {
			t.Errorf("%s: ReadHint(%v) = %+v, %v; want nothing", name, local, got, rest)
		}
	}
	// Over IPv6 there are no predictions.
	refl6 := at("[2001:db8::7]:40000")
	if got, _ := ReadHint(refl6, []netip.AddrPort{at("[2001:db8::7]:40002"), at("[2001:db8::7]:40003")}); got.Valid() {
		t.Errorf("IPv6 hint read: %+v", got)
	}
}
