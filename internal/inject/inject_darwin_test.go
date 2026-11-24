package inject

import (
	"context"
	"math"
	"os"
	"testing"
	"time"
)

// TestMacE2E drives the real Quartz event system. It needs a logged-in
// desktop and, to post events, the Accessibility permission, so it only
// runs with REMOHOP_INPUT_E2E=1 and skips (with the message a host owner
// would get) when the permission is missing. It moves the pointer and
// reads it back, holds and releases modifiers, and checks nothing stays
// held; it does not click or type characters, so whatever has the focus on
// the machine is left alone.
func TestMacE2E(t *testing.T) {
	if os.Getenv("REMOHOP_INPUT_E2E") != "1" {
		t.Skip("set REMOHOP_INPUT_E2E=1 to post real input events on this Mac")
	}
	trusted, err := AccessibilityTrusted()
	if err != nil {
		t.Fatalf("loading the macOS frameworks: %v", err)
	}
	t.Logf("AXIsProcessTrusted = %v", trusted)
	ds, err := MacDisplays()
	if err != nil {
		t.Fatal(err)
	}
	for i, d := range ds {
		t.Logf("display %d: %+v (scale %.2f)", i, d, float64(d.PixelWidth)/d.Width)
	}
	if !trusted {
		if _, err := Open(context.Background(), nil); err != ErrNoAccessibility {
			t.Fatalf("Open without the permission: %v", err)
		}
		t.Skip(ErrNoAccessibility.Error())
	}
	inj, err := Open(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer inj.Close()

	// Move to a pixel near the centre of the main display and read the
	// pointer back (CGEventGetLocation of a fresh event).
	main := ds[0]
	px, py := main.PixelWidth/2+7, main.PixelHeight/2+3
	x, y := main.PixelToPoint(px, py)
	if err := inj.MoveToF(x, y); err != nil {
		t.Fatal(err)
	}
	var gx, gy float64
	deadline := time.Now().Add(2 * time.Second)
	for {
		gx, gy, _ = inj.PointerF()
		if math.Abs(gx-x) < 0.6 && math.Abs(gy-y) < 0.6 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if math.Abs(gx-x) >= 0.6 || math.Abs(gy-y) >= 0.6 {
		t.Fatalf("pointer at %v,%v after moving to %v,%v", gx, gy, x, y)
	}
	if gpx, gpy := main.PointToPixel(gx, gy); gpx != px || gpy != py {
		t.Errorf("pointer is on pixel %d,%d, want %d,%d", gpx, gpy, px, py)
	}

	// Hold Shift and Control (one by keysym, one as a viewer key), then
	// release everything: the HID state must show no key down.
	if err := inj.KeySym(KeysymShiftL, true); err != nil {
		t.Fatal(err)
	}
	if err := inj.KeyEvdev(29, true); err != nil { // KEY_LEFTCTRL
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	t.Logf("while held: shift=%v control=%v (HID state)", cgEventSourceKeyState(cgEventSourceStateHID, kvkShift),
		cgEventSourceKeyState(cgEventSourceStateHID, kvkControl))
	if err := inj.Close(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	for _, vk := range []uint16{kvkShift, kvkControl, kvkCommand, kvkOption} {
		if cgEventSourceKeyState(cgEventSourceStateHID, vk) {
			t.Errorf("key %#x still down after Close", vk)
		}
	}
	for b := uint32(0); b < 3; b++ {
		if cgEventSourceButtonState(cgEventSourceStateHID, b) {
			t.Errorf("mouse button %d still down", b)
		}
	}
	if len(inj.keys) != 0 || len(inj.buttons) != 0 {
		t.Errorf("injector still tracks keys %v buttons %v", inj.keys, inj.buttons)
	}
}
