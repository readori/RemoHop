package input

import (
	"testing"

	"github.com/readori/RemoHop/internal/inject"
	"github.com/readori/RemoHop/internal/svc/screenshot"
)

// macOS geometry, translated without a Mac: a Retina main display and a 1x
// display left of and above it, as CGDisplayBounds and the display modes
// would report them.
func TestMacCoordinates(t *testing.T) {
	geom := []inject.DisplayGeometry{
		{Width: 1440, Height: 900, PixelWidth: 2880, PixelHeight: 1800, Main: true},
		{X: -1920, Y: -300, Width: 1920, Height: 1080, PixelWidth: 1920, PixelHeight: 1080},
	}
	ds := macDisplays(geom)
	if ds[0].Name != "main" || !ds[0].Primary || ds[0].Width != 2880 || ds[0].Height != 1800 ||
		ds[1].Index != 1 || ds[1].Width != 1920 || ds[1].X != -1920 || ds[1].Y != -300 {
		t.Fatalf("displays %+v", ds)
	}
	space := hostSpace{geom: geom}
	for _, tc := range []struct {
		spec   string
		scale  *Size
		p      Point
		hx, hy float64 // global points
	}{
		// Full-size Retina screenshot pixel (1000,500) is point (500.25,250.25).
		{"", nil, Point{1000, 500}, 500.25, 250.25},
		{"main", nil, Point{2879, 1799}, 1439.75, 899.75},
		// A 1440x900 thumbnail of the Retina display: one image pixel is one
		// point; its centre lands on the lower-right pixel of the four.
		{"0", &Size{1440, 900}, Point{720, 450}, 720.75, 450.75},
		// The left display, 1x and at negative coordinates.
		{"1", nil, Point{0, 0}, -1919.5, -299.5},
		{"1", &Size{960, 540}, Point{480, 270}, -958.5, 241.5},
	} {
		d, err := screenshot.SelectDisplay(ds, tc.spec)
		if err != nil {
			t.Fatal(err)
		}
		if err := space.check(d); err != nil {
			t.Fatal(err)
		}
		at, _, err := toDisplay(d, tc.scale, tc.p)
		if err != nil {
			t.Fatal(err)
		}
		hx, hy := space.toHost(d, at)
		if hx != tc.hx || hy != tc.hy {
			t.Errorf("%q %v %v: points %v,%v, want %v,%v", tc.spec, tc.scale, tc.p, hx, hy, tc.hx, tc.hy)
		}
		// The pointer read back at that point reports the same pixel, and
		// the same thumbnail pixel.
		onDisp, scaled, inside := fromVirtual(d, tc.scale, space.fromHost(d, hx, hy))
		if onDisp != at || !inside || (tc.scale != nil && *scaled != tc.p) {
			t.Errorf("%q %v: read back %v %v %v, want %v", tc.spec, tc.p, onDisp, scaled, inside, at)
		}
	}
	// A pointer on the main display is off the left display.
	if _, _, inside := fromVirtual(ds[1], nil, space.fromHost(ds[1], 10, 10)); inside {
		t.Error("main-display point reported on the left display")
	}
	all, _ := screenshot.SelectDisplay(ds, "all")
	if err := space.check(all); err == nil {
		t.Error(`"all" accepted on macOS geometry`)
	}
	// Pixel hosts (X11, Windows) are unchanged: display origin plus pixel.
	px := hostSpace{}
	if x, y := px.toHost(ds[1], Point{5, 6}); x != -1915 || y != -294 {
		t.Errorf("pixel space %v,%v", x, y)
	}
	if err := px.check(all); err != nil {
		t.Error(err)
	}
}

// Every key name a combo accepts has a macOS key, except the few keys a
// Mac keyboard does not have.
func TestMacKeyNames(t *testing.T) {
	missing := map[string]bool{"F21": true, "F22": true, "F23": true, "F24": true,
		"XF86AudioPlay": true, "XF86AudioNext": true, "XF86AudioPrev": true}
	seen := map[string]bool{}
	for _, d := range keyIndex {
		if seen[d.name] {
			continue
		}
		seen[d.name] = true
		if _, _, ok := inject.MacKeyFor(d.sym); ok == missing[d.name] {
			t.Errorf("%s: mapped=%v", d.name, ok)
		}
	}
	for r := 'a'; r <= 'z'; r++ {
		if _, shift, ok := inject.MacKeyFor(inject.Keysym(r)); !ok || shift {
			t.Errorf("%c: %v %v", r, ok, shift)
		}
	}
}
