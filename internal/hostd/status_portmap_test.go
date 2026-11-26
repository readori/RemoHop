package hostd

import (
	"encoding/json"
	"net/netip"
	"strings"
	"testing"

	"github.com/readori/RemoHop/internal/portmap"
)

func fakeMapping(proto, ext string) func() (portmap.Mapping, bool) {
	return func() (portmap.Mapping, bool) {
		return portmap.Mapping{Protocol: proto, External: netip.MustParseAddrPort(ext)}, true
	}
}

func TestPortMapStatus(t *testing.T) {
	h := &Host{}
	if got := h.portMapStatus(); got != nil {
		t.Fatalf("no mapper: %+v", got)
	}
	h.mappingFn = func() (portmap.Mapping, bool) { return portmap.Mapping{}, false }
	if got := h.portMapStatus(); got != nil {
		t.Fatalf("no mapping: %+v", got)
	}
	r := netip.MustParseAddrPort("203.0.113.5:55555")
	h.reflexive.Store(&r)
	h.mappingFn = fakeMapping(portmap.ProtoNATPMP, "203.0.113.5:41641")
	got := h.portMapStatus()
	if got == nil || got.Protocol != "NAT-PMP" || got.External != "203.0.113.5:41641" || !got.Offered {
		t.Fatalf("same address: %+v", got)
	}
	h.mappingFn = fakeMapping(portmap.ProtoUPnP, "198.51.100.7:41641")
	got = h.portMapStatus()
	if got == nil || got.Protocol != "UPnP" || got.External != "198.51.100.7:41641" || got.Offered {
		t.Fatalf("different address: %+v", got)
	}
}

func TestPortMapStatusJSON(t *testing.T) {
	b, _ := json.Marshal(Status{})
	if strings.Contains(string(b), "port_map") {
		t.Fatalf("port_map present when nil: %s", b)
	}
	b, _ = json.Marshal(Status{PortMap: &PortMapStatus{Protocol: "PCP", External: "1.2.3.4:5", Offered: true}})
	for _, k := range []string{`"port_map":{`, `"protocol":"PCP"`, `"external":"1.2.3.4:5"`, `"offered":true`} {
		if !strings.Contains(string(b), k) {
			t.Errorf("missing %s in %s", k, b)
		}
	}
}

func TestPortMapLine(t *testing.T) {
	if l := portMapLine(nil); l != "" {
		t.Errorf("nil: %q", l)
	}
	if l := portMapLine(&PortMapStatus{Protocol: "PCP", External: "1.2.3.4:5", Offered: true}); l != "  Router port: PCP 1.2.3.4:5 (offered to clients)" {
		t.Errorf("offered: %q", l)
	}
	if l := portMapLine(&PortMapStatus{Protocol: "UPnP", External: "1.2.3.4:5"}); l != "  Router port: UPnP 1.2.3.4:5 (not offered: the router is behind another NAT)" {
		t.Errorf("not offered: %q", l)
	}
}
