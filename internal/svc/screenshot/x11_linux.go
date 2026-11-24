//go:build linux

package screenshot

import (
	"context"
	"errors"
	"fmt"
	"image"
	"math/bits"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/randr"
	"github.com/jezek/xgb/xinerama"
	"github.com/jezek/xgb/xproto"
)

// x11 is an open connection plus what is needed to read the root window.
type x11 struct {
	c       *xgb.Conn
	root    xproto.Window
	width   int
	height  int
	depth   byte
	bpp     int
	pad     int
	lsb     bool
	r, g, b uint32 // visual channel masks
}

func (x *x11) close() { x.c.Close() }

func newX11(c *xgb.Conn, screenIdx int) (*x11, error) {
	setup := xproto.Setup(c)
	if screenIdx >= len(setup.Roots) {
		screenIdx = c.DefaultScreen
	}
	if screenIdx < 0 || screenIdx >= len(setup.Roots) {
		c.Close()
		return nil, fmt.Errorf("X server has no screen %d", screenIdx)
	}
	scr := setup.Roots[screenIdx]
	x := &x11{c: c, root: scr.Root, width: int(scr.WidthInPixels), height: int(scr.HeightInPixels),
		depth: scr.RootDepth, lsb: setup.ImageByteOrder == xproto.ImageOrderLSBFirst}

	found := false
	for _, f := range setup.PixmapFormats {
		if f.Depth == scr.RootDepth {
			x.bpp, x.pad, found = int(f.BitsPerPixel), int(f.ScanlinePad), true
		}
	}
	if !found {
		c.Close()
		return nil, fmt.Errorf("X server has no pixmap format for depth %d", scr.RootDepth)
	}
	var vis *xproto.VisualInfo
	for _, d := range scr.AllowedDepths {
		for i := range d.Visuals {
			if d.Visuals[i].VisualId == scr.RootVisual {
				vis = &d.Visuals[i]
			}
		}
	}
	if vis == nil || (vis.Class != xproto.VisualClassTrueColor && vis.Class != xproto.VisualClassDirectColor) {
		c.Close()
		return nil, fmt.Errorf("unsupported X visual (depth %d, not TrueColor)", scr.RootDepth)
	}
	if x.bpp != 16 && x.bpp != 24 && x.bpp != 32 {
		c.Close()
		return nil, fmt.Errorf("unsupported X pixel size: %d bits per pixel", x.bpp)
	}
	x.r, x.g, x.b = vis.RedMask, vis.GreenMask, vis.BlueMask
	return x, nil
}

// monitors lists the screen's monitors, primary first.
func (x *x11) monitors() []Display { return x11Monitors(x.c, x.root, x.width, x.height) }

// X11Displays lists the monitors of c's default screen exactly as the
// screenshot service numbers them, so other services (input) can address
// the same pixels on a connection they already hold.
func X11Displays(c *xgb.Conn) []Display {
	scr := xproto.Setup(c).DefaultScreen(c)
	return x11Monitors(c, scr.Root, int(scr.WidthInPixels), int(scr.HeightInPixels))
}

// x11Monitors lists a screen's monitors, primary first. RandR 1.5 is
// preferred, Xinerama is the fallback and the root window size the last
// resort (single monitor).
func x11Monitors(c *xgb.Conn, root xproto.Window, width, height int) []Display {
	var ds []Display
	if err := randrInit(c); err == nil {
		if rep, err := randr.GetMonitors(c, root, true).Reply(); err == nil {
			for _, m := range rep.Monitors {
				if m.Width == 0 || m.Height == 0 {
					continue
				}
				d := Display{X: int(m.X), Y: int(m.Y), Width: int(m.Width), Height: int(m.Height), Primary: m.Primary}
				if nm, err := xproto.GetAtomName(c, m.Name).Reply(); err == nil {
					d.Name = nm.Name
				}
				ds = append(ds, d)
			}
		}
	}
	if len(ds) == 0 {
		if err := xinerama.Init(c); err == nil {
			if rep, err := xinerama.QueryScreens(c).Reply(); err == nil {
				for i, s := range rep.ScreenInfo {
					if s.Width == 0 || s.Height == 0 {
						continue
					}
					ds = append(ds, Display{X: int(s.XOrg), Y: int(s.YOrg), Width: int(s.Width), Height: int(s.Height), Primary: i == 0})
				}
			}
		}
	}
	if len(ds) == 0 {
		ds = []Display{{Width: width, Height: height, Primary: true}}
	}
	return orderDisplays(ds)
}

// randrInit enables RandR requests on c. randr.Init would also register
// RandR's event decoders in xgb's process-wide tables: a map write racing
// with every other X connection of this process that is decoding events
// (the clipboard, desktop and input services hold some), which the Go
// runtime may turn into a fatal error. Listing monitors needs no RandR
// events, so only the extension's opcode is recorded on c.
func randrInit(c *xgb.Conn) error {
	rep, err := xproto.QueryExtension(c, 5, "RANDR").Reply()
	if err != nil {
		return err
	}
	if !rep.Present {
		return errors.New("no RANDR extension")
	}
	c.ExtLock.Lock()
	c.Extensions["RANDR"] = rep.MajorOpcode
	c.ExtLock.Unlock()
	return nil
}

// grab reads rect (clipped to the root window) in horizontal bands so a 4K
// screen never needs one giant reply.
func (x *x11) grab(ctx context.Context, rect image.Rectangle) (*image.RGBA, error) {
	rect = rect.Intersect(image.Rect(0, 0, x.width, x.height))
	if rect.Empty() {
		return nil, errors.New("screenshot: display lies outside the screen")
	}
	stop := context.AfterFunc(ctx, x.close) // unblocks a stuck reply
	defer stop()

	w, h := rect.Dx(), rect.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	rowBytes := (w*x.bpp + x.pad - 1) / x.pad * x.pad / 8
	band := max(1, (4<<20)/rowBytes)
	for y := 0; y < h; y += band {
		bh := min(band, h-y)
		rep, err := xproto.GetImage(x.c, xproto.ImageFormatZPixmap, xproto.Drawable(x.root),
			int16(rect.Min.X), int16(rect.Min.Y+y), uint16(w), uint16(bh), 0xffffffff).Reply()
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("screenshot: X11 GetImage: %w", err)
		}
		if len(rep.Data) < rowBytes*bh {
			return nil, errors.New("screenshot: X11 returned a short image")
		}
		x.convert(dst, y, rep.Data, w, bh, rowBytes)
	}
	return dst, nil
}

// convert writes bh rows of ZPixmap data into dst starting at row y0,
// honouring the server's byte order and the visual's channel masks.
func (x *x11) convert(dst *image.RGBA, y0 int, data []byte, w, bh, rowBytes int) {
	bytesPP := x.bpp / 8
	fast := x.bpp == 32 && x.lsb && x.r == 0xff0000 && x.g == 0xff00 && x.b == 0xff
	rs, gs, bs := shiftOf(x.r), shiftOf(x.g), shiftOf(x.b)
	rm, gm, bm := maxOf(x.r), maxOf(x.g), maxOf(x.b)
	scale := func(v, m uint32) uint8 {
		if m == 255 {
			return uint8(v)
		}
		return uint8((v*255 + m/2) / m)
	}
	for y := range bh {
		src := data[y*rowBytes:]
		out := dst.Pix[(y0+y)*dst.Stride:]
		if fast {
			for i := range w {
				out[i*4], out[i*4+1], out[i*4+2], out[i*4+3] = src[i*4+2], src[i*4+1], src[i*4], 255
			}
			continue
		}
		for i := range w {
			var p uint32
			px := src[i*bytesPP : i*bytesPP+bytesPP]
			if x.lsb {
				for k := bytesPP - 1; k >= 0; k-- {
					p = p<<8 | uint32(px[k])
				}
			} else {
				for k := range bytesPP {
					p = p<<8 | uint32(px[k])
				}
			}
			out[i*4] = scale((p&x.r)>>rs, rm)
			out[i*4+1] = scale((p&x.g)>>gs, gm)
			out[i*4+2] = scale((p&x.b)>>bs, bm)
			out[i*4+3] = 255
		}
	}
}

func shiftOf(mask uint32) uint {
	if mask == 0 {
		return 0
	}
	return uint(bits.TrailingZeros32(mask))
}

// maxOf is the largest value a channel mask can hold (at least 1, so a
// zero mask cannot divide by zero).
func maxOf(mask uint32) uint32 {
	n := bits.OnesCount32(mask)
	if n == 0 {
		return 1
	}
	return 1<<n - 1
}
