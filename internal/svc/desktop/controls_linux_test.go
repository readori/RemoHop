package desktop

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/randr"
	"github.com/jezek/xgb/xproto"

	"github.com/readori/RemoHop/internal/inject"
	"github.com/readori/RemoHop/internal/peer/peertest"
)

// typedText maps an invisible window with the focus and returns what has
// been typed into it, decoding key presses with the current keymap (which
// the injector rewrites for characters the layout lacks).
func typedText(t *testing.T, c *xgb.Conn) func() string {
	t.Helper()
	setup := xproto.Setup(c)
	scr := setup.DefaultScreen(c)
	var mu sync.Mutex
	var km *xproto.GetKeyboardMappingReply
	refresh := func() {
		r, err := xproto.GetKeyboardMapping(c, setup.MinKeycode, byte(setup.MaxKeycode-setup.MinKeycode+1)).Reply()
		if err == nil {
			mu.Lock()
			km = r
			mu.Unlock()
		}
	}
	refresh()
	wid, _ := xproto.NewWindowId(c)
	xproto.CreateWindow(c, 0, wid, scr.Root, 0, 0, scr.WidthInPixels, scr.HeightInPixels, 0, xproto.WindowClassInputOnly, 0,
		xproto.CwEventMask, []uint32{xproto.EventMaskKeyPress | xproto.EventMaskStructureNotify})
	xproto.MapWindow(c, wid)
	var typed []rune
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
				refresh()
			case xproto.KeyPressEvent:
				mu.Lock()
				per := int(km.KeysymsPerKeycode)
				row := km.Keysyms[(int(e.Detail)-int(setup.MinKeycode))*per:]
				sym := row[0]
				if e.State&xproto.ModMaskShift != 0 && row[1] != 0 {
					sym = row[1]
				}
				if r, ok := inject.KeysymRune(inject.Keysym(sym)); ok {
					typed = append(typed, r)
				}
				mu.Unlock()
			}
		}
	}()
	<-mapped
	xproto.SetInputFocusChecked(c, xproto.InputFocusPointerRoot, wid, xproto.TimeCurrentTime).Check()
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		return string(typed)
	}
}

func TestTypeTextXvfb(t *testing.T) {
	x := startXvfb(t, 320, 240)
	typed := typedText(t, x)
	client, _ := peertest.Pair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sess, err := Open(ctx, client, Options{FPS: 10})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	want := "Hi, Grüße € 日本 ok!"
	if err := sess.TypeText(ctx, want); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for typed() != want {
		if time.Now().After(deadline) {
			t.Fatalf("typed %q, want %q", typed(), want)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Control characters cannot be typed: the rest is, and the error says
	// what was skipped.
	if err := sess.TypeText(ctx, "a\x07b"); err == nil || !strings.Contains(err.Error(), "skipped") {
		t.Fatalf("control character: %v", err)
	}
	if err := sess.TypeText(ctx, strings.Repeat("x", maxTypeText+1)); err == nil {
		t.Fatal("accepted oversized text")
	}
}

// Every host control is refused to a session without input, by the host
// and not only by the client.
func TestControlsNeedInputXvfb(t *testing.T) {
	x := startXvfb(t, 320, 240)
	typed := typedText(t, x)
	client, _ := peertest.Pair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	t.Setenv(hostViewOnlyEnv, "1")
	sess, err := Open(ctx, client, Options{FPS: 10})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	h := sess.Hello()
	for _, k := range []string{ControlType, ControlLock, ControlCtrlAltDel, ControlBlockInput, ControlBlackout} {
		if h.Unsupported[k] == "" {
			t.Errorf("hello does not mark %s unavailable: %v", k, h.Unsupported)
		}
	}
	// Bypass the client's own check: the host must refuse.
	for _, c := range []Control{{Type: ctlType, Text: "nope"}, {Type: ctlLock}} {
		err := sess.call(ctx, c)
		if err == nil || !strings.Contains(err.Error(), "input is off") {
			t.Errorf("%s: %v", c.Type, err)
		}
	}
	time.Sleep(200 * time.Millisecond)
	if s := typed(); s != "" {
		t.Fatalf("typed %q on a view-only host", s)
	}
	// The client refuses too.
	view, err := Open(ctx, client, Options{FPS: 10, ViewOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	if err := view.TypeText(ctx, "x"); !errors.Is(err, ErrViewOnly) {
		t.Fatalf("client-side view-only: %v", err)
	}
}

// Block input and blackout are reported unsupported on X11, with a reason,
// and asking anyway fails with that reason rather than pretending.
func TestUnsupportedControlsXvfb(t *testing.T) {
	startXvfb(t, 320, 240)
	client, _ := peertest.Pair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sess, err := Open(ctx, client, Options{FPS: 10})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	h := sess.Hello()
	if !strings.Contains(h.Unsupported[ControlBlockInput], "X11") || !strings.Contains(h.Unsupported[ControlBlackout], "DPMS") {
		t.Fatalf("unsupported %v", h.Unsupported)
	}
	if _, ok := h.Unsupported[ControlType]; ok {
		t.Fatalf("typing marked unsupported: %v", h.Unsupported)
	}
	for _, f := range []func(context.Context, bool) error{sess.BlockInput, sess.Blackout} {
		if err := f(ctx, true); err == nil || !strings.Contains(err.Error(), "not supported") {
			t.Errorf("got %v", err)
		}
	}
	// Locking needs a real desktop session; under a bare Xvfb there is
	// none, and the error says what was tried.
	if err := sess.Lock(ctx); err == nil || !strings.Contains(err.Error(), "lock") {
		t.Errorf("lock under Xvfb: %v", err)
	}
}

// Switching monitors mid-session: the new stream has the monitor's size,
// starts with a key frame, and input lands on that monitor.
func TestSetDisplayXvfb(t *testing.T) {
	x := startXvfb(t, 640, 240)
	// Two virtual monitors side by side.
	if err := randr.Init(x); err != nil {
		t.Skipf("no RandR: %v", err)
	}
	root := xproto.Setup(x).DefaultScreen(x).Root
	for i, name := range []string{"LEFT", "RIGHT"} {
		atom, err := xproto.InternAtom(x, false, uint16(len(name)), name).Reply()
		if err != nil {
			t.Fatal(err)
		}
		mon := randr.MonitorInfo{Name: atom.Atom, X: int16(320 * i), Width: 320, Height: 240, WidthInMillimeters: 80, HeightInMillimeters: 60}
		if err := randr.SetMonitorChecked(x, root, mon).Check(); err != nil {
			t.Skipf("cannot define monitors: %v", err)
		}
	}
	client, _ := peertest.Pair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sess, err := Open(ctx, client, Options{FPS: 15})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	h := sess.Hello()
	// Xvfb keeps its own whole-screen monitor next to the two defined.
	right := 0
	for i, d := range h.Displays {
		if d.X == 320 && d.Width == 320 {
			right = i + 1
		}
	}
	if h.Width != 640 || h.Display != 0 || len(h.Displays) < 2 || right == 0 {
		t.Fatalf("hello %dx%d display %d displays %v", h.Width, h.Height, h.Display, h.Displays)
	}
	if err := sess.SetDisplay(ctx, right); err != nil {
		t.Fatal(err)
	}
	for {
		f, hello := nextFrame(t, ctx, sess)
		if hello != nil {
			if hello.Width != 320 || hello.Height != 240 || hello.Display != right {
				t.Fatalf("after switching: %dx%d display %d", hello.Width, hello.Height, hello.Display)
			}
			break
		}
		_ = f
	}
	if f, _ := nextFrame(t, ctx, sess); f == nil || !f.Key {
		t.Fatal("new monitor's stream does not start with a key frame")
	}
	// Input coordinates now map onto the right-hand monitor.
	sess.SendInput(InputEvent{Kind: InputMove, X: 0.5, Y: 0.5})
	waitPointer(t, x, root, 320+160, 120)
	if err := sess.SetDisplay(ctx, 9); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("bad monitor: %v", err)
	}
}
