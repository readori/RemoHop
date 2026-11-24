package signal

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/readori/RemoHop/internal/auth"
)

// ErrRelayPortsFull is returned by a port-range listener when every port
// of the range is taken.
var ErrRelayPortsFull = errors.New("every port of the relay port range is in use")

// PortRange is an inclusive range of UDP ports.
type PortRange struct{ Lo, Hi int }

// Size is the number of ports in the range.
func (r PortRange) Size() int { return r.Hi - r.Lo + 1 }

func (r PortRange) String() string { return fmt.Sprintf("%d-%d", r.Lo, r.Hi) }

// ParsePortRange parses "LO-HI" (or a single port).
func ParsePortRange(s string) (PortRange, error) {
	lo, hi, found := strings.Cut(s, "-")
	if !found {
		hi = lo
	}
	a, err1 := strconv.Atoi(lo)
	b, err2 := strconv.Atoi(hi)
	if err1 != nil || err2 != nil || a < 1024 || b > 65535 || a > b {
		return PortRange{}, fmt.Errorf("%q is not a port range like 40000-40099 (ports 1024-65535)", s)
	}
	return PortRange{a, b}, nil
}

// PortRangeListener returns a RelayConfig.Listen that binds each relay
// socket to a free port of r on host ("" for all addresses), so that a
// firewall needs to open only that range. It starts at a random port of
// the range, so that the ports of finished sessions are not reused at
// once, and fails with ErrRelayPortsFull when all are taken.
func PortRangeListener(host string, r PortRange) func() (net.PacketConn, error) {
	return func() (net.PacketConn, error) {
		n := r.Size()
		start := rand.IntN(n)
		for i := 0; i < n; i++ {
			port := r.Lo + (start+i)%n
			pc, err := net.ListenPacket("udp", net.JoinHostPort(host, strconv.Itoa(port)))
			if err == nil {
				return pc, nil
			}
			if !addrInUse(err) {
				return nil, err
			}
		}
		return nil, ErrRelayPortsFull
	}
}

// firewallPlan is what a firewall must let in for `remohop signal`.
type firewallPlan struct {
	main, alt int       // UDP ports; alt 0 when off
	relay     bool      // relay enabled
	ports     PortRange // relay range; zero: any ephemeral port
	// wsTCP is the websocket bridge's TCP port when it listens on a
	// public address; wsLoopback says it listens on loopback (behind a
	// tunnel), where no port needs opening.
	wsTCP      int
	wsLoopback bool
}

func (p firewallPlan) ranges() []PortRange {
	var out []PortRange
	if p.alt == p.main+1 {
		out = append(out, PortRange{p.main, p.alt})
	} else {
		out = append(out, PortRange{p.main, p.main})
		if p.alt != 0 {
			out = append(out, PortRange{p.alt, p.alt})
		}
	}
	if p.relay {
		if p.ports.Lo == 0 {
			out = append(out, PortRange{1024, 65535})
		} else {
			out = append(out, p.ports)
		}
	}
	return out
}

// osFamily reads /etc/os-release: "debian" for Debian and Ubuntu (the
// iptables/netfilter-persistent setup of Oracle's Ubuntu images), "rhel"
// for Oracle Linux, RHEL and friends (firewalld), or "" when unknown.
func osFamily(osRelease string) string {
	f, err := os.Open(osRelease)
	if err != nil {
		return ""
	}
	defer f.Close()
	ids := ""
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if ok && (k == "ID" || k == "ID_LIKE") {
			ids += " " + strings.Trim(v, `"'`)
		}
	}
	for _, id := range strings.Fields(ids) {
		switch id {
		case "ubuntu", "debian":
			return "debian"
		case "ol", "rhel", "centos", "fedora", "rocky", "almalinux":
			return "rhel"
		}
	}
	return ""
}

// printFirewall writes the commands that open the plan's ports on this
// OS. Nothing is run: changing a firewall is the operator's call.
func printFirewall(w io.Writer, p firewallPlan, family string) {
	rs := p.ranges()
	fmt.Fprintln(w, "# Open these UDP ports for `remohop signal` (nothing below has been run):")
	for _, r := range rs {
		what := "rendezvous and NAT detection"
		if r.Lo == r.Hi && r.Lo == p.main {
			what = "rendezvous"
		} else if r.Lo == r.Hi && r.Lo == p.alt {
			what = "NAT detection"
		}
		if p.relay && r == rs[len(rs)-1] {
			what = "relayed sessions"
			if p.ports.Lo == 0 {
				what += " (no --relay-ports: ANY high port; consider --relay-ports 40000-40099)"
			}
		}
		fmt.Fprintf(w, "#   %s/udp  %s\n", r, what)
	}
	if p.wsTCP != 0 {
		fmt.Fprintf(w, "#   %d/tcp  websocket bridge (--ws-listen)\n", p.wsTCP)
	} else if p.wsLoopback {
		fmt.Fprintln(w, "#   (the websocket bridge listens on loopback for a tunnel such as cloudflared: no port to open)")
	}
	iptables := func() {
		fmt.Fprintln(w, "# iptables (Ubuntu/Debian; on Oracle's Ubuntu images the INPUT chain ends in a REJECT rule, so insert at the top):")
		for _, r := range rs {
			fmt.Fprintf(w, "sudo iptables -I INPUT -p udp --dport %s -j ACCEPT\n", strings.Replace(r.String(), "-", ":", 1))
		}
		if p.wsTCP != 0 {
			fmt.Fprintf(w, "sudo iptables -I INPUT -p tcp --dport %d -j ACCEPT\n", p.wsTCP)
		}
		fmt.Fprintln(w, "# keep them across reboots (writes /etc/iptables/rules.v4):")
		fmt.Fprintln(w, "sudo netfilter-persistent save    # or: sudo sh -c 'iptables-save > /etc/iptables/rules.v4'")
	}
	firewalld := func() {
		fmt.Fprintln(w, "# firewalld (Oracle Linux, RHEL, Fedora):")
		for _, r := range rs {
			fmt.Fprintf(w, "sudo firewall-cmd --permanent --add-port=%s/udp\n", r)
		}
		if p.wsTCP != 0 {
			fmt.Fprintf(w, "sudo firewall-cmd --permanent --add-port=%d/tcp\n", p.wsTCP)
		}
		fmt.Fprintln(w, "sudo firewall-cmd --reload")
	}
	switch family {
	case "debian":
		iptables()
	case "rhel":
		firewalld()
	default:
		fmt.Fprintln(w, "# (this OS was not recognised; both common variants follow)")
		iptables()
		firewalld()
	}
	fmt.Fprintln(w, "# The cloud provider filters too. On Oracle Cloud: Networking > Virtual cloud networks > your VCN >")
	fmt.Fprintln(w, "# Security Lists (or the instance's network security group) > Add Ingress Rules: source 0.0.0.0/0,")
	fmt.Fprintf(w, "# IP protocol UDP, destination port range %s", rs[0])
	for _, r := range rs[1:] {
		fmt.Fprintf(w, ", then %s", r)
	}
	fmt.Fprintln(w, " (one rule per range).")
	if p.wsTCP != 0 {
		fmt.Fprintf(w, "# and one more with IP protocol TCP, destination port %d, for the websocket bridge.\n", p.wsTCP)
	}
}

// printPin writes how clients should be pointed at this server and pin its
// key: in their config, or in a build.
func printPin(w io.Writer, key string, port int) {
	addr := "HOST"
	if port != DefaultPort {
		addr += ":" + strconv.Itoa(port)
	}
	fmt.Fprintf(w, "server key: %s\n", key)
	fmt.Fprintf(w, "clients: remohop config set signal %s && remohop config set signal-pin %s\n", addr, key)
	fmt.Fprintf(w, "builds:  go build -ldflags \"-X github.com/readori/RemoHop/internal/dial.DefaultSignal=%s -X github.com/readori/RemoHop/internal/dial.DefaultSignalPin=%s\" ./cmd/remohop\n", addr, key)
	fmt.Fprintln(w, "         (HOST: this server's public name or address)")
}

// showKey prints the key of the server whose data lives in dir, creating
// it if it does not exist yet, without starting the server.
func showKey(w io.Writer, dir string, port int) error {
	id, err := auth.LoadIdentityFile(dir + string(os.PathSeparator) + keyFile)
	if err != nil {
		return err
	}
	printPin(w, auth.EncodeKey(id.Public()), port)
	return nil
}

// firewallPlanFor works out the ports `remohop signal` will use with these
// flags.
func firewallPlanFor(listen, alt string, relay bool, ports PortRange, wsListen string) (firewallPlan, error) {
	_, p, err := net.SplitHostPort(listen)
	if err != nil {
		return firewallPlan{}, fmt.Errorf("--listen %q: %w", listen, err)
	}
	main, err := strconv.Atoi(p)
	if err != nil || main <= 0 || main > 65535 {
		return firewallPlan{}, fmt.Errorf("--listen %q: needs a fixed port", listen)
	}
	plan := firewallPlan{main: main, relay: relay, ports: ports}
	switch alt {
	case "off":
	case "":
		plan.alt = main + 1
	default:
		_, ap, err := net.SplitHostPort(alt)
		if plan.alt, _ = strconv.Atoi(ap); err != nil || plan.alt <= 0 {
			return firewallPlan{}, fmt.Errorf("--alt %q: needs a fixed port", alt)
		}
	}
	if wsListen != "" {
		_, p, err := net.SplitHostPort(wsListen)
		port, perr := strconv.Atoi(p)
		if err != nil || perr != nil || port <= 0 || port > 65535 {
			return firewallPlan{}, fmt.Errorf("--ws-listen %q: needs a fixed port", wsListen)
		}
		if loopbackListen(wsListen) {
			plan.wsLoopback = true
		} else {
			plan.wsTCP = port
		}
	}
	return plan, nil
}

// perConnBytes is what one open connection costs the server, measured
// with the load test (TestLoad): about 70 KB per online host, rounded up
// for headroom.
const perConnBytes = 96 << 10

// memoryBudget is the memory this process may use: the tightest cgroup v2
// memory.max on the path from this process's own cgroup up to the root of
// cgroupRoot (a systemd MemoryMax=, a container limit), else the machine's
// RAM; 0 if neither can be read.
//
// The process's cgroup comes from selfCgroup (/proc/self/cgroup). A
// systemd service does not get a cgroup namespace, so its limit is not in
// /sys/fs/cgroup/memory.max (that is the root cgroup, which has none) but
// in /sys/fs/cgroup/system.slice/NAME.service/memory.max. In a container
// with a cgroup namespace the path is "/" and the root file is the limit.
func memoryBudget(cgroupRoot, selfCgroup, meminfo string) int64 {
	if v := cgroupMemoryLimit(cgroupRoot, selfCgroup); v > 0 {
		return v
	}
	f, err := os.Open(meminfo)
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if fields := strings.Fields(sc.Text()); len(fields) >= 2 && fields[0] == "MemTotal:" {
			kb, _ := strconv.ParseInt(fields[1], 10, 64)
			return kb << 10
		}
	}
	return 0
}

// cgroupMemoryLimit is the smallest memory.max set on this process's
// cgroup or any of its ancestors, or 0 when none is set or readable.
func cgroupMemoryLimit(root, selfCgroup string) int64 {
	rel := "/"
	if b, err := os.ReadFile(selfCgroup); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			// cgroup v2: "0::/system.slice/remohop-signal.service"
			if p, ok := strings.CutPrefix(strings.TrimSpace(line), "0::"); ok && strings.HasPrefix(p, "/") {
				rel = p
				break
			}
		}
	}
	var limit int64
	// path.Clean resolves any ".." lexically, so the walk stays below root.
	for dir := path.Clean(rel); ; dir = path.Dir(dir) {
		if b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(dir), "memory.max")); err == nil {
			if v, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64); err == nil && v > 0 && (limit == 0 || v < limit) {
				limit = v
			}
		}
		if dir == "/" || dir == "." {
			break
		}
	}
	return limit
}

// defaultMaxConns sizes the connection limit to the memory budget: 60 % of
// it for connections, between 1000 and the library's default of 50000. A
// 1 GB machine gets about 6400, a service with MemoryMax=512M about 3200.
func defaultMaxConns(budget int64) int {
	if budget <= 0 {
		return 0 // the library default
	}
	return int(min(max(budget*6/10/perConnBytes, 1000), 50000))
}
