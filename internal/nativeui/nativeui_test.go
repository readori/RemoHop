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
