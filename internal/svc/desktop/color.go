package desktop

import "strings"

// Colour. A screen is RGB; every encoder takes YUV, and the viewer turns it
// back into RGB. Both conversions must use the same matrix and range, and
// the decoder only knows which ones the host used if the stream says so.
// Left to itself swscale converts with BT.601 at limited range and nothing
// labels the stream, while browsers (WebCodecs in Chromium, Safari and
// Firefox) decode an unlabelled stream as BT.709: pure green arrived as
// (0,216,2), red as (255,24,0). ffmpeg-based tests did not notice, because
// ffmpeg's decoder makes the same BT.601 assumption as its encoder side.
//
// So every path converts explicitly and labels what it did (the colour
// description in the H.264 VUI or the AV1 sequence header):
//
//   - colorFull: swscale converts with BT.709 at full range. Decoded in
//     Chromium the worst error over saturated colours, greys and UI
//     colours was 1 level (TestViewerColorsXvfb), against 12 for limited
//     range, and full range keeps all 256 luma levels for dark themes. It
//     costs CPU, so it is used where the pixel rate leaves room (cpuColor).
//   - colorLimited: BT.709 at limited range, for faster streams, for
//     encoders that cannot label full range (Media Foundation writes no
//     colour description at all, and an unlabelled limited-range BT.709
//     stream is exactly what a browser assumes), and for the GPU chains of
//     Quick Sync and AMF, whose converters are told to produce it.
//   - colorRGB601: NVENC fed RGB textures (the zero-copy chain) converts
//     itself with BT.601 at limited range and writes that matrix into the
//     stream; browsers ignore a matrix that comes without primaries and
//     transfer, so those are labelled too, and then honour BT.601.
type colorMode int

const (
	colorFull colorMode = iota
	colorLimited
	colorRGB601
)

func (m colorMode) String() string {
	switch m {
	case colorLimited:
		return "bt709 limited"
	case colorRGB601:
		return "bt601 limited (encoder converts)"
	}
	return "bt709 full"
}

// rangeName is the range in ffmpeg's option values.
func (m colorMode) rangeName() string {
	if m == colorFull {
		return "pc"
	}
	return "tv"
}

// scaleOpts are the options that make a scale filter convert RGB to YUV
// the way m says (appended to its other options).
func (m colorMode) scaleOpts() string {
	return ":out_color_matrix=bt709:out_range=" + m.rangeName()
}

// encArgs label the encoded stream for m.
func (m colorMode) encArgs() []string {
	matrix := "bt709"
	if m == colorRGB601 {
		matrix = "bt470bg"
	}
	return []string{"-colorspace", matrix, "-color_primaries", "bt709", "-color_trc", "bt709", "-color_range", m.rangeName()}
}

// cpuColor is how frames converted by swscale for e are made, at a pixel
// rate of pixels x fps: full range where that is cheap, limited range
// otherwise, and always when e cannot label full range in its stream.
//
// swscale has no SIMD path for full-range RGB to YUV: at 1080p it took 4.7
// ms a frame against 3.3 for limited range (ffmpeg 6.1, 4-core x86), and in
// a whole session at 1080p60 with libx264, where capture, conversion and
// encoder already share the cores, capture-to-encoded latency went from
// 18-21 ms (limited, as before) to 43-62 ms (TestPerfReportXvfb). So full
// range, the exact colours, is used up to fullRangeRate (1080p at 30 fps,
// 720p at 60), and faster streams are converted at limited range, which in
// Chromium is off by at most 12 levels on fully saturated primaries (greys
// and UI colours exact) where the unlabelled BT.601 stream was off by 39.
func (e *encoder) cpuColor(pixels, fps int) colorMode {
	if e.limitedRange || pixels <= 0 || pixels*fps > fullRangeRate {
		return colorLimited
	}
	return colorFull
}

// fullRangeRate is the highest pixel rate converted at full range (see
// cpuColor). A variable so tests can choose.
var fullRangeRate = 1920 * 1080 * 30

// gpuColor is what the capture's GPU chain (captureSpec.gpu) hands e: NVENC
// converts RGB textures itself; the other chains are set up to convert to
// limited-range BT.709 (see ddagrabGPU).
func gpuColor(e *encoder) colorMode {
	if strings.HasSuffix(e.name, "_nvenc") {
		return colorRGB601
	}
	return colorLimited
}

// yuvSource is a filter that labels raw YUV frames read from a pipe (the
// capture hub's), which arrive without any colour properties, with what
// the hub converted them to, so that scaling keeps them as they are.
func yuvSource(m colorMode) string {
	return "setparams=range=" + m.rangeName() + ":colorspace=bt709:color_primaries=bt709:color_trc=bt709"
}
