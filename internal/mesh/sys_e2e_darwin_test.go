//go:build darwin

package mesh

import (
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/svc/vpn"
)

// TestSysDarwinE2E sets the mesh's interface, a route and split DNS up on
// the real system and checks that undoing them leaves nothing behind. It
// changes the routing table and /etc/resolver, so it runs only with
// REMOHOP_NET_E2E=1, as root:
//
//	sudo REMOHOP_NET_E2E=1 go test ./internal/mesh -run SysDarwinE2E -v
func TestSysDarwinE2E(t *testing.T) {
	if os.Getenv("REMOHOP_NET_E2E") != "1" {
		t.Skip("set REMOHOP_NET_E2E=1 (as root) to run")
	}
	if os.Geteuid() != 0 {
		t.Fatal("REMOHOP_NET_E2E=1 needs root (sudo)")
	}
	t.Setenv("REMOHOP_CONFIG_DIR", t.TempDir())
	if _, err := os.Stat(darwinResolverFile); err == nil {
		t.Skipf("%s exists already (a mesh runs here?); not touching it", darwinResolverFile)
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
	if got := routeIface(t, "10.78.9.9"); got != name {
		t.Errorf("the mesh network goes to %q, not %s", got, name)
	}
	if err := sysRoute(name, routed); err != nil {
		t.Error(err)
	}
	if err := sysRoute(name, routed); err != nil { // a second link offering it again
		t.Errorf("adding the same route again: %v", err)
	}
	if got := routeIface(t, "192.0.2.10"); got != name {
		t.Errorf("192.0.2.0/24 goes to %q, not %s", got, name)
	}
	note, ok := sysSplitDNS(changes, name, a4.Addr())
	if !ok {
		t.Errorf("split DNS not set: %s", note)
	}
	if b, err := os.ReadFile(darwinResolverFile); err != nil || !strings.Contains(string(b), "nameserver "+a4.Addr().String()) {
		t.Errorf("%s: %q, %v", darwinResolverFile, b, err)
	}
	if out, _ := exec.Command("scutil", "--dns").Output(); !strings.Contains(string(out), "domain   : "+Domain) {
		t.Logf("scutil --dns does not list the %s resolver yet (it may take a moment):\n%s", Domain, out)
	}

	changes.Undo()
	dev.Close()
	closed = true
	if _, err := os.Stat(darwinResolverFile); !os.IsNotExist(err) {
		t.Errorf("%s is still there after the undo", darwinResolverFile)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := net.InterfaceByName(name); err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Errorf("%s is still there after closing it", name)
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if got := routeIface(t, "192.0.2.10"); got == name {
		t.Errorf("192.0.2.0/24 still goes to %s", name)
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

// routeIface is the interface `route -n get` names for addr ("" if none).
func routeIface(t *testing.T, addr string) string {
	t.Helper()
	out, _ := exec.Command("route", "-n", "get", addr).CombinedOutput()
	for _, line := range strings.Split(string(out), "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[0] == "interface:" {
			return f[1]
		}
	}
	return ""
}
