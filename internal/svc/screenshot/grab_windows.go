//go:build windows

package screenshot

import (
	"context"
	"errors"
	"fmt"
	"image"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32 = windows.NewLazySystemDLL("user32.dll")
	gdi32  = windows.NewLazySystemDLL("gdi32.dll")

	procGetDC               = user32.NewProc("GetDC")
	procReleaseDC           = user32.NewProc("ReleaseDC")
	procGetSystemMetrics    = user32.NewProc("GetSystemMetrics")
	procEnumDisplayMonitors = user32.NewProc("EnumDisplayMonitors")
	procGetMonitorInfoW     = user32.NewProc("GetMonitorInfoW")
	procSetProcessDPIAware  = user32.NewProc("SetProcessDPIAware")
	procCreateCompatibleDC  = gdi32.NewProc("CreateCompatibleDC")
	procCreateCompatibleBmp = gdi32.NewProc("CreateCompatibleBitmap")
	procSelectObject        = gdi32.NewProc("SelectObject")
	procBitBlt              = gdi32.NewProc("BitBlt")
	procGetDIBits           = gdi32.NewProc("GetDIBits")
	procDeleteObject        = gdi32.NewProc("DeleteObject")
	procDeleteDC            = gdi32.NewProc("DeleteDC")
	dpiOnce                 sync.Once
)

const (
	smXVirtualScreen  = 76
	smYVirtualScreen  = 77
	smCXVirtualScreen = 78
	smCYVirtualScreen = 79

	srcCopy            = 0x00CC0020
	captureBlt         = 0x40000000
	monitorInfoPrimary = 1
	dibRGBColors       = 0
	biRGB              = 0
)

type rect struct{ left, top, right, bottom int32 }

type monitorInfo struct {
	size    uint32
	monitor rect
	work    rect
	flags   uint32
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

type winGrabber struct{}

func platformGrabber() grabber { return winGrabber{} }

func (winGrabber) Displays(ctx context.Context) ([]Display, error) {
	dpiOnce.Do(func() { procSetProcessDPIAware.Call() }) // report real pixels, not scaled ones
	var ds []Display
	cb := syscall.NewCallback(func(hmon, hdc, rc, data uintptr) uintptr {
		mi := monitorInfo{size: uint32(unsafe.Sizeof(monitorInfo{}))}
		if r, _, _ := procGetMonitorInfoW.Call(hmon, uintptr(unsafe.Pointer(&mi))); r != 0 {
			ds = append(ds, Display{
				X: int(mi.monitor.left), Y: int(mi.monitor.top),
				Width: int(mi.monitor.right - mi.monitor.left), Height: int(mi.monitor.bottom - mi.monitor.top),
				Primary: mi.flags&monitorInfoPrimary != 0,
			})
		}
		return 1
	})
	if r, _, err := procEnumDisplayMonitors.Call(0, 0, cb, 0); r == 0 {
		return nil, fmt.Errorf("screenshot: EnumDisplayMonitors: %v", err)
	}
	if len(ds) == 0 {
		return nil, errors.New("no display available on the host: Windows reports no monitors (is the daemon running in an interactive desktop session, not a service?)")
	}
	return orderDisplays(ds), nil
}

func (g winGrabber) Grab(ctx context.Context, display int) (image.Image, error) {
	dpiOnce.Do(func() { procSetProcessDPIAware.Call() })
	var x, y, w, h int
	if display == AllDisplays {
		x, y = metric(smXVirtualScreen), metric(smYVirtualScreen)
		w, h = metric(smCXVirtualScreen), metric(smCYVirtualScreen)
	} else {
		ds, err := g.Displays(ctx)
		if err != nil {
			return nil, err
		}
		if display >= len(ds) {
			return nil, fmt.Errorf("screenshot: no display %d (the host has %d)", display, len(ds))
		}
		d := ds[display]
		x, y, w, h = d.X, d.Y, d.Width, d.Height
	}
	if w <= 0 || h <= 0 {
		return nil, errors.New("no display available on the host: empty virtual screen")
	}
	return bitBlt(x, y, w, h)
}

func metric(i uintptr) int {
	r, _, _ := procGetSystemMetrics.Call(i)
	return int(int32(r))
}

// bitBlt copies a screen region through GDI and converts the BGRA DIB to RGBA.
func bitBlt(x, y, w, h int) (image.Image, error) {
	screen, _, err := procGetDC.Call(0)
	if screen == 0 {
		return nil, fmt.Errorf("screenshot: GetDC: %v", err)
	}
	defer procReleaseDC.Call(0, screen)
	mem, _, err := procCreateCompatibleDC.Call(screen)
	if mem == 0 {
		return nil, fmt.Errorf("screenshot: CreateCompatibleDC: %v", err)
	}
	defer procDeleteDC.Call(mem)
	bmp, _, err := procCreateCompatibleBmp.Call(screen, uintptr(w), uintptr(h))
	if bmp == 0 {
		return nil, fmt.Errorf("screenshot: CreateCompatibleBitmap: %v", err)
	}
	defer procDeleteObject.Call(bmp)
	old, _, _ := procSelectObject.Call(mem, bmp)
	defer procSelectObject.Call(mem, old)
	if r, _, err := procBitBlt.Call(mem, 0, 0, uintptr(w), uintptr(h), screen, uintptr(x), uintptr(y), srcCopy|captureBlt); r == 0 {
		return nil, fmt.Errorf("screenshot: BitBlt: %v", err)
	}
	bi := bitmapInfoHeader{size: uint32(unsafe.Sizeof(bitmapInfoHeader{})), width: int32(w), height: -int32(h), planes: 1, bits: 32, compression: biRGB}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	if r, _, err := procGetDIBits.Call(mem, bmp, 0, uintptr(h), uintptr(unsafe.Pointer(&img.Pix[0])), uintptr(unsafe.Pointer(&bi)), dibRGBColors); r == 0 {
		return nil, fmt.Errorf("screenshot: GetDIBits: %v", err)
	}
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+2], img.Pix[i+3] = img.Pix[i+2], img.Pix[i], 255
	}
	return img, nil
}
