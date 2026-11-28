package lan

import (
	"context"
	"crypto/ed25519"
	"errors"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/readori/RemoHop/internal/auth"
)

const (
	fpA = "9b2e:771c:0aa4:d3f1:1234"
	fpB = "0000:1111:2222:3333:4444"
)

func TestTXTRoundTrip(t *testing.T) {
	in := Info{Name: "Studio\x1b]0;pwned\x07 iMac", ID: "418203775", Fingerprint: fpA, Port: 21118}
	txt := in.TXT()
	for _, s := range txt {
		if strings.Contains(s, "\x1b") || strings.Contains(s, "\x07") {
			t.Fatalf("control character announced: %q", s)
		}
	}
	got, err := parseTXT(txt)
	if err != nil {
		t.Fatal(err)
	}
	if got.name != "Studio]0;pwned iMac" || got.id != "418203775" || got.fp != fpA {
		t.Fatalf("got %+v", got)
	}

	// No ID, a malformed one, a long name.
	for _, id := range []string{"", "12345678", "012345678", "12345678a"} {
		txt := Info{Name: strings.Repeat("é", 100), ID: id, Fingerprint: fpA}.TXT()
		got, err := parseTXT(txt)
		if err != nil {
			t.Fatal(err)
		}
		if got.id != "" {
			t.Errorf("ID %q announced", id)
		}
		if len(got.name) > maxName || !strings.HasPrefix(got.name, "éé") {
			t.Errorf("long name kept as %q (%d bytes)", got.name, len(got.name))
		}
	}
	for _, s := range (Info{Name: "x", Fingerprint: fpA}).TXT() {
		if strings.HasPrefix(s, "id=") {
			t.Errorf("ID announced without one: %q", s)
		}
	}
}

func TestParseTXT(t *testing.T) {
	ok := func(txt ...string) txtInfo {
		t.Helper()
		got, err := parseTXT(txt)
		if err != nil {
			t.Fatalf("%q: %v", txt, err)
		}
		return got
	}
	bad := func(txt ...string) {
		t.Helper()
		if got, err := parseTXT(txt); err == nil {
			t.Errorf("%q accepted: %+v", txt, got)
		}
	}
	bad("name=x", "fp="+fpA)                  // no version
	bad("v=2", "name=x", "fp="+fpA)           // another format
	bad("v=1", "name=x")                      // no fingerprint
	bad("v=1", "fp=9B2E:771C:0AA4:D3F1:1234") // not as auth.Fingerprint writes it
	bad("v=1", "fp="+fpA+":5678")
	bad("v=1", "fp=9b2e-771c-0aa4-d3f1-1234")

	// Keys are case-insensitive and the first of a repeated key counts.
	got := ok("V=1", "Name=first", "name=second", "FP="+fpA, "fp="+fpB, "id=418203775", "extra=ignored", "noequals")
	if got.name != "first" || got.fp != fpA || got.id != "418203775" {
		t.Fatalf("got %+v", got)
	}
	// Only the first maxTXT strings are read.
	long := make([]string, 0, maxTXT+1)
	long = append(long, "v=1")
	for len(long) < maxTXT {
		long = append(long, "pad=x")
	}
	bad(append(long, "fp="+fpA)...)
	// Invalid UTF-8 and line breaks in a name.
	if got := ok("v=1", "fp="+fpA, "name=a\xffb\nc‮d"); got.name != "ab cd" {
		t.Errorf("name cleaned to %q", got.name)
	}
}

func TestInstanceName(t *testing.T) {
	n := instanceName(Info{Name: "my.host.name", Fingerprint: fpA})
	if n != "my-host-name (9b2e771c)."+Service {
		t.Errorf("got %q", n)
	}
	if n := instanceName(Info{Fingerprint: fpA}); n != "RemoHop (9b2e771c)."+Service {
		t.Errorf("no name: %q", n)
	}
	n = instanceName(Info{Name: strings.Repeat("界", 40), Fingerprint: fpA})
	label := strings.TrimSuffix(n, "."+Service)
	if len(label) > 63 || !strings.HasSuffix(label, "界 (9b2e771c)") {
		t.Errorf("long name: %q (%d bytes)", label, len(label))
	}
	if _, err := dnsmessage.NewName(n); err != nil {
		t.Error(err)
	}
}

var (
	lanNet   = netip.MustParsePrefix("192.168.1.20/24")
	otherNet = netip.MustParsePrefix("10.9.0.5/16")
	locals   = []netip.Prefix{lanNet, otherNet, netip.MustParsePrefix("127.0.0.1/8")}
	asker    = netip.MustParseAddrPort("192.168.1.33:40000")
	hostInfo = Info{Name: "studio", ID: "418203775", Fingerprint: fpA, Port: 21118}
)

func query(t *testing.T, id uint16, qs ...dnsmessage.Question) []byte {
	t.Helper()
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: id})
	b.StartQuestions()
	for _, q := range qs {
		if err := b.Question(q); err != nil {
			t.Fatal(err)
		}
	}
	out, err := b.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func q(name string, typ dnsmessage.Type) dnsmessage.Question {
	return dnsmessage.Question{Name: dnsmessage.MustNewName(name), Type: typ, Class: dnsmessage.ClassINET}
}

func TestAnswer(t *testing.T) {
	ptr := query(t, 77, q(Service, dnsmessage.TypePTR))
	out := answer(ptr, asker, hostInfo, locals)
	if out == nil {
		t.Fatal("no answer to a browse")
	}
	var m dnsmessage.Message
	if err := m.Unpack(out); err != nil {
		t.Fatal(err)
	}
	if !m.Header.Response || m.Header.ID != 77 || len(m.Questions) != 1 {
		t.Errorf("legacy unicast answer: header %+v, %d questions", m.Header, len(m.Questions))
	}
	var addrs []string
	for _, r := range append(m.Answers, m.Additionals...) {
		if r.Header.TTL != ttlLegacy || r.Header.Class != dnsmessage.ClassINET {
			t.Errorf("%v: TTL %d, class %v", r.Header.Type, r.Header.TTL, r.Header.Class)
		}
		if a, ok := r.Body.(*dnsmessage.AResource); ok {
			addrs = append(addrs, netip.AddrFrom4(a.A).String())
		}
	}
	if len(m.Answers) != 1 || len(m.Additionals) != 3 || strings.Join(addrs, ",") != "192.168.1.20" {
		t.Errorf("%d answers, %d additional, addresses %v", len(m.Answers), len(m.Additionals), addrs)
	}
	for _, s := range m.Additionals {
		if tx, ok := s.Body.(*dnsmessage.TXTResource); ok && strings.Contains(strings.Join(tx.TXT, " "), "pass") {
			t.Errorf("TXT says %q", tx.TXT)
		}
	}
	// What the browser makes of it.
	hosts := parseAnswer(out, netip.AddrPortFrom(lanNet.Addr(), mdnsPort), 77, locals)
	if len(hosts) != 1 || hosts[0] != (Host{Name: "studio", Addr: netip.MustParseAddrPort("192.168.1.20:21118"), ID: "418203775", Fingerprint: fpA}) {
		t.Errorf("browser read %+v", hosts)
	}

	// A full mDNS querier (port 5353): no ID or question, cache-flush bit.
	out = answer(ptr, netip.AddrPortFrom(asker.Addr(), mdnsPort), hostInfo, locals)
	if err := m.Unpack(out); err != nil {
		t.Fatal(err)
	}
	if m.Header.ID != 0 || len(m.Questions) != 0 || m.Additionals[0].Header.Class != classFlush || m.Answers[0].Header.TTL != ttlMDNS {
		t.Errorf("mDNS answer: %+v", m)
	}

	// The other questions a resolver asks.
	inst, host := instanceName(hostInfo), hostName(hostInfo)
	for _, c := range []struct {
		q    dnsmessage.Question
		want []dnsmessage.Type
	}{
		{q(metaService, dnsmessage.TypePTR), []dnsmessage.Type{dnsmessage.TypePTR}},
		{q(strings.ToUpper(inst[:3])+inst[3:], dnsmessage.TypeSRV), []dnsmessage.Type{dnsmessage.TypeSRV}},
		{q(inst, dnsmessage.TypeTXT), []dnsmessage.Type{dnsmessage.TypeTXT}},
		{q(inst, dnsmessage.TypeALL), []dnsmessage.Type{dnsmessage.TypeSRV, dnsmessage.TypeTXT}},
		{q(host, dnsmessage.TypeA), []dnsmessage.Type{dnsmessage.TypeA}},
	} {
		out := answer(query(t, 1, c.q), asker, hostInfo, locals)
		if err := m.Unpack(out); err != nil {
			t.Fatalf("%v: %v", c.q, err)
		}
		var got []dnsmessage.Type
		for _, r := range m.Answers {
			got = append(got, r.Header.Type)
		}
		if len(got) != len(c.want) || got[0] != c.want[0] {
			t.Errorf("%v: answers %v", c.q, got)
		}
	}
}

func TestAnswerRefuses(t *testing.T) {
	ptr := query(t, 1, q(Service, dnsmessage.TypePTR))
	cases := map[string]struct {
		query []byte
		from  netip.AddrPort
		info  Info
	}{
		"off the local network": {ptr, netip.MustParseAddrPort("203.0.113.7:40000"), hostInfo},
		"IPv6":                  {ptr, netip.MustParseAddrPort("[fe80::1]:40000"), hostInfo},
		"bound elsewhere":       {ptr, asker, Info{Name: "x", Fingerprint: fpA, Port: 1, Addr: otherNet.Addr()}},
		"no fingerprint":        {ptr, asker, Info{Name: "x", Port: 1}},
		"no port":               {ptr, asker, Info{Name: "x", Fingerprint: fpA}},
		"other service":         {query(t, 1, q("_ssh._tcp.local.", dnsmessage.TypePTR)), asker, hostInfo},
		"other instance":        {query(t, 1, q("x."+Service, dnsmessage.TypeSRV)), asker, hostInfo},
		"garbage":               {[]byte{1, 2, 3}, asker, hostInfo},
		"a response":            {answer(ptr, asker, hostInfo, locals), asker, hostInfo},
	}
	for name, c := range cases {
		if out := answer(c.query, c.from, c.info, locals); out != nil {
			t.Errorf("%s: answered", name)
		}
	}
	// Bound to the address on the asker's subnet: answered, with it.
	if answer(ptr, asker, Info{Name: "x", Fingerprint: fpA, Port: 1, Addr: lanNet.Addr()}, locals) == nil {
		t.Error("bound to the LAN address: not answered")
	}
}

// answerFrom builds an answer by hand, as another (or a hostile) responder
// might.
func answerFrom(t *testing.T, id uint16, build func(b *dnsmessage.Builder)) []byte {
	t.Helper()
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: id, Response: true})
	b.StartAnswers()
	build(&b)
	out, err := b.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func rh(name string, typ dnsmessage.Type) dnsmessage.ResourceHeader {
	return dnsmessage.ResourceHeader{Name: dnsmessage.MustNewName(name), Type: typ, Class: dnsmessage.ClassINET, TTL: 10}
}

func TestParseAnswerFilters(t *testing.T) {
	from := netip.MustParseAddrPort("192.168.1.20:5353")
	one := func(inst string, txt []string, port uint16, target string, a ...[4]byte) func(*dnsmessage.Builder) {
		return func(b *dnsmessage.Builder) {
			b.SRVResource(rh(inst, dnsmessage.TypeSRV), dnsmessage.SRVResource{Port: port, Target: dnsmessage.MustNewName(target)})
			b.TXTResource(rh(inst, dnsmessage.TypeTXT), dnsmessage.TXTResource{TXT: txt})
			for _, ip := range a {
				b.AResource(rh(target, dnsmessage.TypeA), dnsmessage.AResource{A: ip})
			}
		}
	}
	good := one("a."+Service, []string{"v=1", "fp=" + fpA, "name=Alpha"}, 21118, "t.local.", [4]byte{192, 168, 1, 21})
	if h := parseAnswer(answerFrom(t, 5, good), from, 5, locals); len(h) != 1 || h[0].Addr.String() != "192.168.1.21:21118" || h[0].Name != "Alpha" {
		t.Fatalf("good answer: %+v", h)
	}
	for name, c := range map[string]struct {
		from netip.AddrPort
		id   uint16
	}{
		"wrong ID":         {from, 6},
		"not from 5353":    {netip.MustParseAddrPort("192.168.1.20:5354"), 5},
		"off-link source":  {netip.MustParseAddrPort("8.8.8.8:5353"), 5},
		"IPv6 source":      {netip.MustParseAddrPort("[fe80::1]:5353"), 5},
		"unspecified addr": {netip.AddrPort{}, 5},
	} {
		if h := parseAnswer(answerFrom(t, 5, good), c.from, c.id, locals); len(h) != 0 {
			t.Errorf("%s: accepted %+v", name, h)
		}
	}
	// An address off this machine's subnets, or loopback, is not used:
	// the source of the answer is.
	for _, ip := range [][4]byte{{8, 8, 8, 8}, {127, 0, 0, 1}, {224, 0, 0, 251}, {0, 0, 0, 0}} {
		h := parseAnswer(answerFrom(t, 5, one("a."+Service, []string{"v=1", "fp=" + fpA}, 1, "t.local.", ip)), from, 5, locals)
		if len(h) != 1 || h[0].Addr.Addr() != from.Addr() {
			t.Errorf("A %v: %+v", ip, h)
		}
	}
	// No name: the instance label, cleaned.
	h := parseAnswer(answerFrom(t, 5, one("Bad\x1bName."+Service, []string{"v=1", "fp=" + fpA}, 1, "t.local.")), from, 5, locals)
	if len(h) != 1 || h[0].Name != "BadName" {
		t.Errorf("no name: %+v", h)
	}
	// Records that do not make a host.
	for name, build := range map[string]func(*dnsmessage.Builder){
		"bad TXT":       one("a."+Service, []string{"v=1", "fp=nope"}, 1, "t.local."),
		"port 0":        one("a."+Service, []string{"v=1", "fp=" + fpA}, 0, "t.local."),
		"other service": one("a._ssh._tcp.local.", []string{"v=1", "fp=" + fpA}, 22, "t.local."),
		"no TXT": func(b *dnsmessage.Builder) {
			b.SRVResource(rh("a."+Service, dnsmessage.TypeSRV), dnsmessage.SRVResource{Port: 1, Target: dnsmessage.MustNewName("t.local.")})
		},
	} {
		if h := parseAnswer(answerFrom(t, 5, build), from, 5, locals); len(h) != 0 {
			t.Errorf("%s: %+v", name, h)
		}
	}
	// A flood of records: at most maxRecords are read.
	flood := func(b *dnsmessage.Builder) {
		for i := range 200 {
			inst := strings.Repeat("x", 1+i%60) + string(rune('a'+i/60)) + "." + Service
			one(inst, []string{"v=1", "fp=" + fpA}, 1, "t.local.")(b)
		}
	}
	if h := parseAnswer(answerFrom(t, 5, flood), from, 5, locals); len(h) > maxRecords/2 {
		t.Errorf("flood: %d hosts", len(h))
	}
}

func TestMerge(t *testing.T) {
	a := Host{Name: "a", Fingerprint: fpA, Addr: netip.MustParseAddrPort("192.168.1.2:1")}
	a2 := Host{Name: "a", Fingerprint: fpA, Addr: netip.MustParseAddrPort("10.9.0.2:1"), ID: "418203775"}
	b := Host{Name: "B", Fingerprint: fpB}
	got := sortHosts(merge(merge(nil, []Host{a}), []Host{a2, b}))
	if len(got) != 2 || got[0].Addr != a.Addr || got[0].ID != "418203775" || got[1].Name != "B" {
		t.Errorf("got %+v", got)
	}
	var many []Host
	for i := range maxHosts + 10 {
		many = append(many, Host{Fingerprint: string(rune(i))})
	}
	if n := len(merge(nil, many)); n != maxHosts {
		t.Errorf("kept %d hosts", n)
	}
}

func TestLimiter(t *testing.T) {
	l := &limiter{rate: 10, burst: 3}
	now := time.Unix(1000, 0)
	n := 0
	for range 10 {
		if l.allow(now) {
			n++
		}
	}
	if n != 3 {
		t.Errorf("burst: %d", n)
	}
	if !l.allow(now.Add(100*time.Millisecond)) || l.allow(now.Add(100*time.Millisecond)) {
		t.Error("refill")
	}
}

// fakeNet is an in-memory network: datagrams to the mDNS group reach every
// socket in it, others the socket at their address.
type fakeNet struct {
	mu      sync.Mutex
	conns   map[netip.AddrPort]*fakeConn
	members map[*fakeConn]bool
}

type packet struct {
	b       []byte
	src     netip.AddrPort
	toGroup bool
}

type fakeConn struct {
	n      *fakeNet
	addr   netip.AddrPort
	in     chan packet
	wake   chan struct{}
	done   chan struct{}
	closed atomic.Bool

	mu       sync.Mutex
	deadline time.Time
}

func newFakeNet() *fakeNet {
	return &fakeNet{conns: map[netip.AddrPort]*fakeConn{}, members: map[*fakeConn]bool{}}
}

func (n *fakeNet) open(addr string, member bool) *fakeConn {
	c := &fakeConn{n: n, addr: netip.MustParseAddrPort(addr), in: make(chan packet, 64), wake: make(chan struct{}, 1), done: make(chan struct{})}
	n.mu.Lock()
	n.conns[c.addr] = c
	if member {
		n.members[c] = true
	}
	n.mu.Unlock()
	return c
}

// send delivers a datagram from src as if it came over the network.
func (n *fakeNet) send(b []byte, src, dst netip.AddrPort) {
	n.mu.Lock()
	var to []*fakeConn
	if dst == group {
		for c := range n.members {
			to = append(to, c)
		}
	} else if c := n.conns[dst]; c != nil {
		to = append(to, c)
	}
	n.mu.Unlock()
	for _, c := range to {
		select {
		case c.in <- packet{append([]byte(nil), b...), src, dst == group}:
		default:
		}
	}
}

type timeoutError struct{}

func (timeoutError) Error() string { return "i/o timeout" }
func (timeoutError) Timeout() bool { return true }

func (c *fakeConn) readFrom(b []byte) (int, netip.AddrPort, bool, error) {
	for {
		c.mu.Lock()
		dl := c.deadline
		c.mu.Unlock()
		var timeout <-chan time.Time
		if !dl.IsZero() {
			d := time.Until(dl)
			if d <= 0 {
				return 0, netip.AddrPort{}, false, timeoutError{}
			}
			tm := time.NewTimer(d)
			defer tm.Stop()
			timeout = tm.C
		}
		select {
		case p := <-c.in:
			return copy(b, p.b), p.src, p.toGroup, nil
		case <-timeout:
			return 0, netip.AddrPort{}, false, timeoutError{}
		case <-c.wake:
		case <-c.done:
			return 0, netip.AddrPort{}, false, net.ErrClosed
		}
	}
}

func (c *fakeConn) writeTo(b []byte, dst netip.AddrPort) error {
	if c.closed.Load() {
		return net.ErrClosed
	}
	c.n.send(b, c.addr, dst)
	return nil
}

func (c *fakeConn) setReadDeadline(t time.Time) error {
	c.mu.Lock()
	c.deadline = t
	c.mu.Unlock()
	select {
	case c.wake <- struct{}{}:
	default:
	}
	return nil
}

func (c *fakeConn) close() error {
	if c.closed.Swap(true) {
		return nil
	}
	c.n.mu.Lock()
	delete(c.n.conns, c.addr)
	delete(c.n.members, c)
	c.n.mu.Unlock()
	close(c.done)
	return nil
}

// TestBrowseFakeNetwork runs responders and a browser on an in-memory
// network: deterministic, unlike multicast on a real machine.
func TestBrowseFakeNetwork(t *testing.T) {
	n := newFakeNet()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	defer func() { cancel(); wg.Wait() }()
	run := func(addr string, prefix string, info func() (Info, bool)) {
		listen := func() (packetConn, error) { return n.open(addr, true), nil }
		mine := []netip.Prefix{netip.MustParsePrefix(prefix)}
		wg.Go(func() { serve(ctx, info, nil, listen, func() []netip.Prefix { return mine }) })
	}
	fixed := func(i Info) func() (Info, bool) { return func() (Info, bool) { return i, true } }
	// Two hosts on the browser's subnet, one of them yet without an ID.
	run("192.168.1.20:5353", "192.168.1.20/24", fixed(hostInfo))
	run("192.168.1.21:5353", "192.168.1.21/24", fixed(Info{Name: "nas", Fingerprint: fpB, Port: 4000}))
	// One whose subnets do not include the browser: it must not answer.
	run("192.168.1.22:5353", "10.0.0.22/24", fixed(Info{Name: "far", Fingerprint: "aaaa:aaaa:aaaa:aaaa:aaaa", Port: 1}))
	// One whose owner turned discovery off.
	run("192.168.1.23:5353", "192.168.1.23/24", func() (Info, bool) {
		return Info{Name: "private", Fingerprint: "bbbb:bbbb:bbbb:bbbb:bbbb", Port: 1}, false
	})
	// The responders register as they start.
	for i := 0; ; i++ {
		n.mu.Lock()
		m := len(n.members)
		n.mu.Unlock()
		if m == 3 {
			break
		}
		if i > 200 {
			t.Fatalf("%d responders listening", m)
		}
		time.Sleep(10 * time.Millisecond)
	}

	browser := n.open("192.168.1.33:40000", false)
	defer browser.close()
	hosts, err := browse(context.Background(), browser, []netip.Prefix{netip.MustParsePrefix("192.168.1.33/24")}, 400*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	want := []Host{
		{Name: "nas", Addr: netip.MustParseAddrPort("192.168.1.21:4000"), Fingerprint: fpB},
		{Name: "studio", Addr: netip.MustParseAddrPort("192.168.1.20:21118"), ID: "418203775", Fingerprint: fpA},
	}
	if len(hosts) != len(want) || hosts[0] != want[0] || hosts[1] != want[1] {
		t.Fatalf("found %+v\nwant  %+v", hosts, want)
	}

	// A unicast question to a responder's port 5353 is not answered.
	n.send(query(t, 9, q(Service, dnsmessage.TypePTR)), netip.MustParseAddrPort("192.168.1.33:40001"), netip.MustParseAddrPort("192.168.1.20:5353"))
	probe := n.open("192.168.1.33:40001", false)
	defer probe.close()
	probe.setReadDeadline(time.Now().Add(200 * time.Millisecond))
	if _, _, _, err := probe.readFrom(make([]byte, 1500)); !isTimeout(err) {
		t.Errorf("unicast question answered (%v)", err)
	}

	// A browse ends when its context does.
	bctx, bcancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer bcancel()
	start := time.Now()
	if _, err := browse(bctx, browser, nil, 5*time.Second); !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 2*time.Second {
		t.Errorf("cancelled browse: %v after %v", err, time.Since(start))
	}
}

// TestMulticast announces and browses on this machine's real network
// interfaces. Multicast is often unavailable (containers without a
// multicast interface, CI runners, firewalls): the test then skips, and
// TestBrowseFakeNetwork covers the logic.
func TestMulticast(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	if len(multicastInterfaces()) == 0 {
		t.Skip("no interface with multicast")
	}
	c, err := listenMDNS()
	if err != nil {
		t.Skipf("cannot open the mDNS port: %v", err)
	}
	c.close()
	// A fingerprint of this run's own, to find among other hosts.
	pub, _, _ := ed25519.GenerateKey(nil)
	fp := auth.Fingerprint(pub)
	info := Info{Name: "lan test", Fingerprint: fp, Port: 21118}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); Serve(ctx, func() (Info, bool) { return info, true }, nil) }()
	defer func() { cancel(); <-done }()
	time.Sleep(100 * time.Millisecond)
	hosts, err := Browse(context.Background(), 1500*time.Millisecond)
	if err != nil {
		t.Skipf("browse: %v", err)
	}
	for _, h := range hosts {
		if h.Fingerprint == fp {
			if h.Name != "lan test" || h.Addr.Port() != 21118 || !onLink(h.Addr.Addr(), localPrefixes()) {
				t.Errorf("found %+v", h)
			}
			return
		}
	}
	t.Skipf("multicast does not loop back to this machine here (found %d other hosts)", len(hosts))
}
