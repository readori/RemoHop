//go:build !linux && !windows && !darwin

package desktop

import (
	"fmt"
	"runtime"
)

func discoverCapture(display, fps int) (captureSpec, error) {
	return captureSpec{}, fmt.Errorf("desktop capture is not supported on %s hosts", runtime.GOOS)
}

func newInjector(captureSpec) (injector, error) {
	return nil, fmt.Errorf("input injection is not supported on %s hosts", runtime.GOOS)
}
