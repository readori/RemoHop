//go:build windows

package vpn

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// The tests below create real Wintun adapters and change the routing
// table, so they run only when REMOHOP_VPN_E2E=1, as Administrator, with
// wintun.dll placed by the caller (REMOHOP_WINTUN_DIR or next to the test
// binary). They download nothing.
func e2e(t *testing.T) {
	t.Helper()
	if os.Getenv("REMOHOP_VPN_E2E") != "1" {
		t.Skip("set REMOHOP_VPN_E2E=1 (as Administrator, with wintun.dll available) to run")
	}
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Fatal("REMOHOP_VPN_E2E=1 needs an elevated (Administrator) test process")
	}
}

func adapterIndex(t *testing.T, name string) (uint32, bool) {
	t.Helper()
	ifc, err := net.InterfaceByName(name)
	if err != nil {
		return 0, false
	}
	return uint32(ifc.Index), true
}

func hasRoute(t *testing.T, dst string, ifIndex uint32) bool {
	t.Helper()
	routes, err := routeTable()
	if err != nil {
		t.Fatal(err)
	}
	want := netip.MustParsePrefix(dst)
	for _, r := range routes {
		if r.dst == want && (ifIndex == 0 || r.ifIndex == ifIndex) {
			return true
		}
	}
	return false
}

func udpPacket(src, dst netip.AddrPort, payload []byte) []byte {
	p := make([]byte, 28+len(payload))
	p[0] = 0x45
	binary.BigEndian.PutUint16(p[2:], uint16(len(p)))
	p[8], p[9] = 64, 17
	s, d := src.Addr().As4(), dst.Addr().As4()
	copy(p[12:], s[:])
	copy(p[16:], d[:])
	var sum uint32
	for i := 0; i < 20; i += 2 {
		sum += uint32(binary.BigEndian.Uint16(p[i:]))
	}
	for sum>>16 != 0 {
		sum = sum&0xffff + sum>>16
	}
	binary.BigEndian.PutUint16(p[10:], ^uint16(sum))
	binary.BigEndian.PutUint16(p[20:], src.Port())
	binary.BigEndian.PutUint16(p[22:], dst.Port())
	binary.BigEndian.PutUint16(p[24:], uint16(8+len(payload))) // checksum 0: none (allowed for IPv4)
	copy(p[28:], payload)
	return p
}

// An adapter is created, configured, carries packets both ways, and
// leaves nothing behind.
func TestWintunE2E(t *testing.T) {
	e2e(t)
	// Not t.TempDir: remohop copies wintun.dll into the config dir and keeps it
	// loaded for the life of the process, so Windows refuses to delete it.
	cfg, err := os.MkdirTemp("", "remohop-wintun-e2e-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(cfg) }) // best effort
	t.Setenv("REMOHOP_CONFIG_DIR", cfg)
	local, peerIP := netip.MustParseAddr("10.77.250.2"), netip.MustParseAddr("10.77.250.1")
	dev, err := sys.open("e2e:roundtrip")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	name := dev.Name()
	st, err := newState("client", name)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range dev.(interface{ undoSteps() []undoStep }).undoSteps() {
		st.record(step)
	}
	idx, ok := adapterIndex(t, name)
	if !ok {
		t.Fatalf("adapter %s not visible as an interface", name)
	}
	if err := sys.configure(name, local, peerIP, tunnelMTU); err != nil {
		t.Fatalf("configure: %v", err)
	}
	if _, err := sys.clientRoutes(st, name, clientRouting{routes: []netip.Prefix{netip.MustParsePrefix("198.51.100.0/24")}}); err != nil {
		t.Fatalf("routes: %v", err)
	}
	// The connected /30 appears once Windows has finished with the new
	// address (it may hold it as tentative briefly).
	for _, dst := range []string{"198.51.100.0/24", "10.77.250.0/30"} {
		deadline := time.Now().Add(15 * time.Second)
		for !hasRoute(t, dst, idx) {
			if time.Now().After(deadline) {
				routes, _ := routeTable()
				var on []string
				for _, r := range routes {
					if r.ifIndex == idx {
						on = append(on, r.dst.String()+" via "+r.nextHop.String())
					}
				}
				t.Fatalf("route %s not in the table (GetIpForwardTable2); routes on interface %d: %v", dst, idx, on)
			}
			time.Sleep(250 * time.Millisecond)
		}
	}
	// The IPv4 interface MTU (what netsh sets); net.Interface reports the
	// adapter's own maximum (65535 for Wintun) instead.
	row := windows.MibIpInterfaceRow{Family: windows.AF_INET, InterfaceIndex: idx}
	if err := windows.GetIpInterfaceEntry(&row); err != nil {
		t.Errorf("GetIpInterfaceEntry: %v", err)
	} else if row.NlMtu != tunnelMTU {
		t.Errorf("IPv4 MTU %d, want %d", row.NlMtu, tunnelMTU)
	}
	// Windows may hold a new address as tentative briefly.
	time.Sleep(2 * time.Second)

	// OS → tunnel: a datagram to the routed network comes out of Read.
	got := make(chan []byte, 8)
	go func() {
		buf := make([]byte, 2048)
		for {
			n, err := dev.Read(buf)
			if err != nil {
				return
			}
			got <- append([]byte(nil), buf[:n]...)
		}
	}()
	c, err := net.Dial("udp4", "198.51.100.7:9")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	deadline := time.After(10 * time.Second)
	for found := false; !found; {
		c.Write([]byte("remohop-e2e"))
		select {
		case p := <-got:
			h, ok := parse(p)
			found = ok && h.dst.String() == "198.51.100.7" && h.src == local && h.proto == 17
		case <-time.After(300 * time.Millisecond):
		case <-deadline:
			t.Fatal("a datagram routed into the adapter never came out of Read")
		}
	}

	// Tunnel → OS: a packet written to the adapter reaches a local socket.
	ln, err := net.ListenUDP("udp4", net.UDPAddrFromAddrPort(netip.AddrPortFrom(local, 0)))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.LocalAddr().(*net.UDPAddr).Port
	rule := "name=RemoHop VPN e2e " + strconv.Itoa(port)
	exec.Command("netsh", "advfirewall", "firewall", "add", "rule", rule, "dir=in", "action=allow", "protocol=UDP", "localport="+strconv.Itoa(port)).Run()
	defer exec.Command("netsh", "advfirewall", "firewall", "delete", "rule", rule).Run()
	pkt := udpPacket(netip.MustParseAddrPort("198.51.100.7:4242"), netip.AddrPortFrom(local, uint16(port)), []byte("from-the-tunnel"))
	recv := make(chan string, 1)
	go func() {
		buf := make([]byte, 100)
		ln.SetReadDeadline(time.Now().Add(10 * time.Second))
		n, _, err := ln.ReadFromUDP(buf)
		if err == nil {
			recv <- string(buf[:n])
		}
		close(recv)
	}()
	delivered := false
	for i := 0; i < 20 && !delivered; i++ {
		if _, err := dev.Write(pkt); err != nil {
			t.Fatal(err)
		}
		select {
		case msg, ok := <-recv:
			if !ok {
				t.Fatal("a packet written to the adapter never reached the local socket (firewall?)")
			}
			if msg != "from-the-tunnel" {
				t.Fatalf("socket got %q", msg)
			}
			delivered = true
		case <-time.After(300 * time.Millisecond):
		}
	}
	if !delivered {
		t.Fatal("a packet written to the adapter never reached the local socket (firewall?)")
	}

	// Down: the adapter and its routes are gone, nothing recorded is left.
	dev.Close()
	st.undo()
	time.Sleep(time.Second)
	if _, ok := adapterIndex(t, name); ok {
		t.Fatal("adapter still present after close")
	}
	if hasRoute(t, "198.51.100.0/24", 0) {
		t.Fatal("route still present after close")
	}
}

// A process killed while its tunnel is up leaves at most what it
// recorded, and the next start removes that.
func TestWintunE2ECrash(t *testing.T) {
	if os.Getenv("REMOHOP_VPN_E2E_CHILD") == "1" {
		crashChild()
		return
	}
	e2e(t)
	dir := t.TempDir()
	t.Setenv("REMOHOP_CONFIG_DIR", dir)
	cmd := exec.Command(os.Args[0], "-test.run=^TestWintunE2ECrash$", "-test.v")
	cmd.Env = append(os.Environ(), "REMOHOP_VPN_E2E_CHILD=1", "REMOHOP_CONFIG_DIR="+dir)
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var name string
	sc := bufio.NewScanner(out)
	for sc.Scan() {
		if line := sc.Text(); strings.HasPrefix(line, "READY ") {
			name = strings.TrimPrefix(line, "READY ")
			break
		} else if strings.HasPrefix(line, "FAIL ") {
			t.Fatal(line)
		}
	}
	if name == "" {
		cmd.Process.Kill()
		t.Fatal("the child never got its tunnel up")
	}
	if !hasRoute(t, "192.0.2.77/32", 0) {
		t.Fatal("the child's exception route is not in the table")
	}
	cmd.Process.Kill() // TerminateProcess: no clean-up runs in the child
	cmd.Wait()
	time.Sleep(time.Second)
	if !hasRoute(t, "192.0.2.77/32", 0) {
		t.Log("(the exception route vanished with the process)")
	}
	cleaned, stale := Recover()
	if cleaned != 1 || stale != 0 {
		t.Fatalf("Recover: cleaned %d, stale %d", cleaned, stale)
	}
	time.Sleep(time.Second)
	if hasRoute(t, "192.0.2.77/32", 0) {
		t.Fatal("recovery left the killed process's route")
	}
	if _, ok := adapterIndex(t, name); ok {
		t.Fatalf("adapter %s still present after recovery", name)
	}
}

// crashChild brings a tunnel up the way `vpn up --exit` does for its
// exception routes (without taking over the default route, which would
// cut the CI machine off), then waits to be killed.
func crashChild() {
	fail := func(err error) { fmt.Println("FAIL", err); os.Exit(1) }
	dev, err := sys.open("e2e:crash")
	if err != nil {
		fail(err)
	}
	st, err := newState("client", dev.Name())
	if err != nil {
		fail(err)
	}
	for _, step := range dev.(interface{ undoSteps() []undoStep }).undoSteps() {
		st.record(step)
	}
	if err := sys.configure(dev.Name(), netip.MustParseAddr("10.77.251.2"), netip.MustParseAddr("10.77.251.1"), tunnelMTU); err != nil {
		fail(err)
	}
	r, err := lookupRoute(netip.MustParseAddr("192.0.2.77"), tunnelIndexes())
	if err != nil {
		fail(err)
	}
	k := netip.MustParsePrefix("192.0.2.77/32")
	via := strconv.Itoa(int(r.ifIndex))
	st.record(undoStep{Cmd: delRoute(k, via, r.nextHop)})
	if err := run(addRoute(k, via, r.nextHop)...); err != nil {
		fail(err)
	}
	fmt.Println("READY", dev.Name())
	time.Sleep(time.Hour)
}
