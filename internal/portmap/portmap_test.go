package portmap

import (
	"bytes"
	"context"
	"encoding/binary"
	"log/slog"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testPort = 41641

func TestPCPMapRequest(t *testing.T) {
	nonce := [12]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
	b := pcpMapRequest(netip.MustParseAddr("192.168.1.20"), nonce, testPort, 50000, netip.Addr{}, 7200)
	if len(b) != pcpMapLen || b[0] != 2 || b[1] != 1 || b[2] != 0 || b[3] != 0 {
		t.Fatalf("header % x", b[:4])
	}
	if binary.BigEndian.Uint32(b[4:8]) != 7200 {
		t.Error("lifetime")
	}
	if got := netip.AddrFrom16([16]byte(b[8:24])); got != netip.MustParseAddr("::ffff:192.168.1.20") {
		t.Errorf("client address %s, want it IPv4-mapped", got)
	}
	if !bytes.Equal(b[24:36], nonce[:]) || b[36] != protoUDP || !bytes.Equal(b[37:40], []byte{0, 0, 0}) {
		t.Errorf("opcode data % x", b[24:40])
	}
	if binary.BigEndian.Uint16(b[40:42]) != testPort || binary.BigEndian.Uint16(b[42:44]) != 50000 {
		t.Error("ports")
	}
	if got := netip.AddrFrom16([16]byte(b[44:60])); got != netip.MustParseAddr("::ffff:0.0.0.0") {
		t.Errorf("suggested external address %s, want any IPv4 (::ffff:0.0.0.0)", got)
	}

	// The answer decodes; truncated, oversized, misaligned or foreign
	// packets do not.
	resp := append([]byte(nil), b...)
	resp[1] |= pcpResponse
	resp[3] = 0
	binary.BigEndian.PutUint32(resp[8:12], 99)
	e := fakePublic.As16()
	copy(resp[44:60], e[:])
	r, err := parsePCPMap(resp)
	if err != nil || r.nonce != nonce || r.lifetime != 7200 || r.epoch != 99 || r.extPort != 50000 || r.extIP != fakePublic || r.internal != testPort || r.proto != protoUDP {
		t.Fatalf("parsePCPMap: %+v, %v", r, err)
	}
	for name, p := range map[string][]byte{
		"short":     resp[:56],
		"unaligned": append(append([]byte(nil), resp...), 0),
		"oversized": append(append([]byte(nil), resp...), make([]byte, 1100)...),
		"request":   b,
		"NAT-PMP":   append([]byte{0}, resp[1:]...),
	} {
		if _, err := parsePCPMap(p); err == nil {
			t.Errorf("%s packet accepted", name)
		}
	}
	if pcpResultText(2) != "not authorized" || pcpResultText(200) != "result code 200" {
		t.Error("pcpResultText")
	}
}

func TestNATPMPMessages(t *testing.T) {
	if got := pmpMapRequest(testPort, 50000, 3600); !bytes.Equal(got, []byte{0, 1, 0, 0, 0xa2, 0xa9, 0xc3, 0x50, 0, 0, 0x0e, 0x10}) {
		t.Fatalf("pmpMapRequest = % x", got)
	}
	code, ip, err := parsePMPExtAddr([]byte{0, 128, 0, 0, 0, 0, 0, 7, 203, 0, 113, 5})
	if err != nil || code != 0 || ip != fakePublic {
		t.Fatalf("parsePMPExtAddr: %d %s %v", code, ip, err)
	}
	r, err := parsePMPMap([]byte{0, 129, 0, 0, 0, 0, 0, 7, 0xa2, 0xa9, 0xc3, 0x51, 0, 0, 0x0e, 0x10})
	if err != nil || r.result != 0 || r.internal != testPort || r.extPort != 50001 || r.lifetime != 3600 || r.epoch != 7 {
		t.Fatalf("parsePMPMap: %+v %v", r, err)
	}
	if _, err := parsePMPMap([]byte{0, 128, 0, 0, 0, 0, 0, 7, 0xa2, 0xa9, 0xc3, 0x51, 0, 0, 0x0e, 0x10}); err == nil {
		t.Error("an external address answer taken for a mapping")
	}
	if _, _, err := parsePMPExtAddr([]byte{0, 128, 0, 0}); err == nil {
		t.Error("short answer accepted")
	}
}

func TestSSDPAndDescription(t *testing.T) {
	loc, err := parseSSDP([]byte("HTTP/1.1 200 OK\r\nST: urn:schemas-upnp-org:service:WANIPConnection:1\r\nlocation: http://192.168.1.1:5000/rootDesc.xml\r\n\r\n"))
	if err != nil || loc != "http://192.168.1.1:5000/rootDesc.xml" {
		t.Fatalf("parseSSDP: %q %v", loc, err)
	}
	for _, p := range []string{"NOTIFY * HTTP/1.1\r\n\r\n", "HTTP/1.1 404 Not Found\r\nLOCATION: http://192.168.1.1/\r\n\r\n", "HTTP/1.1 200 OK\r\n\r\n", "garbage"} {
		if _, err := parseSSDP([]byte(p)); err == nil {
			t.Errorf("parseSSDP accepted %q", p)
		}
	}

	base, svcs, err := parseDescription([]byte(fakeDescription))
	if err != nil || base != "" || len(svcs) != 2 {
		t.Fatalf("parseDescription: %q %v %v", base, svcs, err)
	}
	if svcs[0].typ != wanServices[1] || svcs[0].control != "ctl/IPConn" || svcs[1].typ != wanServices[2] {
		t.Errorf("services not in order of preference: %v", svcs)
	}
	if _, _, err := parseDescription([]byte(`<root><device><serviceList><service><serviceType>urn:schemas-upnp-org:service:Layer3Forwarding:1</serviceType><controlURL>/x</controlURL></service></serviceList></device></root>`)); err == nil {
		t.Error("a description without a WAN service accepted")
	}

	f, err := soapFields([]byte(`<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><s:Fault><faultcode>s:Client</faultcode><detail><UPnPError xmlns="urn:schemas-upnp-org:control-1-0"><errorCode>718</errorCode><errorDescription>ConflictInMappingEntry</errorDescription></UPnPError></detail></s:Fault></s:Body></s:Envelope>`))
	if err != nil || f["errorCode"] != "718" || f["errorDescription"] != "ConflictInMappingEntry" {
		t.Fatalf("soapFields: %v %v", f, err)
	}
	body := string(soapBody(wanServices[1], "AddPortMapping", [][2]string{{"NewPortMappingDescription", "a<b&c"}}))
	if !strings.Contains(body, `<u:AddPortMapping xmlns:u="urn:schemas-upnp-org:service:WANIPConnection:1">`) || !strings.Contains(body, "a&lt;b&amp;c") {
		t.Errorf("soapBody: %s", body)
	}
}

// A URL from the network is fetched only if it is plain HTTP to the LAN
// device that answered, by address.
func TestLANURL(t *testing.T) {
	m := &Mapper{env: defaultEnv()}
	dev := netip.MustParseAddr("192.168.1.1")
	if _, err := m.lanURL("http://192.168.1.1:5000/rootDesc.xml", dev); err != nil {
		t.Errorf("the router's own URL refused: %v", err)
	}
	for _, u := range []string{
		"http://8.8.8.8/rootDesc.xml",          // a public server
		"http://192.168.1.2/rootDesc.xml",      // another LAN device
		"http://127.0.0.1:631/",                // a service on this machine
		"http://router.lan/rootDesc.xml",       // a name could resolve anywhere
		"https://192.168.1.1/rootDesc.xml",     // not plain HTTP
		"http://admin:pw@192.168.1.1/",         // credentials
		"http://192.168.1.1:99999/",            // bad port
		"http://[fe80::1%25eth0]/rootDesc.xml", // zoned
		"file:///etc/passwd",
	} {
		if _, err := m.lanURL(u, dev); err == nil {
			t.Errorf("%s accepted", u)
		}
	}
	// A device on a public address is no router of ours.
	if _, err := m.lanURL("http://8.8.8.8/", netip.MustParseAddr("8.8.8.8")); err == nil {
		t.Error("public device accepted")
	}
	// And the HTTP client refuses to connect anywhere but the LAN,
	// whatever it is asked.
	_, err := m.httpClient().Get("http://8.8.8.8/rootDesc.xml")
	if err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Errorf("connection to a public address: %v", err)
	}
}

func TestPCP(t *testing.T) {
	gw := newFakePMP(t)
	gw.set(func(f *fakePMP) { f.pcp, f.pmp = true, true })
	var changes atomic.Int32
	m := start(Config{Port: testPort, OnChange: func() { changes.Add(1) }}, testEnv(loopbackGateway, gw.port, netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), closedPort(t))))
	cur := waitMapping(t, m)
	want := Mapping{Protocol: ProtoPCP, External: netip.AddrPortFrom(fakePublic, testPort), Internal: netip.MustParseAddrPort("127.0.0.1:41641"), Lifetime: 2 * time.Hour}
	if cur != want {
		t.Fatalf("mapping %+v, want %+v", cur, want)
	}
	if changes.Load() != 1 {
		t.Errorf("OnChange called %d times", changes.Load())
	}
	m.Close()
	if _, ok := m.Mapping(); ok {
		t.Error("mapping still reported after Close")
	}
	reqs := gw.requests()
	if len(reqs) != 2 || reqs[0].lifetime != 7200 || reqs[0].extPort != testPort || reqs[1].lifetime != 0 || reqs[1].nonce != reqs[0].nonce {
		t.Fatalf("requests %+v, want a mapping and its deletion with the same nonce", reqs)
	}
	for _, r := range reqs {
		if r.proto != ProtoPCP {
			t.Errorf("a %s request although PCP works", r.proto)
		}
	}
}

func TestNATPMP(t *testing.T) {
	gw := newFakePMP(t)
	gw.set(func(f *fakePMP) { f.pmp = true }) // PCP gets "unsupported version"
	m := start(Config{Port: testPort}, testEnv(loopbackGateway, gw.port, netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), closedPort(t))))
	cur := waitMapping(t, m)
	want := Mapping{Protocol: ProtoNATPMP, External: netip.AddrPortFrom(fakePublic, testPort), Internal: netip.MustParseAddrPort("127.0.0.1:41641"), Lifetime: 2 * time.Hour}
	if cur != want {
		t.Fatalf("mapping %+v, want %+v", cur, want)
	}
	m.Close()
	reqs := gw.requests()
	if len(reqs) != 2 || reqs[1].proto != ProtoNATPMP || reqs[1].lifetime != 0 || reqs[1].extPort != 0 {
		t.Fatalf("requests %+v, want a mapping and its deletion", reqs)
	}
}

func TestUPnP(t *testing.T) {
	igd := newFakeIGD(t)
	// No gateway known and nothing on the PCP port of the device that
	// answered SSDP: UPnP it is.
	m := start(Config{Port: testPort}, testEnv(noGateway, closedPort(t), igd.ssdpAddr()))
	cur := waitMapping(t, m)
	want := Mapping{Protocol: ProtoUPnP, External: netip.AddrPortFrom(fakePublic, testPort), Internal: netip.MustParseAddrPort("127.0.0.1:41641"), Lifetime: 2 * time.Hour}
	if cur != want {
		t.Fatalf("mapping %+v, want %+v", cur, want)
	}
	igd.set(func(f *fakeIGD) {
		if f.mappings[testPort] != "127.0.0.1:41641 7200" {
			t.Errorf("router holds %v", f.mappings)
		}
	})
	m.Close()
	igd.set(func(f *fakeIGD) {
		if len(f.mappings) != 0 || len(f.deletes) != 1 {
			t.Errorf("after Close the router holds %v (deletes %v)", f.mappings, f.deletes)
		}
	})
}

// A router that takes only permanent mappings, with the port we want taken
// by another machine: the mapping is made anyway, on another port, without
// expiry.
func TestUPnPPermanentAndConflict(t *testing.T) {
	igd := newFakeIGD(t)
	igd.set(func(f *fakeIGD) { f.permanentOnly, f.taken[testPort] = true, true })
	m := start(Config{Port: testPort}, testEnv(noGateway, closedPort(t), igd.ssdpAddr()))
	cur := waitMapping(t, m)
	if cur.Protocol != ProtoUPnP || cur.External.Addr() != fakePublic || cur.External.Port() == testPort || cur.External.Port() < 1024 || cur.Lifetime != 0 {
		t.Fatalf("mapping %+v", cur)
	}
	m.Close()
	igd.set(func(f *fakeIGD) {
		if len(f.mappings) != 0 {
			t.Errorf("after Close the router holds %v", f.mappings)
		}
	})
}

// A LOCATION that points anywhere but the LAN device that answered is not
// fetched, and no mapping comes of it.
func TestUPnPRefusesForeignLocation(t *testing.T) {
	for _, loc := range []string{"http://8.8.8.8/rootDesc.xml", "http://192.168.77.1/rootDesc.xml", "http://localhost:1/rootDesc.xml"} {
		igd := newFakeIGD(t)
		igd.set(func(f *fakeIGD) { f.location = loc })
		e := testEnv(noGateway, closedPort(t), igd.ssdpAddr())
		e.lanOK = func(ip netip.Addr) bool { return lanAddr(ip) || ip.IsLoopback() } // the responder is on loopback
		m := &Mapper{cfg: Config{Port: testPort}, env: e, log: discard()}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		dev, err := m.discoverIGD(ctx)
		cancel()
		if err == nil || !strings.Contains(err.Error(), "refusing") {
			t.Errorf("LOCATION %s: device %+v, err %v", loc, dev, err)
		}
	}
	// With the real LAN test, a responder on loopback is ignored outright.
	igd := newFakeIGD(t)
	e := testEnv(noGateway, closedPort(t), igd.ssdpAddr())
	e.lanOK = lanAddr
	m := &Mapper{cfg: Config{Port: testPort}, env: e, log: discard()}
	if dev, err := m.discoverIGD(context.Background()); err == nil {
		t.Errorf("a responder on loopback was used: %+v", dev)
	}
	igd.set(func(f *fakeIGD) {
		if f.fetches != 0 {
			t.Errorf("description fetched %d times", f.fetches)
		}
	})
}

// The lease is renewed at half its lifetime; a router that stops
// answering loses the mapping, and it comes back by itself once the router
// answers again.
func TestRenewal(t *testing.T) {
	gw := newFakePMP(t)
	gw.set(func(f *fakePMP) { f.pcp, f.lifetime = true, 1 }) // a lease of one second
	e := testEnv(loopbackGateway, gw.port, netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), closedPort(t)))
	e.pmpWait = 100 * time.Millisecond
	e.ssdpWait = 100 * time.Millisecond
	m := start(Config{Port: testPort}, e)
	defer m.Close()
	if cur := waitMapping(t, m); cur.Lifetime != time.Second {
		t.Fatalf("lifetime %s", cur.Lifetime)
	}
	time.Sleep(1300 * time.Millisecond)
	reqs := gw.requests()
	if len(reqs) < 3 || len(reqs) > 5 {
		t.Fatalf("%d requests in 1.3 s for a one-second lease, want renewals every half second", len(reqs))
	}
	for _, r := range reqs[1:] {
		if r.nonce != reqs[0].nonce || r.extPort != testPort {
			t.Fatalf("a renewal is not the same mapping: %+v", reqs)
		}
	}

	gw.set(func(f *fakePMP) { f.silent = true })
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, ok := m.Mapping(); !ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("mapping still reported while the router is silent")
		}
		time.Sleep(10 * time.Millisecond)
	}
	gw.set(func(f *fakePMP) { f.silent = false })
	waitMapping(t, m)
}

// Refresh checks the mapping at once instead of at the next renewal.
func TestRefresh(t *testing.T) {
	gw := newFakePMP(t)
	gw.set(func(f *fakePMP) { f.pcp = true })
	m := start(Config{Port: testPort}, testEnv(loopbackGateway, gw.port, netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), closedPort(t))))
	defer m.Close()
	waitMapping(t, m)
	m.Refresh()
	deadline := time.Now().Add(2 * time.Second)
	for len(gw.requests()) < 2 {
		if time.Now().After(deadline) {
			t.Fatal("Refresh did not renew the mapping")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Without any router support the Mapper gives up quickly, keeps trying in
// the background, and Close returns at once.
func TestNoRouter(t *testing.T) {
	e := testEnv(loopbackGateway, closedPort(t), netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), closedPort(t)))
	m := start(Config{Port: testPort}, e)
	time.Sleep(600 * time.Millisecond)
	if cur, ok := m.Mapping(); ok {
		t.Fatalf("mapping %+v without a router", cur)
	}
	t0 := time.Now()
	m.Close()
	if d := time.Since(t0); d > 500*time.Millisecond {
		t.Errorf("Close took %s", d)
	}
}

func TestAllowed(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("REMOHOP_PORTMAP", "")
	if !Allowed(dir) {
		t.Error("off by default")
	}
	for _, v := range []string{"0", "off", "no", "false", "OFF"} {
		t.Setenv("REMOHOP_PORTMAP", v)
		if Allowed(dir) {
			t.Errorf("REMOHOP_PORTMAP=%s leaves it on", v)
		}
	}
}

func TestRenewAfter(t *testing.T) {
	m := &Mapper{env: defaultEnv()}
	for lifetime, want := range map[time.Duration]time.Duration{
		2 * time.Hour:    time.Hour,
		0:                time.Hour, // a permanent UPnP mapping is checked as often
		10 * time.Second: minRenew,  // a stingy router does not make us spin
	} {
		if got := m.renewAfter(lifetime); got != want {
			t.Errorf("renewAfter(%s) = %s, want %s", lifetime, got, want)
		}
	}
}

func TestLANAddr(t *testing.T) {
	for ip, want := range map[string]bool{
		"192.168.1.1": true, "10.0.0.138": true, "172.16.0.1": true, "169.254.1.1": true, "fe80::1": true, "fd00::1": true,
		"8.8.8.8": false, "127.0.0.1": false, "100.64.0.1": false, "::1": false, "2001:db8::1": false, "0.0.0.0": false,
	} {
		if got := lanAddr(netip.MustParseAddr(ip)); got != want {
			t.Errorf("lanAddr(%s) = %v", ip, got)
		}
	}
}

func discard() *slog.Logger { return slog.New(slog.DiscardHandler) }
