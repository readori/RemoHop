package desktop

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/readori/RemoHop/internal/nowindow"
)

// The host captures and encodes with an ffmpeg subprocess: it is the one way
// to reach hardware AV1 encoders (and screen capture APIs) on every platform
// while remohop stays a pure-Go, cgo-free binary.

// encParams are the per-(re)start encoding parameters.
type encParams struct {
	fps     int
	bitrate int64
	gop     int
	pixels  int // output width*height, or 0 if unknown
	// fast: the path to the viewer has bandwidth to spare (a LAN, or a
	// direct path with a LAN-like round trip; see hostSession.fastPath).
	// The rate-control buffer may then be larger, which is what lets a
	// key frame or a scroll keep its text sharp.
	fast bool
}

type encoder struct {
	name string
	// codec is the family the viewer must decode: codecAV1 or codecH264.
	codec string
	hw    bool
	// preInput are arguments that must precede -i (device setup).
	preInput func() []string
	// pixFmt is the filter chain tail that converts to what the encoder
	// takes.
	pixFmt string
	args   func(p encParams) []string
	// version is the encoder library's version when it matters ("" if
	// unknown or irrelevant) and warning a caveat about this choice that
	// the client should show.
	version string
	warning string
	// limitedRange: the encoder cannot label a full-range stream, so it
	// is fed limited range (see colorMode).
	limitedRange bool
}

// vbv sizes the rate-control buffer. It bounds how far a frame may exceed
// its share of the bitrate, and so both what a burst costs in latency on a
// link that runs at the bitrate and how sharp a key frame or a frame with a
// lot of change (scrolling text) can be. With the two frames this used to
// be, every hardware encoder starved its key frames. Measured with x264
// standing in, on a scrolling 1080p60 text clip at 7.5 Mbit/s
// (TestEncoderQuality has the method): the key frame came out at SSIM
// 0.78 (smeared text), 0.87 with a tenth of a second of buffer; a scroll
// at 0.972, 0.991 with it. A tenth of a second (six frames at 60 fps) is
// the default. A fast path gets a quarter of a second, where a 2 Mbit key
// frame takes a few milliseconds on the wire; with the fast path's
// bitrate the key frame reaches 0.979 and a scroll 0.9996.
func vbv(p encParams) string {
	div := int64(10)
	if p.fast {
		div = 4
	}
	return strconv.FormatInt(max(p.bitrate/div, p.bitrate*2/int64(max(p.fps, 1))), 10)
}

// qsvLowDelay is Quick Sync's LowDelayBRC switch. It holds every frame,
// key frames included, close to the average frame size whatever the
// buffer allows, which is what smears the text of a QSV stream after every
// key frame and while scrolling. Where the path has bandwidth to spare it
// is off and the buffer alone bounds the frame sizes.
func qsvLowDelay(p encParams) string {
	if p.fast {
		return "0"
	}
	return "1"
}

func br(p encParams) string { return strconv.FormatInt(p.bitrate, 10) }

// Codec families a session can use.
const (
	codecAV1  = "av1"
	codecH264 = "h264"
)

// encoders lists what the host knows how to drive; choose ranks them. Every
// one is configured for low delay: no frame reordering or lookahead,
// CBR-ish rate control, and a key frame every gop frames so a decoder that
// joins late or lost data resyncs quickly. Where the encoder has a switch
// for it, a forced key frame (keyforce.go) is an IDR frame, which is what
// the host and the viewers take as a key frame.
var encoders = []*encoder{
	{
		name:   "av1_nvenc",
		codec:  codecAV1,
		hw:     true,
		pixFmt: "format=yuv420p",
		args: func(p encParams) []string {
			return []string{"-c:v", "av1_nvenc", "-preset", "p1", "-tune", "ull", "-zerolatency", "1",
				"-delay", "0", "-rc", "cbr", "-b:v", br(p), "-maxrate", br(p), "-bufsize", vbv(p),
				"-g", strconv.Itoa(p.gop), "-bf", "0", "-forced-idr", "1"}
		},
	},
	{
		name:   "av1_qsv",
		codec:  codecAV1,
		hw:     true,
		pixFmt: "format=nv12",
		args: func(p encParams) []string {
			return []string{"-c:v", "av1_qsv", "-preset", "veryfast", "-low_delay_brc", qsvLowDelay(p), "-async_depth", "1",
				"-b:v", br(p), "-maxrate", br(p), "-bufsize", vbv(p), "-g", strconv.Itoa(p.gop), "-bf", "0", "-forced_idr", "1"}
		},
	},
	{
		name:     "av1_vaapi",
		codec:    codecAV1,
		hw:       true,
		preInput: func() []string { return []string{"-vaapi_device", vaapiDevice()} },
		pixFmt:   "format=nv12,hwupload",
		args: func(p encParams) []string {
			return []string{"-c:v", "av1_vaapi", "-rc_mode", "CBR", "-async_depth", "1",
				"-b:v", br(p), "-maxrate", br(p), "-bufsize", vbv(p), "-g", strconv.Itoa(p.gop), "-bf", "0"}
		},
	},
	{
		name:   "av1_amf",
		codec:  codecAV1,
		hw:     true,
		pixFmt: "format=nv12",
		args: func(p encParams) []string {
			return []string{"-c:v", "av1_amf", "-usage", "lowlatency", "-quality", "speed", "-rc", "cbr",
				"-b:v", br(p), "-maxrate", br(p), "-bufsize", vbv(p), "-g", strconv.Itoa(p.gop),
				"-header_insertion_mode", "gop"}
		},
	},
	{
		// Probed with the settings that are right on every version;
		// selectEncoder replaces args once it knows the library version.
		name:   "libsvtav1",
		codec:  codecAV1,
		pixFmt: "format=yuv420p",
		args:   svtPlan{maxPreset: svtSafePreset}.args,
	},
	{
		name:   "libaom-av1",
		codec:  codecAV1,
		pixFmt: "format=yuv420p",
		args: func(p encParams) []string {
			return []string{"-c:v", "libaom-av1", "-usage", "realtime", "-cpu-used", "8", "-lag-in-frames", "0",
				"-row-mt", "1", "-tiles", "2x2", "-g", strconv.Itoa(p.gop),
				"-b:v", br(p), "-maxrate", br(p), "-bufsize", vbv(p), "-aom-params", "tune-content=screen"}
		},
	},
	{
		name:   "h264_nvenc",
		codec:  codecH264,
		hw:     true,
		pixFmt: "format=yuv420p",
		args: func(p encParams) []string {
			return []string{"-c:v", "h264_nvenc", "-preset", "p1", "-tune", "ull", "-zerolatency", "1",
				"-delay", "0", "-rc", "cbr", "-b:v", br(p), "-maxrate", br(p), "-bufsize", vbv(p),
				"-g", strconv.Itoa(p.gop), "-bf", "0", "-forced-idr", "1", "-profile:v", "high"}
		},
	},
	{
		name:   "h264_qsv",
		codec:  codecH264,
		hw:     true,
		pixFmt: "format=nv12",
		args: func(p encParams) []string {
			return []string{"-c:v", "h264_qsv", "-preset", "veryfast", "-low_delay_brc", qsvLowDelay(p), "-async_depth", "1",
				"-look_ahead", "0", "-b:v", br(p), "-maxrate", br(p), "-bufsize", vbv(p),
				"-g", strconv.Itoa(p.gop), "-bf", "0", "-forced_idr", "1"}
		},
	},
	{
		name:     "h264_vaapi",
		codec:    codecH264,
		hw:       true,
		preInput: func() []string { return []string{"-vaapi_device", vaapiDevice()} },
		pixFmt:   "format=nv12,hwupload",
		args: func(p encParams) []string {
			return []string{"-c:v", "h264_vaapi", "-rc_mode", "CBR", "-async_depth", "1",
				"-b:v", br(p), "-maxrate", br(p), "-bufsize", vbv(p), "-g", strconv.Itoa(p.gop), "-bf", "0"}
		},
	},
	{
		name:   "h264_amf",
		codec:  codecH264,
		hw:     true,
		pixFmt: "format=nv12",
		args: func(p encParams) []string {
			return []string{"-c:v", "h264_amf", "-usage", "ultralowlatency", "-quality", "speed", "-rc", "cbr",
				"-b:v", br(p), "-maxrate", br(p), "-bufsize", vbv(p), "-g", strconv.Itoa(p.gop), "-bf", "0"}
		},
	},
	{
		// macOS VideoToolbox; -allow_sw 0 keeps it a hardware encoder.
		name:   "h264_videotoolbox",
		codec:  codecH264,
		hw:     true,
		pixFmt: "format=nv12",
		args: func(p encParams) []string {
			return []string{"-c:v", "h264_videotoolbox", "-realtime", "1", "-allow_sw", "0", "-profile:v", "high",
				"-b:v", br(p), "-maxrate", br(p), "-bufsize", vbv(p), "-g", strconv.Itoa(p.gop), "-bf", "0"}
		},
	},
	{
		// Windows Media Foundation, restricted to hardware transforms.
		name:   "h264_mf",
		codec:  codecH264,
		hw:     true,
		pixFmt: "format=nv12",
		// ffmpeg's Media Foundation wrapper writes no colour description.
		limitedRange: true,
		args: func(p encParams) []string {
			return []string{"-c:v", "h264_mf", "-hw_encoding", "1", "-rate_control", "cbr", "-scenario", "display_remoting",
				"-b:v", br(p), "-g", strconv.Itoa(p.gop), "-bf", "0"}
		},
	},
	{
		name:   "libx264",
		codec:  codecH264,
		pixFmt: "format=yuv420p",
		args: func(p encParams) []string {
			// zerolatency: no B-frames, no lookahead, no frame threads
			// (sliced threads instead), so a frame leaves the encoder as
			// soon as it is coded. The preset follows the pixel rate;
			// measured on a 4-core x86 box, veryfast encodes 1080p at
			// ~130-160 fps and 4K at ~33, superfast 4K at ~45, ultrafast
			// 4K at ~70.
			// The rate counts at least 50 fps: when the host lowers the
			// frame rate because the encoder falls behind (fitRate), a
			// slower preset would only fall behind again.
			preset := "veryfast"
			switch rate := p.pixels * max(p.fps, 50); {
			case rate >= 3840*2160*50:
				preset = "ultrafast"
			case rate > 1920*1080*60:
				preset = "superfast"
			}
			// A quarter second of VBV: with only a frame or two x264 has
			// to starve every key frame to fit it.
			buf := strconv.FormatInt(p.bitrate/4, 10)
			// x264 budgets bits per frame from the frame rate ffmpeg hands
			// it, and that is whatever the capture device declares: x11grab
			// says the real rate, AVFoundation's screen devices say 1000000
			// (their time base). x264 then starves every frame until the
			// picture is flat grey, so tell it the rate ourselves.
			return []string{"-c:v", "libx264", "-preset", preset, "-tune", "zerolatency", "-bf", "0",
				"-g", strconv.Itoa(p.gop), "-keyint_min", strconv.Itoa(p.gop), "-sc_threshold", "0", "-forced-idr", "1",
				"-b:v", br(p), "-maxrate", br(p), "-bufsize", buf, "-x264-params", "fps=" + strconv.Itoa(p.fps) + "/1"}
		},
	},
	{
		name:   "libopenh264",
		codec:  codecH264,
		pixFmt: "format=yuv420p",
		args: func(p encParams) []string {
			return []string{"-c:v", "libopenh264", "-rc_mode", "bitrate", "-allow_skip_frames", "0",
				"-b:v", br(p), "-maxrate", br(p), "-g", strconv.Itoa(p.gop), "-bf", "0"}
		},
	},
}

// SVT-AV1 in low-delay mode.
//
// Up to 3.0 the library switches on its "rtc" tuning whenever pred-struct=1
// is used, and that tuning contains a shortcut (lpd1_bypass_tx_th_div in the
// light-PD1 path of the fast presets) which decides to code an inter block
// without residual from the cost of its rate alone: the distortion handed to
// the cost function is zero. On a static screen where a small area changes,
// the cheap zero-motion prediction wins and the change is never coded: the
// viewer shows a stale screen until the next key frame. Where the shortcut
// applies depends on the version:
//
//	< 2.2        presets >= 9, but not for screen content: scm=1 avoids it
//	2.2 .. 3.0   presets >= 8 or 9, screen content or not: only the slow
//	             presets are correct
//	3.1 .. 3.x   only with rtc=1 (no longer implied by pred-struct=1)
//	>= 4.0       fixed (the shortcut uses the real distortion)
//
// Verified by encoding and decoding the test pattern of
// TestEncoderTracksSmallChanges with 1.7.0, 2.1.0, 2.2.0, 2.3.0, 3.0.2,
// 3.1.2, 4.0.0 and 4.3.0; rtc=1 is never passed, so 3.1+ behaves like 4.x.
const svtSafePreset = 7

// svtPlan is how to run libsvtav1 for one library version.
type svtPlan struct {
	// maxPreset caps the preset (0: no cap). A capped plan is correct but
	// too slow for 1080p60 in software.
	maxPreset int
}

var svtVersionRE = regexp.MustCompile(`SVT-AV1 Encoder Lib v(\d+)\.(\d+)\.(\d+)`)

// parseSVTVersion finds the library banner SVT-AV1 prints on stderr when an
// encoder is created.
func parseSVTVersion(stderr string) (v [3]int, text string, ok bool) {
	m := svtVersionRE.FindStringSubmatch(stderr)
	if m == nil {
		return v, "", false
	}
	for i := range v {
		v[i], _ = strconv.Atoi(m[i+1])
	}
	return v, fmt.Sprintf("%d.%d.%d", v[0], v[1], v[2]), true
}

// svtPlanFor picks the plan for a library version; an unknown version gets
// the one that is correct everywhere.
func svtPlanFor(v [3]int, known bool) svtPlan {
	affected := !known || (v[0] == 2 && v[1] >= 2) || (v[0] == 3 && v[1] == 0)
	if affected {
		return svtPlan{maxPreset: svtSafePreset}
	}
	return svtPlan{}
}

func (pl svtPlan) slow() bool { return pl.maxPreset != 0 }

func (pl svtPlan) args(p encParams) []string {
	// The preset trades quality for speed by pixel rate. Measured on a
	// 4-core x86 box with 1.7: preset 12 encodes 1080p at ~70 fps, preset
	// 13 4K at ~40 fps.
	preset := 10
	rate := p.pixels * p.fps
	switch {
	case rate == 0 || rate >= 1920*1080*50:
		preset = 13
	case rate >= 1280*720*24:
		preset = 12
	}
	if pl.maxPreset != 0 {
		preset = min(preset, pl.maxPreset)
	}
	// pred-struct=1 is the low-delay structure (no reordering);
	// lookahead=0 and scd=0 remove the remaining frame delay; rc=2 is CBR,
	// the only rate control low delay supports. scm=1 forces
	// screen-content tools, which is also what keeps versions before 2.2
	// off the broken shortcut described above.
	// fps-num/fps-denom: the frame rate comes from the capture device
	// otherwise, and the 1000000 fps AVFoundation's screen devices declare is
	// refused by the library as a bad parameter.
	return []string{"-c:v", "libsvtav1", "-preset", strconv.Itoa(preset), "-g", strconv.Itoa(p.gop), "-b:v", br(p),
		"-svtav1-params", "pred-struct=1:lookahead=0:scd=0:rc=2:scm=1:fps-num=" + strconv.Itoa(p.fps) + ":fps-denom=1"}
}

func knownEncoder(name string) bool { return lookupEncoder(name) != nil }

func lookupEncoder(name string) *encoder {
	for _, e := range encoders {
		if e.name == name {
			return e
		}
	}
	return nil
}

func vaapiDevice() string {
	if d := os.Getenv("REMOHOP_VAAPI_DEVICE"); d != "" {
		return d
	}
	if m, _ := filepath.Glob("/dev/dri/renderD*"); len(m) > 0 {
		return m[0]
	}
	return "/dev/dri/renderD128"
}

// autoBitrate picks a bitrate for screen content: about 0.06 bits per pixel
// per frame (≈7.5 Mbit/s for 1080p60, ≈30 Mbit/s for 4K60), twice that
// on a fast path (≈15 Mbit/s for 1080p60, ≈27 for 1440p60, ≈60 for
// 4K60). Doubling the bitrate took the 1080p60 text clip of
// TestEncoderQuality from 45 to 56 dB PSNR; the fast path can
// carry it without queueing, and an encoder spends less than its target
// whenever the screen is still. Above 60 fps each further frame counts
// half: at 120 fps consecutive frames differ half as much, so the bits a
// frame needs fall (1080p120 on a fast path gets ≈22 Mbit/s, not 30).
func autoBitrate(pixels, fps int, fast bool) int64 {
	if pixels <= 0 {
		pixels = 1920 * 1080
	}
	bpp := 0.06
	if fast {
		bpp = 0.12
	}
	rate := float64(fps)
	if rate > 60 {
		rate = 60 + (rate-60)/2
	}
	b := int64(float64(pixels) * rate * bpp)
	return min(max(b, 1_000_000), maxAutoBitrate)
}

// maxAutoBitrate caps the automatic bitrate (4K at 60 fps on a fast path
// fits below it).
const maxAutoBitrate = 100_000_000

// ffmpegPath finds the ffmpeg binary: $REMOHOP_FFMPEG, then PATH.
func ffmpegPath() (string, error) {
	if p := os.Getenv("REMOHOP_FFMPEG"); p != "" {
		return p, nil
	}
	p, err := exec.LookPath("ffmpeg")
	if err != nil {
		return "", errors.New("ffmpeg not found on the host; the desktop service needs ffmpeg with an H.264 or AV1 encoder. " + installHint())
	}
	return p, nil
}

func installHint() string {
	switch runtime.GOOS {
	case "windows":
		return "Install it with `winget install Gyan.FFmpeg` (or put ffmpeg.exe on PATH, or set REMOHOP_FFMPEG)."
	case "darwin":
		return "Install it with `brew install ffmpeg` (or set REMOHOP_FFMPEG)."
	default:
		return "Install it with your package manager (Debian/Ubuntu: `apt install ffmpeg`, Fedora: `dnf install ffmpeg` from RPM Fusion, Arch: `pacman -S ffmpeg`) or set REMOHOP_FFMPEG; ffmpeg 6.0 or newer with libsvtav1 is recommended."
	}
}

// encRequest is what a session asks of the encoder choice.
type encRequest struct {
	// forced names one encoder to use, whatever its rank.
	forced string
	// codec restricts the family ("" for either).
	codec string
	// decode lists the families the viewer can decode (nil: assume any).
	decode []string
	// exclude names encoders not to use (they failed in this session).
	exclude []string
}

func (r encRequest) allows(codec string) bool {
	if r.codec != "" && r.codec != codec {
		return false
	}
	return r.decode == nil || slices.Contains(r.decode, codec)
}

// Encoder classes, from which rank builds the order of preference.
const (
	classAV1HW = iota
	classSVTFast
	classH264HW
	classX264
	classAOM
	classSVTSlow
	classOpenH264
)

func classOf(e *encoder, slowSVT bool) int {
	switch {
	case e.codec == codecAV1 && e.hw:
		return classAV1HW
	case e.name == "libsvtav1" && slowSVT:
		return classSVTSlow
	case e.name == "libsvtav1":
		return classSVTFast
	case e.name == "libaom-av1":
		return classAOM
	case e.hw:
		return classH264HW
	case e.name == "libx264":
		return classX264
	}
	return classOpenH264
}

// rank is the order of preference: hardware AV1, then H.264 (hardware,
// then libx264), then software AV1. Without an AV1 GPU encoder H.264 wins
// on latency: measured glass-to-glass in the browser viewer on a 4-core
// CPU-only box, libx264 took a median 20 ms at 720p and 31 ms at 1080p,
// against 50-56 ms and 61-227 ms for SVT-AV1 1.7, and x264 encodes 2-2.5
// times as many frames per second as libaom (1080p: 129-158 fps against
// 61; 4K: 31-35 against 20). Software AV1 is used when the viewer decodes
// only AV1 or the user forces it (Options.Codec), in the order fast SVT-AV1,
// libaom, SVT-AV1 at its slow safe preset; libopenh264 is the last resort.
func rank() []int {
	return []int{classAV1HW, classH264HW, classX264, classSVTFast, classAOM, classSVTSlow, classOpenH264}
}

// probed is the outcome of trying one encoder on this machine.
type probed struct {
	enc     *encoder // bound to the library version found
	slowSVT bool
	err     error
	at      time.Time
}

type probeKey struct{ ffmpeg, name string }

const (
	// failedProbeLife is how long a failed probe is believed, so that
	// installing a driver or a codec needs no host restart, yet opening a
	// session does not retry a dozen absent GPU encoders every time.
	failedProbeLife = 2 * time.Minute
	listingLife     = 2 * time.Minute
)

type listing struct {
	names map[string]bool
	at    time.Time
}

var (
	probeMu    sync.Mutex
	probeCache = map[probeKey]probed{}
	listCache  = map[string]listing{}
)

// listEncoders asks ffmpeg which encoders it was built with.
func listEncoders(ctx context.Context, ffmpeg string) (map[string]bool, error) {
	if l, ok := listCache[ffmpeg]; ok && time.Since(l.at) < listingLife {
		return l.names, nil
	}
	out, err := nowindow.Set(exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-encoders")).Output()
	if err != nil {
		return nil, fmt.Errorf("running %s -encoders: %v. %s", ffmpeg, err, installHint())
	}
	names := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && strings.HasPrefix(f[0], "V") {
			names[f[1]] = true
		}
	}
	listCache[ffmpeg] = listing{names, time.Now()}
	return names, nil
}

// probe test-encodes with e (a listed GPU encoder often has no GPU or
// driver behind it) and, for SVT-AV1, binds it to the plan for its version.
func probe(ctx context.Context, ffmpeg string, e *encoder) probed {
	key := probeKey{ffmpeg, e.name}
	if p, ok := probeCache[key]; ok && (p.err == nil || time.Since(p.at) < failedProbeLife) {
		return p
	}
	stderr, err := testEncode(ctx, ffmpeg, e)
	p := probed{enc: e, err: err, at: time.Now()}
	if err == nil && e.name == "libsvtav1" {
		v, text, known := parseSVTVersion(stderr)
		plan := svtPlanFor(v, known)
		bound := *e
		bound.args, bound.version = plan.args, text
		if p.slowSVT = plan.slow(); p.slowSVT {
			bound.warning = fmt.Sprintf("the host's SVT-AV1 %s loses small screen updates in its real-time presets, "+
				"so it runs at slow preset %d: expect a low frame rate (SVT-AV1 3.1+, H.264 or a GPU AV1 encoder avoids this)",
				orUnknown(text), svtSafePreset)
		}
		p.enc = &bound
	}
	if ctx.Err() == nil {
		probeCache[key] = p
	}
	return p
}

// selectEncoder picks the encoder for a session: the best-ranked one that
// ffmpeg has, that initialises on this machine, and whose codec the request
// allows.
func selectEncoder(ctx context.Context, ffmpeg string, req encRequest) (*encoder, error) {
	probeMu.Lock()
	defer probeMu.Unlock()
	listed, err := listEncoders(ctx, ffmpeg)
	if err != nil {
		return nil, err
	}
	if req.forced != "" {
		e := lookupEncoder(req.forced)
		if e == nil || !listed[e.name] {
			return nil, fmt.Errorf("ffmpeg at %s has no %s encoder", ffmpeg, req.forced)
		}
		p := probe(ctx, ffmpeg, e)
		if p.err != nil {
			return nil, fmt.Errorf("encoder %s does not work on this host: %v", e.name, p.err)
		}
		return p.enc, nil
	}
	var tried []string
	// slowSVT is the version of an SVT-AV1 passed over as too slow. It is
	// only ever probed once no H.264 encoder qualified (an AV1-only
	// viewer, or a forced AV1), so its warning reaches the hello only
	// when SVT-AV1 or the libaom fallback is what the session gets.
	var slowSVT string
	for _, class := range rank() {
		for _, e := range encoders {
			if !listed[e.name] || !req.allows(e.codec) || slices.Contains(req.exclude, e.name) {
				continue
			}
			// The class of libsvtav1 is only known once probed; for the
			// others, skip without probing.
			if e.name != "libsvtav1" && classOf(e, false) != class {
				continue
			}
			if e.name == "libsvtav1" && class != classSVTFast && class != classSVTSlow {
				continue
			}
			p := probe(ctx, ffmpeg, e)
			if p.err != nil {
				if class != classSVTSlow {
					tried = append(tried, fmt.Sprintf("%s (%v)", e.name, p.err))
				}
				continue
			}
			if classOf(p.enc, p.slowSVT) != class {
				slowSVT = orUnknown(p.enc.version)
				continue
			}
			if slowSVT != "" && class != classSVTSlow {
				bound := *p.enc
				bound.warning = fmt.Sprintf("the host's SVT-AV1 %s loses small screen updates in its real-time presets; "+
					"using %s instead (SVT-AV1 3.1+ or a GPU AV1 encoder gives AV1 at full speed)",
					slowSVT, e.name)
				return &bound, nil
			}
			return p.enc, nil
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	what := "AV1 or H.264"
	switch {
	case !req.allows(codecAV1) && !req.allows(codecH264):
		return nil, fmt.Errorf("the viewer cannot decode the requested codec (it decodes %s)", strings.Join(req.decode, ", "))
	case !req.allows(codecH264):
		what = "AV1"
	case !req.allows(codecAV1):
		what = "H.264"
	}
	msg := fmt.Sprintf("ffmpeg at %s has no working %s encoder", ffmpeg, what)
	if len(tried) > 0 {
		msg += " (tried " + strings.Join(tried, "; ") + ")"
	}
	return nil, errors.New(msg + ". Install an ffmpeg build with libsvtav1, libaom or libx264 (" + installHint() +
		"), or use a GPU encoder and its driver.")
}

// testEncode encodes a few frames of a synthetic source with exactly the
// options a session would use and returns what the encoder printed.
func testEncode(ctx context.Context, ffmpeg string, e *encoder) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error"}
	if e.preInput != nil {
		args = append(args, e.preInput()...)
	}
	p := encParams{fps: 30, bitrate: 1_000_000, gop: 60, pixels: 320 * 240}
	color := e.cpuColor(p.pixels, p.fps)
	args = append(args, "-f", "lavfi", "-i", "color=c=gray:s=320x240:r=30,format=bgr0", "-frames:v", "3",
		"-vf", "scale=flags=bilinear"+color.scaleOpts()+","+e.pixFmt)
	args = append(args, e.args(p)...)
	args = append(args, color.encArgs()...)
	args = append(args, "-f", "null", "-")
	cmd := nowindow.Set(exec.CommandContext(ctx, ffmpeg, args...))
	// SVT-AV1 prints its version banner at its own "info" level; make sure
	// an inherited SVT_LOG does not silence it.
	cmd.Env = append(os.Environ(), "SVT_LOG=3")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", errors.New(lastLine(stderr.String(), err.Error()))
	}
	return stderr.String(), nil
}

func orUnknown(s string) string {
	if s == "" {
		return "(unknown version)"
	}
	return s
}

func lastLine(s, fallback string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return fallback
}
