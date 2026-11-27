//go:build windows

package mesh

import (
	"net"
	"net/netip"
	"os"
	"strings"
	"testing"

	"golang.org/x/sys/windows"

	"github.com/readori/RemoHop/internal/svc/vpn"
)

// TestSysWindowsE2E sets the mesh's adapter, a route and split DNS up on
// the real system and checks that undoing them leaves nothing behind. It
// creates a Wintun adapter and an NRPT rule, so it runs only with
// REMOHOP_NET_E2E=1, as Administrator, with wintun.dll placed by the
// caller (REMOHOP_WINTUN_DIR or next to the test binary), from an elevated
// PowerShell:
//
//	$env:REMOHOP_NET_E2E=1; go test ./internal/mesh -run SysWindowsE2E -v
func TestSysWindowsE2E(t *testing.T) {
	if os.Getenv("REMOHOP_NET_E2E") != "1" {
		t.Skip("set REMOHOP_NET_E2E=1 (as Administrator, with wintun.dll available) to run")
	}
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Fatal("REMOHOP_NET_E2E=1 needs an elevated (Administrator) test process")
	}
	t.Setenv("REMOHOP_CONFIG_DIR", t.TempDir())
	if n := nrptRules(t); n > 0 {
		t.Skipf("%d NRPT rule(s) of a mesh exist already (a mesh runs here?); not touching them", n)
	}
	a4 := netip.MustParsePrefix("10.78.250.1/16")
	a6 := netip.MustParsePrefix("fd52:6d68:6f70::1/64")
	routed := netip.MustParsePrefix("192.0.2.0/24") // TEST-NET-1
	if err := checkPrefixFree(a4.Masked()); err != nil {
		t.Skip(err)
	}
	dev, err := vpn.Open("mesh-e2e")
	if err != nil {
		t.Fatal(err)
	}
	name := dev.Name()
	closed := false
	defer func() {
		if !closed {
			dev.Close()
		}
	}()
	changes, err := vpn.NewChanges("mesh-e2e", dev)
	if err != nil {
		t.Fatal(err)
	}
	v6, notes, err := sysConfigure(name, a4, a6, tunnelMTU)
	if err != nil {
		changes.Undo()
		t.Fatal(err)
	}
	t.Logf("%s: v6=%v notes=%q", name, v6, notes)
	if !ifaceHas(t, name, a4.Addr()) {
		t.Errorf("%s does not have %s", name, a4.Addr())
	}
	if v6 && !ifaceHas(t, name, a6.Addr()) {
		t.Errorf("%s does not have %s", name, a6.Addr())
	}
	idx, _ := vpn.IfIndex(name)
	if !routeOn(t, "10.78.0.0/16", idx) {
		t.Errorf("no on-link route for the mesh network on %s", name)
	}
	if err := sysRoute(name, routed); err != nil {
		t.Error(err)
	}
	if err := sysRoute(name, routed); err != nil { // a second link offering it again
		t.Errorf("adding the same route again: %v", err)
	}
	if !routeOn(t, routed.String(), idx) {
		t.Errorf("no route for %s on %s", routed, name)
	}
	note, ok := sysSplitDNS(changes, name, a4.Addr())
	if !ok {
		t.Errorf("split DNS not set: %s", note)
	}
	if n := nrptRules(t); n != 1 {
		t.Errorf("%d NRPT rules with our comment, want 1", n)
	}

	changes.Undo()
	dev.Close()
	closed = true
	if n := nrptRules(t); n != 0 {
		t.Errorf("%d NRPT rules left after the undo", n)
	}
	if _, err := net.InterfaceByName(name); err == nil {
		t.Errorf("%s is still there after closing it", name)
	}
}

func ifaceHas(t *testing.T, name string, a netip.Addr) bool {
	t.Helper()
	ifc, err := net.InterfaceByName(name)
	if err != nil {
		t.Fatal(err)
	}
	addrs, _ := ifc.Addrs()
	for _, x := range addrs {
		if ipn, ok := x.(*net.IPNet); ok {
			if got, ok := netip.AddrFromSlice(ipn.IP); ok && got.Unmap() == a {
				return true
			}
		}
	}
	return false
}

func routeOn(t *testing.T, prefix, idx string) bool {
	t.Helper()
	out, err := vpn.CommandOutput(powershell("(Get-NetRoute -DestinationPrefix '" + prefix + "' -InterfaceIndex " + idx +
		" -ErrorAction SilentlyContinue | Measure-Object).Count")...)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(out) != "0"
}

func nrptRules(t *testing.T) int {
	t.Helper()
	out, err := vpn.CommandOutput(powershell("(Get-DnsClientNrptRule | Where-Object { $_.Comment -eq '" + nrptComment + "' } | Measure-Object).Count")...)
	if err != nil {
		t.Fatal(err)
	}
	switch strings.TrimSpace(out) {
	case "0":
		return 0
	case "1":
		return 1
	}
	return 2
}
