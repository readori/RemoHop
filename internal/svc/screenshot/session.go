package screenshot

import (
	"context"
	"image"
)

// SessionGrabber captures a desktop this process cannot capture itself: the
// Windows service runs in session 0, so it captures the interactive console
// session through a helper process there (internal/unattended).
type SessionGrabber interface {
	// Displays lists the monitors, primary first, numbered from 0.
	Displays(ctx context.Context) ([]Display, error)
	// Grab captures a display by index, or every display (AllDisplays).
	Grab(ctx context.Context, display int) (image.Image, error)
}

// UseSessionGrabber makes this host's screenshot service, and everything
// that asks LocalDisplays, use g; nil restores the platform's own capture.
func UseSessionGrabber(g SessionGrabber) {
	if g == nil {
		setGrabber(platformGrabber())
		return
	}
	setGrabber(g)
}
