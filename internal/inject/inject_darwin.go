package inject

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/ebitengine/purego"

	"github.com/readori/RemoHop/internal/x11disp"
)

// macOS input goes through Quartz event services: CGEvents posted at the HID
// tap, as if from real devices. The frameworks are loaded with purego, so
// the binary needs no cgo. Coordinates are global display points (see
// DisplayGeometry); keys are kVK_* virtual keys (US positions) and text is
// typed as Unicode strings attached to key events, which needs no key on the
// layout at all.
//
// Posting events needs the Accessibility permission; without it macOS drops
// them silently, so Open checks AXIsProcessTrusted first.

// CoreGraphics types and constants.
type cgPoint struct{ X, Y float64 }

type cgSize struct{ W, H float64 }

type cgRect struct {
	Origin cgPoint
	Size   cgSize
}

const (
	cgHIDEventTap           = 0
	cgEventSourceStateHID   = 1
	cgEventLeftMouseDown    = 1
	cgEventLeftMouseUp      = 2
	cgEventRightMouseDown   = 3
	cgEventRightMouseUp     = 4
	cgEventMouseMoved       = 5
	cgEventLeftMouseDragged = 6
	cgEventRightDragged     = 7
	cgEventOtherMouseDown   = 25
	cgEventOtherMouseUp     = 26
	cgEventOtherDragged     = 27
	cgMouseEventClickState  = 1 // kCGMouseEventClickState field
	cgScrollEventUnitLine   = 1
	// doubleClickTime and doubleClickSlop decide when a press continues a
	// multi-click (macOS only counts clicks that say so in their click
	// state): the system default interval, and a few points of jitter.
	doubleClickTime = 500 * time.Millisecond
	doubleClickSlop = 4.0
)

var (
	macOnce sync.Once
	macErr  error

	cfRelease                   func(ref uintptr)
	cgEventCreate               func(source uintptr) uintptr
	cgEventGetLocation          func(ev uintptr) cgPoint
	cgEventCreateMouseEvent     func(source uintptr, typ uint32, pt cgPoint, button uint32) uintptr
	cgEventCreateKeyboardEvent  func(source uintptr, vk uint16, down bool) uintptr
	cgEventCreateScrollWheel2   func(source uintptr, units uint32, count uint32, w1, w2, w3 int32) uintptr
	cgEventKeyboardSetUnicode   func(ev uintptr, n uint64, s *uint16)
	cgEventSetFlags             func(ev uintptr, flags uint64)
	cgEventSetIntegerValueField func(ev uintptr, field uint32, v int64)
	cgEventPost                 func(tap uint32, ev uintptr)
	cgEventSourceKeyState       func(state int32, vk uint16) bool
	cgEventSourceButtonState    func(state int32, button uint32) bool
	cgGetActiveDisplayList      func(max uint32, ids *uint32, count *uint32) int32
	cgMainDisplayID             func() uint32
	cgDisplayBounds             func(id uint32) cgRect
	cgDisplayPixelsWide         func(id uint32) uint64
	cgDisplayPixelsHigh         func(id uint32) uint64
	cgDisplayCopyDisplayMode    func(id uint32) uintptr
	cgDisplayModeGetPixelWidth  func(mode uintptr) uint64
	cgDisplayModeGetPixelHeight func(mode uintptr) uint64
	cgDisplayModeRelease        func(mode uintptr)
	axIsProcessTrusted          func() bool
)

func loadMac() error {
	macOnce.Do(func() {
		libs := map[string]string{
			"cf": "/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation",
			"cg": "/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics",
			"as": "/System/Library/Frameworks/ApplicationServices.framework/ApplicationServices",
		}
		h := map[string]uintptr{}
		for k, path := range libs {
			lib, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_GLOBAL)
			if err != nil {
				macErr = fmt.Errorf("load %s: %w", path, err)
				return
			}
			h[k] = lib
		}
		for _, f := range []struct {
			lib, name string
			fptr      any
		}{
			{"cf", "CFRelease", &cfRelease},
			{"cg", "CGEventCreate", &cgEventCreate},
			{"cg", "CGEventGetLocation", &cgEventGetLocation},
			{"cg", "CGEventCreateMouseEvent", &cgEventCreateMouseEvent},
			{"cg", "CGEventCreateKeyboardEvent", &cgEventCreateKeyboardEvent},
			{"cg", "CGEventCreateScrollWheelEvent2", &cgEventCreateScrollWheel2},
			{"cg", "CGEventKeyboardSetUnicodeString", &cgEventKeyboardSetUnicode},
			{"cg", "CGEventSetFlags", &cgEventSetFlags},
			{"cg", "CGEventSetIntegerValueField", &cgEventSetIntegerValueField},
			{"cg", "CGEventPost", &cgEventPost},
			{"cg", "CGEventSourceKeyState", &cgEventSourceKeyState},
			{"cg", "CGEventSourceButtonState", &cgEventSourceButtonState},
			{"cg", "CGGetActiveDisplayList", &cgGetActiveDisplayList},
			{"cg", "CGMainDisplayID", &cgMainDisplayID},
			{"cg", "CGDisplayBounds", &cgDisplayBounds},
			{"cg", "CGDisplayPixelsWide", &cgDisplayPixelsWide},
			{"cg", "CGDisplayPixelsHigh", &cgDisplayPixelsHigh},
			{"cg", "CGDisplayCopyDisplayMode", &cgDisplayCopyDisplayMode},
			{"cg", "CGDisplayModeGetPixelWidth", &cgDisplayModeGetPixelWidth},
			{"cg", "CGDisplayModeGetPixelHeight", &cgDisplayModeGetPixelHeight},
			{"cg", "CGDisplayModeRelease", &cgDisplayModeRelease},
			{"as", "AXIsProcessTrusted", &axIsProcessTrusted},
		} {
			sym, err := purego.Dlsym(h[f.lib], f.name)
			if err != nil {
				macErr = fmt.Errorf("macOS symbol %s: %w", f.name, err)
				return
			}
			purego.RegisterFunc(f.fptr, sym)
		}
	})
	return macErr
}

// AccessibilityTrusted reports whether macOS lets this process post input
// events (AXIsProcessTrusted; it never prompts).
func AccessibilityTrusted() (bool, error) {
	if err := loadMac(); err != nil {
		return false, err
	}
	return axIsProcessTrusted(), nil
}

// ErrNoAccessibility is returned by Open when the Accessibility permission
// is missing; its message says where the host owner grants it.
var ErrNoAccessibility = errors.New(accessibilityHelp())

func accessibilityHelp() string {
	exe, err := os.Executable()
	if err != nil {
		exe = "remohop"
	}
	return "macOS has not granted this process the Accessibility permission, so it cannot control the mouse and keyboard " +
		"(macOS would silently drop the events): on the host open System Settings > Privacy & Security > Accessibility " +
		"and enable " + exe + " - or, when remohop runs inside a terminal (Terminal, iTerm) or from a launchd agent, the " +
		"terminal app or the agent's program - then restart remohop host. Screen capture needs Screen Recording in the same place"
}

// MacDisplays lists the active displays, main display first (the order of
// CGGetActiveDisplayList, which is also the numbering of screencapture -D
// and of AVFoundation's "Capture screen N", minus one).
func MacDisplays() ([]DisplayGeometry, error) {
	if err := loadMac(); err != nil {
		return nil, err
	}
	var ids [32]uint32
	var n uint32
	if e := cgGetActiveDisplayList(uint32(len(ids)), &ids[0], &n); e != 0 {
		return nil, fmt.Errorf("CGGetActiveDisplayList failed (error %d)", e)
	}
	if n == 0 {
		return nil, errors.New("no display available on the host: macOS reports no active display (is a user logged in to the desktop?)")
	}
	main := cgMainDisplayID()
	out := make([]DisplayGeometry, 0, n)
	for _, id := range ids[:min(int(n), len(ids))] {
		b := cgDisplayBounds(id)
		g := DisplayGeometry{X: b.Origin.X, Y: b.Origin.Y, Width: b.Size.W, Height: b.Size.H, Main: id == main}
		// The mode's pixel size is the backing store a screenshot has;
		// CGDisplayPixelsWide reports points in HiDPI modes.
		if mode := cgDisplayCopyDisplayMode(id); mode != 0 {
			g.PixelWidth, g.PixelHeight = int(cgDisplayModeGetPixelWidth(mode)), int(cgDisplayModeGetPixelHeight(mode))
			cgDisplayModeRelease(mode)
		}
		if g.PixelWidth <= 0 || g.PixelHeight <= 0 {
			g.PixelWidth, g.PixelHeight = int(cgDisplayPixelsWide(id)), int(cgDisplayPixelsHigh(id))
		}
		out = append(out, g)
	}
	return out, nil
}

// Injector synthesises input with CGEventPost.
type Injector struct {
	keys    map[uint16]uint64 // held virtual keys -> modifier flag they set
	buttons map[Button]bool
	symKey  map[Keysym]uint16
	// last press, for click counting
	lastBtn   Button
	lastAt    cgPoint
	lastTime  time.Time
	clicks    int64
	pressedAt map[Button]int64 // click state of the held press, for its release
	// moved is where MoveToF last put the pointer and when. Posting is
	// asynchronous: for a moment afterwards the system still reports the
	// old position, and a button event created there would click where the
	// pointer was, not where it was just sent.
	moved   cgPoint
	movedAt time.Time
}

// moveSettle is how long after a move its target is trusted over what the
// system reports as the pointer's position; after that a physical mouse may
// have moved it.
const moveSettle = 250 * time.Millisecond

// Open checks the Accessibility permission and prepares to post events. The
// display argument exists for the X11 backend and is ignored here.
func Open(ctx context.Context, _ *x11disp.Display) (*Injector, error) {
	trusted, err := AccessibilityTrusted()
	if err != nil {
		return nil, err
	}
	if !trusted {
		return nil, ErrNoAccessibility
	}
	return &Injector{keys: map[uint16]uint64{}, buttons: map[Button]bool{}, symKey: map[Keysym]uint16{}, pressedAt: map[Button]int64{}}, nil
}

// flags is the modifier state to stamp on every event: what we hold.
func (m *Injector) flags() uint64 {
	var f uint64
	for _, fl := range m.keys {
		f |= fl
	}
	return f
}

// post stamps ev with the held modifiers, posts it at the HID tap and
// releases it.
func (m *Injector) post(ev uintptr) error {
	if ev == 0 {
		return errors.New("macOS refused to create the input event")
	}
	cgEventSetFlags(ev, m.flags())
	cgEventPost(cgHIDEventTap, ev)
	cfRelease(ev)
	return nil
}

func (m *Injector) location() cgPoint {
	ev := cgEventCreate(0)
	if ev == 0 {
		return cgPoint{}
	}
	defer cfRelease(ev)
	return cgEventGetLocation(ev)
}

// MoveTo puts the pointer at x, y in global points.
func (m *Injector) MoveTo(x, y int) error { return m.MoveToF(float64(x), float64(y)) }

// MoveToF puts the pointer at x, y in global points. While a button is held
// the move is a drag of that button, which is what applications expect.
func (m *Injector) MoveToF(x, y float64) error {
	if math.IsNaN(x) || math.IsNaN(y) || math.Abs(x) > 1e6 || math.Abs(y) > 1e6 {
		return fmt.Errorf("pointer position %v,%v is out of range", x, y)
	}
	typ, btn := uint32(cgEventMouseMoved), uint32(0)
	switch {
	case m.buttons[Left]:
		typ = cgEventLeftMouseDragged
	case m.buttons[Right]:
		typ, btn = cgEventRightDragged, 1
	case m.buttons[Middle]:
		typ, btn = cgEventOtherDragged, 2
	}
	if err := m.post(cgEventCreateMouseEvent(0, typ, cgPoint{x, y}, btn)); err != nil {
		return err
	}
	m.moved, m.movedAt = cgPoint{x, y}, time.Now()
	return nil
}

// target is where a button event belongs: the place the pointer was just
// sent to, or else where the system says it is.
func (m *Injector) target() cgPoint {
	if !m.movedAt.IsZero() && time.Since(m.movedAt) < moveSettle {
		return m.moved
	}
	return m.location()
}

// Pointer reports where the pointer is, in whole global points.
func (m *Injector) Pointer() (int, int, error) {
	x, y, err := m.PointerF()
	return int(math.Floor(x)), int(math.Floor(y)), err
}

// PointerF reports where the pointer is in global points.
func (m *Injector) PointerF() (float64, float64, error) {
	p := m.location()
	return p.X, p.Y, nil
}

func macButton(b Button, down bool) (typ, num uint32, err error) {
	switch b {
	case Left:
		return pickU(down, cgEventLeftMouseDown, cgEventLeftMouseUp), 0, nil
	case Right:
		return pickU(down, cgEventRightMouseDown, cgEventRightMouseUp), 1, nil
	case Middle:
		return pickU(down, cgEventOtherMouseDown, cgEventOtherMouseUp), 2, nil
	case Back:
		return pickU(down, cgEventOtherMouseDown, cgEventOtherMouseUp), 3, nil
	case Forward:
		return pickU(down, cgEventOtherMouseDown, cgEventOtherMouseUp), 4, nil
	}
	return 0, 0, fmt.Errorf("unknown mouse button %d", b)
}

func pickU(c bool, a, b uint32) uint32 {
	if c {
		return a
	}
	return b
}

// Button presses or releases a mouse button where the pointer is. A press
// soon after a press of the same button at the same place counts as the
// next click of a double (or triple) click.
func (m *Injector) Button(b Button, down bool) error {
	typ, num, err := macButton(b, down)
	if err != nil {
		return err
	}
	at := m.target()
	state := m.pressedAt[b]
	if down {
		now := time.Now()
		if b == m.lastBtn && now.Sub(m.lastTime) < doubleClickTime &&
			math.Abs(at.X-m.lastAt.X) <= doubleClickSlop && math.Abs(at.Y-m.lastAt.Y) <= doubleClickSlop {
			m.clicks++
		} else {
			m.clicks = 1
		}
		m.lastBtn, m.lastAt, m.lastTime = b, at, now
		state = m.clicks
	} else if state == 0 {
		state = 1
	}
	ev := cgEventCreateMouseEvent(0, typ, at, num)
	if ev == 0 {
		return errors.New("macOS refused to create the mouse event")
	}
	cgEventSetIntegerValueField(ev, cgMouseEventClickState, state)
	if err := m.post(ev); err != nil {
		return err
	}
	if down {
		m.buttons[b] = true
		m.pressedAt[b] = state
	} else {
		delete(m.buttons, b)
		delete(m.pressedAt, b)
	}
	return nil
}

// Scroll turns the wheel by whole notches (one line each): positive dy is
// down, positive dx is right. Quartz counts positive as up and left.
func (m *Injector) Scroll(dx, dy int) error {
	if dx == 0 && dy == 0 {
		return nil
	}
	if tooFar(dx) || tooFar(dy) {
		return fmt.Errorf("scroll of %d,%d notches is out of range", dx, dy)
	}
	return m.post(cgEventCreateScrollWheel2(0, cgScrollEventUnitLine, 2, int32(-dy), int32(-dx), 0))
}

// cgKeyboardEventAutorepeat is kCGKeyboardEventAutorepeat.
const cgKeyboardEventAutorepeat = 8

func tooFar(v int) bool { return v > 1<<20 || v < -(1<<20) }

func (m *Injector) key(k macKey, down bool) error {
	_, held := m.keys[k.vk]
	if down {
		m.keys[k.vk] = k.flag // the flag applies to this event already
	} else {
		delete(m.keys, k.vk) // and is gone from its release
	}
	ev := cgEventCreateKeyboardEvent(0, k.vk, down)
	if down && held && ev != 0 {
		// A key-down for a held key is the keyboard's autorepeat,
		// forwarded by a viewer: macOS repeats in its HID system, never
		// for posted events, so each repeat is posted marked as one.
		cgEventSetIntegerValueField(ev, cgKeyboardEventAutorepeat, 1)
	}
	if err := m.post(ev); err != nil {
		if down {
			delete(m.keys, k.vk)
		}
		return err
	}
	return nil
}

// KeyEvdev presses or releases a physical key given by its Linux evdev
// code, as the macOS key at the same position.
func (m *Injector) KeyEvdev(code uint16, down bool) error {
	k, ok := macEvdev[code]
	if !ok {
		return fmt.Errorf("evdev key %d has no macOS key", code)
	}
	return m.key(k, down)
}

// NeedsShift reports whether the key producing sym must be combined with
// Shift (on US key positions).
func (m *Injector) NeedsShift(sym Keysym) bool {
	k, ok := macKeyForKeysym(sym)
	return ok && k.shift
}

// KeySym presses or releases the key producing sym, without adding Shift
// (see NeedsShift). Characters use US key positions, which is what macOS
// shortcuts are usually documented with.
func (m *Injector) KeySym(sym Keysym, down bool) error {
	if !down {
		if vk, ok := m.symKey[sym]; ok {
			delete(m.symKey, sym)
			return m.key(macKey{vk: vk}, false)
		}
	}
	k, ok := macKeyForKeysym(sym)
	if !ok {
		return fmt.Errorf("keysym %#x has no key on macOS hosts", uint32(sym))
	}
	if down {
		m.symKey[sym] = k.vk
	}
	return m.key(macKey{vk: k.vk, flag: k.flag}, down)
}

// TypeRune types one character. Newline and tab are the Return and Tab
// keys; everything else is a key event carrying the character as a Unicode
// string, which applications take as typed text whatever the layout.
func (m *Injector) TypeRune(r rune) error {
	if _, ok := RuneKeysym(r); !ok {
		return fmt.Errorf("cannot type character %U", r)
	}
	switch r {
	case '\n', '\r':
		return m.tap(kvkReturn)
	case '\t':
		return m.tap(kvkTab)
	}
	units := utf16.Encode([]rune{r})
	for _, down := range []bool{true, false} {
		ev := cgEventCreateKeyboardEvent(0, 0, down)
		if ev == 0 {
			return errors.New("macOS refused to create the key event")
		}
		cgEventKeyboardSetUnicode(ev, uint64(len(units)), &units[0])
		if err := m.post(ev); err != nil {
			return err
		}
	}
	return nil
}

func (m *Injector) tap(vk uint16) error {
	if err := m.key(macKey{vk: vk}, true); err != nil {
		return err
	}
	return m.key(macKey{vk: vk}, false)
}

// ReleaseAll lets go of every key and button held through this Injector.
func (m *Injector) ReleaseAll() error {
	var first error
	for b := range m.buttons {
		if err := m.Button(b, false); err != nil && first == nil {
			first = err
		}
	}
	// Ordinary keys first, modifiers last, so no release combines with a
	// modifier that is still down.
	for _, mods := range []bool{false, true} {
		for vk, flag := range m.keys {
			if (flag != 0) == mods {
				if err := m.key(macKey{vk: vk}, false); err != nil && first == nil {
					first = err
				}
			}
		}
	}
	clear(m.symKey)
	return first
}

// Close releases everything held.
func (m *Injector) Close() error { return m.ReleaseAll() }
