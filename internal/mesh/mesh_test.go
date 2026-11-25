package mesh

import (
	"context"
	"crypto/ed25519"
	"encoding/binary"
	"encoding/json"
	"errors"
	"github.com/readori/RemoHop/internal/peer"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/account"
	"github.com/readori/RemoHop/internal/account/ledger"
	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/nat"
	"golang.org/x/net/dns/dnsmessage"
)

func newID(t *testing.T) *auth.Identity {
	t.Helper()
	id, err := auth.NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// --- addressing ---------------------------------------------------------

func TestAssignDeterministicAndStable(t *testing.T) {
	var ms []Member
	for i := 0; i < 20; i++ {
		ms = append(ms, Member{Key: newID(t).Public(), Name: "dev"})
	}
	p6 := Prefix6([]byte("account"))
	a := Assign(DefaultPrefix4, p6, ms)
	b := Assign(DefaultPrefix4, p6, ms)
	if len(a) != 20 {
		t.Fatalf("%d assignments", len(a))
	}
	seen4, seenL := map[netip.Addr]bool{}, map[string]bool{}
	for i := range a {
		if a[i].Addr4 != b[i].Addr4 || a[i].Addr6 != b[i].Addr6 || a[i].Label != b[i].Label {
			t.Fatal("assignment is not deterministic")
		}
		if !DefaultPrefix4.Contains(a[i].Addr4) || !p6.Contains(a[i].Addr6) {
			t.Fatalf("address outside the prefixes: %v %v", a[i].Addr4, a[i].Addr6)
		}
		last := a[i].Addr4.As4()[3]
		if last == 0 || last == 255 {
			t.Fatalf("address %s ends in .0 or .255", a[i].Addr4)
		}
		if seen4[a[i].Addr4] || seenL[a[i].Label] {
			t.Fatalf("duplicate address or name: %+v", a[i])
		}
		seen4[a[i].Addr4], seenL[a[i].Label] = true, true
	}
	// Same names get suffixes in order.
	if a[0].Label != "dev" || a[1].Label != "dev-2" || a[19].Label != "dev-20" {
		t.Fatalf("labels: %s %s %s", a[0].Label, a[1].Label, a[19].Label)
	}
	// A new member never moves the earlier ones.
	more := append(append([]Member(nil), ms...), Member{Key: newID(t).Public(), Name: "new"})
	c := Assign(DefaultPrefix4, p6, more)
	for i := range a {
		if c[i].Addr4 != a[i].Addr4 {
			t.Fatal("a later member moved an earlier one")
		}
	}
	// The address is the key's own: the same key in another account's
	// order gets the same first choice.
	alone := Assign(DefaultPrefix4, p6, ms[5:6])
	if alone[0].Addr4 != a[5].Addr4 && a[5].Gen == 0 {
		t.Fatal("first-choice address depends on the other members")
	}
}

func TestAssignCollision(t *testing.T) {
	// A /24 with keys until two want the same address.
	p := netip.MustParsePrefix("10.9.9.0/24")
	var ms []Member
	firsts := map[netip.Addr]int{}
	for i := 0; ; i++ {
		k := newID(t).Public()
		a := candidate4(p, k, 0)
		if !a.IsValid() {
			continue
		}
		ms = append(ms, Member{Key: k, Name: "d"})
		if j, dup := firsts[a]; dup {
			got := Assign(p, netip.Prefix{}, ms)
			if got[j].Addr4 != a || got[len(got)-1].Addr4 == a || got[len(got)-1].Gen == 0 {
				t.Fatalf("collision not resolved in favour of the earlier member: %+v %+v", got[j], got[len(got)-1])
			}
			return
		}
		firsts[a] = len(ms) - 1
	}
}

func TestLabelAndPrefix(t *testing.T) {
	for in, want := range map[string]string{
		"Laptop": "laptop", "Anna's MacBook Pro": "anna-s-macbook-pro", "  ": "device", "---": "device",
		"büro-pc": "b-ro-pc", strings.Repeat("x", 80): strings.Repeat("x", 63),
	} {
		if got := Label(in); got != want {
			t.Errorf("Label(%q) = %q, want %q", in, got, want)
		}
	}
	a, b := Prefix6([]byte("one")), Prefix6([]byte("two"))
	if a == b || a.Bits() != 64 || a.Addr().As16()[0] != 0xfd {
		t.Fatalf("Prefix6: %v %v", a, b)
	}
	for _, bad := range []string{"8.8.0.0/16", "100.64.0.0/10", "10.0.0.1/16", "10.0.0.0/30", "fd00::/64", "x"} {
		if _, err := ParsePrefix4(bad); err == nil {
			t.Errorf("ParsePrefix4(%q) accepted", bad)
		}
	}
	if p, err := ParsePrefix4("172.20.0.0/16"); err != nil || p.String() != "172.20.0.0/16" {
		t.Fatal(p, err)
	}
}

// --- packets ------------------------------------------------------------

func ipv4(src, dst string, proto byte, l4 []byte) []byte {
	p := make([]byte, 20+len(l4))
	p[0] = 0x45
	binary.BigEndian.PutUint16(p[2:4], uint16(len(p)))
	p[8] = 64
	p[9] = proto
	s, d := netip.MustParseAddr(src).As4(), netip.MustParseAddr(dst).As4()
	copy(p[12:16], s[:])
	copy(p[16:20], d[:])
	copy(p[20:], l4)
	return p
}

func ipv6(src, dst string, next byte, l4 []byte) []byte {
	p := make([]byte, 40+len(l4))
	p[0] = 0x60
	binary.BigEndian.PutUint16(p[4:6], uint16(len(l4)))
	p[6] = next
	p[7] = 64
	s, d := netip.MustParseAddr(src).As16(), netip.MustParseAddr(dst).As16()
	copy(p[8:24], s[:])
	copy(p[24:40], d[:])
	copy(p[40:], l4)
	return p
}

func tcpSegment(sport, dport uint16, syn bool, mss uint16) []byte {
	t := make([]byte, 24)
	binary.BigEndian.PutUint16(t[0:2], sport)
	binary.BigEndian.PutUint16(t[2:4], dport)
	t[12] = 6 << 4
	if syn {
		t[13] = tcpSYN
	}
	t[20], t[21] = 2, 4
	binary.BigEndian.PutUint16(t[22:24], mss)
	return t
}

func TestParsePacket(t *testing.T) {
	k, ok := parsePacket(ipv4("10.78.1.2", "10.78.3.4", protoTCP, tcpSegment(40000, 22, true, 1460)))
	if !ok || k.v6 || k.proto != protoTCP || !k.ports || k.sport != 40000 || k.dport != 22 || k.src.String() != "10.78.1.2" {
		t.Fatalf("v4 TCP: %+v %v", k, ok)
	}
	k, ok = parsePacket(ipv6("fd00::1", "fd00::2", protoUDP, []byte{0, 53, 0x13, 0x88, 0, 8, 0, 0}))
	if !ok || !k.v6 || k.dport != 5000 || k.sport != 53 {
		t.Fatalf("v6 UDP: %+v %v", k, ok)
	}
	// A hop-by-hop header before ICMPv6 echo.
	hbh := append([]byte{protoICMPv6, 0, 1, 4, 0, 0, 0, 0}, 128, 0, 0, 0, 0, 1, 0, 1)
	k, ok = parsePacket(ipv6("fd00::1", "fd00::2", 0, hbh))
	if !ok || k.proto != protoICMPv6 || !k.isEchoRequest() {
		t.Fatalf("v6 extension header: %+v %v", k, ok)
	}
	// A later fragment has no ports.
	frag := ipv4("10.78.1.2", "10.78.3.4", protoUDP, make([]byte, 16))
	binary.BigEndian.PutUint16(frag[6:8], 100)
	if k, ok = parsePacket(frag); !ok || k.first || k.ports {
		t.Fatalf("fragment: %+v", k)
	}
	for _, bad := range [][]byte{nil, {0x45}, ipv4("1.2.3.4", "5.6.7.8", 1, nil)[:19], append(ipv4("1.2.3.4", "5.6.7.8", 1, nil), 0)} {
		if _, ok := parsePacket(bad); ok {
			t.Errorf("accepted %x", bad)
		}
	}
	echoReply := ipv4("10.78.1.2", "10.78.3.4", protoICMP, []byte{0, 0, 0, 0, 0, 1, 0, 1})
	if k, _ := parsePacket(echoReply); !k.isICMPReply() || k.isEchoRequest() {
		t.Fatal("echo reply")
	}
}

func checksumOK(p []byte, k packet) bool {
	// RFC 1071 over pseudo-header and segment.
	var sum uint32
	add := func(b []byte) {
		for i := 0; i+1 < len(b); i += 2 {
			sum += uint32(binary.BigEndian.Uint16(b[i:]))
		}
		if len(b)%2 == 1 {
			sum += uint32(b[len(b)-1]) << 8
		}
	}
	seg := p[k.l4:]
	if k.v6 {
		add(p[8:40])
	} else {
		add(p[12:20])
	}
	var l [4]byte
	binary.BigEndian.PutUint32(l[:], uint32(len(seg)))
	if k.v6 {
		add(l[:])
		add([]byte{0, 0, 0, k.proto})
	} else {
		add(l[2:])
		add([]byte{0, k.proto})
	}
	add(seg)
	for sum>>16 != 0 {
		sum = sum&0xffff + sum>>16
	}
	return uint16(sum) == 0xffff
}

func fixChecksum(p []byte, k packet) {
	seg := p[k.l4:]
	seg[16], seg[17] = 0, 0
	for c := 0; c < 65536; c++ { // brute force is fine for a test
		binary.BigEndian.PutUint16(seg[16:18], uint16(c))
		if checksumOK(p, k) {
			return
		}
	}
}

func TestClampMSS(t *testing.T) {
	for _, v6 := range []bool{false, true} {
		var p []byte
		if v6 {
			p = ipv6("fd00::1", "fd00::2", protoTCP, tcpSegment(1, 2, true, 1440))
		} else {
			p = ipv4("10.78.1.2", "10.78.3.4", protoTCP, tcpSegment(1, 2, true, 1460))
		}
		k, _ := parsePacket(p)
		fixChecksum(p, k)
		clampMSS(p, k, 1200)
		got := binary.BigEndian.Uint16(p[k.l4+22:])
		want := uint16(1200)
		if v6 {
			want = 1180
		}
		if got != want || !checksumOK(p, k) {
			t.Fatalf("v6=%v: mss %d (want %d), checksum ok %v", v6, got, want, checksumOK(p, k))
		}
	}
	// Not a SYN: untouched.
	p := ipv4("10.78.1.2", "10.78.3.4", protoTCP, tcpSegment(1, 2, false, 1460))
	k, _ := parsePacket(p)
	clampMSS(p, k, 1200)
	if binary.BigEndian.Uint16(p[k.l4+22:]) != 1460 {
		t.Fatal("clamped a segment that is not a SYN")
	}
}

// --- policy -------------------------------------------------------------

func pkt(t *testing.T, proto byte, dport uint16) packet {
	t.Helper()
	var raw []byte
	switch proto {
	case protoICMP:
		raw = ipv4("10.78.1.1", "10.78.2.2", protoICMP, []byte{8, 0, 0, 0, 0, 1, 0, 1})
	case protoTCP:
		raw = ipv4("10.78.1.1", "10.78.2.2", protoTCP, tcpSegment(40000, dport, true, 1400))
	default:
		u := make([]byte, 8)
		binary.BigEndian.PutUint16(u[2:4], dport)
		raw = ipv4("10.78.1.1", "10.78.2.2", protoUDP, u)
	}
	k, ok := parsePacket(raw)
	if !ok {
		t.Fatal("bad test packet")
	}
	return k
}

func TestPolicy(t *testing.T) {
	doc := `{"version": 3, "rules": [
		{"from": ["laptop", "phone"], "to": ["nas"], "ports": ["22/tcp", "8000-8100", "icmp"]},
		{"from": ["*"], "to": ["printer"], "ports": ["631/tcp"]},
		{"from": ["laptop"], "to": ["192.168.1.0/24"]}]}`
	p, err := ParsePolicy([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	c, _ := compile(p)
	laptop, phone, tv := device{label: "laptop"}, device{label: "phone"}, device{label: "tv", fp: "aaaa:bbbb:cccc:dddd:eeee"}
	nas, printer := device{label: "nas"}, device{label: "printer"}
	self := netip.MustParseAddr("10.78.2.2")
	cases := []struct {
		from, to device
		k        packet
		routed   string
		want     bool
	}{
		{laptop, nas, pkt(t, protoTCP, 22), "", true},
		{laptop, nas, pkt(t, protoTCP, 23), "", false},
		{phone, nas, pkt(t, protoUDP, 8050), "", true},
		{phone, nas, pkt(t, protoUDP, 22), "", false}, // 22 is TCP only
		{phone, nas, pkt(t, protoICMP, 0), "", true},
		{tv, nas, pkt(t, protoTCP, 22), "", false},
		{tv, printer, pkt(t, protoTCP, 631), "", true},
		{laptop, nas, pkt(t, protoTCP, 80), "192.168.1.7", true}, // routed through nas
		{phone, nas, pkt(t, protoTCP, 80), "192.168.1.7", false},
		{laptop, nas, pkt(t, protoTCP, 80), "192.168.2.7", false},
	}
	for i, tc := range cases {
		dst, routed := self, false
		if tc.routed != "" {
			dst, routed = netip.MustParseAddr(tc.routed), true
		}
		if got := c.allows(tc.from, tc.to, dst, routed, tc.k); got != tc.want {
			t.Errorf("case %d: %s -> %s port %d routed=%v: %v, want %v", i, tc.from.label, tc.to.label, tc.k.dport, routed, got, tc.want)
		}
	}
	// Selectors by fingerprint.
	p, _ = ParsePolicy([]byte(`{"version":1,"rules":[{"from":["aaaa:bbbb:cccc:dddd:eeee"],"to":["*"]}]}`))
	c, _ = compile(p)
	if !c.allows(tv, nas, self, false, pkt(t, protoTCP, 1)) || c.allows(laptop, nas, self, false, pkt(t, protoTCP, 1)) {
		t.Fatal("fingerprint selector")
	}
	// No policy: open. Empty rules: closed.
	open, _ := compile(nil)
	if !open.allows(tv, nas, self, false, pkt(t, protoTCP, 1)) {
		t.Fatal("no policy should allow everything")
	}
	p, _ = ParsePolicy([]byte(`{"version":1,"rules":[]}`))
	closed, _ := compile(p)
	if closed.allows(laptop, nas, self, false, pkt(t, protoICMP, 0)) {
		t.Fatal("an empty rule list should allow nothing")
	}
	for _, bad := range []string{
		`{"version":1,"rules":[{"from":["*"]}]}`,
		`{"version":1,"rules":[{"from":["*"],"to":["*"],"ports":["70000"]}]}`,
		`{"version":1,"rules":[{"from":["*"],"to":["*"],"ports":["22/sctp"]}]}`,
		`{"version":1,"rules":[{"from":["10.0.0.0/8"],"to":["*"]}]}`,
		`{"version":1,"rules":[{"from":["bad name!"],"to":["*"]}]}`,
		`{"version":1,"rules":[],"extra":1}`,
		`{"version":1,"prefix4":"8.8.0.0/16","rules":[]}`,
	} {
		if _, err := ParsePolicy([]byte(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

func TestSignedPolicy(t *testing.T) {
	member, outsider := newID(t), newID(t)
	acct := []byte("account-id")
	isMember := func(k ed25519.PublicKey) bool { return k.Equal(member.Public()) }
	sp, err := SignPolicy(member, acct, &Policy{Version: 4, Rules: []Rule{{From: []string{"*"}, To: []string{"nas"}}}})
	if err != nil {
		t.Fatal(err)
	}
	// It survives storage, including reformatting of the JSON around it.
	b, _ := json.MarshalIndent(sp, "", "    ")
	var back SignedPolicy
	json.Unmarshal(b, &back)
	if p, err := back.Verify(acct, isMember); err != nil || p.Version != 4 {
		t.Fatalf("verify after round trip: %v", err)
	}
	if _, err := back.Verify([]byte("other"), isMember); err == nil {
		t.Fatal("verified for another account")
	}
	forged, _ := SignPolicy(outsider, acct, &Policy{Version: 9})
	if _, err := forged.Verify(acct, isMember); err == nil {
		t.Fatal("a policy signed by a non-member verified")
	}
	tampered := back
	tampered.Policy = []byte(strings.Replace(string(back.Policy), `"nas"`, `"tv"`, 1))
	if _, err := tampered.Verify(acct, isMember); err == nil {
		t.Fatal("a tampered policy verified")
	}
	older, _ := SignPolicy(member, acct, &Policy{Version: 3})
	if newer(older, 3, sp, 4) || !newer(sp, 4, older, 3) || !newer(sp, 4, nil, 0) {
		t.Fatal("version order")
	}
	twin, _ := SignPolicy(member, acct, &Policy{Version: 4, Rules: []Rule{}})
	if newer(twin, 4, sp, 4) == newer(sp, 4, twin, 4) {
		t.Fatal("equal versions must be ordered one way")
	}
}

// --- flows, DNS, hosts --------------------------------------------------

func TestFlows(t *testing.T) {
	f := newFlows()
	now := time.Now()
	f.now = func() time.Time { return now }
	out := ipv4("10.78.2.2", "10.78.1.1", protoTCP, tcpSegment(40000, 22, true, 1400))
	k, _ := parsePacket(out)
	f.out(k)
	back := ipv4("10.78.1.1", "10.78.2.2", protoTCP, tcpSegment(22, 40000, false, 0))
	kb, _ := parsePacket(back)
	if !f.reply(kb) {
		t.Fatal("reply not recognised")
	}
	other := ipv4("10.78.1.1", "10.78.2.2", protoTCP, tcpSegment(22, 40001, true, 0))
	ko, _ := parsePacket(other)
	if f.reply(ko) {
		t.Fatal("a new connection counted as a reply")
	}
	now = now.Add(tcpFlowTTL + time.Second)
	if f.reply(kb) {
		t.Fatal("an expired flow still counts")
	}
}

func dnsQuery(t *testing.T, name string, typ dnsmessage.Type) []byte {
	t.Helper()
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: 7, RecursionDesired: true})
	b.StartQuestions()
	b.Question(dnsmessage.Question{Name: dnsmessage.MustNewName(name), Type: typ, Class: dnsmessage.ClassINET})
	out, err := b.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestDNS(t *testing.T) {
	var n names
	n.set(map[string][]netip.Addr{"nas.remohop.": {netip.MustParseAddr("10.78.1.2"), netip.MustParseAddr("fd00::2")}})
	ask := func(name string, typ dnsmessage.Type) (dnsmessage.RCode, []dnsmessage.Resource) {
		out, ok := n.answer(dnsQuery(t, name, typ))
		if !ok {
			t.Fatalf("no answer for %s", name)
		}
		var m dnsmessage.Message
		if err := m.Unpack(out); err != nil {
			t.Fatal(err)
		}
		if m.ID != 7 || !m.Response {
			t.Fatal("header")
		}
		return m.RCode, m.Answers
	}
	if rc, ans := ask("NAS.remohop.", dnsmessage.TypeA); rc != dnsmessage.RCodeSuccess || len(ans) != 1 ||
		ans[0].Body.(*dnsmessage.AResource).A != [4]byte{10, 78, 1, 2} {
		t.Fatalf("A: %v %v", rc, ans)
	}
	if rc, ans := ask("nas.remohop.", dnsmessage.TypeAAAA); rc != dnsmessage.RCodeSuccess || len(ans) != 1 {
		t.Fatalf("AAAA: %v %v", rc, ans)
	}
	if rc, _ := ask("tv.remohop.", dnsmessage.TypeA); rc != dnsmessage.RCodeNameError {
		t.Fatalf("unknown name: %v", rc)
	}
	if rc, _ := ask("example.com.", dnsmessage.TypeA); rc != dnsmessage.RCodeRefused {
		t.Fatalf("outside the zone: %v", rc)
	}
	if _, ok := n.answer([]byte{1, 2, 3}); ok {
		t.Fatal("answered garbage")
	}
}

func TestHostsBlock(t *testing.T) {
	orig := "127.0.0.1 localhost\n::1 localhost\n"
	with := withBlock([]byte(orig), []string{"10.78.1.2\tnas.remohop"})
	if !strings.Contains(string(with), hostsBegin) || !strings.Contains(string(with), "nas.remohop") || !strings.HasPrefix(string(with), orig) {
		t.Fatalf("block not added:\n%s", with)
	}
	again := withBlock(with, []string{"10.78.1.3\ttv.remohop"})
	if strings.Contains(string(again), "nas.remohop") || strings.Count(string(again), hostsBegin) != 1 {
		t.Fatalf("block not replaced:\n%s", again)
	}
	if got := string(withBlock(again, nil)); got != orig {
		t.Fatalf("block not removed:\n%q", got)
	}
}

// --- the account's view -------------------------------------------------

// testAccount builds the verified state of an account with the given
// devices (the first creates it), as device self sees it.
func testAccount(t *testing.T, ids []*auth.Identity, names []string) *account.State {
	t.Helper()
	log := []ledger.Entry{ledger.Create(ids[0], "lab@example.com", names[0])}
	ls, err := ledger.Verify(log)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(ids); i++ {
		e, err := ls.Next(ids[0], ledger.OpAdd, ids[i].Public(), names[i])
		if err != nil {
			t.Fatal(err)
		}
		log = append(log, e)
		if err := ls.Apply(e); err != nil {
			t.Fatal(err)
		}
	}
	return &account.State{Status: account.StatusMember, AccountID: ls.ID, Log: log, Verified: time.Now()}
}

func TestViewAndHello(t *testing.T) {
	ids := []*auth.Identity{newID(t), newID(t), newID(t)}
	st := testAccount(t, ids, []string{"alpha", "bravo", "Charlie"})
	va, err := buildView(ids[0].Public(), st, []account.Device{{ID: "123456789", Key: auth.EncodeKey(ids[1].Public()), Online: true}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	vb, _ := buildView(ids[1].Public(), st, nil, nil)
	if va.self.Label != "alpha" || len(va.peers) != 2 || va.peers[1].Label != "charlie" {
		t.Fatalf("view: %+v %d", va.self, len(va.peers))
	}
	if va.byKey[string(ids[1].Public())].ID != "123456789" || !va.byKey[string(ids[1].Public())].Online {
		t.Fatal("device IDs not applied")
	}
	// Both devices compute the same addresses for everybody.
	for k, p := range va.byKey {
		if vb.byKey[k].Addr4 != p.Addr4 || vb.byKey[k].Addr6 != p.Addr6 {
			t.Fatal("two members disagree about an address")
		}
	}
	if p, ok := va.byName("charlie.remohop"); !ok || p.Label != "charlie" {
		t.Fatal("byName")
	}
	if _, ok := va.names()["bravo.remohop."]; !ok {
		t.Fatal("names")
	}
	// A hello from bravo as bravo computes itself checks out at alpha; a
	// claimed address that differs, or a stranger, does not.
	h := &hello{V: protoVersion, Addr4: vb.self.Addr4.String(), Addr6: vb.self.Addr6.String()}
	if _, err := checkHello(va, ids[1].Public(), h); err != nil {
		t.Fatal(err)
	}
	h.Addr4 = "10.78.0.9"
	if _, err := checkHello(va, ids[1].Public(), h); err == nil || !strings.Contains(err.Error(), "disagree") {
		t.Fatalf("wrong address accepted: %v", err)
	}
	if _, err := checkHello(va, newID(t).Public(), h); err == nil {
		t.Fatal("a stranger's hello accepted")
	}
	// A non-member cannot have a view; a stale log is refused.
	if _, err := buildView(newID(t).Public(), st, nil, nil); err == nil {
		t.Fatal("view for a non-member")
	}
	stale := *st
	stale.Verified = time.Now().Add(-account.MaxStale - time.Hour)
	if _, err := buildView(ids[0].Public(), &stale, nil, nil); err == nil {
		t.Fatal("view from a stale log")
	}
	// A policy signed by a member applies, also its prefix.
	sp, _ := SignPolicy(ids[2], st.AccountID, &Policy{Version: 2, Prefix4: "172.30.0.0/16", Rules: []Rule{}})
	vp, err := buildView(ids[0].Public(), st, nil, sp)
	if err != nil || vp.version != 2 || !netip.MustParsePrefix("172.30.0.0/16").Contains(vp.self.Addr4) {
		t.Fatalf("policy view: %v %v", err, vp)
	}
}

// A removed device keeps its address reserved: nobody's address changes.
func TestViewRemovalKeepsAddresses(t *testing.T) {
	ids := []*auth.Identity{newID(t), newID(t), newID(t)}
	st := testAccount(t, ids, []string{"a", "b", "c"})
	before, _ := buildView(ids[0].Public(), st, nil, nil)
	ls, _ := st.Ledger()
	e, err := ls.Next(ids[0], ledger.OpRemove, ids[1].Public(), "")
	if err != nil {
		t.Fatal(err)
	}
	st.Log = append(st.Log, e)
	after, err := buildView(ids[0].Public(), st, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := after.byKey[string(ids[1].Public())]; ok || len(after.peers) != 1 {
		t.Fatal("a removed device is still a peer")
	}
	if after.byKey[string(ids[2].Public())].Addr4 != before.byKey[string(ids[2].Public())].Addr4 {
		t.Fatal("removing a device moved another")
	}
}

// The receiving side's decision: sources must be the sending device's own
// address (or, for replies, what it routes for us), destinations ours (or
// what we route), and the policy must agree.
func TestAdmit(t *testing.T) {
	ids := []*auth.Identity{newID(t), newID(t), newID(t)}
	st := testAccount(t, ids, []string{"self", "router", "other"})
	sp, _ := SignPolicy(ids[0], st.AccountID, &Policy{Version: 1, Rules: []Rule{
		{From: []string{"router"}, To: []string{"self"}, Ports: []string{"22/tcp"}},
		{From: []string{"other"}, To: []string{"192.168.9.0/24"}},
	}})
	v, err := buildView(ids[0].Public(), st, nil, sp)
	if err != nil {
		t.Fatal(err)
	}
	n := &Node{id: ids[0], flows: newFlows(), learned: map[netip.Prefix]string{}, links: map[string]*link{},
		cfg: settings{routes: []netip.Prefix{netip.MustParsePrefix("192.168.9.0/24")}}}
	n.view.Store(v)
	self, router, other := v.self, v.byKey[string(ids[1].Public())], v.byKey[string(ids[2].Public())]
	lr := &link{key: string(ids[1].Public())}
	lo := &link{key: string(ids[2].Public())}
	n.learned[netip.MustParsePrefix("10.20.0.0/16")] = lr.key

	tcp := func(src, dst netip.Addr, sport, dport uint16, syn bool) packet {
		k, _ := parsePacket(ipv4(src.String(), dst.String(), protoTCP, tcpSegment(sport, dport, syn, 1400)))
		return k
	}
	lan := netip.MustParseAddr("10.20.1.1")
	cases := []struct {
		name string
		l    *link
		k    packet
		want bool
	}{
		{"policy allows router -> self:22", lr, tcp(router.Addr4, self.Addr4, 5000, 22, true), true},
		{"policy refuses router -> self:80", lr, tcp(router.Addr4, self.Addr4, 5000, 80, true), false},
		{"policy refuses other -> self:22", lo, tcp(other.Addr4, self.Addr4, 5000, 22, true), false},
		{"other spoofs router's address", lo, tcp(router.Addr4, self.Addr4, 5000, 22, true), false},
		{"to someone else's mesh address", lr, tcp(router.Addr4, other.Addr4, 5000, 22, true), false},
		{"unsolicited from router's LAN", lr, tcp(lan, self.Addr4, 5000, 22, true), false},
		{"other through us to the routed LAN", lo, tcp(other.Addr4, netip.MustParseAddr("192.168.9.5"), 5000, 80, true), true},
		{"router through us to the routed LAN (policy)", lr, tcp(router.Addr4, netip.MustParseAddr("192.168.9.5"), 5000, 80, true), false},
		{"other through us elsewhere (no exit)", lo, tcp(other.Addr4, netip.MustParseAddr("8.8.8.8"), 5000, 80, true), false},
	}
	for _, c := range cases {
		if got := n.admit(c.l, c.k); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	// Replies pass: to our own connection to router:80, and from a machine
	// on router's LAN we connected to.
	n.flows.out(tcp(self.Addr4, router.Addr4, 41000, 80, true))
	if !n.admit(lr, tcp(router.Addr4, self.Addr4, 80, 41000, false)) {
		t.Error("reply from router refused")
	}
	n.flows.out(tcp(self.Addr4, lan, 41001, 443, true))
	if !n.admit(lr, tcp(lan, self.Addr4, 443, 41001, false)) {
		t.Error("reply from router's LAN refused")
	}
	if n.admit(lo, tcp(lan, self.Addr4, 443, 41001, false)) {
		t.Error("a reply claimed by a device that does not route that LAN")
	}
}

func TestParseRoutes(t *testing.T) {
	r, err := parseRoutes("192.168.1.0/24, 10.20.0.0/16")
	if err != nil || len(r) != 2 || r[1].String() != "10.20.0.0/16" {
		t.Fatalf("%v %v", r, err)
	}
	for _, bad := range []string{"0.0.0.0/0", "192.168.1.1/24", "fd00::/64", "nonsense"} {
		if _, err := parseRoutes(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if r, err := parseRoutes(""); err != nil || r != nil {
		t.Fatal("empty")
	}
}

// stuckStream is a link stream to a peer that stopped draining: writes
// block until the stream is reset.
type stuckStream struct {
	peer.Stream
	reset chan struct{}
	once  sync.Once
}

func (s *stuckStream) Write(p []byte) (int, error)      { <-s.reset; return 0, net.ErrClosed }
func (s *stuckStream) SetWriteDeadline(time.Time) error { return nil }
func (s *stuckStream) Reset()                           { s.once.Do(func() { close(s.reset) }) }

// One peer that stops taking packets must not hold up the interface's read
// loop, which serves every peer: its link drops its own packets instead.
func TestStalledPeerDoesNotBlock(t *testing.T) {
	st := &stuckStream{reset: make(chan struct{})}
	l := &link{s: st, done: make(chan struct{}), out: make(chan []byte, sendQueueLen)}
	go l.sendLoop()
	pkt := make([]byte, 1200)
	start := time.Now()
	full := 0
	for i := 0; i < 10*sendQueueLen; i++ {
		if err := l.send(pkt); errors.Is(err, errQueueFull) {
			full++
		}
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("sending to a stalled peer took %s", took)
	}
	if full < 8*sendQueueLen {
		t.Fatalf("%d packets dropped for a full queue, want most of them", full)
	}
	l.close(errors.New("test"))
	if err := l.send(pkt); err == nil {
		t.Fatal("a closed link took a packet")
	}
}

// fakeMux is a session to a peer at a given address that does nothing.
type fakeMux struct {
	peer.Mux
	remote netip.AddrPort
	ctx    context.Context
	cancel context.CancelFunc
}

func newFakeMux(remote string) *fakeMux {
	ctx, cancel := context.WithCancel(context.Background())
	return &fakeMux{remote: netip.MustParseAddrPort(remote), ctx: ctx, cancel: cancel}
}

func (m *fakeMux) RemoteAddr() net.Addr     { return net.UDPAddrFromAddrPort(m.remote) }
func (m *fakeMux) LocalAddr() net.Addr      { return &net.UDPAddr{} }
func (m *fakeMux) Context() context.Context { return m.ctx }
func (m *fakeMux) Close(string) error       { m.cancel(); return nil }
func (m *fakeMux) AcceptStream(ctx context.Context) (peer.Stream, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// A session that runs through the mesh itself (to a peer's mesh address,
// over an existing link) never becomes a link: once the link under it is
// replaced or ends, it would carry its packets into its own tunnel.
func TestNoLinkThroughTheMesh(t *testing.T) {
	defer nat.ExcludeNetworks(DefaultPrefix4)()
	for remote, want := range map[string]bool{"10.78.1.2:41000": true, "100.65.3.2:41000": false} {
		m := newFakeMux(remote)
		c := peer.NewConn(m, peer.Options{Role: peer.RoleClient})
		if got := throughMesh(c); got != want {
			t.Errorf("throughMesh(session to %s) = %v, want %v", remote, got, want)
		}
		if !want {
			c.Close()
			continue
		}
		n := &Node{dialPeer: func(context.Context, string) (*peer.Conn, error) { return c, nil }}
		if _, err := n.dialLink(context.Background(), &peerInfo{ID: "123456789"}); !errors.Is(err, errThroughMesh) {
			t.Errorf("dialLink over a session to %s: err = %v, want %v", remote, err, errThroughMesh)
		}
		select {
		case <-m.ctx.Done():
		case <-time.After(time.Second):
			t.Errorf("the session to %s was not closed", remote)
		}
	}
}
