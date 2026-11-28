package inject

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
	"github.com/jezek/xgb/xtest"

	"github.com/readori/RemoHop/internal/x11disp"
)

// Keymap settle times. X clients translate a keycode with the keymap they
// fetch after a MappingNotify, and many fetch it lazily, so a keycode that
// was just rebound must keep its new meaning until they have looked:
// bindSettle passes between binding a spare keycode and pressing it, and
// mapSettle between the last press of a bound keycode and rebinding or
// restoring it. These are the delays xdotool relies on too.
const (
	bindSettle = 40 * time.Millisecond
	mapSettle  = 200 * time.Millisecond
)

// Injector synthesises input through the XTEST extension, which X servers
// treat exactly like events from a physical device.
type Injector struct {
	c            *xgb.Conn
	root         xproto.Window
	minKC, maxKC xproto.Keycode

	// keymap is the core keyboard mapping, perKC keysyms per keycode from
	// minKC on. It is read once at Open and updated as spares are bound.
	keymap []xproto.Keysym
	perKC  int
	// spares are keycodes with no symbols, which Type and Key bind
	// temporarily to characters the layout cannot produce.
	spares []spare

	keys    map[xproto.Keycode]bool // held down by us
	symKey  map[Keysym]xproto.Keycode
	buttons map[Button]bool
	sleep   func(time.Duration) // tests shorten the settle delays
}

type spare struct {
	kc   xproto.Keycode
	sym  Keysym // currently bound keysym, 0 if unbound
	used time.Time
}

// Open connects to the X display d, or to the one x11disp finds when d is
// nil, and prepares to inject input there.
func Open(ctx context.Context, d *x11disp.Display) (*Injector, error) {
	if d == nil {
		var err error
		if d, err = FindDisplay(ctx); err != nil {
			return nil, err
		}
	}
	c, err := d.Open(ctx)
	if err != nil {
		return nil, fmt.Errorf("connect to %w", err)
	}
	return NewX11(c)
}

// FindDisplay is x11disp.Find with the host-facing error the screenshot
// service also gives when there is nothing to drive.
func FindDisplay(ctx context.Context) (*x11disp.Display, error) {
	d, err := x11disp.Find(ctx)
	if err == nil {
		return d, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	msg := err.Error()
	var fe *x11disp.Error
	if errors.As(err, &fe) {
		msg = strings.Join(fe.Notes, "; ")
	}
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		return nil, fmt.Errorf("no display available on the host: %s; this host runs a Wayland session without X11 and input injection needs an X11 session (choose \"GNOME on Xorg\"/\"Plasma (X11)\" at login)", msg)
	}
	return nil, fmt.Errorf("no display available on the host: %s; input needs a running X11 session", msg)
}

// NewX11 takes over an open connection (it is closed by Close, or here on
// error).
func NewX11(c *xgb.Conn) (*Injector, error) {
	if err := xtest.Init(c); err != nil {
		c.Close()
		return nil, fmt.Errorf("X server has no XTEST extension, input is unavailable: %w", err)
	}
	setup := xproto.Setup(c)
	x := &Injector{
		c:       c,
		root:    setup.DefaultScreen(c).Root,
		minKC:   setup.MinKeycode,
		maxKC:   setup.MaxKeycode,
		keys:    map[xproto.Keycode]bool{},
		symKey:  map[Keysym]xproto.Keycode{},
		buttons: map[Button]bool{},
		sleep:   time.Sleep,
	}
	if err := x.loadKeymap(); err != nil {
		c.Close()
		return nil, err
	}
	return x, nil
}

func (x *Injector) loadKeymap() error {
	n := int(x.maxKC) - int(x.minKC) + 1
	rep, err := xproto.GetKeyboardMapping(x.c, x.minKC, byte(n)).Reply()
	if err != nil {
		return fmt.Errorf("read the X keyboard mapping: %w", err)
	}
	x.perKC = int(rep.KeysymsPerKeycode)
	if x.perKC < 2 || len(rep.Keysyms) < n*x.perKC {
		return errors.New("X server returned an unusable keyboard mapping")
	}
	x.keymap = rep.Keysyms
	// Keycodes bound to a modifier stay out of the spare pool even if they
	// carry no symbol: rebinding them would change modifier state.
	mods := map[xproto.Keycode]bool{}
	if mm, err := xproto.GetModifierMapping(x.c).Reply(); err == nil {
		for _, kc := range mm.Keycodes {
			mods[kc] = true
		}
	}
	// Highest keycodes first: they are the least likely to be special.
	for kc := int(x.maxKC); kc >= int(x.minKC); kc-- {
		if !mods[xproto.Keycode(kc)] && slices.IndexFunc(x.row(xproto.Keycode(kc)), func(s xproto.Keysym) bool { return s != 0 }) < 0 {
			x.spares = append(x.spares, spare{kc: xproto.Keycode(kc)})
		}
	}
	return nil
}

func (x *Injector) row(kc xproto.Keycode) []xproto.Keysym {
	i := (int(kc) - int(x.minKC)) * x.perKC
	return x.keymap[i : i+x.perKC]
}

// lookup finds a keycode producing sym without modifiers (level 0) or with
// Shift (level 1), preferring level 0. Only the first group is considered:
// it is the one active unless the user switched layouts.
func (x *Injector) lookup(sym Keysym) (kc xproto.Keycode, level int, ok bool) {
	shifted := xproto.Keycode(0)
	for k := int(x.minKC); k <= int(x.maxKC); k++ {
		r := x.row(xproto.Keycode(k))
		if Keysym(r[0]) == sym {
			return xproto.Keycode(k), 0, true
		}
		s1 := Keysym(r[1])
		if s1 == 0 && r[0] >= 'a' && r[0] <= 'z' {
			s1 = Keysym(r[0]) - 'a' + 'A' // implied upper case (core protocol rule)
		}
		if s1 == sym && shifted == 0 {
			shifted = xproto.Keycode(k)
		}
	}
	if shifted != 0 {
		return shifted, 1, true
	}
	return 0, 0, false
}

// resolve returns a keycode for sym and whether Shift is needed, binding a
// spare keycode when the layout has no key for it.
func (x *Injector) resolve(sym Keysym) (xproto.Keycode, bool, error) {
	if kc, level, ok := x.lookup(sym); ok {
		for i := range x.spares {
			if x.spares[i].kc == kc {
				x.spares[i].used = time.Now()
			}
		}
		return kc, level == 1, nil
	}
	if len(x.spares) == 0 {
		return 0, false, fmt.Errorf("the keyboard layout has no key for keysym %#x and no free keycode to bind it to", uint32(sym))
	}
	// Reuse an unbound spare, or the least recently used one once clients
	// have had time to look at its previous binding.
	best := 0
	for i, s := range x.spares {
		if s.sym == 0 {
			best = i
			break
		}
		if s.used.Before(x.spares[best].used) {
			best = i
		}
	}
	s := &x.spares[best]
	if x.keys[s.kc] {
		return 0, false, fmt.Errorf("too many unusual keys held at once")
	}
	if s.sym != 0 {
		if wait := mapSettle - time.Since(s.used); wait > 0 {
			x.sleep(wait)
		}
	}
	row := make([]xproto.Keysym, x.perKC)
	// The same symbol on both levels, so Shift or Caps Lock do not matter.
	row[0], row[1] = xproto.Keysym(sym), xproto.Keysym(sym)
	if err := x.setRow(s.kc, row); err != nil {
		return 0, false, err
	}
	s.sym, s.used = sym, time.Now()
	x.sleep(bindSettle)
	return s.kc, false, nil
}

func (x *Injector) setRow(kc xproto.Keycode, row []xproto.Keysym) error {
	if err := xproto.ChangeKeyboardMappingChecked(x.c, 1, kc, byte(x.perKC), row).Check(); err != nil {
		return fmt.Errorf("bind keycode %d: %w", kc, err)
	}
	copy(x.row(kc), row)
	return nil
}

// fake sends one XTEST event and waits for the server to accept it, so
// errors surface here instead of piling up in xgb's event queue.
func (x *Injector) fake(typ, detail byte, rx, ry int16) error {
	return xtest.FakeInputChecked(x.c, typ, detail, 0, x.root, rx, ry, 0).Check()
}

// MoveTo puts the pointer at x, y on the virtual screen.
func (x *Injector) MoveTo(px, py int) error {
	if px < -32768 || px > 32767 || py < -32768 || py > 32767 {
		return fmt.Errorf("pointer position %d,%d is out of range", px, py)
	}
	return x.fake(xproto.MotionNotify, 0, int16(px), int16(py))
}

// MoveBy moves the pointer by dx, dy pixels from where it is: XTEST's
// relative motion, which the server handles like a mouse's own movement
// (pointer acceleration included) and which clients that grab the pointer
// for relative input (games, 3D viewports) see as motion.
func (x *Injector) MoveBy(dx, dy int) error {
	if dx < -32768 || dx > 32767 || dy < -32768 || dy > 32767 {
		return fmt.Errorf("relative move %d,%d is out of range", dx, dy)
	}
	if dx == 0 && dy == 0 {
		return nil
	}
	return x.fake(xproto.MotionNotify, 1, int16(dx), int16(dy))
}

// MoveToF is MoveTo for fractional coordinates (rounded: X11 positions
// are whole pixels).
func (x *Injector) MoveToF(px, py float64) error {
	return x.MoveTo(int(math.Round(px)), int(math.Round(py)))
}

// PointerF is Pointer as floats.
func (x *Injector) PointerF() (float64, float64, error) {
	px, py, err := x.Pointer()
	return float64(px), float64(py), err
}

// Pointer reports where the pointer is on the virtual screen.
func (x *Injector) Pointer() (int, int, error) {
	p, err := xproto.QueryPointer(x.c, x.root).Reply()
	if err != nil {
		return 0, 0, fmt.Errorf("query pointer: %w", err)
	}
	return int(p.RootX), int(p.RootY), nil
}

// Button presses or releases a mouse button where the pointer is.
func (x *Injector) Button(b Button, down bool) error {
	switch b {
	case Left, Middle, Right, Back, Forward:
	default:
		return fmt.Errorf("unknown mouse button %d", b)
	}
	typ := byte(xproto.ButtonRelease)
	if down {
		typ = xproto.ButtonPress
	}
	if err := x.fake(typ, byte(b), 0, 0); err != nil {
		return err
	}
	if down {
		x.buttons[b] = true
	} else {
		delete(x.buttons, b)
	}
	return nil
}

// Scroll turns the wheel by whole notches: positive dy is down, positive
// dx is right.
func (x *Injector) Scroll(dx, dy int) error {
	click := func(b byte, n int) error {
		for ; n > 0; n-- {
			if err := x.fake(xproto.ButtonPress, b, 0, 0); err != nil {
				return err
			}
			if err := x.fake(xproto.ButtonRelease, b, 0, 0); err != nil {
				return err
			}
		}
		return nil
	}
	// X wheel buttons: 4 up, 5 down, 6 left, 7 right.
	var err error
	if dy < 0 {
		err = click(4, -dy)
	} else {
		err = click(5, dy)
	}
	if err == nil {
		if dx < 0 {
			err = click(6, -dx)
		} else {
			err = click(7, dx)
		}
	}
	return err
}

func (x *Injector) keycode(kc xproto.Keycode, down bool) error {
	if down && x.keys[kc] {
		// A repeated press of a held key (a viewer forwards the
		// keyboard's autorepeat): the X server repeats a held key
		// itself, so a second press would double every repeat.
		return nil
	}
	typ := byte(xproto.KeyRelease)
	if down {
		typ = xproto.KeyPress
	}
	if err := x.fake(typ, byte(kc), 0, 0); err != nil {
		return err
	}
	if down {
		x.keys[kc] = true
	} else {
		delete(x.keys, kc)
	}
	return nil
}

// KeyEvdev presses or releases a physical key given by its Linux evdev
// code; the host's own layout decides what it means. X servers using
// evdev/libinput keymaps number keys as evdev code + 8.
func (x *Injector) KeyEvdev(code uint16, down bool) error {
	kc := int(code) + 8
	if kc < int(x.minKC) || kc > int(x.maxKC) {
		return fmt.Errorf("evdev key %d has no X keycode", code)
	}
	return x.keycode(xproto.Keycode(kc), down)
}

// NeedsShift reports whether the key producing sym must be combined with
// Shift (an upper-case letter, or "+" on a US layout).
func (x *Injector) NeedsShift(sym Keysym) bool {
	_, level, ok := x.lookup(sym)
	return ok && level == 1
}

// KeySym presses or releases the key producing sym, without adding Shift
// (see NeedsShift). A release goes to the keycode the press used.
func (x *Injector) KeySym(sym Keysym, down bool) error {
	if !down {
		if kc, ok := x.symKey[sym]; ok {
			delete(x.symKey, sym)
			return x.keycode(kc, false)
		}
	}
	kc, _, err := x.resolve(sym)
	if err != nil {
		return err
	}
	if down {
		x.symKey[sym] = kc
	}
	return x.keycode(kc, down)
}

// TypeRune types one character (see RuneKeysym) with a press and a
// release, holding Shift around it if the layout needs that.
func (x *Injector) TypeRune(r rune) error {
	sym, ok := RuneKeysym(r)
	if !ok {
		return fmt.Errorf("cannot type character %U", r)
	}
	kc, shift, err := x.resolve(sym)
	if err != nil {
		return err
	}
	var shiftKC xproto.Keycode
	if shift {
		if shiftKC, _, err = x.resolve(KeysymShiftL); err != nil {
			return err
		}
		if err := x.keycode(shiftKC, true); err != nil {
			return err
		}
	}
	err = x.keycode(kc, true)
	if err == nil {
		err = x.keycode(kc, false)
	}
	if shift {
		if e := x.keycode(shiftKC, false); err == nil {
			err = e
		}
	}
	return err
}

// ReleaseAll lets go of every key and button held through this Injector,
// where the pointer is.
func (x *Injector) ReleaseAll() error {
	var first error
	keys := make([]xproto.Keycode, 0, len(x.keys))
	for kc := range x.keys {
		keys = append(keys, kc)
	}
	slices.Sort(keys)
	for _, kc := range keys {
		if err := x.keycode(kc, false); err != nil && first == nil {
			first = err
		}
		delete(x.keys, kc)
	}
	clear(x.symKey)
	for b := range x.buttons {
		if err := x.Button(b, false); err != nil && first == nil {
			first = err
		}
		delete(x.buttons, b)
	}
	return first
}

// Close releases everything held, gives spare keycodes their empty mapping
// back once clients have had time to use them, and disconnects.
func (x *Injector) Close() error {
	err := x.ReleaseAll()
	var last time.Time
	for _, s := range x.spares {
		if s.sym != 0 && s.used.After(last) {
			last = s.used
		}
	}
	if !last.IsZero() {
		if wait := mapSettle - time.Since(last); wait > 0 {
			x.sleep(wait)
		}
		empty := make([]xproto.Keysym, x.perKC)
		for i := range x.spares {
			if x.spares[i].sym != 0 {
				if e := x.setRow(x.spares[i].kc, empty); e != nil && err == nil {
					err = e
				}
				x.spares[i].sym = 0
			}
		}
	}
	x.c.Close()
	return err
}
