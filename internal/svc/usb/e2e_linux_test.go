//go:build linux

package usb

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/peer/peertest"
)

// fakeKernel is a sysfs tree plus a write hook that plays the kernel's part
// of usbip-host and vhci_hcd, including their socket handling: the "host
// kernel" echoes every byte back inverted; the "client kernel" sends data and
// checks it comes back inverted, through the whole remohop relay chain.
type fakeKernel struct {
	t    *testing.T
	root string

	mu       sync.Mutex
	writes   []string
	clientC  chan error // result of the client-kernel exchange
	payload  int
	failBind bool
}

func (k *fakeKernel) sys(p ...string) string {
	return filepath.Join(append([]string{k.root, "sys"}, p...)...)
}

func mkfile(t *testing.T, p, c string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
		t.Fatal(err)
	}
}

type devSpec struct {
	busid, vid, pid string
	class           string // device class hex
	ifClass         string
}

// newKernel builds the tree. withHost/withVHCI choose which modules exist.
func newKernel(t *testing.T, withHost, withVHCI bool, devs ...devSpec) *fakeKernel {
	k := &fakeKernel{t: t, root: t.TempDir(), clientC: make(chan error, 4), payload: 256 << 10}
	os.MkdirAll(k.sys("bus/usb/drivers/usb"), 0o755)
	mkfile(t, k.sys("bus/usb/drivers_probe"), "")
	if withHost {
		os.MkdirAll(k.sys("bus/usb/drivers/usbip-host"), 0o755)
	}
	for i, d := range devs {
		dir := k.sys("bus/usb/devices", d.busid)
		mkfile(t, filepath.Join(dir, "idVendor"), d.vid+"\n")
		mkfile(t, filepath.Join(dir, "idProduct"), d.pid+"\n")
		mkfile(t, filepath.Join(dir, "bcdDevice"), "0100\n")
		mkfile(t, filepath.Join(dir, "bDeviceClass"), d.class+"\n")
		mkfile(t, filepath.Join(dir, "bDeviceSubClass"), "00\n")
		mkfile(t, filepath.Join(dir, "bDeviceProtocol"), "00\n")
		mkfile(t, filepath.Join(dir, "busnum"), "1\n")
		mkfile(t, filepath.Join(dir, "devnum"), strconv.Itoa(i+2)+"\n")
		mkfile(t, filepath.Join(dir, "speed"), "480\n")
		mkfile(t, filepath.Join(dir, "bConfigurationValue"), "1\n")
		mkfile(t, filepath.Join(dir, "bNumConfigurations"), "1\n")
		mkfile(t, filepath.Join(dir, "manufacturer"), "ACME\n")
		mkfile(t, filepath.Join(dir, "product"), "Widget\x1b[31m\n")
		ic := d.ifClass
		if ic == "" {
			ic = "ff"
		}
		mkfile(t, filepath.Join(dir, d.busid+":1.0", "bInterfaceClass"), ic+"\n")
		mkfile(t, filepath.Join(dir, d.busid+":1.0", "bInterfaceSubClass"), "00\n")
		mkfile(t, filepath.Join(dir, d.busid+":1.0", "bInterfaceProtocol"), "00\n")
		os.Symlink(k.sys("bus/usb/drivers/usb"), filepath.Join(dir, "driver"))
	}
	if withVHCI {
		os.MkdirAll(k.sys("devices/platform/vhci_hcd.0"), 0o755)
		k.setVhci(-1)
	}
	hookMu.Lock()
	old := hk
	hk = hooks{sys: system{root: k.root, write: k.write}, isRoot: func() bool { return true }, supported: true}
	hookMu.Unlock()
	t.Cleanup(func() { hookMu.Lock(); hk = old; hookMu.Unlock() })
	t.Setenv("REMOHOP_CONFIG_DIR", t.TempDir())
	return k
}

func (k *fakeKernel) setVhci(used int) {
	var b strings.Builder
	b.WriteString("hub port sta spd dev      sockfd local_busid\n")
	for _, hub := range []string{"hs", "ss"} {
		for i := 0; i < 8; i++ {
			port := i
			if hub == "ss" {
				port += 8
			}
			if port == used {
				fmt.Fprintf(&b, "%s  %04d 006 003 00010002 000005 3-1\n", hub, port)
			} else {
				fmt.Fprintf(&b, "%s  %04d 004 000 00000000 000000 0-0\n", hub, port)
			}
		}
	}
	mkfile(k.t, k.sys("devices/platform/vhci_hcd.0/status"), b.String())
}

func (k *fakeKernel) log() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]string(nil), k.writes...)
}

func (k *fakeKernel) dupSock(val string, idx int) *os.File {
	fd, err := strconv.Atoi(strings.Fields(val)[idx])
	if err != nil {
		k.t.Errorf("bad fd in %q", val)
		return nil
	}
	n, err := syscall.Dup(fd) // like the kernel taking its own reference
	if err != nil {
		k.t.Errorf("fd %d from %q is not open: %v", fd, val, err)
		return nil
	}
	var st syscall.Stat_t
	if syscall.Fstat(n, &st) != nil || st.Mode&syscall.S_IFMT != syscall.S_IFSOCK {
		k.t.Errorf("fd %d is not a socket", fd)
	}
	return os.NewFile(uintptr(n), "ksock")
}

func (k *fakeKernel) write(path, val string) error {
	rel := strings.TrimPrefix(path, k.root+"/sys/")
	k.mu.Lock()
	k.writes = append(k.writes, rel+"="+strings.TrimSpace(val))
	k.mu.Unlock()
	base := filepath.Base(path)
	dir := filepath.Base(filepath.Dir(path))
	busid := strings.TrimSpace(val)
	link := func(b string) string { return k.sys("bus/usb/devices", b, "driver") }
	switch {
	case dir == "usbip-host" && base == "bind":
		if k.failBind {
			return syscall.EBUSY
		}
		os.Remove(link(busid))
		os.Symlink(k.sys("bus/usb/drivers/usbip-host"), link(busid))
		mkfile(k.t, k.sys("bus/usb/devices", busid, "usbip_status"), "1\n")
	case dir == "usb" && base == "unbind":
		os.Remove(link(busid))
	case dir == "usbip-host" && base == "unbind":
		os.Remove(link(busid))
	case base == "drivers_probe", dir == "usbip-host" && base == "rebind":
		os.Remove(link(busid))
		os.Symlink(k.sys("bus/usb/drivers/usb"), link(busid))
		os.Remove(k.sys("bus/usb/devices", busid, "usbip_status"))
	case base == "usbip_sockfd":
		f := k.dupSock(val, 0)
		st := filepath.Join(filepath.Dir(path), "usbip_status")
		// SDEV_ST_USED, as usbip-host reports a device with a client.
		mkfile(k.t, st, "2\n")
		go func() { // host kernel: echo inverted until the socket closes
			defer f.Close()
			buf := make([]byte, 4096)
			for {
				n, err := f.Read(buf)
				for i := range buf[:n] {
					buf[i] = ^buf[i]
				}
				if n > 0 {
					f.Write(buf[:n])
				}
				if err != nil {
					mkfile(k.t, st, "1\n")
					return
				}
			}
		}()
	case dir == "vhci_hcd.0" && base == "attach":
		f := k.dupSock(val, 1)
		port, _ := strconv.Atoi(strings.Fields(val)[0])
		k.setVhci(port)
		go func() { // client kernel: send, expect inverted echo
			defer f.Close()
			want := make([]byte, k.payload)
			rand.Read(want)
			go f.Write(want)
			got := make([]byte, len(want))
			_, err := io.ReadFull(f, got)
			if err == nil {
				for i := range got {
					if got[i] != ^want[i] {
						err = fmt.Errorf("byte %d corrupted", i)
						break
					}
				}
			}
			k.clientC <- err
			io.Copy(io.Discard, f) // stay attached until the other side closes
		}()
	case dir == "vhci_hcd.0" && base == "detach":
		k.setVhci(-1)
	}
	return nil
}

func allowDev(t *testing.T, busid string) {
	t.Helper()
	h := getHooks()
	d, _, err := h.sys.device(busid)
	if err != nil {
		t.Fatal(err)
	}
	if err := Allow(AllowEntry{busid, fmt.Sprintf("%04x", d.VendorID), fmt.Sprintf("%04x", d.ProductID)}); err != nil {
		t.Fatal(err)
	}
}

func wantClientData(t *testing.T, k *fakeKernel) {
	t.Helper()
	select {
	case err := <-k.clientC:
		if err != nil {
			t.Fatalf("client kernel exchange: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("client kernel exchange timed out")
	}
}

func waitFor(t *testing.T, what string, f func() bool) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); !f(); {
		if time.Now().After(end) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

var widget = devSpec{busid: "1-2", vid: "1234", pid: "abcd", class: "ff"}

func TestAttachEndToEndAndRestore(t *testing.T) {
	k := newKernel(t, true, true, widget)
	k.payload = 3 << 20
	allowDev(t, "1-2")
	client, _ := peertest.Pair(t)
	a, err := Attach(context.Background(), client, "1-2")
	if err != nil {
		t.Fatal(err)
	}
	if a.Device.VendorID != 0x1234 || a.Device.BusID != "1-2" || a.Port != 0 {
		t.Fatalf("attached %+v", a)
	}
	wantClientData(t, k)
	// While attached: bound to usbip-host, and the kernel got the right strings.
	if d := getHooks().sys.driverOf("1-2"); d != "usbip-host" {
		t.Fatalf("driver %q while exported", d)
	}
	var sawAttach bool
	for _, w := range k.log() {
		if strings.HasPrefix(w, "devices/platform/vhci_hcd.0/attach=0 ") && strings.HasSuffix(w, " 65538 3") { // port fd devid=1<<16|2 speed=high
			sawAttach = true
		}
	}
	if !sawAttach {
		t.Fatalf("attach write missing/wrong: %v", k.log())
	}
	att, _ := ListAttached()
	if len(att) != 1 || att[0].Port != 0 {
		t.Fatalf("attached list %+v", att)
	}
	// A second client cannot take it.
	if _, err := Attach(context.Background(), client, "1-2"); err == nil || !strings.Contains(err.Error(), "already exported") {
		t.Fatalf("second attach: %v", err)
	}

	a.Close()
	waitFor(t, "driver restored", func() bool { return getHooks().sys.driverOf("1-2") == "usb" })
	log := strings.Join(k.log(), "\n")
	for _, want := range []string{"bus/usb/drivers/usbip-host/match_busid=add 1-2", "bus/usb/drivers/usb/unbind=1-2",
		"bus/usb/drivers/usbip-host/bind=1-2", "bus/usb/drivers/usbip-host/unbind=1-2",
		"bus/usb/drivers/usbip-host/match_busid=del 1-2", "bus/usb/drivers/usbip-host/rebind=1-2", "devices/platform/vhci_hcd.0/detach=0"} {
		if !strings.Contains(log, want) {
			t.Errorf("missing write %q in\n%s", want, log)
		}
	}
	if strings.Index(log, "usbip-host/unbind") > strings.Index(log, "match_busid=del") {
		t.Error("del match_busid before unbind (kernel would refuse)")
	}
	if strings.Index(log, "usbip-host/rebind") < strings.Index(log, "match_busid=del") {
		t.Error("rebind before del match_busid (the order of `usbip unbind`)")
	}
	if strings.Contains(log, "drivers_probe") {
		t.Error("re-probed although usbip-host's rebind worked")
	}
}

func TestSessionLossRestoresDriver(t *testing.T) {
	k := newKernel(t, true, true, widget)
	allowDev(t, "1-2")
	client, host := peertest.Pair(t)
	a, err := Attach(context.Background(), client, "1-2")
	if err != nil {
		t.Fatal(err)
	}
	wantClientData(t, k)
	host.Close()
	select {
	case <-a.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("attachment survived session loss")
	}
	waitFor(t, "host restored the original driver", func() bool { return getHooks().sys.driverOf("1-2") == "usb" })
	waitFor(t, "claim released", func() bool { return claim("1-2") && func() bool { unclaim("1-2"); return true }() })
}

func TestRefusals(t *testing.T) {
	kb := newKernel(t, true, true, widget, devSpec{busid: "1-3", vid: "046d", pid: "c31c", class: "00", ifClass: "03"},
		devSpec{busid: "1-4", vid: "05e3", pid: "0608", class: "09", ifClass: "09"})
	_ = kb
	allowDev(t, "1-2")
	client, _ := peertest.Pair(t)
	ctx := context.Background()
	attach := func(id string) error { _, err := Attach(ctx, client, id); return err }

	expect := func(id string, sub string) {
		t.Helper()
		err := attach(id)
		var he *HostError
		if err == nil || !strings.Contains(err.Error(), sub) {
			t.Errorf("attach %s: %v, want %q", id, err, sub)
		}
		_ = he
	}
	expect("1-3", "not allowed")             // a keyboard that nobody allowed
	expect("1-4", "hubs cannot be exported") // hubs: never
	expect("1-9", "no USB device")
	// nothing was unbound for refused devices
	for _, w := range kb.log() {
		if strings.Contains(w, "bind=1-3") || strings.Contains(w, "bind=1-4") || strings.Contains(w, "=add 1-3") {
			t.Fatalf("touched a refused device: %v", kb.log())
		}
	}
	// Allowed busid but a different device plugged in later: VID:PID mismatch.
	mkfile(t, kb.sys("bus/usb/devices/1-2/idVendor"), "dead\n")
	expect("1-2", "not allowed")
	// Hostile busids never reach sysfs.
	if _, err := Attach(ctx, client, "../../etc"); err == nil {
		t.Fatal("bad busid accepted by client")
	}
	// A hostile client: raw import with a traversal busid.
	s, err := client.Open(ctx, Service, request{Op: "usbip"})
	if err != nil {
		t.Fatal(err)
	}
	raw := append(opHeader(OpReqImport, 0), make([]byte, 32)...)
	copy(raw[8:], "../../../etc")
	s.Write(raw)
	code, st, err := ReadOpHeader(s)
	if err != nil || code != OpRepImport || st == StOK {
		t.Fatalf("hostile import: %d %d %v", code, st, err)
	}
}

func TestAllowlistRequiredEvenForOrdinaryDevice(t *testing.T) {
	newKernel(t, true, true, widget)
	client, _ := peertest.Pair(t)
	_, err := Attach(context.Background(), client, "1-2")
	if err == nil || !strings.Contains(err.Error(), "remohop usb allow 1-2") {
		t.Fatalf("%v", err)
	}
}

func TestMissingModulesNamedPrecisely(t *testing.T) {
	// Host lacks usbip-host.
	newKernel(t, false, true, widget)
	allowDev(t, "1-2")
	client, _ := peertest.Pair(t)
	_, err := Attach(context.Background(), client, "1-2")
	var he *HostError
	if err == nil || !strings.Contains(err.Error(), "usbip-host") || !strings.Contains(err.Error(), "modprobe usbip-host") || !strings.Contains(err.Error(), "host") {
		t.Fatalf("host module error: %v", err)
	}
	_ = he
	// Client lacks vhci-hcd: refused before the host is bothered.
	k := newKernel(t, true, false, widget)
	allowDev(t, "1-2")
	_, err = Attach(context.Background(), client, "1-2")
	if err == nil || !strings.Contains(err.Error(), "vhci-hcd") || !strings.Contains(err.Error(), "modprobe vhci-hcd") {
		t.Fatalf("client module error: %v", err)
	}
	if getHooks().sys.driverOf("1-2") != "usb" || len(k.log()) != 0 {
		t.Fatalf("host was touched: %v", k.log())
	}
	// Not root.
	hookMu.Lock()
	hk.isRoot = func() bool { return false }
	hookMu.Unlock()
	if _, err = Attach(context.Background(), client, "1-2"); err == nil || !strings.Contains(err.Error(), "root") {
		t.Fatalf("root error: %v", err)
	}
}

func TestBindFailureRollsBack(t *testing.T) {
	k := newKernel(t, true, true, widget)
	allowDev(t, "1-2")
	k.failBind = true
	client, _ := peertest.Pair(t)
	if _, err := Attach(context.Background(), client, "1-2"); err == nil {
		t.Fatal("attach succeeded despite bind failure")
	}
	waitFor(t, "driver restored after failed bind", func() bool { return getHooks().sys.driverOf("1-2") == "usb" })
}

func TestListAndDevlist(t *testing.T) {
	newKernel(t, false, false, widget, devSpec{busid: "2-1.4", vid: "0001", pid: "0002", class: "00", ifClass: "08"})
	allowDev(t, "1-2")
	client, _ := peertest.Pair(t)
	devs, note, err := List(context.Background(), client)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(note, "usbip-host") {
		t.Fatalf("note %q", note)
	}
	if len(devs) != 2 || devs[0].BusID != "1-2" || !devs[0].Allowed || devs[0].VID != "1234" || devs[0].Class != "vendor" ||
		devs[1].BusID != "2-1.4" || devs[1].Allowed || devs[1].Class != "per-interface" || devs[1].Interfaces[0] != "mass-storage" || devs[0].Driver != "usb" {
		t.Fatalf("devs %+v", devs)
	}
	es, err := DevList(context.Background(), client)
	if err != nil || len(es) != 1 || es[0].Device.BusID != "1-2" || len(es[0].Interfaces) != 1 {
		t.Fatalf("devlist %+v %v", es, err)
	}
}

// ---- CLI ----

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func newEnv(client *peer.Conn) (*cli.Env, *syncBuf, *syncBuf) {
	var out, errb syncBuf
	return &cli.Env{Target: "test", Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb,
		DialFunc: func(context.Context, *cli.Env) (*peer.Conn, error) { return client, nil }}, &out, &errb
}

func TestCLIAllowFlow(t *testing.T) {
	newKernel(t, true, true, widget, devSpec{busid: "1-3", vid: "046d", pid: "c31c", class: "00", ifClass: "03"},
		devSpec{busid: "1-4", vid: "05e3", pid: "0608", class: "09", ifClass: "09"})
	cmd := cli.Lookup("usb")
	run := func(args ...string) (int, string, string) {
		env, out, errb := newEnv(nil)
		c := cmd.Run(context.Background(), env, args)
		return c, out.String(), errb.String()
	}
	if c, _, e := run("allow", "1-3"); c != cli.ExitError || !strings.Contains(e, "--force") {
		t.Fatalf("HID without force: %d %s", c, e)
	}
	if c, _, e := run("allow", "--force", "1-4"); c != cli.ExitError || !strings.Contains(e, "hub") {
		t.Fatalf("hub: %d %s", c, e)
	}
	if c, _, _ := run("allow", "1-9"); c != cli.ExitError {
		t.Fatal("allowed a missing device")
	}
	if c, _, _ := run("allow", "../x"); c != cli.ExitUsage {
		t.Fatal("bad busid")
	}
	if c, out, e := run("allow", "1-2"); c != cli.ExitOK || !strings.Contains(out, "1234:abcd") {
		t.Fatalf("%d %s %s", c, out, e)
	}
	if c, _, _ := run("allow", "--force", "1-3"); c != cli.ExitOK {
		t.Fatal("force HID")
	}
	c, out, _ := run("allowed", "--json")
	var l []AllowEntry
	if c != cli.ExitOK || json.Unmarshal([]byte(out), &l) != nil || len(l) != 2 || l[0].BusID != "1-2" {
		t.Fatalf("allowed: %d %q", c, out)
	}
	if fi, err := os.Stat(filepath.Join(os.Getenv("REMOHOP_CONFIG_DIR"), allowFile)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("allow file mode: %v %v", fi, err)
	}
	if c, _, _ := run("disallow", "1-3"); c != cli.ExitOK {
		t.Fatal("disallow")
	}
	if c, _, _ := run("disallow", "1-3"); c != cli.ExitError {
		t.Fatal("disallow twice")
	}
	if IsAllowed("1-3", "046d", "c31c") || !IsAllowed("1-2", "1234", "abcd") {
		t.Fatal("allow state")
	}
}

func TestCLIRemoteListAndUsage(t *testing.T) {
	newKernel(t, true, true, widget)
	allowDev(t, "1-2")
	client, _ := peertest.Pair(t)
	cmd := cli.Lookup("usb")
	env, out, errb := newEnv(client)
	if c := cmd.RemoteRun(context.Background(), env, []string{"list"}); c != cli.ExitOK {
		t.Fatalf("%d %s", c, errb.String())
	}
	if strings.Contains(out.String(), "\x1b") || !strings.Contains(out.String(), "1234:abcd") || !strings.Contains(out.String(), "allowed") {
		t.Fatalf("table %q", out.String())
	}
	env, out, _ = newEnv(client)
	cmd.RemoteRun(context.Background(), env, []string{"list", "--json"})
	var devs []DeviceInfo
	if json.Unmarshal([]byte(out.String()), &devs) != nil || len(devs) != 1 {
		t.Fatalf("json %q", out.String())
	}
	for _, args := range [][]string{nil, {"bogus"}, {"attach"}, {"attach", "../x"}, {"allow", "1-2"}, {"list", "x"}} {
		env, _, _ := newEnv(client)
		if c := cmd.RemoteRun(context.Background(), env, args); c != cli.ExitUsage {
			t.Errorf("remote %v: %d", args, c)
		}
	}
	env, _, _ = newEnv(client)
	if c := cmd.Run(context.Background(), env, []string{"junk"}); c != cli.ExitUsage {
		t.Errorf("local junk: %d", c)
	}
	if c := cmd.Run(context.Background(), env, []string{"detach"}); c != cli.ExitUsage {
		t.Errorf("detach no arg: %d", c)
	}
	if c := cmd.Run(context.Background(), env, []string{"detach", "7"}); c != cli.ExitError {
		t.Errorf("detach nothing: %d", c)
	}
}

func TestCLIAttachForegroundAndDetach(t *testing.T) {
	k := newKernel(t, true, true, widget)
	allowDev(t, "1-2")
	client, _ := peertest.Pair(t)
	env, out, errb := newEnv(client)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 1)
	go func() { done <- cli.Lookup("usb").RemoteRun(ctx, env, []string{"attach", "1-2", "--json"}) }()
	waitFor(t, "attach json", func() bool { return strings.Contains(out.String(), "\n") })
	var info struct {
		Busid, Vid, Pid string
		Port            int
	}
	if err := json.Unmarshal([]byte(out.String()), &info); err != nil || info.Busid != "1-2" || info.Vid != "1234" {
		t.Fatalf("%q %v", out.String(), err)
	}
	wantClientData(t, k)

	lenv, lout, _ := newEnv(client)
	if c := cli.Lookup("usb").Run(context.Background(), lenv, []string{"--json"}); c != cli.ExitOK {
		t.Fatal(c)
	}
	var at []Attachment
	if json.Unmarshal([]byte(lout.String()), &at) != nil || len(at) != 1 || at[0].BusID != "1-2" || at[0].Target != "test" {
		t.Fatalf("attachments %q", lout.String())
	}
	cancel()
	select {
	case c := <-done:
		if c != cli.ExitOK {
			t.Fatalf("exit %d %s", c, errb.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no exit on cancel")
	}
	waitFor(t, "restored", func() bool { return getHooks().sys.driverOf("1-2") == "usb" })
}

func TestReleaseRecoversStuckDevice(t *testing.T) {
	k := newKernel(t, true, true, widget)
	k.write(k.sys("bus/usb/drivers/usbip-host/bind"), "1-2") // as if left by a crash
	if len(ExportedBusIDs()) != 1 {
		t.Fatal("not stuck")
	}
	if err := Release("1-2"); err != nil {
		t.Fatal(err)
	}
	if getHooks().sys.driverOf("1-2") != "usb" {
		t.Fatal("not released")
	}
	if Release("1-2") == nil {
		t.Fatal("released a device that was not exported")
	}
}
