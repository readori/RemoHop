//go:build !linux && !windows && !darwin

package inject

import (
	"context"
	"fmt"
	"runtime"

	"github.com/readori/RemoHop/internal/x11disp"
)

// Injector is unavailable on this platform: Open always fails.
type Injector struct{}

func errUnsupported() error {
	return fmt.Errorf("input injection is not supported on %s hosts", runtime.GOOS)
}

// Open reports that input injection is not supported here.
func Open(context.Context, *x11disp.Display) (*Injector, error) { return nil, errUnsupported() }

func (*Injector) MoveTo(int, int) error               { return errUnsupported() }
func (*Injector) Pointer() (int, int, error)          { return 0, 0, errUnsupported() }
func (*Injector) Button(Button, bool) error           { return errUnsupported() }
func (*Injector) Scroll(int, int) error               { return errUnsupported() }
func (*Injector) KeyEvdev(uint16, bool) error         { return errUnsupported() }
func (*Injector) NeedsShift(Keysym) bool              { return false }
func (*Injector) KeySym(Keysym, bool) error           { return errUnsupported() }
func (*Injector) TypeRune(rune) error                 { return errUnsupported() }
func (*Injector) ReleaseAll() error                   { return nil }
func (*Injector) Close() error                        { return nil }
func (*Injector) MoveToF(float64, float64) error      { return errUnsupported() }
func (*Injector) PointerF() (float64, float64, error) { return 0, 0, errUnsupported() }
