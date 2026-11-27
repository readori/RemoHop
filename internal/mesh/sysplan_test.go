package mesh

import (
	"errors"
	"net/netip"
	"reflect"
	"strings"
	"testing"
)

func TestDarwinConfigureCmds(t *testing.T) {
	a4 := netip.MustParsePrefix("10.78.3.4/16")
	a6 := netip.MustParsePrefix("fd12:3456:789a::5/64")
	v4, v6 := darwinConfigureCmds("utun7", a4, a6, 1400)
	want4 := [][]string{
		{"ifconfig", "utun7", "inet", "10.78.3.4", "10.78.3.4", "netmask", "255.255.255.255", "mtu", "1400", "up"},
		{"route", "-n", "add", "-net", "10.78.0.0/16", "-interface", "utun7"},
	}
	want6 := [][]string{
		{"ifconfig", "utun7", "inet6", "fd12:3456:789a::5", "prefixlen", "64"},
		{"route", "-n", "add", "-inet6", "-net", "fd12:3456:789a::/64", "-interface", "utun7"},
	}
	if !reflect.DeepEqual(v4, want4) {
		t.Errorf("v4 = %q\nwant %q", v4, want4)
	}
	if !reflect.DeepEqual(v6, want6) {
		t.Errorf("v6 = %q\nwant %q", v6, want6)
	}
	if _, v6 := darwinConfigureCmds("utun7", a4, netip.Prefix{}, 1400); v6 != nil {
		t.Errorf("no IPv6 address, yet IPv6 commands %q", v6)
	}
}

func TestWindowsConfigureCmds(t *testing.T) {
	a4 := netip.MustParsePrefix("10.78.3.4/16")
	a6 := netip.MustParsePrefix("fd12:3456:789a::5/64")
	v4, v6 := windowsConfigureCmds("23", a4, a6, 1400)
	want4 := [][]string{
		{"netsh", "interface", "ipv4", "add", "address", "name=23", "address=10.78.3.4", "mask=255.255.0.0", "store=active"},
		{"netsh", "interface", "ipv4", "set", "subinterface", "23", "mtu=1400", "store=active"},
		{"netsh", "interface", "ipv4", "add", "route", "prefix=10.78.0.0/16", "interface=23", "nexthop=0.0.0.0", "store=active"},
	}
	want6 := [][]string{
		{"netsh", "interface", "ipv6", "add", "address", "interface=23", "address=fd12:3456:789a::5/64", "store=active"},
		{"netsh", "interface", "ipv6", "set", "subinterface", "23", "mtu=1400", "store=active"},
		{"netsh", "interface", "ipv6", "add", "route", "prefix=fd12:3456:789a::/64", "interface=23", "store=active"},
	}
	if !reflect.DeepEqual(v4, want4) {
		t.Errorf("v4 = %q\nwant %q", v4, want4)
	}
	if !reflect.DeepEqual(v6, want6) {
		t.Errorf("v6 = %q\nwant %q", v6, want6)
	}
	// Nothing is stored beyond this boot.
	for _, c := range append(v4, v6...) {
		if c[len(c)-1] != "store=active" {
			t.Errorf("%q is not limited to this boot", c)
		}
	}
	if got := windowsRouteCmd("23", netip.MustParsePrefix("192.168.7.0/24")); !reflect.DeepEqual(got,
		[]string{"netsh", "interface", "ipv4", "add", "route", "prefix=192.168.7.0/24", "interface=23", "nexthop=0.0.0.0", "store=active"}) {
		t.Errorf("route = %q", got)
	}
}

func TestDarwinResolver(t *testing.T) {
	if darwinResolverFile != "/etc/resolver/remohop" {
		t.Errorf("resolver file %s", darwinResolverFile)
	}
	c := darwinResolverContent(netip.MustParseAddr("10.78.3.4"))
	if !strings.Contains(c, "\nnameserver 10.78.3.4\n") || !strings.HasPrefix(c, "#") {
		t.Errorf("resolver content %q", c)
	}
}

func TestNRPTCmds(t *testing.T) {
	add := nrptAddCmd(netip.MustParseAddr("10.78.3.4"))
	rm := nrptRemoveCmd()
	for _, c := range [][]string{add, rm} {
		if c[0] != "powershell.exe" || c[len(c)-2] != "-Command" {
			t.Fatalf("not a PowerShell command: %q", c)
		}
	}
	script := add[len(add)-1]
	for _, want := range []string{"-Namespace '.remohop'", "-NameServers '10.78.3.4'", "-Comment 'remohop-net'"} {
		if !strings.Contains(script, want) {
			t.Errorf("add script %q lacks %q", script, want)
		}
	}
	// Removal touches only rules carrying our comment.
	if s := rm[len(rm)-1]; !strings.Contains(s, "$_.Comment -eq 'remohop-net'") || !strings.Contains(s, "Remove-DnsClientNrptRule -Force") {
		t.Errorf("remove script %q", s)
	}
}

func TestOverlapping(t *testing.T) {
	mesh := netip.MustParsePrefix("10.78.0.0/16")
	used := []netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/0"),
		netip.MustParsePrefix("192.168.1.0/24"),
		netip.MustParsePrefix("fd00::/8"),
	}
	if r, ok := overlapping(mesh, used); ok {
		t.Errorf("overlap with %s reported", r)
	}
	used = append(used, netip.MustParsePrefix("10.0.0.0/8"))
	r, ok := overlapping(mesh, used)
	if !ok || r != netip.MustParsePrefix("10.0.0.0/8") {
		t.Errorf("overlap = %s, %v", r, ok)
	}
	if err := errOverlap(mesh, r); !strings.Contains(err.Error(), "prefix4") {
		t.Errorf("error does not say how to move the mesh: %v", err)
	}
}

func TestAlreadyExists(t *testing.T) {
	for _, s := range []string{
		"route -n add -net 10.1.0.0/16 -interface utun3: exit status 64: add net 10.1.0.0: gateway utun3: File exists",
		"netsh interface ipv4 add route ...: exit status 1: The object already exists.",
	} {
		if !alreadyExists(errors.New(s)) {
			t.Errorf("%q not taken as already there", s)
		}
	}
	if alreadyExists(nil) || alreadyExists(errors.New("ifconfig: ioctl (SIOCAIFADDR): Operation not permitted")) {
		t.Error("a real failure taken as already there")
	}
}
