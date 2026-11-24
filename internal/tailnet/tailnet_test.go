package tailnet

import (
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
)

// A trimmed `tailscale status --json` of a machine on a tailnet with two
// peers. Names and addresses are made up.
const sample = `{
  "Version": "1.70.0-t1234",
  "BackendState": "Running",
  "TailscaleIPs": ["100.101.1.1", "fd7a:115c:a1e0::1"],
  "Self": {"HostName": "laptop", "DNSName": "laptop.tail0000.ts.net.", "OS": "linux",
           "TailscaleIPs": ["100.101.1.1", "fd7a:115c:a1e0::1"], "Online": true},
  "MagicDNSSuffix": "tail0000.ts.net",
  "CurrentTailnet": {"Name": "example.com", "MagicDNSSuffix": "tail0000.ts.net", "MagicDNSEnabled": true},
  "Peer": {
    "nodekey:aa": {"HostName": "nas", "DNSName": "nas.tail0000.ts.net.", "OS": "linux",
                   "TailscaleIPs": ["100.101.1.2", "fd7a:115c:a1e0::2"], "Online": true, "ExitNodeOption": true, "CurAddr": "192.0.2.7:41641"},
    "nodekey:bb": {"HostName": "phone", "DNSName": "phone.tail0000.ts.net.", "OS": "iOS",
                   "TailscaleIPs": ["100.101.1.3"], "Online": false, "ExitNode": true, "Relay": "fra"}
  }
}`

func TestParse(t *testing.T) {
	st, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	if !st.Running || st.Tailnet != "example.com" || !st.MagicDNS || st.MagicDNSSuffix != "tail0000.ts.net" {
		t.Fatalf("status fields: %+v", st)
	}
	if st.Self.Name != "laptop" || len(st.Self.IPs) != 2 {
		t.Fatalf("self: %+v", st.Self)
	}
	if len(st.Peers) != 2 || st.ExitNode != "phone" {
		t.Fatalf("peers %+v exit %q", st.Peers, st.ExitNode)
	}
	if p, ok := st.Peer(netip.MustParseAddr("fd7a:115c:a1e0::2")); !ok || p.Name != "nas" || !p.ExitNodeOption {
		t.Fatalf("peer lookup: %+v %v", p, ok)
	}
	if !st.IsSelf(netip.MustParseAddr("100.101.1.1")) || st.IsSelf(netip.MustParseAddr("100.101.1.2")) {
		t.Fatal("IsSelf")
	}
	if _, err := Parse([]byte(`{"foo": 1}`)); err == nil {
		t.Fatal("accepted something that is not a status")
	}
	if _, err := Parse([]byte(`not json`)); err == nil {
		t.Fatal("accepted garbage")
	}
}

func TestStopped(t *testing.T) {
	st, err := Parse([]byte(`{"BackendState": "Stopped", "TailscaleIPs": ["100.101.1.1"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if st.Running {
		t.Fatal("a stopped Tailscale counts as running")
	}
	if Predicate(st) != nil {
		t.Fatal("a stopped Tailscale yields a preference")
	}
}

func TestInRangeAndInterface(t *testing.T) {
	for s, want := range map[string]bool{
		"100.64.0.1": true, "100.127.255.254": true, "100.128.0.1": false, "10.0.0.1": false,
		"fd7a:115c:a1e0::5": true, "fd7a:115c:a1e1::5": false, "::ffff:100.100.1.1": true,
	} {
		if got := InRange(netip.MustParseAddr(s)); got != want {
			t.Errorf("InRange(%s) = %v", s, got)
		}
	}
	ts := []netip.Addr{netip.MustParseAddr("100.100.1.1")}
	lan := []netip.Addr{netip.MustParseAddr("192.168.1.2")}
	cases := []struct {
		name  string
		addrs []netip.Addr
		want  bool
	}{
		{"tailscale0", ts, true},
		{"Tailscale", ts, true},
		{"utun4", ts, true},
		{"utun4", lan, false}, // another VPN's utun
		{"eth0", ts, false},   // a carrier-grade NAT address on the uplink
		{"tailscale0", nil, false},
	}
	for _, c := range cases {
		if got := IsInterface(c.name, c.addrs); got != c.want {
			t.Errorf("IsInterface(%s, %v) = %v", c.name, c.addrs, got)
		}
	}
}

func TestPredicate(t *testing.T) {
	st, _ := Parse([]byte(sample))
	st.Source = SourceCLI
	p := Predicate(st)
	ap := func(s string) netip.AddrPort { return netip.MustParseAddrPort(s) }
	if !p(ap("100.101.1.2:4000")) || !p(ap("[fd7a:115c:a1e0::2]:4000")) {
		t.Fatal("a listed peer's address is not preferred")
	}
	// In range but not on this tailnet: another tailnet, or a carrier.
	if p(ap("100.101.9.9:4000")) || p(ap("192.168.1.2:4000")) {
		t.Fatal("an address that is not a peer's is preferred")
	}
	// Only an interface: anything in range except our own address.
	st = &Status{Running: true, Source: SourceInterface, Self: Node{IPs: []netip.Addr{netip.MustParseAddr("100.101.1.1")}}}
	p = Predicate(st)
	if !p(ap("100.101.9.9:4000")) || p(ap("100.101.1.1:4000")) || p(ap("10.0.0.1:4000")) {
		t.Fatal("interface-only predicate")
	}
}

func TestDetectFromEnvAndDisable(t *testing.T) {
	f := filepath.Join(t.TempDir(), "status.json")
	if err := os.WriteFile(f, []byte(sample), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REMOHOP_TAILSCALE_STATUS", f)
	t.Setenv("REMOHOP_TAILSCALE_SOCKET", filepath.Join(t.TempDir(), "none"))
	ResetCache()
	st := Detect(context.Background())
	if !st.Running || st.Source != SourceEnv || len(st.Peers) != 2 {
		t.Fatalf("Detect from file: %+v", st)
	}
	remote := []netip.AddrPort{netip.MustParseAddrPort("192.168.1.9:1"), netip.MustParseAddrPort("100.101.1.2:1")}
	p := Prefer(context.Background(), remote)
	if p == nil || !p(remote[1]) || p(remote[0]) {
		t.Fatal("Prefer with a status file")
	}
	if Prefer(context.Background(), remote[:1]) != nil {
		t.Fatal("Prefer without tailnet candidates")
	}
	t.Setenv("REMOHOP_TAILSCALE", "off")
	ResetCache()
	if st := Detect(context.Background()); st.Running || st.Source != SourceDisabled {
		t.Fatalf("REMOHOP_TAILSCALE=off: %+v", st)
	}
	if Prefer(context.Background(), remote) != nil {
		t.Fatal("Prefer while disabled")
	}
	ResetCache()
}
