package desktop

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows"

	"github.com/readori/RemoHop/internal/svc/screenshot"
)

var (
	user32               = windows.NewLazySystemDLL("user32.dll")
	procGetSystemMetrics = user32.NewProc("GetSystemMetrics")
)

// Windows capture prefers the Desktop Duplication API through ffmpeg's
// ddagrab source (ffmpeg 6.0+), downloading the GPU frames for the encoder.
// Desktop Duplication needs a D3D11 device and a real output: on a machine
// without one (a virtual machine with only the basic display adapter, some
// remote sessions) ddagrab fails to start, so the host test-runs it once and
// otherwise falls back to gdigrab (GDI BitBlt), which works everywhere a
// desktop exists but is slower and does not capture some protected content.
// REMOHOP_WIN_CAPTURE=ddagrab or gdigrab forces one of them. The host must run
// in the interactive user session (not as a session-0 service) to see the
// desktop.
func discoverCaptureMonitor(display, fps int) (captureSpec, error) {
	idx := 0
	if display > 0 {
		idx = display - 1
	}
	backend := strings.ToLower(os.Getenv("REMOHOP_WIN_CAPTURE"))
	var ddaErr string
	switch backend {
	case "gdigrab":
	case "ddagrab":
		backend = "ddagrab"
	default:
		backend = "ddagrab"
		if ffmpeg, err := ffmpegPath(); err == nil {
			if ddaErr = ddagrabProblem(ffmpeg, idx); ddaErr != "" {
				backend = "gdigrab"
			}
		}
	}
	if backend == "gdigrab" {
		return gdigrabSpec(idx, fps, ddaErr)
	}
	spec := captureSpec{
		input: []string{
			"-f", "lavfi",
			"-i", fmt.Sprintf("ddagrab=output_idx=%d:framerate=%d:draw_mouse=1", idx, fps),
		},
		filter: "hwdownload,format=bgra",
	}
	if idx == 0 {
		// SM_CXSCREEN/SM_CYSCREEN: the primary monitor, which is what
		// absolute SendInput coordinates address.
		w, _, _ := procGetSystemMetrics.Call(0)
		h, _, _ := procGetSystemMetrics.Call(1)
		spec.width, spec.height = int(w), int(h)
	}
	return spec, nil
}

// gdigrabSpec captures monitor idx (in the screenshot service's order,
// primary first) with GDI. why is what went wrong with ddagrab ("" when
// GDI was asked for).
func gdigrabSpec(idx, fps int, why string) (captureSpec, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ds, err := screenshot.LocalDisplays(ctx)
	if err != nil {
		return captureSpec{}, err
	}
	if idx >= len(ds) {
		return captureSpec{}, fmt.Errorf("display %d does not exist (the host has %d)", idx+1, len(ds))
	}
	d := ds[idx]
	spec := captureSpec{
		input: []string{
			// No -thread_queue_size: gdigrab is not a threaded demuxer and
			// ffmpeg rejects the option for it.
			"-f", "gdigrab",
			"-framerate", fmt.Sprint(fps),
			"-draw_mouse", "1",
			"-offset_x", fmt.Sprint(d.X), "-offset_y", fmt.Sprint(d.Y),
			"-video_size", fmt.Sprintf("%dx%d", d.Width, d.Height),
			"-i", "desktop",
		},
		x: d.X, y: d.Y, width: d.Width, height: d.Height,
	}
	if why != "" {
		spec.warning = "Windows: Desktop Duplication (ddagrab) does not work here (" + why + "); capturing with GDI (gdigrab), which uses more CPU"
	}
	return spec, nil
}

type ddaProbe struct {
	problem string
	at      time.Time
}

type ddaKey struct {
	ffmpeg string
	idx    int
}

var (
	ddaMu    sync.Mutex
	ddaCache = map[ddaKey]ddaProbe{}
)

// ddagrabProblem test-runs ddagrab for one frame and returns why it does
// not work ("" if it does). A success is remembered for the life of the
// process, a failure for failedProbeLife.
func ddagrabProblem(ffmpeg string, idx int) string {
	ddaMu.Lock()
	defer ddaMu.Unlock()
	k := ddaKey{ffmpeg, idx}
	if p, ok := ddaCache[k]; ok && (p.problem == "" || time.Since(p.at) < failedProbeLife) {
		return p.problem
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-nostdin", "-loglevel", "error",
		"-f", "lavfi", "-i", fmt.Sprintf("ddagrab=output_idx=%d:framerate=10", idx),
		"-frames:v", "1", "-vf", "hwdownload,format=bgra", "-f", "null", "-").CombinedOutput()
	p := ddaProbe{at: time.Now()}
	if err != nil {
		p.problem = ddaReason(string(out), err)
	}
	ddaCache[k] = p
	return p.problem
}

// ddaReason picks the informative line of ddagrab's error output (the last
// line is usually just "Error opening input files").
func ddaReason(out string, err error) string {
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); strings.Contains(l, "Failed to") {
			if i := strings.Index(l, "] "); i >= 0 {
				l = l[i+2:]
			}
			return l
		}
	}
	return lastLine(out, err.Error())
}
