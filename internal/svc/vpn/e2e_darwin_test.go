//go:build darwin

package vpn

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/peer/peertest"
)

// The tests below create real utun interfaces and change the routing
// table, so they run only when REMOHOP_VPN_E2E=1 and as root. Build the test
// binary as yourself and run that with sudo, so root never writes to the Go
// caches:
//
//	go test -c -o /tmp/vpn.test ./internal/svc/vpn
//	sudo REMOHOP_VPN_E2E=1 /tmp/vpn.test -test.v -test.run 'TestUtun'
//
// They use documentation networks (198.51.100.0/24, 192.0.2.0/24) and never
// touch the default route.
func e2e(t *testing.T) {
	t.Helper()
	if os.Getenv("REMOHOP_VPN_E2E") != "1" {
		t.Skip("set REMOHOP_VPN_E2E=1 (as root) to run")
	}
	if os.Geteuid() != 0 {
		t.Fatal("REMOHOP_VPN_E2E=1 needs root (sudo)")
	}
}

// routeGet asks the kernel how addr is routed: the keys of `route -n get`
// (destination, mask, gateway, interface, flags).
func routeGet(t *testing.T, addr string) map[string]string {
	t.Helper()
	out, err := exec.Command("route", "-n", "get", addr).CombinedOutput()
	m := map[string]string{}
	if err != nil {
		return m // "not in table"
	}
	for _, line := range strings.Split(string(out), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), ": "); ok {
			m[k] = strings.TrimSpace(v)
		}
	}
	return m
}

// waitRoute polls until addr is (or is no longer) routed through dev.
func waitRoute(t *testing.T, addr, dev string, want bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		r := routeGet(t, addr)
		if (r["interface"] == dev) == want {
			return
		}
		if time.Now().After(deadline) {
			table, _ := exec.Command("netstat", "-rn", "-f", "inet").CombinedOutput()
			t.Fatalf("route to %s: through %s = %v, want %v (route get: %v)\n%s", addr, dev, !want, want, r, table)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func interfaceExists(name string) bool {
	_, err := net.InterfaceByName(name)
	return err == nil
}

func waitInterfaceGone(t *testing.T, name string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for interfaceExists(name) {
		if time.Now().After(deadline) {
			out, _ := exec.Command("ifconfig", name).CombinedOutput()
			t.Fatalf("interface %s still present:\n%s", name, out)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func udpPacket(src, dst netip.AddrPort, payload []byte) []byte {
	p := make([]byte, 28+len(payload))
	p[0] = 0x45
	binary.BigEndian.PutUint16(p[2:], uint16(len(p)))
	p[8], p[9] = 64, 17
	s, d := src.Addr().As4(), dst.Addr().As4()
	copy(p[12:], s[:])
	copy(p[16:], d[:])
	binary.BigEndian.PutUint16(p[10:], ipChecksum(p[:20]))
	binary.BigEndian.PutUint16(p[20:], src.Port())
	binary.BigEndian.PutUint16(p[22:], dst.Port())
	binary.BigEndian.PutUint16(p[24:], uint16(8+len(payload))) // checksum 0: none (allowed for IPv4)
	copy(p[28:], payload)
	return p
}

// readPackets pumps what the kernel routes into dev to a channel until the
// device is closed; done is closed when Read has returned for good.
func readPackets(dev Device) (pkts chan []byte, done chan struct{}) {
	pkts, done = make(chan []byte, 64), make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 2048)
		for {
			n, err := dev.Read(buf)
			if err != nil {
				return
			}
			select {
			case pkts <- append([]byte(nil), buf[:n]...):
			default:
			}
		}
	}()
	return pkts, done
}

// A utun interface is created, configured, carries packets both ways, and
// leaves nothing behind.
func TestUtunE2E(t *testing.T) {
	e2e(t)
	t.Setenv("REMOHOP_CONFIG_DIR", t.TempDir())
	local, peerIP := netip.MustParseAddr("10.77.250.2"), netip.MustParseAddr("10.77.250.1")
	if err := sys.privileged(); err != nil {
		t.Fatalf("privileged as root: %v", err)
	}
	dev, err := sys.open("e2e:roundtrip")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer dev.Close()
	name := dev.Name()
	t.Logf("interface %s", name)
	if !isTunnelName(name) || !interfaceExists(name) {
		t.Fatalf("interface %q is not a visible utun interface", name)
	}
	st, err := newState("client", name)
	if err != nil {
		t.Fatal(err)
	}
	defer st.undo()
	if err := sys.configure(name, local, peerIP, tunnelMTU); err != nil {
		t.Fatalf("configure: %v", err)
	}
	ifc, err := net.InterfaceByName(name)
	if err != nil {
		t.Fatal(err)
	}
	if ifc.MTU != tunnelMTU || ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagPointToPoint == 0 {
		t.Errorf("interface %s: mtu %d flags %v, want mtu %d, up, pointtopoint", name, ifc.MTU, ifc.Flags, tunnelMTU)
	}
	// The address is there, and what this tunnel takes from the pool is
	// its own /30 and nothing more: a second tunnel must still find room.
	var mine []netip.Prefix
	for _, n := range sys.usedNets() {
		if n.Contains(local) {
			mine = append(mine, n)
		}
	}
	if len(mine) == 0 {
		out, _ := exec.Command("ifconfig", name).CombinedOutput()
		t.Fatalf("usedNets does not list the tunnel address:\n%s", out)
	}
	for _, n := range mine {
		if n.Bits() < 30 {
			t.Errorf("the tunnel address counts as the network %s; other tunnels would find the pool taken", n)
		}
	}
	if free := freeNets(sys.usedNets(), 2); len(free) == 0 {
		t.Error("no free tunnel network left with one tunnel up")
	} else if free[0].Contains(local) {
		t.Errorf("freeNets offers %s, which this tunnel uses", free[0])
	}

	lan := netip.MustParsePrefix("198.51.100.0/24")
	if _, err := sys.clientRoutes(st, name, clientRouting{routes: []netip.Prefix{lan}}); err != nil {
		t.Fatalf("routes: %v", err)
	}
	waitRoute(t, "198.51.100.7", name, true)
	waitRoute(t, peerIP.String(), name, true)
	// An exception route the way `vpn up --exit --bypass` makes them (the
	// halves of the default route are not installed: they would cut this
	// machine off), and its recorded undo.
	bypass := netip.MustParsePrefix("203.0.113.0/24")
	before := routeGet(t, "203.0.113.9")
	if err := darwinExcept(st, bypass); err != nil {
		t.Fatalf("exception route: %v", err)
	}
	after := routeGet(t, "203.0.113.9")
	t.Logf("203.0.113.9 before: %v; pinned: %v", before, after)
	if after["destination"] != "203.0.113.0" || after["interface"] != before["interface"] || after["interface"] == name {
		t.Errorf("exception route for %s: %v (before: %v)", bypass, after, before)
	}

	// OS → tunnel: a datagram to the routed network comes out of Read.
	got, readDone := readPackets(dev)
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
			found = ok && h.dst.String() == "198.51.100.7" && h.src == local && h.proto == 17 &&
				strings.HasSuffix(string(p), "remohop-e2e")
			if !found {
				t.Logf("other packet from the interface: %d bytes, parsed %v %+v", len(p), ok, h)
			}
		case <-time.After(300 * time.Millisecond):
		case <-deadline:
			t.Fatal("a datagram routed into the interface never came out of Read")
		}
	}

	// Tunnel → OS: a packet written to the interface reaches a local socket.
	ln, err := net.ListenUDP("udp4", net.UDPAddrFromAddrPort(netip.AddrPortFrom(local, 0)))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.LocalAddr().(*net.UDPAddr).Port
	pkt := udpPacket(netip.MustParseAddrPort("198.51.100.7:4242"), netip.AddrPortFrom(local, uint16(port)), []byte("from-the-tunnel"))
	recv := make(chan string, 1)
	go func() {
		buf := make([]byte, 100)
		ln.SetReadDeadline(time.Now().Add(10 * time.Second))
		n, from, err := ln.ReadFromUDP(buf)
		if err == nil {
			recv <- from.String() + " " + string(buf[:n])
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
				t.Fatal("a packet written to the interface never reached the local socket")
			}
			if msg != "198.51.100.7:4242 from-the-tunnel" {
				t.Fatalf("socket got %q", msg)
			}
			delivered = true
		case <-time.After(300 * time.Millisecond):
		}
	}
	if !delivered {
		t.Fatal("a packet written to the interface never reached the local socket")
	}

	// Down: Close ends a blocked Read (the pump relies on it), the
	// interface and its routes are gone, the exception is undone.
	dev.Close()
	select {
	case <-readDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Read still blocked 5 s after Close")
	}
	st.undo()
	waitInterfaceGone(t, name)
	waitRoute(t, "198.51.100.7", name, false)
	if r := routeGet(t, "203.0.113.9"); r["destination"] == "203.0.113.0" {
		t.Fatalf("exception route still present after undo: %v", r)
	}
	dir, _ := stateDir()
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Fatalf("%d state files left", len(ents))
	}
}

// The client side of `remohop <ID> vpn up --route` over a real session: Up
// creates and configures the utun interface and its routes, the pump
// carries what the kernel routes into it to the host and back, TCP SYNs are
// clamped, and ending the tunnel removes everything. The host end of the
// session is in this process too; its tunnel device is the test (a macOS
// host cannot forward, and one Mac has one routing table), so what the
// "host" receives is exactly what left this machine through the tunnel.
func TestUtunClientOverSession(t *testing.T) {
	e2e(t)
	t.Setenv("REMOHOP_CONFIG_DIR", t.TempDir())
	lan := netip.MustParsePrefix("198.51.100.0/24")
	real := sys
	hostDev := newFakeDev("e2ehost0")
	sys = real
	sys.hostSupported = func() error { return nil }
	sys.hostForward = func(*state, string, netip.Addr) error { return nil }
	sys.open = func(tunnel string) (Device, error) {
		if strings.HasPrefix(tunnel, "host:") {
			return hostDev, nil
		}
		return real.open(tunnel)
	}
	sys.configure = func(dev string, local, peer netip.Addr, mtu int) error {
		if dev == hostDev.Name() {
			return nil
		}
		return real.configure(dev, local, peer, mtu)
	}
	oldConnected := connected
	connected = func() []netip.Prefix { return []netip.Prefix{lan} }
	t.Cleanup(func() { sys, connected = real, oldConnected })

	client, _ := peertest.Pair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	tun, err := Up(ctx, client, Options{Routes: []netip.Prefix{lan}})
	if err != nil {
		t.Fatalf("Up: %v", err)
	}
	t.Logf("tunnel %s %s -> %s (mtu %d, mss %d) notes %v", tun.Device, tun.Local, tun.Peer, tun.MTU, tun.MSS, tun.Notes)
	done := make(chan error, 1)
	go func() { done <- tun.Run(ctx) }()
	if !isTunnelName(tun.Device) || !interfaceExists(tun.Device) {
		t.Fatalf("device %q is not a utun interface", tun.Device)
	}
	waitRoute(t, "198.51.100.7", tun.Device, true)
	waitRoute(t, tun.Peer.String(), tun.Device, true)

	// This machine → host side: an ordinary socket, the kernel's routing,
	// the utun device, the pump, the session.
	fromClient := func(what string, match func(p []byte, h header) bool, send func()) []byte {
		t.Helper()
		deadline := time.After(15 * time.Second)
		for {
			send()
			select {
			case p := <-hostDev.toOS:
				if h, ok := parse(p); ok && match(p, h) {
					return p
				}
			case <-time.After(300 * time.Millisecond):
			case <-deadline:
				t.Fatalf("%s never arrived at the host end (sent %d, refused %d)", what, tun.Stats.Sent.Load(), tun.Stats.DroppedOut.Load())
			}
		}
	}
	c, err := net.Dial("udp4", "198.51.100.7:9")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	fromClient("a datagram to the routed network", func(p []byte, h header) bool {
		return h.proto == 17 && h.src == tun.Local && h.dst.String() == "198.51.100.7" && strings.HasSuffix(string(p), "over-the-session")
	}, func() { c.Write([]byte("over-the-session")) })
	c2, err := net.Dial("udp4", net.JoinHostPort(tun.Peer.String(), "9"))
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	fromClient("a datagram to the host's tunnel address", func(p []byte, h header) bool {
		return h.proto == 17 && h.src == tun.Local && h.dst == tun.Peer
	}, func() { c2.Write([]byte("to-the-host")) })

	// A TCP connection attempt: the kernel's SYN arrives with its MSS
	// clamped to what fits the tunnel.
	syn := fromClient("a TCP SYN", func(p []byte, h header) bool {
		return h.proto == protoTCP && h.dst.String() == "198.51.100.7" && len(p) >= h.ihl+20 && p[h.ihl+13]&tcpSYN != 0
	}, func() {
		go func() {
			if conn, err := net.DialTimeout("tcp4", "198.51.100.7:80", 250*time.Millisecond); err == nil {
				conn.Close()
			}
		}()
	})
	if mss, ok := synMSS(syn); !ok || int(mss) > tun.MSS {
		t.Errorf("SYN through the tunnel has MSS %d (present %v), tunnel clamps to %d", mss, ok, tun.MSS)
	} else {
		t.Logf("kernel SYN arrived with MSS %d (clamp %d)", mss, tun.MSS)
	}

	// Host side → this machine: session, pump, utun, the kernel, a socket.
	ln, err := net.ListenUDP("udp4", net.UDPAddrFromAddrPort(netip.AddrPortFrom(tun.Local, 0)))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := uint16(ln.LocalAddr().(*net.UDPAddr).Port)
	reply := udpPacket(netip.MustParseAddrPort("198.51.100.7:4242"), netip.AddrPortFrom(tun.Local, port), []byte("from-the-host-lan"))
	recv := make(chan string, 1)
	go func() {
		buf := make([]byte, 100)
		ln.SetReadDeadline(time.Now().Add(15 * time.Second))
		if n, from, err := ln.ReadFromUDP(buf); err == nil {
			recv <- from.String() + " " + string(buf[:n])
		}
		close(recv)
	}()
	for delivered := false; !delivered; {
		hostDev.fromOS <- reply
		select {
		case msg, ok := <-recv:
			if !ok || msg != "198.51.100.7:4242 from-the-host-lan" {
				t.Fatalf("local socket: %q (received %d, refused %d)", msg, tun.Stats.Received.Load(), tun.Stats.DroppedIn.Load())
			}
			delivered = true
		case <-time.After(300 * time.Millisecond):
		}
	}
	t.Logf("stats: sent %d received %d via-stream %d refused out %d in %d", tun.Stats.Sent.Load(), tun.Stats.Received.Load(),
		tun.Stats.ViaStream.Load(), tun.Stats.DroppedOut.Load(), tun.Stats.DroppedIn.Load())

	// Ending the tunnel removes the interface, its routes and the state.
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run after cancel: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after cancel (Close does not unblock Read?)")
	}
	waitInterfaceGone(t, tun.Device)
	waitRoute(t, "198.51.100.7", tun.Device, false)
	deadline := time.Now().Add(5 * time.Second)
	for {
		dir, _ := stateDir()
		ents, _ := os.ReadDir(dir)
		if len(ents) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d state files left", len(ents))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// synMSS reads the MSS option of a TCP SYN.
func synMSS(p []byte) (uint16, bool) {
	h, ok := parse(p)
	if !ok || h.proto != protoTCP || len(p) < h.ihl+20 {
		return 0, false
	}
	tcp := p[h.ihl:]
	off := int(tcp[12]>>4) * 4
	if off < 20 || off > len(tcp) {
		return 0, false
	}
	for opts := tcp[20:off]; len(opts) >= 2; {
		switch {
		case opts[0] == 0:
			return 0, false
		case opts[0] == 1:
			opts = opts[1:]
			continue
		case opts[0] == 2 && opts[1] == 4 && len(opts) >= 4:
			return binary.BigEndian.Uint16(opts[2:4]), true
		}
		if int(opts[1]) < 2 || int(opts[1]) > len(opts) {
			return 0, false
		}
		opts = opts[opts[1]:]
	}
	return 0, false
}

// A process killed while its tunnel is up leaves at most what it
// recorded, and the next start removes that.
func TestUtunE2ECrash(t *testing.T) {
	if os.Getenv("REMOHOP_VPN_E2E_CHILD") == "1" {
		crashChild()
		return
	}
	e2e(t)
	dir := t.TempDir()
	t.Setenv("REMOHOP_CONFIG_DIR", dir)
	if r := routeGet(t, "192.0.2.77"); r["destination"] == "192.0.2.77" {
		t.Fatalf("192.0.2.77 already has a host route before the test: %v", r)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestUtunE2ECrash$", "-test.v")
	cmd.Env = append(os.Environ(), "REMOHOP_VPN_E2E_CHILD=1", "REMOHOP_CONFIG_DIR="+dir)
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
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
		t.Fatal("the child never got its tunnel up")
	}
	if !interfaceExists(name) {
		t.Fatalf("the child's interface %s is not visible", name)
	}
	pinned := routeGet(t, "192.0.2.77")
	if pinned["destination"] != "192.0.2.77" || pinned["interface"] == name {
		t.Fatalf("the child's exception route is not in the table: %v", pinned)
	}
	waitRoute(t, "198.51.100.7", name, true)
	cmd.Process.Kill() // SIGKILL: no clean-up runs in the child
	cmd.Wait()
	// The kernel removes the interface, and the routes through it, with
	// the process that created it.
	waitInterfaceGone(t, name)
	waitRoute(t, "198.51.100.7", name, false)
	if r := routeGet(t, "192.0.2.77"); r["destination"] != "192.0.2.77" {
		t.Log("(the exception route vanished with the process)")
	}
	cleaned, stale := Recover()
	if cleaned != 1 || stale != 0 {
		t.Fatalf("Recover: cleaned %d, stale %d", cleaned, stale)
	}
	if r := routeGet(t, "192.0.2.77"); r["destination"] == "192.0.2.77" {
		t.Fatalf("recovery left the killed process's route: %v", r)
	}
	sd, _ := stateDir()
	if ents, _ := os.ReadDir(sd); len(ents) != 0 {
		t.Fatalf("%d state files left after recovery", len(ents))
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
	if err := sys.configure(dev.Name(), netip.MustParseAddr("10.77.251.2"), netip.MustParseAddr("10.77.251.1"), tunnelMTU); err != nil {
		fail(err)
	}
	if _, err := sys.clientRoutes(st, dev.Name(), clientRouting{routes: []netip.Prefix{netip.MustParsePrefix("198.51.100.0/24")}}); err != nil {
		fail(err)
	}
	if err := darwinExcept(st, netip.MustParsePrefix("192.0.2.77/32")); err != nil {
		fail(err)
	}
	fmt.Println("READY", dev.Name())
	time.Sleep(time.Hour)
}
