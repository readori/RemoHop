package inject

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/wintest"
)

// TestWinE2E drives the real SendInput on a Windows desktop. It needs an
// interactive session, so it only runs with REMOHOP_INPUT_E2E=1 (the CI
// runners have one; a service session does not). It moves the pointer and
// reads it back with GetCursorPos (independent of the injector), then types
// into a real text box through every injector path (Unicode text, a viewer
// scan code, a keysym with a held Shift, Return and Tab) and reads the box's
// text back.
func TestWinE2E(t *testing.T) {
	if os.Getenv("REMOHOP_INPUT_E2E") != "1" {
		t.Skip("set REMOHOP_INPUT_E2E=1 to post real input events on this Windows desktop")
	}
	t.Logf("desktop: %s", wintest.Describe())
	inj, err := Open(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer inj.Close()
	sw, sh := wintest.ScreenSize()
	if sw == 0 || sh == 0 {
		t.Fatalf("GetSystemMetrics reports a %dx%d screen: no display on this session", sw, sh)
	}

	pointerAt := func(x, y int) (gx, gy int) {
		deadline := time.Now().Add(2 * time.Second)
		for {
			gx, gy, _ = wintest.CursorPos()
			if (gx == x && gy == y) || time.Now().After(deadline) {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	for _, p := range [][2]int{{sw/2 + 7, sh/2 + 3}, {0, 0}, {sw - 1, sh - 1}, {sw - 1, 0}, {1, sh - 2}, {123, 456}} {
		if err := inj.MoveTo(p[0], p[1]); err != nil {
			t.Fatalf("MoveTo %v: %v", p, err)
		}
		if gx, gy := pointerAt(p[0], p[1]); gx != p[0] || gy != p[1] {
			t.Errorf("GetCursorPos is %d,%d after MoveTo %d,%d (screen %dx%d)", gx, gy, p[0], p[1], sw, sh)
		}
		if ix, iy, err := inj.Pointer(); err != nil || ix != p[0] || iy != p[1] {
			t.Errorf("Injector.Pointer is %d,%d (%v) after MoveTo %d,%d", ix, iy, err, p[0], p[1])
		}
	}
	if err := inj.MoveToF(300.4, 200.6); err != nil {
		t.Fatal(err)
	}
	if gx, gy := pointerAt(300, 201); gx != 300 || gy != 201 {
		t.Errorf("MoveToF(300.4, 200.6) put the pointer at %d,%d", gx, gy)
	}

	// Typing into a real window.
	w := wintest.Edit(t)
	cx, cy := w.Center()
	t.Logf("text box at %v", w.Bounds)
	if err := inj.MoveTo(cx, cy); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if err := inj.Button(Left, true); err != nil {
		t.Fatal(err)
	}
	if err := inj.Button(Left, false); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	for _, r := range "Ünï世界😀" {
		if err := inj.TypeRune(r); err != nil {
			t.Fatalf("TypeRune %q: %v", r, err)
		}
		time.Sleep(15 * time.Millisecond)
	}
	if err := inj.KeyEvdev(30, true); err != nil { // KEY_A, a physical key: "a" on a US layout
		t.Fatal(err)
	}
	if err := inj.KeyEvdev(30, false); err != nil {
		t.Fatal(err)
	}
	bSym, _ := RuneKeysym('b')
	if inj.NeedsShift(bSym) {
		t.Error("NeedsShift('b') on the active layout")
	}
	for _, step := range []struct {
		sym  Keysym
		down bool
	}{{KeysymShiftL, true}, {bSym, true}, {bSym, false}, {KeysymShiftL, false}} {
		if err := inj.KeySym(step.sym, step.down); err != nil {
			t.Fatal(err)
		}
	}
	if err := inj.TypeRune('\n'); err != nil {
		t.Fatal(err)
	}
	if err := inj.TypeRune('\t'); err != nil {
		t.Fatal(err)
	}
	want := "Ünï世界😀aB\n\t"
	if got := w.WaitText(want, 10*time.Second); got != want {
		t.Fatalf("the text box holds %q, want %q\nhelper log: %s\ndesktop: %s", got, want, w.Log(), wintest.Describe())
	}

	// Nothing may stay held: after ReleaseAll a held Shift is gone.
	if err := inj.KeySym(KeysymShiftL, true); err != nil {
		t.Fatal(err)
	}
	if err := inj.ReleaseAll(); err != nil {
		t.Fatal(err)
	}
	if len(inj.keys) != 0 || len(inj.buttons) != 0 {
		t.Errorf("injector still tracks keys %v buttons %v", inj.keys, inj.buttons)
	}
	if err := inj.KeyEvdev(45, true); err != nil { // KEY_X
		t.Fatal(err)
	}
	if err := inj.KeyEvdev(45, false); err != nil {
		t.Fatal(err)
	}
	if got := w.WaitText(want+"x", 5*time.Second); got != want+"x" {
		t.Errorf("after ReleaseAll typing x gave %q (a stuck Shift would give an X)", got)
	}
}
