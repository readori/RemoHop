package desktop

import (
	"context"
	"testing"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"

	"github.com/readori/RemoHop/internal/peer/peertest"
)

// eventWindow maps an invisible full-screen window that receives key and button events
// and has the input focus, and returns a channel of its events.
func eventWindow(t *testing.T, c *xgb.Conn) <-chan xgb.Event {
	t.Helper()
	scr := xproto.Setup(c).DefaultScreen(c)
	wid, err := xproto.NewWindowId(c)
	if err != nil {
		t.Fatal(err)
	}
	mask := uint32(xproto.EventMaskKeyPress | xproto.EventMaskKeyRelease | xproto.EventMaskButtonPress |
		xproto.EventMaskButtonRelease | xproto.EventMaskStructureNotify)
	// InputOnly: invisible, so whatever is painted on the root stays in
	// the captured picture.
	if err := xproto.CreateWindowChecked(c, 0, wid, scr.Root, 0, 0, scr.WidthInPixels, scr.HeightInPixels, 0,
		xproto.WindowClassInputOnly, 0, xproto.CwEventMask, []uint32{mask}).Check(); err != nil {
		t.Fatal(err)
	}
	ch := make(chan xgb.Event, 64)
	go func() {
		for {
			ev, err := c.WaitForEvent()
			if ev == nil && err == nil {
				close(ch)
				return
			}
			if ev != nil {
				ch <- ev
			}
		}
	}()
	xproto.MapWindow(c, wid)
	for ev := range ch { // wait until mapped
		if _, ok := ev.(xproto.MapNotifyEvent); ok {
			break
		}
	}
	if err := xproto.SetInputFocusChecked(c, xproto.InputFocusPointerRoot, wid, xproto.TimeCurrentTime).Check(); err != nil {
		t.Fatal(err)
	}
	return ch
}

func waitEvent[T xgb.Event](t *testing.T, ch <-chan xgb.Event, match func(T) bool) T {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev := <-ch:
			if e, ok := ev.(T); ok && match(e) {
				return e
			}
		case <-timeout:
			var zero T
			t.Fatalf("timed out waiting for %T", zero)
			return zero
		}
	}
}

func TestInputXvfb(t *testing.T) {
	x := startXvfb(t, 640, 480)
	events := eventWindow(t, x)
	client, _ := peertest.Pair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sess, err := Open(ctx, client, Options{FPS: 15})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if !sess.Hello().Input {
		t.Fatalf("no input: %s", sess.Hello().InputError)
	}
	root := xproto.Setup(x).DefaultScreen(x).Root

	// Absolute motion lands where the fractions say (0.25*639, 0.5*479).
	if err := sess.SendInput(InputEvent{Kind: InputMove, X: 0.25, Y: 0.5}); err != nil {
		t.Fatal(err)
	}
	waitPointer(t, x, root, 160, 240)

	// Click: press and release of button 1 at the given position.
	sess.SendInput(InputEvent{Kind: InputButton, Button: 0, Down: true, X: 0.5, Y: 0.5})
	sess.SendInput(InputEvent{Kind: InputButton, Button: 0, Down: false, X: 0.5, Y: 0.5})
	bp := waitEvent(t, events, func(e xproto.ButtonPressEvent) bool { return true })
	if bp.Detail != 1 || bp.RootX != 320 || bp.RootY != 240 {
		t.Fatalf("button press %+v", bp)
	}
	waitEvent(t, events, func(e xproto.ButtonReleaseEvent) bool { return e.Detail == 1 })

	// Right button and wheel down (X button 5).
	sess.SendInput(InputEvent{Kind: InputButton, Button: 2, Down: true, X: 0.5, Y: 0.5})
	sess.SendInput(InputEvent{Kind: InputButton, Button: 2, Down: false, X: 0.5, Y: 0.5})
	waitEvent(t, events, func(e xproto.ButtonPressEvent) bool { return e.Detail == 3 })
	sess.SendInput(InputEvent{Kind: InputWheel, DY: 1})
	waitEvent(t, events, func(e xproto.ButtonPressEvent) bool { return e.Detail == 5 })

	// Key "KeyA" arrives as the keycode the server maps to keysym 'a'.
	sess.SendInput(InputEvent{Kind: InputKey, Code: "KeyA", Down: true})
	sess.SendInput(InputEvent{Kind: InputKey, Code: "KeyA", Down: false})
	kp := waitEvent(t, events, func(e xproto.KeyPressEvent) bool { return true })
	if sym := keysym(t, x, kp.Detail); sym != 'a' {
		t.Fatalf("KeyA produced keycode %d (keysym %#x)", kp.Detail, sym)
	}
	waitEvent(t, events, func(e xproto.KeyReleaseEvent) bool { return e.Detail == kp.Detail })

	// A held modifier is released when the session ends.
	sess.SendInput(InputEvent{Kind: InputKey, Code: "ShiftLeft", Down: true})
	waitEvent(t, events, func(e xproto.KeyPressEvent) bool { return keysym(t, x, e.Detail) == 0xffe1 })
	sess.Close()
	waitEvent(t, events, func(e xproto.KeyReleaseEvent) bool { return keysym(t, x, e.Detail) == 0xffe1 })
}

func TestInputRefusedXvfb(t *testing.T) {
	x := startXvfb(t, 320, 240)
	root := xproto.Setup(x).DefaultScreen(x).Root
	client, _ := peertest.Pair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Client-side view-only.
	sess, err := Open(ctx, client, Options{FPS: 10, ViewOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if h := sess.Hello(); h.Input || h.InputError == "" {
		t.Fatalf("view-only hello %+v", h)
	}
	if err := sess.SendInput(InputEvent{Kind: InputMove, X: 1, Y: 1}); err != ErrViewOnly {
		t.Fatalf("SendInput on view-only: %v", err)
	}
	sess.Close()

	// Host-side refusal: even a client that sends input is ignored.
	t.Setenv(hostViewOnlyEnv, "1")
	sess, err = Open(ctx, client, Options{FPS: 10})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if h := sess.Hello(); h.Input {
		t.Fatalf("host-view-only hello %+v", h)
	}
	if err := sess.send(Control{Type: InputMove, X: 1, Y: 1}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	p, err := xproto.QueryPointer(x, root).Reply()
	if err != nil {
		t.Fatal(err)
	}
	if p.RootX == 319 && p.RootY == 239 {
		t.Fatal("host injected input although it refuses input")
	}
}

func waitPointer(t *testing.T, c *xgb.Conn, root xproto.Window, wx, wy int16) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		p, err := xproto.QueryPointer(c, root).Reply()
		if err != nil {
			t.Fatal(err)
		}
		if p.RootX == wx && p.RootY == wy {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("pointer at %d,%d, want %d,%d", p.RootX, p.RootY, wx, wy)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func keysym(t *testing.T, c *xgb.Conn, kc xproto.Keycode) xproto.Keysym {
	t.Helper()
	rep, err := xproto.GetKeyboardMapping(c, kc, 1).Reply()
	if err != nil || len(rep.Keysyms) == 0 {
		t.Fatalf("keyboard mapping: %v", err)
	}
	return rep.Keysyms[0]
}
