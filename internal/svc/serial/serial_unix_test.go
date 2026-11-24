//go:build linux || darwin

package serial

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/peer/peertest"
)

// fakeUART stands a pty in for the host's serial device: dev is what the
// "UART" is wired to, Path what the host opens.
type fakeUART struct {
	Master *os.File
	slave  *os.File
	Path   string
}

func newUART(t *testing.T) *fakeUART {
	t.Helper()
	m, s, err := pty.Open()
	if err != nil {
		t.Skipf("no pty: %v", err)
	}
	// Raw so the test's own bytes are not mangled before the host opens it.
	ctl(s, func(fd int) error { return makeRaw(fd, DefaultConfig()) })
	u := &fakeUART{Master: m, slave: s, Path: s.Name()}
	t.Cleanup(func() { s.Close(); m.Close() })
	return u
}

// line reads the termios of the UART as the "remote tty" sees it.
func (u *fakeUART) line(t *testing.T) Config {
	t.Helper()
	var c Config
	if err := ctl(u.slave, func(fd int) (err error) { c, err = lineOf(fd); return }); err != nil {
		t.Fatal(err)
	}
	return c
}

// anyPath lets the host open whatever path the test names (the pty slave).
func anyPath(t *testing.T) {
	hookMu.Lock()
	old := hk.resolvePort
	hk.resolvePort = func(n string) (string, error) { return n, nil }
	hookMu.Unlock()
	t.Cleanup(func() { hookMu.Lock(); hk.resolvePort = old; hookMu.Unlock() })
}

func waitFor(t *testing.T, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func readN(t *testing.T, r io.Reader, n int) []byte {
	t.Helper()
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		t.Fatalf("read %d bytes: %v", n, err)
	}
	return buf
}

func allBytes() []byte {
	b := make([]byte, 0, 512)
	for i := 0; i < 256; i++ {
		b = append(b, byte(i))
	}
	return append(b, b...) // includes 0x00, 0x03, 0x04, 0x0a, 0x0d, 0x11, 0x13, 0xff
}

func TestRelayBothWays(t *testing.T) {
	anyPath(t)
	client, _ := peertest.Pair(t)
	u := newUART(t)
	r, err := Open(context.Background(), client, u.Path, Config{Baud: 57600, Data: 8, Parity: 'N', Stop: 1}, OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if got := u.line(t); got.Baud != 57600 || got.Data != 8 || got.Parity != 'N' || got.Stop != 1 {
		t.Fatalf("host line %+v", got)
	}
	want := allBytes()
	go r.Write(want)
	if got := readN(t, u.Master, len(want)); !bytes.Equal(got, want) {
		t.Fatal("client->device bytes differ")
	}
	go u.Master.Write(want)
	if got := readN(t, r, len(want)); !bytes.Equal(got, want) {
		t.Fatal("device->client bytes differ")
	}
}

func TestBulkIntegrity(t *testing.T) {
	anyPath(t)
	client, _ := peertest.Pair(t)
	u := newUART(t)
	r, err := Open(context.Background(), client, u.Path, DefaultConfig(), OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	const size = 3 << 20
	up, down := make([]byte, size), make([]byte, size)
	rand.Read(up)
	rand.Read(down)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); r.Write(up) }()
	go func() { defer wg.Done(); u.Master.Write(down) }()
	sums := make(chan [2][32]byte, 1)
	go func() {
		a := sha256.Sum256(readN(t, u.Master, size))
		b := sha256.Sum256(readN(t, r, size))
		sums <- [2][32]byte{a, b}
	}()
	select {
	case s := <-sums:
		if s[0] != sha256.Sum256(up) || s[1] != sha256.Sum256(down) {
			t.Fatal("bulk data corrupted")
		}
	case <-time.After(60 * time.Second):
		t.Fatal("bulk transfer timed out")
	}
	wg.Wait()
}

func TestConfigFollowsAndBreakModemDoNotCrash(t *testing.T) {
	anyPath(t)
	client, _ := peertest.Pair(t)
	u := newUART(t)
	notices := make(chan string, 8)
	r, err := Open(context.Background(), client, u.Path, DefaultConfig(), OpenOptions{OnNotice: func(s string) { notices <- s }})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	// A Linux pty forces 8N on its slave (macOS may keep 7E), so data bits
	// and parity are accepted either way; baud, stop bits and flow control
	// must match exactly.
	cfg := Config{Baud: 250000, Data: 7, Parity: 'E', Stop: 2, RTSCTS: true}
	if err := r.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "config applied", func() bool {
		g := u.line(t)
		return g.Baud == cfg.Baud && g.Stop == cfg.Stop && g.RTSCTS == cfg.RTSCTS &&
			((g.Data == 7 && g.Parity == 'E') || (g.Data == 8 && g.Parity == 'N'))
	})
	// A pty has no modem lines or break: the host must say so and carry on.
	tr := true
	r.SetModem(&tr, &tr)
	r.Break(10 * time.Millisecond)
	select {
	case n := <-notices:
		if !strings.Contains(n, "modem") && !strings.Contains(n, "break") {
			t.Fatalf("notice %q", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no notice for unsupported modem/break")
	}
	go r.Write([]byte("still alive"))
	if got := readN(t, u.Master, 11); string(got) != "still alive" {
		t.Fatalf("got %q", got)
	}
	if r.SetConfig(Config{Baud: 1}) == nil {
		t.Fatal("invalid config accepted locally")
	}
}

func TestExclusiveOpen(t *testing.T) {
	anyPath(t)
	client, _ := peertest.Pair(t)
	u := newUART(t)
	r1, err := Open(context.Background(), client, u.Path, DefaultConfig(), OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// Second session (same host process) is refused by the host's claim...
	if _, err := Open(context.Background(), client, u.Path, DefaultConfig(), OpenOptions{}); err == nil || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("second open: %v, want busy", err)
	}
	// ...and a program opening the device directly sees TIOCEXCL.
	if p, err := openDevice(u.Path, DefaultConfig()); err == nil || !strings.Contains(err.Error(), "busy") {
		if p != nil {
			p.Close()
		}
		if !exclusiveUnsupported() {
			t.Fatalf("direct open: %v, want busy", err)
		}
	}
	r1.Close()
	// After close the port is free again.
	waitFor(t, "port released", func() bool {
		r, err := Open(context.Background(), client, u.Path, DefaultConfig(), OpenOptions{})
		if err != nil {
			return false
		}
		r.Close()
		return true
	})
}

func TestOpenRefusals(t *testing.T) {
	client, _ := peertest.Pair(t) // default resolver: only listed ports
	for _, p := range []string{"/etc/passwd", "/dev/null", "../../etc/shadow", strings.Repeat("a", 600)} {
		if _, err := Open(context.Background(), client, p, DefaultConfig(), OpenOptions{}); err == nil {
			t.Errorf("port %q opened", p)
		}
	}
	anyPath(t)
	if _, err := Open(context.Background(), client, "/dev/null", DefaultConfig(), OpenOptions{}); err == nil || !strings.Contains(err.Error(), "terminal") {
		t.Errorf("/dev/null: %v", err)
	}
	// A hostile client skipping local validation: raw request, bad baud.
	s, err := client.Open(context.Background(), Service, request{Op: "open", Port: "x", Baud: 1 << 30, Data: 8, Parity: "N", Stop: 1})
	if err != nil {
		t.Fatal(err)
	}
	var rep reply
	if err := readJSON(s, &rep); err != nil || rep.OK {
		t.Fatalf("bad baud accepted: %+v %v", rep, err)
	}
	if _, err := client.Open(context.Background(), Service, request{Op: "bogus"}); err != nil {
		t.Fatal(err)
	}
}

func TestOversizeFrameEndsSession(t *testing.T) {
	anyPath(t)
	client, _ := peertest.Pair(t)
	u := newUART(t)
	s, err := client.Open(context.Background(), Service, request{Op: "open", Port: u.Path, Baud: 9600, Data: 8, Parity: "N", Stop: 1})
	if err != nil {
		t.Fatal(err)
	}
	var rep reply
	if err := readJSON(s, &rep); err != nil || !rep.OK {
		t.Fatalf("%+v %v", rep, err)
	}
	s.Write([]byte{0x7f, 0xff, 0xff, 0xff, frameData}) // announces 2 GiB
	s.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		if _, _, err := readFrame(s, nil); err != nil {
			break // stream ended (modem frames may precede)
		}
	}
	waitFor(t, "port released after bad frame", func() bool { return claim(u.Path) && func() bool { release(u.Path); return true }() })
}

func attach(t *testing.T, client *peer.Conn, u *fakeUART, cfg Config) (*Virtual, string) {
	t.Helper()
	local := filepath.Join(t.TempDir(), "sub", "vport")
	v, err := Attach(context.Background(), client, u.Path, cfg, AttachOptions{LocalPath: local})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { v.Close() })
	return v, local
}

func TestVirtualPortEndToEnd(t *testing.T) {
	anyPath(t)
	client, _ := peertest.Pair(t)
	u := newUART(t)
	v, local := attach(t, client, u, Config{Baud: 19200, Data: 8, Parity: 'N', Stop: 1})
	if st, err := os.Lstat(local); err != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("no symlink at %s: %v", local, err)
	}
	if got, _ := filepath.EvalSymlinks(local); got == "" {
		t.Fatal("dangling link")
	}
	// A "local program" opens the virtual port like a terminal program does.
	app, err := os.OpenFile(local, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	var l Config
	ctl(app, func(fd int) (err error) { l, err = lineOf(fd); return })
	if l.Baud != 19200 {
		t.Fatalf("virtual port baud %d, want 19200", l.Baud)
	}
	want := allBytes()
	go app.Write(want)
	if got := readN(t, u.Master, len(want)); !bytes.Equal(got, want) {
		t.Fatal("app->uart differ")
	}
	go u.Master.Write(want)
	if got := readN(t, app, len(want)); !bytes.Equal(got, want) {
		t.Fatal("uart->app differ")
	}

	// The app changes baud and framing; the remote tty follows. Standard,
	// non-standard, and parity/stop changes.
	for _, c := range []Config{
		{Baud: 9600, Data: 8, Parity: 'N', Stop: 1},
		{Baud: 250000, Data: 8, Parity: 'N', Stop: 1},
		{Baud: 1200, Data: 8, Parity: 'N', Stop: 2}, // pty: 8N only
		{Baud: 1200, Data: 8, Parity: 'N', Stop: 1, RTSCTS: true},
	} {
		if err := ctl(app, func(fd int) error { return applyLine(fd, c) }); err != nil {
			t.Fatalf("local set %+v: %v", c, err)
		}
		waitFor(t, "remote line "+c.String(), func() bool { return u.line(t) == c })
	}
	// Settings then data: the data must not overtake the settings.
	c := Config{Baud: 115200, Data: 8, Parity: 'N', Stop: 1}
	ctl(app, func(fd int) error { return applyLine(fd, c) })
	app.Write([]byte("x"))
	readN(t, u.Master, 1)
	if got := u.line(t); got != c {
		t.Fatalf("data overtook settings: remote %+v", got)
	}

	// Closing the app does not tear the port down.
	app.Close()
	time.Sleep(100 * time.Millisecond)
	select {
	case <-v.Done():
		t.Fatal("virtual port ended when the app closed it")
	default:
	}
}

func TestSessionLossClosesBothEndsAndRemovesLink(t *testing.T) {
	anyPath(t)
	client, host := peertest.Pair(t)
	u := newUART(t)
	v, local := attach(t, client, u, DefaultConfig())
	host.Close()
	select {
	case <-v.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("virtual port survived session loss")
	}
	if _, err := os.Lstat(local); !os.IsNotExist(err) {
		t.Fatalf("symlink still there: %v", err)
	}
	waitFor(t, "host released the claim", func() bool { return claim(u.Path) && func() bool { release(u.Path); return true }() })
	// The host side closed the tty: the exclusive flag is gone.
	p, err := openDevice(u.Path, DefaultConfig())
	if err != nil {
		t.Fatalf("reopen after loss: %v", err)
	}
	p.Close()
}

func TestRemotePortGoneEndsVirtual(t *testing.T) {
	anyPath(t)
	client, _ := peertest.Pair(t)
	u := newUART(t)
	v, local := attach(t, client, u, DefaultConfig())
	u.slave.Close()
	u.Master.Close() // the UART "unplugged"
	select {
	case <-v.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("virtual port survived the remote port vanishing")
	}
	if _, err := os.Lstat(local); !os.IsNotExist(err) {
		t.Fatal("symlink left behind")
	}
}

func TestAttachLinkCollisions(t *testing.T) {
	anyPath(t)
	client, _ := peertest.Pair(t)
	u := newUART(t)
	dir := t.TempDir()
	// A regular file is never replaced.
	f := filepath.Join(dir, "file")
	os.WriteFile(f, []byte("x"), 0o600)
	if _, err := Attach(context.Background(), client, u.Path, DefaultConfig(), AttachOptions{LocalPath: f}); err == nil {
		t.Fatal("overwrote a regular file")
	}
	// A live link is refused, a dangling one replaced.
	live := filepath.Join(dir, "live")
	os.Symlink("/dev/null", live)
	if _, err := Attach(context.Background(), client, u.Path, DefaultConfig(), AttachOptions{LocalPath: live}); err == nil {
		t.Fatal("replaced a live link")
	}
	stale := filepath.Join(dir, "stale")
	os.Symlink("/nonexistent/pts/9", stale)
	v, err := Attach(context.Background(), client, u.Path, DefaultConfig(), AttachOptions{LocalPath: stale})
	if err != nil {
		t.Fatalf("stale link: %v", err)
	}
	v.Close()
	if _, err := os.Lstat(f); err != nil {
		t.Fatal("file gone")
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
	return &cli.Env{
		Target: "test", Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb,
		DialFunc: func(context.Context, *cli.Env) (*peer.Conn, error) { return client, nil },
	}, &out, &errb
}

func isolateConfig(t *testing.T) {
	t.Setenv("REMOHOP_CONFIG_DIR", t.TempDir())
}

func TestCLIList(t *testing.T) {
	hookMu.Lock()
	old := hk.hostList
	hk.hostList = func() ([]PortInfo, error) {
		return []PortInfo{
			{Path: "/dev/ttyUSB0", Name: "ttyUSB0", VID: "1a86", PID: "7523", Driver: "ch341", Description: "evil\x1b]52;c;AAAA\x07 dev", ByID: []string{"/dev/serial/by-id/usb-x"}},
			{Path: "/dev/ttyS1", Name: "ttyS1"},
		}, nil
	}
	hookMu.Unlock()
	defer func() { hookMu.Lock(); hk.hostList = old; hookMu.Unlock() }()
	client, _ := peertest.Pair(t)

	env, out, errb := newEnv(client)
	if code := cli.Lookup("serial").RemoteRun(context.Background(), env, []string{"list"}); code != cli.ExitOK {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if strings.Contains(out.String(), "\x1b") || !strings.Contains(out.String(), "1a86:7523") || !strings.Contains(out.String(), "/dev/ttyS1") {
		t.Fatalf("table:\n%q", out.String())
	}
	env, out, _ = newEnv(client)
	if code := cli.Lookup("serial").RemoteRun(context.Background(), env, []string{"list", "--json"}); code != cli.ExitOK {
		t.Fatal(code)
	}
	var ports []PortInfo
	if err := json.Unmarshal([]byte(out.String()), &ports); err != nil || len(ports) != 2 || ports[0].VID != "1a86" {
		t.Fatalf("json %q: %v", out.String(), err)
	}
}

func TestCLIUsageErrors(t *testing.T) {
	isolateConfig(t)
	client, _ := peertest.Pair(t)
	for _, args := range [][]string{nil, {"a", "b", "c"}, {"p", "fast"}, {"p", "--format", "9X9"}, {"p", "--dtr", "maybe"}, {"p", "5"}, {"--nope"}} {
		env, _, _ := newEnv(client)
		if code := cli.Lookup("serial").RemoteRun(context.Background(), env, args); code != cli.ExitUsage {
			t.Errorf("args %v: exit %d, want %d", args, code, cli.ExitUsage)
		}
	}
	env, _, _ := newEnv(client)
	if code := cli.Lookup("serial").Run(context.Background(), env, []string{"ttyUSB0"}); code != cli.ExitUsage {
		t.Errorf("local with port: %d", code)
	}
	if code := cli.Lookup("unserial").Run(context.Background(), env, nil); code != cli.ExitUsage {
		t.Errorf("unserial no args: %d", code)
	}
	if code := cli.Lookup("unserial").Run(context.Background(), env, []string{"/no/such"}); code != cli.ExitError {
		t.Errorf("unserial unknown: %d", code)
	}
}

func TestCLIAttachForeground(t *testing.T) {
	isolateConfig(t)
	anyPath(t)
	client, _ := peertest.Pair(t)
	u := newUART(t)
	local := filepath.Join(t.TempDir(), "p")
	env, out, errb := newEnv(client)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 1)
	go func() {
		done <- cli.Lookup("serial").RemoteRun(ctx, env, []string{u.Path, "9600", "--local", local, "--format", "7E1", "--json"})
	}()
	waitFor(t, "json line", func() bool { return strings.Contains(out.String(), "\n") })
	var info struct {
		Local, Slave, Port, Format string
		Baud                       int
	}
	if err := json.Unmarshal([]byte(out.String()), &info); err != nil {
		t.Fatalf("%q: %v", out.String(), err)
	}
	if info.Local != local || info.Baud != 9600 || info.Format != "7E1" || info.Port != u.Path {
		t.Fatalf("info %+v", info)
	}
	if got := u.line(t); got.Baud != 9600 { // (7E1 itself cannot be held by a pty)
		t.Fatalf("remote line %+v", got)
	}
	app, err := os.OpenFile(local, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	app.Write([]byte("hello"))
	if got := readN(t, u.Master, 5); string(got) != "hello" {
		t.Fatalf("got %q", got)
	}
	app.Close()

	// `remohop serial` lists it (this process recorded itself via bg.Ready).
	lenv, lout, _ := newEnv(client)
	if code := cli.Lookup("serial").Run(context.Background(), lenv, []string{"--json"}); code != cli.ExitOK {
		t.Fatal(code)
	}
	var act []Active
	if err := json.Unmarshal([]byte(lout.String()), &act); err != nil || len(act) != 1 || act[0].Local != local || act[0].Port != u.Path || act[0].Target != "test" {
		t.Fatalf("active %q: %v", lout.String(), err)
	}

	cancel()
	select {
	case code := <-done:
		if code != cli.ExitOK {
			t.Fatalf("exit %d: %s", code, errb.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("did not exit on cancel")
	}
	if _, err := os.Lstat(local); !os.IsNotExist(err) {
		t.Fatal("link left after exit")
	}
}

func TestCLISessionLossExitsNonZero(t *testing.T) {
	isolateConfig(t)
	anyPath(t)
	client, host := peertest.Pair(t)
	u := newUART(t)
	local := filepath.Join(t.TempDir(), "p")
	env, out, errb := newEnv(client)
	done := make(chan int, 1)
	go func() {
		done <- cli.Lookup("serial").RemoteRun(context.Background(), env, []string{u.Path, "--local", local})
	}()
	waitFor(t, "ready", func() bool { return strings.Contains(out.String(), "virtual serial port") })
	host.Close()
	select {
	case code := <-done:
		if code != cli.ExitError || !strings.Contains(errb.String(), "serial:") {
			t.Fatalf("exit %d, stderr %q", code, errb.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no exit")
	}
}

func TestUnserialStopsOwner(t *testing.T) {
	isolateConfig(t)
	// Fake an owner: a sleeping process recorded as a serial port.
	cmd := newSleeper(t)
	dir, _ := configRunDir()
	local := filepath.Join(t.TempDir(), "link")
	os.Symlink("/dev/null", local)
	rec, _ := json.Marshal(map[string]any{"kind": "serial", "target": "123456789", "detail": local + " -> /dev/ttyUSB0 @ 115200 8N1", "pid": cmd.Process.Pid, "started": time.Now()})
	os.WriteFile(filepath.Join(dir, "1.json"), rec, 0o600)

	env, out, errb := newEnv(nil)
	act, _ := ListActive()
	if len(act) != 1 || act[0].Port != "/dev/ttyUSB0" || act[0].Config != "115200 8N1" {
		t.Fatalf("active %+v", act)
	}
	if code := cli.Lookup("unserial").Run(context.Background(), env, []string{local}); code != cli.ExitOK {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "closed") {
		t.Fatal(out.String())
	}
	if _, err := os.Lstat(local); !os.IsNotExist(err) {
		t.Fatal("link not removed")
	}
}
