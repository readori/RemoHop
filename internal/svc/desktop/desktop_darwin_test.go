package desktop

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/inject"
	"github.com/readori/RemoHop/internal/peer/peertest"
)

// The macOS desktop end-to-end tests run only with REMOHOP_DESKTOP_E2E=1, on
// a Mac with a logged-in desktop, ffmpeg (REMOHOP_FFMPEG or PATH), Xcode's
// swiftc (for the test window) and, for the process that runs them, the
// Screen Recording and Accessibility permissions. They capture the real
// screen through AVFoundation, report which H.264 encoder the host picks
// (VideoToolbox where the machine has a hardware encoder), check what a
// viewer decodes against a window of known colours, and send pointer input
// through the session into that window.
//
// "RESULT" log lines are the findings a run is read for.

func macDesktopE2E(t *testing.T) string {
	t.Helper()
	if os.Getenv("REMOHOP_DESKTOP_E2E") != "1" {
		t.Skip("set REMOHOP_DESKTOP_E2E=1 to capture and control this Mac's desktop")
	}
	ffmpeg, err := ffmpegPath()
	if err != nil {
		t.Fatal(err)
	}
	return ffmpeg
}

// macPatternSource is a borderless window above everything else on the
// main display: left half red, right half blue, a green square in the
// middle, and a corner that blinks so the screen keeps changing. It prints
// where it is and every mouse press it receives.
const macPatternSource = `import AppKit

setvbuf(stdout, nil, _IOLBF, 0)

final class Pattern: NSView {
    var tick = false
    override var isFlipped: Bool { true }
    override func acceptsFirstMouse(for event: NSEvent?) -> Bool { true }
    override func draw(_ dirty: NSRect) {
        let w = bounds.width, h = bounds.height
        NSColor(srgbRed: 1, green: 0, blue: 0, alpha: 1).setFill()
        NSRect(x: 0, y: 0, width: w / 2, height: h).fill()
        NSColor(srgbRed: 0, green: 0, blue: 1, alpha: 1).setFill()
        NSRect(x: w / 2, y: 0, width: w / 2, height: h).fill()
        NSColor(srgbRed: 0, green: 1, blue: 0, alpha: 1).setFill()
        NSRect(x: w / 2 - 40, y: h / 2 - 40, width: 80, height: 80).fill()
        (tick ? NSColor.white : NSColor.black).setFill()
        NSRect(x: w - 12, y: h - 12, width: 12, height: 12).fill()
    }
    private func report(_ what: String, _ e: NSEvent) {
        let p = convert(e.locationInWindow, from: nil)
        print("\(what) \(e.buttonNumber) \(Int(p.x.rounded(.down))) \(Int(p.y.rounded(.down))) \(e.clickCount)")
    }
    override func mouseDown(with e: NSEvent) { report("DOWN", e) }
    override func mouseUp(with e: NSEvent) { report("UP", e) }
    override func rightMouseDown(with e: NSEvent) { report("DOWN", e) }
    override func rightMouseUp(with e: NSEvent) { report("UP", e) }
}

let app = NSApplication.shared
app.setActivationPolicy(.accessory)
guard let screen = NSScreen.screens.first else {
    print("FAIL no screen")
    exit(1)
}
let f = screen.frame
let x: CGFloat = 120, y: CGFloat = 140, w: CGFloat = 480, h: CGFloat = 320
let win = NSWindow(contentRect: NSRect(x: f.minX + x, y: f.maxY - y - h, width: w, height: h),
                   styleMask: [.borderless], backing: .buffered, defer: false)
win.level = .screenSaver
win.isOpaque = true
win.hasShadow = false
win.ignoresMouseEvents = false
win.collectionBehavior = [.canJoinAllSpaces, .stationary]
let view = Pattern(frame: NSRect(x: 0, y: 0, width: w, height: h))
win.contentView = view
win.orderFrontRegardless()
let timer = Timer.scheduledTimer(withTimeInterval: 0.1, repeats: true) { _ in
    view.tick.toggle()
    view.setNeedsDisplay(NSRect(x: w - 12, y: h - 12, width: 12, height: 12))
}
DispatchQueue.main.asyncAfter(deadline: .now() + 0.7) {
    print("READY \(Int(x)) \(Int(y)) \(Int(w)) \(Int(h)) \(screen.backingScaleFactor) \(Int(f.width)) \(Int(f.height)) \(win.isVisible)")
}
_ = timer
app.run()
`

// macPattern is the running test window: its rectangle in points from the
// main display's top-left corner, and the mouse events it reports.
type macPattern struct {
	x, y, w, h int
	scale      float64
	events     chan string
}

// startMacPattern builds and shows the test window.
func startMacPattern(t *testing.T) *macPattern {
	t.Helper()
	swiftc, err := exec.LookPath("swiftc")
	if err != nil {
		t.Fatalf("swiftc not found (the test window needs Xcode or its command-line tools): %v", err)
	}
	dir := t.TempDir()
	src, bin := filepath.Join(dir, "pattern.swift"), filepath.Join(dir, "remohop-e2e-pattern")
	if err := os.WriteFile(src, []byte(macPatternSource), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if out, err := exec.CommandContext(ctx, swiftc, "-swift-version", "5", "-O", "-o", bin, src).CombinedOutput(); err != nil {
		t.Fatalf("building the test window: %v\n%s", err, out)
	}
	cmd := exec.Command(bin)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
	})
	p := &macPattern{events: make(chan string, 64)}
	ready := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			line := sc.Text()
			if strings.HasPrefix(line, "READY ") || strings.HasPrefix(line, "FAIL ") {
				ready <- line
				continue
			}
			select {
			case p.events <- line:
			default:
			}
		}
		close(ready)
	}()
	select {
	case line := <-ready:
		var sw, sh int
		var visible bool
		if _, err := fmt.Sscanf(line, "READY %d %d %d %d %g %d %d %t", &p.x, &p.y, &p.w, &p.h, &p.scale, &sw, &sh, &visible); err != nil {
			t.Fatalf("test window: %q (%v) %s", line, err, stderr.String())
		}
		t.Logf("test window at %d,%d %dx%d points, scale %g, screen %dx%d points, visible %v", p.x, p.y, p.w, p.h, p.scale, sw, sh, visible)
	case <-time.After(30 * time.Second):
		t.Fatalf("the test window did not appear: %s", stderr.String())
	}
	return p
}

// macDecodeLast decodes a recording with ffmpeg and returns its last frame
// as packed RGB.
func macDecodeLast(t *testing.T, ffmpeg, mp4 string) (w, h int, rgb []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	out := mp4 + ".ppm"
	if b, err := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error", "-y", "-i", mp4,
		"-update", "1", "-f", "image2", "-c:v", "ppm", out).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg decode: %v\n%s", err, b)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(bytes.NewReader(data))
	var magic string
	var maxv int
	if _, err := fmt.Fscan(r, &magic, &w, &h, &maxv); err != nil || magic != "P6" {
		t.Fatalf("bad ppm: %v %q", err, magic)
	}
	r.ReadByte()
	rgb, _ = io.ReadAll(r)
	if len(rgb) < w*h*3 {
		t.Fatal("short ppm")
	}
	return w, h, rgb
}

// dominant reports whether channel c (0 red, 1 green, 2 blue) clearly
// dominates the pixel. The display's colour profile shifts pure sRGB
// colours, so exact values are not compared.
func dominant(px [3]int, c int) bool {
	for i, v := range px {
		if i != c && px[c]-v < 60 {
			return false
		}
	}
	return px[c] >= 140
}

// macRecord writes the next n frames of the session to an MP4. If the host
// restarts its encoder meanwhile (a new hello), the recording starts over,
// so the file always holds one stream from its key frame.
func macRecord(t *testing.T, ctx context.Context, sess *Session, n int) (string, []*Frame) {
	t.Helper()
	dir := t.TempDir()
	hello := sess.Hello()
	for attempt := 1; ; attempt++ {
		path := filepath.Join(dir, fmt.Sprintf("out%d.mp4", attempt))
		rec, err := NewRecorder(path, false)
		if err != nil {
			t.Fatal(err)
		}
		if err := rec.SetStream(hello); err != nil {
			t.Fatal(err)
		}
		var frames []*Frame
		restarted := false
		for len(frames) < n && !restarted {
			ev, err := sess.Next(ctx)
			if err != nil {
				t.Fatalf("after %d frames: %v", len(frames), err)
			}
			if ev.Hello != nil {
				t.Logf("the host restarted its encoder after %d frames (generation %d, %d fps, %d bit/s)", len(frames), ev.Hello.Generation, ev.Hello.FPS, ev.Hello.Bitrate)
				hello, restarted = *ev.Hello, true
			}
			if ev.Frame != nil {
				if err := rec.WriteFrame(ev.Frame); err != nil {
					t.Fatal(err)
				}
				frames = append(frames, ev.Frame)
			}
		}
		if err := rec.Close(); err != nil {
			t.Fatal(err)
		}
		if !restarted {
			return path, frames
		}
		if attempt == 4 {
			t.Fatal("the host keeps restarting its encoder")
		}
	}
}

// TestMacCaptureDevices lists what AVFoundation offers ffmpeg on this Mac.
func TestMacCaptureDevices(t *testing.T) {
	ffmpeg := macDesktopE2E(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// Listing "fails" by design: there is no input to open afterwards.
	out, _ := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-nostdin", "-f", "avfoundation", "-list_devices", "true", "-i", "").CombinedOutput()
	var devices []string
	for _, line := range strings.Split(string(out), "\n") {
		if i := strings.Index(line, "] ["); i >= 0 {
			devices = append(devices, strings.TrimSpace(line[i+2:]))
		}
	}
	t.Logf("RESULT AVFoundation devices: %s", strings.Join(devices, "; "))
	if !strings.Contains(string(out), "Capture screen 0") {
		t.Errorf("AVFoundation offers no screen capture device:\n%s", out)
	}
}

// TestMacEncoders says which encoders of the ffmpeg build work on this Mac,
// whether VideoToolbox has a hardware H.264 encoder here, and what the host
// selects.
func TestMacEncoders(t *testing.T) {
	ffmpeg := macDesktopE2E(t)
	if out, err := exec.Command(ffmpeg, "-hide_banner", "-version").Output(); err == nil {
		t.Logf("ffmpeg: %s", strings.SplitN(string(out), "\n", 2)[0])
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	probeMu.Lock()
	listed, err := listEncoders(ctx, ffmpeg)
	probeMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	working := map[string]bool{}
	for _, e := range encoders {
		if !listed[e.name] {
			continue
		}
		probeMu.Lock()
		p := probe(ctx, ffmpeg, stampOf(ffmpeg), e)
		probeMu.Unlock()
		working[e.name] = p.err == nil
		if p.err != nil {
			t.Logf("RESULT encoder %-18s listed, does not work here: %v", e.name, p.err)
		} else {
			t.Logf("RESULT encoder %-18s works", e.name)
		}
	}
	if !listed["h264_videotoolbox"] {
		t.Errorf("this ffmpeg has no h264_videotoolbox encoder")
	} else if !working["h264_videotoolbox"] {
		// Tell "this machine has no hardware encoder" (a VM) from "the
		// options are wrong": the same options, software allowed.
		sw := *lookupEncoder("h264_videotoolbox")
		hwArgs := sw.args
		sw.args = func(p encParams) []string {
			a := hwArgs(p)
			for i := range a {
				if a[i] == "-allow_sw" && i+1 < len(a) {
					a[i+1] = "1"
				}
			}
			return a
		}
		if _, err := testEncode(ctx, ffmpeg, &sw); err != nil {
			t.Errorf("RESULT VideoToolbox fails even with software encoding allowed (the encoder options are at fault): %v", err)
		} else {
			t.Logf("RESULT VideoToolbox works only with software encoding allowed: this machine has no hardware H.264 encoder (a virtual machine); the host falls back to the next encoder")
		}
	}
	e, err := selectEncoder(ctx, ffmpeg, encRequest{})
	if err != nil {
		t.Fatalf("selectEncoder: %v", err)
	}
	t.Logf("RESULT the host selects %s (hardware=%v, codec=%s) %s", e.name, e.hw, e.codec, e.warning)
	switch {
	case working["h264_videotoolbox"] && e.name != "h264_videotoolbox":
		t.Errorf("VideoToolbox works but the host selected %s", e.name)
	case !working["h264_videotoolbox"] && working["libx264"] && e.name != "libx264":
		t.Errorf("without VideoToolbox the host should fall back to libx264, it selected %s", e.name)
	}
}

// TestMacDesktopE2E runs real sessions against this Mac's screen: capture,
// encode (the host's own choice, then libx264 explicitly), decode, compare
// with what is on the screen, then pointer input through the session.
func TestMacDesktopE2E(t *testing.T) {
	ffmpeg := macDesktopE2E(t)
	ds, err := inject.MacDisplays()
	if err != nil {
		t.Fatal(err)
	}
	for i, d := range ds {
		t.Logf("display %d: %+v", i+1, d)
	}
	main := ds[0]
	granted, known := screenCaptureAccess()
	t.Logf("RESULT Screen Recording permission: granted=%v (known=%v)", granted, known)
	trusted, _ := inject.AccessibilityTrusted()
	t.Logf("RESULT Accessibility permission: granted=%v", trusted)
	spec, err := discoverCapture(0, 30)
	if err != nil {
		t.Fatal(err)
	}
	if spec.width != main.PixelWidth || spec.height != main.PixelHeight || (granted && spec.warning != "") {
		t.Errorf("capture spec %dx%d warning %q, display is %dx%d", spec.width, spec.height, spec.warning, main.PixelWidth, main.PixelHeight)
	}
	if _, err := discoverCapture(len(ds)+1, 30); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("display %d accepted: %v", len(ds)+1, err)
	}
	win := startMacPattern(t)
	client, _ := peertest.Pair(t)

	pctx, pcancel := context.WithTimeout(context.Background(), 2*time.Minute)
	info, err := Probe(pctx, client)
	pcancel()
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	t.Logf("RESULT probe: screen %dx%d, encoders %v, input %v %q, warning %q", info.ScreenWidth, info.ScreenHeight, info.Encoders, info.Input, info.InputError, info.Warning)

	// Where the window's parts are in a decoded frame of fw x fh pixels.
	framePoint := func(fw, fh int, wx, wy float64) (int, int) {
		return int((float64(win.x) + wx) * win.scale * float64(fw) / float64(main.PixelWidth)),
			int((float64(win.y) + wy) * win.scale * float64(fh) / float64(main.PixelHeight))
	}
	checkPicture := func(t *testing.T, path string, wantW, wantH int) {
		t.Helper()
		w, h, rgb := macDecodeLast(t, ffmpeg, path)
		if w != wantW || h != wantH {
			t.Errorf("decoded %dx%d, hello said %dx%d", w, h, wantW, wantH)
		}
		ww, wh := float64(win.w), float64(win.h)
		for _, area := range []struct {
			name    string
			channel int
			at      [][2]float64
		}{
			{"red left half", 0, [][2]float64{{ww * 0.15, wh * 0.2}, {ww * 0.25, wh * 0.8}, {ww * 0.1, wh * 0.5}}},
			{"blue right half", 2, [][2]float64{{ww * 0.85, wh * 0.2}, {ww * 0.75, wh * 0.8}, {ww * 0.9, wh * 0.5}}},
			{"green square", 1, [][2]float64{{ww/2 - 25, wh/2 - 25}, {ww/2 + 25, wh/2 - 25}, {ww/2 - 25, wh/2 + 25}}},
		} {
			// Three samples each, two must match: the pointer may be
			// drawn over one.
			ok := 0
			var got [][3]int
			for _, p := range area.at {
				x, y := framePoint(w, h, p[0], p[1])
				if x < 0 || y < 0 || x >= w || y >= h {
					continue
				}
				i := (y*w + x) * 3
				px := [3]int{int(rgb[i]), int(rgb[i+1]), int(rgb[i+2])}
				got = append(got, px)
				if dominant(px, area.channel) {
					ok++
				}
			}
			if ok < 2 {
				t.Errorf("%s of the test window is not in the decoded picture (pixels %v). If the picture shows only the desktop "+
					"background, the process lacks the Screen Recording permission (granted=%v)", area.name, got, granted)
			}
		}
	}

	stream := func(t *testing.T, opts Options) (*Session, Hello) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		t.Cleanup(cancel)
		sess, err := Open(ctx, client, opts)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		t.Cleanup(func() { sess.Close() })
		h := sess.Hello()
		t.Logf("RESULT stream: %dx%d %s encoder %s (hardware=%v) %d fps %d bit/s, input=%v %q, warning %q",
			h.Width, h.Height, h.Codec, h.Encoder, h.Hardware, h.FPS, h.Bitrate, h.Input, h.InputError, h.Warning)
		if h.Width != main.PixelWidth&^1 || h.Height != main.PixelHeight&^1 {
			t.Errorf("stream is %dx%d, the display has %dx%d pixels", h.Width, h.Height, main.PixelWidth, main.PixelHeight)
		}
		if !codecRE.MatchString(h.Codec) || len(h.CodecConfig) == 0 || h.FPS != opts.FPS {
			t.Errorf("bad hello %+v", h)
		}
		start := time.Now()
		path, frames := macRecord(t, ctx, sess, 30)
		el := time.Since(start)
		if !frames[0].Key {
			t.Error("first frame is not a key frame")
		}
		for i, f := range frames[1:] {
			if f.Seq != frames[i].Seq+1 {
				t.Errorf("sequence gap %d -> %d", frames[i].Seq, f.Seq)
			}
		}
		t.Logf("RESULT %s: 30 frames in %v (%.1f fps)", h.Encoder, el.Round(time.Millisecond), 30/el.Seconds())
		checkPicture(t, path, h.Width, h.Height)
		return sess, h
	}

	var auto Hello
	t.Run("host choice", func(t *testing.T) {
		probeMu.Lock()
		vt := probe(context.Background(), ffmpeg, stampOf(ffmpeg), lookupEncoder("h264_videotoolbox"))
		probeMu.Unlock()
		sess, h := stream(t, Options{FPS: 30})
		auto = h
		if !h.IsH264() {
			t.Errorf("codec %s, want H.264 (this ffmpeg has no hardware AV1 on macOS)", h.Codec)
		}
		if vt.err == nil {
			if h.Encoder != "h264_videotoolbox" || !h.Hardware {
				t.Errorf("VideoToolbox works here but the session uses %s", h.Encoder)
			}
		} else {
			t.Logf("RESULT no VideoToolbox hardware encoder here (%v); the session fell back to %s", vt.err, h.Encoder)
			if h.Hardware {
				t.Errorf("fallback encoder %s is marked hardware", h.Encoder)
			}
		}
		// A key frame on request, mid-GOP.
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		asked := time.Now()
		if err := sess.RequestKeyframe(); err != nil {
			t.Fatal(err)
		}
		for {
			ev, err := sess.Next(ctx)
			if err != nil {
				t.Fatalf("no key frame after the request: %v", err)
			}
			if ev.Frame != nil && ev.Frame.Key {
				break
			}
		}
		t.Logf("RESULT %s: key frame %v after the request", h.Encoder, time.Since(asked).Round(time.Millisecond))
	})
	t.Run("libx264", func(t *testing.T) {
		if auto.Encoder == "libx264" {
			t.Skip("the host's own choice was libx264 already")
		}
		probeMu.Lock()
		listed, _ := listEncoders(context.Background(), ffmpeg)
		probeMu.Unlock()
		if !listed["libx264"] {
			t.Skip("this ffmpeg has no libx264")
		}
		_, h := stream(t, Options{FPS: 30, Encoder: "libx264"})
		if h.Encoder != "libx264" || h.Hardware {
			t.Errorf("forced libx264, got %s", h.Encoder)
		}
	})
	// Software AV1, as a viewer that decodes nothing else (or --codec av1)
	// gets it on a Mac: there is no hardware AV1 encoder in ffmpeg here.
	t.Run("software AV1", func(t *testing.T) {
		_, h := stream(t, Options{FPS: 30, Codec: codecAV1})
		if h.IsH264() || h.Hardware || !strings.HasPrefix(h.Codec, "av01.") {
			t.Errorf("asked for AV1, got %s from %s (hardware=%v)", h.Codec, h.Encoder, h.Hardware)
		}
	})
	t.Run("half size", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		sess, err := Open(ctx, client, Options{FPS: 15, Scale: 0.5})
		if err != nil {
			t.Fatal(err)
		}
		defer sess.Close()
		h := sess.Hello()
		if h.Width != (main.PixelWidth/2)&^1 || h.Height != (main.PixelHeight/2)&^1 {
			t.Errorf("scale 0.5 gave %dx%d of %dx%d", h.Width, h.Height, main.PixelWidth, main.PixelHeight)
		}
		path, _ := macRecord(t, ctx, sess, 10)
		checkPicture(t, path, h.Width, h.Height)
	})

	// Input: a move and a click sent by the client arrive at the window,
	// where the picture shows it.
	t.Run("input", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		sess, err := Open(ctx, client, Options{FPS: 15})
		if err != nil {
			t.Fatal(err)
		}
		defer sess.Close()
		go func() { // keep the stream flowing
			for {
				if _, err := sess.Next(ctx); err != nil {
					return
				}
			}
		}()
		h := sess.Hello()
		if !h.Input {
			t.Fatalf("the host accepts no input: %s", h.InputError)
		}
		if h.Unsupported[ControlCtrlAltDel] == "" || h.Unsupported[ControlType] != "" {
			t.Errorf("unsupported controls: %v", h.Unsupported)
		}
		reader, err := inject.Open(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer reader.Close()
		// A viewer position, as a fraction of the picture, for a point of
		// the window given in points from its top-left corner.
		send := func(kind string, wx, wy float64, down bool) (px, py float64) {
			t.Helper()
			pixX := int((float64(win.x) + wx) * win.scale)
			pixY := int((float64(win.y) + wy) * win.scale)
			ev := InputEvent{Kind: kind, X: float64(pixX) / float64(main.PixelWidth-1), Y: float64(pixY) / float64(main.PixelHeight-1), Down: down}
			if err := sess.SendInput(ev); err != nil {
				t.Fatal(err)
			}
			return main.PixelToPoint(pixX, pixY)
		}
		wantX, wantY := send(InputMove, 100, 60, false)
		var gx, gy float64
		deadline := time.Now().Add(5 * time.Second)
		for {
			gx, gy, _ = reader.PointerF()
			if math.Abs(gx-wantX) < 1 && math.Abs(gy-wantY) < 1 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("pointer at %.1f,%.1f after a move to %.1f,%.1f sent through the session", gx, gy, wantX, wantY)
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Logf("RESULT pointer move through the session: at %.1f,%.1f (wanted %.1f,%.1f)", gx, gy, wantX, wantY)

		for len(win.events) > 0 {
			<-win.events
		}
		send(InputButton, 300, 200, true)
		time.Sleep(60 * time.Millisecond)
		send(InputButton, 300, 200, false)
		var got []string
		timeout := time.After(10 * time.Second)
		for len(got) < 2 {
			select {
			case line := <-win.events:
				got = append(got, line)
			case <-timeout:
				x, y, _ := reader.PointerF()
				t.Fatalf("the test window saw %v instead of a press and a release (pointer now at %.1f,%.1f)", got, x, y)
			}
		}
		t.Logf("RESULT click through the session: the window saw %v", got)
		for i, want := range []string{"DOWN", "UP"} {
			var what string
			var btn, x, y, clicks int
			if _, err := fmt.Sscanf(got[i], "%s %d %d %d %d", &what, &btn, &x, &y, &clicks); err != nil {
				t.Fatalf("event %q: %v", got[i], err)
			}
			if what != want || btn != 0 || x < 298 || x > 302 || y < 198 || y > 202 {
				t.Errorf("event %d is %q, want %s of the left button at 300,200", i, got[i], want)
			}
		}
	})

	// A view-only session takes no input.
	t.Run("view only", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		ro, err := Open(ctx, client, Options{FPS: 15, ViewOnly: true})
		if err != nil {
			t.Fatal(err)
		}
		defer ro.Close()
		if ro.Hello().Input || ro.SendInput(InputEvent{Kind: InputMove, X: 0.5, Y: 0.5}) != ErrViewOnly {
			t.Errorf("view-only session accepts input: %+v", ro.Hello())
		}
	})

	// Two viewers at once: a second capture and a second encoder session
	// while the first is still streaming.
	t.Run("two sessions", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		first, err := Open(ctx, client, Options{FPS: 15})
		if err != nil {
			t.Fatal(err)
		}
		defer first.Close()
		go func() {
			for {
				if _, err := first.Next(ctx); err != nil {
					return
				}
			}
		}()
		octx, ocancel := context.WithTimeout(ctx, 30*time.Second)
		defer ocancel()
		start := time.Now()
		second, err := Open(octx, client, Options{FPS: 15})
		if err != nil {
			t.Errorf("RESULT a second session while one (%s) is streaming: %v after %v", first.Hello().Encoder, err, time.Since(start).Round(time.Millisecond))
			// Which half cannot run twice? Each alone, beside the session
			// that is still streaming.
			try := func(what string, args ...string) {
				c, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				began := time.Now()
				out, err := exec.CommandContext(c, ffmpeg, append([]string{"-hide_banner", "-nostdin", "-loglevel", "error"}, args...)...).CombinedOutput()
				if err != nil {
					t.Logf("RESULT beside a streaming session, %s: FAILS after %v: %v: %s", what, time.Since(began).Round(time.Millisecond), err, lastLine(string(out), ""))
				} else {
					t.Logf("RESULT beside a streaming session, %s: works (%v)", what, time.Since(began).Round(time.Millisecond))
				}
			}
			try("a second AVFoundation screen capture", "-f", "avfoundation", "-pixel_format", "bgr0", "-framerate", "15",
				"-i", "Capture screen 0:none", "-frames:v", "5", "-f", "null", "-")
			vt := lookupEncoder("h264_videotoolbox")
			try("a second VideoToolbox encoder", append(append([]string{"-f", "lavfi", "-i", "color=c=gray:s=640x480:r=30", "-frames:v", "5", "-vf", vt.pixFmt},
				vt.args(encParams{fps: 30, bitrate: 1_000_000, gop: 60, pixels: 640 * 480})...), "-f", "null", "-")...)
			x264ctx, x264cancel := context.WithTimeout(ctx, 25*time.Second)
			defer x264cancel()
			if s3, err := Open(x264ctx, client, Options{FPS: 15, Encoder: "libx264"}); err != nil {
				t.Logf("RESULT beside a streaming session, a second session with libx264: FAILS: %v", err)
			} else {
				t.Logf("RESULT beside a streaming session, a second session with libx264: works")
				s3.Close()
			}
			return
		}
		defer second.Close()
		path, _ := macRecord(t, ctx, second, 10)
		checkPicture(t, path, second.Hello().Width, second.Hello().Height)
		t.Logf("RESULT two sessions at once: both stream (%s and %s)", first.Hello().Encoder, second.Hello().Encoder)
	})
}
