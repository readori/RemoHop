package desktop

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/randr"
	"github.com/jezek/xgb/xproto"

	"github.com/readori/RemoHop/internal/inject"
	"github.com/readori/RemoHop/internal/x11disp"
)

// findDisplay resolves the X display to capture with the shared discovery:
// an explicit $DISPLAY/$XAUTHORITY first, else the first live local server
// and a cookie that unlocks it (a host daemon started from systemd or ssh
// often has neither although a desktop session is running).
func findDisplay() (*x11disp.Display, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	d, err := x11disp.Find(ctx)
	if err == nil {
		return d, nil
	}
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		return nil, errors.New("this host runs a Wayland session without X11; desktop capture needs an X11 session (choose \"GNOME on Xorg\"/\"Plasma (X11)\" at login)")
	}
	return nil, fmt.Errorf("%w; desktop capture needs a running X11 session", err)
}

func discoverCapture(display, fps int) (captureSpec, error) {
	d, err := findDisplay()
	if err != nil {
		return captureSpec{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, err := d.Open(ctx)
	if err != nil {
		return captureSpec{}, fmt.Errorf("connect to %w", err)
	}
	defer c.Close()
	scr := xproto.Setup(c).DefaultScreen(c)
	spec := captureSpec{x11: d, env: d.Merge(nil), width: int(scr.WidthInPixels), height: int(scr.HeightInPixels)}
	mons, monErr := x11Monitors(c, scr.Root)
	for _, m := range mons {
		spec.displays = append(spec.displays, DisplayInfo{X: int(m.X), Y: int(m.Y), Width: int(m.Width), Height: int(m.Height)})
	}
	// Display 0 and DisplayAll both capture the whole root window, which
	// spans every monitor; the pointer maps onto the same area.
	spec.all = display <= 0
	if display > 0 {
		if monErr != nil {
			return captureSpec{}, monErr
		}
		if display > len(mons) {
			return captureSpec{}, fmt.Errorf("display %d does not exist (the host has %d monitor(s))", display, len(mons))
		}
		m := mons[display-1]
		spec.x, spec.y, spec.width, spec.height = int(m.X), int(m.Y), int(m.Width), int(m.Height)
	}
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		spec.warning = "the host runs Wayland; only X11 (XWayland) windows are captured and input may be ignored"
	}
	spec.input = []string{
		"-f", "x11grab",
		// A short demuxer queue bounds how stale a captured frame can get
		// when the encoder momentarily falls behind.
		"-thread_queue_size", "2",
		"-framerate", strconv.Itoa(fps),
		"-draw_mouse", "1",
		"-video_size", fmt.Sprintf("%dx%d", spec.width, spec.height),
		"-grab_x", strconv.Itoa(spec.x), "-grab_y", strconv.Itoa(spec.y),
		"-i", d.Name,
	}
	return spec, nil
}

// x11Monitors lists monitors left to right, top to bottom, using RandR 1.5.
func x11Monitors(c *xgb.Conn, root xproto.Window) ([]randr.MonitorInfo, error) {
	if err := randr.Init(c); err != nil {
		return nil, fmt.Errorf("X server has no RandR extension, cannot select a monitor: %w", err)
	}
	rep, err := randr.GetMonitors(c, root, true).Reply()
	if err != nil {
		return nil, fmt.Errorf("list monitors: %w", err)
	}
	mons := rep.Monitors
	sort.SliceStable(mons, func(i, j int) bool {
		if mons[i].X != mons[j].X {
			return mons[i].X < mons[j].X
		}
		return mons[i].Y < mons[j].Y
	})
	return mons, nil
}

func newInjector(spec captureSpec) (injector, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	inj, err := inject.Open(ctx, spec.x11)
	if err != nil {
		return nil, err
	}
	return &hostInjector{inj: inj, spec: spec}, nil
}
