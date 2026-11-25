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

	"github.com/readori/RemoHop/internal/nowindow"

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
		// While the secure desktop is shown nothing can be duplicated;
		// a probe now would keep the host on GDI for failedProbeLife.
		// The session waits for the screen to come back instead.
		if secureDesktop() != "" {
			break
		}
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
			// ddagrab stamps its frames with a monotonic clock counted
			// from its first frame, not with wall-clock time, so the host
			// could not tell when a frame was captured: the viewers then
			// show no latency at all, and the pipeline delay is unknown.
			// The demuxer's wall-clock stamp is taken as the frame leaves
			// ddagrab (the lavfi graph holds nothing else), in
			// microseconds (ddagrab's time base), which is what x11grab
			// and gdigrab give.
			"-use_wallclock_as_timestamps", "1",
			"-f", "lavfi",
			"-i", fmt.Sprintf("ddagrab=output_idx=%d:framerate=%d:draw_mouse=1", idx, fps),
		},
		filter:    "hwdownload,format=bgra",
		gpu:       func(e *encoder) (string, bool) { return ddagrabGPUChain(idx, e) },
		gpuFailed: func(e *encoder) { forgetGPUChain(idx, e) },
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
	out, err := nowindow.Set(exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-nostdin", "-loglevel", "error",
		"-f", "lavfi", "-i", fmt.Sprintf("ddagrab=output_idx=%d:framerate=10", idx),
		"-frames:v", "1", "-vf", "hwdownload,format=bgra", "-f", "null", "-")).CombinedOutput()
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

// GPU encoders can take Desktop Duplication's frames where they are, in
// video memory, instead of the default chain: download every frame to
// system memory (hwdownload: 8 MB a frame at 1080p, 33 MB at 4K), convert
// it from BGRA to NV12 on one CPU core (swscale) and upload it again for the
// encoder. At 1440p and 4K that round trip is what keeps 60 fps out of
// reach and adds milliseconds to every frame. NVENC and AMF encode BGRA
// D3D11 textures themselves (ffmpeg's own ddagrab example feeds h264_nvenc
// directly); Quick Sync needs the frames mapped into its device and
// converted to NV12 by its video processor. NVENC converts RGB with BT.601
// at limited range, Quick Sync's processor is told to produce limited-range
// BT.709 and AMF's encoder converts as its colour options say (gpuColor
// and colorMode). A chain is used only after a
// test run of ddagrab into the session's encoder works on this machine
// (cached like the ddagrab probe), and only when the picture is not scaled.
var ddagrabGPU = map[string]string{
	"h264_nvenc": "",
	"av1_nvenc":  "",
	"h264_amf":   "",
	"av1_amf":    "",
	"h264_qsv":   "hwmap=derive_device=qsv,format=qsv,vpp_qsv=format=nv12:out_color_matrix=bt709:out_range=tv",
	"av1_qsv":    "hwmap=derive_device=qsv,format=qsv,vpp_qsv=format=nv12:out_color_matrix=bt709:out_range=tv",
}

type gpuKey struct {
	ffmpeg, enc string
	idx         int
}

var (
	gpuMu    sync.Mutex
	gpuCache = map[gpuKey]ddaProbe{}
)

// ddagrabGPUChain returns the filter chain that feeds e from ddagrab's GPU
// frames, and whether that works here. REMOHOP_WIN_GPU_CHAIN=0 turns it
// off.
func ddagrabGPUChain(idx int, e *encoder) (string, bool) {
	chain, known := ddagrabGPU[e.name]
	if !known || os.Getenv("REMOHOP_WIN_GPU_CHAIN") == "0" {
		return "", false
	}
	ffmpeg, err := ffmpegPath()
	if err != nil {
		return "", false
	}
	gpuMu.Lock()
	defer gpuMu.Unlock()
	k := gpuKey{ffmpeg, e.name, idx}
	if p, ok := gpuCache[k]; ok && (p.problem == "" || time.Since(p.at) < failedProbeLife) {
		return chain, p.problem == ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error",
		"-f", "lavfi", "-i", fmt.Sprintf("ddagrab=output_idx=%d:framerate=10", idx), "-frames:v", "3"}
	if chain != "" {
		args = append(args, "-vf", chain)
	}
	args = append(args, e.args(encParams{fps: 10, bitrate: 2_000_000, gop: 20})...)
	args = append(args, gpuColor(e).encArgs()...)
	args = append(args, "-f", "null", "-")
	out, err := nowindow.Set(exec.CommandContext(ctx, ffmpeg, args...)).CombinedOutput()
	p := ddaProbe{at: time.Now()}
	if err != nil {
		p.problem = lastLine(string(out), err.Error())
	}
	gpuCache[k] = p
	return chain, p.problem == ""
}

// forgetGPUChain records that a session's encoder failed on the GPU chain,
// so the next start uses the default one.
func forgetGPUChain(idx int, e *encoder) {
	ffmpeg, err := ffmpegPath()
	if err != nil {
		return
	}
	gpuMu.Lock()
	gpuCache[gpuKey{ffmpeg, e.name, idx}] = ddaProbe{problem: "failed in a session", at: time.Now()}
	gpuMu.Unlock()
}
