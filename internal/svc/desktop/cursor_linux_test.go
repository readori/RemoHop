package desktop

import (
	"context"
	"testing"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"

	"github.com/readori/RemoHop/internal/peer/peertest"
)

// nextCursor waits for ok to pass: it is asked first with neither a
// pointer update nor a Hello (what is known already may do), then for each
// pointer update and Hello, skipping video.
func nextCursor(t *testing.T, ctx context.Context, s *Session, what string, ok func(*CursorUpdate, *Hello) bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if ok(nil, nil) {
		return
	}
	for {
		ev, err := s.Next(ctx)
		if err != nil {
			t.Fatalf("waiting for %s: %v", what, err)
		}
		if (ev.Cursor != nil || ev.Hello != nil) && ok(ev.Cursor, ev.Hello) {
			return
		}
	}
}

// setRootCursor shows a cursor on the root window: a glyph of the X cursor
// font, or an empty one (glyph < 0).
func setRootCursor(t *testing.T, x *xgb.Conn, glyph int) {
	t.Helper()
	root := xproto.Setup(x).DefaultScreen(x).Root
	cid, err := xproto.NewCursorId(x)
	if err != nil {
		t.Fatal(err)
	}
	if glyph >= 0 {
		font, _ := xproto.NewFontId(x)
		if err := xproto.OpenFontChecked(x, font, uint16(len("cursor")), "cursor").Check(); err != nil {
			t.Fatal(err)
		}
		err = xproto.CreateGlyphCursorChecked(x, cid, font, font, uint16(glyph), uint16(glyph+1), 0, 0, 0, 0xffff, 0xffff, 0xffff).Check()
	} else {
		pm, _ := xproto.NewPixmapId(x)
		xproto.CreatePixmap(x, 1, pm, xproto.Drawable(root), 1, 1)
		gc, _ := xproto.NewGcontextId(x)
		xproto.CreateGC(x, gc, xproto.Drawable(pm), xproto.GcForeground, []uint32{0})
		xproto.PolyFillRectangle(x, xproto.Drawable(pm), gc, []xproto.Rectangle{{Width: 1, Height: 1}})
		err = xproto.CreateCursorChecked(x, cid, pm, pm, 0, 0, 0, 0, 0, 0, 0, 0).Check()
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := xproto.ChangeWindowAttributesChecked(x, root, xproto.CwCursor, []uint32{uint32(cid)}).Check(); err != nil {
		t.Fatal(err)
	}
}

// The host leaves the pointer out of the video for a client that draws it
// and sends its shape, position and visibility instead: positions follow
// absolute and relative moves, a new pointer brings its shape, an empty
// one hides it, and the cursor control puts it back into the video. A
// client that does not ask (any older one) gets the pointer in the video.
func TestCursorXvfb(t *testing.T) {
	x := startXvfb(t, 640, 480)
	client, _ := peertest.Pair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	old, err := Open(ctx, client, Options{FPS: 15})
	if err != nil {
		t.Fatal(err)
	}
	if h := old.Hello(); h.Cursor || !h.Relative {
		t.Fatalf("hello for a client that draws no pointer: cursor %v, relative %v", h.Cursor, h.Relative)
	}
	old.Close()

	sess, err := Open(ctx, client, Options{FPS: 15, Cursor: true})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if h := sess.Hello(); !h.Cursor || h.ScreenWidth != 640 || h.ScreenHeight != 480 {
		t.Fatalf("hello: cursor %v, screen %dx%d", h.Cursor, h.ScreenWidth, h.ScreenHeight)
	}
	shapes := map[uint32]bool{}
	var state CursorState
	keep := func(u *CursorUpdate) {
		for _, s := range u.Shapes {
			shapes[s.ID] = true
		}
		state = u.State
	}
	nextCursor(t, ctx, sess, "the first pointer", func(u *CursorUpdate, _ *Hello) bool {
		if u != nil {
			keep(u)
		}
		return state.Visible && shapes[state.ID]
	})
	first := state.ID

	sess.SendInput(InputEvent{Kind: InputMove, X: 0.5, Y: 0.5})
	nextCursor(t, ctx, sess, "the pointer at 320,240", func(u *CursorUpdate, _ *Hello) bool {
		if u != nil {
			keep(u)
		}
		return state.X == 320 && state.Y == 240
	})
	sess.SendInput(InputEvent{Kind: InputMoveBy, DX: 3, DY: -2})
	nextCursor(t, ctx, sess, "the pointer moved by 3,-2", func(u *CursorUpdate, _ *Hello) bool {
		if u != nil {
			keep(u)
		}
		return state.X == 323 && state.Y == 238
	})

	setRootCursor(t, x, 68) // left_ptr
	nextCursor(t, ctx, sess, "another shape", func(u *CursorUpdate, _ *Hello) bool {
		if u != nil {
			keep(u)
		}
		return state.Visible && state.ID != first && shapes[state.ID]
	})
	setRootCursor(t, x, -1)
	nextCursor(t, ctx, sess, "the pointer hidden", func(u *CursorUpdate, _ *Hello) bool {
		if u != nil {
			keep(u)
		}
		return !state.Visible
	})

	if err := sess.SetCursor(false); err != nil {
		t.Fatal(err)
	}
	nextCursor(t, ctx, sess, "a hello with the pointer in the video", func(_ *CursorUpdate, h *Hello) bool {
		return h != nil && !h.Cursor
	})
	if err := sess.SetCursor(true); err != nil {
		t.Fatal(err)
	}
	nextCursor(t, ctx, sess, "a hello with the pointer apart again", func(_ *CursorUpdate, h *Hello) bool {
		return h != nil && h.Cursor
	})
}

// The pointer goes back into the video when it cannot be read, so a host
// without a working pointer reader still shows one.
func TestCursorFallbackXvfb(t *testing.T) {
	startXvfb(t, 320, 240)
	orig := newCursorSource
	t.Cleanup(func() { newCursorSource = orig })
	src := &fakeCursor{imgs: map[uint64]cursorImage{}}
	opened := make(chan struct{}, 4)
	newCursorSource = func(captureSpec) (cursorSource, error) {
		opened <- struct{}{}
		return src, nil
	}
	src.err = context.DeadlineExceeded // every read fails
	client, _ := peertest.Pair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sess, err := Open(ctx, client, Options{FPS: 15, Cursor: true})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if !sess.Hello().Cursor {
		t.Fatal("the pointer was not taken out of the video")
	}
	start := time.Now()
	nextCursor(t, ctx, sess, "a hello with the pointer back in the video", func(_ *CursorUpdate, h *Hello) bool {
		return h != nil && !h.Cursor
	})
	if d := time.Since(start); d < cursorFailAfter/2 {
		t.Errorf("fell back after %v", d)
	}
	<-opened
}
