// Package inject synthesises pointer and keyboard input on the host's
// desktop. It is the one implementation behind the desktop viewer (raw
// events streamed from a browser) and the input service (single actions
// from the CLI and the MCP server).
//
// Backends: XTEST on Linux/X11, SendInput on Windows, Quartz events on
// macOS (CoreGraphics through purego, no cgo); other systems get an
// Injector whose Open fails with a clear error. An Injector is not safe for
// concurrent use; callers serialise. Close always releases every key and
// button the Injector still holds, so an aborted shortcut or drag never
// leaves the host with a stuck Ctrl or mouse button.
//
// Coordinates are pixels of the virtual screen (all monitors), origin at
// its top-left on X11 and at the primary monitor's top-left on Windows,
// exactly as the screenshot service reports monitor geometry.
package inject

import (
	"os"
	"unicode/utf8"
)

// Button is a mouse button, numbered like X11 core buttons.
type Button uint8

// Mouse buttons. Wheel notches are sent with Scroll, not as buttons.
const (
	Left    Button = 1
	Middle  Button = 2
	Right   Button = 3
	Back    Button = 8
	Forward Button = 9
)

// Keysym identifies the symbol a key produces, with X11 keysym values on
// every platform (Windows translates them to virtual keys). Unicode
// characters outside Latin-1 use the X convention 0x01000000 + code point.
type Keysym uint32

// Keysyms the backends need to know by name.
const (
	KeysymReturn    Keysym = 0xff0d
	KeysymTab       Keysym = 0xff09
	KeysymShiftL    Keysym = 0xffe1
	KeysymShiftR    Keysym = 0xffe2
	KeysymControlL  Keysym = 0xffe3
	KeysymAltL      Keysym = 0xffe9
	KeysymSuperL    Keysym = 0xffeb
	KeysymLevel3    Keysym = 0xfe03 // ISO_Level3_Shift (AltGr)
	unicodeKeysymOf Keysym = 0x01000000
)

// RuneKeysym returns the keysym that types r: Latin-1 characters are their
// own keysyms, newline and tab are Return and Tab, anything else printable
// is a Unicode keysym. It reports false for control characters and invalid
// runes, which cannot be typed.
func RuneKeysym(r rune) (Keysym, bool) {
	switch {
	case r == '\n' || r == '\r':
		return KeysymReturn, true
	case r == '\t':
		return KeysymTab, true
	case r >= 0x20 && r <= 0x7e, r >= 0xa0 && r <= 0xff:
		return Keysym(r), true
	case r < 0x100, r == utf8.RuneError, !utf8.ValidRune(r), r >= 0xfdd0 && r <= 0xfdef, r&0xfffe == 0xfffe:
		// C0/C1 controls, DEL, invalid runes and noncharacters.
		return 0, false
	}
	return unicodeKeysymOf + Keysym(r), true
}

// KeysymRune is the inverse of RuneKeysym for printable keysyms (Return and
// Tab give '\n' and '\t'); ok is false for function keys and the like.
func KeysymRune(s Keysym) (rune, bool) {
	switch {
	case s == KeysymReturn:
		return '\n', true
	case s == KeysymTab:
		return '\t', true
	case s >= 0x20 && s <= 0x7e, s >= 0xa0 && s <= 0xff:
		return rune(s), true
	case s >= unicodeKeysymOf+0x100 && s <= unicodeKeysymOf+0x10ffff:
		return rune(s - unicodeKeysymOf), true
	}
	return 0, false
}

// ViewOnlyEnv is the host switch that refuses all input injection, for the
// desktop viewer and the input service alike.
const ViewOnlyEnv = "REMOHOP_DESKTOP_VIEW_ONLY"

// ViewOnly reports whether the host owner turned input off with
// ViewOnlyEnv (any value but "" and "0").
func ViewOnly() bool {
	v := os.Getenv(ViewOnlyEnv)
	return v != "" && v != "0"
}
