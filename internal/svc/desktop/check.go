package desktop

import (
	"errors"
	"fmt"

	"github.com/readori/RemoHop/internal/ffmpeg"
)

// FindFFmpeg is the ffmpeg screen sharing would use and where it comes from
// (ffmpeg.Find), or an error that says how to install it on this system.
func FindFFmpeg() (ffmpeg.Found, error) {
	f, err := ffmpeg.Find()
	if err != nil {
		return f, errors.New(ffmpegMissing + ": screen sharing needs ffmpeg with an H.264 or AV1 encoder. " + installHint())
	}
	return f, nil
}

// CheckScreen looks for the screen a desktop session would capture, as a
// probe does, without capturing: what it found (for example "1920x1080, 2
// monitors"), a warning about it, or why there is none (no X display, no
// permission, ...).
func CheckScreen() (found, warning string, err error) {
	spec, err := discoverCapture(0, 30)
	if err != nil {
		return "", "", err
	}
	found = fmt.Sprintf("%dx%d", spec.width, spec.height)
	if spec.width == 0 {
		found = "found"
	}
	if n := len(spec.displays); n > 1 {
		found += fmt.Sprintf(", %d monitors", n)
	}
	if spec.x11 != nil {
		found += " on X display " + spec.x11.Name
	}
	return found, spec.warning, nil
}
