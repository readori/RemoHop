package nativeui

import (
	"errors"
	"testing"
)

// Without internal/webview (this test binary, the remohop command) nothing
// is registered: the app must fall back to a browser.
func TestUnregistered(t *testing.T) {
	if _, err := Probe(); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Probe: %v", err)
	}
	called := false
	if err := Run(Config{}, func(App) { called = true }); !errors.Is(err, ErrUnavailable) || called {
		t.Fatalf("Run: %v (setup called: %v)", err, called)
	}
}

func TestSize(t *testing.T) {
	if w, h := (&Options{}).Size(); w != 960 || h != 680 {
		t.Fatalf("default size %dx%d", w, h)
	}
	if w, h := (&Options{Width: 420, Height: 56}).Size(); w != 420 || h != 56 {
		t.Fatalf("size %dx%d", w, h)
	}
}

func TestFitArea(t *testing.T) {
	for _, c := range []struct {
		w, h, minW, minH, aw, ah int
		want                     [4]int
	}{
		{1280, 800, 800, 600, 1920, 1040, [4]int{1280, 800, 800, 600}}, // fits
		{1280, 800, 800, 600, 1366, 728, [4]int{1256, 669, 800, 600}},  // a 1366x768 laptop
		{1280, 800, 800, 600, 1280, 680, [4]int{1177, 625, 800, 600}},  // 1280x720
		{1280, 800, 800, 600, 700, 500, [4]int{700, 500, 700, 500}},    // smaller than the minimum
		{1280, 800, 800, 600, 0, 0, [4]int{1280, 800, 800, 600}},       // unknown area
	} {
		w, h, mw, mh := FitArea(c.w, c.h, c.minW, c.minH, c.aw, c.ah)
		if got := [4]int{w, h, mw, mh}; got != c.want {
			t.Errorf("FitArea(%d,%d,%d,%d in %dx%d) = %v, want %v", c.w, c.h, c.minW, c.minH, c.aw, c.ah, got, c.want)
		}
	}
}
