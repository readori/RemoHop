package desktop

import (
	"context"
	"fmt"
	"image"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/readori/RemoHop/internal/unattended"
)

// unattendedCapture is the capture of the Windows service with unattended
// access (remohop service install --unattended). The service runs in session 0,
// where ffmpeg's grabbers see nothing; a helper in the console session
// captures with GDI on whatever desktop has the input (the user's desktop,
// the sign-in and lock screens, UAC prompts) and sends raw frames, which the
// session's ffmpeg reads from a pipe and encodes. ok is false when the
// process is not such a service, and the normal capture applies.
func unattendedCapture(display, fps int) (spec captureSpec, ok bool, err error) {
	if !unattended.Active() {
		return captureSpec{}, false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ds, err := unattended.Displays(ctx)
	if err != nil {
		return captureSpec{}, true, err
	}
	infos := make([]DisplayInfo, 0, len(ds))
	for _, d := range ds {
		infos = append(infos, DisplayInfo{X: d.X, Y: d.Y, Width: d.Width, Height: d.Height})
	}
	var r image.Rectangle
	if display == DisplayAll {
		for i, d := range ds {
			dr := image.Rect(d.X, d.Y, d.X+d.Width, d.Y+d.Height)
			if i == 0 {
				r = dr
			} else {
				r = r.Union(dr)
			}
		}
	} else {
		idx := max(display-1, 0)
		if idx >= len(ds) {
			return captureSpec{}, true, fmt.Errorf("display %d does not exist (the console session has %d)", display, len(ds))
		}
		d := ds[idx]
		r = image.Rect(d.X, d.Y, d.X+d.Width, d.Y+d.Height)
	}
	if r.Empty() {
		return captureSpec{}, true, fmt.Errorf("no display available in the console session")
	}
	spec = captureSpec{
		input: []string{"-f", "rawvideo", "-pixel_format", "bgr0", "-video_size", fmt.Sprintf("%dx%d", r.Dx(), r.Dy()),
			"-framerate", strconv.Itoa(fps), "-use_wallclock_as_timestamps", "1", "-i", "pipe:0"},
		x: r.Min.X, y: r.Min.Y, width: r.Dx(), height: r.Dy(),
		displays: infos,
		all:      display == DisplayAll,
		attach: func(fps int) (*os.File, func(), error) {
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			defer cancel()
			st, err := unattended.OpenStream(ctx, r, fps)
			if err != nil {
				return nil, nil, err
			}
			pr, pw, err := os.Pipe()
			if err != nil {
				st.Close()
				return nil, nil, err
			}
			go func() {
				// Ends when the helper ends the stream (another session
				// took the console, the layout changed), on release, or
				// when ffmpeg stops reading.
				if err := st.Copy(pw); err != nil {
					slog.Debug("desktop: console session capture ended", "err", err)
				}
				pw.Close()
				st.Close()
			}()
			return pr, st.Close, nil
		},
	}
	return spec, true, nil
}
