package desktop

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/readori/RemoHop/internal/dpiaware"
	"github.com/readori/RemoHop/internal/unattended"
)

var (
	procGetCursorInfo = user32.NewProc("GetCursorInfo")
	procGetIconInfo   = user32.NewProc("GetIconInfo")
	procGetDC         = user32.NewProc("GetDC")
	procReleaseDC     = user32.NewProc("ReleaseDC")
	gdi32             = windows.NewLazySystemDLL("gdi32.dll")
	procGetDIBits     = gdi32.NewProc("GetDIBits")
	procGetObjectW    = gdi32.NewProc("GetObjectW")
	procDeleteObject  = gdi32.NewProc("DeleteObject")
)

func init() { newCursorSource = newWinCursor }

// CURSORINFO, ICONINFO, BITMAP and BITMAPINFOHEADER.
type winCursorInfo struct {
	size, flags uint32
	cursor      uintptr
	x, y        int32
}

type winIconInfo struct {
	icon             int32
	hotX, hotY       uint32
	hbmMask, hbmColr uintptr
}

type winBitmap struct {
	typ, width, height, widthBytes int32
	planes, bitsPixel              uint16
	bits                           uintptr
}

type winBitmapInfo struct {
	size                 uint32
	width, height        int32
	planes, bitCount     uint16
	compression, sizeImg uint32
	xPels, yPels         int32
	clrUsed, clrImp      uint32
	colors               [256]uint32 // room for a colour table GetDIBits may write
}

const cursorShowing = 1 // CURSOR_SHOWING

// winCursor reads the pointer with GetCursorInfo (position, handle,
// whether it shows) and its image with GetIconInfo and GetDIBits when the
// handle changes. The host runs DPI aware (dpiaware), so the position is
// in physical pixels of the virtual desktop, as the capture area is.
// Pointer images are as large as Windows made them, which follows the
// pointer size setting and the display scale.
type winCursor struct {
	cur uintptr
}

func newWinCursor(spec captureSpec) (cursorSource, error) {
	if unattended.Active() {
		// The service runs in session 0; the pointer is the console
		// session's, which only its helper sees.
		return nil, errors.New("the pointer of the console session cannot be read from the service")
	}
	dpiaware.Enable()
	w := &winCursor{}
	if _, err := w.read(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *winCursor) read() (cursorReading, error) {
	ci := winCursorInfo{size: uint32(unsafe.Sizeof(winCursorInfo{}))}
	if r, _, err := procGetCursorInfo.Call(uintptr(unsafe.Pointer(&ci))); r == 0 {
		// Also while the secure desktop has the input.
		return cursorReading{}, fmt.Errorf("GetCursorInfo: %v", err)
	}
	w.cur = ci.cursor
	return cursorReading{x: int(ci.x), y: int(ci.y), visible: ci.flags&cursorShowing != 0 && ci.cursor != 0, shape: uint64(ci.cursor)}, nil
}

func (w *winCursor) image() (cursorImage, error) {
	var ii winIconInfo
	if r, _, err := procGetIconInfo.Call(w.cur, uintptr(unsafe.Pointer(&ii))); r == 0 {
		return cursorImage{}, fmt.Errorf("GetIconInfo: %v", err)
	}
	defer procDeleteObject.Call(ii.hbmMask)
	if ii.hbmColr != 0 {
		defer procDeleteObject.Call(ii.hbmColr)
	}
	dc, _, err := procGetDC.Call(0)
	if dc == 0 {
		return cursorImage{}, fmt.Errorf("GetDC: %v", err)
	}
	defer procReleaseDC.Call(0, dc)
	mw, mh, mask, err := dibBits(dc, ii.hbmMask)
	if err != nil {
		return cursorImage{}, err
	}
	var color []byte
	w0, h0 := mw, mh/2 // monochrome: the AND mask above the XOR mask
	if ii.hbmColr != 0 {
		if w0, h0, color, err = dibBits(dc, ii.hbmColr); err != nil {
			return cursorImage{}, err
		}
	}
	pix, err := dibCursor(w0, h0, mask, color)
	if err != nil {
		return cursorImage{}, err
	}
	return cursorImage{w: w0, h: h0, hx: int(ii.hotX), hy: int(ii.hotY), pix: pix}, nil
}

func (w *winCursor) close() {}

// dibBits reads a bitmap as 32-bit top-down BGRA pixels.
func dibBits(dc, hbm uintptr) (w, h int, pix []byte, err error) {
	var bm winBitmap
	if r, _, err := procGetObjectW.Call(hbm, unsafe.Sizeof(bm), uintptr(unsafe.Pointer(&bm))); r == 0 {
		return 0, 0, nil, fmt.Errorf("GetObject: %v", err)
	}
	w, h = int(bm.width), int(bm.height)
	if w <= 0 || h <= 0 || w > 2*maxCursorSide || h > 4*maxCursorSide {
		return 0, 0, nil, fmt.Errorf("pointer bitmap of %dx%d is out of range", w, h)
	}
	bi := winBitmapInfo{width: int32(w), height: -int32(h), planes: 1, bitCount: 32}
	bi.size = uint32(unsafe.Offsetof(bi.colors))
	pix = make([]byte, w*h*4)
	if r, _, err := procGetDIBits.Call(dc, hbm, 0, uintptr(h), uintptr(unsafe.Pointer(&pix[0])), uintptr(unsafe.Pointer(&bi)), 0); r == 0 {
		return 0, 0, nil, fmt.Errorf("GetDIBits: %v", err)
	}
	return w, h, pix, nil
}
