package desktop

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"

	"github.com/ebitengine/purego"

	"github.com/readori/RemoHop/internal/inject"
)

// macOS capture uses AVFoundation's screen devices, named "Capture screen N"
// (0-based, in the order of CGGetActiveDisplayList). The host process needs
// the Screen Recording permission (System Settings > Privacy & Security);
// without it macOS still delivers frames, but they show only the desktop
// background and the menu bar, no windows. desktop_darwin_test.go runs the
// pipeline on a real desktop (opt-in).
func discoverCapture(display, fps int) (captureSpec, error) {
	if display == DisplayAll {
		return captureSpec{}, errors.New(allMonitorsMac)
	}
	idx := 0
	if display > 0 {
		idx = display - 1
	}
	ds, err := inject.MacDisplays()
	if err != nil {
		return captureSpec{}, err
	}
	if idx >= len(ds) {
		return captureSpec{}, fmt.Errorf("display %d does not exist (the host has %d)", display, len(ds))
	}
	device := func(fps int) []string {
		return []string{
			"-f", "avfoundation",
			"-thread_queue_size", "2",
			// The screen's native format. ffmpeg's default (yuv420p) is not
			// offered by screen devices: it would log an error on every
			// start and fall back to a format of its own choosing.
			"-pixel_format", "bgr0",
			"-capture_cursor", "1",
			"-framerate", strconv.Itoa(fps),
			"-i", fmt.Sprintf("Capture screen %d:none", idx),
		}
	}
	spec := captureSpec{
		screen: idx,
		width:  ds[idx].PixelWidth,
		height: ds[idx].PixelHeight,
		input:  device(fps),
	}
	// macOS hands the screen to one AVFoundation capture at a time: a
	// second one, started while the first runs, never gets a frame. So the
	// sessions of this host share one capture (capture_hub.go).
	// REMOHOP_MAC_DIRECT_CAPTURE=1 gives every session its own device capture
	// instead, as a way out should the shared one misbehave on some Mac.
	if w, h := spec.width&^1, spec.height&^1; w > 0 && h > 0 && os.Getenv("REMOHOP_MAC_DIRECT_CAPTURE") != "1" {
		spec.input = hubInput(w, h, fps)
		spec.filter = yuvSource(hubColor)
		spec.attach = func(fps int) (*os.File, func(), error) {
			ffmpeg, err := ffmpegPath()
			if err != nil {
				return nil, nil, err
			}
			return attachCapture(fmt.Sprintf("avfoundation:%d", idx), ffmpeg, device, nil, w, h, fps)
		}
	}
	for _, d := range ds {
		// Positions in the pixels of each display's own scale.
		sx, sy := 1.0, 1.0
		if d.Width > 0 && d.Height > 0 {
			sx, sy = float64(d.PixelWidth)/d.Width, float64(d.PixelHeight)/d.Height
		}
		spec.displays = append(spec.displays, DisplayInfo{X: int(d.X * sx), Y: int(d.Y * sy), Width: d.PixelWidth, Height: d.PixelHeight})
	}
	switch granted, known := screenCaptureAccess(); {
	case !known:
		spec.warning = "macOS: the host needs the Screen Recording permission for remohop (or the terminal or agent it runs in)"
	case !granted:
		spec.warning = "macOS has not granted the host the Screen Recording permission, so the picture shows only the desktop " +
			"background, without windows: on the host open System Settings > Privacy & Security > Screen Recording, " +
			"enable remohop (or the terminal or agent it runs in) and restart remohop host"
	}
	return spec, nil
}

var (
	screenAccessOnce sync.Once
	// cgPreflightScreenCapture is CGPreflightScreenCaptureAccess (macOS
	// 10.15+): whether this process may record the screen. It never prompts.
	cgPreflightScreenCapture func() bool
)

// screenCaptureAccess reports whether macOS lets this process (and the
// ffmpeg it starts) see the screen's windows; known is false when the
// system cannot say.
func screenCaptureAccess() (granted, known bool) {
	screenAccessOnce.Do(func() {
		lib, err := purego.Dlopen("/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			return
		}
		sym, err := purego.Dlsym(lib, "CGPreflightScreenCaptureAccess")
		if err != nil || sym == 0 {
			return
		}
		purego.RegisterFunc(&cgPreflightScreenCapture, sym)
	})
	if cgPreflightScreenCapture == nil {
		return false, false
	}
	return cgPreflightScreenCapture(), true
}

// newInjector needs the Accessibility permission (inject.Open fails with
// instructions without it). Pointer positions are mapped from the captured
// display's pixels to global points with that display's geometry.
func newInjector(spec captureSpec) (injector, error) {
	inj, err := inject.Open(context.Background(), nil)
	if err != nil {
		return nil, err
	}
	h := &hostInjector{inj: inj, spec: spec, geomFor: macGeometry}
	if h.geom = macGeometry(spec); h.geom == nil {
		inj.Close()
		return nil, fmt.Errorf("cannot find the geometry of display %d for input", spec.screen+1)
	}
	return h, nil
}

// macGeometry is the point geometry of the captured display, nil if it is
// gone.
func macGeometry(spec captureSpec) *inject.DisplayGeometry {
	ds, err := inject.MacDisplays()
	if err != nil || spec.screen < 0 || spec.screen >= len(ds) {
		return nil
	}
	return &ds[spec.screen]
}
