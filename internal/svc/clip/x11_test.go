//go:build linux

package clip

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/x11disp"

	"github.com/readori/RemoHop/internal/x11disp/xvfbtest"
)

// startXvfb runs a private X server and returns its DISPLAY. The test is
// skipped when the tools are not installed.
//
// The display number is chosen explicitly from a high range and checked
// against every X socket on the machine (file and abstract): other programs
// sharing /tmp/.X11-unix may run X servers without lock files we can see,
// and letting Xvfb pick the lowest free number then collides with them, so
// DISPLAY=:0 would reach somebody else's server.
func startXvfb(t *testing.T, tools ...string) string {
	t.Helper()
	for _, tool := range append([]string{"Xvfb"}, tools...) {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not installed; skipping the real X11 clipboard test", tool)
		}
	}
	for attempt := 0; attempt < 10; attempt++ {
		n := 200 + rand.Intn(700)
		if xDisplayInUse(n) {
			continue
		}
		if d, ok := tryXvfb(t, n); ok {
			return d
		}
	}
	t.Skip("could not start a private Xvfb")
	return ""
}

// xDisplayInUse reports whether anything already claims display n.
func xDisplayInUse(n int) bool {
	sock := fmt.Sprintf("/tmp/.X11-unix/X%d", n)
	if _, err := os.Stat(sock); err == nil {
		return true
	}
	if _, err := os.Stat(fmt.Sprintf("/tmp/.X%d-lock", n)); err == nil {
		return true
	}
	b, _ := os.ReadFile("/proc/net/unix")
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) > 0 && strings.TrimPrefix(f[len(f)-1], "@") == sock {
			return true
		}
	}
	return false
}

func tryXvfb(t *testing.T, n int) (string, bool) {
	rd, wr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer rd.Close()
	display := fmt.Sprintf(":%d", n)
	cmd := exec.Command("Xvfb", display, "-displayfd", "3", "-screen", "0", "320x200x24", "-nolisten", "tcp")
	cmd.ExtraFiles = []*os.File{wr}
	if err := cmd.Start(); err != nil {
		wr.Close()
		t.Skipf("cannot start Xvfb: %v", err)
	}
	wr.Close()
	exited := make(chan struct{})
	go func() { cmd.Wait(); close(exited) }()
	stop := func() {
		// Selection owners (xclip/xsel) exit when their X server goes away.
		xvfbtest.Stop(cmd, display, exited)
	}
	line := make(chan string, 1)
	go func() {
		s, _ := bufio.NewReader(rd).ReadString('\n')
		line <- strings.TrimSpace(s)
	}()
	select {
	case got := <-line:
		if got != strconv.Itoa(n) {
			stop() // it died (display taken after all) or reported nonsense
			return "", false
		}
		t.Cleanup(stop)
		xvfbStops[display] = stop
		return display, true
	case <-time.After(15 * time.Second):
		stop()
		return "", false
	}
}

// xvfbStops ends a test's X server before the test does.
var xvfbStops = map[string]func(){}

func x11For(display, tool string) *x11Board {
	return &x11Board{tool: tool, h: helper{env: withEnv(map[string]string{"DISPLAY": display})}}
}

func TestX11Xclip(t *testing.T) {
	display := startXvfb(t, "xclip")
	b := x11For(display, "xclip")
	ctx := context.Background()

	if _, err := b.Read(ctx); !errors.Is(err, ErrEmpty) {
		t.Fatalf("fresh X clipboard: %v, want ErrEmpty", err)
	}

	// xclip keeps running to own the selection: Write must still return fast.
	for _, text := range []string{"héllo wörld 你好\nline two\n", strings.Repeat("0123456789", 100_000)} {
		start := time.Now()
		if err := b.Write(ctx, Text(text)); err != nil {
			t.Fatal(err)
		}
		if d := time.Since(start); d > 3*time.Second {
			t.Errorf("Write blocked for %s", d)
		}
		c, err := b.Read(ctx)
		if err != nil || c.Kind != KindText || c.String() != text {
			t.Fatalf("text round trip: kind=%v len=%d err=%v", c.Kind, len(c.Data), err)
		}
	}

	// Write returns only once the clipboard holds the content, so writes in
	// quick succession are read back in order (the forked owner used to take
	// over late under load, and an older write could even win).
	for i := 0; i < 40; i++ {
		want := "burst " + strings.Repeat("x", i)
		if err := b.Write(ctx, Text(want)); err != nil {
			t.Fatal(err)
		}
		if c, err := b.Read(ctx); err != nil || c.String() != want {
			t.Fatalf("burst write %d read back %q, %v", i, c.String(), err)
		}
	}

	img, _ := PNG(testPNG(t, 64, 48, true))
	if err := b.Write(ctx, img); err != nil {
		t.Fatal(err)
	}
	c, err := b.Read(ctx)
	if err != nil || c.Kind != KindImage || !bytes.Equal(c.Data, img.Data) {
		t.Fatalf("image round trip: kind=%v err=%v", c.Kind, err)
	}
	// Replacing an image with text takes the selection over.
	if err := b.Write(ctx, Text("text again")); err != nil {
		t.Fatal(err)
	}
	if c, err := b.Read(ctx); err != nil || c.String() != "text again" {
		t.Fatalf("after replacing: %v %v", c.Kind, err)
	}

	// Change detection: a different "application" (another board instance
	// talking to the same X server) copies something.
	wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ch := b.Watch(wctx)
	other := x11For(display, "xclip")
	if err := other.Write(ctx, Text("copied elsewhere")); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-ch:
		if got.String() != "copied elsewhere" {
			t.Fatalf("watch delivered %q", got.String())
		}
	case <-wctx.Done():
		t.Fatal("watch did not report the change")
	}

	// A dead display is reported as unavailable, so detection can skip it.
	dead := x11For(":99", "xclip")
	if _, err := dead.Read(ctx); !errors.Is(err, errUnavailable) {
		t.Errorf("dead display: %v, want errUnavailable", err)
	}
}

func TestX11Xsel(t *testing.T) {
	display := startXvfb(t, "xsel")
	b := x11For(display, "xsel")
	ctx := context.Background()
	if _, err := b.Read(ctx); !errors.Is(err, ErrEmpty) {
		t.Fatalf("fresh clipboard: %v", err)
	}
	if err := b.Write(ctx, Text("via xsel ü")); err != nil {
		t.Fatal(err)
	}
	if c, err := b.Read(ctx); err != nil || c.String() != "via xsel ü" {
		t.Fatalf("xsel round trip: %v %v", c.Kind, err)
	}
	img, _ := PNG(testPNG(t, 4, 4, false))
	if err := b.Write(ctx, img); err == nil {
		t.Error("xsel accepted an image")
	}
}

// The host daemon may run without DISPLAY: detection must find the X server
// the way a user would, from /tmp/.X11-unix.
func TestDetectFindsXWithoutDisplay(t *testing.T) {
	display := startXvfb(t, "xclip")
	// Other X servers may run on this machine (and be in use by someone
	// else's tests): let discovery see only ours, through a directory holding
	// a link to its socket.
	dir := t.TempDir()
	sock := "X" + strings.TrimPrefix(display, ":")
	if err := os.Symlink(filepath.Join("/tmp/.X11-unix", sock), filepath.Join(dir, sock)); err != nil {
		t.Skipf("cannot link the X socket: %v", err)
	}
	defer x11disp.SetSocketDir(dir)()
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	b := Detect(context.Background())
	if !strings.HasPrefix(b.Name(), "x11/") {
		t.Fatalf("Detect = %q, want an x11 backend", b.Name())
	}
	// And the sync path works against it end to end.
	if err := b.Write(context.Background(), Text("detected")); err != nil {
		t.Fatal(err)
	}
	if c, err := b.Read(context.Background()); err != nil || c.String() != "detected" {
		t.Fatalf("read back: %v", err)
	}
}

func TestDetectFallsBackToMemory(t *testing.T) {
	defer x11disp.SetSocketDir(t.TempDir())() // no X servers here, whatever else runs on the machine
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	if b := Detect(context.Background()); b.Name() != "memory" {
		t.Fatalf("Detect on a headless machine = %q", b.Name())
	}
}

// Two-way sync with a real X clipboard (polling backend) on the client side.
func TestSyncWithRealX11(t *testing.T) {
	display := startXvfb(t, "xclip")
	r := newRig(t)
	x := x11For(display, "xclip")
	r.local = nil
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- Sync(ctx, r.client, SyncOptions{Local: x, NoHistory: true, OnReady: func(SyncInfo) { close(ready) }})
	}()
	<-ready

	other := x11For(display, "xclip") // another application copying
	if err := other.Write(ctx, Text("from the desktop")); err != nil {
		t.Fatal(err)
	}
	eventually(t, "X -> host", func() bool { return boardText(r.remote) == "from the desktop" })

	img, _ := PNG(testPNG(t, 16, 16, true))
	r.remote.Write(ctx, img)
	eventually(t, "host -> X", func() bool {
		c, err := x.Read(ctx)
		return err == nil && c.Kind == KindImage && c.Hash() == img.Hash()
	})
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// The host daemon's clipboard follows the desktop session: when the X
// server it found goes away (a log out, a display manager that starts a new
// server or writes a new Xauthority per session) and another comes up, the
// next get or set finds the new one. It used to keep the first board for
// the life of the process, failing every call (a set after the 5 s
// confirmation timeout) until the host was restarted.
func TestHostBoardRedetects(t *testing.T) {
	a := startXvfb(t, "xclip")
	b := startXvfb(t, "xclip")
	dir := t.TempDir()
	link := func(display string) {
		sock := "X" + strings.TrimPrefix(display, ":")
		if err := os.Symlink(filepath.Join("/tmp/.X11-unix", sock), filepath.Join(dir, sock)); err != nil {
			t.Skipf("cannot link the X socket: %v", err)
		}
	}
	link(a)
	defer x11disp.SetSocketDir(dir)()
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	SetHostBoard(nil)
	t.Cleanup(func() { SetHostBoard(nil) })
	ctx := context.Background()
	hb := hostBoard(ctx)
	if err := hb.Write(ctx, Text("one")); err != nil || !strings.HasPrefix(hb.Name(), "x11/") {
		t.Fatalf("first write: %v (%s)", err, hb.Name())
	}
	// The session ends; another X server comes up.
	xvfbStops[a]()
	os.Remove(filepath.Join(dir, "X"+strings.TrimPrefix(a, ":")))
	link(b)
	start := time.Now()
	if err := hostBoard(ctx).Write(ctx, Text("two")); err != nil {
		t.Fatalf("write after the display changed: %v", err)
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("the write took %v", took)
	}
	if c, err := x11For(b, "xclip").Read(ctx); err != nil || c.String() != "two" {
		t.Fatalf("the new display's clipboard: %q %v (host board %s)", c.String(), err, hostBoard(ctx).Name())
	}
}
