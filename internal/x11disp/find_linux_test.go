package x11disp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jezek/xgb/xproto"
)

func ctxT(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// xvfb is a private X server. stop terminates it (also run at cleanup).
type xvfb struct {
	n    int
	sock string
	stop func()
}

func startXvfb(t *testing.T, extra ...string) *xvfb {
	t.Helper()
	bin, err := exec.LookPath("Xvfb")
	if err != nil {
		t.Skip("Xvfb is not installed")
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	args := append([]string{"-displayfd", "3", "-screen", "0", "320x200x24", "-nolisten", "tcp", "-noreset"}, extra...)
	cmd := exec.Command(bin, args...)
	cmd.ExtraFiles = []*os.File{w}
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	w.Close()
	exited := make(chan struct{})
	go func() { cmd.Wait(); close(exited) }()
	stop := func() {
		cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
			cmd.Process.Kill()
			<-exited
		}
	}
	t.Cleanup(func() { stop(); r.Close() })
	line := make(chan string, 1)
	go func() { s, _ := bufio.NewReader(r).ReadString('\n'); line <- s }()
	select {
	case s := <-line:
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil {
			t.Skipf("Xvfb did not report a display (%q)", s)
		}
		return &xvfb{n: n, sock: fmt.Sprintf("/tmp/.X11-unix/X%d", n), stop: stop}
	case <-exited:
		t.Skip("Xvfb exited immediately")
	case <-time.After(15 * time.Second):
		t.Fatal("Xvfb start timed out")
	}
	return nil
}

// sockDir builds a private socket directory: stale sockets (listeners that
// were closed without unlinking, so connecting is refused) and symlinks that
// stand in for live servers under chosen display numbers.
type sockDir struct {
	t   *testing.T
	dir string
}

func newSockDir(t *testing.T) *sockDir { return &sockDir{t, t.TempDir()} }

func (s *sockDir) stale(n int) {
	s.t.Helper()
	p := filepath.Join(s.dir, "X"+strconv.Itoa(n))
	ln, err := net.Listen("unix", p)
	if err != nil {
		s.t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	if _, err := os.Stat(p); err != nil {
		s.t.Fatalf("stale socket file vanished: %v", err)
	}
}

func (s *sockDir) live(n int, x *xvfb) {
	s.t.Helper()
	if err := os.Symlink(x.sock, filepath.Join(s.dir, "X"+strconv.Itoa(n))); err != nil {
		s.t.Fatal(err)
	}
}

// env is an environment with no real files, variables or sockets beyond
// what a test adds.
func (s *sockDir) env(vars map[string]string, files map[string][]byte) *Env {
	return &Env{
		Getenv: func(k string) string { return vars[k] },
		Glob: func(p string) []string {
			var out []string
			for f := range files {
				if ok, _ := filepath.Match(p, f); ok {
					out = append(out, f)
				}
			}
			if strings.HasPrefix(p, s.dir) { // the real socket directory
				m, _ := filepath.Glob(p)
				out = append(out, m...)
			}
			return out
		},
		ReadFile: func(n string) ([]byte, error) {
			if b, ok := files[n]; ok {
				return b, nil
			}
			return nil, os.ErrNotExist
		},
		UID: 4242, Home: "/home/nobody", Hostname: "testhost", SocketDir: s.dir,
	}
}

func TestNothingFoundIsDescriptive(t *testing.T) {
	s := newSockDir(t)
	_, err := NewFinder(s.env(nil, nil)).Find(ctxT(t))
	var fe *Error
	if !errors.As(err, &fe) {
		t.Fatalf("err %v", err)
	}
	for _, want := range []string{"DISPLAY is not set", "no X11 sockets in " + s.dir} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q lacks %q", err, want)
		}
	}
}

func TestOnlyStaleSocketsReported(t *testing.T) {
	s := newSockDir(t)
	s.stale(0)
	s.stale(1)
	_, err := NewFinder(s.env(nil, nil)).Find(ctxT(t))
	if err == nil || !strings.Contains(err.Error(), "X display :0") || !strings.Contains(err.Error(), "X display :1") ||
		!strings.Contains(err.Error(), "refused") {
		t.Fatalf("err %v", err)
	}
}

func TestStaleSocketSkippedAndExplicitDisplay(t *testing.T) {
	x := startXvfb(t, "-ac")
	s := newSockDir(t)
	s.stale(0)
	s.stale(1)
	s.live(9, x)
	s.live(12, x)

	// DISPLAY unset: the stale lower-numbered sockets are skipped.
	d, err := NewFinder(s.env(nil, nil)).Find(ctxT(t))
	if err != nil {
		t.Fatal(err)
	}
	if d.Name != ":9" || d.XAuthority != "" {
		t.Fatalf("got %+v", d)
	}
	if got := strings.Join(d.Env(), " "); got != "DISPLAY=:9" {
		t.Fatalf("env %q", got)
	}
	c, err := d.Open(ctxT(t))
	if err != nil {
		t.Fatal(err)
	}
	if xproto.Setup(c).DefaultScreen(c).WidthInPixels != 320 {
		t.Fatal("wrong screen")
	}
	c.Close()

	// An explicit DISPLAY wins over lower-numbered live sockets.
	d, err = NewFinder(s.env(map[string]string{"DISPLAY": ":12"}, nil)).Find(ctxT(t))
	if err != nil || d.Name != ":12" {
		t.Fatalf("explicit: %+v %v", d, err)
	}
	// An explicit DISPLAY that does not answer falls back to probing.
	d, err = NewFinder(s.env(map[string]string{"DISPLAY": ":1"}, nil)).Find(ctxT(t))
	if err != nil || d.Name != ":9" {
		t.Fatalf("explicit but stale: %+v %v", d, err)
	}
}

func cookieFile(t *testing.T, family uint16, addr, display string, cookie []byte) []byte {
	t.Helper()
	return marshalXauthority([]xauthEntry{
		{family: 256, addr: "other-host", display: "", name: "MIT-MAGIC-COOKIE-1", data: bytes.Repeat([]byte{9}, 16)},
		{family: family, addr: addr, display: display, name: "MIT-MAGIC-COOKIE-1", data: cookie},
	})
}

func TestCookieFromEachLocation(t *testing.T) {
	cookie := bytes.Repeat([]byte{0x5a}, 16)
	authFile := filepath.Join(t.TempDir(), "server.auth")
	if err := os.WriteFile(authFile, marshalXauthority([]xauthEntry{{family: 65535, name: "MIT-MAGIC-COOKIE-1", data: cookie}}), 0o600); err != nil {
		t.Fatal(err)
	}
	x := startXvfb(t, "-auth", authFile)
	t.Setenv("REMOHOP_CONFIG_DIR", t.TempDir())
	s := newSockDir(t)
	s.live(5, x)

	good := func(disp string) []byte { return cookieFile(t, 256, "testhost", disp, cookie) }
	locations := []struct {
		name  string
		vars  map[string]string
		path  string
		match bool // glob-provided
	}{
		{"XAUTHORITY", map[string]string{"XAUTHORITY": "/custom/place"}, "/custom/place", false},
		{"home", nil, "/home/nobody/.Xauthority", false},
		{"gdm", nil, "/run/user/4242/gdm/Xauthority", false},
		{"mutter", nil, "/run/user/4242/.mutter-Xwaylandauth.ABC", true},
		{"xauth_", nil, "/run/user/4242/xauth_xyz", true},
		{"sddm", nil, "/run/sddm/xauth_q1", true},
	}
	for _, loc := range locations {
		t.Run(loc.name, func(t *testing.T) {
			env := s.env(loc.vars, map[string][]byte{loc.path: good("5")})
			d, err := NewFinder(env).Find(ctxT(t))
			if err != nil {
				t.Fatal(err)
			}
			if d.Name != ":5" || d.XAuthority != loc.path || d.XAuthorityTemp {
				t.Fatalf("got %+v", d)
			}
			c, err := d.Open(ctxT(t))
			if err != nil {
				t.Fatal(err)
			}
			c.Close()
		})
	}

	t.Run("no cookie anywhere", func(t *testing.T) {
		if _, err := NewFinder(s.env(nil, nil)).Find(ctxT(t)); err == nil || !strings.Contains(err.Error(), "X display :5") {
			t.Fatalf("err %v", err)
		}
	})
	t.Run("wrong cookie", func(t *testing.T) {
		bad := cookieFile(t, 256, "testhost", "5", bytes.Repeat([]byte{1}, 16))
		if _, err := NewFinder(s.env(map[string]string{"XAUTHORITY": "/x"}, map[string][]byte{"/x": bad})).Find(ctxT(t)); err == nil {
			t.Fatal("a wrong cookie must not connect")
		}
	})
	t.Run("cookie for another hostname gets a temp file", func(t *testing.T) {
		src := cookieFile(t, 256, "container-abc", "5", cookie)
		d, err := NewFinder(s.env(map[string]string{"XAUTHORITY": "/x"}, map[string][]byte{"/x": src})).Find(ctxT(t))
		if err != nil {
			t.Fatal(err)
		}
		if !d.XAuthorityTemp || d.XAuthority == "/x" {
			t.Fatalf("got %+v", d)
		}
		st, err := os.Stat(d.XAuthority)
		if err != nil || st.Mode().Perm() != 0o600 {
			t.Fatalf("temp file %v %v", st, err)
		}
		b, _ := os.ReadFile(d.XAuthority)
		es := parseXauthority(bytes.NewReader(b))
		if len(es) != 1 || es[0].addr != "testhost" || es[0].display != "5" || hex.EncodeToString(es[0].data) != hex.EncodeToString(cookie) {
			t.Fatalf("temp entries %+v", es)
		}
		// The exported environment really authenticates a standard client.
		if out, err := exec.Command("xauth", "-f", d.XAuthority, "list").CombinedOutput(); err == nil && !strings.Contains(string(out), "testhost") {
			t.Fatalf("xauth list: %s", out)
		}
	})
}

func TestCacheAndReprobe(t *testing.T) {
	a := startXvfb(t, "-ac")
	b := startXvfb(t, "-ac")
	s := newSockDir(t)
	s.live(3, a)
	s.live(4, b)
	f := NewFinder(s.env(nil, nil))

	d1, err := f.Find(ctxT(t))
	if err != nil || d1.Name != ":3" {
		t.Fatalf("first: %+v %v", d1, err)
	}
	if d2, _ := f.Find(ctxT(t)); d2 != d1 {
		t.Fatal("fresh cache entry not reused")
	}

	// Past the TTL the cached display is verified; once it stops answering
	// the finder probes again and finds the other server.
	old := cacheTTL
	cacheTTL = 0
	defer func() { cacheTTL = old }()
	if d2, _ := f.Find(ctxT(t)); d2 != d1 {
		t.Fatal("healthy cached display should be kept")
	}
	a.stop()
	d3, err := f.Find(ctxT(t))
	if err != nil || d3.Name != ":4" {
		t.Fatalf("after the first server died: %+v %v", d3, err)
	}

	// A failing Open drops the cache even inside the TTL.
	cacheTTL = time.Hour
	b.stop()
	if _, err := d3.Open(ctxT(t)); err == nil {
		t.Fatal("Open on a dead server should fail")
	}
	if _, err := f.Find(ctxT(t)); err == nil {
		t.Fatal("Find should re-probe and fail now that no server is left")
	}
}

func TestEnvAndMerge(t *testing.T) {
	d := &Display{Name: ":7", XAuthority: "/a/b"}
	if got := strings.Join(d.Env(), " "); got != "DISPLAY=:7 XAUTHORITY=/a/b" {
		t.Fatalf("env %q", got)
	}
	got := d.Merge([]string{"PATH=/bin", "DISPLAY=:0", "XAUTHORITY=/old", "HOME=/h"})
	if strings.Join(got, " ") != "PATH=/bin HOME=/h DISPLAY=:7 XAUTHORITY=/a/b" {
		t.Fatalf("merge %v", got)
	}
	// With no cookie file the caller's XAUTHORITY is left alone.
	d = &Display{Name: ":7"}
	if got := strings.Join(d.Merge([]string{"XAUTHORITY=/old"}), " "); got != "XAUTHORITY=/old DISPLAY=:7" {
		t.Fatalf("merge %q", got)
	}
}

func TestParseDisplayAndCookies(t *testing.T) {
	for in, want := range map[string][3]any{
		":0": {"", 0, 0}, ":12.1": {"", 12, 1}, "unix:3": {"unix", 3, 0}, "host:2.0": {"host", 2, 0},
	} {
		h, n, s, err := parseDisplay(in)
		if err != nil || h != want[0] || n != want[1] || s != want[2] {
			t.Errorf("parseDisplay(%q) = %q %d %d %v", in, h, n, s, err)
		}
	}
	for _, bad := range []string{"", "0", ":", ":x", ":0.x", ":-1"} {
		if _, _, _, err := parseDisplay(bad); err == nil {
			t.Errorf("parseDisplay(%q) should fail", bad)
		}
	}
	ck := func(b byte) []byte { return bytes.Repeat([]byte{b}, 16) }
	entries := []xauthEntry{
		{family: 256, addr: "other", display: "0", name: "MIT-MAGIC-COOKIE-1", data: ck(1)},
		{family: 256, addr: "me", display: "0", name: "MIT-MAGIC-COOKIE-1", data: ck(2)},
		{family: 256, addr: "me", display: "7", name: "MIT-MAGIC-COOKIE-1", data: ck(3)},
		{family: 65535, addr: "", display: "", name: "MIT-MAGIC-COOKIE-1", data: ck(4)},
		{family: 256, addr: "me", display: "0", name: "XDM-AUTHORIZATION-1", data: ck(5)},
		{family: 256, addr: "me", display: "0", name: "MIT-MAGIC-COOKIE-1", data: []byte("short")},
	}
	got := cookiesFor(entries, 0, "me")
	want := []cookie{{hex.EncodeToString(ck(2)), true}, {hex.EncodeToString(ck(4)), true}, {hex.EncodeToString(ck(1)), false}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("cookies %v, want %v", got, want)
	}
	var b bytes.Buffer
	b.Write([]byte{1, 0, 0, 3, 'a', 'b'})
	if es := parseXauthority(&b); len(es) != 0 {
		t.Errorf("truncated parse = %v", es)
	}
	rt := parseXauthority(bytes.NewReader(marshalXauthority(entries)))
	if len(rt) != len(entries) || rt[1].addr != "me" {
		t.Errorf("round trip %v", rt)
	}
}
