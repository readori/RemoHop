package inject

import (
	"context"
	"fmt"
	"math"
	"sync"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/readori/RemoHop/internal/x11disp"
)

// Windows input goes through SendInput. Pointer positions are absolute on
// the virtual desktop (every monitor), keys of the desktop viewer are scan
// codes so the host's layout applies, combos use virtual keys, and text is
// typed as KEYEVENTF_UNICODE events, which need no key on the layout at
// all. The calls go through a Sys (sys_windows.go), so that a Windows
// service can have them made on the interactive desktop.

var (
	user32                 = windows.NewLazySystemDLL("user32.dll")
	procSendInput          = user32.NewProc("SendInput")
	procGetSystemMetrics   = user32.NewProc("GetSystemMetrics")
	procGetCursorPos       = user32.NewProc("GetCursorPos")
	procVkKeyScanW         = user32.NewProc("VkKeyScanW")
	procMapVirtualKeyW     = user32.NewProc("MapVirtualKeyW")
	procSetProcessDPIAware = user32.NewProc("SetProcessDPIAware")
	dpiOnce                sync.Once
)

const (
	inputMouse    = 0
	inputKeyboard = 1

	mouseMove        = 0x0001
	mouseLeftDown    = 0x0002
	mouseLeftUp      = 0x0004
	mouseRightDown   = 0x0008
	mouseRightUp     = 0x0010
	mouseMiddleDown  = 0x0020
	mouseMiddleUp    = 0x0040
	mouseXDown       = 0x0080
	mouseXUp         = 0x0100
	mouseWheel       = 0x0800
	mouseHWheel      = 0x1000
	mouseVirtualDesk = 0x4000
	mouseAbsolute    = 0x8000

	keyExtended = 0x0001
	keyUp       = 0x0002
	keyUnicode  = 0x0004
	keyScanCode = 0x0008

	wheelDelta = 120
	vkPause    = 0x13
	vkReturn   = 0x0d
	vkTab      = 0x09

	smXVirtualScreen  = 76
	smYVirtualScreen  = 77
	smCXVirtualScreen = 78
	smCYVirtualScreen = 79
)

type mouseInput struct {
	dx, dy    int32
	mouseData uint32
	flags     uint32
	time      uint32
	extra     uintptr
}

type keybdInput struct {
	vk, scan uint16
	flags    uint32
	time     uint32
	extra    uintptr
}

// winInput mirrors the C INPUT struct: a type tag followed by a union whose
// largest member is MOUSEINPUT.
type winInput struct {
	typ uint32
	mi  mouseInput
}

// extendedScan maps evdev codes beyond the set-1 range (or whose set-1 code
// needs the E0 prefix) to scan codes; bit 8 means "extended". Evdev codes
// 1..88 coincide with PC scan code set 1.
var extendedScan = map[uint16]uint16{
	96: 0x11c, 97: 0x11d, 98: 0x135, 99: 0x137, 100: 0x138,
	102: 0x147, 103: 0x148, 104: 0x149, 105: 0x14b, 106: 0x14d,
	107: 0x14f, 108: 0x150, 109: 0x151, 110: 0x152, 111: 0x153,
	113: 0x120, 114: 0x12e, 115: 0x130, 117: 0x59, 121: 0x7e,
	89: 0x73, 92: 0x79, 93: 0x70, 94: 0x7b, 124: 0x7d,
	125: 0x15b, 126: 0x15c, 127: 0x15d,
	158: 0x16a, 159: 0x169, 163: 0x119, 164: 0x122, 165: 0x110, 166: 0x124,
	172: 0x132, 173: 0x167, 217: 0x165, 156: 0x166, 155: 0x16c,
	183: 0x64, 184: 0x65, 185: 0x66, 186: 0x67, 187: 0x68, 188: 0x69,
	189: 0x6a, 190: 0x6b, 191: 0x6c, 192: 0x6d, 193: 0x6e, 194: 0x76,
}

// vkKey is a virtual key for a non-character keysym; ext marks keys that
// need KEYEVENTF_EXTENDEDKEY (otherwise the arrows and the navigation
// block arrive as their numeric keypad twins).
type vkKey struct {
	vk  uint16
	ext bool
}

var keysymVK = map[Keysym]vkKey{
	0xffe3: {0xa2, false}, 0xffe4: {0xa3, true}, // Control_L/R
	0xffe1: {0xa0, false}, 0xffe2: {0xa1, false}, // Shift_L/R
	0xffe9: {0xa4, false}, 0xffea: {0xa5, true}, // Alt_L/R
	0xffeb: {0x5b, true}, 0xffec: {0x5c, true}, // Super_L/R
	0xfe03: {0xa5, true}, // ISO_Level3_Shift: AltGr is the right Alt
	0xff0d: {0x0d, false}, 0xff8d: {0x0d, true}, 0xff09: {0x09, false},
	0xff1b: {0x1b, false}, 0xff08: {0x08, false}, 0xffff: {0x2e, true},
	0xff63: {0x2d, true}, 0xff50: {0x24, true}, 0xff57: {0x23, true},
	0xff55: {0x21, true}, 0xff56: {0x22, true},
	0xff51: {0x25, true}, 0xff52: {0x26, true}, 0xff53: {0x27, true}, 0xff54: {0x28, true},
	0x20: {0x20, false}, 0xff67: {0x5d, true}, 0xff61: {0x2c, true}, 0xff13: {0x13, false},
	0xffe5: {0x14, false}, 0xff7f: {0x90, true}, 0xff14: {0x91, false},
	0x1008ff12: {0xad, false}, 0x1008ff11: {0xae, false}, 0x1008ff13: {0xaf, false},
	0x1008ff14: {0xb3, false}, 0x1008ff17: {0xb0, false}, 0x1008ff16: {0xb1, false},
}

func init() {
	for i := Keysym(0); i < 24; i++ { // F1..F24
		keysymVK[0xffbe+i] = vkKey{0x70 + uint16(i), false}
	}
}

// Injector synthesises input with SendInput.
type Injector struct {
	keys    map[string]winInput // held key -> the event releasing it
	buttons map[Button]bool
	sas     map[string]bool // Delete keys turned into the secure attention sequence
}

// Open prepares SendInput. The display argument exists for the X11
// backend and is ignored here.
func Open(ctx context.Context, _ *x11disp.Display) (*Injector, error) {
	if err := procSendInput.Find(); err != nil {
		return nil, err
	}
	// Physical pixels, like the screenshot service, not DPI-scaled ones.
	dpiOnce.Do(func() { procSetProcessDPIAware.Call() })
	return &Injector{keys: map[string]winInput{}, buttons: map[Button]bool{}}, nil
}

func sendInputs(in []winInput) error {
	if len(in) == 0 {
		return nil
	}
	b := unsafe.Slice((*byte)(unsafe.Pointer(&in[0])), len(in)*InputSize)
	return currentSys().SendInput(b, len(in), InputSize)
}

func mouse(flags uint32, dx, dy int32, data uint32) winInput {
	return winInput{typ: inputMouse, mi: mouseInput{dx: dx, dy: dy, mouseData: data, flags: flags}}
}

func key(scan, vk uint16, flags uint32) winInput {
	in := winInput{typ: inputKeyboard}
	*(*keybdInput)(unsafe.Pointer(&in.mi)) = keybdInput{vk: vk, scan: scan, flags: flags}
	return in
}

func metric(i uintptr) int { return currentSys().Metric(int(i)) }

// MoveTo puts the pointer at x, y on the virtual desktop. Absolute
// coordinates are normalised to 0..65536 over the virtual desktop; Windows
// maps them back by truncating, so rounding up lands on the exact pixel.
func (w *Injector) MoveTo(x, y int) error {
	vx, vy := metric(smXVirtualScreen), metric(smYVirtualScreen)
	vw, vh := metric(smCXVirtualScreen), metric(smCYVirtualScreen)
	if vw <= 0 || vh <= 0 {
		return fmt.Errorf("no display available on the host: empty virtual screen")
	}
	nx := ((x-vx)*65536 + vw - 1) / vw
	ny := ((y-vy)*65536 + vh - 1) / vh
	return sendInputs([]winInput{mouse(mouseMove|mouseAbsolute|mouseVirtualDesk, int32(nx), int32(ny), 0)})
}

// MoveToF is MoveTo for fractional coordinates (rounded: Windows
// positions are whole pixels).
func (w *Injector) MoveToF(x, y float64) error {
	if math.IsNaN(x) || math.IsNaN(y) || math.Abs(x) > 1e6 || math.Abs(y) > 1e6 {
		return fmt.Errorf("pointer position %v,%v is out of range", x, y)
	}
	return w.MoveTo(int(math.Round(x)), int(math.Round(y)))
}

// PointerF is Pointer as floats.
func (w *Injector) PointerF() (float64, float64, error) {
	x, y, err := w.Pointer()
	return float64(x), float64(y), err
}

// Pointer reports where the pointer is on the virtual desktop.
func (w *Injector) Pointer() (int, int, error) {
	return currentSys().CursorPos()
}

func pick(c bool, a, b uint32) uint32 {
	if c {
		return a
	}
	return b
}

// Button presses or releases a mouse button where the pointer is.
func (w *Injector) Button(b Button, down bool) error {
	var f, data uint32
	switch b {
	case Left:
		f = pick(down, mouseLeftDown, mouseLeftUp)
	case Middle:
		f = pick(down, mouseMiddleDown, mouseMiddleUp)
	case Right:
		f = pick(down, mouseRightDown, mouseRightUp)
	case Back, Forward:
		f = pick(down, mouseXDown, mouseXUp)
		data = uint32(b - Back + 1) // XBUTTON1 = 1, XBUTTON2 = 2
	default:
		return fmt.Errorf("unknown mouse button %d", b)
	}
	if err := sendInputs([]winInput{mouse(f, 0, 0, data)}); err != nil {
		return err
	}
	if down {
		w.buttons[b] = true
	} else {
		delete(w.buttons, b)
	}
	return nil
}

// Scroll turns the wheel by whole notches: positive dy is down, positive
// dx is right.
func (w *Injector) Scroll(dx, dy int) error {
	var in []winInput
	if dy != 0 { // Windows: positive is away from the user (up)
		in = append(in, mouse(mouseWheel, 0, 0, uint32(int32(-dy*wheelDelta))))
	}
	if dx != 0 {
		in = append(in, mouse(mouseHWheel, 0, 0, uint32(int32(dx*wheelDelta))))
	}
	return sendInputs(in)
}

// sendKey sends a key event and keeps track of held keys under id.
// Delete pressed with Ctrl and Alt held becomes the secure attention
// sequence where the Sys can raise it (see SASSender); its release is then
// swallowed too.
//
// A key-down for a key already held is the keyboard's autorepeat forwarded
// by a viewer: Windows produces typematic repeat below SendInput, so
// injected keys never repeat by themselves, and each repeated key-down is
// sent as Windows' own repeats are (another key-down, no key-up).
func (w *Injector) sendKey(id string, down bool, ev winInput) error {
	if down && w.sas[id] {
		return nil // the secure attention sequence is raised once
	}
	if down {
		if sas, ok := currentSys().(SASSender); ok && isSASChord(id, func(k string) bool { _, held := w.keys[k]; return held }) {
			if w.sas == nil {
				w.sas = map[string]bool{}
			}
			w.sas[id] = true
			return sas.SendSAS()
		}
	} else if w.sas[id] {
		delete(w.sas, id)
		return nil
	}
	if err := sendInputs([]winInput{ev}); err != nil {
		return err
	}
	if down {
		up := ev
		kb := (*keybdInput)(unsafe.Pointer(&up.mi))
		kb.flags |= keyUp
		w.keys[id] = up
	} else {
		delete(w.keys, id)
	}
	return nil
}

// KeyEvdev presses or releases a physical key given by its Linux evdev
// code, sent as a scan code so the host's layout applies.
func (w *Injector) KeyEvdev(code uint16, down bool) error {
	id := fmt.Sprintf("ev%d", code)
	flags := pick(down, 0, keyUp)
	if code == 119 { // Pause has no plain scan code (E1 1D 45); use its virtual key
		return w.sendKey(id, down, key(0, vkPause, flags))
	}
	scan, ok := extendedScan[code]
	if !ok {
		if code == 0 || code > 88 {
			return fmt.Errorf("evdev key %d is not supported on Windows hosts", code)
		}
		scan = code
	}
	flags |= keyScanCode
	if scan&0x100 != 0 {
		flags |= keyExtended
	}
	return w.sendKey(id, down, key(scan&0xff, 0, flags))
}

// vkFor finds the virtual key for sym and whether Shift is part of it.
// Characters go through VkKeyScanW, which knows the active layout.
func vkFor(sym Keysym) (vkKey, bool, error) {
	if k, ok := keysymVK[sym]; ok {
		return k, false, nil
	}
	r, ok := KeysymRune(sym)
	if !ok || r > 0xffff {
		return vkKey{}, false, fmt.Errorf("keysym %#x has no key on Windows hosts", uint32(sym))
	}
	v := currentSys().VkKeyScan(uint16(r))
	if v == -1 {
		return vkKey{}, false, fmt.Errorf("the keyboard layout has no key for %q", r)
	}
	return vkKey{vk: uint16(v & 0xff)}, v>>8&1 != 0, nil
}

func vkEvent(k vkKey, down bool) winInput {
	scan := currentSys().MapVirtualKey(uint32(k.vk), 0) // MAPVK_VK_TO_VSC
	flags := pick(down, 0, keyUp)
	if k.ext {
		flags |= keyExtended
	}
	return key(uint16(scan), k.vk, flags)
}

// NeedsShift reports whether the key producing sym must be combined with
// Shift on the active layout.
func (w *Injector) NeedsShift(sym Keysym) bool {
	_, shift, err := vkFor(sym)
	return err == nil && shift
}

// KeySym presses or releases the key producing sym, without adding Shift
// (see NeedsShift).
func (w *Injector) KeySym(sym Keysym, down bool) error {
	id := fmt.Sprintf("sym%x", uint32(sym))
	if !down {
		if up, ok := w.keys[id]; ok {
			return w.sendKey(id, false, up)
		}
	}
	k, _, err := vkFor(sym)
	if err != nil {
		return err
	}
	return w.sendKey(id, down, vkEvent(k, down))
}

// TypeRune types one character. Newline and tab are the Return and Tab
// keys (applications treat a Unicode '\n' inconsistently); everything else
// is sent as UTF-16 code units, surrogate pairs included.
func (w *Injector) TypeRune(r rune) error {
	if _, ok := RuneKeysym(r); !ok {
		return fmt.Errorf("cannot type character %U", r)
	}
	switch r {
	case '\n', '\r':
		return sendInputs([]winInput{vkEvent(vkKey{vk: vkReturn}, true), vkEvent(vkKey{vk: vkReturn}, false)})
	case '\t':
		return sendInputs([]winInput{vkEvent(vkKey{vk: vkTab}, true), vkEvent(vkKey{vk: vkTab}, false)})
	}
	var in []winInput
	for _, u := range utf16.Encode([]rune{r}) {
		in = append(in, key(u, 0, keyUnicode), key(u, 0, keyUnicode|keyUp))
	}
	return sendInputs(in)
}

// ReleaseAll lets go of every key and button held through this Injector.
func (w *Injector) ReleaseAll() error {
	var in []winInput
	for _, up := range w.keys {
		in = append(in, up)
	}
	clear(w.keys)
	clear(w.sas)
	for b := range w.buttons {
		var f, data uint32
		switch b {
		case Left:
			f = mouseLeftUp
		case Middle:
			f = mouseMiddleUp
		case Right:
			f = mouseRightUp
		default:
			f, data = mouseXUp, uint32(b-Back+1)
		}
		in = append(in, mouse(f, 0, 0, data))
	}
	clear(w.buttons)
	return sendInputs(in)
}

// Close releases everything held.
func (w *Injector) Close() error { return w.ReleaseAll() }
