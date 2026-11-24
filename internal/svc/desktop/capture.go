package desktop

import (
	"os"

	"github.com/readori/RemoHop/internal/x11disp"
)

// captureSpec says what ffmpeg captures and where input should land.
type captureSpec struct {
	// input are the ffmpeg arguments selecting the capture device,
	// ending with -i.
	input []string
	// filter is a filter chain to run before scaling ("" for none), e.g.
	// downloading GPU frames.
	filter string
	// x, y, width, height is the captured area in host screen pixels
	// (width 0 if unknown). Input coordinates map onto it.
	x, y, width, height int
	// x11 is the X display on Linux, with the credentials to reach it.
	x11 *x11disp.Display
	// env is the environment ffmpeg runs in (nil inherits ours); on Linux
	// it carries DISPLAY and XAUTHORITY for x11grab.
	env     []string
	warning string
	// displays are the host's monitors, when known.
	displays []DisplayInfo
	// all is set when the captured area is the whole virtual desktop, every
	// monitor in one picture.
	all bool
	// screen is, on macOS, the captured display's index in
	// CGGetActiveDisplayList (AVFoundation's "Capture screen N"), which
	// input needs to find the display's geometry in points.
	screen int
	// attach, when set, replaces capturing from a device: the session's
	// ffmpeg reads the screen from the returned pipe (input then describes
	// that stream), fed by a capture shared with other sessions; see
	// capture_hub.go. The caller closes its copy of the pipe once ffmpeg
	// runs and calls release when that ffmpeg is done.
	attach func(fps int) (stdin *os.File, release func(), err error)
}

// injector delivers input events on the host.
type injector interface {
	inject(ev InputEvent) error
	// typeText types text as if on the keyboard, any Unicode.
	typeText(text string) error
	// retarget aims pointer coordinates at a newly selected capture area.
	retarget(spec captureSpec)
	// close releases everything still held down and frees resources.
	close()
}

func clamp01(v float64) float64 {
	if !(v >= 0) { // also catches NaN
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// wheelSteps turns fractional notches into whole clicks, keeping the
// remainder so slow trackpad scrolling still adds up.
type wheelAcc struct{ x, y float64 }

func (w *wheelAcc) steps(dx, dy float64) (sx, sy int) {
	if dx != dx || dy != dy { // NaN
		return 0, 0
	}
	w.x += max(min(dx, 50), -50)
	w.y += max(min(dy, 50), -50)
	sx, sy = int(w.x), int(w.y)
	w.x -= float64(sx)
	w.y -= float64(sy)
	return sx, sy
}
