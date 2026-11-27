package desktop

import (
	"errors"
	"strings"
	"testing"
)

// TestProbeError: "desktop:" is said once, and a host without ffmpeg comes
// with what works meanwhile.
func TestProbeError(t *testing.T) {
	got := probeError("123456789", errors.New("desktop: host: "+ffmpegMissing+": screen sharing needs ffmpeg"))
	if strings.Count(got, "desktop:") != 1 || !strings.Contains(got, "the host has no ffmpeg") ||
		!strings.Contains(got, "`remohop 123456789 screenshot`") {
		t.Errorf("no ffmpeg: %q", got)
	}
	if got := probeError("x", errors.New("desktop: host: no display")); got != "desktop: host: no display" {
		t.Errorf("other: %q", got)
	}
	if got := probeError("x", errors.New("stream refused")); got != "desktop: stream refused" {
		t.Errorf("unprefixed: %q", got)
	}
}
