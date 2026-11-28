package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/wire"
)

// The text I-beam is a monochrome pointer that inverts the screen: it
// becomes black with a white outline, so it shows on any background; the
// pixels that only paint (white, black) keep their colour, and the ones
// that leave the screen alone are transparent.
func TestMaskedCursor(t *testing.T) {
	const w, h = 5, 5
	and := make([]bool, w*h)
	xor := make([]uint32, w*h)
	for i := range and {
		and[i] = true // transparent everywhere...
	}
	for y := 1; y <= 3; y++ { // ...but a vertical bar of inverting pixels
		and[y*w+2], xor[y*w+2] = true, 0xffffff
	}
	and[0], xor[0] = false, 0xffffff // a white pixel
	and[4], xor[4] = false, 0        // a black pixel
	pix := maskedCursor(w, h, and, xor)
	at := func(x, y int) [4]byte { i := 4 * (y*w + x); return [4]byte(pix[i : i+4]) }
	black, white, clear := [4]byte{0, 0, 0, 255}, [4]byte{255, 255, 255, 255}, [4]byte{}
	for y := 1; y <= 3; y++ {
		if got := at(2, y); got != black {
			t.Errorf("inverting pixel 2,%d = %v, want black", y, got)
		}
		for _, x := range []int{1, 3} {
			if got := at(x, y); got != white {
				t.Errorf("outline pixel %d,%d = %v, want white", x, y, got)
			}
		}
	}
	for _, p := range [][2]int{{2, 0}, {2, 4}, {1, 0}, {3, 4}} {
		if got := at(p[0], p[1]); got != white {
			t.Errorf("outline pixel %v = %v, want white", p, got)
		}
	}
	if at(0, 0) != white || at(4, 0) != black {
		t.Errorf("painted pixels: %v %v", at(0, 0), at(4, 0))
	}
	for _, p := range [][2]int{{0, 2}, {4, 2}, {0, 4}} {
		if got := at(p[0], p[1]); got != clear {
			t.Errorf("pixel %v = %v, want transparent", p, got)
		}
	}
}

// dibCursor reads Windows' bitmaps as GetDIBits gives them (BGRA, top
// down): a monochrome pointer from its double-height mask, a colour pointer
// with alpha by its alpha, one without alpha by its AND mask.
func TestDIBCursor(t *testing.T) {
	px := func(b, g, r, a byte) []byte { return []byte{b, g, r, a} }
	white, black := px(255, 255, 255, 0), px(0, 0, 0, 0)
	cat := func(ps ...[]byte) []byte { return bytes.Join(ps, nil) }

	// 2x1 monochrome: AND row (transparent, opaque), XOR row (white, white).
	pix, err := dibCursor(2, 1, cat(white, black, white, white), nil)
	if err != nil {
		t.Fatal(err)
	}
	// Pixel 0 inverts (AND 1, XOR 1): black; pixel 1 paints white.
	if want := []byte{0, 0, 0, 255, 255, 255, 255, 255}; !bytes.Equal(pix, want) {
		t.Errorf("monochrome: %v, want %v", pix, want)
	}

	// Colour with alpha: BGRA to RGBA, the mask ignored.
	pix, err = dibCursor(2, 1, cat(white, white), cat(px(1, 2, 3, 128), px(4, 5, 6, 0)))
	if err != nil {
		t.Fatal(err)
	}
	if want := []byte{3, 2, 1, 128, 6, 5, 4, 0}; !bytes.Equal(pix, want) {
		t.Errorf("alpha: %v, want %v", pix, want)
	}

	// Colour without alpha: the AND mask decides.
	pix, err = dibCursor(2, 1, cat(black, white), cat(px(1, 2, 3, 0), px(0, 0, 0, 0)))
	if err != nil {
		t.Fatal(err)
	}
	if want := []byte{3, 2, 1, 255, 0, 0, 0, 0}; !bytes.Equal(pix, want) {
		t.Errorf("masked colour: %v, want %v", pix, want)
	}

	if _, err := dibCursor(2, 2, cat(white, white), nil); err == nil {
		t.Error("a short mask was accepted")
	}
}

func TestPremultipliedToStraight(t *testing.T) {
	got := premultipliedToStraight([]uint32{0x80402000, 0xff102030, 0})
	want := []byte{0x7f, 0x3f, 0, 0x80, 0x10, 0x20, 0x30, 0xff, 0, 0, 0, 0}
	if !bytes.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func testImage(w, h, hx, hy int, seed byte) cursorImage {
	pix := make([]byte, w*h*4)
	for i := range pix {
		pix[i] = byte(i) + seed
	}
	for i := 3; i < len(pix); i += 4 {
		pix[i] = 255
	}
	return cursorImage{w: w, h: h, hx: hx, hy: hy, pix: pix}
}

// A shape's PNG holds the pixels as they were, its ID depends on the
// pixels and the hotspot only, and blank or oversized images are refused.
func TestEncodeCursor(t *testing.T) {
	img := testImage(12, 20, 3, 4, 0)
	s, err := encodeCursor(img)
	if err != nil {
		t.Fatal(err)
	}
	if s.ID == 0 || s.Width != 12 || s.Height != 20 || s.HotX != 3 || s.HotY != 4 {
		t.Fatalf("shape %+v", s)
	}
	dec, err := png.Decode(bytes.NewReader(s.PNG))
	if err != nil {
		t.Fatal(err)
	}
	for y := range 20 {
		for x := range 12 {
			r, g, b, a := dec.At(x, y).RGBA()
			i := 4 * (y*12 + x)
			if got, want := [4]byte{byte(r >> 8), byte(g >> 8), byte(b >> 8), byte(a >> 8)}, [4]byte(img.pix[i:i+4]); got != want {
				t.Fatalf("pixel %d,%d: %v, want %v", x, y, got, want)
			}
		}
	}
	again, _ := encodeCursor(testImage(12, 20, 3, 4, 0))
	other, _ := encodeCursor(testImage(12, 20, 4, 4, 0))
	if again.ID != s.ID || other.ID == s.ID {
		t.Errorf("IDs: %d, again %d, other hotspot %d", s.ID, again.ID, other.ID)
	}
	if _, err := encodeCursor(cursorImage{w: 2, h: 2, pix: make([]byte, 16)}); err != errBlankCursor {
		t.Errorf("blank image: %v", err)
	}
	if _, err := encodeCursor(testImage(maxCursorSide+1, 2, 0, 0, 0)); err == nil {
		t.Error("an oversized image was accepted")
	}
	// A hotspot outside the image is clamped into it.
	if s, err := encodeCursor(testImage(4, 4, 9, -1, 0)); err != nil || s.HotX != 3 || s.HotY != 0 {
		t.Errorf("clamped hotspot: %+v, %v", s, err)
	}
}

// fakeCursor is a cursorSource whose readings a test sets.
type fakeCursor struct {
	r      cursorReading
	err    error
	imgs   map[uint64]cursorImage
	imgErr error
	loads  int
}

func (f *fakeCursor) read() (cursorReading, error) { return f.r, f.err }
func (f *fakeCursor) image() (cursorImage, error) {
	f.loads++
	if f.imgErr != nil {
		return cursorImage{}, f.imgErr
	}
	return f.imgs[f.r.shape], nil
}
func (f *fakeCursor) close() {}

// The tracker sends a message only when something changed, a shape only
// the first time the client needs it (again once it has sent
// CursorShapesKept others since), positions relative to the capture area,
// and treats an empty image as a hidden pointer.
func TestCursorTracker(t *testing.T) {
	src := &fakeCursor{imgs: map[uint64]cursorImage{}}
	for k := range uint64(CursorShapesKept + 2) {
		src.imgs[k+1] = testImage(8, 8, 1, 1, byte(k))
	}
	src.imgs[100] = cursorImage{w: 4, h: 4, pix: make([]byte, 64)} // blank
	tr := newCursorTracker(src)
	poll := func(what string) *cursorMsg {
		t.Helper()
		m, err := tr.poll(100, 50)
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		if m != nil {
			tr.commit(m)
		}
		return m
	}

	src.r = cursorReading{x: 110, y: 70, visible: true, shape: 1}
	m := poll("first")
	if m == nil || m.Shape == nil || m.ID != m.Shape.ID || m.X != 10 || m.Y != 20 || !m.Visible {
		t.Fatalf("first message %+v", m)
	}
	arrow := m.ID
	if m := poll("unchanged"); m != nil {
		t.Fatalf("unchanged pointer sent %+v", m)
	}
	src.r.x = 111
	if m := poll("moved"); m == nil || m.Shape != nil || m.X != 11 || m.ID != arrow {
		t.Fatalf("moved: %+v", m)
	}
	src.r.shape = 2
	m = poll("new shape")
	if m == nil || m.Shape == nil || m.ID == arrow {
		t.Fatalf("new shape: %+v", m)
	}
	loads := src.loads
	src.r.shape = 1
	if m := poll("back"); m == nil || m.Shape != nil || m.ID != arrow {
		t.Fatalf("back to the first shape: %+v", m)
	}
	if src.loads != loads {
		t.Error("a known shape was read again")
	}

	// Hidden: no shape, ID 0; an empty image hides it too.
	src.r.visible = false
	if m := poll("hidden"); m == nil || m.Visible || m.ID != 0 || m.Shape != nil {
		t.Fatalf("hidden: %+v", m)
	}
	src.r.visible, src.r.shape = true, 100
	if m := poll("blank"); m != nil {
		t.Fatalf("an empty pointer image was sent as visible: %+v", m)
	}

	// A message that could not be queued is sent again.
	src.r.shape = 1
	m, _ = tr.poll(100, 50)
	if m == nil || !m.Visible {
		t.Fatalf("visible again: %+v", m)
	}
	if m2, _ := tr.poll(100, 50); m2 == nil || m2.CursorState != m.CursorState {
		t.Fatalf("an uncommitted message was not repeated: %+v", m2)
	}
	tr.commit(m)

	// After CursorShapesKept other shapes, the first is sent again.
	for k := uint64(2); k <= CursorShapesKept+1; k++ {
		src.r.shape = k
		poll(fmt.Sprint("shape ", k))
	}
	src.r.shape = 1
	if m := poll("evicted"); m == nil || m.Shape == nil || m.ID != arrow {
		t.Fatalf("a shape the client may have dropped was not sent again: %+v", m)
	}

	// An image that cannot be read: a default arrow (ID 0), read again
	// at the next poll.
	src.imgErr = errors.New("no image")
	src.r.shape = CursorShapesKept + 2
	if m := poll("unreadable"); m == nil || !m.Visible || m.ID != 0 {
		t.Fatalf("unreadable image: %+v", m)
	}
	src.imgErr = nil
	if m := poll("readable"); m == nil || m.ID == 0 || m.Shape == nil {
		t.Fatalf("image read again: %+v", m)
	}

	src.err = errors.New("gone")
	if _, err := tr.poll(0, 0); err == nil {
		t.Fatal("a read error was not reported")
	}
}

// cursorFrame builds a 'C' wire frame.
func cursorFrame(t *testing.T, m cursorMsg) []byte {
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	msg := make([]byte, 5+len(b))
	putFrameHead(msg, frameCursor, len(b))
	copy(msg[5:], b)
	return msg
}

// fakeHostSession opens a client Session on one end of a pipe; the test
// plays the host on the other end, starting with hello.
func fakeHostSession(t *testing.T, opts Options, hello Hello) (*Session, net.Conn) {
	t.Helper()
	hostEnd, clientEnd := net.Pipe()
	t.Cleanup(func() { hostEnd.Close(); clientEnd.Close() })
	s := &Session{s: newPipeStream(clientEnd), opts: opts, events: make(chan Event, eventQueue), done: make(chan struct{}),
		waiting: map[uint32]chan Reply{}, cursorReady: make(chan struct{}, 1)}
	first := make(chan error, 1)
	go s.readLoop(first)
	// Drain what the client sends (pings, input).
	go func() {
		for {
			var c Control
			if wire.ReadJSON(hostEnd, &c) != nil {
				return
			}
		}
	}()
	hello.Width, hello.Height, hello.Codec = 64, 64, "av01.0.04M.08"
	body, _ := json.Marshal(hello)
	msg := make([]byte, 5+len(body))
	putFrameHead(msg, frameHello, len(body))
	copy(msg[5:], body)
	if _, err := hostEnd.Write(msg); err != nil {
		t.Fatal(err)
	}
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	return s, hostEnd
}

func shapeFor(t *testing.T, img cursorImage) *CursorShape {
	s, err := encodeCursor(img)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// A client that asked for the pointer gets it coalesced: every shape in
// order and the latest state; bad updates are dropped. A client that did
// not ask (as an older client, which knows no 'C' frames) gets none and
// goes on.
func TestSessionCursor(t *testing.T) {
	ctx := t.Context()
	s, host := fakeHostSession(t, Options{Cursor: true}, Hello{Cursor: true, ScreenWidth: 640, ScreenHeight: 480})
	a, b := shapeFor(t, testImage(8, 8, 0, 0, 1)), shapeFor(t, testImage(8, 8, 0, 0, 2))
	bad := *b
	bad.Width = 9 // does not match its PNG
	for _, m := range []cursorMsg{
		{CursorState: CursorState{ID: a.ID, X: 1, Y: 2, Visible: true}, Shape: a},
		{CursorState: CursorState{ID: b.ID, X: 3, Y: 4, Visible: true}, Shape: &bad},
		{CursorState: CursorState{ID: b.ID, X: 5, Y: 6, Visible: true}, Shape: b},
		{CursorState: CursorState{ID: a.ID, X: 7, Y: 8, Visible: true}},
	} {
		host.Write(cursorFrame(t, m))
	}
	var shapes []uint32
	var last CursorState
	for last.X != 7 {
		ev, err := s.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if ev.Cursor == nil {
			continue
		}
		for _, sh := range ev.Cursor.Shapes {
			shapes = append(shapes, sh.ID)
		}
		last = ev.Cursor.State
		if last.X == 3 {
			t.Fatal("an update with a bad shape was delivered")
		}
	}
	if len(shapes) != 2 || shapes[0] != a.ID || shapes[1] != b.ID || last.ID != a.ID || last.Y != 8 {
		t.Fatalf("shapes %v, state %+v", shapes, last)
	}

	old, host2 := fakeHostSession(t, Options{}, Hello{})
	host2.Write(cursorFrame(t, cursorMsg{CursorState: CursorState{ID: a.ID, Visible: true}, Shape: a}))
	host2.Write(cursorFrame(t, cursorMsg{CursorState: CursorState{X: 1, Visible: true}}))
	if err := old.SetCursor(true); err == nil {
		t.Error("SetCursor on a session opened without Options.Cursor")
	}
	tctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	if ev, err := old.Next(tctx); err == nil {
		t.Fatalf("a client that did not ask got %+v", ev)
	}
	if err := old.Err(); err != nil {
		t.Fatalf("the session failed on a pointer frame: %v", err)
	}
}

// A Hello that says the pointer comes apart must say the screen size too.
func TestValidateHelloCursor(t *testing.T) {
	h := Hello{Width: 64, Height: 64, Codec: "av01.0.04M.08", Cursor: true}
	if err := validateHello(&h); err == nil || !strings.Contains(err.Error(), "screen size") {
		t.Fatalf("hello without the screen size: %v", err)
	}
	h.ScreenWidth, h.ScreenHeight = 640, 480
	if err := validateHello(&h); err != nil {
		t.Fatal(err)
	}
}

// Relative input goes only to a host that says it takes it: an older
// host would ignore a move_by and move the pointer to 0,0 for a button
// without a position.
func TestSendRelative(t *testing.T) {
	old, _ := fakeHostSession(t, Options{}, Hello{Input: true})
	for _, ev := range []InputEvent{{Kind: InputMoveBy, DX: 1}, {Kind: InputButton, Down: true, Rel: true}} {
		if err := old.SendInput(ev); err != ErrNoRelative {
			t.Errorf("%+v to an older host: %v", ev, err)
		}
	}
	if err := old.SendInput(InputEvent{Kind: InputMove, X: 0.5, Y: 0.5}); err != nil {
		t.Errorf("absolute move to an older host: %v", err)
	}
	cur, _ := fakeHostSession(t, Options{}, Hello{Input: true, Relative: true})
	if err := cur.SendInput(InputEvent{Kind: InputMoveBy, DX: 1}); err != nil {
		t.Errorf("move_by to a host that takes it: %v", err)
	}
}

// Old and new peers read each other's messages: an older host decodes a
// new client's Options and relative events without error (unknown fields
// are ignored, unknown control types dropped), and a new client reads an
// older host's Hello as one without the pointer and relative input.
func TestCursorWireCompat(t *testing.T) {
	type oldOptions struct {
		FPS     int  `json:"fps,omitempty"`
		Display int  `json:"display,omitempty"`
		Probe   bool `json:"probe,omitempty"`
	}
	b, _ := json.Marshal(Options{FPS: 30, Cursor: true})
	var oo oldOptions
	if err := json.Unmarshal(b, &oo); err != nil || oo.FPS != 30 {
		t.Fatalf("older host reading new options: %+v, %v", oo, err)
	}
	type oldControl struct {
		Type string  `json:"type"`
		DX   float64 `json:"dx,omitempty"`
	}
	b, _ = json.Marshal(InputEvent{Kind: InputButton, Rel: true}.control())
	var oc oldControl
	if err := json.Unmarshal(b, &oc); err != nil || oc.Type != InputButton {
		t.Fatalf("older host reading a relative button: %+v, %v", oc, err)
	}
	var h Hello
	if err := json.Unmarshal([]byte(`{"width":64,"height":64,"codec":"av01.0.04M.08","input":true}`), &h); err != nil {
		t.Fatal(err)
	}
	if h.Cursor || h.Relative {
		t.Fatalf("an older host's hello read as %+v", h)
	}
}
