//go:build darwin

package vpn

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// ifPrefix is the kernel's name for its tunnel interfaces.
const ifPrefix = "utun"

// The macOS side uses the kernel's utun interfaces and the BSD ifconfig and
// route tools; e2e_darwin_test.go exercises it against the real system (as
// root, opt-in). It covers the client role and a host that is reached itself;
// forwarding for a client (the host role with routes) would need pf rules
// and is refused.
func newPlatform() platform {
	return platform{
		privileged: func() error {
			if os.Geteuid() == 0 {
				return nil
			}
			return errPrivilege("a VPN tunnel")
		},
		hostSupported: func() error {
			return errors.New("a macOS host cannot forward for VPN clients yet (it can only be reached itself through the tunnel)")
		},
		open:         openUTUN,
		usedNets:     interfaceNets,
		configure:    darwinConfigure,
		clientRoutes: darwinClientRoutes,
		hostForward:  func(*state, string, netip.Addr) error { return errors.New("not supported on macOS") },
		hostRelease:  func() {},
		resolvers:    func() []string { return nil },
	}
}

func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func runPrivileged(cmd *exec.Cmd) ([]byte, error) { return cmd.CombinedOutput() }

type utun struct {
	f    *os.File
	name string
	rbuf []byte // Read is called from one goroutine only (the pump)
}

// A utun packet is preceded by a 4-byte address family in network order.
func (t *utun) Read(p []byte) (int, error) {
	if cap(t.rbuf) < len(p)+4 {
		t.rbuf = make([]byte, len(p)+4)
	}
	buf := t.rbuf[:len(p)+4]
	n, err := t.f.Read(buf)
	if err != nil {
		return 0, err
	}
	if n < 4 {
		return 0, nil
	}
	return copy(p, buf[4:n]), nil
}

func (t *utun) Write(p []byte) (int, error) {
	buf := make([]byte, len(p)+4)
	// The mesh carries IPv6 too; the VPN only IPv4.
	family := uint32(unix.AF_INET)
	if len(p) > 0 && p[0]>>4 == 6 {
		family = unix.AF_INET6
	}
	binary.BigEndian.PutUint32(buf, family)
	copy(buf[4:], p)
	if _, err := t.f.Write(buf); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (t *utun) Close() error { return t.f.Close() }
func (t *utun) Name() string { return t.name }

func openUTUN(string) (Device, error) {
	fd, err := unix.Socket(unix.AF_SYSTEM, unix.SOCK_DGRAM, 2 /* SYSPROTO_CONTROL */)
	if err != nil {
		return nil, fmt.Errorf("utun control socket: %w", err)
	}
	info := &unix.CtlInfo{}
	copy(info.Name[:], "com.apple.net.utun_control")
	if err := unix.IoctlCtlInfo(fd, info); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("utun control id: %w", err)
	}
	// Go marks the sockets it creates close-on-exec; this one comes straight
	// from the system call, and every ifconfig or route started below (or a
	// background child) would otherwise inherit it and keep the interface
	// alive after this process is gone.
	unix.CloseOnExec(fd)
	// Unit 0 lets the kernel pick the next free utunN.
	if err := unix.Connect(fd, &unix.SockaddrCtl{ID: info.Id, Unit: 0}); err != nil {
		unix.Close(fd)
		if errors.Is(err, unix.EPERM) {
			return nil, errPrivilege("creating the tunnel interface")
		}
		return nil, fmt.Errorf("create utun interface: %w", err)
	}
	name, err := unix.GetsockoptString(fd, 2 /* SYSPROTO_CONTROL */, 2 /* UTUN_OPT_IFNAME */)
	if err != nil {
		unix.Close(fd)
		return nil, err
	}
	if err := unix.SetNonblock(fd, true); err != nil {
		unix.Close(fd)
		return nil, err
	}
	return &utun{f: os.NewFile(uintptr(fd), name), name: name}, nil
}

// darwinConfigure sets the two ends of the point-to-point link. The netmask
// is given because ifconfig otherwise derives it from the address class
// (255.0.0.0 for 10.x), and the whole of 10.0.0.0/8 would then count as
// taken when the next tunnel looks for a free network. The kernel adds the
// host route to peer itself and removes it with the interface.
func darwinConfigure(dev string, local, peer netip.Addr, mtu int) error {
	return run("ifconfig", dev, "inet", local.String(), peer.String(), "netmask", "255.255.255.255", "mtu", fmt.Sprint(mtu), "up")
}

// darwinVia finds how addr is reached right now, as arguments for `route`:
// the gateway, or for a destination on a directly attached network (the
// host is on the same LAN, say) the interface.
func darwinVia(addr netip.Addr) ([]string, error) {
	out, err := output("route", "-n", "get", addr.String())
	if err != nil {
		return nil, err
	}
	var gw, ifc string
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		switch f[0] {
		case "gateway:":
			gw = f[1]
		case "interface:":
			ifc = f[1]
		}
	}
	// A gateway that is not an address is a link ("link#12"): on-link.
	if a, err := netip.ParseAddr(gw); err == nil && a.Is4() {
		return []string{gw}, nil
	}
	if ifc == "" {
		return nil, fmt.Errorf("no route to %s", addr)
	}
	return []string{"-interface", ifc}, nil
}

// darwinExcept pins dst to the way it is routed today, before the tunnel
// takes over the default, and records how to remove the pin.
func darwinExcept(st *state, dst netip.Prefix) error {
	hop, err := darwinVia(dst.Addr())
	if err != nil {
		return err
	}
	target := []string{"-net", dst.String()}
	if dst.Bits() == 32 {
		target = []string{"-host", dst.Addr().String()}
	}
	add := append(append([]string{"route", "-n", "add"}, target...), hop...)
	del := append(append([]string{"route", "-n", "delete"}, target...), hop...)
	if err := st.record(undoStep{Cmd: del}); err != nil {
		return err
	}
	return run(add...)
}

func darwinClientRoutes(st *state, dev string, o clientRouting) ([]string, error) {
	// Routes through the tunnel interface need no undo: they go when the
	// interface goes.
	for _, r := range o.routes {
		if err := run("route", "-n", "add", "-net", r.String(), "-interface", dev); err != nil {
			return nil, err
		}
	}
	if !o.exit {
		return nil, nil
	}
	for _, a := range o.keep {
		if err := darwinExcept(st, netip.PrefixFrom(a, 32)); err != nil {
			return nil, fmt.Errorf("keeping the session's own path off the tunnel: %w", err)
		}
	}
	for _, b := range o.bypass {
		if err := darwinExcept(st, b); err != nil {
			return nil, fmt.Errorf("bypass %s: %w", b, err)
		}
	}
	for _, half := range []string{"0.0.0.0/1", "128.0.0.0/1"} {
		if err := run("route", "-n", "add", "-net", half, "-interface", dev); err != nil {
			return nil, err
		}
	}
	return []string{"DNS still uses this machine's current resolvers"}, nil
}
