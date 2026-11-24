package input

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/peer/peertest"
	"github.com/readori/RemoHop/internal/wintest"
)

func winE2E(t *testing.T) {
	t.Helper()
	if os.Getenv("REMOHOP_INPUT_E2E") != "1" {
		t.Skip("set REMOHOP_INPUT_E2E=1 to post real input events on this Windows desktop")
	}
	t.Logf("desktop: %s", wintest.Describe())
}

// TestWinMoveE2E runs Move through a real session on this machine and
// checks, with GetCursorPos, that the pointer lands on the requested
// screenshot pixel at full size and through --scale-from.
func TestWinMoveE2E(t *testing.T) {
	winE2E(t)
	client, _ := peertest.Pair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := Move(ctx, client, 101, 51, MoveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("result %+v", res)
	if res.Pointer != (Point{101, 51}) || !res.PointerOnDisplay {
		t.Fatalf("pointer at %+v, want 101,51", res.Pointer)
	}
	if x, y, err := wintest.CursorPos(); err != nil || x != 101 || y != 51 {
		t.Fatalf("GetCursorPos is %d,%d (%v), want 101,51", x, y, err)
	}
	half := Size{res.Display.Width / 2, res.Display.Height / 2}
	res, err = Move(ctx, client, 200, 100, MoveOptions{Target: Target{ScaleFrom: half}})
	if err != nil {
		t.Fatal(err)
	}
	if res.PointerScaled == nil || *res.PointerScaled != (Point{200, 100}) {
		t.Fatalf("scaled pointer %+v (%+v)", res.PointerScaled, res)
	}
	if x, y, _ := wintest.CursorPos(); x != 401 || y != 201 {
		// A scaled pixel maps to the display pixel under its centre: (200+0.5)*2.
		t.Fatalf("GetCursorPos is %d,%d after a scaled move to 200,100 of a half-size image, want 401,201", x, y)
	}
	res, err = Move(ctx, client, 3, 4, MoveOptions{Target: Target{Display: "all"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.PointerVirtual != (Point{res.Display.X + 3, res.Display.Y + 4}) {
		t.Fatalf("display all: %+v", res)
	}
}

// TestWinTypeE2E clicks into and types in a real text box through the
// input service: text (including characters outside the layout), key
// combinations and repeated keys, and reads the box's text back.
func TestWinTypeE2E(t *testing.T) {
	winE2E(t)
	client, _ := peertest.Pair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	w := wintest.Edit(t)
	cx, cy := w.Center()
	t.Logf("text box at %v", w.Bounds)

	res, err := Click(ctx, client, cx, cy, ClickOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Pointer != (Point{cx, cy}) || res.Clicks != 1 {
		t.Fatalf("click result %+v", res)
	}
	time.Sleep(300 * time.Millisecond)

	const text = "Hello remohop, ÄÖü 你好 123!"
	if _, err := Type(ctx, client, text, TypeOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := w.WaitText(text, 10*time.Second); got != text {
		t.Fatalf("after Type the text box holds %q, want %q\nhelper log: %s\ndesktop: %s", got, text, w.Log(), wintest.Describe())
	}

	// ctrl+a selects everything, BackSpace deletes it.
	if _, err := Key(ctx, client, []string{"ctrl+a", "BackSpace"}, KeyOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := w.WaitText("", 10*time.Second); got != "" {
		t.Fatalf("after ctrl+a BackSpace the box holds %q\nhelper log: %s", got, w.Log())
	}
	if _, err := Type(ctx, client, "done", TypeOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := Key(ctx, client, []string{"Return", "shift+a", "Left", "Delete", "z", "z"}, KeyOptions{}); err != nil {
		t.Fatal(err)
	}
	// "done", newline, "A", Left, Delete removes the "A", then "zz".
	want := "done\nzz"
	if got := w.WaitText(want, 10*time.Second); got != want {
		t.Fatalf("after key combinations the box holds %q, want %q\nhelper log: %s", got, want, w.Log())
	}
	if _, err := Key(ctx, client, []string{"BackSpace"}, KeyOptions{Repeat: 3}); err != nil {
		t.Fatal(err)
	}
	want = "done"
	if got := w.WaitText(want, 10*time.Second); got != want {
		t.Fatalf("after BackSpace x3 the box holds %q, want %q", got, want)
	}
}
