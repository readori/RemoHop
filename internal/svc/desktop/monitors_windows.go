package desktop

import (
	"context"
	"errors"
	"time"

	"github.com/readori/RemoHop/internal/svc/screenshot"
)

// discoverCapture adds to the per-monitor capture (discoverCaptureMonitor,
// in capture_windows.go) what multi-monitor viewing needs: the list of
// monitors, the geometry that input is mapped with for a monitor other than
// the primary, and the all-monitors view.
//
// Monitors are numbered as the screenshot service numbers them (primary
// first), in virtual-desktop pixels. Absolute pointer positions are
// normalised over the whole virtual desktop by the injector, so a picture of
// monitor N or of the virtual desktop needs only its own origin and size.
// Not verified on a multi-monitor Windows machine.
func discoverCapture(display, fps int) (captureSpec, error) {
	if spec, ok, err := unattendedCapture(display, fps); ok {
		return spec, err // the service's console-session capture
	}
	ds := windowsMonitors()
	if display == DisplayAll {
		return allMonitorsSpec(ds, fps)
	}
	spec, err := discoverCaptureMonitor(display, fps)
	if err != nil {
		return spec, err
	}
	spec.displays = ds
	idx := max(display-1, 0)
	if spec.width == 0 && idx < len(ds) {
		// ddagrab names no geometry for a monitor other than the primary.
		spec.x, spec.y, spec.width, spec.height = ds[idx].X, ds[idx].Y, ds[idx].Width, ds[idx].Height
	}
	return spec, nil
}

// windowsMonitors lists the monitors, nil when they cannot be listed.
func windowsMonitors() []DisplayInfo {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ds, err := screenshot.LocalDisplays(ctx)
	if err != nil {
		return nil
	}
	out := make([]DisplayInfo, 0, len(ds))
	for _, d := range ds {
		out = append(out, DisplayInfo{X: d.X, Y: d.Y, Width: d.Width, Height: d.Height})
	}
	return out
}

// allMonitorsSpec captures the bounding box of every monitor with GDI.
// Desktop Duplication (ddagrab) duplicates one output at a time, so the
// virtual desktop comes from gdigrab, which uses more CPU; areas between
// monitors of different sizes are black.
func allMonitorsSpec(ds []DisplayInfo, fps int) (captureSpec, error) {
	if len(ds) == 0 {
		return captureSpec{}, errors.New("cannot list the host's monitors for the all-monitors view")
	}
	minX, minY, maxX, maxY := ds[0].X, ds[0].Y, ds[0].X+ds[0].Width, ds[0].Y+ds[0].Height
	for _, d := range ds[1:] {
		minX, minY = min(minX, d.X), min(minY, d.Y)
		maxX, maxY = max(maxX, d.X+d.Width), max(maxY, d.Y+d.Height)
	}
	w, h := maxX-minX, maxY-minY
	return captureSpec{
		input:    gdigrabInput(fps, minX, minY, w, h, true),
		noCursor: gdigrabInput(fps, minX, minY, w, h, false),
		x:        minX, y: minY, width: w, height: h,
		displays: ds,
		all:      true,
	}, nil
}
