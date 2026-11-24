package unattended

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32 = windows.NewLazySystemDLL("user32.dll")
	gdi32  = windows.NewLazySystemDLL("gdi32.dll")

	procOpenInputDesktop    = user32.NewProc("OpenInputDesktop")
	procSetThreadDesktop    = user32.NewProc("SetThreadDesktop")
	procCloseDesktop        = user32.NewProc("CloseDesktop")
	procGetUserObjectInfoW  = user32.NewProc("GetUserObjectInformationW")
	procSetProcessDPIAware  = user32.NewProc("SetProcessDPIAware")
	procLockWorkStation     = user32.NewProc("LockWorkStation")
	procGetDC               = user32.NewProc("GetDC")
	procReleaseDC           = user32.NewProc("ReleaseDC")
	procGetCursorInfo       = user32.NewProc("GetCursorInfo")
	procGetIconInfo         = user32.NewProc("GetIconInfo")
	procDrawIconEx          = user32.NewProc("DrawIconEx")
	procCreateCompatibleDC  = gdi32.NewProc("CreateCompatibleDC")
	procCreateCompatibleBmp = gdi32.NewProc("CreateCompatibleBitmap")
	procSelectObject        = gdi32.NewProc("SelectObject")
	procBitBlt              = gdi32.NewProc("BitBlt")
	procGetDIBits           = gdi32.NewProc("GetDIBits")
	procDeleteObject        = gdi32.NewProc("DeleteObject")
	procDeleteDC            = gdi32.NewProc("DeleteDC")
)

const (
	// desktopAccess is what a thread needs to capture and inject on a
	// desktop: read and write objects, enumerate, create windows and
	// menus (some GDI and input paths ask for it), hooks, switch.
	desktopAccess = 0x0001 | 0x0002 | 0x0004 | 0x0040 | 0x0080 | 0x0008 | 0x0100 | windows.GENERIC_WRITE

	uoiName       = 2
	srcCopy       = 0x00CC0020
	captureBlt    = 0x40000000
	cursorShowing = 0x00000001
	diNormal      = 0x0003
)

var errThreadDone = errors.New("unattended: desktop thread stopped")

// deskThread is an OS thread that attaches itself to the input desktop
// before each job, so that GDI capture and SendInput act on whatever the
// user sees: the Default desktop, Winlogon (sign-in, lock screen, UAC) or
// the screen saver. SetThreadDesktop works only on a thread without windows
// or hooks; this one never creates any. The thread is never unlocked from
// its goroutine, so it ends with it.
type deskThread struct {
	jobs     chan func()
	quit     chan struct{}
	stopOnce sync.Once

	// Owned by the thread.
	cur  uintptr // desktop handle set on the thread; 0 = the one it started on
	name string
	gen  int   // incremented on every switch
	err  error // why the input desktop could not be opened last time
	note func(name string)
}

// startDeskThread starts a thread; note is told the name of each desktop
// the thread moves to.
func startDeskThread(note func(name string)) *deskThread {
	d := &deskThread{jobs: make(chan func()), quit: make(chan struct{}), note: note}
	go func() {
		runtime.LockOSThread()
		for {
			select {
			case f := <-d.jobs:
				f()
			case <-d.quit:
				if d.cur != 0 {
					procCloseDesktop.Call(d.cur)
				}
				return
			}
		}
	}()
	return d
}

func (d *deskThread) stop() { d.stopOnce.Do(func() { close(d.quit) }) }

// do runs f on the thread, attached to the current input desktop.
func (d *deskThread) do(f func() error) error {
	errc := make(chan error, 1)
	job := func() {
		defer func() {
			if r := recover(); r != nil {
				errc <- fmt.Errorf("unattended: panic: %v", r)
			}
		}()
		d.follow()
		errc <- f()
	}
	select {
	case d.jobs <- job:
	case <-d.quit:
		return errThreadDone
	}
	return <-errc
}

// follow attaches the thread to the input desktop if it is not on it.
func (d *deskThread) follow() {
	h, _, err := procOpenInputDesktop.Call(0, 0, desktopAccess)
	if h == 0 {
		// While the session is disconnected, or during a switch, there
		// is no input desktop to open; stay where we are.
		d.err = fmt.Errorf("OpenInputDesktop: %v", err)
		return
	}
	d.err = nil
	name := objectName(h)
	if name == d.name && d.cur != 0 {
		procCloseDesktop.Call(h)
		return
	}
	if r, _, err := procSetThreadDesktop.Call(h); r == 0 {
		procCloseDesktop.Call(h)
		d.err = fmt.Errorf("SetThreadDesktop(%s): %v", name, err)
		return
	}
	if d.cur != 0 {
		procCloseDesktop.Call(d.cur)
	}
	d.cur, d.name = h, name
	d.gen++
	if d.note != nil {
		d.note(name)
	}
}

func objectName(h uintptr) string {
	var buf [256]uint16
	var n uint32
	if r, _, _ := procGetUserObjectInfoW.Call(h, uoiName, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)*2), uintptr(unsafe.Pointer(&n))); r == 0 {
		return ""
	}
	return windows.UTF16ToString(buf[:])
}

type bitmapInfoHeader struct {
	size          uint32
	width, height int32
	planes, bits  uint16
	compression   uint32
	sizeImage     uint32
	xPels, yPels  int32
	clrUsed       uint32
	clrImportant  uint32
}

type cursorInfo struct {
	size   uint32
	flags  uint32
	cursor uintptr
	x, y   int32
}

type iconInfo struct {
	isIcon     int32
	hotX, hotY uint32
	mask       uintptr
	color      uintptr
}

// gdiCapture copies screen areas with BitBlt on its desk thread. It keeps
// its memory DC and bitmap between frames of one size on one desktop.
type gdiCapture struct {
	mem, bmp uintptr
	w, h     int
	gen      int
}

func (g *gdiCapture) free() {
	if g.bmp != 0 {
		procDeleteObject.Call(g.bmp)
		g.bmp = 0
	}
	if g.mem != 0 {
		procDeleteDC.Call(g.mem)
		g.mem = 0
	}
}

// grab copies r into dst as 32-bit BGRX pixels, top row first, drawing the
// pointer if cursor is set. It must run on d's thread (inside d.do).
func (g *gdiCapture) grab(d *deskThread, r rect, cursor bool, dst []byte) error {
	if len(dst) != r.W*r.H*4 || r.W <= 0 || r.H <= 0 {
		return fmt.Errorf("capture of %dx%d into %d bytes", r.W, r.H, len(dst))
	}
	screen, _, err := procGetDC.Call(0)
	if screen == 0 {
		return fmt.Errorf("GetDC: %v (%v)", err, d.err)
	}
	defer procReleaseDC.Call(0, screen)
	if g.mem == 0 || g.w != r.W || g.h != r.H || g.gen != d.gen {
		g.free()
		if g.mem, _, err = procCreateCompatibleDC.Call(screen); g.mem == 0 {
			return fmt.Errorf("CreateCompatibleDC: %v", err)
		}
		if g.bmp, _, err = procCreateCompatibleBmp.Call(screen, uintptr(r.W), uintptr(r.H)); g.bmp == 0 {
			g.free()
			return fmt.Errorf("CreateCompatibleBitmap: %v", err)
		}
		g.w, g.h, g.gen = r.W, r.H, d.gen
	}
	old, _, _ := procSelectObject.Call(g.mem, g.bmp)
	ok, _, err := procBitBlt.Call(g.mem, 0, 0, uintptr(r.W), uintptr(r.H), screen, uintptr(r.X), uintptr(r.Y), srcCopy|captureBlt)
	if ok == 0 {
		procSelectObject.Call(g.mem, old)
		if d.err != nil {
			return fmt.Errorf("BitBlt: %v (%v)", err, d.err)
		}
		return fmt.Errorf("BitBlt: %v", err)
	}
	if cursor {
		drawCursor(g.mem, r)
	}
	// GetDIBits wants the bitmap not selected into a DC.
	procSelectObject.Call(g.mem, old)
	bi := bitmapInfoHeader{size: uint32(unsafe.Sizeof(bitmapInfoHeader{})), width: int32(r.W), height: -int32(r.H), planes: 1, bits: 32}
	if n, _, err := procGetDIBits.Call(g.mem, g.bmp, 0, uintptr(r.H), uintptr(unsafe.Pointer(&dst[0])), uintptr(unsafe.Pointer(&bi)), 0); n == 0 {
		return fmt.Errorf("GetDIBits: %v", err)
	}
	return nil
}

// drawCursor paints the pointer onto the captured picture: GDI captures
// do not include it.
func drawCursor(dc uintptr, r rect) {
	ci := cursorInfo{size: uint32(unsafe.Sizeof(cursorInfo{}))}
	if ok, _, _ := procGetCursorInfo.Call(uintptr(unsafe.Pointer(&ci))); ok == 0 || ci.flags&cursorShowing == 0 || ci.cursor == 0 {
		return
	}
	var ii iconInfo
	if ok, _, _ := procGetIconInfo.Call(ci.cursor, uintptr(unsafe.Pointer(&ii))); ok == 0 {
		return
	}
	if ii.mask != 0 {
		procDeleteObject.Call(ii.mask)
	}
	if ii.color != 0 {
		procDeleteObject.Call(ii.color)
	}
	x := int(ci.x) - int(ii.hotX) - r.X
	y := int(ci.y) - int(ii.hotY) - r.Y
	procDrawIconEx.Call(dc, uintptr(int32(x)), uintptr(int32(y)), ci.cursor, 0, 0, 0, 0, diNormal)
}
