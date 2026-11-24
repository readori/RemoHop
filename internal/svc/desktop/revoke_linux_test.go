package desktop

import (
	"context"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/jezek/xgb/xproto"

	"github.com/readori/RemoHop/internal/config"
	"github.com/readori/RemoHop/internal/dial"
	"github.com/readori/RemoHop/internal/hostd"
	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/perm"
)

// hostdConn runs a real host (feature switches, sessions) on loopback with
// its own config directory, which `remohop config` (config.Set) then acts
// on, and logs in to it.
func hostdConn(t *testing.T) *peer.Conn {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("REMOHOP_CONFIG_DIR", dir)
	noLocals := func() []netip.Addr { return nil }
	h, err := hostd.New(hostd.Config{Dir: dir, Listen: "127.0.0.1:0", Locals: noLocals})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	dctx, dcancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer dcancel()
	conn, err := dial.Dial(dctx, h.Addr().String(), dial.Options{Dir: t.TempDir(), Password: h.Password(), Locals: noLocals})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func setFeature(t *testing.T, f perm.Feature, on bool) {
	t.Helper()
	v := "off"
	if on {
		v = "on"
	}
	if err := config.Set(hostd.FeatureSetting(f), v); err != nil {
		t.Fatal(err)
	}
}

// nextHello waits for a Hello announcing input as want, and then for a
// video frame after it: the picture keeps coming either way.
func nextHello(t *testing.T, ctx context.Context, sess *Session, want bool) Hello {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var got *Hello
	for {
		ev, err := sess.Next(ctx)
		if err != nil {
			t.Fatalf("waiting for a hello with input=%v: %v", want, err)
		}
		if ev.Hello != nil && ev.Hello.Input == want {
			got = ev.Hello
		}
		if got != nil && ev.Frame != nil {
			return *got
		}
	}
}

// TestInputRevokedXvfb: the host's owner switches keyboard and mouse off
// while a helper is connected. Keys and buttons the helper holds are let
// go, nothing the helper sends afterwards reaches the screen, the picture
// keeps coming, and the viewer is told by the host that the session is now
// view-only. Switching input back on gives the open session control again.
func TestInputRevokedXvfb(t *testing.T) {
	x := startXvfb(t, 640, 480)
	events := eventWindow(t, x)
	root := xproto.Setup(x).DefaultScreen(x).Root
	conn := hostdConn(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	sess, err := Open(ctx, conn, Options{FPS: 15})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if !sess.Hello().Input {
		t.Fatalf("no input: %s", sess.Hello().InputError)
	}

	if err := sess.SendInput(InputEvent{Kind: InputMove, X: 0.25, Y: 0.5}); err != nil {
		t.Fatal(err)
	}
	waitPointer(t, x, root, 160, 240)
	// Hold Shift and the left button.
	sess.SendInput(InputEvent{Kind: InputKey, Code: "ShiftLeft", Down: true})
	waitEvent(t, events, func(e xproto.KeyPressEvent) bool { return keysym(t, x, e.Detail) == 0xffe1 })
	sess.SendInput(InputEvent{Kind: InputButton, Button: 0, Down: true, X: 0.25, Y: 0.5})
	waitEvent(t, events, func(e xproto.ButtonPressEvent) bool { return e.Detail == 1 })

	setFeature(t, perm.Input, false)
	// What was held is released by the host, at once.
	waitEvent(t, events, func(e xproto.KeyReleaseEvent) bool { return keysym(t, x, e.Detail) == 0xffe1 })
	waitEvent(t, events, func(e xproto.ButtonReleaseEvent) bool { return e.Detail == 1 })
	// The viewer learns it from the host, and the picture goes on.
	h := nextHello(t, ctx, sess, false)
	if !strings.Contains(h.InputError, "switched") || h.Unsupported[ControlType] == "" {
		t.Fatalf("hello after input was switched off: %+v", h)
	}
	if !sess.ViewOnly() {
		t.Fatal("the client still offers input after the host withdrew it")
	}
	// A client that sends anyway is ignored, events and host controls alike.
	if err := sess.send(Control{Type: InputMove, X: 0.75, Y: 0.75}); err != nil {
		t.Fatal(err)
	}
	sess.send(Control{Type: InputKey, Code: "KeyA", Down: true})
	sess.send(Control{Type: InputKey, Code: "KeyA", Down: false})
	if err := sess.call(ctx, Control{Type: ctlType, Text: "x"}); err == nil {
		t.Fatal("the host typed text after input was switched off")
	}
	time.Sleep(300 * time.Millisecond)
	if p, err := xproto.QueryPointer(x, root).Reply(); err != nil {
		t.Fatal(err)
	} else if p.RootX == 479 && p.RootY == 359 {
		t.Fatal("the host injected a pointer move after input was switched off")
	}
	for drained := false; !drained; {
		select {
		case ev := <-events:
			if kp, ok := ev.(xproto.KeyPressEvent); ok {
				t.Fatalf("the host injected a key after input was switched off: %+v", kp)
			}
		default:
			drained = true
		}
	}

	// Back on: the open session drives again.
	setFeature(t, perm.Input, true)
	nextHello(t, ctx, sess, true)
	if sess.ViewOnly() {
		t.Fatal("the client did not take input back")
	}
	if err := sess.SendInput(InputEvent{Kind: InputMove, X: 0.75, Y: 0.75}); err != nil {
		t.Fatal(err)
	}
	waitPointer(t, x, root, 479, 359)
}

// TestScreenRevokedXvfb: switching the screen off ends the open desktop
// stream, and the viewer is told why.
func TestScreenRevokedXvfb(t *testing.T) {
	startXvfb(t, 320, 240)
	conn := hostdConn(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	sess, err := Open(ctx, conn, Options{FPS: 10})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	setFeature(t, perm.Screen, false)
	select {
	case <-sess.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the desktop stream outlived the screen feature")
	}
	if err := sess.Err(); err == nil || !strings.Contains(err.Error(), "switched") {
		t.Fatalf("stream ended with %v, want the host's reason", err)
	}
}
