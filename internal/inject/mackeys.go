package inject

// macOS virtual key codes (kVK_* from HIToolbox/Events.h). They name
// physical key positions of an ANSI (US) keyboard; the host's layout decides
// what a position types, exactly as for a local keyboard. The tables live in
// a file built on every OS so their tests run everywhere.
const (
	kvkReturn       = 0x24
	kvkTab          = 0x30
	kvkSpace        = 0x31
	kvkDelete       = 0x33 // Backspace
	kvkEscape       = 0x35
	kvkRightCommand = 0x36
	kvkCommand      = 0x37
	kvkShift        = 0x38
	kvkCapsLock     = 0x39
	kvkOption       = 0x3a
	kvkControl      = 0x3b
	kvkRightShift   = 0x3c
	kvkRightOption  = 0x3d
	kvkRightControl = 0x3e
	kvkFunction     = 0x3f
	kvkKeypadEnter  = 0x4c
	kvkKeypadClear  = 0x47
	kvkHelp         = 0x72 // the Insert position on PC keyboards
	kvkHome         = 0x73
	kvkPageUp       = 0x74
	kvkForwardDel   = 0x75
	kvkEnd          = 0x77
	kvkPageDown     = 0x79
	kvkLeftArrow    = 0x7b
	kvkRightArrow   = 0x7c
	kvkDownArrow    = 0x7d
	kvkUpArrow      = 0x7e
	kvkContextMenu  = 0x6e
	kvkVolumeUp     = 0x48
	kvkVolumeDown   = 0x49
	kvkMute         = 0x4a
)

// CGEventFlags of the modifiers (kCGEventFlagMask*).
const (
	macFlagShift   uint64 = 0x00020000
	macFlagControl uint64 = 0x00040000
	macFlagOption  uint64 = 0x00080000
	macFlagCommand uint64 = 0x00100000
	macFlagFn      uint64 = 0x00800000
)

// macFKeys are kVK_F1..kVK_F20 (macOS has no F21-F24).
var macFKeys = [20]uint16{
	0x7a, 0x78, 0x63, 0x76, 0x60, 0x61, 0x62, 0x64, 0x65, 0x6d,
	0x67, 0x6f, 0x69, 0x6b, 0x71, 0x6a, 0x40, 0x4f, 0x50, 0x5a,
}

// macUSChars maps the characters of a US layout to their key, and whether
// Shift is part of it.
var macUSChars = func() map[rune]macKey {
	m := map[rune]macKey{}
	plain := map[rune]uint16{
		'a': 0x00, 's': 0x01, 'd': 0x02, 'f': 0x03, 'h': 0x04, 'g': 0x05, 'z': 0x06, 'x': 0x07,
		'c': 0x08, 'v': 0x09, 'b': 0x0b, 'q': 0x0c, 'w': 0x0d, 'e': 0x0e, 'r': 0x0f, 'y': 0x10,
		't': 0x11, '1': 0x12, '2': 0x13, '3': 0x14, '4': 0x15, '6': 0x16, '5': 0x17, '=': 0x18,
		'9': 0x19, '7': 0x1a, '-': 0x1b, '8': 0x1c, '0': 0x1d, ']': 0x1e, 'o': 0x1f, 'u': 0x20,
		'[': 0x21, 'i': 0x22, 'p': 0x23, 'l': 0x25, 'j': 0x26, '\'': 0x27, 'k': 0x28, ';': 0x29,
		'\\': 0x2a, ',': 0x2b, '/': 0x2c, 'n': 0x2d, 'm': 0x2e, '.': 0x2f, '`': 0x32, ' ': kvkSpace,
	}
	shifted := map[rune]rune{
		'!': '1', '@': '2', '#': '3', '$': '4', '%': '5', '^': '6', '&': '7', '*': '8', '(': '9', ')': '0',
		'_': '-', '+': '=', '{': '[', '}': ']', '|': '\\', ':': ';', '"': '\'', '<': ',', '>': '.', '?': '/', '~': '`',
	}
	for r, vk := range plain {
		m[r] = macKey{vk: vk}
		if r >= 'a' && r <= 'z' {
			m[r-'a'+'A'] = macKey{vk: vk, shift: true}
		}
	}
	for r, base := range shifted {
		m[r] = macKey{vk: plain[base], shift: true}
	}
	return m
}()

// macKey is a virtual key, whether it needs Shift, and the modifier flag it
// sets while held (0 for ordinary keys).
type macKey struct {
	vk    uint16
	shift bool
	flag  uint64
}

// macKeysyms maps the non-character keysyms of key combos to keys. PC keys
// without a Mac counterpart use the usual equivalents: Insert is Help,
// Print/Scroll_Lock/Pause are F13/F14/F15, Num_Lock is Clear, AltGr is the
// right Option key.
var macKeysyms = func() map[Keysym]macKey {
	m := map[Keysym]macKey{
		0xffe3: {vk: kvkControl, flag: macFlagControl}, 0xffe4: {vk: kvkRightControl, flag: macFlagControl},
		0xffe1: {vk: kvkShift, flag: macFlagShift}, 0xffe2: {vk: kvkRightShift, flag: macFlagShift},
		0xffe9: {vk: kvkOption, flag: macFlagOption}, 0xffea: {vk: kvkRightOption, flag: macFlagOption},
		0xffeb: {vk: kvkCommand, flag: macFlagCommand}, 0xffec: {vk: kvkRightCommand, flag: macFlagCommand},
		0xfe03: {vk: kvkRightOption, flag: macFlagOption},
		0xff0d: {vk: kvkReturn}, 0xff8d: {vk: kvkKeypadEnter}, 0xff09: {vk: kvkTab},
		0xff1b: {vk: kvkEscape}, 0xff08: {vk: kvkDelete}, 0xffff: {vk: kvkForwardDel},
		0xff63: {vk: kvkHelp}, 0xff50: {vk: kvkHome}, 0xff57: {vk: kvkEnd},
		0xff55: {vk: kvkPageUp}, 0xff56: {vk: kvkPageDown},
		0xff51: {vk: kvkLeftArrow}, 0xff52: {vk: kvkUpArrow}, 0xff53: {vk: kvkRightArrow}, 0xff54: {vk: kvkDownArrow},
		0xff67: {vk: kvkContextMenu}, 0xffe5: {vk: kvkCapsLock}, 0xff7f: {vk: kvkKeypadClear},
		0xff61: {vk: macFKeys[12]}, 0xff14: {vk: macFKeys[13]}, 0xff13: {vk: macFKeys[14]},
		0x1008ff12: {vk: kvkMute}, 0x1008ff11: {vk: kvkVolumeDown}, 0x1008ff13: {vk: kvkVolumeUp},
	}
	for i, vk := range macFKeys {
		m[0xffbe+Keysym(i)] = macKey{vk: vk}
	}
	return m
}()

// macKeyForKeysym finds the key producing sym on a US-position keyboard.
func macKeyForKeysym(sym Keysym) (macKey, bool) {
	if k, ok := macKeysyms[sym]; ok {
		return k, true
	}
	if r, ok := KeysymRune(sym); ok {
		k, ok := macUSChars[r]
		return k, ok
	}
	return macKey{}, false
}

// macEvdev maps Linux evdev codes (the desktop viewer's physical keys) to
// macOS virtual keys.
var macEvdev = func() map[uint16]macKey {
	m := map[uint16]macKey{
		1: {vk: kvkEscape}, 14: {vk: kvkDelete}, 15: {vk: kvkTab}, 28: {vk: kvkReturn},
		29: {vk: kvkControl, flag: macFlagControl}, 42: {vk: kvkShift, flag: macFlagShift},
		54: {vk: kvkRightShift, flag: macFlagShift}, 56: {vk: kvkOption, flag: macFlagOption},
		57: {vk: kvkSpace}, 58: {vk: kvkCapsLock}, 69: {vk: kvkKeypadClear},
		97: {vk: kvkRightControl, flag: macFlagControl}, 100: {vk: kvkRightOption, flag: macFlagOption},
		125: {vk: kvkCommand, flag: macFlagCommand}, 126: {vk: kvkRightCommand, flag: macFlagCommand},
		102: {vk: kvkHome}, 103: {vk: kvkUpArrow}, 104: {vk: kvkPageUp}, 105: {vk: kvkLeftArrow},
		106: {vk: kvkRightArrow}, 107: {vk: kvkEnd}, 108: {vk: kvkDownArrow}, 109: {vk: kvkPageDown},
		110: {vk: kvkHelp}, 111: {vk: kvkForwardDel}, 113: {vk: kvkMute}, 114: {vk: kvkVolumeDown},
		115: {vk: kvkVolumeUp}, 127: {vk: kvkContextMenu}, 138: {vk: kvkHelp}, 464: {vk: kvkFunction, flag: macFlagFn},
		// Keypad.
		55: {vk: 0x43}, 74: {vk: 0x4e}, 78: {vk: 0x45}, 83: {vk: 0x41}, 96: {vk: kvkKeypadEnter},
		98: {vk: 0x4b}, 117: {vk: 0x51}, 121: {vk: 0x5f},
		82: {vk: 0x52}, 79: {vk: 0x53}, 80: {vk: 0x54}, 81: {vk: 0x55}, 75: {vk: 0x56},
		76: {vk: 0x57}, 77: {vk: 0x58}, 71: {vk: 0x59}, 72: {vk: 0x5b}, 73: {vk: 0x5c},
		// ISO and JIS extras: the key left of Z, Yen, Ro, Eisu/Kana.
		86: {vk: 0x0a}, 124: {vk: 0x5d}, 89: {vk: 0x5e}, 122: {vk: 0x68}, 123: {vk: 0x66},
		// PC-only keys: the conventional Mac equivalents.
		99: {vk: macFKeys[12]}, 70: {vk: macFKeys[13]}, 119: {vk: macFKeys[14]},
	}
	// The character block, by the US character each evdev key carries.
	for ev, r := range map[uint16]rune{
		2: '1', 3: '2', 4: '3', 5: '4', 6: '5', 7: '6', 8: '7', 9: '8', 10: '9', 11: '0',
		12: '-', 13: '=', 16: 'q', 17: 'w', 18: 'e', 19: 'r', 20: 't', 21: 'y', 22: 'u', 23: 'i',
		24: 'o', 25: 'p', 26: '[', 27: ']', 30: 'a', 31: 's', 32: 'd', 33: 'f', 34: 'g', 35: 'h',
		36: 'j', 37: 'k', 38: 'l', 39: ';', 40: '\'', 41: '`', 43: '\\', 44: 'z', 45: 'x', 46: 'c',
		47: 'v', 48: 'b', 49: 'n', 50: 'm', 51: ',', 52: '.', 53: '/',
	} {
		m[ev] = macKey{vk: macUSChars[r].vk}
	}
	// F1-F10 are evdev 59-68, F11/F12 87/88, F13-F20 183-190.
	for i := range 10 {
		m[uint16(59+i)] = macKey{vk: macFKeys[i]}
	}
	m[87], m[88] = macKey{vk: macFKeys[10]}, macKey{vk: macFKeys[11]}
	for i := range 8 {
		m[uint16(183+i)] = macKey{vk: macFKeys[12+i]}
	}
	return m
}()

// MacKeyFor reports the macOS virtual key (kVK_*) that produces sym on US
// key positions, and whether Shift is part of it.
func MacKeyFor(sym Keysym) (vk uint16, shift, ok bool) {
	k, ok := macKeyForKeysym(sym)
	return k.vk, k.shift, ok
}

// MacKeyForEvdev reports the macOS virtual key at the position of the
// Linux evdev key code.
func MacKeyForEvdev(code uint16) (vk uint16, ok bool) {
	k, ok := macEvdev[code]
	return k.vk, ok
}
