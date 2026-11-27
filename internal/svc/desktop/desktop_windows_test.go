package desktop

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/inject"
	"github.com/readori/RemoHop/internal/peer/peertest"
	"github.com/readori/RemoHop/internal/wintest"
)

// The Windows desktop end-to-end tests run only with REMOHOP_DESKTOP_E2E=1,
// on a machine with an interactive desktop and ffmpeg (REMOHOP_FFMPEG or PATH).
// They capture the real screen with each backend (ddagrab, gdigrab), report
// which encoder the host picks, and check what a viewer decodes against a
// window of known colour.

func winDesktopE2E(t *testing.T) string {
	t.Helper()
	if os.Getenv("REMOHOP_DESKTOP_E2E") != "1" {
		t.Skip("set REMOHOP_DESKTOP_E2E=1 to capture and control this Windows desktop")
	}
	ffmpeg, err := ffmpegPath()
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(ffmpeg, "-hide_banner", "-version").Output(); err == nil {
		t.Logf("ffmpeg: %s", strings.SplitN(string(out), "\n", 2)[0])
	}
	t.Logf("desktop: %s", wintest.Describe())
	return ffmpeg
}

// TestWinCaptureBackends runs each capture source for a few frames, says
// which one works, and which one the host picks on its own.
func TestWinCaptureBackends(t *testing.T) {
	ffmpeg := winDesktopE2E(t)
	run := func(args ...string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, ffmpeg, append([]string{"-hide_banner", "-nostdin", "-loglevel", "error"}, args...)...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	ddaErr := run("-f", "lavfi", "-i", "ddagrab=output_idx=0:framerate=15", "-frames:v", "5", "-vf", "hwdownload,format=bgra", "-f", "null", "-")
	gdiErr := run("-f", "gdigrab", "-framerate", "15", "-i", "desktop", "-frames:v", "5", "-f", "null", "-")
	report := func(name string, err error) {
		if err != nil {
			t.Logf("RESULT %s: FAILS: %v", name, err)
		} else {
			t.Logf("RESULT %s: works", name)
		}
	}
	report("ddagrab (Desktop Duplication, needs D3D11 and a real output)", ddaErr)
	report("gdigrab (GDI)", gdiErr)
	if out, err := exec.Command("powershell.exe", "-NoProfile", "-Command",
		"Get-CimInstance Win32_VideoController | Format-List Name, AdapterCompatibility, DriverVersion, VideoProcessor, CurrentHorizontalResolution, CurrentVerticalResolution").CombinedOutput(); err == nil {
		t.Logf("video controllers:\n%s", strings.TrimSpace(string(out)))
	}

	spec, err := discoverCapture(0, 30)
	if err != nil {
		t.Fatal(err)
	}
	picked := "ddagrab"
	if strings.Contains(strings.Join(spec.input, " "), "gdigrab") {
		picked = "gdigrab"
	}
	t.Logf("RESULT the host picks %s (%dx%d); warning: %q", picked, spec.width, spec.height, spec.warning)
	if (ddaErr == nil) != (picked == "ddagrab") {
		t.Errorf("host picked %s but ddagrab works=%v", picked, ddaErr == nil)
	}
	if ddaErr != nil && gdiErr != nil {
		t.Fatal("neither ddagrab nor gdigrab can capture this desktop")
	}
	sw, sh := wintest.ScreenSize()
	if spec.width != sw || spec.height != sh {
		t.Errorf("capture spec says %dx%d, the screen is %dx%d", spec.width, spec.height, sw, sh)
	}
}

// TestWinEncoders lists which H.264/AV1 encoders of the ffmpeg build work on
// this machine and which one the host selects.
func TestWinEncoders(t *testing.T) {
	ffmpeg := winDesktopE2E(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	probeMu.Lock()
	listed, err := listEncoders(ctx, ffmpeg)
	probeMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range encoders {
		if !listed[e.name] {
			continue
		}
		probeMu.Lock()
		p := probe(ctx, ffmpeg, stampOf(ffmpeg), e)
		probeMu.Unlock()
		if p.err != nil {
			t.Logf("RESULT encoder %-12s listed, does not work here: %v", e.name, p.err)
		} else {
			t.Logf("RESULT encoder %-12s works", e.name)
		}
	}
	for _, codec := range []string{codecH264, ""} {
		e, err := selectEncoder(ctx, ffmpeg, encRequest{codec: codec})
		if err != nil {
			t.Errorf("selectEncoder(%q): %v", codec, err)
			continue
		}
		t.Logf("RESULT selected for codec %q: %s (hardware=%v, codec=%s) %s", codec, e.name, e.hw, e.codec, e.warning)
	}
}

// decodeLastFrame decodes a recording with ffmpeg and returns its last
// frame as RGB.
func decodeLastFrame(t *testing.T, ffmpeg, mp4 string) (w, h int, rgb []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	out := filepath.Join(filepath.Dir(mp4), filepath.Base(mp4)+".ppm")
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

func rgbAt(rgb []byte, w, x, y int) [3]int {
	i := (y*w + x) * 3
	return [3]int{int(rgb[i]), int(rgb[i+1]), int(rgb[i+2])}
}

func closeTo(got, want [3]int) bool {
	for i := range got {
		if d := got[i] - want[i]; d > 45 || d < -45 {
			return false
		}
	}
	return true
}

// record writes n frames of the session to an MP4 and returns its path.
func record(t *testing.T, ctx context.Context, sess *Session, n int, name string) (string, []*Frame) {
	t.Helper()
	path := filepath.Join(t.TempDir(), name+".mp4")
	rec, err := NewRecorder(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := rec.SetStream(sess.Hello()); err != nil {
		t.Fatal(err)
	}
	var frames []*Frame
	for len(frames) < n {
		ev, err := sess.Next(ctx)
		if err != nil {
			t.Fatalf("next: %v", err)
		}
		if ev.Hello != nil {
			if err := rec.SetStream(*ev.Hello); err != nil {
				t.Fatal(err)
			}
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
	return path, frames
}

// TestWinDesktopE2E runs a real desktop session with each capture backend:
// capture the screen, encode, stream to a client, decode, compare with a
// window of known colour (also after the colour changes), then drive the
// host with pointer, button, key and text events into a real text box.
func TestWinDesktopE2E(t *testing.T) {
	ffmpeg := winDesktopE2E(t)
	for _, backend := range []string{"", "ddagrab", "gdigrab"} {
		name := backend
		if name == "" {
			name = "auto"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv("REMOHOP_WIN_CAPTURE", backend)
			if backend == "ddagrab" {
				if why := ddagrabProblem(ffmpeg, 0); why != "" {
					t.Skipf("ddagrab does not work on this machine: %s", why)
				}
			}
			t.Run("video", func(t *testing.T) { winVideo(t, ffmpeg) })
			t.Run("input", func(t *testing.T) { winInput(t) })
			t.Run("keyframe", func(t *testing.T) { winKeyframe(t) })
		})
	}
}

func winVideo(t *testing.T, ffmpeg string) {
	sw, sh := wintest.ScreenSize()
	red := wintest.Color(t, "FF0000")
	wintest.SetCursorPos(2, 2)
	client, _ := peertest.Pair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	sess, err := Open(ctx, client, Options{FPS: 15, Codec: codecH264})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	h := sess.Hello()
	t.Logf("RESULT hello: %dx%d %s encoder %s hardware=%v fps %d bitrate %d input=%v (%s) warning=%q",
		h.Width, h.Height, h.Codec, h.Encoder, h.Hardware, h.FPS, h.Bitrate, h.Input, h.InputError, h.Warning)
	if h.Width != sw || h.Height != sh {
		t.Fatalf("stream is %dx%d, the screen %dx%d", h.Width, h.Height, sw, sh)
	}
	if !strings.HasPrefix(h.Codec, "avc1.") || h.Encoder == "" || len(h.CodecConfig) == 0 {
		t.Fatalf("bad hello %+v", h)
	}
	if !h.Input {
		t.Errorf("input unavailable: %s", h.InputError)
	}

	path, frames := record(t, ctx, sess, 25, "red")
	if !frames[0].Key {
		t.Error("first frame is not a key frame")
	}
	for i, f := range frames[1:] {
		if f.Seq != frames[i].Seq+1 {
			t.Fatalf("sequence gap %d -> %d", frames[i].Seq, f.Seq)
		}
	}
	last := frames[len(frames)-1]
	t.Logf("RESULT capture->received latency of the last frame: %v (capture time zero: %v)", last.Received.Sub(last.Capture), last.Capture.IsZero())
	w, hh, rgb := decodeLastFrame(t, ffmpeg, path)
	if w != sw || hh != sh {
		t.Fatalf("decoded %dx%d, want %dx%d", w, hh, sw, sh)
	}
	cx, cy := red.Center()
	for _, p := range [][2]int{{cx, cy}, {sw / 4, sh / 4}, {sw - 20, sh - 20}} {
		if got := rgbAt(rgb, w, p[0], p[1]); !closeTo(got, [3]int{255, 0, 0}) {
			t.Errorf("decoded pixel %d,%d is %v, want red", p[0], p[1], got)
		}
	}

	// The picture follows the screen: a blue window on top of the red one.
	wintest.Color(t, "0000FF")
	wintest.SetCursorPos(2, 2)
	time.Sleep(500 * time.Millisecond)
	path, _ = record(t, ctx, sess, 45, "blue")
	w, _, rgb = decodeLastFrame(t, ffmpeg, path)
	if got := rgbAt(rgb, w, cx, cy); !closeTo(got, [3]int{0, 0, 255}) {
		t.Errorf("after showing a blue window the decoded pixel %d,%d is %v, want blue (the stream does not follow the screen)", cx, cy, got)
	}
}

func winInput(t *testing.T) {
	sw, sh := wintest.ScreenSize()
	w := wintest.Edit(t)
	client, _ := peertest.Pair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	sess, err := Open(ctx, client, Options{FPS: 10, Codec: codecH264})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if h := sess.Hello(); !h.Input {
		t.Fatalf("no input: %s", h.InputError)
	}
	// Keep consuming the stream (and acknowledging frames).
	go func() {
		for {
			if _, err := sess.Next(ctx); err != nil {
				return
			}
		}
	}()

	cx, cy := w.Center()
	nx, ny := float64(cx)/float64(sw-1), float64(cy)/float64(sh-1)
	if err := sess.SendInput(InputEvent{Kind: InputMove, X: nx, Y: ny}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		x, y, _ := wintest.CursorPos()
		if x == cx && y == cy {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("GetCursorPos is %d,%d, want %d,%d", x, y, cx, cy)
		}
		time.Sleep(50 * time.Millisecond)
	}
	for _, down := range []bool{true, false} {
		if err := sess.SendInput(InputEvent{Kind: InputButton, Button: 0, Down: down, X: nx, Y: ny}); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(400 * time.Millisecond)
	if err := sess.TypeText(ctx, "Hello 你好 ÄÖ"); err != nil {
		t.Fatal(err)
	}
	for _, down := range []bool{true, false} {
		if err := sess.SendInput(InputEvent{Kind: InputKey, Code: "KeyA", Down: down}); err != nil {
			t.Fatal(err)
		}
	}
	// A held key repeats: the viewer forwards the browser's repeated
	// key-downs, and Windows (where SendInput gets no typematic repeat of
	// its own) types one character for each.
	for _, down := range []bool{true, true, true, false} {
		if err := sess.SendInput(InputEvent{Kind: InputKey, Code: "KeyB", Down: down}); err != nil {
			t.Fatal(err)
		}
	}
	want := "Hello 你好 ÄÖabbb"
	if got := w.WaitText(want, 10*time.Second); got != want {
		t.Fatalf("the text box holds %q, want %q\nhelper log: %s\ndesktop: %s", got, want, w.Log(), wintest.Describe())
	}
	// A held modifier is released when the session ends.
	if err := sess.SendInput(InputEvent{Kind: InputKey, Code: "ShiftLeft", Down: true}); err != nil {
		t.Fatal(err)
	}
	sess.Close()
	time.Sleep(500 * time.Millisecond)
	inj, err := inject.Open(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer inj.Close()
	inj.KeyEvdev(30, true) // KEY_A: a stuck Shift would type "A"
	inj.KeyEvdev(30, false)
	want += "a"
	if got := w.WaitText(want, 5*time.Second); got != want {
		t.Errorf("after the session ended a key typed gave %q, want %q (Shift left held?)", got, want)
	}
}

// winKeyframe asks for key frames and measures the longest gap between
// frames around each: the replacement encoder starts while the running one
// streams (two captures side by side for a moment), so there should be none.
func winKeyframe(t *testing.T) {
	client, _ := peertest.Pair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	sess, err := Open(ctx, client, Options{FPS: 30, Codec: codecH264})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	stop := make(chan struct{})
	defer close(stop)
	go func() { // keep the screen changing so that frames keep coming
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			case <-time.After(50 * time.Millisecond):
			}
			wintest.SetCursorPos(10+i%200, 10+i%100)
		}
	}()
	start := time.Now()
	var last, requested time.Time
	var gaps []time.Duration
	var maxGap time.Duration
	var st HostStats
	nextReq := start.Add(4 * time.Second)
	for time.Since(start) < 30*time.Second && len(gaps) < 4 {
		ev, err := sess.Next(ctx)
		if err != nil {
			t.Fatalf("session ended: %v", err)
		}
		if ev.Stats != nil {
			st = *ev.Stats
		}
		f := ev.Frame
		if f == nil {
			continue
		}
		if !requested.IsZero() && !last.IsZero() {
			maxGap = max(maxGap, f.Received.Sub(last))
			if f.Key {
				gaps = append(gaps, maxGap)
				t.Logf("key frame %v after the request", f.Received.Sub(requested))
				requested = time.Time{}
				nextReq = f.Received.Add(3 * time.Second)
			}
		}
		if requested.IsZero() && f.Received.After(nextReq) {
			requested, maxGap = f.Received, 0
			sess.RequestKeyframe()
		}
		last = f.Received
	}
	h := sess.Hello()
	t.Logf("RESULT key frames on demand (%s, %s): longest gap around each %v; host %+v", h.Encoder, os.Getenv("REMOHOP_WIN_CAPTURE"), gaps, st)
	if len(gaps) < 2 {
		t.Fatalf("only %d requested key frames came", len(gaps))
	}
	bad := 0
	for _, g := range gaps {
		if g > 250*time.Millisecond {
			bad++
		}
	}
	if bad > 1 {
		t.Errorf("key frames on demand left gaps %v", gaps)
	}
}
