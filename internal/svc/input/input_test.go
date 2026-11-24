package input

import (
	"strings"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/svc/screenshot"
)

func TestToDisplay(t *testing.T) {
	// A monitor left of and above the primary, as Windows and X11 allow.
	left := screenshot.Display{Index: 1, Name: "DP-1", X: -1920, Y: -200, Width: 1920, Height: 1080}
	right := screenshot.Display{Index: 0, Name: "HDMI-1", X: 0, Y: 0, Width: 2560, Height: 1440, Primary: true}
	for _, tc := range []struct {
		d       screenshot.Display
		scale   *Size
		p       Point
		onDisp  Point
		virtual Point
	}{
		{right, nil, Point{0, 0}, Point{0, 0}, Point{0, 0}},
		{right, nil, Point{2559, 1439}, Point{2559, 1439}, Point{2559, 1439}},
		{left, nil, Point{10, 20}, Point{10, 20}, Point{-1910, -180}},
		{left, nil, Point{1919, 1079}, Point{1919, 1079}, Point{-1, 879}},
		// Half-size screenshot: pixel centres map to the lower-right of
		// the two display pixels they cover.
		{right, &Size{1280, 720}, Point{640, 360}, Point{1281, 721}, Point{1281, 721}},
		{right, &Size{1280, 720}, Point{0, 0}, Point{1, 1}, Point{1, 1}},
		{right, &Size{1280, 720}, Point{1279, 719}, Point{2559, 1439}, Point{2559, 1439}},
		// Non-integer ratio (1920 -> 1568 wide).
		{left, &Size{1568, 882}, Point{784, 441}, Point{960, 540}, Point{-960, 340}},
		// Upscaled source never leaves the display.
		{left, &Size{3840, 2160}, Point{3839, 2159}, Point{1919, 1079}, Point{-1, 879}},
	} {
		onDisp, v, err := toDisplay(tc.d, tc.scale, tc.p)
		if err != nil || onDisp != tc.onDisp || v != tc.virtual {
			t.Errorf("%s %v %v: got %v %v %v, want %v %v", tc.d.Name, tc.scale, tc.p, onDisp, v, err, tc.onDisp, tc.virtual)
		}
	}
	for _, tc := range []struct {
		scale *Size
		p     Point
		want  string
	}{
		{nil, Point{2560, 0}, "outside display 0 (HDMI-1) (2560x1440)"},
		{nil, Point{0, 1440}, "outside display"},
		{nil, Point{-1, 0}, "outside display"},
		{&Size{1280, 720}, Point{1280, 0}, "outside the 1280x720 image"},
	} {
		if _, _, err := toDisplay(right, tc.scale, tc.p); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v %v: %v, want %q", tc.scale, tc.p, err, tc.want)
		}
	}

	// Back from the virtual screen.
	onDisp, scaled, inside := fromVirtual(right, &Size{1280, 720}, Point{1281, 721})
	if onDisp != (Point{1281, 721}) || *scaled != (Point{640, 360}) || !inside {
		t.Fatalf("fromVirtual %v %v %v", onDisp, scaled, inside)
	}
	onDisp, scaled, inside = fromVirtual(left, nil, Point{100, 100})
	if onDisp != (Point{2020, 300}) || scaled != nil || inside {
		t.Fatalf("fromVirtual off-display %v %v %v", onDisp, scaled, inside)
	}
}

func TestParseSize(t *testing.T) {
	if s, err := ParseSize("1280x720"); err != nil || s != (Size{1280, 720}) {
		t.Fatalf("%v %v", s, err)
	}
	if s, err := ParseSize(" 800X600 "); err != nil || s != (Size{800, 600}) {
		t.Fatalf("%v %v", s, err)
	}
	for _, bad := range []string{"", "1280", "x720", "0x5", "-5x5", "axb", "100000x5"} {
		if _, err := ParseSize(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestValidate(t *testing.T) {
	good := []request{
		clickReq(1, 2, ClickOptions{}),
		clickReq(1, 2, ClickOptions{Button: "right", Double: true}),
		moveReq(0, 0, MoveOptions{Target: Target{Display: "HDMI-1", ScaleFrom: Size{10, 10}}}),
		typeReq("hello\r\nworld\t", TypeOptions{}),
		typeReq(strings.Repeat("é", MaxTextRunes), TypeOptions{}),
		keyReq([]string{"ctrl+c"}, KeyOptions{Repeat: MaxCombos}),
		scrollReq(0, -MaxScroll, ScrollOptions{}),
	}
	for _, r := range good {
		if _, err := r.validate(); err != nil {
			t.Errorf("%+v: %v", r.Op, err)
		}
	}
	p, _ := typeReq("a\r\nb\rc", TypeOptions{}).validate()
	if string(p.text) != "a\nb\rc" || p.delay != DefaultTypeDelay {
		t.Errorf("text %q delay %v", string(p.text), p.delay)
	}
	if p, _ := typeReq("a", TypeOptions{Delay: time.Nanosecond}).validate(); p.delay != 0 {
		t.Errorf("explicit zero delay became %v", p.delay)
	}
	bad := []request{
		{Op: "launch"},
		{Op: opClick},
		clickReq(-1, 2, ClickOptions{}),
		clickReq(1, 2, ClickOptions{Button: "side"}),
		{Op: opClick, At: &Point{1, 1}, Clicks: 9},
		moveReq(1, 1<<16, MoveOptions{}),
		moveReq(1, 1, MoveOptions{Target: Target{ScaleFrom: Size{-1, 5}}}),
		moveReq(1, 1, MoveOptions{Target: Target{Display: strings.Repeat("d", 300)}}),
		typeReq("", TypeOptions{}),
		typeReq("a\x00b", TypeOptions{}),
		typeReq("\xff", TypeOptions{}),
		typeReq(strings.Repeat("a", MaxTextRunes+1), TypeOptions{}),
		typeReq(strings.Repeat("a", 1000), TypeOptions{Delay: time.Second}),
		typeReq("a", TypeOptions{Delay: 2 * time.Second}),
		keyReq(nil, KeyOptions{}),
		keyReq([]string{"a", "b"}, KeyOptions{Repeat: MaxCombos}),
		keyReq([]string{"hyper+a"}, KeyOptions{}),
		{Op: opKey, Keys: []string{"a"}, Repeat: -1},
		scrollReq(0, 0, ScrollOptions{}),
		scrollReq(MaxScroll+1, 0, ScrollOptions{}),
		{Op: opType, Text: "a", DelayMS: -2},
	}
	for _, r := range bad {
		if _, err := r.validate(); err == nil {
			t.Errorf("accepted %+v", r)
		}
	}
}
