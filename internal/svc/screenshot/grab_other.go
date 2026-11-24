//go:build !linux && !darwin && !windows

package screenshot

import (
	"context"
	"fmt"
	"image"
	"runtime"
)

type unsupportedGrabber struct{}

func platformGrabber() grabber { return unsupportedGrabber{} }

func (unsupportedGrabber) Displays(context.Context) ([]Display, error) {
	return nil, fmt.Errorf("screenshot: not supported on %s", runtime.GOOS)
}

func (unsupportedGrabber) Grab(context.Context, int) (image.Image, error) {
	return nil, fmt.Errorf("screenshot: not supported on %s", runtime.GOOS)
}
