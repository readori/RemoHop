//go:build windows

package vpn

import (
	"net/netip"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"

	"golang.org/x/sys/windows"
)

// fakeWindows replaces netsh/pnputil, the route table and the adapter
// index with recorders, so the Windows steps can be checked without the
// driver or Administrator rights.
type fakeWindows struct {
	mu   sync.Mutex
	cmds []string
}

func (f *fakeWindows) install(t *testing.T) {
	t.Helper()
	oldRun, oldTable, oldIndex := runPrivileged, routeTable, interfaceIndex
	t.Cleanup(func() { runPrivileged, routeTable, interfaceIndex = oldRun, oldTable, oldIndex })
	runPrivileged = func(cmd *exec.Cmd) ([]byte, error) {
		f.mu.Lock()
		f.cmds = append(f.cmds, strings.Join(cmd.Args, " "))
		f.mu.Unlock()
		return nil, nil
	}
	routeTable = func() ([]route, error) {
		return []route{
			{dst: netip.MustParsePrefix("0.0.0.0/0"), ifIndex: 7, nextHop: netip.MustParseAddr("192.168.2.1"), metric: 25},
			{dst: netip.MustParsePrefix("0.0.0.0/0"), ifIndex: 9, nextHop: netip.MustParseAddr("10.9.9.1"), metric: 50},
			{dst: netip.MustParsePrefix("10.0.0.0/8"), ifIndex: 9, nextHop: netip.MustParseAddr("10.9.9.1"), metric: 5},
			{dst: netip.MustParsePrefix("192.168.2.0/24"), ifIndex: 7, metric: 1},
		}, nil
	}
	interfaceIndex = func(string) (string, error) { return "42", nil }
}

func (f *fakeWindows) take() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.cmds
	f.cmds = nil
	return out
}

func TestWindowsConfigureAndRoutes(t *testing.T) {
	t.Setenv("REMOHOP_CONFIG_DIR", t.TempDir())
	f := &fakeWindows{}
	f.install(t)

	if err := windowsConfigure("RemoHop-1a2b", netip.MustParseAddr("10.77.0.2"), netip.MustParseAddr("10.77.0.1"), tunnelMTU); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"netsh interface ipv4 add address name=42 address=10.77.0.2 mask=255.255.255.252 store=active",
		"netsh interface ipv4 set subinterface 42 mtu=1400 store=active",
	}
	if got := f.take(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("configure ran\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	st, err := newState("client", "RemoHop-1a2b")
	if err != nil {
		t.Fatal(err)
	}
	notes, err := windowsClientRoutes(st, "RemoHop-1a2b", clientRouting{
		routes: []netip.Prefix{netip.MustParsePrefix("192.168.1.0/24")},
		exit:   true,
		keep:   []netip.Addr{netip.MustParseAddr("203.0.113.20")},
		bypass: []netip.Prefix{netip.MustParsePrefix("10.20.0.0/16")},
		dns:    []netip.Addr{netip.MustParseAddr("9.9.9.9"), netip.MustParseAddr("1.1.1.1")},
	})
	if err != nil {
		t.Fatal(err)
	}
	want = []string{
		"netsh interface ipv4 add route prefix=192.168.1.0/24 interface=42 nexthop=0.0.0.0 store=active",
		// bypass: the most specific route (10/8 via interface 9)
		"netsh interface ipv4 add route prefix=10.20.0.0/16 interface=9 nexthop=10.9.9.1 store=active",
		// the session's peer: the default route with the lower metric
		"netsh interface ipv4 add route prefix=203.0.113.20/32 interface=7 nexthop=192.168.2.1 store=active",
		"netsh interface ipv4 add route prefix=0.0.0.0/1 interface=42 nexthop=0.0.0.0 store=active",
		"netsh interface ipv4 add route prefix=128.0.0.0/1 interface=42 nexthop=0.0.0.0 store=active",
		"netsh interface ipv4 set dnsservers name=42 source=static address=9.9.9.9 register=none validate=no",
		"netsh interface ipv4 add dnsservers name=42 address=1.1.1.1 index=2 validate=no",
		"netsh interface ipv4 set interface 42 metric=1 store=active",
	}
	if got := f.take(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("routes ran\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "DNS") {
		t.Fatalf("notes %v", notes)
	}

	// Only what outlives the adapter was recorded, and undo removes it,
	// newest first.
	st.undo()
	want = []string{
		"netsh interface ipv4 delete route prefix=203.0.113.20/32 interface=7 nexthop=192.168.2.1 store=active",
		"netsh interface ipv4 delete route prefix=10.20.0.0/16 interface=9 nexthop=10.9.9.1 store=active",
	}
	if got := f.take(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("undo ran\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if _, err := os.Stat(st.path); !os.IsNotExist(err) {
		t.Fatal("state file left after undo")
	}
}

// A killed process's adapter-removal and route steps are run by the next
// remohop that starts.
func TestWindowsRecover(t *testing.T) {
	t.Setenv("REMOHOP_CONFIG_DIR", t.TempDir())
	f := &fakeWindows{}
	f.install(t)
	st, err := newState("client", "RemoHop-dead")
	if err != nil {
		t.Fatal(err)
	}
	st.record(undoStep{Cmd: []string{"pnputil", "/remove-device", `SWD\Wintun\{00000000-0000-5000-8000-000000000000}`}})
	st.record(undoStep{Cmd: delRoute(netip.MustParsePrefix("203.0.113.20/32"), "7", netip.MustParseAddr("192.168.2.1"))})
	st.record(undoStep{Cmd: []string{"cmd", "/c", "del", "C:\\important"}}) // never run
	// Pretend the owner died.
	raw, _ := os.ReadFile(st.path)
	os.WriteFile(st.path, []byte(strings.Replace(string(raw), `"pid": `+strconv.Itoa(os.Getpid()), `"pid": 1073741824`, 1)), 0o600)
	if cleaned, _ := Recover(); cleaned != 1 {
		t.Fatalf("cleaned %d", cleaned)
	}
	got := strings.Join(f.take(), "\n")
	if !strings.Contains(got, "pnputil /remove-device") || !strings.Contains(got, "delete route prefix=203.0.113.20/32") || strings.Contains(got, "important") {
		t.Fatalf("recovery ran:\n%s", got)
	}
}

func TestWindowsAdapterIdentity(t *testing.T) {
	t.Setenv("REMOHOP_CONFIG_DIR", t.TempDir())
	a, b := adapterGUID("client:123456789 10.77.0.0/30"), adapterGUID("client:123456789 10.77.0.0/30")
	if a != b {
		t.Fatal("the GUID of a tunnel is not stable")
	}
	c := adapterGUID("client:987654321 10.77.0.0/30")
	if a == c || adapterName(a) == adapterName(c) && a.Data1 == c.Data1 {
		t.Fatal("two tunnels share an adapter identity")
	}
	if !strings.HasPrefix(adapterName(a), "RemoHop-") || !isTunnelName(adapterName(a)) {
		t.Fatalf("adapter name %q", adapterName(a))
	}
	if id := adapterInstanceID(a); !strings.HasPrefix(id, `SWD\Wintun\{`) {
		t.Fatalf("instance id %q", id)
	}
	if a.Data3>>12 != 5 || a.Data4[0]>>6 != 2 {
		t.Fatalf("GUID %s is not a well-formed name-based GUID", a)
	}
}

// Without Administrator rights the refusal says exactly what to do, and
// no adapter is attempted. (Skipped on an elevated runner.)
func TestWindowsPrivilegeMessage(t *testing.T) {
	if windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("running elevated")
	}
	err := sys.privileged()
	if err == nil || !strings.Contains(err.Error(), "Administrator") || !strings.Contains(err.Error(), "Run as administrator") {
		t.Fatalf("privilege error: %v", err)
	}
}

func TestWindowsHostRefusesForwarding(t *testing.T) {
	err := sys.hostSupported()
	if err == nil || !strings.Contains(err.Error(), "cannot be a VPN host") {
		t.Fatalf("hostSupported: %v", err)
	}
}

func TestWindowsMissingDriver(t *testing.T) {
	t.Setenv("REMOHOP_CONFIG_DIR", t.TempDir())
	t.Setenv("REMOHOP_WINTUN_DIR", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	if exe, err := os.Executable(); err == nil {
		if _, err := os.Stat(exe[:strings.LastIndexAny(exe, `\/`)+1] + "wintun.dll"); err == nil {
			t.Skip("a wintun.dll sits next to the test binary")
		}
	}
	_, err := locateWintun(wintunSearchDirs(), t.TempDir(), "amd64")
	if err == nil || !strings.Contains(err.Error(), "never downloads") {
		t.Fatalf("missing driver: %v", err)
	}
}
