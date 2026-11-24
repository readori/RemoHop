package input

import (
	"context"
	"fmt"
	"os"

	"github.com/readori/RemoHop/internal/inject"
	"github.com/readori/RemoHop/internal/svc/screenshot"
)

// openBackend connects to the X display x11disp finds and lists its
// monitors on the same connection the injector then uses, so coordinates
// and injection always refer to the same screen.
func openBackend(ctx context.Context) (*backend, error) {
	d, err := inject.FindDisplay(ctx)
	if err != nil {
		return nil, err
	}
	c, err := d.Open(ctx)
	if err != nil {
		return nil, fmt.Errorf("connect to %w", err)
	}
	ds := screenshot.X11Displays(c)
	inj, err := inject.NewX11(c)
	if err != nil {
		return nil, err
	}
	b := &backend{inj: inj, displays: ds}
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		b.warning = "the host runs Wayland: input reaches only X11 (XWayland) windows"
	}
	return b, nil
}
