package inject

import "testing"

func TestMacKeyTables(t *testing.T) {
	for sym, want := range map[Keysym]macKey{
		'a': {vk: 0x00}, 'A': {vk: 0x00, shift: true}, 'z': {vk: 0x06}, 'q': {vk: 0x0c},
		'1': {vk: 0x12}, '!': {vk: 0x12, shift: true}, '0': {vk: 0x1d}, ')': {vk: 0x1d, shift: true},
		'=': {vk: 0x18}, '+': {vk: 0x18, shift: true}, '/': {vk: 0x2c}, '?': {vk: 0x2c, shift: true},
		'`': {vk: 0x32}, '~': {vk: 0x32, shift: true}, ' ': {vk: kvkSpace},
		KeysymReturn: {vk: kvkReturn}, KeysymTab: {vk: kvkTab}, 0xff1b: {vk: kvkEscape},
		0xff08: {vk: kvkDelete}, 0xffff: {vk: kvkForwardDel}, 0xff51: {vk: kvkLeftArrow},
		0xff55: {vk: kvkPageUp}, 0xff56: {vk: kvkPageDown}, 0xff50: {vk: kvkHome}, 0xff57: {vk: kvkEnd},
		0xffbe: {vk: 0x7a}, 0xffbe + 3: {vk: 0x76}, 0xffbe + 11: {vk: 0x6f}, 0xffbe + 19: {vk: 0x5a},
		KeysymControlL: {vk: kvkControl, flag: macFlagControl}, KeysymShiftL: {vk: kvkShift, flag: macFlagShift},
		KeysymAltL: {vk: kvkOption, flag: macFlagOption}, KeysymSuperL: {vk: kvkCommand, flag: macFlagCommand},
		KeysymLevel3: {vk: kvkRightOption, flag: macFlagOption},
	} {
		got, ok := macKeyForKeysym(sym)
		if !ok || got != want {
			t.Errorf("keysym %#x: %+v %v, want %+v", sym, got, ok, want)
		}
	}
	// Every printable ASCII character has a key; no two unshifted
	// characters share one.
	seen := map[uint16]rune{}
	for r := rune(0x20); r <= 0x7e; r++ {
		k, ok := macUSChars[r]
		if !ok {
			t.Errorf("no key for %q", r)
			continue
		}
		if k.shift {
			continue
		}
		if prev, dup := seen[k.vk]; dup {
			t.Errorf("%q and %q share key %#x", prev, r, k.vk)
		}
		seen[k.vk] = r
	}
	// What macOS has no key for is refused, not mapped to something else.
	for _, sym := range []Keysym{0xffbe + 20, 0xffbe + 23, 0x1008ff14, 0x1008ff17, 'é', 0x01004e16} {
		if _, ok := macKeyForKeysym(sym); ok {
			t.Errorf("keysym %#x mapped", sym)
		}
	}
	// Desktop viewer keys (evdev codes): the letters follow US positions,
	// modifiers carry their flags, F-keys and the navigation block exist.
	for ev, want := range map[uint16]macKey{
		30: {vk: 0x00}, 16: {vk: 0x0c}, 44: {vk: 0x06}, 2: {vk: 0x12}, 28: {vk: kvkReturn}, 1: {vk: kvkEscape},
		29: {vk: kvkControl, flag: macFlagControl}, 125: {vk: kvkCommand, flag: macFlagCommand},
		56: {vk: kvkOption, flag: macFlagOption}, 54: {vk: kvkRightShift, flag: macFlagShift},
		59: {vk: 0x7a}, 88: {vk: 0x6f}, 183: {vk: 0x69}, 190: {vk: 0x5a}, 103: {vk: kvkUpArrow},
		110: {vk: kvkHelp}, 111: {vk: kvkForwardDel}, 96: {vk: kvkKeypadEnter}, 86: {vk: 0x0a},
	} {
		if got, ok := macEvdev[ev]; !ok || got != want {
			t.Errorf("evdev %d: %+v %v, want %+v", ev, got, ok, want)
		}
	}
	for ev := uint16(1); ev <= 88; ev++ {
		if ev == 84 || ev == 85 {
			continue // unassigned / Zenkaku: no Mac key
		}
		if _, ok := macEvdev[ev]; !ok {
			t.Errorf("evdev %d unmapped", ev)
		}
	}
	if _, ok := MacKeyForEvdev(9999); ok {
		t.Error("bogus evdev mapped")
	}
}

func TestDisplayGeometry(t *testing.T) {
	// A Retina main display (1440x900 points, 2880x1800 pixels) and a 1x
	// display left of and above it.
	retina := DisplayGeometry{Width: 1440, Height: 900, PixelWidth: 2880, PixelHeight: 1800, Main: true}
	left := DisplayGeometry{X: -1920, Y: -300, Width: 1920, Height: 1080, PixelWidth: 1920, PixelHeight: 1080}
	// A scaled "looks like 1680x1050" mode with a 3360x2100 backing store.
	scaled := DisplayGeometry{X: 1440, Width: 1680, Height: 1050, PixelWidth: 3360, PixelHeight: 2100}
	for _, tc := range []struct {
		g      DisplayGeometry
		px, py int
		x, y   float64
	}{
		{retina, 0, 0, 0.25, 0.25},
		{retina, 1, 1, 0.75, 0.75},
		{retina, 2879, 1799, 1439.75, 899.75},
		{retina, 1440, 900, 720.25, 450.25},
		{left, 0, 0, -1919.5, -299.5},
		{left, 1919, 1079, -0.5, 779.5},
		{scaled, 100, 50, 1490.25, 25.25},
	} {
		x, y := tc.g.PixelToPoint(tc.px, tc.py)
		if x != tc.x || y != tc.y {
			t.Errorf("%+v pixel %d,%d -> %v,%v, want %v,%v", tc.g, tc.px, tc.py, x, y, tc.x, tc.y)
		}
		if px, py := tc.g.PointToPixel(x, y); px != tc.px || py != tc.py {
			t.Errorf("%+v round trip %d,%d -> %d,%d", tc.g, tc.px, tc.py, px, py)
		}
	}
	// A point on another display maps outside this one.
	if px, py := retina.PointToPixel(-10, 5); px != -20 || py != 10 {
		t.Errorf("off-display point -> %d,%d", px, py)
	}
	if px, _ := left.PointToPixel(0, 0); px != 1920 {
		t.Errorf("left edge -> %d", px)
	}
}
