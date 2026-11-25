package input

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/randr"
	"github.com/jezek/xgb/xproto"

	"github.com/readori/RemoHop/internal/x11disp/xvfbtest"
)

func TestMain(m *testing.M) {
	// xgb complains about the missing ~/.Xauthority on every connection to
	// the test's private Xvfb.
	xgb.Logger = log.New(io.Discard, "", 0)
	os.Exit(m.Run())
}

// startXvfb runs a private X server (WxH, on a display Xvfb picks), points
// DISPLAY at it and returns a connection to it.
func startXvfb(t *testing.T, w, h int) *xgb.Conn {
	t.Helper()
	if _, err := exec.LookPath("Xvfb"); err != nil {
		t.Skip("Xvfb not installed")
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("Xvfb", "-displayfd", "3", "-screen", "0", fmt.Sprintf("%dx%dx24", w, h),
		"-nolisten", "tcp", "-noreset", "-ac")
	cmd.ExtraFiles = []*os.File{pw}
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pw.Close()
	var display string // known once Xvfb reports it
	t.Cleanup(func() { xvfbtest.Stop(cmd, display, nil) })
	line := make(chan string, 1)
	go func() {
		s, _ := bufio.NewReader(pr).ReadString('\n')
		line <- s
		pr.Close()
	}()
	var n string
	select {
	case n = <-line:
	case <-time.After(15 * time.Second):
		t.Fatal("Xvfb did not start")
	}
	n = strings.TrimSpace(n)
	if _, err := strconv.Atoi(n); err != nil {
		t.Skipf("Xvfb did not report a display (%q)", n)
	}
	display = ":" + n
	t.Setenv("DISPLAY", display)
	t.Setenv("XAUTHORITY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	c, err := xgb.NewConnDisplay(":" + n)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

// splitMonitors adds RandR monitors LEFT (x 0) and RIGHT (x w/2, primary)
// covering the halves of the screen, as two monitors side by side.
func splitMonitors(t *testing.T, c *xgb.Conn) {
	t.Helper()
	if err := randr.Init(c); err != nil {
		t.Skipf("no RandR: %v", err)
	}
	scr := xproto.Setup(c).DefaultScreen(c)
	half := scr.WidthInPixels / 2
	for _, name := range []string{"LEFT", "RIGHT"} {
		a, err := xproto.InternAtom(c, false, uint16(len(name)), name).Reply()
		if err != nil {
			t.Fatal(err)
		}
		m := randr.MonitorInfo{Name: a.Atom, Width: half, Height: scr.HeightInPixels, WidthInMillimeters: 100, HeightInMillimeters: 100}
		if name == "RIGHT" {
			m.X, m.Primary = int16(half), true
		}
		if err := randr.SetMonitorChecked(c, scr.Root, m).Check(); err != nil {
			t.Skipf("RandR SetMonitor unsupported: %v", err)
		}
	}
}

// ev is one recorded X event.
type ev struct {
	kind   string // press, release, bpress, brelease, motion
	detail byte
	x, y   int16 // root coordinates
	sym    xproto.Keysym
	state  uint16
}

// recorder is a tiny X client: a window covering the screen with the
// keyboard focus that records what it receives, translating keys with the
// keymap as it stands when they arrive (refetched on every MappingNotify,
// like a real client).
type recorder struct {
	t      *testing.T
	c      *xgb.Conn
	mu     sync.Mutex
	evs    []ev
	notify chan struct{}
	minKC  xproto.Keycode
	keymap *xproto.GetKeyboardMappingReply
}

func newRecorder(t *testing.T, c *xgb.Conn) *recorder {
	t.Helper()
	setup := xproto.Setup(c)
	scr := setup.DefaultScreen(c)
	r := &recorder{t: t, c: c, notify: make(chan struct{}, 1), minKC: setup.MinKeycode}
	r.refresh()
	wid, err := xproto.NewWindowId(c)
	if err != nil {
		t.Fatal(err)
	}
	mask := uint32(xproto.EventMaskKeyPress | xproto.EventMaskKeyRelease | xproto.EventMaskButtonPress |
		xproto.EventMaskButtonRelease | xproto.EventMaskPointerMotion | xproto.EventMaskStructureNotify)
	if err := xproto.CreateWindowChecked(c, 0, wid, scr.Root, 0, 0, scr.WidthInPixels, scr.HeightInPixels, 0,
		xproto.WindowClassInputOnly, 0, xproto.CwEventMask, []uint32{mask}).Check(); err != nil {
		t.Fatal(err)
	}
	mapped := make(chan struct{})
	go func() {
		for {
			e, err := c.WaitForEvent()
			if e == nil && err == nil {
				return
			}
			switch e := e.(type) {
			case xproto.MapNotifyEvent:
				close(mapped)
			case xproto.MappingNotifyEvent:
				r.refresh()
			case xproto.KeyPressEvent:
				r.add(ev{kind: "press", detail: byte(e.Detail), sym: r.sym(e.Detail, e.State), state: e.State})
			case xproto.KeyReleaseEvent:
				r.add(ev{kind: "release", detail: byte(e.Detail), sym: r.sym(e.Detail, e.State), state: e.State})
			case xproto.ButtonPressEvent:
				r.add(ev{kind: "bpress", detail: byte(e.Detail), x: e.RootX, y: e.RootY, state: e.State})
			case xproto.ButtonReleaseEvent:
				r.add(ev{kind: "brelease", detail: byte(e.Detail), x: e.RootX, y: e.RootY, state: e.State})
			case xproto.MotionNotifyEvent:
				r.add(ev{kind: "motion", x: e.RootX, y: e.RootY})
			}
		}
	}()
	xproto.MapWindow(c, wid)
	select {
	case <-mapped:
	case <-time.After(5 * time.Second):
		t.Fatal("window not mapped")
	}
	if err := xproto.SetInputFocusChecked(c, xproto.InputFocusPointerRoot, wid, xproto.TimeCurrentTime).Check(); err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *recorder) refresh() {
	setup := xproto.Setup(r.c)
	km, err := xproto.GetKeyboardMapping(r.c, setup.MinKeycode, byte(setup.MaxKeycode-setup.MinKeycode+1)).Reply()
	if err != nil {
		return
	}
	r.mu.Lock()
	r.keymap = km
	r.mu.Unlock()
}

// sym translates a keycode the way the core protocol does for group 1:
// level 1 with Shift, with the implied upper case of letters.
func (r *recorder) sym(kc xproto.Keycode, state uint16) xproto.Keysym {
	r.mu.Lock()
	defer r.mu.Unlock()
	per := int(r.keymap.KeysymsPerKeycode)
	row := r.keymap.Keysyms[(int(kc)-int(r.minKC))*per:]
	if state&xproto.ModMaskShift != 0 {
		if row[1] != 0 {
			return row[1]
		}
		if row[0] >= 'a' && row[0] <= 'z' {
			return row[0] - 'a' + 'A'
		}
	}
	return row[0]
}

func (r *recorder) add(e ev) {
	r.mu.Lock()
	r.evs = append(r.evs, e)
	r.mu.Unlock()
	select {
	case r.notify <- struct{}{}:
	default:
	}
}

// take waits until match accepts the events recorded so far (or times
// out), then returns them and starts afresh.
func (r *recorder) take(match func([]ev) bool) []ev {
	r.t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		r.mu.Lock()
		evs := append([]ev(nil), r.evs...)
		r.mu.Unlock()
		if match(evs) {
			r.mu.Lock()
			r.evs = r.evs[len(evs):]
			r.mu.Unlock()
			return evs
		}
		select {
		case <-r.notify:
		case <-time.After(50 * time.Millisecond):
		case <-deadline:
			r.t.Fatalf("timed out; recorded %+v", evs)
		}
	}
}

// count returns a matcher waiting for n events of a kind.
func count(kind string, n int) func([]ev) bool {
	return func(evs []ev) bool {
		k := 0
		for _, e := range evs {
			if e.kind == kind {
				k++
			}
		}
		return k >= n
	}
}

// only filters events by kind.
func only(evs []ev, kinds ...string) []ev {
	var out []ev
	for _, e := range evs {
		for _, k := range kinds {
			if e.kind == k {
				out = append(out, e)
			}
		}
	}
	return out
}

// nothingHeld fails if the X server still has any key down.
func nothingHeld(t *testing.T, c *xgb.Conn) {
	t.Helper()
	km, err := xproto.QueryKeymap(c).Reply()
	if err != nil {
		t.Fatal(err)
	}
	for i, b := range km.Keys {
		if b != 0 {
			t.Fatalf("keys still held: byte %d = %08b", i, b)
		}
	}
	p, err := xproto.QueryPointer(c, xproto.Setup(c).DefaultScreen(c).Root).Reply()
	if err != nil {
		t.Fatal(err)
	}
	if p.Mask&(xproto.KeyButMaskButton1|xproto.KeyButMaskButton2|xproto.KeyButMaskButton3|xproto.KeyButMaskShift|xproto.KeyButMaskControl|xproto.KeyButMaskMod1|xproto.KeyButMaskMod4) != 0 {
		t.Fatalf("modifier/button state still set: %#x", p.Mask)
	}
}

func ctxT(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}
