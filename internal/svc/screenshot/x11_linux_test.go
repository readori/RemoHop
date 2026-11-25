//go:build linux

package screenshot

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/randr"
	"github.com/jezek/xgb/xproto"

	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/peer/peertest"
	"github.com/readori/RemoHop/internal/x11disp"

	"github.com/readori/RemoHop/internal/x11disp/xvfbtest"
)

// startXvfb runs Xvfb on a free display (chosen by the server through
// -displayfd) and returns the display number. The server is terminated when
// the test ends.
func startXvfb(t *testing.T, extra ...string) int {
	t.Helper()
	bin, err := exec.LookPath("Xvfb")
	if err != nil {
		t.Skip("Xvfb is not installed")
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	args := append([]string{"-displayfd", "3", "-screen", "0", "640x480x24", "-nolisten", "tcp", "-noreset"}, extra...)
	cmd := exec.Command(bin, args...)
	cmd.ExtraFiles = []*os.File{w}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	w.Close()
	exited := make(chan struct{})
	go func() { cmd.Wait(); close(exited) }()
	var display string // known once Xvfb reports it
	t.Cleanup(func() {
		xvfbtest.Stop(cmd, display, exited)
		r.Close()
	})

	line := make(chan string, 1)
	go func() { s, _ := bufio.NewReader(r).ReadString('\n'); line <- s }()
	select {
	case s := <-line:
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil {
			t.Skipf("Xvfb did not report a display (%q); cannot run X11 tests here", s)
		}
		display = ":" + strconv.Itoa(n)
		return n
	case <-exited:
		t.Skip("Xvfb exited immediately; cannot run X11 tests here")
	case <-time.After(15 * time.Second):
		t.Fatal("Xvfb start timed out")
	}
	return 0
}

type rectFill struct {
	x, y, w, h int
	c          color.RGBA
}

// paintRoot fills rectangles on the root window of display n.
func paintRoot(t *testing.T, display string, fills []rectFill) {
	t.Helper()
	c, err := xgb.NewConnDisplay(display)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	root := xproto.Setup(c).DefaultScreen(c).Root
	gc, err := xproto.NewGcontextId(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := xproto.CreateGCChecked(c, gc, xproto.Drawable(root), 0, nil).Check(); err != nil {
		t.Fatal(err)
	}
	for _, f := range fills {
		pix := uint32(f.c.R)<<16 | uint32(f.c.G)<<8 | uint32(f.c.B)
		xproto.ChangeGC(c, gc, xproto.GcForeground, []uint32{pix})
		if err := xproto.PolyFillRectangleChecked(c, xproto.Drawable(root), gc,
			[]xproto.Rectangle{{X: int16(f.x), Y: int16(f.y), Width: uint16(f.w), Height: uint16(f.h)}}).Check(); err != nil {
			t.Fatal(err)
		}
	}
}

var testFills = []rectFill{
	{0, 0, 640, 480, color.RGBA{0, 0, 0, 255}},
	{10, 10, 100, 80, color.RGBA{255, 0, 0, 255}},
	{200, 10, 100, 80, color.RGBA{0, 255, 0, 255}},
	{10, 200, 100, 80, color.RGBA{0, 0, 255, 255}},
	{400, 300, 200, 150, color.RGBA{250, 200, 20, 255}},
}

func near(a, b color.Color, tol int) bool {
	ar, ag, ab, _ := a.RGBA()
	br, bg, bb, _ := b.RGBA()
	d := func(x, y uint32) bool { return abs(int(x>>8)-int(y>>8)) <= tol }
	return d(ar, br) && d(ag, bg) && d(ab, bb)
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func checkPainted(t *testing.T, img image.Image, scale float64, tol int) {
	t.Helper()
	for _, f := range testFills[1:] {
		x := int((float64(f.x) + float64(f.w)/2) * scale)
		y := int((float64(f.y) + float64(f.h)/2) * scale)
		if !near(img.At(x, y), f.c, tol) {
			t.Errorf("pixel (%d,%d) = %v, want %v", x, y, img.At(x, y), f.c)
		}
	}
	// The background between the rectangles stays black.
	if !near(img.At(int(150*scale), int(150*scale)), color.RGBA{0, 0, 0, 255}, tol) {
		t.Errorf("background pixel = %v", img.At(int(150*scale), int(150*scale)))
	}
}

func TestCaptureXvfb(t *testing.T) {
	n := startXvfb(t, "-ac")
	display := fmt.Sprintf(":%d", n)
	paintRoot(t, display, testFills)
	t.Setenv("DISPLAY", display)
	t.Setenv("XAUTHORITY", "")
	useGrabber(t, platformGrabber())
	client, _ := peertest.Pair(t)
	ctx := ctxT(t)

	img, err := Capture(ctx, client, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if img.Width != 640 || img.Height != 480 || img.Format != PNG {
		t.Fatalf("got %s %dx%d", img.Format, img.Width, img.Height)
	}
	dec, err := png.Decode(bytes.NewReader(img.Data))
	if err != nil {
		t.Fatal(err)
	}
	checkPainted(t, dec, 1, 0)

	// jpeg + downscale to half width.
	small, err := Capture(ctx, client, Options{Format: JPEG, Quality: 90, MaxWidth: 320})
	if err != nil {
		t.Fatal(err)
	}
	if small.Width != 320 || small.Height != 240 {
		t.Fatalf("got %dx%d", small.Width, small.Height)
	}
	jd, err := jpeg.Decode(bytes.NewReader(small.Data))
	if err != nil {
		t.Fatal(err)
	}
	checkPainted(t, jd, 0.5, 12)

	ds, err := Displays(ctx, client)
	if err != nil || len(ds) < 1 || !ds[0].Primary || ds[0].Width != 640 || ds[0].Height != 480 {
		t.Fatalf("displays %+v err %v", ds, err)
	}
	all, err := Capture(ctx, client, Options{Display: AllDisplays})
	if err != nil || all.Width != 640 {
		t.Fatalf("all displays: %+v %v", all, err)
	}
	if _, err := Capture(ctx, client, Options{Display: 9}); err == nil || !strings.Contains(err.Error(), "no display 9") {
		t.Fatalf("bad display: %v", err)
	}
}

// A server that demands a cookie, with the cookie in ~/.Xauthority and
// DISPLAY unset: discovery must find both.
func TestCaptureXvfbWithCookieAndProbing(t *testing.T) {
	home := t.TempDir()
	authFile := filepath.Join(home, ".Xauthority")
	cookie := make([]byte, 16)
	for i := range cookie {
		cookie[i] = byte(0x40 + i)
	}
	// One wildcard entry (any host, any display), as the server's -auth file.
	var rec bytes.Buffer
	rec.Write([]byte{0xff, 0xff, 0, 0, 0, 0, 0, 18})
	rec.WriteString("MIT-MAGIC-COOKIE-1")
	rec.Write([]byte{0, 16})
	rec.Write(cookie)
	if err := os.WriteFile(authFile, rec.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	n := startXvfb(t, "-auth", authFile)
	display := fmt.Sprintf(":%d", n)
	host, _ := os.Hostname()

	// Without the cookie the server must refuse us.
	noCookie := &sysEnv{Getenv: func(string) string { return "" }, Glob: func(string) []string { return nil },
		ReadFile: func(string) ([]byte, error) { return nil, os.ErrNotExist }, Hostname: host}
	noCookie.Getenv = func(k string) string {
		if k == "DISPLAY" {
			return display
		}
		return ""
	}
	if _, err := x11disp.NewFinder(noCookie.x11Env()).Find(ctxT(t)); err == nil {
		t.Fatal("server accepted a connection without a cookie")
	}

	// Paint with an authenticated xgb connection (XAUTHORITY for the helper only).
	t.Setenv("XAUTHORITY", authFile)
	paintRoot(t, display, testFills)

	sock := fmt.Sprintf("/tmp/.X11-unix/X%d", n)
	env := &sysEnv{
		Getenv: func(string) string { return "" }, // no DISPLAY, no XAUTHORITY
		Glob: func(p string) []string {
			if p == "/tmp/.X11-unix/X*" {
				return []string{sock}
			}
			return nil
		},
		ReadFile: os.ReadFile,
		LookPath: func(string) (string, error) { return "", errors.New("none") },
		UID:      os.Getuid(),
		Home:     home,
		Hostname: host,
	}
	g := &linuxGrabber{env: env}
	img, err := g.Grab(ctxT(t), Primary)
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() != 640 {
		t.Fatalf("bounds %v", img.Bounds())
	}
	checkPainted(t, img, 1, 0)
}

func headlessEnv() *sysEnv {
	return &sysEnv{
		Getenv:   func(string) string { return "" },
		Glob:     func(string) []string { return nil },
		ReadFile: func(string) ([]byte, error) { return nil, os.ErrNotExist },
		LookPath: func(string) (string, error) { return "", errors.New("not found") },
		Run:      func(context.Context, []string, string, ...string) error { return errors.New("must not run") },
		UID:      1000,
		Home:     "/nonexistent",
		Hostname: "headless",
	}
}

func TestHeadlessError(t *testing.T) {
	useGrabber(t, &linuxGrabber{env: headlessEnv()})
	client, _ := peertest.Pair(t)
	_, err := Capture(ctxT(t), client, Options{})
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"no display available on the host", "DISPLAY is not set", "/tmp/.X11-unix", "grim"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if _, err := Displays(ctxT(t), client); err == nil || !strings.Contains(err.Error(), "no display available") {
		t.Fatalf("Displays: %v", err)
	}
}

func TestUnreachableDisplayIsNamed(t *testing.T) {
	e := headlessEnv()
	e.Getenv = func(k string) string {
		if k == "DISPLAY" {
			return ":4242"
		}
		return ""
	}
	_, err := (&linuxGrabber{env: e}).Grab(ctxT(t), 0)
	if err == nil || !strings.Contains(err.Error(), "X display :4242") {
		t.Fatalf("err %v", err)
	}
}

func TestWaylandToolFallback(t *testing.T) {
	var mu sync.Mutex
	var ran []string
	var gotEnv []string
	e := headlessEnv()
	e.UID = 1234
	e.Glob = func(p string) []string {
		switch p {
		case "/run/user/1234/wayland-*":
			return []string{"/run/user/1234/wayland-0", "/run/user/1234/wayland-0.lock"}
		case "/run/user/1234/bus":
			return []string{"/run/user/1234/bus"}
		}
		return nil
	}
	e.LookPath = func(n string) (string, error) {
		if n == "grim" {
			return "", errors.New("missing")
		}
		return "/usr/bin/" + n, nil
	}
	e.Run = func(_ context.Context, env []string, name string, args ...string) error {
		mu.Lock()
		ran = append(ran, name)
		gotEnv = env
		mu.Unlock()
		if name == "gnome-screenshot" {
			return errors.New("simulated failure")
		}
		// spectacle -b -n -o FILE
		file := args[len(args)-1]
		img := image.NewRGBA(image.Rect(0, 0, 30, 20))
		for i := 0; i < len(img.Pix); i += 4 {
			img.Pix[i], img.Pix[i+3] = 200, 255
		}
		var b bytes.Buffer
		png.Encode(&b, img)
		return os.WriteFile(file, b.Bytes(), 0o600)
	}
	useGrabber(t, &linuxGrabber{env: e})
	client, _ := peertest.Pair(t)
	img, err := Capture(ctxT(t), client, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if img.Width != 30 || img.Height != 20 {
		t.Fatalf("got %dx%d", img.Width, img.Height)
	}
	mu.Lock()
	toolsRun, env := strings.Join(ran, ","), strings.Join(gotEnv, " ")
	mu.Unlock()
	if toolsRun != "gnome-screenshot,spectacle" {
		t.Fatalf("tools run: %v", toolsRun)
	}
	joined := env
	for _, want := range []string{"WAYLAND_DISPLAY=wayland-0", "XDG_RUNTIME_DIR=/run/user/1234", "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1234/bus"} {
		if !strings.Contains(joined, want) {
			t.Errorf("tool env %q lacks %s", joined, want)
		}
	}
	ds, err := Displays(ctxT(t), client)
	if err != nil || len(ds) != 1 || ds[0].Width != 30 {
		t.Fatalf("displays %+v %v", ds, err)
	}
}

func TestPixelConversion(t *testing.T) {
	// 16-bit RGB565, little endian: red = 0xF800, green = 0x07E0.
	x := &x11{bpp: 16, pad: 32, lsb: true, r: 0xf800, g: 0x07e0, b: 0x001f}
	data := []byte{0x00, 0xf8, 0xe0, 0x07, 0x1f, 0x00, 0, 0} // row padded to 8 bytes
	dst := image.NewRGBA(image.Rect(0, 0, 3, 1))
	x.convert(dst, 0, data, 3, 1, 8)
	for i, want := range []color.RGBA{{255, 0, 0, 255}, {0, 255, 0, 255}, {0, 0, 255, 255}} {
		if got := dst.RGBAAt(i, 0); got != want {
			t.Errorf("565 pixel %d = %v, want %v", i, got, want)
		}
	}
	// 32-bit big endian, BGR masks (0x0000ff red).
	x = &x11{bpp: 32, pad: 32, lsb: false, r: 0xff, g: 0xff00, b: 0xff0000}
	dst = image.NewRGBA(image.Rect(0, 0, 1, 1))
	x.convert(dst, 0, []byte{0x00, 0x33, 0x22, 0x11}, 1, 1, 4) // value 0x00332211
	if got := dst.RGBAAt(0, 0); got != (color.RGBA{0x11, 0x22, 0x33, 255}) {
		t.Errorf("BE pixel = %v", got)
	}
	// 24-bit packed little endian with standard masks.
	x = &x11{bpp: 24, pad: 32, lsb: true, r: 0xff0000, g: 0xff00, b: 0xff}
	dst = image.NewRGBA(image.Rect(0, 0, 1, 1))
	x.convert(dst, 0, []byte{0x30, 0x20, 0x10, 0}, 1, 1, 4)
	if got := dst.RGBAAt(0, 0); got != (color.RGBA{0x10, 0x20, 0x30, 255}) {
		t.Errorf("24bpp pixel = %v", got)
	}
}

// splitMonitors adds RandR monitors covering the left and right halves of
// display, named LEFT and RIGHT (RIGHT primary), as two physical monitors
// side by side would look. Xvfb keeps its automatic whole-screen monitor
// "screen" as well; it sorts after them.
func splitMonitors(t *testing.T, display string) {
	t.Helper()
	c, err := xgb.NewConnDisplay(display)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := randr.Init(c); err != nil {
		t.Skipf("no RandR: %v", err)
	}
	root := xproto.Setup(c).DefaultScreen(c).Root
	if _, err := randr.GetMonitors(c, root, false).Reply(); err != nil {
		t.Skipf("RandR 1.5 monitors unavailable: %v", err)
	}
	for _, name := range []string{"LEFT", "RIGHT"} {
		a, err := xproto.InternAtom(c, false, uint16(len(name)), name).Reply()
		if err != nil {
			t.Fatal(err)
		}
		m := randr.MonitorInfo{Name: a.Atom, Y: 0, Width: 320, Height: 480, WidthInMillimeters: 100, HeightInMillimeters: 150}
		if name == "RIGHT" {
			m.X, m.Primary = 320, true
		}
		if err := randr.SetMonitorChecked(c, root, m).Check(); err != nil {
			t.Skipf("RandR SetMonitor unsupported: %v", err)
		}
	}
}

func TestRegionAndDisplayNameXvfb(t *testing.T) {
	n := startXvfb(t, "-ac")
	display := fmt.Sprintf(":%d", n)
	paintRoot(t, display, testFills)
	splitMonitors(t, display)
	t.Setenv("DISPLAY", display)
	t.Setenv("XAUTHORITY", "")
	useGrabber(t, platformGrabber())
	client, _ := peertest.Pair(t)
	ctx := ctxT(t)

	ds, err := Displays(ctx, client)
	if err != nil || len(ds) < 2 || ds[0].Name != "RIGHT" || ds[0].X != 320 || ds[1].Name != "LEFT" {
		t.Fatalf("displays %+v %v", ds, err)
	}
	idx, err := ResolveDisplay(ctx, client, "left")
	if err != nil || idx != 1 {
		t.Fatalf("resolve left: %d %v", idx, err)
	}
	// The red rectangle (10,10)-(110,90) sits on LEFT; capture a region
	// straddling its corner and check every pixel.
	img, err := Capture(ctx, client, Options{Display: idx, Region: image.Rect(5, 5, 25, 15)})
	if err != nil {
		t.Fatal(err)
	}
	dec, err := png.Decode(bytes.NewReader(img.Data))
	if err != nil || img.Width != 20 || img.Height != 10 {
		t.Fatalf("region %dx%d %v", img.Width, img.Height, err)
	}
	red, black := color.RGBA{255, 0, 0, 255}, color.RGBA{0, 0, 0, 255}
	for y := range 10 {
		for x := range 20 {
			want := black
			if x+5 >= 10 && y+5 >= 10 {
				want = red
			}
			if !near(dec.At(x, y), want, 0) {
				t.Fatalf("pixel (%d,%d) = %v, want %v", x, y, dec.At(x, y), want)
			}
		}
	}
	// The yellow rectangle (400,300)-(600,450) is at (80,300) on RIGHT,
	// the primary display: index 0 by name through the CLI.
	out := filepath.Join(t.TempDir(), "r.png")
	var stderr bytes.Buffer
	env := newEnv(client, &bytes.Buffer{}, &stderr)
	if code := cli.Lookup("screenshot").Run(ctx, env, []string{"-d", "RIGHT", "-R", "78,298,4,4", "-o", out}); code != cli.ExitOK {
		t.Fatalf("cli exit %d: %s", code, &stderr)
	}
	data, _ := os.ReadFile(out)
	rimg, err := png.Decode(bytes.NewReader(data))
	if err != nil || rimg.Bounds().Dx() != 4 {
		t.Fatalf("cli region: %v", err)
	}
	yellow := color.RGBA{250, 200, 20, 255}
	for y := range 4 {
		for x := range 4 {
			want := black
			if x >= 2 && y >= 2 {
				want = yellow
			}
			if !near(rimg.At(x, y), want, 0) {
				t.Fatalf("cli pixel (%d,%d) = %v, want %v", x, y, rimg.At(x, y), want)
			}
		}
	}
}
