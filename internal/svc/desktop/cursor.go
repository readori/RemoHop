package desktop

import (
	"bytes"
	"errors"
	"fmt"
	"hash/fnv"
	"image"
	"image/png"
	"log/slog"
	"slices"
	"time"
)

// The pointer drawn by the client (see "The pointer" in protocol.go): the
// host reads its mouse pointer with a platform cursorSource (cursor_*.go)
// and a cursorTracker turns what it reads into 'C' frames.

// CursorShapesKept is how many pointer shapes a client must keep, by ID:
// the host sends a shape again once it has sent this many others since.
const CursorShapesKept = 32

// Bounds of a pointer image the host sends and a client accepts.
const (
	maxCursorSide = 256
	maxCursorPNG  = 256 << 10
)

// CursorShape is one image of the host's mouse pointer.
type CursorShape struct {
	// ID names the shape for the rest of the session; it is never 0.
	ID     uint32 `json:"id"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	// HotX, HotY is the hotspot: the pixel of the image that is the
	// pointer's position.
	HotX int `json:"hot_x"`
	HotY int `json:"hot_y"`
	// PNG is the image, Width x Height host pixels, with alpha.
	PNG []byte `json:"png"`
}

// CursorState is where the host's pointer is and how it looks.
type CursorState struct {
	// ID is the current shape; 0 when it is unknown (draw a default
	// arrow) or the pointer is hidden.
	ID uint32 `json:"id"`
	// X, Y is the hotspot's position in host pixels from the captured
	// area's top-left (Hello.ScreenWidth x ScreenHeight). It lies outside
	// that area when the pointer is on another monitor.
	X int `json:"x"`
	Y int `json:"y"`
	// Visible is false while the pointer is hidden: a game or a video
	// player hid it, or an application shows an empty pointer.
	Visible bool `json:"visible"`
}

// cursorMsg is the body of a 'C' frame: the pointer's state, with the
// shape it names when the client does not have that yet.
type cursorMsg struct {
	CursorState
	Shape *CursorShape `json:"shape,omitempty"`
}

// CursorUpdate is what changed about the host's pointer (Event.Cursor).
type CursorUpdate struct {
	// Shapes are images not delivered before, oldest first. Keep at
	// least the last CursorShapesKept of them: states name them by ID.
	Shapes []CursorShape
	// State is the latest position, shape and visibility.
	State CursorState
}

// cursorReading is one look at the host's pointer.
type cursorReading struct {
	// x, y is the hotspot in screen pixels (the coordinates the capture
	// area is given in).
	x, y    int
	visible bool
	// shape identifies the current image: it changes whenever the image
	// may have (a cursor handle, an XFixes serial).
	shape uint64
}

// cursorImage is a pointer image: straight-alpha RGBA, w*h*4 bytes.
type cursorImage struct {
	w, h, hx, hy int
	pix          []byte
	// shape, when not 0, is the shape key of this image, which may be
	// newer than the one the last read reported.
	shape uint64
}

// cursorSource reads the host's pointer.
type cursorSource interface {
	read() (cursorReading, error)
	// image is the current pointer image (the one read's shape names).
	image() (cursorImage, error)
	close()
}

// newCursorSource opens the platform's pointer reader for a capture, or
// is nil where there is none (the pointer then stays in the video).
var newCursorSource func(spec captureSpec) (cursorSource, error)

// cursorTracker turns readings into 'C' frames: one only when something
// changed, with a shape only when the client does not have it.
type cursorTracker struct {
	src cursorSource

	shape  uint64 // the source's shape key of cur
	haveIt bool   // cur is the image of shape
	cur    loadedShape

	// shapes are encoded images by source key, so that a pointer that
	// comes back (the arrow after the text I-beam) is not encoded again.
	shapes map[uint64]loadedShape
	// sent are the IDs of the shapes the client has, oldest first.
	sent []uint32

	last   CursorState
	lastOK bool
}

// loadedShape is a pointer image as the tracker keeps it.
type loadedShape struct {
	s     *CursorShape // nil: blank, or too large to send
	blank bool         // fully transparent: the pointer is hidden
}

func newCursorTracker(src cursorSource) *cursorTracker {
	return &cursorTracker{src: src, shapes: map[uint64]loadedShape{}}
}

// poll reads the pointer and returns the message to send, nil if nothing
// changed. ox, oy is the captured area's top-left on the screen.
func (t *cursorTracker) poll(ox, oy int) (*cursorMsg, error) {
	r, err := t.src.read()
	if err != nil {
		return nil, err
	}
	if r.visible && (!t.haveIt || r.shape != t.shape) {
		t.load(r.shape)
	}
	st := CursorState{X: r.x - ox, Y: r.y - oy}
	known := t.haveIt && t.shape == r.shape
	switch {
	case !r.visible:
	case known && t.cur.blank:
		// An empty image: the application hid the pointer.
	case known && t.cur.s != nil:
		st.Visible, st.ID = true, t.cur.s.ID
	default:
		st.Visible = true // an image that cannot be read: a default arrow
	}
	if t.lastOK && st == t.last {
		return nil, nil
	}
	m := &cursorMsg{CursorState: st}
	if st.ID != 0 && !slices.Contains(t.sent, st.ID) {
		m.Shape = t.cur.s
	}
	return m, nil
}

// load reads and encodes the image for the source's shape key k.
func (t *cursorTracker) load(k uint64) {
	if l, ok := t.shapes[k]; ok {
		t.shape, t.haveIt, t.cur = k, true, l
		return
	}
	img, err := t.src.image()
	if err != nil {
		// Tried again at the next poll; meanwhile a default arrow.
		t.haveIt = false
		return
	}
	if img.shape != 0 {
		k = img.shape
	}
	var l loadedShape
	l.s, err = encodeCursor(img)
	l.blank = err == errBlankCursor
	if len(t.shapes) >= 2*CursorShapesKept {
		clear(t.shapes)
	}
	t.shapes[k] = l
	t.shape, t.haveIt, t.cur = k, true, l
}

// commit records that m is on its way to the client.
func (t *cursorTracker) commit(m *cursorMsg) {
	t.last, t.lastOK = m.CursorState, true
	if m.Shape != nil && !slices.Contains(t.sent, m.Shape.ID) {
		t.sent = append(t.sent, m.Shape.ID)
		if len(t.sent) > CursorShapesKept {
			t.sent = slices.Delete(t.sent, 0, len(t.sent)-CursorShapesKept)
		}
	}
}

// resend makes the next poll send the state even if it did not change.
func (t *cursorTracker) resend() { t.lastOK = false }

var errBlankCursor = errors.New("the pointer image is blank")

// encodeCursor makes a shape of an image: a PNG and an ID derived from
// the pixels and the hotspot, so that equal images get equal IDs.
func encodeCursor(img cursorImage) (*CursorShape, error) {
	if img.w <= 0 || img.h <= 0 || img.w > maxCursorSide || img.h > maxCursorSide || len(img.pix) != img.w*img.h*4 {
		return nil, fmt.Errorf("pointer image of %dx%d is out of range", img.w, img.h)
	}
	blank := true
	for i := 3; i < len(img.pix); i += 4 {
		if img.pix[i] != 0 {
			blank = false
			break
		}
	}
	if blank {
		return nil, errBlankCursor
	}
	hx, hy := min(max(img.hx, 0), img.w-1), min(max(img.hy, 0), img.h-1)
	f := fnv.New32a()
	fmt.Fprintf(f, "%d %d %d %d ", img.w, img.h, hx, hy)
	f.Write(img.pix)
	id := f.Sum32()
	if id == 0 {
		id = 1
	}
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := enc.Encode(&buf, &image.NRGBA{Pix: img.pix, Stride: img.w * 4, Rect: image.Rect(0, 0, img.w, img.h)}); err != nil {
		return nil, err
	}
	if buf.Len() > maxCursorPNG {
		return nil, errors.New("the pointer image is too large")
	}
	return &CursorShape{ID: id, Width: img.w, Height: img.h, HotX: hx, HotY: hy, PNG: buf.Bytes()}, nil
}

// maskedCursor makes the straight-alpha RGBA image of a pointer given as
// an AND mask and an XOR image, as Windows draws monochrome pointers and
// colour pointers without alpha. Where and[i] is false the pixel is xor[i]
// (0xRRGGBB); where it is true and xor[i] is 0 the screen shows through;
// where it is true and xor[i] is not 0 the screen is inverted, which an
// image cannot do: those pixels become black, outlined in white, so that
// the shape (the text I-beam is drawn this way) shows on dark and light
// backgrounds alike.
func maskedCursor(w, h int, and []bool, xor []uint32) []byte {
	pix := make([]byte, w*h*4)
	invert := make([]bool, w*h)
	for i := range w * h {
		switch {
		case !and[i]:
			c := xor[i]
			pix[4*i], pix[4*i+1], pix[4*i+2], pix[4*i+3] = byte(c>>16), byte(c>>8), byte(c), 0xff
		case xor[i]&0xffffff != 0:
			invert[i] = true
			pix[4*i+3] = 0xff // black
		}
	}
	for y := range h {
		for x := range w {
			i := y*w + x
			if pix[4*i+3] != 0 {
				continue
			}
			near := false
			for dy := -1; dy <= 1 && !near; dy++ {
				for dx := -1; dx <= 1; dx++ {
					nx, ny := x+dx, y+dy
					if nx >= 0 && ny >= 0 && nx < w && ny < h && invert[ny*w+nx] {
						near = true
						break
					}
				}
			}
			if near {
				pix[4*i], pix[4*i+1], pix[4*i+2], pix[4*i+3] = 0xff, 0xff, 0xff, 0xff
			}
		}
	}
	return pix
}

// dibCursor makes the straight-alpha RGBA image of a Windows pointer of
// w x h pixels from its bitmaps as GetDIBits returns them at 32 bits per
// pixel, top-down (BGRA). color is the colour bitmap, nil for a monochrome
// pointer; mask is the AND mask (w x h), with for a monochrome pointer the
// XOR mask below it (w x 2h). A colour pointer with alpha is drawn with
// its alpha; one without, and a monochrome one, with the masks
// (maskedCursor).
func dibCursor(w, h int, mask, color []byte) ([]byte, error) {
	n := w * h
	if w <= 0 || h <= 0 {
		return nil, errors.New("empty pointer bitmap")
	}
	if color != nil && len(color) < 4*n || len(mask) < 4*n || color == nil && len(mask) < 8*n {
		return nil, errors.New("short pointer bitmap")
	}
	if color != nil {
		alpha := false
		for i := range n {
			if color[4*i+3] != 0 {
				alpha = true
				break
			}
		}
		if alpha {
			pix := make([]byte, 4*n)
			for i := range n {
				pix[4*i], pix[4*i+1], pix[4*i+2], pix[4*i+3] = color[4*i+2], color[4*i+1], color[4*i], color[4*i+3]
			}
			return pix, nil
		}
	}
	and, xor := make([]bool, n), make([]uint32, n)
	for i := range n {
		and[i] = mask[4*i]|mask[4*i+1]|mask[4*i+2] != 0
		if color != nil {
			xor[i] = uint32(color[4*i+2])<<16 | uint32(color[4*i+1])<<8 | uint32(color[4*i])
		} else {
			j := 4 * (n + i)
			xor[i] = uint32(mask[j+2])<<16 | uint32(mask[j+1])<<8 | uint32(mask[j])
		}
	}
	return maskedCursor(w, h, and, xor), nil
}

// premultipliedToStraight converts premultiplied ARGB pixels (XFixes'
// format, 0xAARRGGBB) to straight-alpha RGBA.
func premultipliedToStraight(argb []uint32) []byte {
	pix := make([]byte, len(argb)*4)
	for i, p := range argb {
		a := p >> 24
		r, g, b := p>>16&0xff, p>>8&0xff, p&0xff
		if a != 0 && a != 0xff {
			r, g, b = min(r*255/a, 255), min(g*255/a, 255), min(b*255/a, 255)
		}
		pix[4*i], pix[4*i+1], pix[4*i+2], pix[4*i+3] = byte(r), byte(g), byte(b), byte(a)
	}
	return pix
}

// Pointer polling by the host.
const (
	// cursorEvery is how often the host looks at its pointer while the
	// client draws it. Messages go only when something changed.
	cursorEvery = 8 * time.Millisecond
	// cursorFailAfter: a pointer that cannot be read for this long while
	// the screen is captured goes back into the video.
	cursorFailAfter = 2 * time.Second
)

// cursorRun is a running pointer loop.
type cursorRun struct {
	stop, done chan struct{}
}

// setCursorLoop starts or stops the pointer loop, as the Hello just sent
// says. It runs on run's goroutine.
func (h *hostSession) setCursorLoop(on bool) {
	if on == (h.cursorRun != nil) {
		return
	}
	if !on {
		close(h.cursorRun.stop)
		<-h.cursorRun.done
		h.cursorRun = nil
		return
	}
	if h.cursor == nil {
		h.cursor = newCursorTracker(nil)
	}
	r := &cursorRun{stop: make(chan struct{}), done: make(chan struct{})}
	h.cursorRun = r
	go h.cursorLoop(h.spec, h.cursor, r)
}

// cursorLoop sends the pointer's state and shape whenever they change,
// until stopped. It tells run when the pointer cannot be read.
func (h *hostSession) cursorLoop(spec captureSpec, t *cursorTracker, r *cursorRun) {
	defer close(r.done)
	fail := func(fatal bool) {
		select {
		case h.cursorFailed <- fatal:
		default:
		}
	}
	src, err := newCursorSource(spec)
	if err != nil {
		slog.Debug("desktop: cannot read the pointer; drawing it into the video", "err", err)
		fail(true)
		return
	}
	defer src.close()
	t.src, t.haveIt = src, false
	clear(t.shapes) // keys of another source
	t.resend()
	tick := time.NewTicker(cursorEvery)
	defer tick.Stop()
	var failing time.Time
	for {
		select {
		case <-r.stop:
			return
		case <-h.ctx.Done():
			return
		case now := <-tick.C:
			at := h.cursorAt.Load()
			m, err := t.poll(at.X, at.Y)
			if err != nil {
				if failing.IsZero() {
					failing = now
				} else if now.Sub(failing) >= cursorFailAfter {
					slog.Debug("desktop: cannot read the pointer", "err", err)
					fail(false)
					failing = now
				}
				continue
			}
			failing = time.Time{}
			// A position that cannot be queued now is sent at the next
			// tick (commit is what makes a state count as sent); a shape
			// waits its turn.
			if m != nil && h.enqueueJSON(frameCursor, m, m.Shape == nil) {
				t.commit(m)
			}
		}
	}
}

// onCursorFailed puts the pointer back into the video when it cannot be
// read, except while the screen itself cannot be captured (Windows' secure
// desktop), when nothing can be read and the pointer comes back with it.
func (h *hostSession) onCursorFailed(now time.Time, fatal bool) {
	if h.cursorBroken || !fatal && (h.paused != "" || secureDesktop() != "") {
		return
	}
	h.cursorBroken = true
	h.setCursorLoop(false)
	h.scheduleRestart(now)
}
