//go:build linux

package vpn

import (
	"bufio"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

// ifPrefix names tunnel interfaces remohop0, remohop1, ... Firewall rules match
// the whole family with one wildcard.
const ifPrefix = "remohop"

func newPlatform() platform {
	return platform{
		privileged:    linuxPrivileged,
		hostSupported: func() error { return nil },
		open:          openTUN,
		usedNets:      linuxUsedNets,
		configure:     linuxConfigure,
		clientRoutes:  linuxClientRoutes,
		hostForward:   linuxHostForward,
		hostRelease:   linuxHostRelease,
		resolvers:     linuxResolvers,
	}
}

func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

const capNetAdmin = 12

func hasCapNetAdmin() bool {
	hdr := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	var data [2]unix.CapUserData
	if err := unix.Capget(&hdr, &data[0]); err != nil {
		return false
	}
	return data[0].Effective&(1<<capNetAdmin) != 0
}

func linuxPrivileged() error {
	if os.Geteuid() == 0 || hasCapNetAdmin() {
		return nil
	}
	return errPrivilege("a VPN tunnel")
}

// runPrivileged runs a system tool. A process that is not root but holds
// CAP_NET_ADMIN through file capabilities would start `ip` without it:
// capabilities are not inherited across exec unless they are also in the
// inheritable and ambient sets. Those are per thread, so they are raised
// on this thread, which is then the one that forks.
func runPrivileged(cmd *exec.Cmd) ([]byte, error) {
	if os.Geteuid() != 0 {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		hdr := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
		var data [2]unix.CapUserData
		if unix.Capget(&hdr, &data[0]) == nil {
			data[0].Inheritable |= data[0].Permitted
			if unix.Capset(&hdr, &data[0]) == nil {
				for c := uintptr(0); c < 32; c++ {
					if data[0].Permitted&(1<<c) != 0 {
						unix.Prctl(unix.PR_CAP_AMBIENT, unix.PR_CAP_AMBIENT_RAISE, c, 0, 0)
					}
				}
			}
		}
	}
	return cmd.CombinedOutput()
}

type tun struct {
	f    *os.File
	name string
}

func (t *tun) Read(p []byte) (int, error)  { return t.f.Read(p) }
func (t *tun) Write(p []byte) (int, error) { return t.f.Write(p) }
func (t *tun) Close() error                { return t.f.Close() }
func (t *tun) Name() string                { return t.name }

// openTUN creates a TUN interface. It is not persistent: the kernel
// removes it, with its addresses and every route through it, as soon as
// this descriptor is closed, which includes the process dying.
func openTUN(string) (Device, error) {
	fd, err := unix.Open("/dev/net/tun", unix.O_RDWR|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil, errors.New("this kernel has no TUN support (/dev/net/tun is missing; load the `tun` module)")
		}
		return nil, fmt.Errorf("open /dev/net/tun: %w", err)
	}
	ifr, err := unix.NewIfreq(ifPrefix + "%d")
	if err != nil {
		unix.Close(fd)
		return nil, err
	}
	ifr.SetUint16(unix.IFF_TUN | unix.IFF_NO_PI)
	if err := unix.IoctlIfreq(fd, unix.TUNSETIFF, ifr); err != nil {
		unix.Close(fd)
		if errors.Is(err, unix.EPERM) {
			return nil, errPrivilege("creating the tunnel interface")
		}
		return nil, fmt.Errorf("create TUN interface: %w", err)
	}
	return &tun{f: os.NewFile(uintptr(fd), "/dev/net/tun"), name: ifr.Name()}, nil
}

// linuxUsedNets reads the main routing table and the interface addresses.
func linuxUsedNets() []netip.Prefix {
	out := interfaceNets()
	f, err := os.Open("/proc/net/route")
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Scan() // header
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 8 {
			continue
		}
		dst, err1 := hex.DecodeString(fields[1])
		mask, err2 := hex.DecodeString(fields[7])
		if err1 != nil || err2 != nil || len(dst) != 4 || len(mask) != 4 {
			continue
		}
		// The kernel prints these in host byte order of a little-endian
		// view of the network-order address.
		d := binary.LittleEndian.Uint32(dst)
		m := binary.LittleEndian.Uint32(mask)
		bits := 0
		for b := uint32(1) << 31; b != 0 && m&b != 0; b >>= 1 {
			bits++
		}
		if bits == 0 {
			continue // a default route collides with nothing
		}
		var a [4]byte
		binary.BigEndian.PutUint32(a[:], d)
		out = append(out, netip.PrefixFrom(netip.AddrFrom4(a), bits).Masked())
	}
	return out
}

func linuxConfigure(dev string, local, peer netip.Addr, mtu int) error {
	if err := run("ip", "addr", "add", local.String(), "peer", peer.String()+"/32", "dev", dev); err != nil {
		return err
	}
	return run("ip", "link", "set", "dev", dev, "mtu", fmt.Sprint(mtu), "up")
}

// via finds how addr is reached right now, as arguments for `ip route`.
func via(addr netip.Addr) ([]string, error) {
	out, err := output("ip", "-4", "route", "get", addr.String())
	if err != nil {
		return nil, err
	}
	f := strings.Fields(out)
	var gw, dev string
	for i := 0; i+1 < len(f); i++ {
		switch f[i] {
		case "via":
			gw = f[i+1]
		case "dev":
			dev = f[i+1]
		}
	}
	if dev == "" || isTunnelName(dev) {
		return nil, fmt.Errorf("no route to %s outside the tunnel", addr)
	}
	if gw != "" {
		return []string{"via", gw, "dev", dev}, nil
	}
	return []string{"dev", dev}, nil
}

// except pins dst to the way it is routed today, before the tunnel takes
// over the default.
func except(st *state, dst netip.Prefix) error {
	hop, err := via(dst.Addr())
	if err != nil {
		return err
	}
	add := append([]string{"ip", "route", "add", dst.String()}, hop...)
	del := append([]string{"ip", "route", "del", dst.String()}, hop...)
	if err := st.record(undoStep{Cmd: del}); err != nil {
		return err
	}
	if err := run(add...); err != nil && !strings.Contains(err.Error(), "File exists") {
		return err
	}
	return nil
}

func linuxClientRoutes(st *state, dev string, o clientRouting) (notes []string, err error) {
	// Routes through the tunnel interface need no undo: they go when the
	// interface goes.
	for _, r := range o.routes {
		if err := run("ip", "route", "add", r.String(), "dev", dev); err != nil {
			return nil, err
		}
	}
	if !o.exit {
		return nil, nil
	}
	for _, a := range o.keep {
		if err := except(st, netip.PrefixFrom(a, 32)); err != nil {
			return nil, fmt.Errorf("keeping the session's own path off the tunnel: %w", err)
		}
	}
	for _, b := range o.bypass {
		if err := except(st, b); err != nil {
			return nil, fmt.Errorf("bypass %s: %w", b, err)
		}
	}
	// Two halves instead of replacing the default route: more specific
	// than it, so they win, and nothing of the user's is modified. The
	// LAN's own routes are more specific still and keep working.
	for _, half := range []string{"0.0.0.0/1", "128.0.0.0/1"} {
		if err := run("ip", "route", "add", half, "dev", dev); err != nil {
			return nil, err
		}
	}
	if len(o.dns) > 0 {
		if ok, why := ResolvedSharesNetns(); !ok {
			notes = append(notes, "DNS still uses this machine's current resolvers ("+why+")")
			return notes, nil
		}
		if _, err := exec.LookPath("resolvectl"); err == nil {
			args := []string{"resolvectl", "dns", dev}
			for _, d := range o.dns {
				args = append(args, d.String())
			}
			// Per-link settings: systemd-resolved drops them with the link.
			if run(args...) == nil && run("resolvectl", "domain", dev, "~.") == nil {
				notes = append(notes, "DNS goes through the tunnel")
				return notes, nil
			}
		}
		notes = append(notes, "DNS still uses this machine's current resolvers (no systemd-resolved to redirect it)")
	}
	return notes, nil
}

func firewallTool() (string, error) {
	if t := os.Getenv("REMOHOP_VPN_FIREWALL"); t == "iptables" || t == "nft" {
		return t, nil
	}
	for _, t := range []string{"iptables", "nft"} {
		if _, err := exec.LookPath(t); err == nil {
			return t, nil
		}
	}
	return "", errors.New("forwarding for a VPN client needs iptables or nft on the host, and neither is installed")
}

func setSysctl(st *state, path, value string) error {
	old, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(old)) == value {
		return nil
	}
	if st != nil {
		if err := st.record(undoStep{Sysctl: path, Value: strings.TrimSpace(string(old))}); err != nil {
			return err
		}
	}
	return os.WriteFile(path, []byte(value), 0o644)
}

// shared is the part of forwarding that tunnels of one host have in
// common: packets coming back for a client arrive on the host's ordinary
// interfaces, which must therefore forward too. Where that had to be
// switched on, a guard rule keeps those interfaces from forwarding
// anything that is not headed into a tunnel, so the host does not turn
// into a general router while a tunnel is up. Both are undone when the
// last tunnel ends.
var shared struct {
	mu   sync.Mutex
	refs int
	st   *state
}

func linuxHostRelease() {
	shared.mu.Lock()
	defer shared.mu.Unlock()
	if shared.refs--; shared.refs > 0 || shared.st == nil {
		return
	}
	shared.st.undo()
	shared.st = nil
}

func acquireShared(tool string) error {
	shared.mu.Lock()
	defer shared.mu.Unlock()
	shared.refs++
	if shared.refs > 1 {
		return nil
	}
	st, err := newState("host-shared", "fwd")
	if err != nil {
		return err
	}
	shared.st = st
	ents, err := os.ReadDir("/proc/sys/net/ipv4/conf")
	if err != nil {
		return err
	}
	for _, e := range ents {
		name := e.Name()
		if name == "all" || name == "default" || name == "lo" || isTunnelName(name) {
			continue
		}
		path := "/proc/sys/net/ipv4/conf/" + name + "/forwarding"
		if old, err := os.ReadFile(path); err != nil || strings.TrimSpace(string(old)) == "1" {
			continue
		}
		// Guard first, then open the switch.
		if tool == "iptables" {
			rule := []string{"-i", name, "!", "-o", ifPrefix + "+", "-m", "comment", "--comment", "remohop-vpn:guard", "-j", "DROP"}
			if err := st.record(undoStep{Cmd: append([]string{"iptables", "-w", "-D", "FORWARD"}, rule...)}); err != nil {
				return err
			}
			if err := run(append([]string{"iptables", "-w", "-I", "FORWARD", "1"}, rule...)...); err != nil {
				return err
			}
		} else {
			table := "remohop_vpn_guard_" + sanitizeName(name)
			if err := st.record(undoStep{Cmd: []string{"nft", "delete", "table", "ip", table}}); err != nil {
				return err
			}
			script := fmt.Sprintf("table ip %s { chain forward { type filter hook forward priority -10; policy accept; iifname \"%s\" oifname != \"%s*\" drop; }; }", table, name, ifPrefix)
			if err := run("nft", script); err != nil {
				return err
			}
		}
		if err := setSysctl(st, path, "1"); err != nil {
			return err
		}
	}
	return nil
}

func sanitizeName(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return '_'
	}, s)
}

// linuxHostForward lets the client behind dev out through this host: its
// packets are forwarded and leave with the host's address; only replies
// come back in. Every rule names the tunnel interface or the client's
// tunnel address, and is tagged, so removal is exact.
func linuxHostForward(st *state, dev string, client netip.Addr) error {
	return linuxForward(st, dev, client.String())
}

// linuxForward is linuxHostForward for a source that is an address or a
// network (the mesh network forwards for all its members at once).
func linuxForward(st *state, dev string, client string) error {
	tool, err := firewallTool()
	if err != nil {
		return err
	}
	if tool == "iptables" {
		tag := []string{"-m", "comment", "--comment", "remohop-vpn:" + dev}
		type rule struct {
			table string
			chain []string // insertion
			spec  []string
		}
		rules := []rule{
			{"nat", []string{"-A", "POSTROUTING"}, append([]string{"-s", client, "!", "-o", ifPrefix + "+"}, append(tag, "-j", "MASQUERADE")...)},
			// Inserted at the top one by one, so the last listed is first.
			{"filter", []string{"-I", "FORWARD", "1"}, append([]string{"-o", dev}, append(tag, "-j", "DROP")...)},
			{"filter", []string{"-I", "FORWARD", "1"}, append([]string{"-o", dev, "-m", "conntrack", "--ctstate", "ESTABLISHED,RELATED"}, append(tag, "-j", "ACCEPT")...)},
			{"filter", []string{"-I", "FORWARD", "1"}, append([]string{"-i", dev, "!", "-o", ifPrefix + "+"}, append(tag, "-j", "ACCEPT")...)},
		}
		for _, r := range rules {
			del := append([]string{"iptables", "-w", "-t", r.table, "-D", r.chain[1]}, r.spec...)
			if err := st.record(undoStep{Cmd: del}); err != nil {
				return err
			}
			add := append(append([]string{"iptables", "-w", "-t", r.table}, r.chain...), r.spec...)
			if err := run(add...); err != nil {
				return err
			}
		}
	} else {
		table := "remohop_vpn_" + dev
		if err := st.record(undoStep{Cmd: []string{"nft", "delete", "table", "ip", table}}); err != nil {
			return err
		}
		script := fmt.Sprintf(`table ip %[1]s {
	chain forward { type filter hook forward priority -5; policy accept;
		iifname "%[2]s" oifname != "%[3]s*" accept
		oifname "%[2]s" ct state established,related accept
		oifname "%[2]s" drop
	}
	chain postrouting { type nat hook postrouting priority 100; policy accept;
		ip saddr %[4]s oifname != "%[3]s*" masquerade
	}
}`, table, dev, ifPrefix, client)
		if err := run("nft", script); err != nil {
			return err
		}
	}
	// Packets arriving on the tunnel may be forwarded. The switch goes
	// away with the interface.
	if err := os.WriteFile("/proc/sys/net/ipv4/conf/"+dev+"/forwarding", []byte("1"), 0o644); err != nil {
		return err
	}
	return acquireShared(tool)
}

func linuxResolvers() []string {
	var out []string
	for _, path := range []string{"/etc/resolv.conf", "/run/systemd/resolve/resolv.conf"} {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			f := strings.Fields(line)
			if len(f) >= 2 && f[0] == "nameserver" {
				if a, err := netip.ParseAddr(f[1]); err == nil && a.Is4() && !a.IsLoopback() && len(out) < 3 {
					out = append(out, a.String())
				}
			}
		}
		if len(out) > 0 {
			break
		}
	}
	return out
}
