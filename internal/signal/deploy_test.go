package signal

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/nat"
	"github.com/readori/RemoHop/internal/nat/vnet"
)

func TestParsePortRange(t *testing.T) {
	if r, err := ParsePortRange("40000-40099"); err != nil || r != (PortRange{40000, 40099}) || r.Size() != 100 {
		t.Fatalf("%v %v", r, err)
	}
	if r, err := ParsePortRange("50000"); err != nil || r.Size() != 1 {
		t.Fatalf("single port: %v %v", r, err)
	}
	for _, bad := range []string{"", "x", "40099-40000", "80-90", "40000-70000", "1-2-3", "-5"} {
		if _, err := ParsePortRange(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// Relay sockets stay inside the range, and a full range says so.
func TestPortRangeListener(t *testing.T) {
	// Find two adjacent free ports.
	probe, _ := net.ListenPacket("udp", "127.0.0.1:0")
	base := probe.LocalAddr().(*net.UDPAddr).Port
	probe.Close()
	if base > 65000 {
		base = 41000
	}
	r := PortRange{base, base + 1}
	listen := PortRangeListener("127.0.0.1", r)
	a, err := listen()
	if err != nil {
		t.Skipf("ports %s not free: %v", r, err)
	}
	defer a.Close()
	b, err := listen()
	if err != nil {
		t.Skipf("ports %s not free: %v", r, err)
	}
	for _, pc := range []net.PacketConn{a, b} {
		if p := pc.LocalAddr().(*net.UDPAddr).Port; p < r.Lo || p > r.Hi {
			t.Fatalf("bound port %d outside %s", p, r)
		}
	}
	if _, err := listen(); !errors.Is(err, ErrRelayPortsFull) {
		t.Fatalf("third socket in a two-port range: %v", err)
	}
	b.Close()
	c, err := listen()
	if err != nil {
		t.Fatalf("after a port was freed: %v", err)
	}
	c.Close()
}

// When the relay range is exhausted the client is told "full", and the
// session slot is given back.
func TestRelayRangeFull(t *testing.T) {
	tn := &testNet{t: t, net: vnet.New(), dir: t.TempDir()}
	host := tn.net.Host("198.51.100.1")
	srv, err := NewServer(Config{DataDir: tn.dir, Relay: &RelayConfig{
		Listen: func() (net.PacketConn, error) { return nil, ErrRelayPortsFull },
	}})
	if err != nil {
		t.Fatal(err)
	}
	sctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(sctx, host.MustListen(21116), host.MustListen(21117)) }()
	t.Cleanup(func() { cancel(); <-done; srv.Close() })
	ctx := testCtx(t)
	hc := tn.dial(tn.socket(tn.net.Host("192.0.2.1")), Options{})
	reg, err := hc.Register(ctx, newIdentity(t))
	if err != nil {
		t.Fatal(err)
	}
	hc.OnRelay = func([]byte, netip.AddrPort) error { return nil }
	go hc.ServeOffers(ctx, func(Offer) Answer { return Answer{} })
	cc := tn.dial(tn.socket(tn.net.Host("192.0.2.2")), Options{})
	in, err := cc.Connect(ctx, reg.ID, nil, nat.TypeUnknown)
	if err != nil {
		t.Fatal(err)
	}
	_, err = cc.Relay(ctx, in.Nonce)
	if CodeOf(err) != CodeFull || !strings.Contains(err.Error(), "relay ports") {
		t.Fatalf("relay with the range exhausted: %v", err)
	}
	srv.mu.Lock()
	n := srv.relays
	srv.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d relay slots still taken", n)
	}
}

func TestFirewallCommands(t *testing.T) {
	plan, err := firewallPlanFor(":21116", "", true, PortRange{40000, 40099}, "")
	if err != nil {
		t.Fatal(err)
	}
	var deb, rhel, unknown bytes.Buffer
	printFirewall(&deb, plan, "debian")
	printFirewall(&rhel, plan, "rhel")
	printFirewall(&unknown, plan, "")
	for _, want := range []string{
		"sudo iptables -I INPUT -p udp --dport 21116:21117 -j ACCEPT\n",
		"sudo iptables -I INPUT -p udp --dport 40000:40099 -j ACCEPT\n",
		"sudo netfilter-persistent save", "/etc/iptables/rules.v4", "Security Lists", "21116-21117", "40000-40099",
	} {
		if !strings.Contains(deb.String(), want) {
			t.Errorf("debian output lacks %q:\n%s", want, deb.String())
		}
	}
	for _, want := range []string{
		"sudo firewall-cmd --permanent --add-port=21116-21117/udp\n",
		"sudo firewall-cmd --permanent --add-port=40000-40099/udp\n",
		"sudo firewall-cmd --reload\n", "Security Lists",
	} {
		if !strings.Contains(rhel.String(), want) {
			t.Errorf("rhel output lacks %q:\n%s", want, rhel.String())
		}
	}
	if strings.Contains(deb.String(), "firewall-cmd") || strings.Contains(rhel.String(), "iptables") {
		t.Error("the wrong tool was printed")
	}
	if !strings.Contains(unknown.String(), "iptables") || !strings.Contains(unknown.String(), "firewall-cmd") {
		t.Error("unknown OS: both variants expected")
	}
	// Relay without a range: every high port, and it says so.
	open, _ := firewallPlanFor(":21116", "", true, PortRange{}, "")
	var o bytes.Buffer
	printFirewall(&o, open, "debian")
	if !strings.Contains(o.String(), "--dport 1024:65535") || !strings.Contains(o.String(), "ANY high port") {
		t.Errorf("relay without --relay-ports:\n%s", o.String())
	}
	// No relay, NAT detection elsewhere or off.
	sep, _ := firewallPlanFor("0.0.0.0:443", "0.0.0.0:3478", false, PortRange{}, "")
	if r := sep.ranges(); len(r) != 2 || r[0] != (PortRange{443, 443}) || r[1] != (PortRange{3478, 3478}) {
		t.Errorf("ranges %v", r)
	}
	off, _ := firewallPlanFor(":21116", "off", false, PortRange{}, "")
	if r := off.ranges(); len(r) != 1 {
		t.Errorf("alt off: %v", r)
	}
	if _, err := firewallPlanFor(":0", "", false, PortRange{}, ""); err == nil {
		t.Error("ephemeral --listen port accepted for a firewall plan")
	}
}

func TestOSFamily(t *testing.T) {
	dir := t.TempDir()
	for content, want := range map[string]string{
		"NAME=\"Ubuntu\"\nID=ubuntu\nID_LIKE=debian\n":                  "debian",
		"NAME=\"Oracle Linux Server\"\nID=\"ol\"\nID_LIKE=\"fedora\"\n": "rhel",
		"ID=\"rocky\"\nID_LIKE=\"rhel centos fedora\"\n":                "rhel",
		"ID=alpine\n": "",
		"ID=linuxmint\nID_LIKE=\"ubuntu debian\"\n": "debian",
	} {
		p := filepath.Join(dir, "os-release")
		os.WriteFile(p, []byte(content), 0o644)
		if got := osFamily(p); got != want {
			t.Errorf("%q: %q, want %q", content, got, want)
		}
	}
	if osFamily(filepath.Join(dir, "missing")) != "" {
		t.Error("missing file")
	}
}

func TestMemorySizing(t *testing.T) {
	dir := t.TempDir()
	cg, mi := filepath.Join(dir, "memory.max"), filepath.Join(dir, "meminfo")
	self := filepath.Join(dir, "self-cgroup") // missing: the process is at the root ("/")
	os.WriteFile(mi, []byte("MemTotal:        1006632 kB\nMemFree: 1 kB\n"), 0o644)
	os.WriteFile(cg, []byte("max\n"), 0o644)
	if b := memoryBudget(dir, self, mi); b != 1006632<<10 {
		t.Fatalf("no cgroup limit: %d", b)
	}
	os.WriteFile(cg, []byte("536870912\n"), 0o644)
	if b := memoryBudget(dir, self, mi); b != 512<<20 {
		t.Fatalf("cgroup limit: %d", b)
	}
	if b := memoryBudget(filepath.Join(dir, "x"), self, filepath.Join(dir, "y")); b != 0 {
		t.Fatalf("nothing readable: %d", b)
	}

	// A systemd service: no cgroup namespace, so the limit is on the
	// service's own cgroup, not on the root.
	root := filepath.Join(dir, "cgroup")
	svc := filepath.Join(root, "system.slice", "remohop-signal.service")
	os.MkdirAll(svc, 0o755)
	os.WriteFile(filepath.Join(root, "system.slice", "memory.max"), []byte("max\n"), 0o644)
	os.WriteFile(filepath.Join(svc, "memory.max"), []byte("536870912\n"), 0o644)
	os.WriteFile(self, []byte("0::/system.slice/remohop-signal.service\n"), 0o644)
	if b := memoryBudget(root, self, mi); b != 512<<20 {
		t.Fatalf("service cgroup limit: %d", b)
	}
	// A tighter limit higher up wins.
	os.WriteFile(filepath.Join(root, "system.slice", "memory.max"), []byte("268435456\n"), 0o644)
	if b := memoryBudget(root, self, mi); b != 256<<20 {
		t.Fatalf("parent cgroup limit: %d", b)
	}
	// A path that tries to climb out of the root is cleaned, not followed.
	os.WriteFile(self, []byte("0::/../../etc\n"), 0o644)
	if b := memoryBudget(root, self, mi); b != 1006632<<10 {
		t.Fatalf("escaping path: %d", b)
	}
	if n := defaultMaxConns(1 << 30); n < 6000 || n > 7000 {
		t.Errorf("1 GB: %d connections", n)
	}
	if n := defaultMaxConns(512 << 20); n < 3000 || n > 3500 {
		t.Errorf("512 MB: %d connections", n)
	}
	if defaultMaxConns(64<<20) != 1000 || defaultMaxConns(64<<30) != 50000 || defaultMaxConns(0) != 0 {
		t.Error("bounds")
	}
}

func runSignalCmd(t *testing.T, ctx context.Context, args ...string) (int, string, string) {
	t.Helper()
	var out, errb syncBuffer
	code := cli.Lookup("signal").Run(ctx, &cli.Env{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb}, args)
	return code, out.String(), errb.String()
}

func TestSignalCommandDeploymentFlags(t *testing.T) {
	t.Setenv("REMOHOP_CONFIG_DIR", t.TempDir())
	ctx := context.Background()
	code, out, _ := runSignalCmd(t, ctx, "--print-firewall", "--relay", "--relay-ports", "40000-40099")
	if code != cli.ExitOK || !strings.Contains(out, "40000") || !strings.Contains(out, "21116") || !strings.Contains(out, "nothing below has been run") {
		t.Fatalf("--print-firewall: %d\n%s", code, out)
	}
	if code, _, errOut := runSignalCmd(t, ctx, "--relay-ports", "40000-40099"); code != cli.ExitUsage || !strings.Contains(errOut, "needs --relay") {
		t.Errorf("--relay-ports without --relay: %d %q", code, errOut)
	}
	if code, _, _ := runSignalCmd(t, ctx, "--relay", "--relay-ports", "80-90"); code != cli.ExitUsage {
		t.Error("privileged relay ports accepted")
	}
	data := t.TempDir()
	code, out, _ = runSignalCmd(t, ctx, "--show-key", "--data", data)
	if code != cli.ExitOK || !strings.Contains(out, "remohop config set signal-pin ") || !strings.Contains(out, "DefaultSignalPin=") {
		t.Fatalf("--show-key: %d\n%s", code, out)
	}
	id, err := auth.LoadIdentityFile(filepath.Join(data, keyFile))
	if err != nil || !strings.Contains(out, auth.EncodeKey(id.Public())) {
		t.Fatalf("--show-key printed another key than the server's (%v):\n%s", err, out)
	}

	// A real start prints the pin lines and the relay range, then stops.
	sctx, cancel := context.WithCancel(ctx)
	res := make(chan string, 1)
	go func() {
		_, out, errOut := runSignalCmd(t, sctx, "--listen", "127.0.0.1:0", "--alt", "off", "--data", data, "--relay", "--relay-ports", "45000-45009")
		res <- out + errOut
	}()
	time.Sleep(500 * time.Millisecond)
	cancel()
	got := <-res
	for _, want := range []string{"server key: " + auth.EncodeKey(id.Public()), "remohop config set signal-pin " + auth.EncodeKey(id.Public()),
		"DefaultSignal=HOST:", "ports 45000-45009", "up to 10 sessions", "connection limit sized to memory"} {
		if want == "connection limit sized to memory" && runtime.GOOS != "linux" {
			continue // memory sizing reads cgroup and /proc/meminfo, Linux only
		}
		if !strings.Contains(got, want) {
			t.Errorf("start output lacks %q:\n%s", want, got)
		}
	}
	// Without a range the operator is warned.
	sctx, cancel = context.WithCancel(ctx)
	go func() {
		_, out, errOut := runSignalCmd(t, sctx, "--listen", "127.0.0.1:0", "--alt", "off", "--data", data, "--relay")
		res <- out + errOut
	}()
	time.Sleep(500 * time.Millisecond)
	cancel()
	if got := <-res; !strings.Contains(got, "without --relay-ports") || !strings.Contains(got, "1024-65535") {
		t.Errorf("no warning without --relay-ports:\n%s", got)
	}
}

// syncBuffer is a bytes.Buffer safe to read while a command writes to it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
