package desktop

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xfixes"
	"github.com/jezek/xgb/xproto"
)

func init() { newCursorSource = newX11Cursor }

// x11Cursor reads the X pointer on a connection of its own: the position
// with QueryPointer, the image with XFixes GetCursorImage when the server
// reports another pointer (CursorNotify), which it identifies by the
// cursor's serial number. X has no "hidden" state an application sets;
// applications hide the pointer by showing an empty one, which the tracker
// recognises.
type x11Cursor struct {
	c      *xgb.Conn
	root   xproto.Window
	serial uint32
}

func newX11Cursor(spec captureSpec) (cursorSource, error) {
	if spec.x11 == nil {
		return nil, errors.New("no X display")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := spec.x11.Open(ctx)
	if err != nil {
		return nil, fmt.Errorf("connect to %w", err)
	}
	fail := func(what string, err error) (cursorSource, error) {
		c.Close()
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	if err := xfixes.Init(c); err != nil {
		return fail("X server has no XFIXES extension", err)
	}
	if _, err := xfixes.QueryVersion(c, 4, 0).Reply(); err != nil {
		return fail("XFIXES version", err)
	}
	x := &x11Cursor{c: c, root: xproto.Setup(c).DefaultScreen(c).Root}
	if err := xfixes.SelectCursorInputChecked(c, x.root, xfixes.CursorNotifyMaskDisplayCursor).Check(); err != nil {
		return fail("watch the pointer", err)
	}
	r, err := xfixes.GetCursorImage(c).Reply()
	if err != nil {
		return fail("read the pointer", err)
	}
	x.serial = r.CursorSerial
	return x, nil
}

func (x *x11Cursor) read() (cursorReading, error) {
	for {
		ev, xerr := x.c.PollForEvent()
		if ev == nil && xerr == nil {
			break
		}
		if n, ok := ev.(xfixes.CursorNotifyEvent); ok {
			x.serial = n.CursorSerial
		}
	}
	p, err := xproto.QueryPointer(x.c, x.root).Reply()
	if err != nil {
		return cursorReading{}, fmt.Errorf("query pointer: %w", err)
	}
	return cursorReading{x: int(p.RootX), y: int(p.RootY), visible: true, shape: uint64(x.serial)}, nil
}

func (x *x11Cursor) image() (cursorImage, error) {
	r, err := xfixes.GetCursorImage(x.c).Reply()
	if err != nil {
		return cursorImage{}, fmt.Errorf("read the pointer image: %w", err)
	}
	w, h := int(r.Width), int(r.Height)
	if len(r.CursorImage) < w*h {
		return cursorImage{}, errors.New("short pointer image")
	}
	return cursorImage{w: w, h: h, hx: int(r.Xhot), hy: int(r.Yhot), shape: uint64(r.CursorSerial),
		pix: premultipliedToStraight(r.CursorImage[:w*h])}, nil
}

func (x *x11Cursor) close() { x.c.Close() }
