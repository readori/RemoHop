package desktop

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/randr"
	"github.com/jezek/xgb/xproto"

	"github.com/readori/RemoHop/internal/peer/peertest"
)

// twoMonitors makes an Xvfb of 800x300 with a 320x240 monitor at 0,0 and a
// 400x240 one at 400,60, painted red and blue over a green desktop, so that
// the whole-screen picture, each monitor, and the gap between them are
// told apart by colour. It returns the indices of the monitors in
// HostInfo.Displays order (Xvfb adds a whole-screen monitor of its own).
func twoMonitors(t *testing.T) *xgb.Conn {
	t.Helper()
	x := startXvfb(t, 800, 300)
	if err := randr.Init(x); err != nil {
		t.Skipf("no RandR: %v", err)
	}
	root := xproto.Setup(x).DefaultScreen(x).Root
	for _, m := range []struct {
		name string
		x, y int16
		w, h uint16
	}{{"LEFT", 0, 0, 320, 240}, {"RIGHT", 400, 60, 400, 240}} {
		atom, err := xproto.InternAtom(x, false, uint16(len(m.name)), m.name).Reply()
		if err != nil {
			t.Fatal(err)
		}
		mon := randr.MonitorInfo{Name: atom.Atom, X: m.x, Y: m.y, Width: m.w, Height: m.h, WidthInMillimeters: 80, HeightInMillimeters: 60}
		if err := randr.SetMonitorChecked(x, root, mon).Check(); err != nil {
			t.Skipf("cannot define monitors: %v", err)
		}
	}
	paint(t, x,
		rect{0, 0, 800, 300, 0x00ff00},
		rect{0, 0, 320, 240, 0xff0000},
		rect{400, 60, 400, 240, 0x0000ff},
	)
	return x
}

func monitorIndex(h Hello, x, y, w int) int {
	for i, d := range h.Displays {
		if d.X == x && d.Y == y && d.Width == w {
			return i + 1
		}
	}
	return 0
}

// picture records frames until it has n, starting at the stream's key frame
// (the one after the hello just seen), and returns the decoded last one.
func picture(t *testing.T, ctx context.Context, sess *Session, h Hello, n int) (int, int, []byte) {
	t.Helper()
	rec, err := NewRecorder(filepath.Join(t.TempDir(), "p.mp4"), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := rec.SetStream(h); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; {
		f, hello := nextFrame(t, ctx, sess)
		if hello != nil {
			t.Fatalf("unexpected hello %dx%d", hello.Width, hello.Height)
		}
		if i == 0 && !f.Key {
			continue
		}
		if err := rec.WriteFrame(f); err != nil {
			t.Fatal(err)
		}
		i++
	}
	if err := rec.Close(); err != nil {
		t.Fatal(err)
	}
	return decodeLast(t, rec.Files()[0])
}

func near(t *testing.T, rgb []byte, w, x, y int, want [3]int, what string) {
	t.Helper()
	r, g, b := pixel(rgb, w, x, y)
	got := [3]int{r, g, b}
	for i := range got {
		if d := got[i] - want[i]; d > 40 || d < -40 {
			t.Errorf("%s: pixel (%d,%d) = %v, want ~%v", what, x, y, got, want)
			return
		}
	}
}

// The all-monitors view shows the whole virtual desktop, including what
// lies between monitors of different sizes and positions; switching to one
// monitor and back is live, and the pointer maps onto whatever is shown.
func TestAllMonitorsXvfb(t *testing.T) {
	x := twoMonitors(t)
	root := xproto.Setup(x).DefaultScreen(x).Root
	client, _ := peertest.Pair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	sess, err := Open(ctx, client, Options{FPS: 15, Codec: codecH264, Display: DisplayAll})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	h := sess.Hello()
	left, right := monitorIndex(h, 0, 0, 320), monitorIndex(h, 400, 60, 400)
	if left == 0 || right == 0 {
		t.Fatalf("monitors not listed: %+v", h.Displays)
	}
	if h.Width != 800 || h.Height != 300 || h.Display != DisplayAll || !h.AllDisplays || h.Unsupported[ControlAllMonitors] != "" {
		t.Fatalf("all-monitors hello: %dx%d display %d all %v unsupported %v", h.Width, h.Height, h.Display, h.AllDisplays, h.Unsupported)
	}
	w, _, rgb := picture(t, ctx, sess, h, 10)
	near(t, rgb, w, 100, 100, [3]int{255, 0, 0}, "left monitor")
	near(t, rgb, w, 600, 200, [3]int{0, 0, 255}, "right monitor")
	near(t, rgb, w, 360, 150, [3]int{0, 255, 0}, "between the monitors")

	// The pointer covers the whole virtual desktop.
	sess.SendInput(InputEvent{Kind: InputMove, X: 0.5, Y: 0.5})
	waitPointer(t, x, root, 400, 150)
	sess.SendInput(InputEvent{Kind: InputMove, X: 1, Y: 1})
	waitPointer(t, x, root, 799, 299)

	// One monitor: its picture, and the pointer on it.
	if err := sess.SetDisplay(ctx, right); err != nil {
		t.Fatal(err)
	}
	var hello *Hello
	for hello == nil {
		_, hello = nextFrame(t, ctx, sess)
	}
	if hello.Width != 400 || hello.Height != 240 || hello.Display != right || hello.AllDisplays {
		t.Fatalf("monitor hello: %dx%d display %d all %v", hello.Width, hello.Height, hello.Display, hello.AllDisplays)
	}
	w, _, rgb = picture(t, ctx, sess, *hello, 10)
	near(t, rgb, w, 200, 120, [3]int{0, 0, 255}, "right monitor alone")
	sess.SendInput(InputEvent{Kind: InputMove, X: 0.5, Y: 0.5})
	waitPointer(t, x, root, 600, 180)
	sess.SendInput(InputEvent{Kind: InputMove, X: 0, Y: 0})
	waitPointer(t, x, root, 400, 60)

	// And back to everything.
	if err := sess.SetDisplay(ctx, DisplayAll); err != nil {
		t.Fatal(err)
	}
	hello = nil
	for hello == nil {
		_, hello = nextFrame(t, ctx, sess)
	}
	if hello.Width != 800 || hello.Height != 300 || !hello.AllDisplays {
		t.Fatalf("back to all: %dx%d all %v", hello.Width, hello.Height, hello.AllDisplays)
	}
	w, _, rgb = picture(t, ctx, sess, *hello, 10)
	near(t, rgb, w, 100, 100, [3]int{255, 0, 0}, "left monitor after switching back")
	sess.SendInput(InputEvent{Kind: InputMove, X: 0.25, Y: 0.5})
	waitPointer(t, x, root, 200, 150)
}

// Display 0 is the host's default, which on X11 is every monitor; the hello
// says so, so that a viewer can show the right menu entry.
func TestDefaultDisplayIsAllXvfb(t *testing.T) {
	twoMonitors(t)
	client, _ := peertest.Pair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sess, err := Open(ctx, client, Options{FPS: 15, Codec: codecH264})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if h := sess.Hello(); h.Display != 0 || !h.AllDisplays || h.Width != 800 {
		t.Fatalf("default hello: %dx%d display %d all %v", h.Width, h.Height, h.Display, h.AllDisplays)
	}
	info, err := Probe(ctx, client)
	if err != nil || len(info.Displays) < 2 || info.Unsupported[ControlAllMonitors] != "" {
		t.Fatalf("probe: %+v %v", info, err)
	}
}
