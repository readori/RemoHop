package desktop

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"

	"github.com/readori/RemoHop/internal/peer/peertest"
)

// Quality and performance reports for screen content. Both are skipped
// unless asked for; they print numbers, they do not assert targets.
//
// TestEncoderQuality encodes a scrolling text clip the way the host does
// and scores each frame against the source (SSIM and PSNR of the luma),
// with libx264 standing in for the hardware encoders this machine lacks:
// it is run with the bitrate and the rate-control buffer each version of
// the host gives a hardware encoder. The clip is a still second, a second
// and a half of scrolling, and a still second and a half, at 60 fps. Crops
// of the first frame (a key frame) and of a scrolling frame go to the
// directory, before and after side by side.
//
//	REMOHOP_QUALITY=/tmp/q [REMOHOP_QUALITY_IMAGE=tall.png] [REMOHOP_PERF_SIZES=1920x1080,2560x1440] \
//	    go test ./internal/svc/desktop -run TestEncoderQuality -v -count=1
//
// REMOHOP_QUALITY_IMAGE is a picture twice the screen height to scroll
// through (a screenshot of a text-heavy page), with {size} standing for
// WxH; without it the text is drawn with ffmpeg's drawtext from this
// package's sources.
func TestEncoderQuality(t *testing.T) {
	dir := os.Getenv("REMOHOP_QUALITY")
	if dir == "" {
		t.Skip("set REMOHOP_QUALITY to a directory to measure encoding quality")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	x264 := lookupEncoder("libx264")
	for _, size := range perfSizes() {
		w, h := size[0], size[1]
		name := fmt.Sprintf("%dx%d", w, h)
		src := filepath.Join(dir, "src-"+name+".nut")
		makeTextClip(t, ffmpeg, dir, w, h, src)
		const fps = 60
		pixels := w * h
		type config struct {
			name    string
			bitrate int64
			bufsize int64
		}
		oldRate := int64(float64(pixels) * fps * 0.06) // the automatic bitrate before
		var cfgs []config
		cfgs = append(cfgs, config{"before (hardware VBV)", oldRate, oldRate * 2 / fps})
		for _, fast := range []bool{false, true} {
			p := encParams{fps: fps, bitrate: autoBitrate(pixels, fps, fast), gop: 2 * fps, pixels: pixels, fast: fast}
			v, _ := strconv.ParseInt(vbv(p), 10, 64)
			label := "after, internet path"
			if fast {
				label = "after, fast path"
			}
			cfgs = append(cfgs, config{label + " (hardware VBV)", p.bitrate, v})
		}
		outs := map[string]string{}
		for _, c := range cfgs {
			p := encParams{fps: fps, bitrate: c.bitrate, gop: 2 * fps, pixels: pixels}
			args := x264.args(p)
			for i := range args {
				if args[i] == "-bufsize" {
					args[i+1] = strconv.FormatInt(c.bufsize, 10)
				}
			}
			out := filepath.Join(dir, fmt.Sprintf("%s-%s.h264", name, slug(c.name)))
			start := time.Now()
			run(t, ffmpeg, append(append([]string{"-hide_banner", "-loglevel", "error", "-y", "-i", src, "-an", "-vf", x264.pixFmt}, args...), "-f", "h264", out)...)
			took := time.Since(start)
			ssim, psnr := scoreClip(t, ffmpeg, out, src)
			sizes := packetSizes(t, out)
			t.Logf("%s %-34s %5.1f Mbit/s target, buffer %4.0f kbit: SSIM key frame %.4f, still %.4f, scroll %.4f, after scroll %.4f; PSNR %.2f dB; "+
				"largest frame %4.0f kbit; offline encode %.0f fps",
				name, c.name, float64(c.bitrate)/1e6, float64(c.bufsize)/1e3, ssim[0], mean(ssim[30:60]), mean(ssim[70:140]), mean(ssim[180:240]),
				mean(psnr), float64(maxInt(sizes))*8/1e3, float64(len(ssim))/took.Seconds())
			outs[c.name] = out
		}
		// Crops: the key frame and a frame in mid-scroll, before | after.
		for _, at := range []int{0, 110} {
			png := filepath.Join(dir, fmt.Sprintf("crop-%s-frame%d.png", name, at))
			cw, ch := min(640, w/2), min(360, h/2)
			filter := fmt.Sprintf("[0]select=eq(n\\,%d),crop=%d:%d:0:0,scale=iw*2:ih*2:flags=neighbor[a];[1]select=eq(n\\,%d),crop=%d:%d:0:0,scale=iw*2:ih*2:flags=neighbor[b];[a][b]hstack", at, cw, ch, at, cw, ch)
			run(t, ffmpeg, "-hide_banner", "-loglevel", "error", "-y", "-i", outs["before (hardware VBV)"], "-i", outs["after, fast path (hardware VBV)"],
				"-filter_complex", filter, "-frames:v", "1", png)
		}
	}
}

// perfSizes is REMOHOP_PERF_SIZES (WxH,WxH...), by default 1080p and 1440p.
func perfSizes() [][2]int {
	spec := os.Getenv("REMOHOP_PERF_SIZES")
	if spec == "" {
		spec = "1920x1080,2560x1440"
	}
	var out [][2]int
	for _, f := range strings.Split(spec, ",") {
		var w, h int
		if _, err := fmt.Sscanf(strings.TrimSpace(f), "%dx%d", &w, &h); err == nil && w > 0 && h > 0 {
			out = append(out, [2]int{w &^ 1, h &^ 1})
		}
	}
	return out
}

func slug(s string) string {
	return strings.Trim(regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(strings.ToLower(s), "-"), "-")
}

func run(t *testing.T, name string, args ...string) {
	t.Helper()
	if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
}

// makeTextClip writes the scrolling text clip for w x h to out.
func makeTextClip(t *testing.T, ffmpeg, dir string, w, h int, out string) {
	t.Helper()
	tall := strings.ReplaceAll(os.Getenv("REMOHOP_QUALITY_IMAGE"), "{size}", fmt.Sprintf("%dx%d", w, h))
	if tall == "" {
		tall = filepath.Join(dir, fmt.Sprintf("tall-%dx%d.png", w, h))
		var dt []string
		files := []string{"host.go", "encoder.go", "client.go", "protocol.go", "capture_hub.go", "bench.go"}
		colors := []string{"0xd4d4d4", "0x9cdcfe", "0xce9178", "0xb5cea8"}
		col := 0
		for x := 8; x+300 < w; x += w / 4 {
			text, err := filepath.Abs(files[col%len(files)])
			if err != nil {
				t.Fatal(err)
			}
			dt = append(dt, fmt.Sprintf("drawtext=fontfile=/usr/share/fonts/truetype/dejavu/DejaVuSansMono.ttf:textfile=%s:fontsize=13:line_spacing=3:fontcolor=%s:x=%d:y=8",
				text, colors[col%len(colors)], x))
			col++
		}
		run(t, ffmpeg, "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", fmt.Sprintf("color=c=0x1e1f22:s=%dx%d", w, 2*h),
			"-vf", strings.Join(dt, ","), "-frames:v", "1", tall)
	}
	crop := fmt.Sprintf("crop=%d:%d:0:'trunc(if(lt(t\\,1)\\,0\\,if(lt(t\\,2.5)\\,(t-1)*%d/4.5\\,%d/3)))',format=yuv420p", w, h, h, h)
	run(t, ffmpeg, "-hide_banner", "-loglevel", "error", "-y", "-loop", "1", "-framerate", "60", "-i", tall, "-vf", crop, "-t", "4", "-c:v", "ffv1", out)
}

var (
	ssimRE = regexp.MustCompile(`Y:([\d.]+)`)
	psnrRE = regexp.MustCompile(`psnr_y:([\d.]+|inf)`)
)

// scoreClip returns per-frame luma SSIM and PSNR of enc against src.
func scoreClip(t *testing.T, ffmpeg, enc, src string) (ssim, psnr []float64) {
	t.Helper()
	ss, ps := enc+".ssim", enc+".psnr"
	run(t, ffmpeg, "-hide_banner", "-loglevel", "error", "-y", "-i", enc, "-i", src,
		"-lavfi", fmt.Sprintf("[0][1]ssim=stats_file=%s;[0][1]psnr=stats_file=%s", ss, ps), "-f", "null", "-")
	read := func(path string, re *regexp.Regexp) []float64 {
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		var out []float64
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if m := re.FindStringSubmatch(sc.Text()); m != nil {
				v, err := strconv.ParseFloat(m[1], 64)
				if err != nil {
					v = 99 // inf: identical
				}
				out = append(out, v)
			}
		}
		return out
	}
	ssim, psnr = read(ss, ssimRE), read(ps, psnrRE)
	if len(ssim) < 240 || len(psnr) < 240 {
		t.Fatalf("scored %d/%d frames of %s", len(ssim), len(psnr), enc)
	}
	return ssim, psnr
}

func packetSizes(t *testing.T, path string) []int {
	t.Helper()
	out, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "v", "-show_entries", "packet=size", "-of", "csv=p=0", path).Output()
	if err != nil {
		t.Skipf("ffprobe: %v", err)
	}
	var sizes []int
	for _, l := range strings.Fields(string(out)) {
		if n, err := strconv.Atoi(strings.TrimSuffix(l, ",")); err == nil {
			sizes = append(sizes, n)
		}
	}
	return sizes
}

func mean(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	var s float64
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}

func maxInt(v []int) int {
	m := 0
	for _, x := range v {
		m = max(m, x)
	}
	return m
}

// scrollText keeps an Xvfb screen showing text that scrolls for two
// seconds and then stands still for two, over and over: the screen
// content a remote desktop mostly carries. The text is drawn once into a
// pixmap twice the screen's height with the server's built-in font, and
// each frame copies a window of it onto the root, on the X server.
func scrollText(t *testing.T, x *xgb.Conn, w, h int) {
	t.Helper()
	scr := xproto.Setup(x).DefaultScreen(x)
	root := scr.Root
	pix, err := xproto.NewPixmapId(x)
	if err != nil {
		t.Fatal(err)
	}
	xproto.CreatePixmap(x, scr.RootDepth, pix, xproto.Drawable(root), uint16(w), uint16(2*h))
	gc, err := xproto.NewGcontextId(x)
	if err != nil {
		t.Fatal(err)
	}
	font, err := xproto.NewFontId(x)
	if err != nil {
		t.Fatal(err)
	}
	if err := xproto.OpenFontChecked(x, font, uint16(len("fixed")), "fixed").Check(); err != nil {
		t.Skipf("X font: %v", err)
	}
	xproto.CreateGC(x, gc, xproto.Drawable(pix), xproto.GcForeground|xproto.GcBackground|xproto.GcFont, []uint32{0xd4d4d4, 0x1e1f22, uint32(font)})
	xproto.ChangeGC(x, gc, xproto.GcForeground, []uint32{0x1e1f22})
	xproto.PolyFillRectangle(x, xproto.Drawable(pix), gc, []xproto.Rectangle{{Width: uint16(w), Height: uint16(2 * h)}})
	src, err := os.ReadFile("host.go")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(src), "\n")
	colors := []uint32{0xd4d4d4, 0x9cdcfe, 0xce9178, 0xb5cea8}
	n := 0
	for col := 0; col*480 < w; col++ {
		for y := 13; y < 2*h; y += 14 {
			l := strings.ReplaceAll(lines[n%len(lines)], "\t", "    ")
			n++
			if len(l) > 78 {
				l = l[:78]
			}
			if l == "" {
				continue
			}
			xproto.ChangeGC(x, gc, xproto.GcForeground, []uint32{colors[(n/7)%len(colors)]})
			xproto.ImageText8(x, byte(len(l)), xproto.Drawable(pix), gc, int16(col*480+6), int16(y), l)
		}
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTicker(16 * time.Millisecond)
		defer tick.Stop()
		start := time.Now()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
			}
			phase := time.Since(start) % (4 * time.Second)
			off := 0
			if phase < 2*time.Second {
				off = int(phase.Seconds() * float64(h) / 2) // scrolling: half a screen a second
			} else {
				off = h // still
			}
			xproto.CopyArea(x, xproto.Drawable(pix), xproto.Drawable(root), gc, 0, int16(off), 0, 0, uint16(w), uint16(h))
			x.Sync()
		}
	}()
	t.Cleanup(func() { close(stop); <-done })
}

// TestPerfReportXvfb measures whole sessions over loopback QUIC (a fast
// path) with the screen scrolling text: frame rate, bitrate, capture to
// receipt latency, drops and encoder restarts, per screen size. The first
// 8 s are not measured, so that the host's adaptations have settled.
//
//	REMOHOP_PERF_REPORT=1 [REMOHOP_PERF_SIZES=1920x1080,2560x1440,3840x2160] \
//	    go test ./internal/svc/desktop -run TestPerfReportXvfb -v -count=1
func TestPerfReportXvfb(t *testing.T) {
	if os.Getenv("REMOHOP_PERF_REPORT") == "" {
		t.Skip("set REMOHOP_PERF_REPORT=1 to print performance numbers")
	}
	for _, size := range perfSizes() {
		w, h := size[0], size[1]
		t.Run(fmt.Sprintf("%dx%d", w, h), func(t *testing.T) {
			x := startXvfb(t, w, h)
			scrollText(t, x, w, h)
			client, _ := peertest.Pair(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			fps := 60
			if v, err := strconv.Atoi(os.Getenv("REMOHOP_PERF_FPS")); err == nil && v > 0 {
				fps = v
			}
			res, err := Bench(ctx, client, Options{FPS: fps, Codec: codecH264}, BenchOptions{Warmup: 8 * time.Second, Duration: 10 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			b, _ := json.Marshal(res)
			t.Logf("%dx%d\n%s%s", w, h, res.String(), b)
		})
	}
}
