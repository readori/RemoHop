package portmap

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Fakes of the three kinds of router, on loopback.

var fakePublic = netip.MustParseAddr("203.0.113.5")

// testEnv points a Mapper at fakes: the gateway and SSDP addresses given,
// loopback accepted as a LAN address, and every wait short.
func testEnv(gw func() (netip.Addr, error), pmpPort uint16, ssdp netip.AddrPort) env {
	e := defaultEnv()
	e.gateway = gw
	e.pmpPort = pmpPort
	e.ssdp = ssdp
	e.lanOK = func(ip netip.Addr) bool { return lanAddr(ip) || ip.IsLoopback() }
	e.pmpWait = 300 * time.Millisecond
	e.ssdpWait = 400 * time.Millisecond
	e.timeout = 2 * time.Second
	e.minRenew = 50 * time.Millisecond
	e.minRetry = 100 * time.Millisecond
	e.maxRetry = 400 * time.Millisecond
	return e
}

func loopbackGateway() (netip.Addr, error) { return netip.MustParseAddr("127.0.0.1"), nil }

func noGateway() (netip.Addr, error) { return netip.Addr{}, errors.New("no gateway in this test") }

// closedPort is a loopback UDP port nobody listens on.
func closedPort(t *testing.T) uint16 {
	t.Helper()
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	port := uint16(c.LocalAddr().(*net.UDPAddr).Port)
	c.Close()
	return port
}

// waitMapping waits for m to hold a mapping.
func waitMapping(t *testing.T, m *Mapper) Mapping {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cur, ok := m.Mapping(); ok {
			return cur
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no mapping")
	return Mapping{}
}

// fakePMP is a gateway's PCP and NAT-PMP port.
type fakePMP struct {
	t    *testing.T
	pc   *net.UDPConn
	port uint16

	mu       sync.Mutex
	pcp      bool   // answer PCP (else answer it as a NAT-PMP-only router does)
	pmp      bool   // answer NAT-PMP
	silent   bool   // answer nothing
	lifetime uint32 // the most granted, in seconds; 0 grants what is asked
	reqs     []pmpReq
}

type pmpReq struct {
	proto    string // ProtoPCP or ProtoNATPMP
	lifetime uint32
	extPort  uint16
	nonce    [12]byte
}

func newFakePMP(t *testing.T) *fakePMP {
	t.Helper()
	pc, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	f := &fakePMP{t: t, pc: pc, port: uint16(pc.LocalAddr().(*net.UDPAddr).Port)}
	done := make(chan struct{})
	go f.serve(done)
	t.Cleanup(func() { pc.Close(); <-done })
	return f
}

func (f *fakePMP) set(fn func(f *fakePMP)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *fakePMP) requests() []pmpReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]pmpReq(nil), f.reqs...)
}

func (f *fakePMP) grant(asked uint32) uint32 {
	if f.lifetime != 0 && asked > f.lifetime {
		return f.lifetime
	}
	return asked
}

func (f *fakePMP) serve(done chan struct{}) {
	defer close(done)
	buf := make([]byte, 1500)
	for {
		n, from, err := f.pc.ReadFromUDPAddrPort(buf)
		if err != nil {
			return
		}
		if resp := f.answer(buf[:n]); resp != nil {
			f.pc.WriteToUDPAddrPort(resp, from)
		}
	}
}

func (f *fakePMP) answer(b []byte) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.silent || len(b) < 2 {
		return nil
	}
	switch {
	case b[0] == pcpVersion && len(b) == pcpMapLen && b[1] == pcpOpMap:
		if !f.pcp {
			// What a NAT-PMP-only router says to a version it does
			// not know (RFC 6886 3.5).
			return []byte{0, pmpResponse | b[1], 0, 1, 0, 0, 0, 0}
		}
		lifetime := binary.BigEndian.Uint32(b[4:8])
		ext := binary.BigEndian.Uint16(b[42:44])
		f.reqs = append(f.reqs, pmpReq{proto: ProtoPCP, lifetime: lifetime, extPort: ext, nonce: [12]byte(b[24:36])})
		if ext == 0 {
			ext = binary.BigEndian.Uint16(b[40:42])
		}
		resp := make([]byte, pcpMapLen)
		resp[0], resp[1] = pcpVersion, pcpResponse|pcpOpMap
		binary.BigEndian.PutUint32(resp[4:8], f.grant(lifetime))
		binary.BigEndian.PutUint32(resp[8:12], 1234) // epoch
		copy(resp[24:44], b[24:44])                  // nonce, protocol, internal port
		binary.BigEndian.PutUint16(resp[42:44], ext)
		e := fakePublic.As16()
		copy(resp[44:60], e[:])
		return resp
	case b[0] == pmpVersion && b[1] == pmpOpExtAddr && len(b) == 2 && f.pmp:
		resp := make([]byte, pmpExtAddrLen)
		resp[1] = pmpResponse | pmpOpExtAddr
		ip := fakePublic.As4()
		copy(resp[8:12], ip[:])
		return resp
	case b[0] == pmpVersion && b[1] == pmpOpMapUDP && len(b) == 12 && f.pmp:
		internal := binary.BigEndian.Uint16(b[4:6])
		ext := binary.BigEndian.Uint16(b[6:8])
		lifetime := binary.BigEndian.Uint32(b[8:12])
		f.reqs = append(f.reqs, pmpReq{proto: ProtoNATPMP, lifetime: lifetime, extPort: ext})
		if ext == 0 {
			ext = internal
		}
		resp := make([]byte, pmpMapLen)
		resp[1] = pmpResponse | pmpOpMapUDP
		binary.BigEndian.PutUint16(resp[8:10], internal)
		binary.BigEndian.PutUint16(resp[10:12], ext)
		binary.BigEndian.PutUint32(resp[12:16], f.grant(lifetime))
		return resp
	}
	return nil
}

// fakeIGD is a UPnP gateway: an SSDP responder and its HTTP server.
type fakeIGD struct {
	t    *testing.T
	srv  *httptest.Server
	ssdp *net.UDPConn

	mu            sync.Mutex
	location      string // what SSDP answers; default the server's description
	permanentOnly bool   // refuse leases (some routers do)
	taken         map[uint16]bool
	mappings      map[uint16]string // external port -> "client:port lease"
	adds, deletes []string
	fetches       int
}

const fakeDescription = `<?xml version="1.0"?>
<root xmlns="urn:schemas-upnp-org:device-1-0">
 <specVersion><major>1</major><minor>0</minor></specVersion>
 <device>
  <deviceType>urn:schemas-upnp-org:device:InternetGatewayDevice:1</deviceType>
  <serviceList><service>
   <serviceType>urn:schemas-upnp-org:service:Layer3Forwarding:1</serviceType>
   <controlURL>/l3f</controlURL>
  </service></serviceList>
  <deviceList><device>
   <deviceType>urn:schemas-upnp-org:device:WANDevice:1</deviceType>
   <deviceList><device>
    <deviceType>urn:schemas-upnp-org:device:WANConnectionDevice:1</deviceType>
    <serviceList>
     <service>
      <serviceType>urn:schemas-upnp-org:service:WANPPPConnection:1</serviceType>
      <controlURL>/ppp</controlURL>
     </service>
     <service>
      <serviceType>urn:schemas-upnp-org:service:WANIPConnection:1</serviceType>
      <controlURL>ctl/IPConn</controlURL>
     </service>
    </serviceList>
   </device></deviceList>
  </device></deviceList>
 </device>
</root>`

func newFakeIGD(t *testing.T) *fakeIGD {
	t.Helper()
	f := &fakeIGD{t: t, taken: map[uint16]bool{}, mappings: map[uint16]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /rootDesc.xml", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.fetches++
		f.mu.Unlock()
		w.Header().Set("Content-Type", "text/xml")
		io.WriteString(w, fakeDescription)
	})
	mux.HandleFunc("POST /ctl/IPConn", f.control)
	mux.HandleFunc("POST /ppp", func(w http.ResponseWriter, r *http.Request) {
		t.Error("the WANPPPConnection service was used although WANIPConnection works")
		http.Error(w, "no", http.StatusInternalServerError)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)

	pc, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	f.ssdp = pc
	done := make(chan struct{})
	go f.serveSSDP(done)
	t.Cleanup(func() { pc.Close(); <-done })
	return f
}

func (f *fakeIGD) ssdpAddr() netip.AddrPort {
	return f.ssdp.LocalAddr().(*net.UDPAddr).AddrPort()
}

func (f *fakeIGD) set(fn func(f *fakeIGD)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *fakeIGD) serveSSDP(done chan struct{}) {
	defer close(done)
	buf := make([]byte, 1500)
	for {
		n, from, err := f.ssdp.ReadFromUDPAddrPort(buf)
		if err != nil {
			return
		}
		req := string(buf[:n])
		if !strings.HasPrefix(req, "M-SEARCH * HTTP/1.1\r\n") || !strings.Contains(req, `MAN: "ssdp:discover"`) {
			f.t.Errorf("malformed M-SEARCH %q", req)
			continue
		}
		var st string
		for _, line := range strings.Split(req, "\r\n") {
			if v, ok := strings.CutPrefix(line, "ST: "); ok {
				st = v
			}
		}
		f.mu.Lock()
		loc := f.location
		f.mu.Unlock()
		if loc == "" {
			loc = f.srv.URL + "/rootDesc.xml"
		}
		resp := "HTTP/1.1 200 OK\r\nCACHE-CONTROL: max-age=120\r\nST: " + st + "\r\nUSN: uuid:fake::" + st +
			"\r\nEXT:\r\nSERVER: fake/1.0 UPnP/1.1 fake/1.0\r\nLOCATION: " + loc + "\r\n\r\n"
		f.ssdp.WriteToUDPAddrPort([]byte(resp), from)
	}
}

func (f *fakeIGD) control(w http.ResponseWriter, r *http.Request) {
	const svc = "urn:schemas-upnp-org:service:WANIPConnection:1"
	action, ok := strings.CutPrefix(strings.Trim(r.Header.Get("SOAPAction"), `"`), svc+"#")
	body, _ := io.ReadAll(r.Body)
	args, err := soapFields(body)
	if !ok || err != nil {
		f.t.Errorf("bad SOAP request: %q %v", r.Header.Get("SOAPAction"), err)
		http.Error(w, "bad", http.StatusBadRequest)
		return
	}
	fault := func(code int, desc string) {
		w.Header().Set("Content-Type", `text/xml; charset="utf-8"`)
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><s:Fault>`+
			`<faultcode>s:Client</faultcode><faultstring>UPnPError</faultstring><detail>`+
			`<UPnPError xmlns="urn:schemas-upnp-org:control-1-0"><errorCode>%d</errorCode><errorDescription>%s</errorDescription></UPnPError>`+
			`</detail></s:Fault></s:Body></s:Envelope>`, code, desc)
	}
	reply := func(inner string) {
		w.Header().Set("Content-Type", `text/xml; charset="utf-8"`)
		fmt.Fprintf(w, `<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body>`+
			`<u:%sResponse xmlns:u="%s">%s</u:%sResponse></s:Body></s:Envelope>`, action, svc, inner, action)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch action {
	case "GetExternalIPAddress":
		reply("<NewExternalIPAddress>" + fakePublic.String() + "</NewExternalIPAddress>")
	case "AddPortMapping":
		port, _ := strconv.Atoi(args["NewExternalPort"])
		desc := args["NewInternalClient"] + ":" + args["NewInternalPort"] + " " + args["NewLeaseDuration"]
		f.adds = append(f.adds, args["NewExternalPort"]+" "+desc)
		switch {
		case args["NewProtocol"] != "UDP" || args["NewEnabled"] != "1" || args["NewRemoteHost"] != "":
			fault(402, "Invalid Args")
		case f.permanentOnly && args["NewLeaseDuration"] != "0":
			fault(faultPermanentLease, "OnlyPermanentLeasesSupported")
		case f.taken[uint16(port)]:
			fault(faultConflict, "ConflictInMappingEntry")
		default:
			f.mappings[uint16(port)] = desc
			reply("")
		}
	case "DeletePortMapping":
		port, _ := strconv.Atoi(args["NewExternalPort"])
		f.deletes = append(f.deletes, args["NewExternalPort"])
		if _, ok := f.mappings[uint16(port)]; !ok {
			fault(714, "NoSuchEntryInArray")
			return
		}
		delete(f.mappings, uint16(port))
		reply("")
	default:
		fault(401, "Invalid Action")
	}
}
