package input

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/inject"
	"github.com/readori/RemoHop/internal/peer/peertest"
)

// TestMacMoveE2E runs a Move through a real session on this Mac (see
// inject.TestMacE2E for the conditions) and checks the pointer lands on the
// requested screenshot pixel, at full size and through --scale-from.
func TestMacMoveE2E(t *testing.T) {
	if os.Getenv("REMOHOP_INPUT_E2E") != "1" {
		t.Skip("set REMOHOP_INPUT_E2E=1 to post real input events on this Mac")
	}
	client, _ := peertest.Pair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := Move(ctx, client, 101, 51, MoveOptions{})
	if errors.Is(err, inject.ErrNoAccessibility) || (err != nil && err.Error() == inject.ErrNoAccessibility.Error()) {
		t.Skip(err.Error())
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("result %+v", res)
	if res.Pointer != (Point{101, 51}) || !res.PointerOnDisplay {
		t.Fatalf("pointer at %+v, want 101,51", res.Pointer)
	}
	half := Size{res.Display.Width / 2, res.Display.Height / 2}
	res, err = Move(ctx, client, 200, 100, MoveOptions{Target: Target{ScaleFrom: half}})
	if err != nil {
		t.Fatal(err)
	}
	if res.PointerScaled == nil || *res.PointerScaled != (Point{200, 100}) {
		t.Fatalf("scaled pointer %+v (%+v)", res.PointerScaled, res)
	}
	if _, err := Move(ctx, client, 1, 1, MoveOptions{Target: Target{Display: "all"}}); err == nil {
		t.Fatal(`display "all" accepted on macOS`)
	}
}
